package invoice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Order struct {
	ID             string `json:"id"`
	Campaign       string `json:"campaign"`
	DonorName      string `json:"donor_name"`
	DonorEmail     string `json:"donor_email"`
	VolunteerEmail string `json:"volunteer_email,omitempty"`
	DonationCents  int64  `json:"donation_cents"`
	GoodsCents     int64  `json:"goods_cents"`
}

type DispatchPlan struct {
	ReceiptCents    int64  `json:"receipt_cents"`
	CampaignRevenue int64  `json:"campaign_revenue_cents"`
	SendReminder    bool   `json:"send_volunteer_reminder"`
	ReceiptLabel    string `json:"receipt_label"`
}

func Plan(order Order) DispatchPlan {
	label := "purchase acknowledgement"
	if order.DonationCents > 0 {
		label = "donor receipt"
	}
	return DispatchPlan{
		ReceiptCents:    order.DonationCents,
		CampaignRevenue: order.DonationCents + order.GoodsCents,
		SendReminder:    order.VolunteerEmail != "",
		ReceiptLabel:    label,
	}
}

type APIError struct {
	Code       string
	Message    string
	HTTPStatus int
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return e.Code + ": " + e.Message
	}
	return e.Code
}

type Client struct {
	BaseURL string
	Key     string
	HTTP    *http.Client
	Sleep   func(time.Duration)
}

type apiEnvelope struct {
	OK       bool            `json:"ok"`
	Data     json.RawMessage `json:"data"`
	Error    *apiError       `json:"error"`
	Metadata json.RawMessage `json:"metadata"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *Client) post(ctx context.Context, path string, body any, idempotencyKey string, output any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode request: %w", err)
	}
	httpClient := c.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	sleep := c.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+path, bytes.NewReader(payload))
		if err != nil {
			return fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.Key)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idempotencyKey)

		res, err := httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("send request: %w", err)
		}
		raw, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read response: %w", readErr)
		}

		var envelope apiEnvelope
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("decode response envelope: %w", err)
		}
		if !envelope.OK {
			if res.StatusCode == http.StatusTooManyRequests && attempt < 3 {
				sleep(retryDelay(res.Header.Get("Retry-After"), attempt))
				continue
			}
			if envelope.Error == nil {
				return &APIError{Code: "request_rejected", HTTPStatus: res.StatusCode}
			}
			return &APIError{Code: envelope.Error.Code, Message: envelope.Error.Message, HTTPStatus: res.StatusCode}
		}
		if res.StatusCode >= http.StatusInternalServerError {
			return fmt.Errorf("upstream transport status %d", res.StatusCode)
		}
		if output != nil {
			if err := json.Unmarshal(envelope.Data, output); err != nil {
				return fmt.Errorf("decode response data: %w", err)
			}
		}
		return nil
	}
	return errors.New("retry budget exhausted")
}

func retryDelay(header string, attempt int) time.Duration {
	if seconds, err := strconv.Atoi(header); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

type pdfResult struct {
	URL string `json:"url"`
}

type emailResult struct {
	MessageID string `json:"message_id"`
}

type DispatchResult struct {
	OrderID           string `json:"order_id"`
	InvoiceMessageID  string `json:"invoice_message_id"`
	ReminderMessageID string `json:"reminder_message_id,omitempty"`
	ReceiptCents      int64  `json:"receipt_cents"`
	CampaignRevenue   int64  `json:"campaign_revenue_cents"`
	Campaign          string `json:"campaign"`
}

func (c *Client) Dispatch(ctx context.Context, order Order) (DispatchResult, error) {
	plan := Plan(order)
	document, err := invoiceHTML(order, plan)
	if err != nil {
		return DispatchResult{}, err
	}

	var pdf pdfResult
	if err := c.post(ctx, "/v1/pdf/generate", map[string]any{
		"html":        document,
		"page_size":   "A4",
		"orientation": "portrait",
		"store":       false,
	}, "order-"+order.ID+"-pdf", &pdf); err != nil {
		return DispatchResult{}, err
	}

	var invoice emailResult
	mail := `<p>Your invoice and ` + template.HTMLEscapeString(plan.ReceiptLabel) + ` are ready.</p><p><a href="` + template.HTMLEscapeString(pdf.URL) + `">Download invoice PDF</a></p>`
	if err := c.post(ctx, "/v1/email/send", map[string]any{
		"to":      order.DonorEmail,
		"subject": "Invoice " + order.ID + " from " + order.Campaign,
		"html":    mail,
	}, "order-"+order.ID+"-invoice-email", &invoice); err != nil {
		return DispatchResult{}, err
	}

	result := DispatchResult{OrderID: order.ID, InvoiceMessageID: invoice.MessageID, ReceiptCents: plan.ReceiptCents, CampaignRevenue: plan.CampaignRevenue, Campaign: order.Campaign}
	if plan.SendReminder {
		var reminder emailResult
		if err := c.post(ctx, "/v1/email/send", map[string]any{
			"to":      order.VolunteerEmail,
			"subject": "Fulfillment reminder for order " + order.ID,
			"body":    "Prepare the nonprofit order for " + order.DonorName + ".",
		}, "order-"+order.ID+"-volunteer-email", &reminder); err != nil {
			return DispatchResult{}, err
		}
		result.ReminderMessageID = reminder.MessageID
	}
	return result, nil
}

var invoiceTemplate = template.Must(template.New("invoice").Funcs(template.FuncMap{"money": formatMoney}).Parse(`<!doctype html>
<html><body>
<h1>Invoice {{.Order.ID}}</h1>
<p>Campaign: {{.Order.Campaign}}</p>
<p>Donor: {{.Order.DonorName}}</p>
<table><tr><th>Donation</th><td>{{money .Order.DonationCents}}</td></tr>
<tr><th>Goods</th><td>{{money .Order.GoodsCents}}</td></tr>
<tr><th>Total</th><td>{{money .Plan.CampaignRevenue}}</td></tr></table>
<p>{{.Plan.ReceiptLabel}} amount: {{money .Plan.ReceiptCents}}</p>
</body></html>`))

func invoiceHTML(order Order, plan DispatchPlan) (string, error) {
	tmpl, err := invoiceTemplate.Clone()
	if err != nil {
		return "", err
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, struct {
		Order Order
		Plan  DispatchPlan
	}{order, plan}); err != nil {
		return "", fmt.Errorf("render invoice: %w", err)
	}
	return out.String(), nil
}

func formatMoney(cents int64) string {
	return fmt.Sprintf("$%d.%02d", cents/100, cents%100)
}
