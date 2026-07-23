package observability

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
)

const (
	DefaultQueueCapacity     = 8192
	DefaultQueueByteCapacity = 64 << 20
	DefaultBatchSize         = 200
	DefaultFlushInterval     = 10 * time.Second
	DefaultSendTimeout       = 10 * time.Second
	DefaultShutdownTimeout   = 500 * time.Millisecond
	DefaultFallbackFileMax   = 2 << 20
	DefaultFallbackMaxFiles  = 256
	DefaultFallbackMaxBytes  = 64 << 20
	DefaultFallbackMaxAge    = 7 * 24 * time.Hour
	maximumQueueCapacity     = 65_536
	maximumQueueBytes        = 256 << 20
	maximumBatchSize         = 1_000
	maximumSendTimeout       = time.Minute
	maximumFallbackFileBytes = 16 << 20
	maximumFallbackFiles     = 512
	maximumFallbackBytes     = 1 << 30
	maximumFallbackScanItems = 1024
	maximumFallbackTempAge   = time.Hour
	maximumSpoolJSONDepth    = 128
	maximumPolicyAttributes  = 256
	maximumPolicyValueRunes  = 4096
	spoolVersion             = 2
)

var (
	errSinkExportFailed    = errors.New("observability sink export failed")
	errSinkShutdownFailed  = errors.New("observability sink shutdown failed")
	errSinkPanicked        = errors.New("observability sink panicked")
	errFallbackCapacity    = errors.New("observability fallback capacity is unavailable")
	errFallbackCorrupt     = errors.New("observability fallback entry is corrupt")
	errFallbackUnsafe      = errors.New("observability fallback entry is unsafe")
	errFallbackWriteFailed = errors.New("observability fallback write failed")
	errFallbackUnsupported = errors.New("observability durable fallback is unsupported on this platform")
	errDuplicateJSON       = errors.New("observability fallback JSON contains a duplicate member")
)

// Sink receives only admitted Record values. Its failure is contained by the
// exporter and is never returned through Observe.
type Sink interface {
	Export(context.Context, []Record) error
	Shutdown(context.Context) error
}

type ExporterConfig struct {
	Destination       Destination
	Policy            Policy
	QueueCapacity     int
	QueueByteCapacity int64
	BatchSize         int
	FlushInterval     time.Duration
	SendTimeout       time.Duration
	FallbackDir       string
	FallbackFileMax   int64
	FallbackMaxFiles  int
	FallbackMaxBytes  int64
	FallbackMaxAge    time.Duration
}

