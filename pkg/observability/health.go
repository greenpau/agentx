package observability

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	DefaultProbeTimeout = 2 * time.Second
	DefaultMaxChecks    = 64
)

type Source string

const (
	SourceDefault  Source = "default"
	SourceUser     Source = "user"
	SourceProject  Source = "project"
	SourceLocal    Source = "local"
	SourceFlag     Source = "flag"
	SourceManaged  Source = "managed"
	SourceRemote   Source = "remote"
	SourceProvider Source = "provider"
	SourceRuntime  Source = "runtime"
	SourceBuild    Source = "build"
)

type HealthStatus string

const (
	HealthOK          HealthStatus = "ok"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
	HealthError       HealthStatus = "error"
	HealthUnknown     HealthStatus = "unknown"
)

// Fact retains source attribution where it changes the repair path.
type Fact struct {
	Value  string `json:"value"`
	Source Source `json:"source"`
}

// Check is a read-only component-health projection. Details must be bounded
// classifications, not arbitrary config structs or service bodies.
type Check struct {
	Name      string            `json:"name"`
	Status    HealthStatus      `json:"status"`
	Summary   string            `json:"summary,omitempty"`
	Source    Source            `json:"source"`
	Details   map[string]string `json:"details,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
}

// Snapshot is safe for a doctor command or structured diagnostics. Provider
// and authentication fields describe state, never credential material.
type Snapshot struct {
	Version        int             `json:"version"`
	Product        Fact            `json:"product"`
	Surface        Fact            `json:"surface"`
	Platform       Fact            `json:"platform"`
	Installation   Fact            `json:"installation"`
	Model          Fact            `json:"model"`
	Provider       Fact            `json:"provider"`
	Authentication Fact            `json:"authentication"`
	Policy         Fact            `json:"policy"`
	Sandbox        Fact            `json:"sandbox"`
	Facts          map[string]Fact `json:"facts,omitempty"`
	Components     []Check         `json:"components"`
	Usage          *UsageSnapshot  `json:"usage,omitempty"`
	GeneratedAt    time.Time       `json:"generated_at"`
}

type Probe func(context.Context) Check

type DoctorConfig struct {
	ProbeTimeout time.Duration
	MaxChecks    int
}

type namedProbe struct {
	sequence uint64
	name     string
	probe    Probe
}

// Doctor runs read-only probes concurrently and contains timeout, panic, and
// malformed-result failures to the owning component row.
type Doctor struct {
	config DoctorConfig
	base   Snapshot

	mu      sync.Mutex
	next    uint64
	probes  map[string]namedProbe
	running map[string]struct{}
}

func NewDoctor(config DoctorConfig, base Snapshot) *Doctor {
	if config.ProbeTimeout <= 0 {
		config.ProbeTimeout = DefaultProbeTimeout
	}
	if config.MaxChecks <= 0 {
		config.MaxChecks = DefaultMaxChecks
	}
	return &Doctor{config: config, base: sanitizeSnapshot(base), probes: make(map[string]namedProbe), running: make(map[string]struct{})}
}

func (d *Doctor) Register(name string, probe Probe) error {
	if !attributeKeyPattern.MatchString(name) || probe == nil {
		return errors.New("health probe requires a bounded name and callback")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.probes[name]; exists {
		return errors.New("health probe already registered")
	}
	if len(d.probes) >= d.config.MaxChecks {
		return errors.New("health probe limit reached")
	}
	d.next++
	d.probes[name] = namedProbe{sequence: d.next, name: name, probe: probe}
	return nil
}

func (d *Doctor) Run(ctx context.Context) Snapshot {
	d.mu.Lock()
	probes := make([]namedProbe, 0, len(d.probes))
	for _, probe := range d.probes {
		probes = append(probes, probe)
	}
	base := sanitizeSnapshot(d.base)
	d.mu.Unlock()
	sort.Slice(probes, func(i, j int) bool { return probes[i].sequence < probes[j].sequence })

	type probeResult struct {
		sequence uint64
		check    Check
	}
	results := make(chan probeResult, len(probes))
	for _, item := range probes {
		item := item
		go func() {
			if !d.beginProbe(item.name) {
				results <- probeResult{sequence: item.sequence, check: Check{
					Name: item.name, Status: HealthDegraded, Summary: "previous health probe is still running",
					Source: SourceRuntime, CheckedAt: time.Now().UTC(),
				}}
				return
			}
			probeCtx, cancel := context.WithTimeout(ctx, d.config.ProbeTimeout)
			defer cancel()
			completed := make(chan Check, 1)
			go func() {
				check := safeProbe(item.name, item.probe, probeCtx)
				d.finishProbe(item.name)
				completed <- check
			}()
			select {
			case check := <-completed:
				results <- probeResult{sequence: item.sequence, check: sanitizeCheck(item.name, check)}
			case <-probeCtx.Done():
				status := HealthError
				summary := "health probe timed out"
				if errors.Is(probeCtx.Err(), context.Canceled) {
					status, summary = HealthUnknown, "health inspection cancelled"
				}
				results <- probeResult{sequence: item.sequence, check: Check{Name: item.name, Status: status, Summary: summary, Source: SourceRuntime, CheckedAt: time.Now().UTC()}}
			}
		}()
	}
	collected := make([]probeResult, 0, len(probes))
	for range probes {
		collected = append(collected, <-results)
	}
	sort.Slice(collected, func(i, j int) bool { return collected[i].sequence < collected[j].sequence })
	base.Components = make([]Check, 0, len(collected))
	for _, result := range collected {
		base.Components = append(base.Components, result.check)
	}
	base.GeneratedAt = time.Now().UTC()
	return base
}

func (d *Doctor) beginProbe(name string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, active := d.running[name]; active {
		return false
	}
	d.running[name] = struct{}{}
	return true
}

func (d *Doctor) finishProbe(name string) {
	d.mu.Lock()
	delete(d.running, name)
	d.mu.Unlock()
}

func safeProbe(name string, probe Probe, ctx context.Context) (result Check) {
	defer func() {
		if recover() != nil {
			// Panic payloads are arbitrary callback-controlled values and may carry
			// credentials. Preserve the failure class without reflecting the value.
			result = Check{Name: name, Status: HealthError, Summary: "health probe panicked", Source: SourceRuntime, CheckedAt: time.Now().UTC()}
		}
	}()
	return probe(ctx)
}

func sanitizeCheck(name string, check Check) Check {
	check.Name = name
	if check.Status != HealthOK && check.Status != HealthDegraded && check.Status != HealthUnavailable && check.Status != HealthError && check.Status != HealthUnknown {
		check.Status = HealthUnknown
	}
	if !validSource(check.Source) {
		check.Source = SourceRuntime
	}
	check.Summary = truncateRunes(RedactText(check.Summary), 512)
	if check.CheckedAt.IsZero() {
		check.CheckedAt = time.Now().UTC()
	} else {
		check.CheckedAt = check.CheckedAt.UTC()
	}
	if check.Details != nil {
		cloned := make(map[string]string, len(check.Details))
		for key, value := range check.Details {
			cloned[key] = value
		}
		check.Details = cloned
	}
	if len(check.Details) > 16 {
		check.Details = map[string]string{"details": "too many detail fields"}
	} else {
		for key, value := range check.Details {
			if !attributeKeyPattern.MatchString(key) {
				delete(check.Details, key)
				continue
			}
			check.Details[key] = truncateRunes(RedactText(value), 256)
		}
	}
	return check
}

func sanitizeSnapshot(snapshot Snapshot) Snapshot {
	snapshot.Version = 1
	snapshot.Product = sanitizeFact(snapshot.Product)
	snapshot.Surface = sanitizeFact(snapshot.Surface)
	snapshot.Platform = sanitizeFact(snapshot.Platform)
	snapshot.Installation = sanitizeFact(snapshot.Installation)
	snapshot.Model = sanitizeFact(snapshot.Model)
	snapshot.Provider = sanitizeFact(snapshot.Provider)
	snapshot.Authentication = sanitizeFact(snapshot.Authentication)
	snapshot.Policy = sanitizeFact(snapshot.Policy)
	snapshot.Sandbox = sanitizeFact(snapshot.Sandbox)
	if snapshot.Facts != nil {
		facts := make(map[string]Fact)
		for key, value := range snapshot.Facts {
			if attributeKeyPattern.MatchString(key) && len(facts) < 32 {
				facts[key] = sanitizeFact(value)
			}
		}
		snapshot.Facts = facts
	}
	snapshot.Components = nil
	return snapshot
}

func sanitizeFact(fact Fact) Fact {
	fact.Value = truncateRunes(RedactText(fact.Value), 256)
	if !validSource(fact.Source) {
		fact.Source = SourceRuntime
	}
	return fact
}

func validSource(source Source) bool {
	return source == SourceDefault || source == SourceUser || source == SourceProject || source == SourceLocal ||
		source == SourceFlag || source == SourceManaged || source == SourceRemote || source == SourceProvider ||
		source == SourceRuntime || source == SourceBuild
}

// Text renders a compact deterministic doctor view without writing or
// repairing any external state.
func (s Snapshot) Text() string {
	var builder strings.Builder
	writeFact := func(name string, fact Fact) {
		if fact.Value != "" {
			fmt.Fprintf(&builder, "%s: %s (%s)\n", name, fact.Value, fact.Source)
		}
	}
	writeFact("product", s.Product)
	writeFact("surface", s.Surface)
	writeFact("platform", s.Platform)
	writeFact("model", s.Model)
	writeFact("provider", s.Provider)
	writeFact("authentication", s.Authentication)
	writeFact("policy", s.Policy)
	writeFact("sandbox", s.Sandbox)
	for _, check := range s.Components {
		fmt.Fprintf(&builder, "%s: %s", check.Name, check.Status)
		if check.Summary != "" {
			fmt.Fprintf(&builder, " — %s", check.Summary)
		}
		builder.WriteByte('\n')
	}
	return builder.String()
}
