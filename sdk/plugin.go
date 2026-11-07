package sdk

import (
	"context"
	"fmt"
	"time"

	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

// Info is what a plugin announces during Initialize. The host compares it
// with the signed manifest the user approved and refuses anything more.
type Info struct {
	ID           string
	Name         string
	Author       string
	Description  string
	Version      string
	Capabilities []*corev1.Capability
	Requires     []string
	Settings     []*corev1.SettingField
}

// FirmConfig is the configuration for one firm.
type FirmConfig struct {
	Name    string
	Enabled bool
	Values  map[string]string
}

// Config is pushed by the host after Initialize and on every change. Secrets are
// in the values. The plugin keeps them in memory, because it retries later
// without the host.
type Config struct {
	Instance map[string]string
	Firms    map[string]FirmConfig // keyed by firm id
}

// Health is reported in the plugins screen. Messages must not hold customer data.
type Health struct {
	Status  corev1.HealthStatus
	Message string
	Firms   []*corev1.FirmHealth
}

// Result of a successful Handle.
type Result struct {
	// ProviderRef is the provider's id for what was sent, for example a
	// WhatsApp message id.
	ProviderRef string
	// Unconfirmed means the provider accepted the item but cannot tell us
	// whether it arrived. It is reported as SENT, not DELIVERED, so the shop is
	// never told something arrived when nobody knows.
	Unconfirmed bool
	// Note is shown beside the status, for example "Accepted by WhatsApp".
	Note string
}

// PermanentError tells the SDK not to retry: the item can never succeed, for
// example an invalid recipient or a template that does not exist.
type PermanentError struct {
	Class   deliveryv1.ErrorClass
	Message string
}

func (e *PermanentError) Error() string { return fmt.Sprintf("permanent: %s", e.Message) }

// Permanent builds a PermanentError.
func Permanent(class deliveryv1.ErrorClass, format string, args ...any) error {
	return &PermanentError{Class: class, Message: fmt.Sprintf(format, args...)}
}

// BlockedError tells the SDK the item cannot succeed until someone fixes
// something, for example credentials that were rejected or a template that does
// not exist yet. The item is kept and retried with the usual backoff, and the
// host is told it is blocked instead of an ordinary retry.
type BlockedError struct {
	Class   deliveryv1.ErrorClass
	Message string
}

func (e *BlockedError) Error() string { return fmt.Sprintf("blocked: %s", e.Message) }

// Blocked builds a BlockedError.
func Blocked(class deliveryv1.ErrorClass, format string, args ...any) error {
	return &BlockedError{Class: class, Message: fmt.Sprintf(format, args...)}
}

// Plugin is what an author implements. The SDK owns everything else: the
// stream to the host, the durable queue, retries with backoff, expiry and
// status updates.
type Plugin interface {
	Info() Info
	Configure(ctx context.Context, cfg Config) error
	Health(ctx context.Context) Health

	// Handle does the work for one item, for example sends the message. Return
	// nil on success. Return a *PermanentError when retrying cannot help and a
	// *BlockedError when it waits on a fix. Any other error is retried with
	// backoff until the item expires. Handle may be
	// called again for the same delivery id after a crash, so it should be safe to
	// repeat or use the provider's own idempotency key.
	Handle(ctx context.Context, item *deliveryv1.Item) (Result, error)
}

// Options tune the runtime. The zero value uses the defaults.
type Options struct {
	// Backoff is the wait before each retry. The last value repeats.
	// Default: 30s, 2m, 10m, 1h, 6h.
	Backoff []time.Duration
	// Retention is how long finished items are remembered so that a re-offer is
	// recognized. Default: 8 days, which covers the longest handoff window.
	Retention time.Duration
	// Window is how many items the host may have outstanding. Default: 16.
	Window uint32
	// HandleTimeout bounds one call to Handle. Default: 60s.
	HandleTimeout time.Duration
}

func (o Options) withDefaults() Options {
	if len(o.Backoff) == 0 {
		o.Backoff = []time.Duration{30 * time.Second, 2 * time.Minute, 10 * time.Minute, time.Hour, 6 * time.Hour}
	}
	if o.Retention == 0 {
		o.Retention = 8 * 24 * time.Hour
	}
	if o.Window == 0 {
		o.Window = 16
	}
	if o.HandleTimeout == 0 {
		o.HandleTimeout = 60 * time.Second
	}
	return o
}

func (o Options) backoff(attempts int) time.Duration {
	if attempts < 0 {
		attempts = 0
	}
	if attempts >= len(o.Backoff) {
		return o.Backoff[len(o.Backoff)-1]
	}
	return o.Backoff[attempts]
}
