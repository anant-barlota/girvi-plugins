package sdk

import (
	"context"

	"github.com/hashicorp/go-plugin"
	"google.golang.org/grpc"

	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

// Handshake is the go-plugin handshake for plugins built on core.v1 and
// delivery.v1. The protocol version rises whenever the wire contract changes
// incompatibly, so a plugin built against an older SDK fails to start with a clear
// error instead of failing on its first call.
var Handshake = plugin.HandshakeConfig{
	ProtocolVersion:  3,
	MagicCookieKey:   "GIRVI_PLUGIN_MAGIC_COOKIE",
	MagicCookieValue: "girvi-app-secure-handshake-v1",
}

// PluginName is the key under which the host dispenses a plugin.
const PluginName = "plugin"

// Clients are the host's views of one plugin process.
type Clients struct {
	Core     corev1.PluginClient
	Consumer deliveryv1.ConsumerClient
}

// GRPCPlugin connects both services through go-plugin.
type GRPCPlugin struct {
	plugin.Plugin
	Runtime *Runtime // set on the plugin side
}

func (p *GRPCPlugin) GRPCServer(_ *plugin.GRPCBroker, s *grpc.Server) error {
	corev1.RegisterPluginServer(s, p.Runtime)
	deliveryv1.RegisterConsumerServer(s, p.Runtime)
	return nil
}

func (p *GRPCPlugin) GRPCClient(_ context.Context, _ *plugin.GRPCBroker, c *grpc.ClientConn) (any, error) {
	return &Clients{
		Core:     corev1.NewPluginClient(c),
		Consumer: deliveryv1.NewConsumerClient(c),
	}, nil
}

// Run serves plug until the host stops it. This is main() for a plugin.
func Run(plug Plugin) { RunWithOptions(plug, Options{}) }

// RunWithOptions is Run with tuned retry and queue settings.
func RunWithOptions(plug Plugin, opts Options) {
	rt := NewRuntime(plug, opts)
	defer rt.Close()
	plugin.Serve(&plugin.ServeConfig{
		HandshakeConfig: Handshake,
		Plugins:         map[string]plugin.Plugin{PluginName: &GRPCPlugin{Runtime: rt}},
		GRPCServer:      plugin.DefaultGRPCServer,
	})
}
