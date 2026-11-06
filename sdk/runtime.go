package sdk

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
	"github.com/anant-barlota/girvi-plugins/sdk/queue"
)

const defaultHandoffExpiry = 24 * time.Hour

// Runtime implements core.v1 and delivery.v1 on top of an Extension. Run wires
// it into go-plugin. It is exported so tests and hosts can drive it directly.
type Runtime struct {
	corev1.UnimplementedExtensionServer
	deliveryv1.UnimplementedConsumerServer

	ext  Extension
	opts Options
	now  func() time.Time

	mu     sync.Mutex
	q      *queue.Queue
	sess   *session
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	lastGC time.Time
}

// NewRuntime builds a runtime for ext.
func NewRuntime(ext Extension, opts Options) *Runtime {
	return &Runtime{ext: ext, opts: opts.withDefaults(), now: time.Now, wake: make(chan struct{}, 1)}
}

type session struct {
	srv grpc.BidiStreamingServer[deliveryv1.FromHost, deliveryv1.FromExtension]
	mu  sync.Mutex
}

func (s *session) send(m *deliveryv1.FromExtension) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.srv.Send(m)
}

// ---- core.v1 ----

func (r *Runtime) Initialize(ctx context.Context, req *corev1.InitializeRequest) (*corev1.InitializeResponse, error) {
	dir := req.GetDataDir()
	if dir == "" {
		dir = filepath.Join(os.TempDir(), "girvi-extension")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, status.Errorf(codes.Internal, "create data dir: %v", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.q == nil {
		q, err := queue.Open(filepath.Join(dir, "queue.db"))
		if err != nil {
			return nil, status.Errorf(codes.Internal, "open queue: %v", err)
		}
		r.q = q
		workCtx, cancel := context.WithCancel(context.Background())
		r.cancel = cancel
		r.done = make(chan struct{})
		go r.work(workCtx, r.done)
	}

	info := r.ext.Info()
	return &corev1.InitializeResponse{
		ExtensionId:      info.ID,
		ExtensionVersion: info.Version,
		Name:             info.Name,
		Author:           info.Author,
		Description:      info.Description,
		Capabilities:     info.Capabilities,
		Requires:         info.Requires,
		Settings:         info.Settings,
	}, nil
}

func (r *Runtime) Configure(ctx context.Context, req *corev1.ConfigureRequest) (*corev1.ConfigureResponse, error) {
	cfg := Config{Instance: req.GetInstance(), Firms: map[string]FirmConfig{}}
	for id, f := range req.GetFirms() {
		cfg.Firms[id] = FirmConfig{Name: f.GetName(), Enabled: f.GetEnabled(), Values: f.GetValues()}
	}
	if err := r.ext.Configure(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%v", err)
	}
	r.poke()
	return &corev1.ConfigureResponse{}, nil
}

func (r *Runtime) Health(ctx context.Context, _ *corev1.HealthRequest) (*corev1.HealthResponse, error) {
	h := r.ext.Health(ctx)
	return &corev1.HealthResponse{Status: h.Status, Message: h.Message, Firms: h.Firms}, nil
}

func (r *Runtime) Shutdown(ctx context.Context, _ *corev1.ShutdownRequest) (*corev1.ShutdownResponse, error) {
	r.Close()
	return &corev1.ShutdownResponse{}, nil
}

// Close stops the worker and closes the queue. Items stay on disk.
func (r *Runtime) Close() {
	r.mu.Lock()
	cancel, done, q := r.cancel, r.done, r.q
	r.cancel, r.done, r.q = nil, nil, nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if q != nil {
		_ = q.Close()
	}
}

// ---- delivery.v1 ----

func (r *Runtime) queue() *queue.Queue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.q
}

func (r *Runtime) Consume(srv grpc.BidiStreamingServer[deliveryv1.FromHost, deliveryv1.FromExtension]) error {
	if r.queue() == nil {
		return status.Error(codes.FailedPrecondition, "Initialize must be called before Consume")
	}
	sess := &session{srv: srv}
	if err := sess.send(&deliveryv1.FromExtension{Msg: &deliveryv1.FromExtension_Subscribe{
		Subscribe: &deliveryv1.Subscribe{Capabilities: r.ext.Info().Capabilities},
	}}); err != nil {
		return err
	}
	if err := sess.send(&deliveryv1.FromExtension{Msg: &deliveryv1.FromExtension_Ready{
		Ready: &deliveryv1.Ready{N: r.opts.Window},
	}}); err != nil {
		return err
	}

	r.mu.Lock()
	r.sess = sess
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.sess == sess {
			r.sess = nil
		}
		r.mu.Unlock()
	}()
	r.flush()

	for {
		msg, err := srv.Recv()
		if errors.Is(err, io.EOF) || status.Code(err) == codes.Canceled {
			return nil
		}
		if err != nil {
			return err
		}
		switch m := msg.GetMsg().(type) {
		case *deliveryv1.FromHost_Item:
			if err := r.accept(sess, m.Item); err != nil {
				return err
			}
		case *deliveryv1.FromHost_Revoke:
			r.revoke(m.Revoke.GetDeliveryId())
		}
	}
}

