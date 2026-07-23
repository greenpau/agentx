package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type testSink struct {
	mu       sync.Mutex
	records  []Record
	fail     error
	panicNow bool
	started  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (s *testSink) Export(ctx context.Context, records []Record) error {
	if s.panicNow {
		panic("broken observer")
	}
	if s.started != nil {
		s.once.Do(func() { close(s.started) })
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if s.fail != nil {
		return s.fail
	}
	s.mu.Lock()
	s.records = append(s.records, cloneRecords(records)...)
	s.mu.Unlock()
	return nil
}

func (s *testSink) Shutdown(context.Context) error { return nil }

func requireDurableFallback(t *testing.T) {
	t.Helper()
	if !fallbackPlatformSupported() {
		t.Skip("durable observability fallback is unavailable on this platform")
	}
}

type sinkFuncs struct {
	export   func(context.Context, []Record) error
	shutdown func(context.Context) error
}

func (s sinkFuncs) Export(ctx context.Context, records []Record) error {
	if s.export == nil {
		return nil
	}
	return s.export(ctx, records)
}

func (s sinkFuncs) Shutdown(ctx context.Context) error {
	if s.shutdown == nil {
		return nil
	}
	return s.shutdown(ctx)
}

type cyclicSinkError struct{}

func (*cyclicSinkError) Error() string { panic("sink Error must not be called") }
func (err *cyclicSinkError) Unwrap() error {
	return err
}

type panickingSinkUnwrapError struct{}

func (*panickingSinkUnwrapError) Error() string { panic("sink Error must not be called") }
func (*panickingSinkUnwrapError) Unwrap() error {
	panic("private unwrap panic payload")
}

type panickingSinkIsError struct{}

func (*panickingSinkIsError) Error() string { panic("sink Error must not be called") }
func (*panickingSinkIsError) Is(error) bool {
	panic("custom Is must not be called")
}

type panickingSinkAsError struct{}

func (*panickingSinkAsError) Error() string { panic("sink Error must not be called") }
func (*panickingSinkAsError) As(any) bool {
	panic("custom As must not be called")
}

type wideSinkError struct {
	children []error
}

func (*wideSinkError) Error() string       { panic("sink Error must not be called") }
func (err *wideSinkError) Unwrap() []error { return err.children }

type blockingSinkUnwrapError struct {
	called  chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockingSinkUnwrapError) Error() string { return "foreign sink failure" }
func (err *blockingSinkUnwrapError) Unwrap() error {
	err.once.Do(func() { close(err.called) })
	<-err.release
	return context.Canceled
}

func TestExporterBoundsQueueWithoutAffectingProducer(t *testing.T) {
	sink := &testSink{started: make(chan struct{}), release: make(chan struct{})}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics, Policy: Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity: 1, BatchSize: 1, FlushInterval: time.Hour,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := exporter.Observe(validEvent()); got.Queue != QueueEnqueued {
		t.Fatalf("first queue result = %+v", got)
	}
	<-sink.started
	event := validEvent()
	event.ID = "event-2"
	if got := exporter.Observe(event); got.Queue != QueueEnqueued {
		t.Fatalf("second queue result = %+v", got)
	}
	event.ID = "event-3"
	if got := exporter.Observe(event); got.Queue != QueueFull {
		t.Fatalf("overflow result = %+v", got)
	}
	close(sink.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := exporter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stats := exporter.Stats()
	if stats.DroppedQueue != 1 || stats.Exported != 2 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestExporterContainsSinkFailureAndRetriesSanitizedDiskFallback(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	failing := &testSink{fail: errors.New("offline")}
	config := ExporterConfig{
		Destination: DestinationAnalytics, Policy: Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity: 2, BatchSize: 1, FlushInterval: time.Hour, FallbackDir: dir,
	}
	exporter, err := NewExporter(config, failing)
	if err != nil {
		t.Fatal(err)
	}
	event := validEvent()
	event.Attributes["diagnostic"] = StringAttribute("token=do-not-store", PrivacyOperational, CardinalityBounded)
	if got := exporter.Observe(event); got.Queue != QueueEnqueued {
		t.Fatal(got)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := exporter.Stats()
	if stats.ExportFailures != 1 || stats.Persisted != 1 {
		t.Fatalf("failure stats = %+v", stats)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("fallback entries = %v, %v", entries, err)
	}
	info, err := os.Stat(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("fallback mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || contains(string(data), "do-not-store") {
		t.Fatalf("fallback contains unredacted secret: %s", data)
	}

	success := &testSink{}
	retry, err := NewExporter(config, success)
	if err != nil {
		t.Fatal(err)
	}
	if err := retry.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := retry.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("fallback was not drained: %v, %v", entries, err)
	}
	success.mu.Lock()
	defer success.mu.Unlock()
	if len(success.records) != 1 {
		t.Fatalf("retried records = %+v", success.records)
	}
}

func TestFallbackRetentionBoundsSustainedOutageDeterministically(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	config := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir, FallbackFileMax: 1 << 20,
		FallbackMaxFiles: 3, FallbackMaxBytes: 1 << 20, FallbackMaxAge: time.Hour,
	}
	exporter, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	record, admission := Admit(validEvent(), DestinationAnalytics, config.Policy)
	if admission.Status != AdmissionAccepted {
		t.Fatal(admission)
	}
	for index := 0; index < 3; index++ {
		record.ID = fmt.Sprintf("retained-%d", index)
		if err := exporter.persist([]Record{record}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	stamp := time.Now()
	for _, entry := range entries {
		names = append(names, entry.Name())
		if err := os.Chtimes(filepath.Join(dir, entry.Name()), stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(names)

	record.ID = "retained-new"
	if err := exporter.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, names[0])); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deterministic oldest/name eviction left %q: %v", names[0], err)
	}
	entries, err = os.ReadDir(dir)
	if err != nil || len(entries) != config.FallbackMaxFiles {
		t.Fatalf("bounded fallback entries = %d, %v", len(entries), err)
	}
	stats := exporter.Stats()
	if stats.FallbackEvictions != 1 || stats.FallbackEvictedBytes == 0 {
		t.Fatalf("eviction evidence = %+v", stats)
	}
}

func TestFallbackRetentionBoundsAggregateBytesAndAge(t *testing.T) {
	requireDurableFallback(t)
	probeDir := t.TempDir()
	base := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: probeDir, FallbackFileMax: 1 << 20,
		FallbackMaxFiles: 10, FallbackMaxBytes: 1 << 20, FallbackMaxAge: time.Hour,
	}
	probe, err := NewExporter(base, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := Admit(validEvent(), DestinationAnalytics, base.Policy)
	if err := probe.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	probeEntries, err := os.ReadDir(probeDir)
	if err != nil || len(probeEntries) != 1 {
		t.Fatalf("probe entries = %v, %v", probeEntries, err)
	}
	probeInfo, err := probeEntries[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	config := base
	config.FallbackDir = dir
	config.FallbackFileMax = probeInfo.Size() + 256
	config.FallbackMaxBytes = probeInfo.Size()*3 + 768
	exporter, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	for index := 0; index < 12; index++ {
		record.ID = fmt.Sprintf("byte-bound-%02d", index)
		if err := exporter.persist([]Record{record}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(0)
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			t.Fatal(infoErr)
		}
		total += info.Size()
	}
	if total > config.FallbackMaxBytes || len(entries) > config.FallbackMaxFiles {
		t.Fatalf("fallback retained %d bytes in %d files beyond %+v", total, len(entries), config)
	}
	oldest := entries[0].Name()
	old := time.Now().Add(-2 * config.FallbackMaxAge)
	if err := os.Chtimes(filepath.Join(dir, oldest), old, old); err != nil {
		t.Fatal(err)
	}
	record.ID = "age-prune"
	if err := exporter.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, oldest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired fallback remained: %v", err)
	}
}

func TestFallbackDrainRejectsAttackerSizedDirectoryEnumeration(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	for index := 0; index <= maximumFallbackScanItems; index++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("junk-%04d", index)), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	if err := exporter.DrainFallback(context.Background()); !errors.Is(err, errFallbackCapacity) {
		t.Fatalf("DrainFallback error = %v, want capacity failure", err)
	}
}

func TestFallbackRejectsDirectSymlinkWithoutWritingOrChmoddingTarget(t *testing.T) {
	requireDurableFallback(t)
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "fallback")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	config := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: link,
	}
	exporter, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	record, _ := Admit(validEvent(), DestinationAnalytics, config.Policy)
	if err := exporter.persist([]Record{record}); err == nil {
		t.Fatal("symlink fallback was accepted")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed to %o", info.Mode().Perm())
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != 0 {
		t.Fatalf("symlink target received fallback files: %v, %v", entries, err)
	}
}

func TestExporterConfigRejectsAllocationAndRetentionExtremes(t *testing.T) {
	base := func() ExporterConfig {
		return ExporterConfig{
			Destination: DestinationAnalytics,
			Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		}
	}
	for name, mutate := range map[string]func(*ExporterConfig){
		"queue count": func(config *ExporterConfig) { config.QueueCapacity = maximumQueueCapacity + 1 },
		"queue bytes": func(config *ExporterConfig) { config.QueueByteCapacity = maximumQueueBytes + 1 },
		"batch size":  func(config *ExporterConfig) { config.BatchSize = maximumBatchSize + 1 },
		"send timeout": func(config *ExporterConfig) {
			config.SendTimeout = maximumSendTimeout + time.Nanosecond
		},
		"fallback file": func(config *ExporterConfig) {
			config.FallbackFileMax = maximumFallbackFileBytes + 1
		},
		"fallback files": func(config *ExporterConfig) { config.FallbackMaxFiles = maximumFallbackFiles + 1 },
		"fallback bytes": func(config *ExporterConfig) { config.FallbackMaxBytes = maximumFallbackBytes + 1 },
		"attributes": func(config *ExporterConfig) {
			config.Policy.MaxAttributes = maximumPolicyAttributes + 1
		},
		"attribute value": func(config *ExporterConfig) {
			config.Policy.MaxValueRunes = maximumPolicyValueRunes + 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := base()
			mutate(&config)
			exporter, err := NewExporter(config, &testSink{})
			if err == nil {
				_ = exporter.Close(context.Background())
				t.Fatal("unsafe exporter configuration was accepted")
			}
		})
	}
}

func TestDurableFallbackAvailabilityFailsClosed(t *testing.T) {
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: filepath.Join(t.TempDir(), "fallback"),
	}, &testSink{})
	if !fallbackPlatformSupported() {
		if !errors.Is(err, errFallbackUnsupported) || exporter != nil {
			t.Fatalf("unsupported fallback construction = %v, %v", exporter, err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExporterQueueByteReservationIsBounded(t *testing.T) {
	exporter, err := NewExporter(ExporterConfig{
		Destination:       DestinationAnalytics,
		Policy:            Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueByteCapacity: 64,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	if !exporter.reserveQueueBytes(64) {
		t.Fatal("exact queue byte capacity was rejected")
	}
	if exporter.reserveQueueBytes(1) {
		t.Fatal("queue byte capacity overflow was accepted")
	}
	exporter.queuedBytes.Add(-64)
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestExporterRetainsByteReservationWhileRecordIsBatched(t *testing.T) {
	event := validEvent()
	record, admission := Admit(event, DestinationAnalytics, Policy{OptionalEnabled: true, ManagedAllowed: true})
	if admission.Status != AdmissionAccepted {
		t.Fatal(admission)
	}
	wire, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination:       DestinationAnalytics,
		Policy:            Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity:     2,
		QueueByteCapacity: int64(len(wire)),
		BatchSize:         2,
		FlushInterval:     time.Hour,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	if got := exporter.Observe(event); got.Queue != QueueEnqueued {
		t.Fatal(got)
	}
	deadline := time.Now().Add(time.Second)
	for len(exporter.queue) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("worker did not move record into its batch")
		}
		time.Sleep(time.Millisecond)
	}
	if got := exporter.Observe(event); got.Queue != QueueFull {
		t.Fatalf("batched byte reservation was released early: %+v", got)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queued := exporter.queuedBytes.Load(); queued != 0 {
		t.Fatalf("batch completion retained %d bytes", queued)
	}
}

func TestExporterShutdownDropAccountingReleasesQueueBytes(t *testing.T) {
	exporter := &Exporter{queue: make(chan queuedRecord, 1)}
	exporter.queue <- queuedRecord{record: Record{ID: "queued"}, bytes: 17}
	exporter.queuedBytes.Store(40)
	exporter.dropShutdownRecords([]queuedRecord{{record: Record{ID: "batched"}, bytes: 23}})
	exporter.dropQueuedRecords()
	if queued := exporter.queuedBytes.Load(); queued != 0 {
		t.Fatalf("shutdown retained %d queued bytes", queued)
	}
	if drops := exporter.Stats().FallbackDrops; drops != 2 {
		t.Fatalf("shutdown drop evidence = %d, want 2", drops)
	}
}

func TestFallbackPruningCannotBeDefeatedByOneOversizedInjectedFile(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	smallName := "000000000000000000000001.json"
	largeName := "000000000000000000000002.json"
	newestName := "000000000000000000000003.json"
	if err := os.WriteFile(filepath.Join(dir, smallName), make([]byte, 10), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, largeName), make([]byte, 80), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, newestName), make([]byte, 80), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(filepath.Join(dir, smallName), now.Add(-time.Minute), now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(filepath.Join(dir, largeName), now.Add(-time.Second), now.Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir, FallbackFileMax: 80, FallbackMaxFiles: 10,
		FallbackMaxBytes: 100, FallbackMaxAge: time.Hour,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	exporter.fallbackMu.Lock()
	owner, err := exporter.fallbackDirectoryLocked()
	if err == nil {
		err = exporter.pruneFallbackLocked(owner, 0, false, now)
	}
	exporter.fallbackMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != newestName {
		t.Fatalf("bounded retained entries = %v, %v", entries, err)
	}
	stats := exporter.Stats()
	if stats.FallbackEvictions != 2 || stats.FallbackEvictedBytes != 90 {
		t.Fatalf("aggregate eviction evidence = %+v", stats)
	}
}

func TestFallbackPrunesCrashOrphanTemporaryActivation(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	name := ".0123456789abcdef01234567.json.1123456789abcdef01234567.tmp"
	pathname := filepath.Join(dir, name)
	if err := os.WriteFile(pathname, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * maximumFallbackTempAge)
	if err := os.Chtimes(pathname, old, old); err != nil {
		t.Fatal(err)
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	if err := exporter.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(pathname); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan temporary fallback remained: %v", err)
	}
	stats := exporter.Stats()
	if stats.FallbackEvictions != 1 || stats.FallbackEvictedBytes != uint64(len("partial")) {
		t.Fatalf("orphan cleanup evidence = %+v", stats)
	}
}

func TestFallbackCapacityNeverEvictsFreshTemporaryActivation(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	name := ".0123456789abcdef01234567.json.1123456789abcdef01234567.tmp"
	pathname := filepath.Join(dir, name)
	if err := os.WriteFile(pathname, []byte("active"), 0o600); err != nil {
		t.Fatal(err)
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir, FallbackFileMax: 1 << 20,
		FallbackMaxFiles: 1, FallbackMaxBytes: 1 << 20,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	record, _ := Admit(validEvent(), DestinationAnalytics, exporter.config.Policy)
	if err := exporter.persist([]Record{record}); !errors.Is(err, errFallbackCapacity) {
		t.Fatalf("persist with protected temporary = %v, want capacity failure", err)
	}
	data, err := os.ReadFile(pathname)
	if err != nil || string(data) != "active" {
		t.Fatalf("fresh temporary activation was mutated: %q, %v", data, err)
	}
	if stats := exporter.Stats(); stats.FallbackEvictions != 0 {
		t.Fatalf("fresh temporary activation counted as evicted: %+v", stats)
	}
}

func TestSpoolDecoderRejectsAmbiguousAndUnboundedJSON(t *testing.T) {
	id := "0123456789abcdef01234567"
	record, admission := Admit(validEvent(), DestinationAnalytics, Policy{OptionalEnabled: true, ManagedAllowed: true})
	if admission.Status != AdmissionAccepted {
		t.Fatal(admission)
	}
	recordWire, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Format(time.RFC3339Nano)
	valid := fmt.Sprintf(
		`{"version":2,"batch_id":%q,"created_at":%q,"destination":"analytics","traffic":"optional","records":[%s]}`,
		id, created, recordWire,
	)
	if _, err := decodeSpoolEnvelope([]byte(valid)); err != nil {
		t.Fatalf("valid spool rejected: %v", err)
	}
	deep := strings.Repeat("[", maximumSpoolJSONDepth+2) + "0" + strings.Repeat("]", maximumSpoolJSONDepth+2)
	for name, wire := range map[string]string{
		"duplicate envelope": strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
		"duplicate record":   strings.Replace(valid, `"name":"tool.completed"`, `"name":"tool.completed","name":"tool.completed"`, 1),
		"unknown envelope":   strings.TrimSuffix(valid, "}") + `,"unknown":true}`,
		"unknown record":     strings.Replace(valid, `"source":"runtime"`, `"source":"runtime","unknown":true`, 1),
		"trailing value":     valid + `{}`,
		"excess depth":       strings.TrimSuffix(valid, "}") + `,"unknown":` + deep + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSpoolEnvelope([]byte(wire)); err == nil {
				t.Fatal("ambiguous fallback JSON was accepted")
			}
		})
	}
}

func TestFallbackDrainQuarantinesDuplicateJSONWithoutCallingSink(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	id := "0123456789abcdef01234567"
	record, _ := Admit(validEvent(), DestinationAnalytics, Policy{OptionalEnabled: true, ManagedAllowed: true})
	recordWire, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	wire := fmt.Sprintf(
		`{"version":2,"version":2,"batch_id":%q,"created_at":%q,"destination":"analytics","traffic":"optional","records":[%s]}`,
		id, time.Now().UTC().Format(time.RFC3339Nano), recordWire,
	)
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(wire), 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &testSink{}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	if err := exporter.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".corrupt" {
		t.Fatalf("corrupt fallback quarantine = %v, %v", entries, err)
	}
	if exporter.Stats().FallbackCorruptions != 1 {
		t.Fatalf("corrupt fallback evidence = %+v", exporter.Stats())
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.records) != 0 {
		t.Fatalf("corrupt fallback reached sink: %+v", sink.records)
	}
}

func TestFallbackDrainRejectsHardLinkedManagedEntry(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	first := filepath.Join(dir, "0123456789abcdef01234567.json")
	second := filepath.Join(dir, "1123456789abcdef01234567.json")
	if err := os.WriteFile(first, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	if err := exporter.DrainFallback(context.Background()); !errors.Is(err, errFallbackUnsafe) {
		t.Fatalf("hard-linked fallback error = %v", err)
	}
	for _, pathname := range []string{first, second} {
		if _, err := os.Lstat(pathname); err != nil {
			t.Fatalf("hard-linked evidence was mutated: %v", err)
		}
	}
}

func TestFallbackPinnedOwnerRejectsReplacementDirectoryWithoutWriting(t *testing.T) {
	requireDurableFallback(t)
	parent := t.TempDir()
	dir := filepath.Join(parent, "fallback")
	config := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}
	exporter, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	record, _ := Admit(validEvent(), DestinationAnalytics, config.Policy)
	if err := exporter.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := exporter.persist([]Record{record}); err == nil {
		t.Fatal("replacement fallback directory was accepted")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement directory received data: %v, %v", entries, err)
	}
	entries, err = os.ReadDir(moved)
	if err != nil || len(entries) != 1 {
		t.Fatalf("original fallback evidence changed: %v, %v", entries, err)
	}
}

func TestFallbackPinnedOwnerRejectsRelaxedDirectoryPermissions(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	config := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}
	exporter, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer exporter.Close(context.Background())
	record, _ := Admit(validEvent(), DestinationAnalytics, config.Policy)
	if err := exporter.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackDirectoryModePrivate(info) {
		t.Skip("platform permission bits do not express relaxed owner-only directory access")
	}
	if err := exporter.persist([]Record{record}); err == nil {
		t.Fatal("non-private fallback directory was accepted")
	}
	after, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("non-private fallback directory received data: %v", after)
	}
}

func TestFallbackNumericAttributesRetainExactIntegers(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	config := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}
	writer, err := NewExporter(config, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := Admit(validEvent(), DestinationAnalytics, config.Policy)
	record.Attributes["large_integer"] = int64(math.MaxInt64)
	if err := writer.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink := &testSink{}
	reader, err := NewExporter(config, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.records) != 1 || sink.records[0].Attributes["large_integer"] != int64(math.MaxInt64) {
		t.Fatalf("restored numeric attribute = %+v", sink.records)
	}
}

func TestFallbackRetryRechecksCurrentPrivacyPolicy(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	writerConfig := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}
	writer, err := NewExporter(writerConfig, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := Admit(validEvent(), DestinationAnalytics, writerConfig.Policy)
	if err := writer.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	sink := &testSink{}
	reader, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{ManagedAllowed: true},
		FallbackDir: dir,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.records) != 0 {
		t.Fatalf("opted-out fallback reached sink: %+v", sink.records)
	}
	if stats := reader.Stats(); stats.FallbackDrops != 1 || stats.FallbackCorruptions != 0 {
		t.Fatalf("privacy drop evidence = %+v", stats)
	}
}

func TestFallbackCurrentLimitsDoNotMisclassifyPriorValidWireAsCorrupt(t *testing.T) {
	requireDurableFallback(t)
	dir := t.TempDir()
	writerConfig := ExporterConfig{
		Destination: DestinationAnalytics,
		Policy: Policy{
			OptionalEnabled: true, ManagedAllowed: true,
			MaxAttributes: maximumPolicyAttributes, MaxValueRunes: maximumPolicyValueRunes,
		},
		FallbackDir: dir,
	}
	writer, err := NewExporter(writerConfig, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	record, _ := Admit(validEvent(), DestinationAnalytics, writerConfig.Policy)
	for index := 0; index < DefaultMaxAttributes+1; index++ {
		record.Attributes[fmt.Sprintf("attribute_%03d", index)] = "value"
	}
	if err := writer.persist([]Record{record}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reader, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: dir,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.DrainFallback(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := reader.Stats(); stats.FallbackDrops != 1 || stats.FallbackCorruptions != 0 {
		t.Fatalf("current-limit fallback classification = %+v", stats)
	}
}

func TestExporterNilContextsAreSafe(t *testing.T) {
	requireDurableFallback(t)
	if err := safeExport(nil, &testSink{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := safeShutdown(nil, &testSink{}); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(t.TempDir(), "fallback")
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
		FallbackDir: fallback,
	}, &testSink{})
	if err != nil {
		t.Fatal(err)
	}
	if err := exporter.DrainFallback(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(fallback); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty drain materialized fallback directory: %v", err)
	}
	if err := exporter.Close(nil); err != nil {
		t.Fatal(err)
	}
}

func TestExporterCloseBoundsBlockingSinkAndDoesNotFanOutCallbacks(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	sink := sinkFuncs{
		export: func(context.Context, []Record) error {
			if calls.Add(1) == 1 {
				close(started)
			}
			<-release
			return nil
		},
	}
	exporter, err := NewExporter(ExporterConfig{
		Destination:   DestinationAnalytics,
		Policy:        Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity: 16, BatchSize: 1, SendTimeout: 20 * time.Millisecond,
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if got := exporter.Observe(validEvent()); got.Queue != QueueEnqueued {
		t.Fatal(got)
	}
	<-started
	for index := 0; index < 8; index++ {
		event := validEvent()
		event.ID = fmt.Sprintf("blocked-%d", index)
		exporter.Observe(event)
	}
	time.Sleep(80 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocking sink fanned out to %d callback goroutines", got)
	}
	start := time.Now()
	closeErr := exporter.Close(context.Background())
	if closeErr != nil && !errors.Is(closeErr, context.DeadlineExceeded) {
		t.Fatalf("Close error = %v", closeErr)
	}
	if elapsed := time.Since(start); elapsed > DefaultShutdownTimeout+250*time.Millisecond {
		t.Fatalf("Close blocked for %v", elapsed)
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		if err := exporter.Close(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exporter worker did not terminate after hostile sink release")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if queued := exporter.queuedBytes.Load(); queued != 0 {
		t.Fatalf("queued byte reservation leaked after shutdown: %d", queued)
	}
	if exporter.Stats().FallbackDrops == 0 {
		t.Fatalf("shutdown drops lacked evidence: %+v", exporter.Stats())
	}
}

func TestExporterCloseBoundsBlockingShutdownCallback(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	sink := sinkFuncs{shutdown: func(context.Context) error {
		close(started)
		<-release
		return nil
	}}
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- exporter.Close(ctx) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("shutdown callback did not start")
	}
	select {
	case closeErr := <-result:
		if closeErr != nil && !errors.Is(closeErr, context.DeadlineExceeded) {
			t.Fatalf("Close error = %v", closeErr)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Close did not bound hostile shutdown")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for {
		if err := exporter.Close(context.Background()); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("exporter worker did not terminate after shutdown release")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestExporterBoundsBlockingFallbackPersistenceAndDoesNotFanOut(t *testing.T) {
	requireDurableFallback(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int64
	exporter, err := NewExporter(ExporterConfig{
		Destination:   DestinationAnalytics,
		Policy:        Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity: 8, BatchSize: 1, SendTimeout: 25 * time.Millisecond,
		FallbackDir: filepath.Join(t.TempDir(), "fallback"),
	}, &testSink{fail: errors.New("offline")})
	if err != nil {
		t.Fatal(err)
	}
	exporter.persistBatch = func([]Record) error {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return nil
	}
	for index := 0; index < 4; index++ {
		event := validEvent()
		event.ID = fmt.Sprintf("blocked-fallback-%d", index)
		if got := exporter.Observe(event); got.Queue != QueueEnqueued {
			t.Fatal(got)
		}
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fallback persistence did not start")
	}
	deadline := time.Now().Add(time.Second)
	for exporter.Stats().PersistenceFailures < 4 {
		if time.Now().After(deadline) {
			t.Fatalf("fallback persistence did not time out: %+v", exporter.Stats())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("blocking fallback fanned out to %d callback goroutines", got)
	}
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if queued := exporter.queuedBytes.Load(); queued != 0 {
		t.Fatalf("blocking fallback retained %d queued bytes", queued)
	}
	close(release)
}

func TestExporterContainsSinkPanic(t *testing.T) {
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics, Policy: Policy{OptionalEnabled: true, ManagedAllowed: true},
		QueueCapacity: 1, BatchSize: 1,
	}, &testSink{panicNow: true})
	if err != nil {
		t.Fatal(err)
	}
	exporter.Observe(validEvent())
	if err := exporter.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exporter.Stats().ExportFailures != 1 {
		t.Fatalf("panic was not contained: %+v", exporter.Stats())
	}
}

func TestSafeSinkOperationsNeverExposeProviderFailures(t *testing.T) {
	const secret = "sink-secret-must-not-escape"
	raw := errors.New(secret)
	if got := safeExport(context.Background(), sinkFuncs{
		export: func(context.Context, []Record) error { return raw },
	}, nil); got != errSinkExportFailed || got == raw || contains(got.Error(), secret) {
		t.Fatalf("safeExport(raw) = %T %q", got, got)
	}
	if got := safeShutdown(context.Background(), sinkFuncs{
		shutdown: func(context.Context) error { return raw },
	}); got != errSinkShutdownFailed || got == raw || contains(got.Error(), secret) {
		t.Fatalf("safeShutdown(raw) = %T %q", got, got)
	}

	if got := safeExport(context.Background(), sinkFuncs{
		export: func(context.Context, []Record) error { panic(secret) },
	}, nil); got != errSinkPanicked || contains(got.Error(), secret) {
		t.Fatalf("safeExport(panic) = %T %q", got, got)
	}
	if got := safeShutdown(context.Background(), sinkFuncs{
		shutdown: func(context.Context) error { panic(errors.New(secret)) },
	}); got != errSinkPanicked || contains(got.Error(), secret) {
		t.Fatalf("safeShutdown(panic) = %T %q", got, got)
	}
}

func TestSafeSinkOperationsBoundHostileErrorGraphs(t *testing.T) {
	wide := &wideSinkError{children: make([]error, 10_000)}
	for index := range wide.children {
		wide.children[index] = errors.New("untrusted")
	}
	wide.children[len(wide.children)-1] = context.DeadlineExceeded

	for _, hostile := range []error{
		&cyclicSinkError{},
		&panickingSinkUnwrapError{},
		&panickingSinkIsError{},
		&panickingSinkAsError{},
		wide,
	} {
		sink := sinkFuncs{
			export:   func(context.Context, []Record) error { return hostile },
			shutdown: func(context.Context) error { return hostile },
		}
		if got := safeExport(context.Background(), sink, nil); got != errSinkExportFailed {
			t.Fatalf("safeExport(%T) = %T %q", hostile, got, got)
		}
		if got := safeShutdown(context.Background(), sink); got != errSinkShutdownFailed {
			t.Fatalf("safeShutdown(%T) = %T %q", hostile, got, got)
		}
	}
}

func TestSafeSinkOperationsPreserveOnlyContextCategories(t *testing.T) {
	for name, test := range map[string]struct {
		source error
		want   error
	}{
		"cancelled":       {source: context.Canceled, want: context.Canceled},
		"deadline":        {source: context.DeadlineExceeded, want: context.DeadlineExceeded},
		"wrapped opaque":  {source: fmt.Errorf("private wrapper: %w", context.Canceled), want: errSinkExportFailed},
		"joined opaque":   {source: errors.Join(errors.New("private"), context.DeadlineExceeded), want: errSinkExportFailed},
		"ordinary opaque": {source: errors.New("private"), want: errSinkExportFailed},
	} {
		t.Run(name, func(t *testing.T) {
			sink := sinkFuncs{
				export:   func(context.Context, []Record) error { return test.source },
				shutdown: func(context.Context) error { return test.source },
			}
			if got := safeExport(context.Background(), sink, nil); got != test.want {
				t.Fatalf("safeExport() = %T %q, want %q", got, got, test.want)
			}
			shutdownWant := test.want
			if shutdownWant == errSinkExportFailed {
				shutdownWant = errSinkShutdownFailed
			}
			if got := safeShutdown(context.Background(), sink); got != shutdownWant {
				t.Fatalf("safeShutdown() = %T %q, want %q", got, got, shutdownWant)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	ordinary := sinkFuncs{
		export:   func(context.Context, []Record) error { return errors.New("private") },
		shutdown: func(context.Context) error { return errors.New("private") },
	}
	if got := safeExport(cancelled, ordinary, nil); got != context.Canceled {
		t.Fatalf("safeExport() lost owned cancellation state: %v", got)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if got := safeShutdown(deadline, ordinary); got != context.DeadlineExceeded {
		t.Fatalf("safeShutdown() lost owned deadline state: %v", got)
	}
}

func TestExporterCloseDoesNotInvokeBlockingForeignUnwrap(t *testing.T) {
	cause := &blockingSinkUnwrapError{
		called:  make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(cause.release)
	exporter, err := NewExporter(ExporterConfig{
		Destination: DestinationAnalytics,
		Policy:      Policy{OptionalEnabled: true, ManagedAllowed: true},
	}, sinkFuncs{shutdown: func(context.Context) error { return cause }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- exporter.Close(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Exporter.Close() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Exporter.Close blocked in foreign Unwrap")
	}
	select {
	case <-cause.called:
		t.Fatal("Exporter.Close invoked foreign Unwrap")
	default:
	}
}

func contains(value, substring string) bool {
	for index := 0; index+len(substring) <= len(value); index++ {
		if value[index:index+len(substring)] == substring {
			return true
		}
	}
	return false
}

func FuzzDecodeSpoolEnvelopeNeverPanics(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"version":1,"version":2}`))
	f.Add([]byte(strings.Repeat("[", maximumSpoolJSONDepth+2) + "0" + strings.Repeat("]", maximumSpoolJSONDepth+2)))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = decodeSpoolEnvelope(data)
	})
}