func (c ExporterConfig) normalized() (ExporterConfig, error) {
	if c.Destination != DestinationLocal && c.Destination != DestinationAnalytics && c.Destination != DestinationEssential {
		return c, errors.New("invalid observability destination")
	}
	if c.QueueCapacity <= 0 {
		c.QueueCapacity = DefaultQueueCapacity
	}
	if c.QueueCapacity > maximumQueueCapacity {
		return c, errors.New("observability queue capacity exceeds its limit")
	}
	if c.QueueByteCapacity <= 0 {
		c.QueueByteCapacity = DefaultQueueByteCapacity
	}
	if c.QueueByteCapacity > maximumQueueBytes {
		return c, errors.New("observability queue byte capacity exceeds its limit")
	}
	if c.BatchSize <= 0 {
		c.BatchSize = DefaultBatchSize
	}
	if c.BatchSize > maximumBatchSize {
		return c, errors.New("observability batch size exceeds its limit")
	}
	if c.BatchSize > c.QueueCapacity {
		c.BatchSize = c.QueueCapacity
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = DefaultFlushInterval
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.SendTimeout > maximumSendTimeout {
		return c, errors.New("observability send timeout exceeds its limit")
	}
	if c.FallbackFileMax <= 0 {
		c.FallbackFileMax = DefaultFallbackFileMax
	}
	if c.FallbackFileMax > maximumFallbackFileBytes {
		return c, errors.New("observability fallback file bound exceeds its limit")
	}
	if c.FallbackMaxFiles <= 0 {
		c.FallbackMaxFiles = DefaultFallbackMaxFiles
	}
	if c.FallbackMaxFiles > maximumFallbackFiles {
		return c, errors.New("observability fallback file count exceeds its limit")
	}
	if c.FallbackMaxBytes <= 0 {
		c.FallbackMaxBytes = DefaultFallbackMaxBytes
	}
	if c.FallbackMaxBytes > maximumFallbackBytes {
		return c, errors.New("observability fallback byte capacity exceeds its limit")
	}
	if c.FallbackFileMax > c.FallbackMaxBytes {
		return c, errors.New("observability fallback file bound exceeds total capacity")
	}
	if c.FallbackMaxAge < 0 {
		return c, errors.New("observability fallback age must not be negative")
	}
	if c.FallbackMaxAge == 0 {
		c.FallbackMaxAge = DefaultFallbackMaxAge
	}
	c.Policy = c.Policy.normalized()
	if c.Policy.MaxAttributes > maximumPolicyAttributes {
		return c, errors.New("observability attribute count exceeds its limit")
	}
	if c.Policy.MaxValueRunes > maximumPolicyValueRunes {
		return c, errors.New("observability attribute value bound exceeds its limit")
	}
	return c, nil
}

type QueueStatus string

const (
	QueueEnqueued QueueStatus = "enqueued"
	QueueFiltered QueueStatus = "filtered"
	QueueInvalid  QueueStatus = "invalid"
	QueueFull     QueueStatus = "queue_full"
	QueueClosed   QueueStatus = "closed"
)

type ObservationResult struct {
	Queue     QueueStatus `json:"queue"`
	Admission Admission   `json:"admission"`
}

type ExporterStats struct {
	Accepted             uint64 `json:"accepted"`
	Filtered             uint64 `json:"filtered"`
	Invalid              uint64 `json:"invalid"`
	DroppedQueue         uint64 `json:"dropped_queue"`
	DroppedClosed        uint64 `json:"dropped_closed"`
	Exported             uint64 `json:"exported"`
	ExportFailures       uint64 `json:"export_failures"`
	Persisted            uint64 `json:"persisted"`
	PersistenceFailures  uint64 `json:"persistence_failures"`
	FallbackEvictions    uint64 `json:"fallback_evictions"`
	FallbackEvictedBytes uint64 `json:"fallback_evicted_bytes"`
	FallbackDrops        uint64 `json:"fallback_drops"`
	FallbackCorruptions  uint64 `json:"fallback_corruptions"`
	ShutdownFailures     uint64 `json:"shutdown_failures"`
}

type exporterCounters struct {
	accepted, filtered, invalid, droppedQueue, droppedClosed atomic.Uint64
	exported, exportFailures, persisted, persistenceFailures atomic.Uint64
	fallbackEvictions, fallbackEvictedBytes, fallbackDrops   atomic.Uint64
	fallbackCorruptions                                      atomic.Uint64
	shutdownFailures                                         atomic.Uint64
}

type queuedRecord struct {
	record Record
	bytes  int64
}

// Exporter is a bounded best-effort asynchronous observation queue. The
// producer path is nonblocking after privacy admission.
type Exporter struct {
	config ExporterConfig
	sink   *sinkBoundary
	queue  chan queuedRecord
	stop   chan struct{}
	done   chan struct{}
	runCtx context.Context
	cancel context.CancelFunc

	mu          sync.RWMutex
	closed      bool
	once        sync.Once
	stats       exporterCounters
	queuedBytes atomic.Int64
	shutdownAt  atomic.Int64

	fallbackMu    sync.Mutex
	fallbackOwner *platform.OwnedDirectory
	fallbackGate  chan struct{}
	persistBatch  func([]Record) error
}

func NewExporter(config ExporterConfig, sink Sink) (*Exporter, error) {
	if sink == nil {
		return nil, errors.New("observability sink is nil")
	}
	config, err := config.normalized()
	if err != nil {
		return nil, err
	}
	if config.FallbackDir != "" && !fallbackPlatformSupported() {
		return nil, errFallbackUnsupported
	}
	runCtx, cancel := context.WithCancel(context.Background())
	exporter := &Exporter{
		config: config, sink: newSinkBoundary(sink), queue: make(chan queuedRecord, config.QueueCapacity),
		stop: make(chan struct{}), done: make(chan struct{}), runCtx: runCtx, cancel: cancel,
		fallbackGate: make(chan struct{}, 1),
	}
	exporter.persistBatch = exporter.persist
	go exporter.run()
	return exporter, nil
}

// Observe never returns a sink error. Queue pressure produces explicit local
// drop evidence and leaves the semantic operation unchanged.
func (e *Exporter) Observe(event Event) ObservationResult {
	if !e.config.Policy.EnabledFor(event.Traffic, e.config.Destination) {
		e.stats.filtered.Add(1)
		return ObservationResult{Queue: QueueFiltered, Admission: Admission{Status: AdmissionFiltered, Reason: "traffic disabled by privacy or managed policy"}}
	}
	record, admission := Admit(event, e.config.Destination, e.config.Policy)
	if admission.Status == AdmissionFiltered {
		e.stats.filtered.Add(1)
		return ObservationResult{Queue: QueueFiltered, Admission: admission}
	}
	if admission.Status == AdmissionInvalid {
		e.stats.invalid.Add(1)
		return ObservationResult{Queue: QueueInvalid, Admission: admission}
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		e.stats.invalid.Add(1)
		return ObservationResult{Queue: QueueInvalid, Admission: Admission{Status: AdmissionInvalid, Reason: "record cannot be bounded"}}
	}
	size := int64(len(encoded))
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.closed {
		e.stats.droppedClosed.Add(1)
		return ObservationResult{Queue: QueueClosed, Admission: admission}
	}
	if !e.reserveQueueBytes(size) {
		e.stats.droppedQueue.Add(1)
		return ObservationResult{Queue: QueueFull, Admission: admission}
	}
	select {
	case e.queue <- queuedRecord{record: cloneRecord(record), bytes: size}:
		e.stats.accepted.Add(1)
		return ObservationResult{Queue: QueueEnqueued, Admission: admission}
	default:
		e.queuedBytes.Add(-size)
		e.stats.droppedQueue.Add(1)
		return ObservationResult{Queue: QueueFull, Admission: admission}
	}
}

func (e *Exporter) reserveQueueBytes(size int64) bool {
	if size < 0 || size > e.config.QueueByteCapacity {
		return false
	}
	for {
		current := e.queuedBytes.Load()
		if current > e.config.QueueByteCapacity-size {
			return false
		}
		if e.queuedBytes.CompareAndSwap(current, current+size) {
			return true
		}
	}
}

func (e *Exporter) run() {
	defer close(e.done)
	ticker := time.NewTicker(e.config.FlushInterval)
	defer ticker.Stop()
	batch := make([]queuedRecord, 0, e.config.BatchSize)
	flush := func(ctx context.Context) {
		if len(batch) == 0 {
			return
		}
		toSend := make([]Record, len(batch))
		reservedBytes := int64(0)
		for index := range batch {
			toSend[index] = batch[index].record
			reservedBytes += batch[index].bytes
		}
		batch = batch[:0]
		e.exportBatch(ctx, toSend)
		e.queuedBytes.Add(-reservedBytes)
	}
running:
	for {
		select {
		case <-e.stop:
			break running
		default:
		}
		select {
		case queued := <-e.queue:
			batch = append(batch, queued)
			if len(batch) >= e.config.BatchSize {
				flush(e.runCtx)
			}
		case <-ticker.C:
			flush(e.runCtx)
		case <-e.stop:
			break running
		}
	}
	shutdownCtx, shutdownCancel := e.shutdownContext()
	defer shutdownCancel()
	for {
		if shutdownCtx.Err() != nil {
			e.dropShutdownRecords(batch)
			e.dropQueuedRecords()
			e.recordShutdown(e.sink.shutdown(shutdownCtx))
			return
		}
		select {
		case queued := <-e.queue:
			batch = append(batch, queued)
			if len(batch) >= e.config.BatchSize {
				flush(shutdownCtx)
			}
		default:
			flush(shutdownCtx)
			e.recordShutdown(e.sink.shutdown(shutdownCtx))
			return
		}
	}
}

func (e *Exporter) recordShutdown(err error) {
	if err != nil {
		e.stats.shutdownFailures.Add(1)
	}
}

func (e *Exporter) shutdownContext() (context.Context, context.CancelFunc) {
	deadlineNanos := e.shutdownAt.Load()
	if deadlineNanos <= 0 {
		return context.WithTimeout(context.Background(), DefaultShutdownTimeout)
	}
	return context.WithDeadline(context.Background(), time.Unix(0, deadlineNanos))
}

func (e *Exporter) dropShutdownRecords(batch []queuedRecord) {
	if len(batch) == 0 {
		return
	}
	e.stats.fallbackDrops.Add(uint64(len(batch)))
	for _, queued := range batch {
		e.queuedBytes.Add(-queued.bytes)
	}
}

func (e *Exporter) dropQueuedRecords() {
	for {
		select {
		case queued := <-e.queue:
			e.queuedBytes.Add(-queued.bytes)
			e.stats.fallbackDrops.Add(1)
		default:
			return
		}
	}
}

func (e *Exporter) exportBatch(parent context.Context, batch []Record) {
	ctx, cancel := context.WithTimeout(nonNilContext(parent), e.config.SendTimeout)
	defer cancel()
	err := e.sink.export(ctx, batch)
	if err == nil {
		e.stats.exported.Add(uint64(len(batch)))
		return
	}
	e.stats.exportFailures.Add(1)
	if ctx.Err() != nil || nonNilContext(parent).Err() != nil {
		e.stats.fallbackDrops.Add(uint64(len(batch)))
		return
	}
	if e.config.FallbackDir == "" {
		e.stats.fallbackDrops.Add(uint64(len(batch)))
		return
	}
	if persistErr := e.persistWithContext(ctx, batch); persistErr != nil {
		e.stats.persistenceFailures.Add(1)
		e.stats.fallbackDrops.Add(uint64(len(batch)))
		return
	}
	e.stats.persisted.Add(uint64(len(batch)))
}

func (e *Exporter) persistWithContext(ctx context.Context, batch []Record) error {
	ctx = nonNilContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case e.fallbackGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	result := make(chan error, 1)
	go func() {
		defer func() { <-e.fallbackGate }()
		result <- invokeFallbackWrite(func() error { return e.persistBatch(batch) })
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func invokeFallbackWrite(callback func() error) (result error) {
	defer func() {
		if recover() != nil {
			result = errFallbackWriteFailed
		}
	}()
	return callback()
}

// Close is idempotent. Context expiration only stops this waiter; it does not
// turn lost analytics into a semantic failure or abandon the worker silently.
func (e *Exporter) Close(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	select {
	case <-e.done:
		e.cancel()
		return nil
	default:
	}
	e.once.Do(func() {
		deadline := time.Now().Add(DefaultShutdownTimeout)
		if callerDeadline, ok := ctx.Deadline(); ok && callerDeadline.Before(deadline) {
			deadline = callerDeadline
		}
		e.shutdownAt.Store(deadline.UnixNano())
		e.mu.Lock()
		e.closed = true
		close(e.stop)
		e.mu.Unlock()
		time.AfterFunc(max(time.Until(deadline), 0), e.cancel)
	})
	deadlineNanos := e.shutdownAt.Load()
	waitCtx, cancel := context.WithDeadline(ctx, time.Unix(0, deadlineNanos))
	defer cancel()
	select {
	case <-e.done:
		e.cancel()
		return nil
	case <-waitCtx.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.DeadlineExceeded
	}
}

func (e *Exporter) Stats() ExporterStats {
	return ExporterStats{
		Accepted: e.stats.accepted.Load(), Filtered: e.stats.filtered.Load(), Invalid: e.stats.invalid.Load(),
		DroppedQueue: e.stats.droppedQueue.Load(), DroppedClosed: e.stats.droppedClosed.Load(), Exported: e.stats.exported.Load(),
		ExportFailures: e.stats.exportFailures.Load(), Persisted: e.stats.persisted.Load(), PersistenceFailures: e.stats.persistenceFailures.Load(),
		FallbackEvictions: e.stats.fallbackEvictions.Load(), FallbackEvictedBytes: e.stats.fallbackEvictedBytes.Load(),
		FallbackDrops: e.stats.fallbackDrops.Load(), FallbackCorruptions: e.stats.fallbackCorruptions.Load(),
		ShutdownFailures: e.stats.shutdownFailures.Load(),
	}
}

type spoolEnvelope struct {
	Version     int          `json:"version"`
	BatchID     string       `json:"batch_id"`
	CreatedAt   time.Time    `json:"created_at"`
	Destination Destination  `json:"destination"`
	Traffic     TrafficClass `json:"traffic"`
	Records     []Record     `json:"records"`
}

func (e *Exporter) persist(records []Record) error {
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	id := hex.EncodeToString(random)
	traffic := TrafficOptional
	if e.config.Destination == DestinationEssential {
		traffic = TrafficEssential
	}
	envelope := spoolEnvelope{
		Version: spoolVersion, BatchID: id, CreatedAt: time.Now().UTC(),
		Destination: e.config.Destination, Traffic: traffic, Records: cloneRecords(records),
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	if int64(len(data)) > e.config.FallbackFileMax {
		return errors.New("fallback batch exceeds file bound")
	}
	e.fallbackMu.Lock()
	defer e.fallbackMu.Unlock()
	owner, err := e.fallbackDirectoryLocked()
	if err != nil {
		return err
	}
	if err := e.pruneFallbackLocked(owner, int64(len(data)), true, time.Now()); err != nil {
		return err
	}
	return writeFallbackLocked(owner, id+".json", data)
}

type fallbackDiskEntry struct {
	name      string
	size      int64
	modified  time.Time
	identity  os.FileInfo
	temporary bool
	protected bool
}

type fallbackHandle struct {
	owner     *platform.OwnedDirectory
	root      *os.Root
	directory *os.File
}

func openFallbackHandle(owner *platform.OwnedDirectory) (*fallbackHandle, error) {
	if err := owner.Verify(); err != nil {
		return nil, err
	}
	before, err := os.Lstat(owner.Path())
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(owner.Path())
	if err != nil {
		return nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !fallbackDirectoryModePrivate(opened) || !os.SameFile(before, opened) {
		_ = directory.Close()
		_ = root.Close()
		return nil, errors.New("fallback directory changed while opening")
	}
	rooted, err := root.Stat(".")
	if err != nil || !rooted.IsDir() || !fallbackDirectoryModePrivate(rooted) || !os.SameFile(opened, rooted) {
		_ = directory.Close()
		_ = root.Close()
		return nil, errors.New("fallback directory root identity is inconsistent")
	}
	if err := owner.Verify(); err != nil {
		_ = directory.Close()
		_ = root.Close()
		return nil, err
	}
	return &fallbackHandle{owner: owner, root: root, directory: directory}, nil
}

func (handle *fallbackHandle) close() error {
	if handle == nil {
		return nil
	}
	return errors.Join(handle.directory.Close(), handle.root.Close())
}

func (handle *fallbackHandle) verify() error {
	if handle == nil || handle.owner == nil || handle.root == nil || handle.directory == nil {
		return errors.New("fallback directory handle is incomplete")
	}
	if err := handle.owner.Verify(); err != nil {
		return err
	}
	opened, err := handle.directory.Stat()
	if err != nil || !opened.IsDir() || !fallbackDirectoryModePrivate(opened) {
		return errors.New("fallback directory handle is no longer valid")
	}
	rooted, err := handle.root.Stat(".")
	if err != nil || !rooted.IsDir() || !fallbackDirectoryModePrivate(rooted) || !os.SameFile(opened, rooted) {
		return errors.New("fallback directory root identity changed")
	}
	return nil
}

func writeFallbackLocked(owner *platform.OwnedDirectory, name string, data []byte) (resultErr error) {
	if !managedFallbackName(name) || filepath.Ext(name) != ".json" {
		return errors.New("fallback batch name is invalid")
	}
	handle, err := openFallbackHandle(owner)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.close()) }()
	if _, err := handle.root.Lstat(name); err == nil {
		return errors.New("fallback batch identity already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	temporary := "." + name + "." + hex.EncodeToString(token) + ".tmp"
	file, err := handle.root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			resultErr = errors.Join(resultErr, handle.root.Remove(temporary))
		}
	}()
	if err := writeFallbackAll(file, data); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Chmod(0o600); err != nil {
		return errors.Join(err, file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(err, file.Close())
	}
	written, err := file.Stat()
	if err != nil {
		return errors.Join(err, file.Close())
	}
	links, err := fallbackFileLinkCount(file, written)
	if err != nil || links != 1 {
		return errors.Join(errFallbackUnsafe, file.Close())
	}
	if err := file.Close(); err != nil {
		return err
	}
	if !written.Mode().IsRegular() || !fallbackFileModePrivate(written) || written.Size() != int64(len(data)) {
		return errors.New("fallback batch write did not produce the expected file")
	}
	if err := handle.root.Rename(temporary, name); err != nil {
		return err
	}
	removeTemporary = false
	activated, err := handle.root.Lstat(name)
	if err != nil || !activated.Mode().IsRegular() || !os.SameFile(written, activated) {
		return errors.New("fallback batch changed while being activated")
	}
	activatedEntry := fallbackDiskEntry{
		name: name, size: activated.Size(), modified: activated.ModTime(), identity: activated,
	}
	if _, err := verifyFallbackEntry(handle, activatedEntry); err != nil {
		return err
	}
	if err := syncFallbackDirectory(handle.directory); err != nil {
		return err
	}
	return handle.verify()
}

func writeFallbackAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func (e *Exporter) fallbackDirectoryLocked() (*platform.OwnedDirectory, error) {
	if e.fallbackOwner != nil {
		if err := e.fallbackOwner.Verify(); err != nil {
			return nil, err
		}
		return e.fallbackOwner, nil
	}
	owner, err := platform.AcquirePrivateDirectory(e.config.FallbackDir)
	if err != nil {
		return nil, err
	}
	e.fallbackOwner = owner
	return owner, nil
}

func (e *Exporter) scanFallbackLocked(owner *platform.OwnedDirectory) ([]fallbackDiskEntry, error) {
	handle, err := openFallbackHandle(owner)
	if err != nil {
		return nil, err
	}
	entries, readErr := handle.directory.ReadDir(maximumFallbackScanItems + 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, handle.close())
	}
	if len(entries) > maximumFallbackScanItems {
		return nil, errors.Join(errFallbackCapacity, handle.close())
	}
	result := make([]fallbackDiskEntry, 0, min(len(entries), e.config.FallbackMaxFiles))
	for _, entry := range entries {
		temporary := managedFallbackTemporaryName(entry.Name())
		if !temporary && !managedFallbackName(entry.Name()) {
			continue
		}
		info, inspectErr := inspectManagedFallback(handle, entry)
		if inspectErr != nil {
			return nil, errors.Join(inspectErr, handle.close())
		}
		result = append(result, fallbackDiskEntry{
			name: entry.Name(), size: info.Size(), modified: info.ModTime(), identity: info, temporary: temporary,
		})
	}
	if err := handle.verify(); err != nil {
		return nil, errors.Join(err, handle.close())
	}
	if err := handle.close(); err != nil {
		return nil, err
	}
	if err := owner.Verify(); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].modified.Equal(result[j].modified) {
			return result[i].modified.Before(result[j].modified)
		}
		return result[i].name < result[j].name
	})
	return result, nil
}

