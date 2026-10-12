package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/anant-barlota/girvi-plugins/sdk"
	pluginv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/plugin/v1"
)

// SampleNotifier demonstrates a full Girvi notification extension.
type SampleNotifier struct {
	pluginv1.UnimplementedNotificationPluginServer
	dataDir string
}

// GetMetadata declares identification, author, and dynamic configuration schema.
func (s *SampleNotifier) GetMetadata(ctx context.Context, req *pluginv1.GetMetadataRequest) (*pluginv1.PluginMetadata, error) {
	return &pluginv1.PluginMetadata{
		Id:             "girvi-plugin-sample",
		Name:           "Sample Notifier Gateway",
		Version:        "1.0.0",
		Author:         "Community Developer",
		Channel:        "webhook",
		RequiredConfig: []string{"webhook_url"},
		ConfigFields: []*pluginv1.ConfigField{
			{
				Key:          "webhook_url",
				Title:        "Webhook Endpoint URL",
				Description:  "The HTTPS destination URL to receive event payloads",
				Type:         "string",
				IsSecret:     false,
				DefaultValue: "https://httpbin.org/post",
			},
			{
				Key:          "auth_token",
				Title:        "Authorization Bearer Token",
				Description:  "Secret token sent in the Authorization header",
				Type:         "string",
				IsSecret:     true,
				DefaultValue: "",
			},
			{
				Key:          "notify_on_pledge_disbursed",
				Title:        "Notify on Form F Disbursal",
				Description:  "Send a message when a new loan is created",
				Type:         "boolean",
				DefaultValue: "true",
			},
		},
	}, nil
}

// HealthCheck confirms connectivity to the provider.
func (s *SampleNotifier) HealthCheck(ctx context.Context, req *pluginv1.HealthCheckRequest) (*pluginv1.HealthCheckResponse, error) {
	return &pluginv1.HealthCheckResponse{
		IsHealthy:     true,
		StatusMessage: "Sample notifier operational",
	}, nil
}

// SendMessage handles dispatched events.
func (s *SampleNotifier) SendMessage(ctx context.Context, req *pluginv1.SendMessageRequest) (*pluginv1.SendMessageResponse, error) {
	webhookURL := req.Config["webhook_url"]
	log.Printf("[SampleNotifier] Dispatching event=%s to phone=%s via %s",
		req.TemplateKey, req.RecipientPhone, webhookURL)

	// Filter or ignore events this plugin isn't interested in:
	switch req.TemplateKey {
	case "pledge_disbursed", "payment_received", "loan_closed":
		// Dispatched notification logic here (HTTP POST, external API call, etc.)
		msgID := fmt.Sprintf("sample-%d", os.Getpid())
		return &pluginv1.SendMessageResponse{
			Success:   true,
			MessageId: msgID,
		}, nil
	default:
		// Return graceful no-op for unhandled event keys
		return &pluginv1.SendMessageResponse{
			Success:      true,
			ErrorMessage: "ignored: event key not handled by this extension",
		}, nil
	}
}

func main() {
	// Girvi injects a dedicated, sandboxed data directory via GIRVI_PLUGIN_DATA_DIR
	dataDir := os.Getenv("GIRVI_PLUGIN_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(os.TempDir(), "girvi-plugin-sample")
	}
	_ = os.MkdirAll(dataDir, 0755)

	// Serve the plugin over gRPC using the standard Girvi handshake
	sdk.Serve(&SampleNotifier{dataDir: dataDir})
}
