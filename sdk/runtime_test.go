package sdk

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/core/v1"
	deliveryv1 "github.com/anant-barlota/girvi-plugins/sdk/gen/girvi/delivery/v1"
)

// fakeExt records calls and lets a test script what Handle returns.
type fakeExt struct {
	mu     sync.Mutex
	calls  map[string]int
	handle func(ctx context.Context, item *deliveryv1.Item, call int) (Result, error)
}

func (f *fakeExt) Info() Info {
	return Info{
		ID:           "test/fake",
		Version:      "0.0.1",
		Capabilities: []*corev1.Capability{{Name: "notify", Version: 1}},
	}
}
func (f *fakeExt) Configure(context.Context, Config) error { return nil }
func (f *fakeExt) Health(context.Context) Health {
	return Health{Status: corev1.HealthStatus_HEALTH_STATUS_OK}
}
func (f *fakeExt) Handle(ctx context.Context, item *deliveryv1.Item) (Result, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[item.GetDeliveryId()]++
	n := f.calls[item.GetDeliveryId()]
	h := f.handle
	f.mu.Unlock()
	if h == nil {
		return Result{ProviderRef: "ref-1"}, nil
	}
	return h(ctx, item, n)
}
func (f *fakeExt) callCount(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[id]
}

type harness struct {
	t      *testing.T
	rt     *Runtime
	conn   *grpc.ClientConn
	core   corev1.ExtensionClient
	client deliveryv1.ConsumerClient
	srv    *grpc.Server
}

func newHarness(t *testing.T, ext Extension, dir string, opts Options) *harness {
	t.Helper()
	rt := NewRuntime(ext, opts)
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	corev1.RegisterExtensionServer(srv, rt)
	deliveryv1.RegisterConsumerServer(srv, rt)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	h := &harness{t: t, rt: rt, conn: conn, core: corev1.NewExtensionClient(conn), client: deliveryv1.NewConsumerClient(conn), srv: srv}
	t.Cleanup(func() {
		_ = conn.Close()
		srv.Stop()
		rt.Close()
	})
	if _, err := h.core.Initialize(context.Background(), &corev1.InitializeRequest{DataDir: dir}); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	return h
}

// stream is the host's side of one Consume call.
type stream struct {
	t      *testing.T
	send   func(*deliveryv1.FromHost) error
	msgs   chan *deliveryv1.FromExtension
	cancel context.CancelFunc
}

func (h *harness) open() *stream {
	h.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cs, err := h.client.Consume(ctx)
	if err != nil {
		h.t.Fatalf("consume: %v", err)
	}
	s := &stream{t: h.t, send: cs.Send, msgs: make(chan *deliveryv1.FromExtension, 64), cancel: cancel}
	go func() {
		for {
			m, err := cs.Recv()
			if err != nil {
				close(s.msgs)
				return
			}
			s.msgs <- m
		}
	}()
	return s
}

func (s *stream) next() *deliveryv1.FromExtension {
	s.t.Helper()
	select {
	case m, ok := <-s.msgs:
		if !ok {
			s.t.Fatal("stream closed while waiting for a message")
		}
		return m
	case <-time.After(5 * time.Second):
		s.t.Fatal("timed out waiting for a message")
		return nil
	}
}

// expectHello consumes the Subscribe and the initial Ready.
func (s *stream) expectHello() {
	s.t.Helper()
	if s.next().GetSubscribe() == nil {
		s.t.Fatal("first message should be Subscribe")
	}
	if s.next().GetReady() == nil {
		s.t.Fatal("second message should be Ready")
	}
}

func (s *stream) offer(id string, expires time.Time) {
	s.t.Helper()
	if err := s.send(&deliveryv1.FromHost{Msg: &deliveryv1.FromHost_Item{Item: &deliveryv1.Item{
		DeliveryId: id,
		Capability: "notify",
		ExpiresAt:  timestamppb.New(expires),
	}}}); err != nil {
		s.t.Fatalf("offer: %v", err)
	}
}