func inspectManagedFallback(handle *fallbackHandle, entry os.DirEntry) (result os.FileInfo, resultErr error) {
	if entry.Type()&os.ModeSymlink != 0 {
		return nil, errFallbackUnsafe
	}
	info, err := entry.Info()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || !fallbackFileModePrivate(info) {
		return nil, errFallbackUnsafe
	}
	file, err := handle.root.Open(entry.Name())
	if err != nil {
		return nil, errFallbackUnsafe
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || !fallbackFileModePrivate(opened) {
		return nil, errFallbackUnsafe
	}
	links, err := fallbackFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return nil, errFallbackUnsafe
	}
	confirmed, err := handle.root.Lstat(entry.Name())
	if err != nil || !confirmed.Mode().IsRegular() || !fallbackFileModePrivate(confirmed) ||
		confirmed.Size() != opened.Size() || !confirmed.ModTime().Equal(opened.ModTime()) ||
		!os.SameFile(opened, confirmed) {
		return nil, errFallbackUnsafe
	}
	return confirmed, nil
}

func verifyFallbackEntry(handle *fallbackHandle, entry fallbackDiskEntry) (result os.FileInfo, resultErr error) {
	current, err := handle.root.Lstat(entry.name)
	if err != nil || !current.Mode().IsRegular() || !fallbackFileModePrivate(current) ||
		current.Size() != entry.size || !current.ModTime().Equal(entry.modified) ||
		!os.SameFile(entry.identity, current) {
		return nil, errFallbackUnsafe
	}
	file, err := handle.root.Open(entry.name)
	if err != nil {
		return nil, errFallbackUnsafe
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !fallbackFileModePrivate(opened) ||
		opened.Size() != entry.size || !opened.ModTime().Equal(entry.modified) ||
		!os.SameFile(current, opened) {
		return nil, errFallbackUnsafe
	}
	links, err := fallbackFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return nil, errFallbackUnsafe
	}
	confirmed, err := handle.root.Lstat(entry.name)
	if err != nil || !confirmed.Mode().IsRegular() || !fallbackFileModePrivate(confirmed) ||
		confirmed.Size() != opened.Size() || !confirmed.ModTime().Equal(opened.ModTime()) ||
		!os.SameFile(opened, confirmed) {
		return nil, errFallbackUnsafe
	}
	return confirmed, nil
}

