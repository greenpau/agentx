package distributed

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sync"
)

type TransportKind string

const (
	TransportHybrid    TransportKind = "hybrid"
	TransportCCR       TransportKind = "ccr_sse"
	TransportWebSocket TransportKind = "websocket"
	TransportDirect    TransportKind = "direct_connect"
	TransportSSH       TransportKind = "ssh"
)

type UnavailableState string

const (
	UnavailableBuildExcluded   UnavailableState = "build_excluded"
	UnavailableGateDisabled    UnavailableState = "gate_disabled"
	UnavailableUnconfigured    UnavailableState = "unconfigured"
	UnavailableImplementation  UnavailableState = "implementation_unavailable"
	UnavailableMalformedConfig UnavailableState = "malformed_configuration"
)

type UnavailableError struct {
	Kind   TransportKind
	State  UnavailableState
	Reason string
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("transport %s unavailable (%s): %s", e.Kind, e.State, e.Reason)
}

type TransportConfig struct {
	Kind     TransportKind `json:"kind"`
	Included bool          `json:"included"`
	Enabled  bool          `json:"enabled"`
	Endpoint string        `json:"endpoint,omitempty"`
}

// CloseEvidence reports only loss still observable during an orderly close.
// Abrupt process death has no exact in-memory count.
type CloseEvidence struct {
	Dropped               int  `json:"dropped"`
	RemoteDurabilityKnown bool `json:"remote_durability_known"`
}

type Transport interface {
	Kind() TransportKind
	Send(context.Context, OutboundEvent) (Acceptance, error)
	Close(context.Context) (CloseEvidence, error)
}

type Factory func(context.Context, TransportConfig) (Transport, error)

// TransportRegistry has no implicit network implementation. A configured
// endpoint still fails explicitly until the owning build registers a factory.
type TransportRegistry struct {
	mu        sync.RWMutex
	factories map[TransportKind]Factory
}

func NewTransportRegistry() *TransportRegistry {
	return &TransportRegistry{factories: make(map[TransportKind]Factory)}
}

func (r *TransportRegistry) Register(kind TransportKind, factory Factory) error {
	if !knownTransport(kind) || factory == nil {
		return errors.New("transport registration requires known kind and factory")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.factories[kind]; exists {
		return errors.New("transport factory already registered")
	}
	r.factories[kind] = factory
	return nil
}

func (r *TransportRegistry) Build(ctx context.Context, config TransportConfig) (Transport, error) {
	if !knownTransport(config.Kind) {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableMalformedConfig, Reason: "unknown transport kind"}
	}
	if !config.Included {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableBuildExcluded, Reason: "transport code is not included in this build"}
	}
	if !config.Enabled {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableGateDisabled, Reason: "runtime gate or managed policy is disabled"}
	}
	if config.Endpoint == "" {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableUnconfigured, Reason: "endpoint is not configured"}
	}
	parsed, err := url.Parse(config.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableMalformedConfig, Reason: "endpoint must be an absolute URL"}
	}
	r.mu.RLock()
	factory := r.factories[config.Kind]
	r.mu.RUnlock()
	if factory == nil {
		return nil, &UnavailableError{Kind: config.Kind, State: UnavailableImplementation, Reason: "no transport factory is registered"}
	}
	transport, err := factory(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("construct %s transport: %w", config.Kind, err)
	}
	if transport == nil {
		return nil, errors.New("transport factory returned nil without error")
	}
	if transport.Kind() != config.Kind {
		_, _ = transport.Close(ctx)
		return nil, errors.New("transport factory returned a different kind")
	}
	return transport, nil
}

func knownTransport(kind TransportKind) bool {
	return kind == TransportHybrid || kind == TransportCCR || kind == TransportWebSocket || kind == TransportDirect || kind == TransportSSH
}