// statuses reads messages until it has seen the wanted states for id, ignoring acks and credits.
func (s *stream) statuses(id string, want ...deliveryv1.DeliveryState) []*deliveryv1.StatusUpdate {
	s.t.Helper()
	var got []*deliveryv1.StatusUpdate
	for len(got) < len(want) {
		m := s.next()
		if u := m.GetStatus(); u != nil && u.GetDeliveryId() == id {
			got = append(got, u)
		}
	}
	for i, w := range want {
		if got[i].GetState() != w {
			s.t.Fatalf("status %d = %v; want %v", i, got[i].GetState(), w)
		}
	}
	return got
}

func fastOpts() Options {
	return Options{Backoff: []time.Duration{20 * time.Millisecond}, HandleTimeout: 5 * time.Second}
}

func TestAcceptsThenDeliversAndReports(t *testing.T) {
	ext := &fakeExt{}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	ack := s.next().GetAck()
	if ack == nil || ack.GetResult() != deliveryv1.AckResult_ACK_RESULT_ACCEPTED || ack.GetDeliveryId() != "d1" {
		t.Fatalf("want ACCEPTED ack for d1, got %v", ack)
	}
	got := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)
	if got[0].GetProviderRef() != "ref-1" {
		t.Errorf("provider ref = %q; want ref-1", got[0].GetProviderRef())
	}
}

func TestReOfferedItemIsAcknowledgedButNotHandledTwice(t *testing.T) {
	ext := &fakeExt{}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)

	// The host never saw the ack and offers it again.
	s.offer("d1", time.Now().Add(time.Hour))
	for {
		if a := s.next().GetAck(); a != nil && a.GetResult() == deliveryv1.AckResult_ACK_RESULT_ACCEPTED {
			// either the first ack (already consumed by statuses) or the second one
			break
		}
	}
	time.Sleep(100 * time.Millisecond)
	if n := ext.callCount("d1"); n != 1 {
		t.Errorf("Handle called %d times; want 1", n)
	}
}

func TestTransientFailureIsRetriedUntilItWorks(t *testing.T) {
	ext := &fakeExt{handle: func(_ context.Context, _ *deliveryv1.Item, call int) (Result, error) {
		if call < 3 {
			return Result{}, errors.New("provider unavailable")
		}
		return Result{ProviderRef: "ok-after-retries"}, nil
	}}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	got := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_RETRYING, deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)
	if got[1].GetProviderRef() != "ok-after-retries" {
		t.Errorf("provider ref = %q", got[1].GetProviderRef())
	}
	if n := ext.callCount("d1"); n != 3 {
		t.Errorf("Handle called %d times; want 3", n)
	}
}

func TestPermanentErrorIsNotRetried(t *testing.T) {
	ext := &fakeExt{handle: func(context.Context, *deliveryv1.Item, int) (Result, error) {
		return Result{}, Permanent(deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT, "recipient is not a valid number")
	}}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	got := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_FAILED)
	if got[0].GetErrorClass() != deliveryv1.ErrorClass_ERROR_CLASS_INVALID_INPUT {
		t.Errorf("error class = %v", got[0].GetErrorClass())
	}
	time.Sleep(100 * time.Millisecond)
	if n := ext.callCount("d1"); n != 1 {
		t.Errorf("Handle called %d times; want 1", n)
	}
}

func TestExpiredItemIsDroppedWithoutHandling(t *testing.T) {
	ext := &fakeExt{}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(-time.Minute))
	s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_EXPIRED)
	if n := ext.callCount("d1"); n != 0 {
		t.Errorf("Handle called %d times for an expired item; want 0", n)
	}
}

func TestStatusUpdatesWaitForTheNextStream(t *testing.T) {
	release := make(chan struct{})
	ext := &fakeExt{handle: func(ctx context.Context, _ *deliveryv1.Item, _ int) (Result, error) {
		select {
		case <-release:
			return Result{ProviderRef: "late"}, nil
		case <-ctx.Done():
			return Result{}, ctx.Err()
		}
	}}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()
	s.offer("d1", time.Now().Add(time.Hour))
	if s.next().GetAck() == nil {
		t.Fatal("want an ack")
	}

	// The stream drops while the extension is still working.
	s.cancel()
	time.Sleep(50 * time.Millisecond)
	close(release)
	time.Sleep(100 * time.Millisecond)

	// A new stream receives the update that was held back.
	s2 := h.open()
	s2.expectHello()
	got := s2.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)
	if got[0].GetProviderRef() != "late" {
		t.Errorf("provider ref = %q; want late", got[0].GetProviderRef())
	}
}