func managedFallbackName(name string) bool {
	base := strings.TrimSuffix(name, ".corrupt")
	if !strings.HasSuffix(base, ".json") {
		return false
	}
	identifier := strings.TrimSuffix(base, ".json")
	return validFallbackIdentifier(identifier)
}

func managedFallbackTemporaryName(name string) bool {
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".tmp") {
		return false
	}
	core := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".tmp")
	parts := strings.Split(core, ".json.")
	return len(parts) == 2 && validFallbackIdentifier(parts[0]) && validFallbackIdentifier(parts[1])
}

func validFallbackIdentifier(identifier string) bool {
	if len(identifier) != 24 || strings.ToLower(identifier) != identifier {
		return false
	}
	decoded, err := hex.DecodeString(identifier)
	return err == nil && len(decoded) == 12
}

func (e *Exporter) pruneFallbackLocked(owner *platform.OwnedDirectory, incoming int64, reserveFile bool, now time.Time) error {
	if incoming < 0 || incoming > e.config.FallbackMaxBytes {
		return errFallbackCapacity
	}
	entries, err := e.scanFallbackLocked(owner)
	if err != nil {
		return err
	}
	retained := entries[:0]
	for _, entry := range entries {
		age := time.Duration(0)
		if !now.IsZero() && !entry.modified.After(now) {
			age = now.Sub(entry.modified)
		}
		entry.protected = entry.temporary && age <= maximumFallbackTempAge
		expired := !entry.protected && (entry.size > e.config.FallbackFileMax ||
			age > e.config.FallbackMaxAge ||
			entry.temporary && age > maximumFallbackTempAge)
		if expired {
			if err := e.removeFallbackLocked(owner, entry); err != nil {
				return err
			}
			continue
		}
		retained = append(retained, entry)
	}
	reserved := 0
	if reserveFile {
		reserved = 1
	}
	for len(retained)+reserved > e.config.FallbackMaxFiles ||
		fallbackTotalSize(retained, e.config.FallbackMaxBytes-incoming) > e.config.FallbackMaxBytes-incoming {
		eviction := -1
		for index := range retained {
			if !retained[index].protected {
				eviction = index
				break
			}
		}
		if eviction < 0 {
			return errFallbackCapacity
		}
		oldest := retained[eviction]
		retained = append(retained[:eviction], retained[eviction+1:]...)
		if err := e.removeFallbackLocked(owner, oldest); err != nil {
			return err
		}
	}
	return nil
}

