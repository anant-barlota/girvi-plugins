# Girvi plugins

The SDK, the contract and the official plugins for [Girvi](https://girvi.app/), the pawnbroking app.

A plugin is a separate program that Girvi starts, approves and supervises. Plugins receive loan events (a loan
was created, a payment came in, a loan was paid off) and can do something with them: send a WhatsApp message,
post to a webhook, keep an accounting system in step. Girvi's core stays small, and anyone can write the part
that fits their shop.

This repository holds:

| Path | What |
|---|---|
| `sdk/` | The Go SDK a plugin is built on (`sdk.Run`), and the durable queue it uses. |
| `sdk/proto/` | The contract, as protobuf. `core.v1` is what every plugin is; `delivery.v1` is how work reaches it. |
| `sdk/gen/` | The generated Go code (`scripts/generate.sh`). |
| `plugins/whatsapp/` | The official WhatsApp Cloud API plugin. |
| `examples/echo/` | The smallest working plugin, and the host's test fixture. |
| `.github/workflows/release-plugin.yml` | A reusable workflow that builds a plugin for every platform and publishes it. |

A small complete example outside this repository: [girvi-webhook](https://github.com/anant-barlota/girvi-webhook).

---

## How it works

```
 Girvi (the host)                                         your plugin
 ┌──────────────────────────────┐   gRPC over go-plugin   ┌─────────────────────────┐
 │ writes a delivery row in the │ ──────────────────────► │ core.v1   Plugin        │
 │ same transaction as the loan │   Initialize, Configure │ delivery.v1 Consumer    │
 │                              │ ◄────── Consume ──────► │   (one two-way stream)  │
 └──────────────────────────────┘                         └─────────────────────────┘
```

- **The host offers, the plugin takes custody.** The plugin opens a `Consume` stream and the host leases it
  items. When the plugin accepts an item, it owns the retries: the SDK keeps it in a durable SQLite queue and
  reports status back. The host does not retry on a plugin's behalf.
- **Events are part of the change that caused them.** Girvi writes the event in the same database transaction as the
  loan or payment, so a saved change and its event stay together. Publishing never decides whether a loan or payment
  is saved: if an event cannot be prepared, the change is saved anyway and the failure shows in the plugin's Activity.
- **At least once.** An item may be offered again after a crash, so `Handle` must be safe to repeat.
- **Settings, per instance or per firm.** A plugin declares its settings; Girvi draws the form, keeps the values,
  and pushes them to the plugin. Secrets are pushed to the plugin but never shown to the screen again.
- **Off until switched on.** A plugin gets nothing for a firm until a person turns it on for that firm, and events
  from before that moment are never delivered to it.

### What a plugin can listen for

| Capability | When |
|---|---|
| `events.loan.created` | A new loan (Form F) was recorded. |
| `events.payment.received` | A payment was recorded that leaves principal owing (Form H). |
| `events.loan.redeemed` | A payment paid the loan off. |
| `notify` | A message Girvi composed for the customer, with a template key (`pledge_disbursed`, `payment_received`, `loan_closed`) and variables. Only sent when the customer has a usable Indian mobile number; otherwise it is recorded as skipped. |

Events carry **view models** (`model.v1`): money in whole paise, weight in whole milligrams, dates in ISO 8601, each
with a display string Girvi formats, so a plugin never formats or computes money itself. ID document numbers are
never included.

---

## Write a plugin

```bash
mkdir my-plugin && cd my-plugin
go mod init example.com/my-plugin
go get github.com/anant-barlota/girvi-plugins@latest
```

A plugin implements four methods and calls `sdk.Run`:

```go
package main

import (
	"context"

	"github.com/anant-barlota/girvi-plugins/sdk"
	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

type myPlugin struct{}

// Info is what the plugin announces about itself. The id is owner/name; use your GitHub account as the owner.
func (myPlugin) Info() sdk.Info {
	return sdk.Info{
		ID:          "your-github-name/loan-logger",
		Name:        "Loan logger",
		Author:      "You",
		Description: "Does something when a loan is created.",
		Version:     "0.1.0", // must equal the tag you release
		Capabilities: []*corev1.Capability{{Name: "events.loan.created", Version: 1}},
		Requires:     []string{"storage.private"}, // "network" if it uses the internet
		Settings: []*corev1.SettingField{
			{Key: "token", Title: "Token", Type: "string", Secret: true, Scope: corev1.SettingScope_SETTING_SCOPE_FIRM},
		},
	}
}

// Configure receives the settings, on start and on every change. Keep them in memory.
func (myPlugin) Configure(ctx context.Context, cfg sdk.Config) error { return nil }

// Health is shown in the plugins screen. Never put customer data in the message.
func (myPlugin) Health(ctx context.Context) sdk.Health {
	return sdk.Health{Status: corev1.HealthStatus_HEALTH_STATUS_OK, Message: "ready"}
}

// Handle does the work for one item. Return nil when it is done.
//   - sdk.Permanent(...) when retrying cannot help (an invalid recipient).
//   - sdk.Blocked(...) when it waits on a fix (rejected credentials); the item is kept.
//   - any other error is retried with backoff until the item expires.
func (myPlugin) Handle(ctx context.Context, item *deliveryv1.Item) (sdk.Result, error) {
	loan := item.GetLoanCreated().GetLoan()
	_ = loan // do something with loan.GetNumber(), loan.GetPrincipal().GetDisplay(), ...
	return sdk.Result{}, nil
}

func main() { sdk.Run(myPlugin{}) }
```

`Result` can carry a `ProviderRef` (your provider's id for what you sent), and `Unconfirmed: true` when the
provider accepted the item but cannot say whether it arrived. Girvi then shows it as **Sent**, never as
**Delivered**, so nobody is told something arrived when nobody knows.

`examples/echo` is a fuller working plugin; `plugins/whatsapp` is a real one.

### The plugin contract version

The handshake has a protocol version (currently **3**). A plugin built on an older SDK refuses to start with a clear
message, and needs rebuilding. The contract is still alpha (`v0.1.0-alpha.N`): expect breaking changes until it
settles.

### Settings fields

`string`, `number`, `boolean` or `select` (with `options`); `secret: true` for credentials; `scope` is the whole
installation (`INSTANCE`) or per firm (`FIRM`); `required`; `advanced` folds a field away on the screen. Girvi
applies a field's `default_value` when nothing has been set.

---

## Release it

Build for every platform with the reusable workflow. In your plugin's repository:

```yaml
# .github/workflows/release.yml
name: Release
on:
  push:
    tags: ['v*']
permissions:
  contents: read
jobs:
  release:
    permissions:
      contents: write
    uses: anant-barlota/girvi-plugins/.github/workflows/release-plugin.yml@<a commit SHA of this repository>
    with:
      name: loan-logger
```

Pushing a tag such as `v0.1.0` builds Windows, macOS (Intel and Apple silicon) and Linux programs with no cgo and
attaches them, with a `SHA256SUMS` file, to a GitHub Release. The tag must equal the `Version` your plugin reports.
Pin the workflow to a commit, not a tag.

Then list it in the [plugin catalog](https://github.com/anant-barlota/girvi-registry) so people can install it from
inside Girvi (**Plugins, Browse**). The catalog's README explains how.

You can also give someone the program directly. They put it in Girvi's plugins folder (named `girvi-plugin-<name>`,
with `.exe` on Windows) and choose **Scan folder**.

---

## What Girvi does about a plugin you did not write

A plugin is somebody else's program running on a shop's computer, so Girvi treats it with suspicion. Be clear about
what is and is not protected today.

**Done:**
- **Nothing runs until a person approves it.** Approval is for the exact bytes (a SHA-256). Girvi re-hashes the file
  before every launch, and a changed file goes back to waiting for approval. Two files cannot claim the same plugin id.
- **A scrubbed environment.** A plugin is started with an explicit allowlist (PATH, temporary directories, the
  Windows system root, locale, proxy and certificate settings, its own data directory). The host's other
  environment variables are not passed on.
- **Its own data directory** (`GIRVI_PLUGIN_DATA_DIR`) for databases, caches and logs.
- **Declared permissions.** What a plugin asks for (events it receives, `network`, `storage.private`) is shown on its
  card before and after approval.
- **Downloads are checked.** From a catalog, Girvi verifies the SHA-256 and size, accepts only https, and refuses
  a redirect that leaves https or reaches back to the computer.

**Not done yet, so do not rely on it:**
- **No process isolation on Windows or macOS.** A plugin runs with the user's own permissions and can read the same
  files the user can, including Girvi's databases. On Linux, if Bubblewrap (`bwrap`) is installed Girvi uses it, but
  the sandbox still shares the temporary directory and allows all network access, and without `bwrap` the plugin
  runs unconfined. Approve only plugins you trust and whose source you can read.
- The official catalog is not signed yet; downloads are still verified by checksum.

---

## License

MIT. See [LICENSE](LICENSE).
