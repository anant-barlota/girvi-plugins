# Girvi Plugins SDK & Reference

Welcome to **`girvi-plugins`**, the official developer SDK, Protobuf contract specifications, and extension directory for the **Girvi Pawn Broker** desktop application.

Girvi extensions allow you to integrate messaging gateways (WhatsApp, SMS, Telegram), biometric fingerprint scanners, thermal receipt printers, and external accounting software (Tally, Busy, QuickBooks) without modifying Girvi's core codebase.

---

## Architecture Overview

```
┌────────────────────────────────┐         gRPC IPC          ┌──────────────────────────────────┐
│         Girvi Desktop          │ ◄──────────────────────►  │        Extension Binary          │
│   (Core Ledger & Database)     │   (Unix Socket / Pipes)   │  (girvi-plugin-whatsapp, etc.)   │
└────────────────────────────────┘                           └──────────────────────────────────┘
                 │                                                             │
        [100% Shielded]                                                [Sandboxed Root]
                 ▼                                                             ▼
   ~/.config/girvi/tenants/*.db                              ~/.config/girvi/plugin_data/<id>/
```

- **Process Isolation:** Extensions run as independent subprocesses communicating over standard input/output using HashiCorp `go-plugin` and gRPC.
- **Process Sandboxing:** On Linux, extensions are wrapped in unprivileged Bubblewrap (`bwrap`) mount namespaces. The core pawnbroking database is **invisible and inaccessible** to extension processes.
- **Dedicated Storage:** The host injects `GIRVI_PLUGIN_DATA_DIR=~/.config/girvi/plugin_data/<id>/`. Extensions can freely create and manage private SQLite databases, caches, or logs in this directory.
- **Concurrent Fan-Out:** The host broadcasts events to all active extensions concurrently in parallel goroutines, with a fail-safe **5-second deadline context** per extension.

---

## Repository Structure

```
girvi-plugins/
├── sdk/
│   ├── proto/
│   │   └── girvi/plugin/v1/notification.proto   # Canonical Protobuf contract definition
│   ├── gen/
│   │   └── girvi/plugin/v1/                     # Pre-generated Go stubs (pb & gRPC)
│   └── handshake.go                             # Standard handshake & sdk.Serve helper
├── examples/
│   └── sample-notifier/                         # Compilable reference implementation
├── go.mod
└── README.md
```

---

## Quickstart: Build a Plugin in 5 Minutes

### 1. Add the Dependency

```bash
mkdir my-girvi-plugin && cd my-girvi-plugin
go mod init my-girvi-plugin
go get github.com/anant-barlota/girvi-plugins@latest
```

### 2. Implement the Service

Create `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/anant-barlota/girvi-plugins/sdk"
	pluginv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/plugin/v1"
)

type MyPlugin struct {
	pluginv1.UnimplementedNotificationPluginServer
}

// 1. Declare plugin metadata and dynamic settings fields
func (p *MyPlugin) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.PluginMetadata, error) {
	return &pluginv1.PluginMetadata{
		Id:             "girvi-plugin-fast2sms",
		Name:           "Fast2SMS Gateway",
		Version:        "1.0.0",
		Author:         "My Company",
		Channel:        "sms",
		RequiredConfig: []string{"api_key"},
		ConfigFields: []*pluginv1.ConfigField{
			{
				Key:         "api_key",
				Title:       "Fast2SMS API Key",
				Description: "Authorization token from your Fast2SMS developer dashboard",
				Type:        "string",
				IsSecret:    true,
			},
			{
				Key:          "sender_id",
				Title:        "Sender ID / Header",
				Description:  "6-character DLT registered Sender Header",
				Type:         "string",
				DefaultValue: "GRVPWN",
			},
		},
	}, nil
}

// 2. Health check endpoint called by Girvi Desktop
func (p *MyPlugin) HealthCheck(ctx context.Context, req *pluginv1.HealthCheckRequest) (*pluginv1.HealthCheckResponse, error) {
	return &pluginv1.HealthCheckResponse{
		IsHealthy:     true,
		StatusMessage: "Fast2SMS credentials verified",
	}, nil
}

// 3. Receive and dispatch notifications
func (p *MyPlugin) SendMessage(ctx context.Context, req *pluginv1.SendMessageRequest) (*pluginv1.SendMessageResponse, error) {
	apiKey := req.Config["api_key"]
	log.Printf("Sending %s notification to %s with API Key %s...", req.TemplateKey, req.RecipientPhone, apiKey)

	switch req.TemplateKey {
	case "pledge_disbursed", "payment_received", "loan_closed":
		// TODO: Call your SMS or WhatsApp provider HTTP API
		return &pluginv1.SendMessageResponse{
			Success:   true,
			MessageId: "msg_12345",
		}, nil
	default:
		// Gracefully acknowledge unhandled events without error
		return &pluginv1.SendMessageResponse{
			Success:      true,
			ErrorMessage: "ignored: event not handled by this extension",
		}, nil
	}
}

func main() {
	// Handshake and gRPC server startup in one line:
	sdk.Serve(&MyPlugin{})
}
```

