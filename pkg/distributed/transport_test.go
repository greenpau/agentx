package distributed

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeTransport struct {
	kind      TransportKind
	accepted  bool
	closeData CloseEvidence
	closeErr  error
	closed    atomic.Int32
	sent      atomic.Int32
}

func (f *fakeTransport) Kind() TransportKind { return f.kind }

func (f *fakeTransport) Send(context.Context, OutboundEvent) (Acceptance, error) {
	f.sent.Add(1)
	return Acceptance{Accepted: f.accepted, QueueIdentity: "fake-queue"}, nil
}

func (f *fakeTransport) Close(context.Context) (CloseEvidence, error) {
	f.closed.Add(1)
	return f.closeData, f.closeErr
}

func TestTransportRegistryReportsIndependentUnavailableStates(t *testing.T) {
	registry := NewTransportRegistry()
	tests := []struct {
		config TransportConfig
		want   UnavailableState
	}{
		{TransportConfig{Kind: TransportCCR}, UnavailableBuildExcluded},
		{TransportConfig{Kind: TransportCCR, Included: true}, UnavailableGateDisabled},
		{TransportConfig{Kind: TransportCCR, Included: true, Enabled: true}, UnavailableUnconfigured},
		{TransportConfig{Kind: TransportCCR, Included: true, Enabled: true, Endpoint: "://bad"}, UnavailableMalformedConfig},
		{TransportConfig{Kind: TransportCCR, Included: true, Enabled: true, Endpoint: "https://example.test"}, UnavailableImplementation},
	}
	for _, test := range tests {
		_, err := registry.Build(context.Background(), test.config)
		var unavailable *UnavailableError
		if !errors.As(err, &unavailable) || unavailable.State != test.want {
			t.Fatalf("config %+v error=%v, want %s", test.config, err, test.want)
		}
	}

	want := &fakeTransport{kind: TransportCCR, accepted: true}
	if err := registry.Register(TransportCCR, func(context.Context, TransportConfig) (Transport, error) { return want, nil }); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Build(context.Background(), TransportConfig{Kind: TransportCCR, Included: true, Enabled: true, Endpoint: "https://example.test"})
	if err != nil || got != want {
		t.Fatalf("registered build = %v, %v", got, err)
	}
}

func TestReconnectIsSerializedAndFencesOldEpoch(t *testing.T) {
	old := &fakeTransport{kind: TransportCCR, accepted: true}
	coordinator, err := NewReconnectCoordinator(context.Background(), "session", 1, Cursor{Sequence: 8}, old)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &fakeTransport{kind: TransportCCR, accepted: true, closeData: CloseEvidence{Dropped: 3}}
	started := make(chan struct{})
	release := make(chan struct{})
	var attempts atomic.Int32
	attempt := func(_ context.Context, resume ResumePoint) (Transport, Epoch, error) {
		if attempts.Add(1) == 1 {
			close(started)
		}
		if resume.Epoch != 1 || resume.Cursor.Sequence != 8 || resume.Session != "session" {
			t.Errorf("resume point = %+v", resume)
		}
		<-release
		return replacement, 2, nil
	}
	type result struct {
		transport Transport
		epoch     Epoch
		err       error
	}
	results := make(chan result, 2)
	go func() {
		transport, epoch, err := coordinator.Recover(context.Background(), "401", attempt)
		results <- result{transport, epoch, err}
	}()
	<-started
	go func() {
		transport, epoch, err := coordinator.Recover(context.Background(), "proactive", attempt)
		results <- result{transport, epoch, err}
	}()
	time.Sleep(10 * time.Millisecond)
	close(release)
	for range 2 {
		got := <-results
		if got.err != nil || got.transport != replacement || got.epoch != 2 {
			t.Fatalf("recovery result = %+v", got)
		}
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d", attempts.Load())
	}
	if old.closed.Load() != 1 {
		t.Fatalf("old transport close count = %d", old.closed.Load())
	}
	if _, err := coordinator.Send(context.Background(), 1, outbound("stale")); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale send error = %v", err)
	}
	if acceptance, err := coordinator.Send(context.Background(), 2, outbound("current")); err != nil || !acceptance.Accepted {
		t.Fatalf("current send = %+v, %v", acceptance, err)
	}

	first, err := coordinator.Close(context.Background())
	if err != nil || first.Dropped != 3 {
		t.Fatalf("first close = %+v, %v", first, err)
	}
	second, err := coordinator.Close(context.Background())
	if err != nil || second != first {
		t.Fatalf("second close = %+v, %v", second, err)
	}
	if replacement.closed.Load() != 1 {
		t.Fatalf("replacement close count = %d", replacement.closed.Load())
	}
}

func TestReconnectWaiterCancellationDoesNotCancelSharedRecovery(t *testing.T) {
	coordinator, err := NewReconnectCoordinator(context.Background(), "session", 1, Cursor{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	replacement := &fakeTransport{kind: TransportCCR, accepted: true}
	attempt := func(context.Context, ResumePoint) (Transport, Epoch, error) {
		once.Do(func() { close(started) })
		<-release
		return replacement, 2, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, _, err := coordinator.Recover(ctx, "test", attempt)
		errCh <- err
	}()
	<-started
	if err := <-errCh; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v", err)
	}
	joined := make(chan error, 1)
	go func() {
		_, epoch, err := coordinator.Recover(context.Background(), "join", attempt)
		if err == nil && epoch != 2 {
			err = errors.New("wrong epoch")
		}
		joined <- err
	}()
	// Give the second waiter a chance to join the still-blocked shared flight.
	time.Sleep(10 * time.Millisecond)
	close(release)
	if err := <-joined; err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}