func fallbackTotalSize(entries []fallbackDiskEntry, limit int64) int64 {
	if limit < 0 {
		return 0
	}
	total := int64(0)
	for _, entry := range entries {
		if entry.size < 0 || entry.size > limit-total {
			return limit + 1
		}
		total += entry.size
	}
	return total
}

func (e *Exporter) removeFallbackLocked(owner *platform.OwnedDirectory, entry fallbackDiskEntry) error {
	if err := unlinkFallbackLocked(owner, entry); err != nil {
		return err
	}
	e.stats.fallbackEvictions.Add(1)
	e.stats.fallbackEvictedBytes.Add(uint64(entry.size))
	return nil
}

func unlinkFallbackLocked(owner *platform.OwnedDirectory, entry fallbackDiskEntry) (resultErr error) {
	handle, err := openFallbackHandle(owner)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.close()) }()
	if _, err := verifyFallbackEntry(handle, entry); err != nil {
		return err
	}
	if err := handle.root.Remove(entry.name); err != nil {
		return err
	}
	if err := syncFallbackDirectory(handle.directory); err != nil {
		return err
	}
	return handle.verify()
}

// DrainFallback retries sanitized persisted batches in filename order and
// stops after the first endpoint failure to avoid outage amplification.
func (e *Exporter) DrainFallback(ctx context.Context) error {
	if e.config.FallbackDir == "" {
		return nil
	}
	e.fallbackMu.Lock()
	defer e.fallbackMu.Unlock()
	if e.fallbackOwner == nil {
		if _, err := os.Lstat(e.config.FallbackDir); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
	}
	owner, err := e.fallbackDirectoryLocked()
	if err != nil {
		return err
	}
	if err := e.pruneFallbackLocked(owner, 0, false, time.Now()); err != nil {
		return err
	}
	entries, err := e.scanFallbackLocked(owner)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	for _, entry := range entries {
		if filepath.Ext(entry.name) != ".json" {
			continue
		}
		if err := nonNilContext(ctx).Err(); err != nil {
			return err
		}
		envelope, readErr := e.readSpool(owner, entry)
		if readErr != nil {
			if !errors.Is(readErr, errFallbackCorrupt) {
				return readErr
			}
			if err := e.quarantineFallbackLocked(owner, entry); err != nil {
				return err
			}
			e.stats.fallbackCorruptions.Add(1)
			continue
		}
		if envelope.Destination != e.config.Destination {
			continue
		}
		if !e.config.Policy.EnabledFor(envelope.Traffic, envelope.Destination) ||
			!e.applyCurrentSpoolPolicy(envelope.Records) {
			if removeErr := unlinkFallbackLocked(owner, entry); removeErr != nil {
				return removeErr
			}
			e.stats.fallbackDrops.Add(uint64(len(envelope.Records)))
			continue
		}
		sendCtx, cancel := context.WithTimeout(nonNilContext(ctx), e.config.SendTimeout)
		exportErr := e.sink.export(sendCtx, envelope.Records)
		cancel()
		if exportErr != nil {
			return exportErr
		}
		e.stats.exported.Add(uint64(len(envelope.Records)))
		if removeErr := unlinkFallbackLocked(owner, entry); removeErr != nil {
			return removeErr
		}
	}
	return nil
}