### 3. Compile and Install

Compile your executable with the prefix `girvi-plugin-*` directly into Girvi's plugin folder:

```bash
# Build for your operating system:
go build -o ~/.config/girvi/plugins/girvi-plugin-fast2sms .

# In Girvi Desktop, navigate to Extensions -> click "Scan Directory".
```

---

## Event Catalog

When a pawnbroking transaction occurs, the host triggers `SendMessage` with structured variables in `req.TemplateVars`:

| Template Key | Trigger | Available Variables in `req.TemplateVars` |
| :--- | :--- | :--- |
| `pledge_disbursed` | New Form F loan created | `customer_name`, `loan_number`, `amount`, `weight_grams`, `interest_rate`, `due_date` |
| `payment_received` | Partial/interest payment logged | `customer_name`, `loan_number`, `amount_paid`, `balance_due` |
| `loan_closed` | Form H final loan redemption | `customer_name`, `loan_number`, `weight_grams` |
| `due_reminder` | Scheduled reminder sweep | `customer_name`, `loan_number`, `balance_due`, `due_date` |

---

## Schema-Driven Dynamic Configuration

You do not need to build your own configuration UI. When your plugin returns `ConfigFields` in `GetMetadata`:

- Girvi Desktop automatically generates a settings modal with inputs, password reveal toggles, and dropdowns.
- Stored values are encrypted in `~/.config/girvi/plugin_configs.json` and passed in `req.Config[key]` during every `SendMessage` call.

### Supported Field Types
- `"string"`: Standard single-line text input.
- `"boolean"`: Toggle switch.
- `"number"`: Numeric input.
- `"select"`: Dropdown selection (provide options in `ConfigField.Options`).
- `is_secret = true`: Masks the value with dots (`••••••••`) in the UI.

---

## Event Opt-Out & Filtering

Pawnbrokers or extension authors can opt out of specific events:
- **Host-side toggle:** Setting `notify_on_<template_key> = "false"` in plugin configuration skips IPC dispatch completely.
- **Plugin-side no-op:** If an extension receives an event it does not handle, returning `Success: true` with an explanatory message prevents error alerts or retry loops.

---

## Security & Sandbox Verification

1. **Quarantine by Default:** New or modified binaries detected on disk are quarantined based on SHA-256 checksum until the pawnbroker clicks **"Trust & Enable"**.
2. **Filesystem Sandboxing:** 
   - Linux: Bubblewrap (`bwrap`) limits visibility strictly to system libraries and the plugin's dedicated data directory.
   - Core loan ledgers, customer records, and financial databases cannot be read or modified by extension processes.
3. **No Sudo/Root:** Extensions never require elevated system privileges.

---

## License & Community

Girvi Plugins SDK is distributed under the MIT License. Contributions, community plugins, and feedback are welcome!