// accept persists an item and then acknowledges it. The ack is what hands
// custody to the extension, so persisting must come first. A delivery id seen
// before is acknowledged again without queuing a second copy.
func (r *Runtime) accept(sess *session, item *deliveryv1.Item) error {
	q := r.queue()
	if q == nil {
		return status.Error(codes.Unavailable, "extension is shutting down")
	}
	raw, err := proto.Marshal(item)
	if err != nil {
		return status.Errorf(codes.Internal, "marshal item: %v", err)
	}
	now := r.now()
	expires := now.Add(defaultHandoffExpiry)
	if item.GetExpiresAt() != nil {
		expires = item.GetExpiresAt().AsTime()
	}
	if _, _, err := q.Add(item.GetDeliveryId(), raw, expires, now); err != nil {
		// Not persisted, so not acknowledged: the host offers it again.
		return status.Errorf(codes.Internal, "persist item: %v", err)
	}
	if err := sess.send(&deliveryv1.FromExtension{Msg: &deliveryv1.FromExtension_Ack{Ack: &deliveryv1.Ack{
		DeliveryId: item.GetDeliveryId(),
		Result:     deliveryv1.AckResult_ACK_RESULT_ACCEPTED,
	}}}); err != nil {
		return err
	}
	if err := sess.send(&deliveryv1.FromExtension{Msg: &deliveryv1.FromExtension_Ready{
		Ready: &deliveryv1.Ready{N: 1},
	}}); err != nil {
		return err
	}
	r.poke()
	return nil
}

func (r *Runtime) revoke(deliveryID string) {
	q := r.queue()
	if q == nil {
		return
	}
	row, ok, err := q.Get(deliveryID)
	if err != nil || !ok || row.Final() {
		return
	}
	now := r.now()
	_ = q.MarkExpired(deliveryID, now)
	r.report(q, deliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_EXPIRED, deliveryv1.ErrorClass_ERROR_CLASS_UNSPECIFIED, "revoked by the host", "")
}

// ---- worker ----

