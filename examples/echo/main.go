// Command echo is the smallest plugin on the v0 contract. It takes custody of
// every notify message and event, appends a line to echo.log in its data
// directory, and reports the line number as the provider reference. Host tests
// use it as a fixture.
//
// A message whose "fail" variable is "permanent" is rejected, and one whose
// "fail" variable is "transient" fails on its first attempt, so both paths can
// be exercised.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/anant-barlota/girvi-plugins/sdk"
	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

type echo struct {
	mu      sync.Mutex
	dataDir string
	lines   int
}

func (e *echo) Info() sdk.Info {
	return sdk.Info{
		ID:          "girvi/echo",
		Name:        "Echo",
		Author:      "Girvi",
		Description: "Writes every delivery to a log file. Used to test the host.",
		Version:     "0.1.0",
		Capabilities: []*corev1.Capability{
			{Name: "notify", Version: 1},
			{Name: "events.loan.created", Version: 1},
			{Name: "events.payment.received", Version: 1},
			{Name: "events.loan.redeemed", Version: 1},
		},
		Requires: []string{"storage.private"},
		Settings: []*corev1.SettingField{{
			Key:   "label",
			Title: "Label",
			Type:  "string",
			Scope: corev1.SettingScope_SETTING_SCOPE_FIRM,
		}},
	}
}

func (e *echo) Configure(context.Context, sdk.Config) error { return nil }

func (e *echo) Health(context.Context) sdk.Health {
	return sdk.Health{Status: corev1.HealthStatus_HEALTH_STATUS_OK, Message: "echo is running"}
}

func (e *echo) Handle(_ context.Context, item *deliveryv1.Item) (sdk.Result, error) {
	if n := item.GetNotify(); n != nil {
		switch n.GetVars()["fail"] {
		case "permanent":
			return sdk.Result{}, sdk.Permanent(deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT, "echo rejects this message")
		case "transient":
			if item.GetAttempt() == 1 {
				return sdk.Result{}, fmt.Errorf("echo is pretending to be down")
			}
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	f, err := os.OpenFile(filepath.Join(e.dataDir, "echo.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return sdk.Result{}, err
	}
	defer f.Close()
	e.lines++
	if _, err := fmt.Fprintf(f, "%s %s firm=%s attempt=%d\n", item.GetDeliveryId(), item.GetCapability(), item.GetFirmId(), item.GetAttempt()); err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{ProviderRef: fmt.Sprintf("echo-%d", e.lines)}, nil
}

func main() {
	dir := os.Getenv("GIRVI_PLUGIN_DATA_DIR")
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "girvi-echo")
	}
	_ = os.MkdirAll(dir, 0o700)
	sdk.Run(&echo{dataDir: dir})
}
