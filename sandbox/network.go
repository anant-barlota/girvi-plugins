package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/anant-barlota/girvi-plugins/contract"
	"github.com/anant-barlota/girvi-plugins/manifest"
	"github.com/anant-barlota/girvi-plugins/sdk"
)

// Network is the Sender Girvi uses: real requests, with the address checked
// at connect time. Redirects are handed back to the plugin, since following
// one would leave the granted host. The system proxy is not used: the address
// check has to see the real destination.
type Network struct {
	client *http.Client
}

func NewNetwork() *Network {
	dialer := &net.Dialer{Timeout: 10 * time.Second, ControlContext: guardDial}
	return &Network{client: &http.Client{
		Transport: &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: contract.CallDeadline,
			MaxIdleConns:          16,
			IdleConnTimeout:       time.Minute,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type localAllowedKey struct{}

// Send makes the request. Unless local, it must not connect to this computer
// or network.
func (n *Network) Send(ctx context.Context, r sdk.Request, local bool) sdk.Response {
	req, err := http.NewRequestWithContext(context.WithValue(ctx, localAllowedKey{}, local), r.Method, r.URL, bytes.NewReader(r.Body))
	if err != nil {
		return sdk.Response{Error: err.Error()}
	}
	for k, v := range r.Headers {
		req.Header.Set(k, v)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		// url.Error repeats the full URL, whose query string can hold a secret.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return sdk.Response{Error: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, contract.MaxHTTPResponse))
	return sdk.Response{Status: resp.StatusCode, Body: body}
}

// guardDial runs just before a connection is made, with the address the name
// resolved to. A plugin granted a public name must not land on this computer
// or network: otherwise a plugin could name a host it controls, point it at
// 127.0.0.1, and call Girvi's own API or anything else running here. Checking
// at connect time, not at lookup, closes that however the name resolves.
func guardDial(ctx context.Context, _, address string, _ syscall.RawConn) error {
	if local, _ := ctx.Value(localAllowedKey{}).(bool); local {
		return nil
	}
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("unexpected address %q", address)
	}
	if manifest.LocalAddr(ap.Addr()) {
		return errors.New("this plugin is not allowed to reach this computer or network")
	}
	return nil
}