func TestAcceptedItemsSurviveARestart(t *testing.T) {
	dir := t.TempDir()

	// First run: the item is accepted, then the process stops mid-call.
	started := make(chan struct{})
	ext1 := &fakeExt{handle: func(ctx context.Context, _ *deliveryv1.Item, _ int) (Result, error) {
		close(started)
		<-ctx.Done()
		return Result{}, ctx.Err()
	}}
	h1 := newHarness(t, ext1, dir, fastOpts())
	s := h1.open()
	s.expectHello()
	s.offer("d1", time.Now().Add(time.Hour))
	if s.next().GetAck() == nil {
		t.Fatal("want an ack")
	}
	<-started
	h1.rt.Close()

	// Second run on the same directory picks it up and finishes it.
	ext2 := &fakeExt{}
	h2 := newHarness(t, ext2, dir, fastOpts())
	s2 := h2.open()
	s2.expectHello()
	s2.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)
	if n := ext2.callCount("d1"); n != 1 {
		t.Errorf("Handle called %d times after restart; want 1", n)
	}
}

func TestConsumeBeforeInitializeFails(t *testing.T) {
	rt := NewRuntime(&fakeExt{}, fastOpts())
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	deliveryv1.RegisterConsumerServer(srv, rt)
	go func() { _ = srv.Serve(lis) }()
	defer srv.Stop()
	conn, _ := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	defer conn.Close()

	cs, err := deliveryv1.NewConsumerClient(conn).Consume(context.Background())
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if _, err := cs.Recv(); err == nil {
		t.Error("Consume before Initialize should fail")
	}
}

func TestABlockedItemWaitsAndIsDeliveredOnceTheFixIsIn(t *testing.T) {
	fixed := make(chan struct{})
	ext := &fakeExt{handle: func(_ context.Context, _ *deliveryv1.Item, _ int) (Result, error) {
		select {
		case <-fixed:
			return Result{ProviderRef: "after-fix"}, nil
		default:
			return Result{}, Blocked(deliveryv1.ErrorClass_ERROR_CLASS_CONFIG, "access token rejected")
		}
	}}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	got := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_BLOCKED)
	if got[0].GetErrorClass() != deliveryv1.ErrorClass_ERROR_CLASS_CONFIG || got[0].GetMessage() != "access token rejected" {
		t.Errorf("blocked status wrong: %v", got[0])
	}

	// It keeps retrying without repeating the report, then finishes.
	time.Sleep(150 * time.Millisecond)
	close(fixed)
	final := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_DELIVERED)
	if final[0].GetProviderRef() != "after-fix" {
		t.Errorf("provider ref = %q", final[0].GetProviderRef())
	}
}

func TestAnUnconfirmedSendIsReportedAsSentNotDelivered(t *testing.T) {
	ext := &fakeExt{handle: func(context.Context, *deliveryv1.Item, int) (Result, error) {
		return Result{ProviderRef: "wamid.1", Unconfirmed: true, Note: "Accepted by WhatsApp. Delivery is not confirmed."}, nil
	}}
	h := newHarness(t, ext, t.TempDir(), fastOpts())
	s := h.open()
	s.expectHello()

	s.offer("d1", time.Now().Add(time.Hour))
	got := s.statuses("d1", deliveryv1.DeliveryState_DELIVERY_STATE_SENT)
	if got[0].GetProviderRef() != "wamid.1" || got[0].GetMessage() != "Accepted by WhatsApp. Delivery is not confirmed." {
		t.Errorf("sent status wrong: %v", got[0])
	}
	time.Sleep(100 * time.Millisecond)
	if n := ext.callCount("d1"); n != 1 {
		t.Errorf("Handle called %d times; an unconfirmed send is still finished, so want 1", n)
	}
}
