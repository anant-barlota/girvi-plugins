# Webhook

Posts loan events to a URL each firm chooses, as signed JSON: when a loan is
created, a payment is received, or a loan is redeemed. A building block for a
Google Sheet, Zapier, Make, or your own service.

    go test ./...                       # the plugin's logic, natively
    girvi plugin build                  # dist/webhook-<version>.zip
    girvi plugin test --set url=https://example.com/hook --respond 503

## What it sends

The event exactly as Girvi gives it (see `girvi contract`), plus the
firm, with these headers:

| Header | Value |
|---|---|
| `X-Girvi-Event` | `loan.created`, `payment.received` or `loan.redeemed` |
| `X-Girvi-Delivery` | The delivery id; the same on every retry, so a receiver can drop duplicates |
| `X-Girvi-Timestamp` | Unix seconds when it was sent |
| `X-Girvi-Signature` | `v1=` + hex HMAC-SHA256 of `timestamp.body`, keyed with the signing secret, when one is set |

Receivers should check the signature and reject old timestamps.

## What each answer means

| Receiver says | Outcome |
|---|---|
| 2xx | Done |
| 3xx, 401, 403, 404, 410 | Blocked until the shop fixes the URL or credentials |
| 408, 425, 429, 5xx, or no answer | Retried in 5 minutes |
| Any other 4xx | Failed; the user can send it again |
