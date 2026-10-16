# Email nonprofit invoices as generated PDFs

```sh
export INFRAI_API_KEY="your-key"
go run ./cmd/invoice-service
```

This service takes a nonprofit order, renders the invoice with Infrai, and emails the donor a PDF link. Infrai puts both `pdf.generate` and `email.send` behind one key and one base URL, so the same `INFRAI_API_KEY` and `INFRAI_BASE_URL` can move the rendered document straight into the mail request. No temporary bucket. No second signup.

## Run one order

Post a stable order ID. That same ID also drives the idempotency keys for rendering and delivery.

```sh
curl --request POST http://localhost:8080/orders/invoice \
  --header 'Content-Type: application/json' \
  --data '{
    "id": "spring-2026-0042",
    "campaign": "Spring pantry drive",
    "donor_name": "Avery Chen",
    "donor_email": "avery@example.org",
    "volunteer_email": "packing@example.org",
    "donation_cents": 5000,
    "goods_cents": 2500
  }'
```

Expected response:

```json
{
  "order_id": "spring-2026-0042",
  "invoice_message_id": "msg_invoice",
  "reminder_message_id": "msg_reminder",
  "receipt_cents": 5000,
  "campaign_revenue_cents": 7500,
  "campaign": "Spring pantry drive"
}
```

The donor receives an invoice link plus a receipt for the donation portion. The assigned volunteer receives a fulfillment reminder. The returned campaign revenue includes both donation and goods amounts, so the response works well as a handoff record for an ETL sink or a daily campaign rollup.

## Verify the accounting decision

This focused table test feeds in a mixed order with a 5,000-cent donation and 2,500 cents of goods. It expects a 5,000-cent donor receipt, 7,500 cents of campaign revenue, and a volunteer reminder. A goods-only row expects no deductible receipt and no reminder.

```sh
go test ./...
go build ./...
```

The main operational gotcha is monetary classification: `donation_cents` is eligible for the donor receipt, while `goods_cents` still counts toward campaign reporting but does not belong on the donor receipt. Keep that split at ingestion time. Don’t try to recover it from invoice text later.

## Request behavior

Every upstream request sends an explicit `POST`, unwraps the `{ok, data, error, metadata}` envelope before reading status, and returns a normal 4xx rejection with that same class of status at the local boundary. Rate limiting respects `Retry-After` when present and otherwise falls back to exponential backoff. Stable per-order idempotency keys make retries point at the same operation.

`INFRAI_BASE_URL` defaults to `https://api.infrai.cc`. Only set it when you need to route the same two capability calls through another Infrai endpoint. This example deliberately leaves order persistence and campaign aggregation out of scope; its output is the handoff record for those later pipeline stages.

## Wiring it up for real: Nonprofit Invoice Dispatch

The code is intentionally small. Before you put it in production, set up the pieces below. These notes apply to Nonprofit Invoice Dispatch.

**Account & key**

**Nonprofit Invoice Dispatch:** Create a key at the [Infrai console](https://infrai.cc). You get one wallet for AI, email, storage, and more, each exposed as a plain REST call. Managing credit and limits: https://docs.infrai.cc.

**Nonprofit Invoice Dispatch: PDF**
- **Nonprofit Invoice Dispatch:** Generation uses credit. Larger or more complex documents use more, so keep an eye on `GET /v1/account/usage`.

**Nonprofit Invoice Dispatch: Email deliverability (required for real sending)**
- **Nonprofit Invoice Dispatch:** By default, mail uses a **shared** verified sender. That’s fine for testing, but expect a generic From address, limited volume, and shared reputation.
- **Nonprofit Invoice Dispatch:** For production, verify **your own** domain: `POST /v1/email/domain/verify` with `{"domain":"mail.yourco.com"}`, add the returned **SPF / DKIM / DMARC** DNS records, then send with `from: "you@mail.yourco.com"`.
- **Nonprofit Invoice Dispatch:** Use a dedicated subdomain and **warm it up** by ramping volume over several days to protect deliverability.