func (r *Runtime) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *Runtime) work(ctx context.Context, done chan struct{}) {
	defer close(done)
	for {
		if ctx.Err() != nil {
			return
		}
		q := r.queue()
		if q == nil {
			return
		}
		r.gc(q)

		row, ok, err := q.NextDue(r.now())
		if err == nil && ok {
			r.process(ctx, q, row)
			continue
		}

		wait := time.Minute
		if next, has, err := q.NextWake(); err == nil && has {
			if d := next.Sub(r.now()); d < wait {
				wait = d
			}
		}
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-r.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (r *Runtime) gc(q *queue.Queue) {
	now := r.now()
	if now.Sub(r.lastGC) < time.Hour {
		return
	}
	r.lastGC = now
	_, _ = q.Purge(now.Add(-r.opts.Retention))
}

func (r *Runtime) process(ctx context.Context, q *queue.Queue, row queue.Row) {
	now := r.now()
	if !now.Before(row.ExpiresAt) {
		_ = q.MarkExpired(row.DeliveryID, now)
		r.report(q, row.DeliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_EXPIRED, deliveryv1.ErrorClass_ERROR_CLASS_UNSPECIFIED, "expired before it could be sent", "")
		return
	}

	item := &deliveryv1.Item{}
	if err := proto.Unmarshal(row.Raw, item); err != nil {
		_ = q.MarkFailed(row.DeliveryID, "unreadable item", now)
		r.report(q, row.DeliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_FAILED, deliveryv1.ErrorClass_ERROR_CLASS_INTERNAL, "unreadable item", "")
		return
	}
	item.Attempt = uint32(row.Attempts + 1)

	hctx, cancel := context.WithTimeout(ctx, r.opts.HandleTimeout)
	res, err := r.ext.Handle(hctx, item)
	cancel()

	now = r.now()
	var perm *PermanentError
	switch {
	case err == nil:
		_ = q.MarkDelivered(row.DeliveryID, res.ProviderRef, now)
		r.report(q, row.DeliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED, deliveryv1.ErrorClass_ERROR_CLASS_UNSPECIFIED, "", res.ProviderRef)
	case errors.As(err, &perm):
		_ = q.MarkFailed(row.DeliveryID, perm.Message, now)
		r.report(q, row.DeliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_FAILED, perm.Class, perm.Message, "")
	case ctx.Err() != nil:
		// Shutting down mid-call. Leave the item as it was; it runs again later.
	default:
		next := now.Add(r.opts.backoff(row.Attempts))
		if next.After(row.ExpiresAt) {
			next = row.ExpiresAt
		}
		_ = q.MarkRetry(row.DeliveryID, err.Error(), next, now)
		if row.Attempts == 0 {
			r.report(q, row.DeliveryID, deliveryv1.DeliveryState_DELIVERY_STATE_RETRYING, deliveryv1.ErrorClass_ERROR_CLASS_PROVIDER, err.Error(), "")
		}
	}
}

// report queues a status update and sends whatever is pending if a stream is open.
func (r *Runtime) report(q *queue.Queue, deliveryID string, state deliveryv1.DeliveryState, class deliveryv1.ErrorClass, msg, providerRef string) {
	_ = q.AddUpdate(queue.Update{
		DeliveryID:  deliveryID,
		State:       int32(state),
		ErrorClass:  int32(class),
		Message:     msg,
		ProviderRef: providerRef,
		At:          r.now(),
	})
	r.flush()
}

// flush sends queued status updates in order and removes each once sent.
func (r *Runtime) flush() {
	r.mu.Lock()
	sess, q := r.sess, r.q
	r.mu.Unlock()
	if sess == nil || q == nil {
		return
	}
	for {
		updates, err := q.PendingUpdates(50)
		if err != nil || len(updates) == 0 {
			return
		}
		for _, u := range updates {
			err := sess.send(&deliveryv1.FromExtension{Msg: &deliveryv1.FromExtension_Status{Status: &deliveryv1.StatusUpdate{
				DeliveryId:  u.DeliveryID,
				State:       deliveryv1.DeliveryState(u.State),
				ErrorClass:  deliveryv1.ErrorClass(u.ErrorClass),
				Message:     u.Message,
				ProviderRef: u.ProviderRef,
				At:          timestamppb.New(u.At),
			}}})
			if err != nil {
				return // the stream is gone; the rest wait for the next one
			}
			_ = q.DeleteUpdate(u.ID)
		}
	}
}
