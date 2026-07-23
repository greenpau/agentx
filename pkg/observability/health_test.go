package observability

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDoctorContainsProbeFailuresAndRedactsProjection(t *testing.T) {
	base := Snapshot{
		Product:        Fact{Value: "agentx", Source: SourceBuild},
		Surface:        Fact{Value: "headless", Source: SourceFlag},
		Platform:       Fact{Value: "macos", Source: SourceRuntime},
		Model:          Fact{Value: "gpt-5.6-sol", Source: SourceProvider},
		Authentication: Fact{Value: "token=must-not-appear", Source: SourceProvider},
	}
	doctor := NewDoctor(DoctorConfig{ProbeTimeout: 15 * time.Millisecond}, base)
	if err := doctor.Register("model_api", func(context.Context) Check {
		return Check{Status: HealthOK, Summary: "ready at /Users/alice/private", Source: SourceProvider, Details: map[string]string{"credential": "api_key=hunter2"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := doctor.Register("panicking_probe", func(context.Context) Check { panic("unstructured-panic-secret") }); err != nil {
		t.Fatal(err)
	}
	block := make(chan struct{})
	if err := doctor.Register("hanging_probe", func(context.Context) Check {
		<-block
		return Check{Status: HealthOK}
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := doctor.Run(context.Background())
	close(block)
	if len(snapshot.Components) != 3 {
		t.Fatalf("components = %+v", snapshot.Components)
	}
	if snapshot.Components[0].Status != HealthOK || snapshot.Components[1].Status != HealthError || snapshot.Components[2].Status != HealthError {
		t.Fatalf("component statuses = %+v", snapshot.Components)
	}
	encoded := snapshot.Text()
	if strings.Contains(encoded, "alice") || strings.Contains(encoded, "hunter2") || strings.Contains(encoded, "must-not-appear") || strings.Contains(encoded, "unstructured-panic-secret") {
		t.Fatalf("doctor projection leaked sensitive data: %s", encoded)
	}
	if snapshot.Authentication.Value != "token=[REDACTED]" {
		t.Fatalf("authentication fact = %q", snapshot.Authentication.Value)
	}
}

func TestDoctorDoesNotOverlapTimedOutNonCooperativeProbe(t *testing.T) {
	doctor := NewDoctor(DoctorConfig{ProbeTimeout: 5 * time.Millisecond}, Snapshot{})
	release := make(chan struct{})
	firstFinished := make(chan struct{})
	var calls atomic.Int32
	if err := doctor.Register("noncooperative", func(context.Context) Check {
		invocation := calls.Add(1)
		if invocation == 1 {
			<-release
			close(firstFinished)
		}
		return Check{Status: HealthOK, Source: SourceRuntime}
	}); err != nil {
		t.Fatal(err)
	}
	if snapshot := doctor.Run(t.Context()); snapshot.Components[0].Status != HealthError {
		t.Fatalf("first timeout = %+v", snapshot.Components)
	}
	for range 3 {
		if snapshot := doctor.Run(t.Context()); snapshot.Components[0].Status != HealthDegraded {
			t.Fatalf("overlapping probe = %+v", snapshot.Components)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("timed-out probe was started %d times", got)
	}
	close(release)
	select {
	case <-firstFinished:
	case <-time.After(time.Second):
		t.Fatal("released probe did not finish")
	}
	deadline := time.Now().Add(time.Second)
	for {
		doctor.mu.Lock()
		_, active := doctor.running["noncooperative"]
		doctor.mu.Unlock()
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed probe remained marked active")
		}
		time.Sleep(time.Millisecond)
	}
	if snapshot := doctor.Run(t.Context()); snapshot.Components[0].Status != HealthOK {
		t.Fatalf("probe did not become runnable after completion: %+v", snapshot.Components)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("completed probe invocation count = %d", got)
	}
}
