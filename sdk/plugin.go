package sdk

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

// ExtensionHandshake is the go-plugin handshake for extensions built on core.v1
// and delivery.v1. It differs from HandshakeConfig (the old notification
// contract) by protocol version, so a mismatch is an explicit error.
var ExtensionHandshake = plugin.HandshakeConfig{
	ProtocolVersion:  2,
	MagicCookieKey:   "GIRVI_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "girvi-app-secure-handshake-v1",
}

// PluginName is the key under which the host dispenses an extension.
const PluginName = "extension"

// Clients are the host's views of one extension process.
type Clients struct {
	Core     corev1.ExtensionClient
	Consumer deliveryv1.ConsumerClient
}

// ExtensionPlugin connects both services through go-plugin.
type ExtensionPlugin struct {
	plugin.Plugin
	Runtime *Runtime // set on the extension side
}

func (p *ExtensionPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	corev1.RegisterExtensionServer(s, p.Runtime)
	deliveryv1.RegisterConsumerServer(s, p.Runtime)
	return nil
}

func (p *ExtensionPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &Clients{
		Core:     corev1.NewExtensionClient(c),
		Consumer: deliveryv1.NewConsumerClient(c),
	}, nil
}

// Run serves ext until the host stops it. This is main() for an extension.
func Run(ext Extension) { RunWithOptions(ext, Options{}) }

// RunWithOptions is Run with tuned retry and queue settings.
func RunWithOptions(ext Extension, opts Options) {
	rt := NewRuntime(ext, opts)
	defer rt.Close()
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: ExtensionHandshake,
		Plugins:         map[string]plugin.Plugin{PluginName: &ExtensionPlugin{Runtime: rt}},
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}
