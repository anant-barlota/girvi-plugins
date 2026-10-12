package sdk

import (
	"context"

	pluginv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/plugin/v1"
	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"
)

// HandshakeConfig defines the magic cookie required by Girvi to discover and load plugins.
var HandshakeConfig = plugin.HandshakeConfig{
	ProtocolVersion:  1,
	MagicCookieKey:   "GIRVI_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "girvi-app-secure-handshake-v1",
}

// NotificationGRPCPlugin implements the HashiCorp go-plugin GRPCPlugin interface.
type NotificationGRPCPlugin struct {
	plugin.Plugin
	Impl pluginv1.NotificationPluginServer
}

func (p *NotificationGRPCPlugin) GRPCServer(broker *plugin.GRPCBroker, s *grpc.Server) error {
	pluginv1.RegisterNotificationPluginServer(s, p.Impl)
	return nil
}

func (p *NotificationGRPCPlugin) GRPCClient(ctx context.Context, broker *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return pluginv1.NewNotificationPluginClient(c), nil
}

// Serve launches the gRPC plugin server with standard Girvi handshake configuration.
func Serve(impl pluginv1.NotificationPluginServer) {
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: HandshakeConfig,
		Plugins: map[string]plugin.Plugin{
			"notification": &NotificationGRPCPlugin{
				Impl: impl,
			},
		},
		GRPCServer: plugin.DefaultGRPCServer,
	})
}