func (e *Exporter) quarantineFallbackLocked(owner *platform.OwnedDirectory, entry fallbackDiskEntry) (resultErr error) {
	handle, err := openFallbackHandle(owner)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.close()) }()
	if _, err := verifyFallbackEntry(handle, entry); err != nil {
		return err
	}
	token := make([]byte, 12)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	quarantine := hex.EncodeToString(token) + ".json.corrupt"
	if _, err := handle.root.Lstat(quarantine); err == nil {
		return errors.New("fallback quarantine identity already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := handle.root.Rename(entry.name, quarantine); err != nil {
		return err
	}
	if err := syncFallbackDirectory(handle.directory); err != nil {
		return err
	}
	return handle.verify()
}

func (e *Exporter) readSpool(owner *platform.OwnedDirectory, entry fallbackDiskEntry) (result spoolEnvelope, resultErr error) {
	if entry.size < 0 || entry.size > e.config.FallbackFileMax {
		return spoolEnvelope{}, errFallbackCorrupt
	}
	handle, err := openFallbackHandle(owner)
	if err != nil {
		return spoolEnvelope{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, handle.close()) }()
	current, err := handle.root.Lstat(entry.name)
	if err != nil || !current.Mode().IsRegular() || !os.SameFile(entry.identity, current) {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	file, err := handle.root.Open(entry.name)
	if err != nil {
		return spoolEnvelope{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(current, opened) || !fallbackFileModePrivate(opened) ||
		opened.Size() != entry.size || !opened.ModTime().Equal(entry.modified) {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	links, err := fallbackFileLinkCount(file, opened)
	if err != nil || links != 1 {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	data, err := io.ReadAll(io.LimitReader(file, e.config.FallbackFileMax+1))
	if err != nil {
		return spoolEnvelope{}, err
	}
	if int64(len(data)) > e.config.FallbackFileMax || int64(len(data)) != opened.Size() {
		return spoolEnvelope{}, errFallbackCorrupt
	}
	afterRead, err := file.Stat()
	if err != nil || !os.SameFile(opened, afterRead) || !fallbackFileModePrivate(afterRead) ||
		afterRead.Size() != opened.Size() || !afterRead.ModTime().Equal(opened.ModTime()) {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	links, err = fallbackFileLinkCount(file, afterRead)
	if err != nil || links != 1 {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	confirmed, err := handle.root.Lstat(entry.name)
	if err != nil || !confirmed.Mode().IsRegular() || !fallbackFileModePrivate(confirmed) ||
		confirmed.Size() != afterRead.Size() || !confirmed.ModTime().Equal(afterRead.ModTime()) ||
		!os.SameFile(afterRead, confirmed) {
		return spoolEnvelope{}, errFallbackUnsafe
	}
	envelope, err := decodeSpoolEnvelope(data)
	if err != nil {
		return spoolEnvelope{}, errFallbackCorrupt
	}
	expectedID := strings.TrimSuffix(entry.name, ".json")
	if envelope.Version != spoolVersion || envelope.BatchID != expectedID || envelope.CreatedAt.IsZero() ||
		!validSpoolRoute(envelope.Destination, envelope.Traffic) ||
		len(envelope.Records) == 0 || len(envelope.Records) > maximumBatchSize {
		return spoolEnvelope{}, errFallbackCorrupt
	}
	for index := range envelope.Records {
		if err := sanitizeSpoolRecord(&envelope.Records[index], maximumPolicyAttributes, maximumPolicyValueRunes); err != nil {
			return spoolEnvelope{}, errFallbackCorrupt
		}
	}
	return envelope, handle.verify()
}

func validSpoolRoute(destination Destination, traffic TrafficClass) bool {
	switch destination {
	case DestinationEssential:
		return traffic == TrafficEssential
	case DestinationAnalytics, DestinationLocal:
		return traffic == TrafficOptional
	default:
		return false
	}
}

func (e *Exporter) applyCurrentSpoolPolicy(records []Record) bool {
	for index := range records {
		if len(records[index].Attributes) > e.config.Policy.MaxAttributes {
			return false
		}
		for key, value := range records[index].Attributes {
			if text, ok := value.(string); ok {
				records[index].Attributes[key] = truncateRunes(text, e.config.Policy.MaxValueRunes)
			}
		}
	}
	return true
}

func decodeSpoolEnvelope(data []byte) (spoolEnvelope, error) {
	if err := rejectDuplicateSpoolJSONMembers(data); err != nil {
		return spoolEnvelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	var envelope spoolEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return spoolEnvelope{}, err
	}
	if err := consumeSpoolJSONEOF(decoder); err != nil {
		return spoolEnvelope{}, err
	}
	return envelope, nil
}

func consumeSpoolJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return errors.New("fallback JSON contains a trailing value")
	}
	return nil
}

func rejectDuplicateSpoolJSONMembers(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanSpoolJSONValue(decoder, 0); err != nil {
		return err
	}
	return consumeSpoolJSONEOF(decoder)
}

func scanSpoolJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maximumSpoolJSONDepth {
		return errors.New("fallback JSON nesting exceeds its limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		members := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("fallback JSON member name is not a string")
			}
			if _, duplicate := members[key]; duplicate {
				return errDuplicateJSON
			}
			members[key] = struct{}{}
			if err := scanSpoolJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("fallback JSON object is not terminated")
		}
	case '[':
		for decoder.More() {
			if err := scanSpoolJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("fallback JSON array is not terminated")
		}
	default:
		return errors.New("fallback JSON has an unexpected delimiter")
	}
	return nil
}

func sanitizeSpoolRecord(record *Record, attributeLimit, valueLimit int) error {
	if record.Version != CurrentEventVersion || !eventNamePattern.MatchString(record.Name) || record.ID == "" || record.Timestamp.IsZero() ||
		len(record.Attributes) > attributeLimit {
		return errors.New("invalid fallback record")
	}
	record.ID = truncateRunes(RedactText(record.ID), 128)
	record.Source = truncateRunes(RedactText(record.Source), 64)
	record.Profile = truncateRunes(RedactText(record.Profile), 64)
	for key, value := range record.Attributes {
		if !attributeKeyPattern.MatchString(key) {
			return errors.New("invalid fallback attribute key")
		}
		switch typed := value.(type) {
		case string:
			record.Attributes[key] = truncateRunes(RedactText(typed), valueLimit)
		case bool:
		case json.Number:
			number, err := sanitizeSpoolNumber(typed)
			if err != nil {
				return err
			}
			record.Attributes[key] = number
		default:
			return errors.New("invalid fallback attribute value")
		}
	}
	return nil
}

func sanitizeSpoolNumber(number json.Number) (any, error) {
	text := number.String()
	if !strings.ContainsAny(text, ".eE") {
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			return integer, nil
		}
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, errors.New("invalid fallback numeric attribute")
	}
	return value, nil
}

type sinkBoundary struct {
	sink Sink
	gate chan struct{}
}

func newSinkBoundary(sink Sink) *sinkBoundary {
	return &sinkBoundary{sink: sink, gate: make(chan struct{}, 1)}
}

func (boundary *sinkBoundary) export(ctx context.Context, batch []Record) error {
	ctx = nonNilContext(ctx)
	return boundary.invoke(ctx, errSinkExportFailed, func() error {
		return boundary.sink.Export(ctx, cloneRecords(batch))
	})
}

func (boundary *sinkBoundary) shutdown(ctx context.Context) error {
	ctx = nonNilContext(ctx)
	return boundary.invoke(ctx, errSinkShutdownFailed, func() error {
		return boundary.sink.Shutdown(ctx)
	})
}

// invoke bounds host liveness even when a sink ignores cancellation. The gate
// remains held until the sink actually returns, so a hostile sink can strand
// at most one callback goroutine per exporter rather than one per retry.
func (boundary *sinkBoundary) invoke(ctx context.Context, fallback error, callback func() error) error {
	if err := ctx.Err(); err != nil {
		return projectSinkError(err, err, fallback)
	}
	select {
	case boundary.gate <- struct{}{}:
	case <-ctx.Done():
		return projectSinkError(ctx.Err(), ctx.Err(), fallback)
	}
	result := make(chan error, 1)
	go func() {
		defer func() { <-boundary.gate }()
		result <- invokeSinkCallback(callback)
	}()
	select {
	case err := <-result:
		return projectSinkError(err, ctx.Err(), fallback)
	case <-ctx.Done():
		return projectSinkError(ctx.Err(), ctx.Err(), fallback)
	}
}

func invokeSinkCallback(callback func() error) (result error) {
	defer func() {
		if recover() != nil {
			result = errSinkPanicked
		}
	}()
	return callback()
}

func safeExport(ctx context.Context, sink Sink, batch []Record) error {
	if sink == nil {
		return errSinkExportFailed
	}
	return newSinkBoundary(sink).export(ctx, batch)
}

func safeShutdown(ctx context.Context, sink Sink) error {
	if sink == nil {
		return errSinkShutdownFailed
	}
	return newSinkBoundary(sink).shutdown(ctx)
}

func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// projectSinkError retains only an exact context sentinel or the host-owned
// operation context's terminal state. It never invokes Error, Is, As, or
// Unwrap on a sink-owned error.
func projectSinkError(err, contextErr, fallback error) error {
	if err == nil {
		return nil
	}
	if exactSinkError(err, errSinkPanicked) {
		return errSinkPanicked
	}
	if exactSinkError(contextErr, context.DeadlineExceeded) ||
		exactSinkError(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if exactSinkError(contextErr, context.Canceled) ||
		exactSinkError(err, context.Canceled) {
		return context.Canceled
	}
	return fallback
}

func exactSinkError(err, target error) bool {
	typ := reflect.TypeOf(err)
	return err != nil && target != nil && typ != nil && typ.Comparable() && err == target
}
