package invoice

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestPlanSeparatesReceiptsFromCampaignRevenue(t *testing.T) {
	tests := []struct {
		name  string
		order Order
		want  DispatchPlan
	}{
		{
			name:  "mixed donation and goods with volunteer",
			order: Order{DonationCents: 5000, GoodsCents: 2500, VolunteerEmail: "shift@example.org"},
			want:  DispatchPlan{ReceiptCents: 5000, CampaignRevenue: 7500, SendReminder: true, ReceiptLabel: "donor receipt"},
		},
		{
			name:  "goods only has no deductible receipt",
			order: Order{GoodsCents: 2500},
			want:  DispatchPlan{ReceiptCents: 0, CampaignRevenue: 2500, SendReminder: false, ReceiptLabel: "purchase acknowledgement"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plan(tc.order); got != tc.want {
				t.Fatalf("Plan() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestDispatchUsesRenderOutputInEmail(t *testing.T) {
	var calls []struct {
		path string
		body map[string]any
		key  string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, struct {
			path string
			body map[string]any
			key  string
		}{r.URL.Path, body, r.Header.Get("Idempotency-Key")})
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/pdf/generate" {
			json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]string{"url": "data:application/pdf;base64,JVBERi0="}, "metadata": map[string]any{}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]string{"message_id": "msg_123"}, "metadata": map[string]any{}})
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Key: "test-key", HTTP: server.Client()}
	order := Order{ID: "order-42", Campaign: "Pantry", DonorName: "Avery", DonorEmail: "donor@example.org", DonationCents: 5000, GoodsCents: 2500}
	result, err := client.Dispatch(context.Background(), order)
	if err != nil {
		t.Fatal(err)
	}
	if result.InvoiceMessageID != "msg_123" || result.CampaignRevenue != 7500 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if got := calls[0].path; got != "/v1/pdf/generate" {
		t.Fatalf("render path = %q", got)
	}
	if got := calls[1].path; got != "/v1/email/send" {
		t.Fatalf("email path = %q", got)
	}
	wantPDFFields := []string{"html", "orientation", "page_size", "store"}
	gotPDFFields := make([]string, 0, len(calls[0].body))
	for _, field := range wantPDFFields {
		if _, ok := calls[0].body[field]; ok {
			gotPDFFields = append(gotPDFFields, field)
		}
	}
	if !reflect.DeepEqual(gotPDFFields, wantPDFFields) || len(calls[0].body) != len(wantPDFFields) {
		t.Fatalf("PDF fields = %v", calls[0].body)
	}
	if html, _ := calls[1].body["html"].(string); html == "" || !strings.Contains(html, "data:application/pdf") {
		t.Fatalf("email does not contain rendered PDF URL: %q", html)
	}
	if calls[0].key != "order-order-42-pdf" || calls[1].key != "order-order-42-invoice-email" {
		t.Fatalf("unexpected idempotency keys: %q, %q", calls[0].key, calls[1].key)
	}
}
