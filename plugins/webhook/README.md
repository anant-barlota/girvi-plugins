# Webhook (`girvi/webhook`)

The official [Girvi](https://girvi.app/) plugin that posts loan events to a URL each firm chooses, as signed JSON:

- `loan.created`: a loan was recorded (Form F)
- `payment.received`: a payment was recorded (Form H), including the one that closes the loan
- `loan.redeemed`: a loan was paid off

It is a building block. Google Sheets, Zapier, Make, Slack alerts and accounting bridges can all start from a
webhook. Nothing is posted until the plugin is turned on for a firm.

A sandboxed WebAssembly plugin that can reach only the URL each firm enters. It is also the SDK's complete
example: the logic is in [`handler.go`](handler.go), tested natively in [`handler_test.go`](handler_test.go).

## Install

Download `webhook-<version>.zip` from a `webhook/v…` [release](https://github.com/anant-barlota/girvi-sdk/releases)
and install it from Girvi's **Plugins** screen. Approve it, enter the settings, and turn it on for a firm.

## Settings

| Setting | What it does |
|---|---|
| Webhook URL | Where to post. Use `https` unless it is on your own network. Plain `http` to the internet is refused unless allowed under Advanced. |
| Signing secret | Optional. If set, every post is signed (see below). |
| Include customer names and phone numbers | Turn off to send only customer ids. |
| Authorization header | Advanced. Sent as it is, for example `Bearer abc123`. |
| Allow plain http outside my network | Advanced. Off by default. |

## What is posted

A `POST` with the event as Girvi gives it (see the [contract](../../plugin/contract/CONTRACT.md#events)),
plus the firm, and these headers:

| Header | Value |
|---|---|
| `X-Girvi-Event` | `loan.created`, `payment.received` or `loan.redeemed` |
| `X-Girvi-Delivery` | The delivery id: the same on every retry, so a receiver can drop duplicates |
| `X-Girvi-Timestamp` | Unix seconds when it was sent |
| `X-Girvi-Signature` | `v1=` and a hex HMAC-SHA256 of `timestamp.body`, keyed with the signing secret, when one is set |

Receivers should check the signature and refuse old timestamps.

## What each answer means

| Receiver says | Outcome |
|---|---|
| 2xx | Done |
| 3xx, 401, 403, 404, 410 | Held until the shop fixes the URL or credentials |
| 408, 425, 429, 5xx, or no answer | Retried in 5 minutes |
| Any other 4xx | Failed; it can be sent again from Girvi |

## Try it

Run the example receiver, which checks the signature the way a real one should, and point Girvi at it:

```sh
go run ./examples/receiver -addr :9090 -secret s3cret
# Webhook URL:    http://127.0.0.1:9090/
# Signing secret: s3cret
```

Then press **Send test** on the plugin in Girvi.

## Develop and release

```sh
go test ./...            # the handler and the receiver, natively
girvi plugin build       # dist/webhook-<version>.zip
girvi plugin test --set url=https://example.com/hook --respond 503
```

To release, set `version` in `plugin.json` and push the tag `webhook/v<version>` from `main`.

## License

MIT. See [LICENSE](../../LICENSE).
