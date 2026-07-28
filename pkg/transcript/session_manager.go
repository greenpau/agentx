package transcript

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/greenpau/agentx/pkg/platform"
	"github.com/greenpau/agentx/pkg/sessionlock"
)

const (
	// SessionManagementVersion identifies the closed inventory and deletion
	// result schemas projected by provider-free runtime entrypoints.
	SessionManagementVersion = 1

	DefaultSessionPageSize = 100
	MaxSessionPageSize     = 500
	MaxWorkspaceEntries    = 4096
	MaxDeletionStages      = 512
	MaxDeletionReceipts    = 512

	// MaxSessionInventoryDigestBytes bounds one complete inventory pass.
	// MaxSessionManagementDigestBytes additionally bounds the fixed set of
	// repeated passes and mutation-boundary validations in one operation.
	MaxSessionInventoryDigestBytes  int64 = 2 * 1024 * 1024 * 1024
	MaxSessionManagementDigestBytes int64 = 8 * 1024 * 1024 * 1024

	forkIncompleteMarker    = ".fork-incomplete"
	sessionLockName         = ".session.lock"
	transcriptName          = "transcript.jsonl"
	deleteIntentName        = ".agentx-delete-intent-v1.json"
	deleteIntentTempName    = ".agentx-delete-intent-v1.tmp"
	deleteStagePrefix       = ".agentx-delete-v1-"
	deleteReceiptRoot       = ".agentx-delete-receipts-v1"
	deleteReceiptPrefix     = "d1_"
	deleteReceiptTempPrefix = "t1-"
	deleteReceiptGCPrefix   = "g1_"
	deleteRegistryLockName  = ".registry.lock"
	deleteReceiptName       = "receipt.json"
	deleteCompleteName      = "complete"
	maxIntentBytes          = 2048
	maxReceiptBytes         = 4096
	receiptRetention        = 256
)

var (
	ErrSessionStoreUnsafe      = errors.New("native session store is unsafe")
	ErrSessionPageStale        = errors.New("session inventory page token is stale")
	ErrSessionDeletionStaged   = errors.New("session deletion is pending")
	ErrNoPreviousSession       = errors.New("no previous session found")
	errDeletionBoundaryChanged = errors.New("session changed at deletion boundary")
	errDeletionGenerationStale = errors.New("session ID belongs to a newer generation")
	errDeletionReceiptRetired  = errors.New("deletion receipt was retired")
)

type SessionListStatus string

const (
	SessionListOK          SessionListStatus = "ok"
	SessionListStale       SessionListStatus = "stale"
	SessionListStoreUnsafe SessionListStatus = "store_unsafe"
)

type SessionDeleteStatus string

const (
	SessionDeleted          SessionDeleteStatus = "deleted"
	SessionNotFound         SessionDeleteStatus = "not_found"
	SessionStale            SessionDeleteStatus = "stale"
	SessionLocked           SessionDeleteStatus = "session_locked"
	SessionDeleteIncomplete SessionDeleteStatus = "delete_incomplete"
	SessionStoreUnsafe      SessionDeleteStatus = "store_unsafe"
)

// SessionInventoryItem deliberately exposes only continuity metadata. The
// revision is opaque to callers and binds deletion to the exact workspace
// parent, session directory, transcript, and lock identities observed by this
// inventory generation.
type SessionInventoryItem struct {
	SessionID string `json:"session_id"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Revision  string `json:"revision"`

	updatedTime time.Time
}

type SessionListResult struct {
	Version       int                    `json:"version"`
	Status        SessionListStatus      `json:"status"`
	Sessions      []SessionInventoryItem `json:"sessions"`
	NextPageToken string                 `json:"next_page_token,omitempty"`
}

type SessionDeleteResult struct {
	Version   int                 `json:"version"`
	Status    SessionDeleteStatus `json:"status"`
	SessionID string              `json:"session_id"`
}

// NativeSessionState is an internal runtime projection used to keep
// resume/fork/creation selection aligned with authoritative inventory.
type NativeSessionState struct {
	Exists          bool
	Resumable       bool
	IncompleteFork  bool
	DeletionPending bool
	Revision        string
}

// SessionManager is the sole filesystem authority for native session
// inventory and deletion. Its root is the frozen application-home sessions
// capability; callers provide only the already-normalized absolute workspace,
// never a store path, partition key, or transcript path.
type SessionManager struct {
	sessionsRoot *platform.OwnedDirectory
	workspace    string
	workspaceKey string

	afterIntent                func() error
	afterReceipt               func() error
	afterDetach                func() error
	beforeCleanup              func() error
	afterCleanup               func() error
	beforeLockRaceStageCleanup func() error

	receiptEntryLimitForTest int
	receiptRetentionForTest  int
}

type sessionCandidate struct {
	item               SessionInventoryItem
	owner              *platform.OwnedDirectory
	directoryIdentity  string
	transcriptEvidence fileEvidence
	lockEvidence       *fileEvidence
	incompleteFork     bool
	deletionPending    bool
	recoverableIntent  bool
	intent             *deletionIntent
}

type deletionStage struct {
	sessionID          string
	revision           string
	name               string
	owner              *platform.OwnedDirectory
	partitionIdentity  string
	directoryIdentity  string
	transcriptEvidence *fileEvidence
	lockEvidence       *fileEvidence
	intent             *deletionIntent
	receipt            *deletionReceiptRecord
}

type workspaceScan struct {
	partition                   *platform.OwnedDirectory
	partitionIdentity           string
	entryCount                  int
	receiptRootExists           bool
	receiptRegistryLockEvidence *fileEvidence
	candidates                  map[string]*sessionCandidate
	stages                      map[string]*deletionStage
	receipts                    map[string]*deletionReceiptRecord
	receiptTemps                map[string]*deletionReceiptTemp
	receiptGC                   map[string]*platform.OwnedDirectory
	visible                     []SessionInventoryItem
}

type fileEvidence struct {
	identity  string
	size      int64
	modTime   time.Time
	mode      os.FileMode
	digest    [sha256.Size]byte
	hasDigest bool
}

type deletionIntent struct {
	Version     int    `json:"version"`
	SessionID   string `json:"session_id"`
	Revision    string `json:"revision"`
	StagingName string `json:"staging_name"`
}

type deletionReceipt struct {
	Version           int    `json:"version"`
	SessionID         string `json:"session_id"`
	Revision          string `json:"revision"`
	StagingName       string `json:"staging_name"`
	PartitionBinding  string `json:"partition_binding"`
	DirectoryBinding  string `json:"directory_binding"`
	TranscriptBinding string `json:"transcript_binding"`
	LockBinding       string `json:"lock_binding"`
}

type deletionReceiptRecord struct {
	receipt  deletionReceipt
	name     string
	owner    *platform.OwnedDirectory
	complete bool
}

type deletionReceiptTemp struct {
	sessionID string
	revision  string
	name      string
	owner     *platform.OwnedDirectory
	data      []byte
}

type digestBudget struct {
	remaining      int64
	phaseRemaining int64
}

func newDigestBudget() *digestBudget {
	return &digestBudget{
		remaining:      MaxSessionManagementDigestBytes,
		phaseRemaining: MaxSessionInventoryDigestBytes,
	}
}

func (budget *digestBudget) beginPhase() error {
	if budget == nil || budget.remaining < 0 {
		return ErrResourceLimit
	}
	budget.phaseRemaining = MaxSessionInventoryDigestBytes
	return nil
}

func (budget *digestBudget) claim(size int64) error {
	if budget == nil || size < 0 || size > budget.remaining || size > budget.phaseRemaining {
		return ErrResourceLimit
	}
	budget.remaining -= size
	budget.phaseRemaining -= size
	return nil
}

// NewSessionManager freezes the compatibility workspace partition key beneath
// the already-frozen sessions root. workspace must come from AgentX's shared
// absolute-workspace normalization boundary.
func NewSessionManager(sessionsRoot *platform.OwnedDirectory, workspace string) (*SessionManager, error) {
	if sessionsRoot == nil {
		return nil, errors.New("sessions root identity is unavailable")
	}
	if err := sessionsRoot.Verify(); err != nil {
		return nil, fmt.Errorf("verify sessions root: %w", err)
	}
	if workspace == "" || !filepath.IsAbs(workspace) || filepath.Clean(workspace) != workspace {
		return nil, errors.New("workspace must be a normalized absolute path")
	}
	sum := sha256.Sum256([]byte(workspace))
	return &SessionManager{
		sessionsRoot: sessionsRoot,
		workspace:    workspace,
		workspaceKey: fmt.Sprintf("%x", sum[:12]),
	}, nil
}

// ValidSessionID preserves the v1.0.6 native identifier grammar.
func ValidSessionID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' ||
			r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// OpenWorkspacePartition performs a read-only, identity-pinned lookup. A
// missing partition is an ordinary empty-store outcome and is never created.
func (manager *SessionManager) OpenWorkspacePartition() (*platform.OwnedDirectory, bool, error) {
	if err := manager.verify(); err != nil {
		return nil, false, err
	}
	root, err := manager.sessionsRoot.OpenRoot()
	if err != nil {
		return nil, false, fmt.Errorf("open sessions root: %w", err)
	}
	_, statErr := root.Lstat(manager.workspaceKey)
	closeErr := root.Close()
	if statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) && closeErr == nil {
			if err := manager.sessionsRoot.Verify(); err != nil {
				return nil, false, err
			}
			return nil, false, nil
		}
		return nil, false, errors.Join(fmt.Errorf("inspect workspace partition: %w", statErr), closeErr)
	}
	if closeErr != nil {
		return nil, false, closeErr
	}
	partition, err := manager.sessionsRoot.InspectPrivateChild(manager.workspaceKey)
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect workspace partition: %v", ErrSessionStoreUnsafe, err)
	}
	if err := manager.verify(); err != nil {
		return nil, false, err
	}
	return partition, true, nil
}

// EnsureWorkspacePartition is used by semantic session creation while keeping
// workspace hashing owned by the same runtime service as inventory/deletion.
func (manager *SessionManager) EnsureWorkspacePartition() (*platform.OwnedDirectory, error) {
	if err := manager.verify(); err != nil {
		return nil, err
	}
	partition, err := manager.sessionsRoot.EnsurePrivateChild(manager.workspaceKey)
	if err != nil {
		return nil, fmt.Errorf("open workspace session directory: %w", err)
	}
	if err := manager.verify(); err != nil {
		return nil, err
	}
	return partition, nil
}

// WorkspacePartitionKey returns the frozen v1.0.6 compatibility key for
// application-internal stores that intentionally share workspace partitioning.
// It is never accepted from or projected to an external caller.
func (manager *SessionManager) WorkspacePartitionKey() string {
	if manager == nil {
		return ""
	}
	return manager.workspaceKey
}

func (manager *SessionManager) verify() error {
	if manager == nil || manager.sessionsRoot == nil || manager.workspace == "" || manager.workspaceKey == "" {
		return errors.New("session manager is unavailable")
	}
	if !filepath.IsAbs(manager.workspace) || filepath.Clean(manager.workspace) != manager.workspace {
		return errors.New("session manager workspace identity is invalid")
	}
	return manager.sessionsRoot.Verify()
}

func (manager *SessionManager) receiptEntryLimit() int {
	if manager != nil &&
		manager.receiptEntryLimitForTest > 0 &&
		manager.receiptEntryLimitForTest <= MaxDeletionReceipts {
		return manager.receiptEntryLimitForTest
	}
	return MaxDeletionReceipts
}

func (manager *SessionManager) completedReceiptRetention() int {
	if manager != nil &&
		manager.receiptRetentionForTest > 0 &&
		manager.receiptRetentionForTest <= receiptRetention {
		return manager.receiptRetentionForTest
	}
	return receiptRetention
}

// List returns one deterministic page. Every page is derived from a complete
// bounded scan; callers receive a continuation token instead of truncation.
func (manager *SessionManager) List(ctx context.Context, pageSize int, pageToken string) (SessionListResult, error) {
	result := SessionListResult{
		Version:  SessionManagementVersion,
		Status:   SessionListOK,
		Sessions: []SessionInventoryItem{},
	}
	if pageSize == 0 {
		pageSize = DefaultSessionPageSize
	}
	if pageSize < 1 || pageSize > MaxSessionPageSize {
		result.Status = SessionListStoreUnsafe
		return result, fmt.Errorf("%w: invalid page size", ErrSessionStoreUnsafe)
	}
	scan, err := manager.scanWorkspace(ctx, "", newDigestBudget())
	if err != nil {
		result.Status = SessionListStoreUnsafe
		return result, err
	}
	generation := inventoryGeneration(scan.visible)
	offset := 0
	if pageToken != "" {
		offset, err = decodePageToken(pageToken, generation)
		// A continuation token is emitted only for a nonterminal page, so zero
		// and the current inventory length are never valid continuation
		// offsets. Rejecting them prevents malformed tokens from producing a
		// successful but empty/skipped page.
		if err != nil || offset <= 0 || offset >= len(scan.visible) {
			result.Status = SessionListStale
			return result, ErrSessionPageStale
		}
	}
	end := offset + pageSize
	if end > len(scan.visible) {
		end = len(scan.visible)
	}
	result.Sessions = append(result.Sessions, scan.visible[offset:end]...)
	if end < len(scan.visible) {
		result.NextPageToken = encodePageToken(end, generation)
	}
	return result, nil
}

// Latest returns the same first candidate exposed by authoritative inventory.
func (manager *SessionManager) Latest(ctx context.Context) (string, error) {
	item, err := manager.LatestItem(ctx)
	if err != nil {
		return "", err
	}
	return item.SessionID, nil
}

// LatestItem returns the same first candidate and generation-bound revision
// exposed by authoritative inventory. Runtime selectors retain that revision
// until they acquire the candidate's session lock so delete/recreate cannot
// substitute a different generation under the same ID.
func (manager *SessionManager) LatestItem(ctx context.Context) (SessionInventoryItem, error) {
	scan, err := manager.scanWorkspace(ctx, "", newDigestBudget())
	if err != nil {
		return SessionInventoryItem{}, err
	}
	if len(scan.visible) == 0 {
		return SessionInventoryItem{}, ErrNoPreviousSession
	}
	return scan.visible[0], nil
}

// DeletionPending reports whether the exact native ID is either marked for
// deletion at its live name or detached into validated cleanup staging.
func (manager *SessionManager) DeletionPending(ctx context.Context, sessionID string) (bool, error) {
	if !ValidSessionID(sessionID) {
		return false, errors.New("session identifier is invalid")
	}
	scan, err := manager.scanWorkspace(ctx, sessionID, newDigestBudget())
	if err != nil {
		return false, err
	}
	if scan.stages[sessionID] != nil || scan.hasPendingReceipt(sessionID) {
		return true, nil
	}
	candidate := scan.candidates[sessionID]
	return candidate != nil && candidate.deletionPending, nil
}

// State returns the bounded authoritative selection state for one native ID.
func (manager *SessionManager) State(ctx context.Context, sessionID string) (NativeSessionState, error) {
	if !ValidSessionID(sessionID) {
		return NativeSessionState{}, errors.New("session identifier is invalid")
	}
	scan, err := manager.scanWorkspace(ctx, sessionID, newDigestBudget())
	if err != nil {
		return NativeSessionState{}, err
	}
	state := NativeSessionState{
		DeletionPending: scan.stages[sessionID] != nil || scan.hasPendingReceipt(sessionID),
	}
	if candidate := scan.candidates[sessionID]; candidate != nil {
		state.Exists = true
		state.IncompleteFork = candidate.incompleteFork
		state.DeletionPending = state.DeletionPending || candidate.deletionPending
		state.Revision = candidate.item.Revision
		state.Resumable = candidate.item.Revision != "" &&
			!candidate.incompleteFork &&
			!candidate.deletionPending
	}
	return state, nil
}

// Delete removes exactly one revision from the selected workspace. Expected
// operational outcomes are represented by the closed Status union; err carries
// internal diagnostics for non-JSON callers and must not be serialized.
func (manager *SessionManager) Delete(ctx context.Context, sessionID, revision string) (SessionDeleteResult, error) {
	result := SessionDeleteResult{
		Version: SessionManagementVersion,
		Status:  SessionStoreUnsafe,
	}
	if !ValidSessionID(sessionID) {
		return result, fmt.Errorf("%w: invalid session identifier", ErrSessionStoreUnsafe)
	}
	result.SessionID = sessionID
	if !validRevision(revision) {
		result.Status = SessionStale
		return result, errors.New("session revision is invalid")
	}
	budget := newDigestBudget()
	scan, err := manager.scanWorkspace(ctx, sessionID, budget)
	if err != nil {
		return result, err
	}
	stage := scan.stages[sessionID]
	live := scan.candidates[sessionID]
	receipt := scan.receipts[deletionReceiptKey(sessionID, revision)]
	if stage != nil && live != nil {
		return result, fmt.Errorf("%w: live and detached session identities overlap", ErrSessionStoreUnsafe)
	}
	if receipt != nil && receipt.complete {
		if stage != nil && stage.revision != revision {
			result.Status = SessionStale
			return result, errors.New("session ID now belongs to a newer staged generation")
		}
		if stage != nil || (live != nil && live.item.Revision == revision) {
			return result, fmt.Errorf("%w: completed deletion receipt contradicts native session state", ErrSessionStoreUnsafe)
		}
		if live != nil {
			result.Status = SessionStale
			return result, errors.New("session ID now belongs to a newer generation")
		}
		if err := manager.finalizeDeletionReceipt(
			ctx,
			scan.partition,
			scan.receiptRegistryLockEvidence,
			receipt,
			false,
		); err != nil {
			if errors.Is(err, sessionlock.ErrContended) {
				result.Status = SessionLocked
				return result, nil
			}
			if errors.Is(err, errDeletionGenerationStale) {
				result.Status = SessionStale
				return result, err
			}
			if errors.Is(err, ErrSessionStoreUnsafe) {
				result.Status = SessionStoreUnsafe
				return result, err
			}
			if errors.Is(err, errDeletionReceiptRetired) {
				result.Status = SessionNotFound
				return result, nil
			}
			result.Status = SessionDeleteIncomplete
			return result, err
		}
		result.Status = SessionDeleted
		return result, nil
	}
	if receipt != nil && stage == nil && live == nil {
		if err := manager.finalizeDeletionReceipt(
			ctx,
			scan.partition,
			scan.receiptRegistryLockEvidence,
			receipt,
			true,
		); err != nil {
			if errors.Is(err, sessionlock.ErrContended) {
				result.Status = SessionLocked
				return result, nil
			}
			if errors.Is(err, errDeletionGenerationStale) {
				result.Status = SessionStale
				return result, err
			}
			if errors.Is(err, ErrSessionStoreUnsafe) {
				result.Status = SessionStoreUnsafe
				return result, err
			}
			if errors.Is(err, errDeletionReceiptRetired) {
				result.Status = SessionNotFound
				return result, nil
			}
			result.Status = SessionDeleteIncomplete
			return result, err
		}
		result.Status = SessionDeleted
		return result, nil
	}
	if stage != nil {
		if stage.revision != revision {
			result.Status = SessionStale
			return result, errors.New("session revision no longer matches deletion staging")
		}
		return manager.cleanupStage(ctx, result, scan, stage, budget)
	}
	if live == nil || live.incompleteFork || live.item.Revision == "" {
		result.Status = SessionNotFound
		return result, nil
	}
	if live.item.Revision != revision {
		result.Status = SessionStale
		return result, errors.New("session revision no longer matches")
	}

	if receipt != nil && receipt.receipt.Revision != live.item.Revision {
		return result, fmt.Errorf("%w: pending deletion receipt overlaps a new live generation", ErrSessionStoreUnsafe)
	}
	lock, err := sessionlock.AcquireExisting(ctx, filepath.Join(live.owner.Path(), sessionLockName))
	if err != nil {
		if errors.Is(err, sessionlock.ErrContended) {
			result.Status = SessionLocked
			return result, nil
		}
		if errors.Is(err, os.ErrNotExist) ||
			errors.Is(err, sessionlock.ErrUnsafePath) ||
			errors.Is(err, platform.ErrDirectoryIdentityChanged) {
			return manager.reconcileLiveLockRace(
				ctx,
				result,
				scan,
				sessionID,
				revision,
				budget,
			)
		}
		return result, fmt.Errorf("%w: acquire deletion lock: %v", ErrSessionStoreUnsafe, err)
	}
	lockHeld := true
	closeLock := func() error {
		if !lockHeld {
			return nil
		}
		lockHeld = false
		return lock.Close()
	}
	defer func() {
		if lockHeld {
			_ = lock.Close()
		}
	}()

	// Re-scan after locking. This rebinds the caller's opaque revision to the
	// current parent, target, transcript, and any pre-existing lock identity.
	lockedScan, err := manager.scanWorkspace(ctx, sessionID, budget)
	if err != nil {
		_ = closeLock()
		return result, err
	}
	if lockedScan.stages[sessionID] != nil {
		_ = closeLock()
		return result, fmt.Errorf("%w: deletion stage appeared before mutation", ErrSessionStoreUnsafe)
	}
	live = lockedScan.candidates[sessionID]
	if live == nil || live.incompleteFork || live.item.Revision != revision {
		_ = closeLock()
		result.Status = SessionStale
		return result, errors.New("session changed before deletion lock was acquired")
	}
	if err := lockedScan.partition.PreflightPrivateChildDetach(live.owner); err != nil {
		_ = closeLock()
		return result, fmt.Errorf("%w: preflight deletion detach: %v", ErrSessionStoreUnsafe, err)
	}
	if len(lockedScan.stages) >= MaxDeletionStages ||
		(!lockedScan.receiptRootExists && lockedScan.entryCount >= MaxWorkspaceEntries) {
		_ = closeLock()
		return result, fmt.Errorf("%w: deletion transaction has no bounded metadata headroom", ErrSessionStoreUnsafe)
	}
	receiptRoot, registryLock, err := manager.acquireReceiptRegistry(
		ctx,
		lockedScan.partition,
		lockedScan.receiptRegistryLockEvidence,
		true,
	)
	if err != nil {
		_ = closeLock()
		if errors.Is(err, sessionlock.ErrContended) {
			result.Status = SessionLocked
			return result, nil
		}
		return result, fmt.Errorf("%w: acquire deletion receipt registry: %v", ErrSessionStoreUnsafe, err)
	}
	registryHeld := true
	closeRegistry := func() error {
		if !registryHeld {
			return nil
		}
		registryHeld = false
		return registryLock.Close()
	}
	defer func() {
		if registryHeld {
			_ = registryLock.Close()
		}
	}()

	// Re-scan while both the target and registry mutation leases are held.
	// This makes stage and receipt headroom a serialized reservation rather
	// than a stale preflight count.
	lockedScan, err = manager.scanWorkspace(ctx, sessionID, budget)
	if err != nil {
		_ = closeRegistry()
		_ = closeLock()
		return result, err
	}
	live = lockedScan.candidates[sessionID]
	if live == nil || live.incompleteFork || live.item.Revision != revision ||
		lockedScan.stages[sessionID] != nil {
		_ = closeRegistry()
		_ = closeLock()
		result.Status = SessionStale
		return result, errors.New("session changed before intent")
	}
	if len(lockedScan.stages) >= MaxDeletionStages ||
		lockedScan.entryCount > MaxWorkspaceEntries {
		_ = closeRegistry()
		_ = closeLock()
		return result, fmt.Errorf("%w: deletion transaction lost metadata headroom", ErrSessionStoreUnsafe)
	}
	stageName := deletionStageName(sessionID, revision)
	if err := requireStageAbsent(lockedScan.partition, stageName); err != nil {
		_ = closeRegistry()
		_ = closeLock()
		return result, fmt.Errorf("%w: %v", ErrSessionStoreUnsafe, err)
	}
	// Reserve receipt capacity before durable target intent exists. This may
	// finish bounded GC of older completed metadata while the registry lease is
	// held, but inability to make room must leave the target fully selectable.
	if err := manager.prepareReceiptCapacity(
		lockedScan.partition,
		receiptRoot,
		registryLock,
		lockedScan.receipts,
		lockedScan.receiptTemps,
		lockedScan.receiptGC,
		deletionReceiptKey(sessionID, revision),
	); err != nil {
		_ = closeRegistry()
		_ = closeLock()
		return result, fmt.Errorf("%w: reserve deletion receipt capacity: %v", ErrSessionStoreUnsafe, err)
	}
	if err := manager.verifyReceiptRegistryLease(
		lockedScan.partition,
		receiptRoot,
		registryLock,
	); err != nil {
		_ = closeRegistry()
		_ = closeLock()
		return result, fmt.Errorf("%w: verify deletion receipt reservation: %v", ErrSessionStoreUnsafe, err)
	}
	intent := deletionIntent{
		Version:     SessionManagementVersion,
		SessionID:   sessionID,
		Revision:    revision,
		StagingName: stageName,
	}
	if err := ensureDeletionIntent(
		live.owner,
		intent,
		func() error {
			return manager.verifyReceiptRegistryLease(
				lockedScan.partition,
				receiptRoot,
				registryLock,
			)
		},
	); err != nil {
		_ = closeRegistry()
		_ = closeLock()
		result.Status = SessionDeleteIncomplete
		return result, fmt.Errorf("persist deletion intent: %w", err)
	}
	if manager.afterIntent != nil {
		if err := manager.afterIntent(); err != nil {
			_ = closeRegistry()
			_ = closeLock()
			result.Status = SessionDeleteIncomplete
			return result, fmt.Errorf("after deletion intent: %w", err)
		}
	}

	if err := budget.beginPhase(); err != nil {
		_ = closeRegistry()
		_ = closeLock()
		result.Status = SessionDeleteIncomplete
		return result, err
	}
	finalCandidate, err := manager.inspectSessionCandidate(
		ctx,
		lockedScan.partition,
		lockedScan.partitionIdentity,
		sessionID,
		sessionID,
		budget,
	)
	boundaryChanged := err == nil && (finalCandidate == nil ||
		finalCandidate.item.Revision != revision ||
		finalCandidate.intent == nil ||
		!sameDeletionIntent(*finalCandidate.intent, intent))
	if err != nil || boundaryChanged {
		registryErr := closeRegistry()
		closeErr := closeLock()
		result.Status = SessionDeleteIncomplete
		return result, errors.Join(
			errors.New("session changed after durable deletion intent"),
			err,
			registryErr,
			closeErr,
		)
	}
	receipt, err = manager.ensureCandidateReceipt(
		lockedScan,
		receiptRoot,
		registryLock,
		finalCandidate,
		intent,
	)
	if err != nil {
		registryErr := closeRegistry()
		closeErr := closeLock()
		result.Status = SessionDeleteIncomplete
		return result, errors.Join(
			fmt.Errorf("persist deletion receipt: %w", err),
			registryErr,
			closeErr,
		)
	}

	// DetachPrivateChildVerified invokes this after its own final rooted
	// parent/source/destination checks and immediately before the atomic
	// no-replace rename. Reinspect all revision and intent evidence here so no
	// earlier path-derived decision authorizes the mutation.
	verifyDetach := func() error {
		if err := manager.verify(); err != nil {
			return err
		}
		current, inspectErr := manager.inspectSessionCandidate(
			ctx,
			lockedScan.partition,
			lockedScan.partitionIdentity,
			sessionID,
			sessionID,
			budget,
		)
		if inspectErr != nil {
			return inspectErr
		}
		if current == nil || current.item.Revision != revision ||
			current.intent == nil || !sameDeletionIntent(*current.intent, intent) {
			return fmt.Errorf("%w: revision or intent mismatch", errDeletionBoundaryChanged)
		}
		if err := requireStageAbsent(lockedScan.partition, stageName); err != nil {
			return err
		}
		if err := manager.verifyReceipt(lockedScan.partition, receipt); err != nil {
			return err
		}
		return errors.Join(
			lockedScan.partition.Verify(),
			current.owner.Verify(),
			lock.Verify(),
			registryLock.Verify(),
			receiptRoot.Verify(),
			manager.verify(),
		)
	}
	detached, detachErr := lockedScan.partition.DetachPrivateChildVerified(
		finalCandidate.owner,
		stageName,
		verifyDetach,
	)
	if !detached.Committed {
		registryErr := closeRegistry()
		closeErr := closeLock()
		result.Status = SessionDeleteIncomplete
		return result, errors.Join(
			fmt.Errorf("detach session for deletion: %w", detachErr),
			registryErr,
			closeErr,
		)
	}
	stage = &deletionStage{
		sessionID:          sessionID,
		revision:           revision,
		name:               stageName,
		owner:              detached.Owner,
		partitionIdentity:  lockedScan.partitionIdentity,
		directoryIdentity:  finalCandidate.directoryIdentity,
		transcriptEvidence: &finalCandidate.transcriptEvidence,
		lockEvidence:       finalCandidate.lockEvidence,
		intent:             &intent,
		receipt:            receipt,
	}
	if manager.afterDetach != nil {
		if err := manager.afterDetach(); err != nil {
			registryErr := closeRegistry()
			closeErr := closeLock()
			result.Status = SessionDeleteIncomplete
			return result, errors.Join(detachErr, registryErr, fmt.Errorf("after session detach: %w", err), closeErr)
		}
	}
	// The live valid-ID path is now unreachable. Only now may the internal lock
	// be released; recursive cleanup never runs while that live lock path is
	// still addressable.
	if err := closeLock(); err != nil {
		registryErr := closeRegistry()
		result.Status = SessionDeleteIncomplete
		return result, errors.Join(
			detachErr,
			fmt.Errorf("release detached session lock: %w", err),
			registryErr,
		)
	}
	// Transfer registry-lock ownership to cleanup. The lease is deliberately
	// continuous across detach, internal-lock release, recursive cleanup,
	// durable parent sync, and receipt completion.
	registryHeld = false
	cleanupResult, cleanupErr := manager.cleanupStageWithRegistry(
		ctx,
		result,
		lockedScan,
		stage,
		budget,
		receiptRoot,
		registryLock,
		true,
	)
	if cleanupResult.Status == SessionDeleted && detachErr != nil {
		// A post-commit detach diagnostic can be recovered only when cleanup,
		// parent sync, and final absence checks all succeeded.
		return cleanupResult, nil
	}
	return cleanupResult, errors.Join(detachErr, cleanupErr)
}

func (manager *SessionManager) reconcileLiveLockRace(
	ctx context.Context,
	result SessionDeleteResult,
	scan *workspaceScan,
	sessionID string,
	revision string,
	budget *digestBudget,
) (SessionDeleteResult, error) {
	if scan == nil || scan.partition == nil {
		result.Status = SessionStoreUnsafe
		return result, fmt.Errorf("%w: deletion lock-race state is unavailable", ErrSessionStoreUnsafe)
	}
	receiptRoot, registryLock, err := manager.acquireReceiptRegistry(
		ctx,
		scan.partition,
		scan.receiptRegistryLockEvidence,
		false,
	)
	if errors.Is(err, sessionlock.ErrContended) {
		result.Status = SessionLocked
		return result, nil
	}
	if err != nil {
		result.Status = SessionStoreUnsafe
		return result, fmt.Errorf("%w: reconcile deletion lock race: %v", ErrSessionStoreUnsafe, err)
	}
	registryHeld := true
	closeRegistry := func() error {
		if !registryHeld {
			return nil
		}
		registryHeld = false
		return registryLock.Close()
	}
	defer func() {
		if registryHeld {
			_ = registryLock.Close()
		}
	}()
	finish := func(cause error) (SessionDeleteResult, error) {
		closeErr := closeRegistry()
		if closeErr != nil {
			result.Status = SessionDeleteIncomplete
		}
		return result, errors.Join(cause, closeErr)
	}

	currentScan, err := manager.scanWorkspace(ctx, sessionID, budget)
	if err != nil {
		result.Status = SessionStoreUnsafe
		return finish(err)
	}
	if live := currentScan.candidates[sessionID]; live != nil {
		if live.item.Revision != revision {
			result.Status = SessionStale
			return finish(errDeletionGenerationStale)
		}
		result.Status = SessionStoreUnsafe
		return finish(fmt.Errorf(
			"%w: live session lock path changed without a deletion handoff",
			ErrSessionStoreUnsafe,
		))
	}
	if stage := currentScan.stages[sessionID]; stage != nil {
		if stage.revision != revision {
			result.Status = SessionStale
			return finish(errDeletionGenerationStale)
		}
		// This path did not retain the original live target lock. Do not hand
		// the registry lease into staged cleanup: ordinary recovery must acquire
		// the staged session lock first and the registry lock second, otherwise
		// it inverts the global order against another cleanup retry.
		if err := closeRegistry(); err != nil {
			result.Status = SessionDeleteIncomplete
			return result, fmt.Errorf("release deletion registry before staged cleanup: %w", err)
		}
		if manager.beforeLockRaceStageCleanup != nil {
			if err := manager.beforeLockRaceStageCleanup(); err != nil {
				result.Status = SessionDeleteIncomplete
				return result, fmt.Errorf("before lock-race staged cleanup: %w", err)
			}
		}
		return manager.cleanupStage(ctx, result, currentScan, stage, budget)
	}
	receipt := currentScan.receipts[deletionReceiptKey(sessionID, revision)]
	if receipt == nil {
		initial := scan.candidates[sessionID]
		if initial == nil || initial.lockEvidence == nil {
			result.Status = SessionStoreUnsafe
			return finish(fmt.Errorf(
				"%w: initial listed-session binding is unavailable",
				ErrSessionStoreUnsafe,
			))
		}
		intent := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   sessionID,
			Revision:    revision,
			StagingName: deletionStageName(sessionID, revision),
		}
		binding := &deletionReceiptRecord{
			receipt: manager.receiptForCandidate(scan.partitionIdentity, initial, intent),
		}
		partitionIdentity, entries, snapshotErr := boundedPartitionSnapshot(currentScan.partition)
		if snapshotErr != nil {
			result.Status = SessionStoreUnsafe
			return finish(snapshotErr)
		}
		if movedErr := manager.rejectMovedReceiptIdentity(
			currentScan.partition,
			partitionIdentity,
			entries,
			binding,
		); movedErr != nil {
			result.Status = SessionStoreUnsafe
			return finish(fmt.Errorf("%w: listed session identity moved: %v", ErrSessionStoreUnsafe, movedErr))
		}
		result.Status = SessionStoreUnsafe
		return finish(fmt.Errorf(
			"%w: listed session vanished without a deletion receipt",
			ErrSessionStoreUnsafe,
		))
	}
	if receipt.complete {
		err = manager.confirmCompletedReceipt(
			currentScan.partition,
			receiptRoot,
			registryLock,
			receipt,
		)
	} else {
		err = manager.completeReceiptAfterAbsentStage(
			currentScan.partition,
			receiptRoot,
			registryLock,
			receipt,
		)
	}
	switch {
	case errors.Is(err, errDeletionGenerationStale):
		result.Status = SessionStale
	case err != nil:
		result.Status = SessionDeleteIncomplete
	default:
		result.Status = SessionDeleted
	}
	return finish(err)
}

func (manager *SessionManager) cleanupStage(
	ctx context.Context,
	result SessionDeleteResult,
	scan *workspaceScan,
	stage *deletionStage,
	budget *digestBudget,
) (SessionDeleteResult, error) {
	return manager.cleanupStageWithRegistry(
		ctx,
		result,
		scan,
		stage,
		budget,
		nil,
		nil,
		false,
	)
}

func (manager *SessionManager) cleanupStageWithRegistry(
	ctx context.Context,
	result SessionDeleteResult,
	scan *workspaceScan,
	stage *deletionStage,
	budget *digestBudget,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	liveDetachHandoff bool,
) (SessionDeleteResult, error) {
	result.Status = SessionDeleteIncomplete
	registryHeld := registryLock != nil
	closeRegistry := func() error {
		if !registryHeld {
			return nil
		}
		registryHeld = false
		return registryLock.Close()
	}
	defer func() {
		if registryHeld {
			_ = registryLock.Close()
		}
	}()
	finishRegistry := func(cause error) (SessionDeleteResult, error) {
		closeErr := closeRegistry()
		if closeErr != nil {
			result.Status = SessionDeleteIncomplete
		}
		return result, errors.Join(cause, closeErr)
	}
	if (receiptRoot == nil) != (registryLock == nil) {
		return finishRegistry(errors.New("partial deletion receipt registry handoff"))
	}
	if err := contextError(ctx); err != nil {
		return finishRegistry(err)
	}
	if budget == nil {
		return finishRegistry(errors.New("deletion digest budget is unavailable"))
	}
	if err := budget.beginPhase(); err != nil {
		return finishRegistry(err)
	}
	if scan == nil || scan.partition == nil || stage == nil || stage.owner == nil {
		return finishRegistry(errors.New("deletion staging identity is unavailable"))
	}
	partition := scan.partition

	var stageLock *sessionlock.Lock
	var stageLockErr error
	if !liveDetachHandoff && stage.lockEvidence != nil {
		stageLock, stageLockErr = sessionlock.AcquireExisting(
			ctx,
			filepath.Join(stage.owner.Path(), sessionLockName),
		)
		if errors.Is(stageLockErr, sessionlock.ErrContended) {
			result.Status = SessionLocked
			return finishRegistry(nil)
		}
	}
	closeStageLock := func() error {
		if stageLock == nil {
			return nil
		}
		err := stageLock.Close()
		stageLock = nil
		return err
	}
	defer func() {
		if stageLock != nil {
			_ = stageLock.Close()
		}
	}()

	if registryLock == nil {
		var err error
		allowRegistryCreate := stage.receipt == nil &&
			scan.receiptRegistryLockEvidence == nil &&
			len(scan.receipts) == 0 &&
			len(scan.receiptTemps) == 0 &&
			len(scan.receiptGC) == 0 &&
			(scan.receiptRootExists || scan.entryCount < MaxWorkspaceEntries) &&
			stageLock != nil
		receiptRoot, registryLock, err = manager.acquireReceiptRegistry(
			ctx,
			partition,
			scan.receiptRegistryLockEvidence,
			allowRegistryCreate,
		)
		if errors.Is(err, sessionlock.ErrContended) {
			result.Status = SessionLocked
			stageCloseErr := closeStageLock()
			if stageCloseErr != nil {
				result.Status = SessionDeleteIncomplete
			}
			return finishRegistry(stageCloseErr)
		}
		if err != nil {
			stageCloseErr := closeStageLock()
			if stageCloseErr != nil {
				result.Status = SessionDeleteIncomplete
			}
			return finishRegistry(errors.Join(
				fmt.Errorf("%w: acquire staged-cleanup registry: %v", ErrSessionStoreUnsafe, err),
				stageCloseErr,
			))
		}
		registryHeld = true
	} else if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		stageCloseErr := closeStageLock()
		if stageCloseErr != nil {
			result.Status = SessionDeleteIncomplete
		}
		return finishRegistry(errors.Join(
			fmt.Errorf("%w: verify staged-cleanup registry handoff: %v", ErrSessionStoreUnsafe, err),
			stageCloseErr,
		))
	}

	finish := func(cause error) (SessionDeleteResult, error) {
		stageCloseErr := closeStageLock()
		closeErr := closeRegistry()
		if stageCloseErr != nil || closeErr != nil {
			result.Status = SessionDeleteIncomplete
		}
		return result, errors.Join(cause, stageCloseErr, closeErr)
	}

	currentScan, err := manager.scanWorkspace(ctx, stage.sessionID, budget)
	if err != nil {
		return finish(err)
	}
	current := currentScan.stages[stage.sessionID]
	if current == nil {
		return manager.finishAbsentStageWithRegistry(
			&result,
			currentScan,
			stage,
			receiptRoot,
			registryLock,
			finish,
		)
	}
	if current.revision != stage.revision {
		result.Status = SessionStale
		return finish(errDeletionGenerationStale)
	}
	if stageLockErr != nil && current.lockEvidence != nil {
		return finish(fmt.Errorf(
			"%w: acquire stable staged deletion lock: %v",
			ErrSessionStoreUnsafe,
			stageLockErr,
		))
	}
	if !liveDetachHandoff && current.lockEvidence != nil && stageLock == nil {
		return finish(fmt.Errorf("%w: staged deletion lock lease is unavailable", ErrSessionStoreUnsafe))
	}
	stage = current
	if stage.receipt == nil {
		stage.receipt, err = manager.ensureStageReceipt(
			currentScan,
			receiptRoot,
			registryLock,
			stage,
		)
		if err != nil {
			return finish(fmt.Errorf("persist staged deletion receipt: %w", err))
		}
	}
	if stageLock != nil {
		if err := errors.Join(
			stageLock.Verify(),
			stage.owner.Verify(),
			manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
		); err != nil {
			return finish(fmt.Errorf("%w: verify staged deletion leases: %v", ErrSessionStoreUnsafe, err))
		}
	}
	if err := closeStageLock(); err != nil {
		return finish(fmt.Errorf("release staged deletion lock: %w", err))
	}
	if manager.beforeCleanup != nil {
		if err := manager.beforeCleanup(); err != nil {
			return finish(fmt.Errorf("before staged cleanup: %w", err))
		}
	}
	if err := errors.Join(
		manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
		stage.owner.Verify(),
	); err != nil {
		return finish(fmt.Errorf("%w: verify staged cleanup boundary: %v", ErrSessionStoreUnsafe, err))
	}
	if err := stage.owner.RemoveAllExisting(); err != nil {
		return finish(fmt.Errorf("remove detached session contents: %w", err))
	}
	if manager.afterCleanup != nil {
		if err := manager.afterCleanup(); err != nil {
			return finish(fmt.Errorf("after staged cleanup: %w", err))
		}
	}
	if err := partition.Sync(); err != nil {
		return finish(fmt.Errorf("sync workspace session directory: %w", err))
	}
	if err := requireStageCleanupComplete(partition, stage.name); err != nil {
		return finish(err)
	}
	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return finish(fmt.Errorf("%w: verify staged completion boundary: %v", ErrSessionStoreUnsafe, err))
	}
	if err := manager.completeReceiptAfterAbsentStage(
		partition,
		receiptRoot,
		registryLock,
		stage.receipt,
	); err != nil {
		if errors.Is(err, errDeletionGenerationStale) {
			result.Status = SessionStale
		}
		return finish(fmt.Errorf("complete deletion receipt: %w", err))
	}
	result.Status = SessionDeleted
	return finish(nil)
}

func (manager *SessionManager) finishAbsentStageWithRegistry(
	result *SessionDeleteResult,
	scan *workspaceScan,
	expectedStage *deletionStage,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	finish func(error) (SessionDeleteResult, error),
) (SessionDeleteResult, error) {
	if result == nil {
		return SessionDeleteResult{
			Version: SessionManagementVersion,
			Status:  SessionDeleteIncomplete,
		}, errors.New("staged-cleanup result is unavailable")
	}
	if scan == nil || scan.partition == nil || expectedStage == nil {
		return finish(fmt.Errorf("%w: missing staged-cleanup reconciliation state", ErrSessionStoreUnsafe))
	}
	if live := scan.candidates[expectedStage.sessionID]; live != nil {
		result.Status = SessionStale
		return finish(errDeletionGenerationStale)
	}
	if newerStage := scan.stages[expectedStage.sessionID]; newerStage != nil {
		result.Status = SessionStale
		return finish(errDeletionGenerationStale)
	}
	receipt := scan.receipts[deletionReceiptKey(expectedStage.sessionID, expectedStage.revision)]
	if receipt == nil {
		if expectedStage.receipt == nil {
			return finish(fmt.Errorf(
				"%w: unreceipted deletion stage vanished during cleanup",
				ErrSessionStoreUnsafe,
			))
		}
		partitionIdentity, entries, err := boundedPartitionSnapshot(scan.partition)
		if err != nil {
			return finish(err)
		}
		if err := manager.rejectMovedReceiptIdentity(
			scan.partition,
			partitionIdentity,
			entries,
			expectedStage.receipt,
		); err != nil {
			return finish(fmt.Errorf("%w: reconcile retired deletion receipt: %v", ErrSessionStoreUnsafe, err))
		}
		result.Status = SessionNotFound
		return finish(nil)
	}
	if expectedStage.receipt != nil &&
		!sameDeletionReceipt(receipt.receipt, expectedStage.receipt.receipt) {
		return finish(fmt.Errorf("%w: deletion receipt changed after staged cleanup", ErrSessionStoreUnsafe))
	}
	var err error
	if receipt.complete {
		err = manager.confirmCompletedReceipt(
			scan.partition,
			receiptRoot,
			registryLock,
			receipt,
		)
	} else {
		err = manager.completeReceiptAfterAbsentStage(
			scan.partition,
			receiptRoot,
			registryLock,
			receipt,
		)
	}
	if errors.Is(err, errDeletionGenerationStale) {
		result.Status = SessionStale
		return finish(err)
	}
	if err != nil {
		result.Status = SessionDeleteIncomplete
		return finish(err)
	}
	result.Status = SessionDeleted
	return finish(nil)
}

func (manager *SessionManager) scanWorkspace(
	ctx context.Context,
	recoverIntentID string,
	budget *digestBudget,
) (*workspaceScan, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := budget.beginPhase(); err != nil {
		return nil, err
	}
	partition, exists, err := manager.OpenWorkspacePartition()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSessionStoreUnsafe, err)
	}
	scan := &workspaceScan{
		partition:    partition,
		candidates:   make(map[string]*sessionCandidate),
		stages:       make(map[string]*deletionStage),
		receipts:     make(map[string]*deletionReceiptRecord),
		receiptTemps: make(map[string]*deletionReceiptTemp),
		receiptGC:    make(map[string]*platform.OwnedDirectory),
		visible:      []SessionInventoryItem{},
	}
	if !exists {
		if err := manager.verify(); err != nil {
			return nil, fmt.Errorf("%w: sessions root changed after empty inventory: %v", ErrSessionStoreUnsafe, err)
		}
		return scan, nil
	}
	root, err := partition.OpenRoot()
	if err != nil {
		return nil, fmt.Errorf("%w: open workspace inventory: %v", ErrSessionStoreUnsafe, err)
	}
	partitionIdentity, err := rootedDirectoryIdentity(root)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("%w: inspect workspace identity: %v", ErrSessionStoreUnsafe, err)
	}
	scan.partitionIdentity = partitionIdentity
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("%w: open workspace entries: %v", ErrSessionStoreUnsafe, err)
	}
	entries, readErr := directory.ReadDir(MaxWorkspaceEntries + 1)
	closeErr := errors.Join(directory.Close(), root.Close())
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("%w: enumerate workspace entries: %v", ErrSessionStoreUnsafe, errors.Join(readErr, closeErr))
	}
	if closeErr != nil {
		return nil, fmt.Errorf("%w: close workspace inventory: %v", ErrSessionStoreUnsafe, closeErr)
	}
	if len(entries) > MaxWorkspaceEntries {
		return nil, fmt.Errorf("%w: workspace entry bound exceeded", ErrSessionStoreUnsafe)
	}
	scan.entryCount = len(entries)
	for _, entry := range entries {
		if entry.Name() == deleteReceiptRoot {
			scan.receiptRootExists = true
			break
		}
	}
	receipts, receiptTemps, receiptGC, registryEvidence, err := manager.scanDeletionReceipts(
		ctx,
		partition,
		partitionIdentity,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: unsafe deletion receipt store: %v", ErrSessionStoreUnsafe, err)
	}
	scan.receipts = receipts
	scan.receiptTemps = receiptTemps
	scan.receiptGC = receiptGC
	scan.receiptRegistryLockEvidence = registryEvidence

	stageCount := 0
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		name := entry.Name()
		if strings.HasPrefix(name, deleteReceiptRoot) {
			if name != deleteReceiptRoot {
				return nil, fmt.Errorf("%w: malformed deletion receipt namespace", ErrSessionStoreUnsafe)
			}
			continue
		}
		if strings.HasPrefix(name, deleteStagePrefix) {
			stageCount++
			if stageCount > MaxDeletionStages {
				return nil, fmt.Errorf("%w: deletion staging bound exceeded", ErrSessionStoreUnsafe)
			}
			sessionID, revision, parseErr := parseDeletionStageName(name)
			if parseErr != nil {
				return nil, fmt.Errorf("%w: malformed deletion staging entry", ErrSessionStoreUnsafe)
			}
			if scan.stages[sessionID] != nil {
				return nil, fmt.Errorf("%w: duplicate deletion staging entry", ErrSessionStoreUnsafe)
			}
			stage, openErr := manager.inspectDeletionStage(
				partition,
				partitionIdentity,
				name,
				sessionID,
				revision,
				scan.receipts[deletionReceiptKey(sessionID, revision)],
				budget,
			)
			if openErr != nil {
				return nil, fmt.Errorf("%w: unsafe deletion staging entry: %v", ErrSessionStoreUnsafe, openErr)
			}
			scan.stages[sessionID] = stage
			continue
		}
		if !ValidSessionID(name) {
			continue
		}
		candidate, inspectErr := manager.inspectSessionCandidate(
			ctx,
			partition,
			partitionIdentity,
			name,
			recoverIntentID,
			budget,
		)
		if inspectErr != nil {
			return nil, fmt.Errorf("%w: unsafe session candidate: %v", ErrSessionStoreUnsafe, inspectErr)
		}
		if candidate == nil {
			continue
		}
		scan.candidates[name] = candidate
		if !candidate.incompleteFork && !candidate.deletionPending && candidate.item.Revision != "" {
			scan.visible = append(scan.visible, candidate.item)
		}
	}
	for _, candidate := range scan.candidates {
		if !candidate.deletionPending {
			continue
		}
		if !scan.receiptRootExists {
			return nil, fmt.Errorf("%w: live deletion intent lacks its receipt registry", ErrSessionStoreUnsafe)
		}
		receiptRoot, err := partition.InspectPrivateChild(deleteReceiptRoot)
		if err != nil {
			return nil, fmt.Errorf("%w: inspect live-intent receipt registry: %v", ErrSessionStoreUnsafe, err)
		}
		if _, err := inspectReceiptRegistryLock(receiptRoot); err != nil {
			return nil, fmt.Errorf("%w: live deletion intent lacks its persistent registry lock: %v", ErrSessionStoreUnsafe, err)
		}
	}
	for id := range scan.stages {
		if scan.candidates[id] != nil {
			return nil, fmt.Errorf("%w: live session overlaps deletion staging", ErrSessionStoreUnsafe)
		}
	}
	for key, temp := range scan.receiptTemps {
		var expected deletionReceipt
		var receiptErr error
		if stage := scan.stages[temp.sessionID]; stage != nil && stage.revision == temp.revision {
			expected, receiptErr = manager.receiptForStage(partitionIdentity, stage)
		} else if candidate := scan.candidates[temp.sessionID]; candidate != nil &&
			candidate.item.Revision == temp.revision &&
			candidate.intent != nil {
			expected = manager.receiptForCandidate(partitionIdentity, candidate, *candidate.intent)
		} else {
			receiptErr = errors.New("temporary deletion receipt has no matching transaction")
		}
		if receiptErr != nil {
			return nil, fmt.Errorf("%w: %v", ErrSessionStoreUnsafe, receiptErr)
		}
		encoded, encodeErr := deletionReceiptData(expected)
		if encodeErr != nil || len(temp.data) > len(encoded) ||
			!bytes.Equal(temp.data, encoded[:len(temp.data)]) ||
			deletionReceiptKey(expected.SessionID, expected.Revision) != key {
			return nil, fmt.Errorf("%w: temporary deletion receipt does not match transaction", ErrSessionStoreUnsafe)
		}
		expectedName, nameErr := deletionReceiptEntryName(expected)
		if nameErr != nil || scan.receiptGC[deletionReceiptGCName(expectedName)] != nil {
			return nil, fmt.Errorf("%w: temporary and GC receipt identities overlap", ErrSessionStoreUnsafe)
		}
	}
	pendingByID := make(map[string]string)
	for key, receipt := range scan.receipts {
		stage := scan.stages[receipt.receipt.SessionID]
		if receipt.complete && stage != nil && deletionReceiptKey(stage.sessionID, stage.revision) == key {
			return nil, fmt.Errorf("%w: completed receipt overlaps deletion staging", ErrSessionStoreUnsafe)
		}
		candidate := scan.candidates[receipt.receipt.SessionID]
		if receipt.complete && candidate != nil && candidate.item.Revision == receipt.receipt.Revision {
			return nil, fmt.Errorf("%w: completed receipt overlaps the same live generation", ErrSessionStoreUnsafe)
		}
		if !receipt.complete {
			if previous := pendingByID[receipt.receipt.SessionID]; previous != "" && previous != key {
				return nil, fmt.Errorf("%w: multiple pending receipts reserve one session ID", ErrSessionStoreUnsafe)
			}
			pendingByID[receipt.receipt.SessionID] = key
			if stage != nil && deletionReceiptKey(stage.sessionID, stage.revision) != key {
				return nil, fmt.Errorf("%w: pending receipt overlaps a different staged generation", ErrSessionStoreUnsafe)
			}
			if candidate != nil && candidate.item.Revision != receipt.receipt.Revision {
				return nil, fmt.Errorf("%w: pending receipt overlaps a different live generation", ErrSessionStoreUnsafe)
			}
			if candidate != nil && (candidate.intent == nil ||
				!sameDeletionIntent(*candidate.intent, receiptIntent(receipt.receipt))) {
				return nil, fmt.Errorf("%w: pending receipt lacks matching live deletion intent", ErrSessionStoreUnsafe)
			}
			if stage == nil && candidate == nil {
				if err := manager.rejectMovedReceiptIdentity(
					partition,
					partitionIdentity,
					entries,
					receipt,
				); err != nil {
					return nil, fmt.Errorf("%w: pending receipt cleanup is ambiguous: %v", ErrSessionStoreUnsafe, err)
				}
			}
		}
	}
	if err := errors.Join(partition.Verify(), manager.verify()); err != nil {
		return nil, fmt.Errorf("%w: session store changed at scan completion: %v", ErrSessionStoreUnsafe, err)
	}
	sort.Slice(scan.visible, func(i, j int) bool {
		left, right := scan.visible[i], scan.visible[j]
		if left.updatedTime.Equal(right.updatedTime) {
			return left.SessionID < right.SessionID
		}
		return left.updatedTime.After(right.updatedTime)
	})
	return scan, nil
}

func (scan *workspaceScan) hasPendingReceipt(sessionID string) bool {
	if scan == nil {
		return false
	}
	for _, receipt := range scan.receipts {
		if receipt.receipt.SessionID == sessionID && !receipt.complete {
			return true
		}
	}
	return false
}

func (manager *SessionManager) rejectMovedReceiptIdentity(
	partition *platform.OwnedDirectory,
	partitionIdentity string,
	entries []os.DirEntry,
	receipt *deletionReceiptRecord,
) error {
	if partition == nil || receipt == nil {
		return errors.New("pending receipt identity is unavailable")
	}
	root, err := partition.OpenRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	for _, entry := range entries {
		name := entry.Name()
		if name == deleteReceiptRoot || name == receipt.receipt.StagingName {
			continue
		}
		info, err := root.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		owner, err := partition.InspectPrivateChild(name)
		if err != nil {
			return fmt.Errorf("cannot rule out moved staged directory %q: %w", name, err)
		}
		ownerRoot, err := owner.OpenRoot()
		if err != nil {
			return err
		}
		identity, identityErr := rootedDirectoryIdentity(ownerRoot)
		closeErr := ownerRoot.Close()
		if identityErr != nil || closeErr != nil {
			return errors.Join(identityErr, closeErr)
		}
		if manager.directoryBinding(
			partitionIdentity,
			receipt.receipt.SessionID,
			identity,
		) == receipt.receipt.DirectoryBinding {
			return errors.New("bound staged directory remains under another workspace entry")
		}
	}
	return errors.Join(partition.Verify(), manager.verify())
}

func (manager *SessionManager) inspectDeletionStage(
	partition *platform.OwnedDirectory,
	partitionIdentity string,
	name string,
	sessionID string,
	revision string,
	receipt *deletionReceiptRecord,
	budget *digestBudget,
) (_ *deletionStage, resultErr error) {
	if receipt != nil && receipt.complete {
		return nil, errors.New("completed deletion receipt retains a stage")
	}
	owner, err := partition.InspectPrivateChild(name)
	if err != nil {
		return nil, err
	}
	root, err := owner.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directoryIdentity, err := rootedDirectoryIdentity(root)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		if receipt.receipt.PartitionBinding != manager.partitionBinding(partitionIdentity) ||
			receipt.receipt.DirectoryBinding != manager.directoryBinding(
				partitionIdentity,
				sessionID,
				directoryIdentity,
			) {
			return nil, errors.New("deletion receipt does not bind the staged directory")
		}
	}
	if exists, _, _, err := inspectOptionalRegular(root, deleteIntentTempName, maxIntentBytes, true); err != nil {
		return nil, err
	} else if exists {
		return nil, errors.New("detached session retains an uncommitted intent")
	}
	intentExists, _, intentData, err := inspectOptionalRegular(root, deleteIntentName, maxIntentBytes, true)
	if err != nil {
		return nil, err
	}
	var intent *deletionIntent
	if intentExists {
		intent, err = decodeDeletionIntent(intentData)
		if err != nil {
			return nil, err
		}
		if intent.SessionID != sessionID || intent.Revision != revision || intent.StagingName != name {
			return nil, errors.New("detached session intent does not match staging identity")
		}
	}
	if receipt == nil && !intentExists {
		return nil, errors.New("deletion stage lacks both a receipt and committed intent")
	}
	if receipt != nil && intentExists {
		expected := receiptIntent(receipt.receipt)
		if !sameDeletionIntent(*intent, expected) {
			return nil, errors.New("detached session intent does not match its receipt")
		}
	}
	if exists, _, _, err := inspectOptionalRegular(root, forkIncompleteMarker, 64, true); err != nil {
		return nil, err
	} else if exists {
		return nil, errors.New("detached session is marked as an incomplete fork")
	}
	transcriptExists, transcriptEvidence, _, err := inspectOptionalRegularDigest(
		root,
		transcriptName,
		DefaultMaxFileBytes,
		budget,
	)
	if err != nil {
		return nil, err
	}
	lockExists, lockEvidence, _, err := inspectOptionalRegular(root, sessionLockName, maxIntentBytes, false)
	if err != nil {
		return nil, err
	}
	if transcriptExists && transcriptEvidence.size == 0 {
		return nil, errors.New("detached transcript is empty")
	}
	if receipt == nil {
		if !transcriptExists || !lockExists {
			return nil, errors.New("partial deletion stage lacks a durable receipt")
		}
		if sessionRevision(
			manager.workspaceKey,
			partitionIdentity,
			sessionID,
			directoryIdentity,
			transcriptEvidence,
			&lockEvidence,
		) != revision {
			return nil, errors.New("detached transcript does not match staging revision")
		}
	} else {
		if transcriptExists && receipt.receipt.TranscriptBinding !=
			manager.transcriptBinding(partitionIdentity, sessionID, directoryIdentity, transcriptEvidence) {
			return nil, errors.New("detached transcript does not match its deletion receipt")
		}
		if lockExists && receipt.receipt.LockBinding !=
			manager.lockBinding(partitionIdentity, sessionID, directoryIdentity, lockEvidence) {
			return nil, errors.New("detached lock does not match its deletion receipt")
		}
	}
	if err := errors.Join(owner.Verify(), partition.Verify(), manager.verify()); err != nil {
		return nil, err
	}
	stage := &deletionStage{
		sessionID:         sessionID,
		revision:          revision,
		name:              name,
		owner:             owner,
		partitionIdentity: partitionIdentity,
		directoryIdentity: directoryIdentity,
		intent:            intent,
		receipt:           receipt,
	}
	if transcriptExists {
		stage.transcriptEvidence = &transcriptEvidence
	}
	if lockExists {
		stage.lockEvidence = &lockEvidence
	}
	return stage, nil
}

func (manager *SessionManager) inspectSessionCandidate(
	ctx context.Context,
	partition *platform.OwnedDirectory,
	partitionIdentity string,
	sessionID string,
	recoverIntentID string,
	budget *digestBudget,
) (_ *sessionCandidate, resultErr error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	owner, err := partition.InspectPrivateChild(sessionID)
	if err != nil {
		return nil, err
	}
	root, err := owner.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directoryIdentity, err := rootedDirectoryIdentity(root)
	if err != nil {
		return nil, err
	}

	forkExists, _, _, err := inspectOptionalRegular(root, forkIncompleteMarker, 64, false)
	if err != nil {
		return nil, fmt.Errorf("inspect incomplete-fork marker: %w", err)
	}
	candidate := &sessionCandidate{
		owner:             owner,
		directoryIdentity: directoryIdentity,
		incompleteFork:    forkExists,
	}

	finalExists, finalEvidence, finalData, finalErr := inspectOptionalRegular(root, deleteIntentName, maxIntentBytes, true)
	tempExists, tempEvidence, tempData, tempErr := inspectOptionalRegular(root, deleteIntentTempName, maxIntentBytes, true)
	if finalErr != nil || tempErr != nil {
		return nil, errors.Join(finalErr, tempErr)
	}
	var finalIntent, tempIntent *deletionIntent
	var finalDecodeErr, tempDecodeErr error
	if finalExists {
		finalIntent, finalDecodeErr = decodeDeletionIntent(finalData)
	}
	if tempExists {
		tempIntent, tempDecodeErr = decodeDeletionIntent(tempData)
	}
	if finalExists || tempExists {
		candidate.deletionPending = true
	}

	transcriptExists, transcriptEvidence, _, err := inspectOptionalRegularDigest(
		root,
		transcriptName,
		DefaultMaxFileBytes,
		budget,
	)
	if err != nil {
		return nil, fmt.Errorf("inspect transcript identity: %w", err)
	}
	lockExists, lockEvidence, _, err := inspectOptionalRegular(root, sessionLockName, maxIntentBytes, false)
	if err != nil {
		return nil, fmt.Errorf("inspect session lock identity: %w", err)
	}
	if lockExists {
		candidate.lockEvidence = &lockEvidence
	}
	if !transcriptExists || transcriptEvidence.size == 0 {
		if finalExists || tempExists {
			return nil, errors.New("deletion intent belongs to an incomplete session")
		}
		if err := owner.Verify(); err != nil {
			return nil, err
		}
		return candidate, nil
	}
	if !lockExists {
		return nil, errors.New("nonempty native transcript is missing its session lock")
	}
	candidate.transcriptEvidence = transcriptEvidence
	candidate.item = SessionInventoryItem{
		SessionID: sessionID,
		UpdatedAt: canonicalUpdatedAt(transcriptEvidence.modTime),
		Revision: sessionRevision(
			manager.workspaceKey,
			partitionIdentity,
			sessionID,
			directoryIdentity,
			transcriptEvidence,
			candidate.lockEvidence,
		),
		updatedTime: transcriptEvidence.modTime,
	}
	if finalExists || tempExists {
		expected := deletionIntent{
			Version:     SessionManagementVersion,
			SessionID:   sessionID,
			Revision:    candidate.item.Revision,
			StagingName: deletionStageName(sessionID, candidate.item.Revision),
		}
		encoded, encodeErr := json.Marshal(expected)
		if encodeErr != nil {
			return nil, encodeErr
		}
		encoded = append(encoded, '\n')
		finalExact := finalExists && finalDecodeErr == nil && finalIntent != nil &&
			sameDeletionIntent(*finalIntent, expected) &&
			privateMetadataModeSafe(finalEvidence.mode)
		tempExact := tempExists && tempDecodeErr == nil && tempIntent != nil &&
			sameDeletionIntent(*tempIntent, expected) &&
			privateMetadataModeSafe(tempEvidence.mode)
		finalPrefix := finalExists && privateMetadataModeSafe(finalEvidence.mode) &&
			len(finalData) < len(encoded) && bytes.Equal(finalData, encoded[:len(finalData)])
		tempPrefix := tempExists && privateMetadataModeSafe(tempEvidence.mode) &&
			len(tempData) <= len(encoded) && bytes.Equal(tempData, encoded[:len(tempData)])
		switch {
		case finalExact && !tempExists:
			candidate.intent = &expected
		case finalExact && tempExact:
			candidate.intent = &expected
			candidate.recoverableIntent = true
		case tempExact && (!finalExists || finalPrefix):
			candidate.intent = &expected
			candidate.recoverableIntent = true
		case !finalExists && tempPrefix:
			candidate.intent = &expected
			candidate.recoverableIntent = true
		case sessionID == recoverIntentID &&
			finalDecodeErr == nil && finalIntent != nil &&
			finalIntent.SessionID == sessionID &&
			finalIntent.StagingName == deletionStageName(sessionID, finalIntent.Revision) &&
			privateMetadataModeSafe(finalEvidence.mode) &&
			(!tempExists || (tempDecodeErr == nil && tempIntent != nil &&
				sameDeletionIntent(*tempIntent, *finalIntent) &&
				privateMetadataModeSafe(tempEvidence.mode))):
			candidate.intent = finalIntent
			candidate.recoverableIntent = true
		default:
			return nil, errors.New("deletion intent is malformed or belongs to another revision")
		}
	}
	if err := owner.Verify(); err != nil {
		return nil, err
	}
	return candidate, nil
}

func rootedDirectoryIdentity(root *os.Root) (result string, resultErr error) {
	handle, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer func() {
		resultErr = errors.Join(resultErr, handle.Close())
	}()
	info, err := handle.Stat()
	if err != nil || !info.IsDir() {
		return "", errors.New("session directory identity is unavailable")
	}
	identity, _, err := openedFilesystemIdentity(handle, info)
	if err != nil {
		return "", err
	}
	after, err := root.Stat(".")
	if err != nil || !after.IsDir() || !os.SameFile(info, after) {
		return "", errors.New("session directory changed while inspecting")
	}
	return identity, nil
}

func inspectOptionalRegular(
	root *os.Root,
	name string,
	maxBytes int64,
	read bool,
) (exists bool, evidence fileEvidence, data []byte, resultErr error) {
	return inspectOptionalRegularBounded(root, name, maxBytes, read, nil, false)
}

func inspectOptionalRegularDigest(
	root *os.Root,
	name string,
	maxBytes int64,
	budget *digestBudget,
) (exists bool, evidence fileEvidence, data []byte, resultErr error) {
	return inspectOptionalRegularBounded(root, name, maxBytes, false, budget, true)
}

func inspectOptionalRegularBounded(
	root *os.Root,
	name string,
	maxBytes int64,
	read bool,
	budget *digestBudget,
	contentDigest bool,
) (exists bool, evidence fileEvidence, data []byte, resultErr error) {
	// The initial lookup is only a no-follow type guard. In particular, do
	// not retain its identity: Windows resolves os.Lstat identities lazily,
	// so a later os.SameFile call could otherwise adopt a replacement that
	// appeared after this lookup.
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, fileEvidence{}, nil, nil
	}
	if err != nil {
		return false, fileEvidence{}, nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	file, err := root.Open(name)
	if err != nil {
		return false, fileEvidence{}, nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, file.Close())
	}()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	if opened.Size() < 0 || opened.Size() > maxBytes {
		return false, fileEvidence{}, nil, ErrResourceLimit
	}
	identity, links, err := openedFilesystemIdentity(file, opened)
	if err != nil || links != 1 {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	current, err := root.Lstat(name)
	if err != nil || !current.Mode().IsRegular() || current.Mode()&os.ModeSymlink != 0 ||
		!sameRegularSnapshot(opened, current) {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	var digest [sha256.Size]byte
	if read || contentDigest {
		if contentDigest {
			if err := budget.claim(opened.Size()); err != nil {
				return false, fileEvidence{}, nil, err
			}
		}
		limited := io.LimitReader(file, maxBytes+1)
		var count int64
		if read {
			data, err = io.ReadAll(limited)
			count = int64(len(data))
		} else {
			hash := sha256.New()
			count, err = io.Copy(hash, limited)
			copy(digest[:], hash.Sum(nil))
		}
		if err != nil || count > maxBytes || count != opened.Size() {
			return false, fileEvidence{}, nil, ErrResourceLimit
		}
	}
	finalInfo, err := file.Stat()
	if err != nil || !sameRegularSnapshot(opened, finalInfo) {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	finalIdentity, finalLinks, err := openedFilesystemIdentity(file, finalInfo)
	if err != nil || finalLinks != 1 || finalIdentity != identity {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	after, err := root.Lstat(name)
	if err != nil || !after.Mode().IsRegular() || after.Mode()&os.ModeSymlink != 0 ||
		!sameRegularSnapshot(finalInfo, after) {
		return false, fileEvidence{}, nil, ErrUnsafePath
	}
	return true, fileEvidence{
		identity:  identity,
		size:      finalInfo.Size(),
		modTime:   finalInfo.ModTime(),
		mode:      finalInfo.Mode(),
		digest:    digest,
		hasDigest: contentDigest,
	}, data, nil
}

func sameRegularSnapshot(left, right os.FileInfo) bool {
	return left != nil && right != nil &&
		left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) &&
		left.Mode() == right.Mode()
}

func privateMetadataModeSafe(mode os.FileMode) bool {
	if !mode.IsRegular() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return mode.Perm() == 0o600
}

func sessionRevision(
	workspaceKey string,
	partitionIdentity string,
	sessionID string,
	directoryIdentity string,
	transcript fileEvidence,
	lock *fileEvidence,
) string {
	hash := sha256.New()
	writeHashField(hash, "agentx-native-session-revision-v1")
	writeHashField(hash, workspaceKey)
	writeHashField(hash, partitionIdentity)
	writeHashField(hash, sessionID)
	writeHashField(hash, directoryIdentity)
	writeHashField(hash, transcript.identity)
	writeHashField(hash, strconv.FormatInt(transcript.size, 10))
	writeHashTime(hash, transcript.modTime)
	writeHashField(hash, strconv.FormatUint(uint64(transcript.mode), 10))
	if transcript.hasDigest {
		writeHashField(hash, base64.RawURLEncoding.EncodeToString(transcript.digest[:]))
	} else {
		writeHashField(hash, "transcript-digest-missing")
	}
	if lock == nil {
		writeHashField(hash, "lock-missing")
	} else {
		writeHashField(hash, lock.identity)
		writeHashField(hash, strconv.FormatInt(lock.size, 10))
		writeHashTime(hash, lock.modTime)
		writeHashField(hash, strconv.FormatUint(uint64(lock.mode), 10))
	}
	return "r1_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func canonicalUpdatedAt(value time.Time) string {
	utc := value.UTC()
	if utc.Year() < 0 || utc.Year() > 9999 {
		return ""
	}
	encoded := utc.Format(time.RFC3339Nano)
	parsed, err := time.Parse(time.RFC3339Nano, encoded)
	if err != nil || !parsed.Equal(utc) || parsed.UTC().Format(time.RFC3339Nano) != encoded {
		return ""
	}
	return encoded
}

func writeHashField(writer io.Writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = io.WriteString(writer, value)
}

func writeHashTime(writer io.Writer, value time.Time) {
	writeHashField(writer, strconv.FormatInt(value.Unix(), 10))
	writeHashField(writer, strconv.Itoa(value.Nanosecond()))
}

func inventoryGeneration(items []SessionInventoryItem) [32]byte {
	hash := sha256.New()
	writeHashField(hash, "agentx-native-session-inventory-v1")
	for _, item := range items {
		writeHashField(hash, item.SessionID)
		writeHashField(hash, item.UpdatedAt)
		writeHashField(hash, item.Revision)
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result
}

func encodePageToken(offset int, generation [32]byte) string {
	var raw [37]byte
	raw[0] = 1
	binary.BigEndian.PutUint32(raw[1:5], uint32(offset))
	copy(raw[5:], generation[:])
	return "p1_" + base64.RawURLEncoding.EncodeToString(raw[:])
}

func decodePageToken(token string, generation [32]byte) (int, error) {
	const encodedPayloadLength = 50 // base64.RawURLEncoding.EncodedLen(37)
	if !strings.HasPrefix(token, "p1_") ||
		len(strings.TrimPrefix(token, "p1_")) != encodedPayloadLength {
		return 0, ErrSessionPageStale
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "p1_"))
	if err != nil || len(raw) != 37 || raw[0] != 1 || !bytes.Equal(raw[5:], generation[:]) {
		return 0, ErrSessionPageStale
	}
	offset := binary.BigEndian.Uint32(raw[1:5])
	if uint64(offset) > uint64(^uint(0)>>1) {
		return 0, ErrSessionPageStale
	}
	return int(offset), nil
}

func validRevision(revision string) bool {
	const encodedDigestLength = 43 // base64.RawURLEncoding.EncodedLen(sha256.Size)
	if !strings.HasPrefix(revision, "r1_") ||
		len(strings.TrimPrefix(revision, "r1_")) != encodedDigestLength {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(revision, "r1_"))
	return err == nil && len(raw) == sha256.Size
}

func deletionStageName(sessionID, revision string) string {
	return fmt.Sprintf("%s%03d-%s-%s", deleteStagePrefix, len(sessionID), sessionID, revision)
}

func parseDeletionStageName(name string) (string, string, error) {
	if !strings.HasPrefix(name, deleteStagePrefix) {
		return "", "", errors.New("not a deletion staging name")
	}
	rest := strings.TrimPrefix(name, deleteStagePrefix)
	if len(rest) < 4 || rest[3] != '-' {
		return "", "", errors.New("deletion staging length is missing")
	}
	idLength, err := strconv.Atoi(rest[:3])
	if err != nil || idLength < 1 || idLength > 128 {
		return "", "", errors.New("deletion staging length is invalid")
	}
	rest = rest[4:]
	if len(rest) <= idLength || rest[idLength] != '-' {
		return "", "", errors.New("deletion staging identifier is truncated")
	}
	sessionID, revision := rest[:idLength], rest[idLength+1:]
	if !ValidSessionID(sessionID) || !validRevision(revision) ||
		deletionStageName(sessionID, revision) != name {
		return "", "", errors.New("deletion staging identity is invalid")
	}
	return sessionID, revision, nil
}

func decodeDeletionIntent(data []byte) (*deletionIntent, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var intent deletionIntent
	if err := decoder.Decode(&intent); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("deletion intent has trailing data")
	}
	if intent.Version != SessionManagementVersion ||
		!ValidSessionID(intent.SessionID) ||
		!validRevision(intent.Revision) ||
		intent.StagingName != deletionStageName(intent.SessionID, intent.Revision) {
		return nil, errors.New("deletion intent fields are invalid")
	}
	return &intent, nil
}

func sameDeletionIntent(left, right deletionIntent) bool {
	return left.Version == right.Version &&
		left.SessionID == right.SessionID &&
		left.Revision == right.Revision &&
		left.StagingName == right.StagingName
}

func deletionReceiptKey(sessionID, revision string) string {
	return sessionID + "\x00" + revision
}

func receiptIntent(receipt deletionReceipt) deletionIntent {
	return deletionIntent{
		Version:     receipt.Version,
		SessionID:   receipt.SessionID,
		Revision:    receipt.Revision,
		StagingName: receipt.StagingName,
	}
}

func (manager *SessionManager) partitionBinding(partitionIdentity string) string {
	return opaqueBinding("agentx-delete-partition-v1", manager.workspaceKey, partitionIdentity)
}

func (manager *SessionManager) directoryBinding(
	partitionIdentity string,
	sessionID string,
	directoryIdentity string,
) string {
	return opaqueBinding(
		"agentx-delete-directory-v1",
		manager.workspaceKey,
		partitionIdentity,
		sessionID,
		directoryIdentity,
	)
}

func (manager *SessionManager) transcriptBinding(
	partitionIdentity string,
	sessionID string,
	directoryIdentity string,
	evidence fileEvidence,
) string {
	return manager.evidenceBinding(
		"agentx-delete-transcript-v1",
		partitionIdentity,
		sessionID,
		directoryIdentity,
		evidence,
	)
}

func (manager *SessionManager) lockBinding(
	partitionIdentity string,
	sessionID string,
	directoryIdentity string,
	evidence fileEvidence,
) string {
	return manager.evidenceBinding(
		"agentx-delete-lock-v1",
		partitionIdentity,
		sessionID,
		directoryIdentity,
		evidence,
	)
}

func (manager *SessionManager) evidenceBinding(
	domain string,
	partitionIdentity string,
	sessionID string,
	directoryIdentity string,
	evidence fileEvidence,
) string {
	digest := "digest-missing"
	if evidence.hasDigest {
		digest = base64.RawURLEncoding.EncodeToString(evidence.digest[:])
	}
	hash := sha256.New()
	for _, value := range []string{
		domain,
		manager.workspaceKey,
		partitionIdentity,
		sessionID,
		directoryIdentity,
		evidence.identity,
		strconv.FormatInt(evidence.size, 10),
	} {
		writeHashField(hash, value)
	}
	writeHashTime(hash, evidence.modTime)
	writeHashField(hash, strconv.FormatUint(uint64(evidence.mode), 10))
	writeHashField(hash, digest)
	return "b1_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func opaqueBinding(domain string, fields ...string) string {
	hash := sha256.New()
	writeHashField(hash, domain)
	for _, field := range fields {
		writeHashField(hash, field)
	}
	return "b1_" + base64.RawURLEncoding.EncodeToString(hash.Sum(nil))
}

func validOpaqueBinding(value string) bool {
	if !strings.HasPrefix(value, "b1_") {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, "b1_"))
	return err == nil && len(raw) == sha256.Size
}

func deletionReceiptData(receipt deletionReceipt) ([]byte, error) {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxReceiptBytes {
		return nil, ErrResourceLimit
	}
	return encoded, nil
}

func deletionReceiptEntryName(receipt deletionReceipt) (string, error) {
	encoded, err := deletionReceiptData(receipt)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return deleteReceiptPrefix + base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func validDeletionReceiptEntryName(name string) bool {
	if !strings.HasPrefix(name, deleteReceiptPrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(name, deleteReceiptPrefix))
	return err == nil && len(raw) == sha256.Size
}

func deletionReceiptTempName(sessionID, revision string) string {
	return fmt.Sprintf("%s%03d-%s-%s", deleteReceiptTempPrefix, len(sessionID), sessionID, revision)
}

func parseDeletionReceiptTempName(name string) (string, string, error) {
	if !strings.HasPrefix(name, deleteReceiptTempPrefix) {
		return "", "", errors.New("not a temporary deletion receipt")
	}
	rest := strings.TrimPrefix(name, deleteReceiptTempPrefix)
	if len(rest) < 4 || rest[3] != '-' {
		return "", "", errors.New("temporary deletion receipt length is missing")
	}
	idLength, err := strconv.Atoi(rest[:3])
	if err != nil || idLength < 1 || idLength > 128 {
		return "", "", errors.New("temporary deletion receipt length is invalid")
	}
	rest = rest[4:]
	if len(rest) <= idLength || rest[idLength] != '-' {
		return "", "", errors.New("temporary deletion receipt identifier is truncated")
	}
	sessionID, revision := rest[:idLength], rest[idLength+1:]
	if !ValidSessionID(sessionID) || !validRevision(revision) ||
		deletionReceiptTempName(sessionID, revision) != name {
		return "", "", errors.New("temporary deletion receipt identity is invalid")
	}
	return sessionID, revision, nil
}

func decodeDeletionReceipt(data []byte) (*deletionReceipt, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt deletionReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("deletion receipt has trailing data")
	}
	if receipt.Version != SessionManagementVersion ||
		!ValidSessionID(receipt.SessionID) ||
		!validRevision(receipt.Revision) ||
		receipt.StagingName != deletionStageName(receipt.SessionID, receipt.Revision) ||
		!validOpaqueBinding(receipt.PartitionBinding) ||
		!validOpaqueBinding(receipt.DirectoryBinding) ||
		!validOpaqueBinding(receipt.TranscriptBinding) ||
		!validOpaqueBinding(receipt.LockBinding) {
		return nil, errors.New("deletion receipt fields are invalid")
	}
	canonical, err := deletionReceiptData(receipt)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, errors.New("deletion receipt is not canonical")
	}
	return &receipt, nil
}

func sameDeletionReceipt(left, right deletionReceipt) bool {
	return left == right
}

func (manager *SessionManager) scanDeletionReceipts(
	ctx context.Context,
	partition *platform.OwnedDirectory,
	partitionIdentity string,
) (
	map[string]*deletionReceiptRecord,
	map[string]*deletionReceiptTemp,
	map[string]*platform.OwnedDirectory,
	*fileEvidence,
	error,
) {
	records := make(map[string]*deletionReceiptRecord)
	temps := make(map[string]*deletionReceiptTemp)
	gcStages := make(map[string]*platform.OwnedDirectory)
	partitionRoot, err := partition.OpenRoot()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	_, statErr := partitionRoot.Lstat(deleteReceiptRoot)
	closeErr := partitionRoot.Close()
	if errors.Is(statErr, os.ErrNotExist) && closeErr == nil {
		return records, temps, gcStages, nil, errors.Join(partition.Verify(), manager.verify())
	}
	if statErr != nil || closeErr != nil {
		return nil, nil, nil, nil, errors.Join(statErr, closeErr)
	}
	receiptRoot, err := partition.InspectPrivateChild(deleteReceiptRoot)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	root, err := receiptRoot.OpenRoot()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, nil, nil, nil, err
	}
	receiptLimit := manager.receiptEntryLimit()
	entries, readErr := directory.ReadDir(receiptLimit + 2)
	closeErr = errors.Join(directory.Close(), root.Close())
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, nil, nil, nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, nil, nil, nil, closeErr
	}
	entryCount := 0
	var registryEvidence *fileEvidence
	for _, entry := range entries {
		if err := contextError(ctx); err != nil {
			return nil, nil, nil, nil, err
		}
		if entry.Name() == deleteRegistryLockName {
			if registryEvidence != nil {
				return nil, nil, nil, nil, errors.New("duplicate deletion receipt registry lock")
			}
			evidence, err := inspectReceiptRegistryLock(receiptRoot)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			registryEvidence = &evidence
			continue
		}
		entryCount++
		if entryCount > receiptLimit {
			return nil, nil, nil, nil, ErrResourceLimit
		}
		if strings.HasPrefix(entry.Name(), deleteReceiptGCPrefix) {
			owner, err := receiptRoot.InspectPrivateChild(entry.Name())
			if err != nil {
				return nil, nil, nil, nil, err
			}
			if err := manager.inspectDeletionReceiptGC(owner, entry.Name()); err != nil {
				return nil, nil, nil, nil, err
			}
			if gcStages[entry.Name()] != nil {
				return nil, nil, nil, nil, errors.New("duplicate deletion receipt GC stage")
			}
			gcStages[entry.Name()] = owner
			continue
		}
		if strings.HasPrefix(entry.Name(), deleteReceiptTempPrefix) {
			sessionID, revision, err := parseDeletionReceiptTempName(entry.Name())
			if err != nil {
				return nil, nil, nil, nil, err
			}
			key := deletionReceiptKey(sessionID, revision)
			if temps[key] != nil || records[key] != nil {
				return nil, nil, nil, nil, errors.New("duplicate temporary deletion receipt")
			}
			owner, err := receiptRoot.InspectPrivateChild(entry.Name())
			if err != nil {
				return nil, nil, nil, nil, err
			}
			temp, err := manager.inspectDeletionReceiptTemp(owner, entry.Name(), sessionID, revision)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			temps[key] = temp
			continue
		}
		if !validDeletionReceiptEntryName(entry.Name()) {
			return nil, nil, nil, nil, errors.New("malformed deletion receipt entry")
		}
		owner, err := receiptRoot.InspectPrivateChild(entry.Name())
		if err != nil {
			return nil, nil, nil, nil, err
		}
		record, err := manager.inspectDeletionReceipt(owner, entry.Name())
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if err := confirmDeletionReceiptDurability(owner); err != nil {
			return nil, nil, nil, nil, err
		}
		if record.receipt.PartitionBinding != manager.partitionBinding(partitionIdentity) {
			return nil, nil, nil, nil, errors.New("deletion receipt belongs to another workspace identity")
		}
		key := deletionReceiptKey(record.receipt.SessionID, record.receipt.Revision)
		if records[key] != nil || temps[key] != nil {
			return nil, nil, nil, nil, errors.New("duplicate deletion receipt")
		}
		records[key] = record
	}
	// A newly created empty receipt root may precede creation of its persistent
	// lock after a crash. Once any transaction metadata exists, however, the
	// lock inode must remain present: replacing it could let a second mutator
	// bypass an advisory lease still held on the original inode.
	if entryCount > 0 && registryEvidence == nil {
		return nil, nil, nil, nil, errors.New("deletion receipt registry lock is missing")
	}
	for _, record := range records {
		if gcStages[deletionReceiptGCName(record.name)] != nil {
			return nil, nil, nil, nil, errors.New("final and GC receipt identities overlap")
		}
	}
	if err := errors.Join(receiptRoot.Verify(), partition.Verify(), manager.verify()); err != nil {
		return nil, nil, nil, nil, err
	}
	return records, temps, gcStages, registryEvidence, nil
}

func inspectReceiptRegistryLock(
	receiptRoot *platform.OwnedDirectory,
) (_ fileEvidence, resultErr error) {
	root, err := receiptRoot.OpenRoot()
	if err != nil {
		return fileEvidence{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	exists, evidence, _, err := inspectOptionalRegular(
		root,
		deleteRegistryLockName,
		maxIntentBytes,
		false,
	)
	if err != nil || !exists || !privateMetadataModeSafe(evidence.mode) {
		return fileEvidence{}, errors.Join(errors.New("deletion receipt registry lock is unsafe"), err)
	}
	if err := receiptRoot.Verify(); err != nil {
		return fileEvidence{}, err
	}
	return evidence, nil
}

func deletionReceiptGCName(receiptName string) string {
	return deleteReceiptGCPrefix + strings.TrimPrefix(receiptName, deleteReceiptPrefix)
}

func validDeletionReceiptGCName(name string) bool {
	if !strings.HasPrefix(name, deleteReceiptGCPrefix) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(name, deleteReceiptGCPrefix))
	return err == nil && len(raw) == sha256.Size
}

func (manager *SessionManager) inspectDeletionReceiptGC(
	owner *platform.OwnedDirectory,
	name string,
) (resultErr error) {
	if !validDeletionReceiptGCName(name) {
		return errors.New("malformed deletion receipt GC stage")
	}
	root, err := owner.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(3)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) > 2 {
		return errors.New("deletion receipt GC stage content is over-bound")
	}
	for _, entry := range entries {
		switch entry.Name() {
		case deleteReceiptName:
			exists, evidence, data, err := inspectOptionalRegular(root, deleteReceiptName, maxReceiptBytes, true)
			if err != nil || !exists || !privateMetadataModeSafe(evidence.mode) {
				return errors.Join(errors.New("GC receipt record is unsafe"), err)
			}
			receipt, err := decodeDeletionReceipt(data)
			if err != nil {
				return err
			}
			receiptName, err := deletionReceiptEntryName(*receipt)
			if err != nil || deletionReceiptGCName(receiptName) != name {
				return errors.Join(errors.New("GC receipt record does not match its stage"), err)
			}
		case deleteCompleteName:
			exists, evidence, _, err := inspectOptionalRegular(root, deleteCompleteName, 0, false)
			if err != nil || !exists || evidence.size != 0 || !privateMetadataModeSafe(evidence.mode) {
				return errors.Join(errors.New("GC completion marker is unsafe"), err)
			}
		default:
			return errors.New("unexpected deletion receipt GC content")
		}
	}
	return owner.Verify()
}

func (manager *SessionManager) inspectDeletionReceipt(
	owner *platform.OwnedDirectory,
	name string,
) (_ *deletionReceiptRecord, resultErr error) {
	root, err := owner.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(3)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) < 1 || len(entries) > 2 {
		return nil, errors.New("deletion receipt entry count is invalid")
	}
	seenReceipt := false
	seenComplete := false
	for _, entry := range entries {
		switch entry.Name() {
		case deleteReceiptName:
			if seenReceipt {
				return nil, errors.New("duplicate receipt record")
			}
			seenReceipt = true
		case deleteCompleteName:
			if seenComplete {
				return nil, errors.New("duplicate completion marker")
			}
			seenComplete = true
		default:
			return nil, errors.New("unexpected deletion receipt content")
		}
	}
	if !seenReceipt {
		return nil, errors.New("deletion receipt record is missing")
	}
	exists, evidence, data, err := inspectOptionalRegular(root, deleteReceiptName, maxReceiptBytes, true)
	if err != nil || !exists || !privateMetadataModeSafe(evidence.mode) {
		return nil, errors.Join(errors.New("deletion receipt record is unsafe"), err)
	}
	receipt, err := decodeDeletionReceipt(data)
	if err != nil {
		return nil, err
	}
	expectedName, err := deletionReceiptEntryName(*receipt)
	if err != nil || expectedName != name {
		return nil, errors.Join(errors.New("deletion receipt name does not match its record"), err)
	}
	if seenComplete {
		exists, evidence, _, err = inspectOptionalRegular(root, deleteCompleteName, 0, false)
		if err != nil || !exists || evidence.size != 0 || !privateMetadataModeSafe(evidence.mode) {
			return nil, errors.Join(errors.New("deletion completion marker is unsafe"), err)
		}
	}
	if err := owner.Verify(); err != nil {
		return nil, err
	}
	return &deletionReceiptRecord{
		receipt:  *receipt,
		name:     name,
		owner:    owner,
		complete: seenComplete,
	}, nil
}

func confirmDeletionReceiptDurability(
	owner *platform.OwnedDirectory,
) (resultErr error) {
	root, err := owner.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	exists, evidence, _, err := inspectOptionalRegular(root, deleteReceiptName, maxReceiptBytes, false)
	if err != nil || !exists || !privateMetadataModeSafe(evidence.mode) {
		return errors.Join(errors.New("deletion receipt durability evidence is unsafe"), err)
	}
	if err := syncVerifiedRegular(root, deleteReceiptName, evidence); err != nil {
		return fmt.Errorf("durability-confirm deletion receipt: %w", err)
	}
	complete, completeEvidence, _, err := inspectOptionalRegular(root, deleteCompleteName, 0, false)
	if err != nil {
		return err
	}
	if complete {
		if completeEvidence.size != 0 || !privateMetadataModeSafe(completeEvidence.mode) {
			return errors.New("deletion completion durability evidence is unsafe")
		}
		if err := syncVerifiedRegular(root, deleteCompleteName, completeEvidence); err != nil {
			return fmt.Errorf("durability-confirm deletion completion: %w", err)
		}
	}
	return errors.Join(owner.Sync(), owner.Verify())
}

func (manager *SessionManager) inspectDeletionReceiptTemp(
	owner *platform.OwnedDirectory,
	name string,
	sessionID string,
	revision string,
) (_ *deletionReceiptTemp, resultErr error) {
	root, err := owner.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, readErr := directory.ReadDir(2)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(entries) > 1 || (len(entries) == 1 && entries[0].Name() != deleteReceiptName) {
		return nil, errors.New("temporary deletion receipt content is invalid")
	}
	var data []byte
	if len(entries) == 1 {
		exists, evidence, content, err := inspectOptionalRegular(root, deleteReceiptName, maxReceiptBytes, true)
		if err != nil || !exists || !privateMetadataModeSafe(evidence.mode) {
			return nil, errors.Join(errors.New("temporary deletion receipt record is unsafe"), err)
		}
		data = content
	}
	if err := owner.Verify(); err != nil {
		return nil, err
	}
	return &deletionReceiptTemp{
		sessionID: sessionID,
		revision:  revision,
		name:      name,
		owner:     owner,
		data:      data,
	}, nil
}

func (manager *SessionManager) receiptForCandidate(
	partitionIdentity string,
	candidate *sessionCandidate,
	intent deletionIntent,
) deletionReceipt {
	return deletionReceipt{
		Version:           SessionManagementVersion,
		SessionID:         intent.SessionID,
		Revision:          intent.Revision,
		StagingName:       intent.StagingName,
		PartitionBinding:  manager.partitionBinding(partitionIdentity),
		DirectoryBinding:  manager.directoryBinding(partitionIdentity, intent.SessionID, candidate.directoryIdentity),
		TranscriptBinding: manager.transcriptBinding(partitionIdentity, intent.SessionID, candidate.directoryIdentity, candidate.transcriptEvidence),
		LockBinding:       manager.lockBinding(partitionIdentity, intent.SessionID, candidate.directoryIdentity, *candidate.lockEvidence),
	}
}

func (manager *SessionManager) receiptForStage(
	partitionIdentity string,
	stage *deletionStage,
) (deletionReceipt, error) {
	if stage == nil || stage.transcriptEvidence == nil || stage.lockEvidence == nil || stage.intent == nil {
		return deletionReceipt{}, errors.New("unreceipted deletion stage is incomplete")
	}
	candidate := &sessionCandidate{
		directoryIdentity:  stage.directoryIdentity,
		transcriptEvidence: *stage.transcriptEvidence,
		lockEvidence:       stage.lockEvidence,
	}
	return manager.receiptForCandidate(partitionIdentity, candidate, *stage.intent), nil
}

func (manager *SessionManager) acquireReceiptRegistry(
	ctx context.Context,
	partition *platform.OwnedDirectory,
	expectedLockEvidence *fileEvidence,
	allowCreate bool,
) (*platform.OwnedDirectory, *sessionlock.Lock, error) {
	if err := contextError(ctx); err != nil {
		return nil, nil, err
	}
	root, err := partition.OpenRoot()
	if err != nil {
		return nil, nil, err
	}
	_, statErr := root.Lstat(deleteReceiptRoot)
	closeErr := root.Close()
	var receiptRoot *platform.OwnedDirectory
	switch {
	case errors.Is(statErr, os.ErrNotExist) && closeErr == nil:
		if !allowCreate {
			return nil, nil, errors.New("deletion receipt registry is missing")
		}
		receiptRoot, err = partition.CreatePrivateChild(deleteReceiptRoot)
		if errors.Is(err, os.ErrExist) {
			receiptRoot, err = partition.InspectPrivateChild(deleteReceiptRoot)
		}
		if err == nil {
			err = partition.Sync()
		}
	case statErr == nil && closeErr == nil:
		receiptRoot, err = partition.InspectPrivateChild(deleteReceiptRoot)
	default:
		err = errors.Join(statErr, closeErr)
	}
	if err != nil {
		return nil, nil, err
	}

	currentEvidence, err := inspectOptionalReceiptRegistryLock(receiptRoot)
	if err != nil {
		return nil, nil, err
	}
	if expectedLockEvidence != nil &&
		(currentEvidence == nil || !sameFileEvidence(*currentEvidence, *expectedLockEvidence)) {
		return nil, nil, errors.New("deletion receipt registry lock changed after inventory")
	}
	createdFromEmptyRoot := false
	var lock *sessionlock.Lock
	if currentEvidence != nil {
		lock, err = sessionlock.AcquireExisting(
			ctx,
			filepath.Join(receiptRoot.Path(), deleteRegistryLockName),
		)
	} else {
		if !allowCreate {
			return nil, nil, errors.New("deletion receipt registry lock is missing")
		}
		empty, inspectErr := receiptRegistryMetadataEmpty(receiptRoot)
		if inspectErr != nil {
			return nil, nil, inspectErr
		}
		if !empty {
			return nil, nil, errors.New("deletion receipt registry metadata lacks its persistent lock")
		}
		createdFromEmptyRoot = true
		lock, err = sessionlock.Acquire(
			ctx,
			filepath.Join(receiptRoot.Path(), deleteRegistryLockName),
		)
	}
	if err != nil {
		return nil, nil, err
	}
	acquiredEvidence, err := inspectReceiptRegistryLock(receiptRoot)
	if err != nil ||
		(currentEvidence != nil && !sameFileEvidence(acquiredEvidence, *currentEvidence)) ||
		(expectedLockEvidence != nil && !sameFileEvidence(acquiredEvidence, *expectedLockEvidence)) {
		_ = lock.Close()
		return nil, nil, errors.Join(errors.New("deletion receipt registry lock changed during acquisition"), err)
	}
	if createdFromEmptyRoot {
		empty, inspectErr := receiptRegistryMetadataEmpty(receiptRoot)
		if inspectErr != nil || !empty {
			_ = lock.Close()
			return nil, nil, errors.Join(
				errors.New("deletion receipt registry changed during first lock creation"),
				inspectErr,
			)
		}
	}
	if err := lock.Sync(); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("sync deletion receipt registry lock: %w", err)
	}
	if err := receiptRoot.Sync(); err != nil {
		_ = lock.Close()
		return nil, nil, fmt.Errorf("sync deletion receipt registry directory: %w", err)
	}
	durableEvidence, err := inspectReceiptRegistryLock(receiptRoot)
	if err != nil || !sameFileEvidence(durableEvidence, acquiredEvidence) {
		_ = lock.Close()
		return nil, nil, errors.Join(errors.New("deletion receipt registry lock changed during durability sync"), err)
	}
	if err := errors.Join(lock.Verify(), receiptRoot.Verify(), partition.Verify(), manager.verify()); err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return receiptRoot, lock, nil
}

func inspectOptionalReceiptRegistryLock(
	receiptRoot *platform.OwnedDirectory,
) (_ *fileEvidence, resultErr error) {
	if receiptRoot == nil {
		return nil, errors.New("deletion receipt root is unavailable")
	}
	root, err := receiptRoot.OpenRoot()
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	exists, evidence, _, err := inspectOptionalRegular(
		root,
		deleteRegistryLockName,
		maxIntentBytes,
		false,
	)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := receiptRoot.Verify(); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if !privateMetadataModeSafe(evidence.mode) {
		return nil, errors.New("deletion receipt registry lock permissions are unsafe")
	}
	if err := receiptRoot.Verify(); err != nil {
		return nil, err
	}
	return &evidence, nil
}

func receiptRegistryMetadataEmpty(
	receiptRoot *platform.OwnedDirectory,
) (empty bool, resultErr error) {
	if receiptRoot == nil {
		return false, errors.New("deletion receipt root is unavailable")
	}
	root, err := receiptRoot.OpenRoot()
	if err != nil {
		return false, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	entries, readErr := directory.ReadDir(3)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return false, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return false, closeErr
	}
	for _, entry := range entries {
		if entry.Name() != deleteRegistryLockName {
			return false, nil
		}
	}
	if len(entries) > 1 {
		return false, errors.New("deletion receipt registry contains duplicate lock entries")
	}
	if err := receiptRoot.Verify(); err != nil {
		return false, err
	}
	return true, nil
}

func sameFileEvidence(left, right fileEvidence) bool {
	return left.identity == right.identity &&
		left.size == right.size &&
		left.modTime.Equal(right.modTime) &&
		left.mode == right.mode &&
		left.hasDigest == right.hasDigest &&
		(!left.hasDigest || left.digest == right.digest)
}

func (manager *SessionManager) verifyReceiptRegistryLease(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	lock *sessionlock.Lock,
) error {
	if partition == nil || receiptRoot == nil || lock == nil {
		return errors.New("deletion receipt registry lease is unavailable")
	}
	return errors.Join(
		lock.Verify(),
		receiptRoot.Verify(),
		partition.Verify(),
		manager.verify(),
	)
}

func (manager *SessionManager) cleanupReceiptGCStages(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	stages map[string]*platform.OwnedDirectory,
) error {
	if receiptRoot == nil {
		return errors.New("deletion receipt root is unavailable")
	}
	names := make([]string, 0, len(stages))
	for name := range stages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		owner := stages[name]
		if err := manager.inspectDeletionReceiptGC(owner, name); err != nil {
			return err
		}
		if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
			return err
		}
		if err := owner.RemoveAllExisting(); err != nil {
			return err
		}
		if err := receiptRoot.Sync(); err != nil {
			return err
		}
	}
	return errors.Join(receiptRoot.Verify(), manager.verify())
}

func (manager *SessionManager) retireDeletionReceipt(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	record *deletionReceiptRecord,
) error {
	if receiptRoot == nil || record == nil || record.owner == nil {
		return errors.New("deletion receipt retirement identity is unavailable")
	}
	gcName := deletionReceiptGCName(record.name)
	if err := receiptRoot.PreflightPrivateChildDetach(record.owner); err != nil {
		return err
	}
	if err := requireStageAbsent(receiptRoot, gcName); err != nil {
		return err
	}
	verify := func() error {
		current, err := manager.inspectDeletionReceipt(record.owner, record.name)
		if err != nil || !sameDeletionReceipt(current.receipt, record.receipt) ||
			current.complete != record.complete {
			return errors.Join(errors.New("deletion receipt changed before retirement"), err)
		}
		return errors.Join(
			record.owner.Verify(),
			manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
		)
	}
	detached, detachErr := receiptRoot.DetachPrivateChildVerified(record.owner, gcName, verify)
	if !detached.Committed {
		return fmt.Errorf("detach deletion receipt for retirement: %w", detachErr)
	}
	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return errors.Join(detachErr, err)
	}
	if err := detached.Owner.RemoveAllExisting(); err != nil {
		return errors.Join(detachErr, err)
	}
	if err := receiptRoot.Sync(); err != nil {
		return errors.Join(detachErr, err)
	}
	if err := requireStageCleanupComplete(receiptRoot, gcName); err != nil {
		return errors.Join(detachErr, err)
	}
	return nil
}

func (manager *SessionManager) prepareReceiptCapacity(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	records map[string]*deletionReceiptRecord,
	temps map[string]*deletionReceiptTemp,
	gcStages map[string]*platform.OwnedDirectory,
	reservationKey string,
) error {
	if err := manager.cleanupReceiptGCStages(partition, receiptRoot, registryLock, gcStages); err != nil {
		return err
	}
	var complete []*deletionReceiptRecord
	pending := 0
	for _, record := range records {
		if record.complete {
			complete = append(complete, record)
		} else {
			pending++
		}
	}
	receiptLimit := manager.receiptEntryLimit()
	completedRetention := manager.completedReceiptRetention()
	additionalEntry := 1
	if records[reservationKey] != nil || temps[reservationKey] != nil {
		additionalEntry = 0
	}
	if pending+len(temps)+additionalEntry > receiptLimit {
		return ErrResourceLimit
	}
	// Reserve one completed-receipt slot for the deletion being prepared.
	// Settling at retention-1 here keeps the post-completion steady state at,
	// rather than one above, the configured retention ceiling.
	if len(complete) < completedRetention &&
		len(records)+len(temps)+additionalEntry <= receiptLimit {
		return manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock)
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].name < complete[j].name })
	removeCount := len(complete) - completedRetention + 1
	if required := len(records) + len(temps) + additionalEntry - receiptLimit; required > removeCount {
		removeCount = required
	}
	if removeCount <= 0 {
		return ErrResourceLimit
	}
	for _, record := range complete[:removeCount] {
		if err := manager.retireDeletionReceipt(partition, receiptRoot, registryLock, record); err != nil {
			return err
		}
		delete(records, deletionReceiptKey(record.receipt.SessionID, record.receipt.Revision))
	}
	return manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock)
}

func (manager *SessionManager) ensureCandidateReceipt(
	scan *workspaceScan,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	candidate *sessionCandidate,
	intent deletionIntent,
) (*deletionReceiptRecord, error) {
	if scan == nil || scan.partition == nil || candidate == nil || candidate.lockEvidence == nil {
		return nil, errors.New("deletion receipt evidence is unavailable")
	}
	if receiptRoot == nil {
		return nil, errors.New("deletion receipt registry is unavailable")
	}
	receipt := manager.receiptForCandidate(scan.partitionIdentity, candidate, intent)
	return manager.ensureDeletionReceipt(scan.partition, receipt, registryLock)
}

func (manager *SessionManager) ensureStageReceipt(
	scan *workspaceScan,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	stage *deletionStage,
) (*deletionReceiptRecord, error) {
	if scan == nil || scan.partition == nil || stage == nil {
		return nil, errors.New("staged deletion receipt evidence is unavailable")
	}
	if err := manager.prepareReceiptCapacity(
		scan.partition,
		receiptRoot,
		registryLock,
		scan.receipts,
		scan.receiptTemps,
		scan.receiptGC,
		deletionReceiptKey(stage.sessionID, stage.revision),
	); err != nil {
		return nil, err
	}
	receipt, err := manager.receiptForStage(scan.partitionIdentity, stage)
	if err != nil {
		return nil, err
	}
	return manager.ensureDeletionReceipt(scan.partition, receipt, registryLock)
}

func (manager *SessionManager) finalizeDeletionReceipt(
	ctx context.Context,
	partition *platform.OwnedDirectory,
	registryEvidence *fileEvidence,
	expected *deletionReceiptRecord,
	markComplete bool,
) (resultErr error) {
	if expected == nil {
		return errors.New("deletion receipt is unavailable")
	}
	receiptRoot, lock, err := manager.acquireReceiptRegistry(
		ctx,
		partition,
		registryEvidence,
		false,
	)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Close())
	}()
	owner, err := receiptRoot.InspectPrivateChild(expected.name)
	expectedIdentityErr := expected.owner.Verify()
	if err == nil && expectedIdentityErr != nil {
		err = expectedIdentityErr
	}
	if errors.Is(err, os.ErrNotExist) ||
		errors.Is(err, platform.ErrDirectoryIdentityChanged) {
		currentScan, scanErr := manager.scanWorkspace(
			ctx,
			expected.receipt.SessionID,
			newDigestBudget(),
		)
		if scanErr != nil {
			return scanErr
		}
		if currentScan.candidates[expected.receipt.SessionID] != nil ||
			currentScan.stages[expected.receipt.SessionID] != nil {
			return errDeletionGenerationStale
		}
		current := currentScan.receipts[deletionReceiptKey(
			expected.receipt.SessionID,
			expected.receipt.Revision,
		)]
		if current == nil {
			partitionIdentity, entries, snapshotErr := boundedPartitionSnapshot(partition)
			if snapshotErr != nil {
				return snapshotErr
			}
			if movedErr := manager.rejectMovedReceiptIdentity(
				partition,
				partitionIdentity,
				entries,
				expected,
			); movedErr != nil {
				return fmt.Errorf(
					"%w: retired deletion receipt retains its bound directory: %v",
					ErrSessionStoreUnsafe,
					movedErr,
				)
			}
			return errDeletionReceiptRetired
		}
		if expectedIdentityErr != nil {
			return fmt.Errorf("%w: deletion receipt directory identity changed", ErrSessionStoreUnsafe)
		}
		if !sameDeletionReceipt(current.receipt, expected.receipt) {
			return fmt.Errorf("%w: deletion receipt generation changed", ErrSessionStoreUnsafe)
		}
		owner = current.owner
		expected = current
		err = nil
	}
	if err != nil {
		return err
	}
	current, err := manager.inspectDeletionReceipt(owner, expected.name)
	if err != nil || !sameDeletionReceipt(current.receipt, expected.receipt) {
		return errors.Join(errors.New("deletion receipt changed before completion"), err)
	}
	if markComplete && !current.complete {
		return manager.completeReceiptAfterAbsentStage(partition, receiptRoot, lock, current)
	}
	if !current.complete {
		return errors.New("deletion receipt is still pending")
	}
	return manager.confirmCompletedReceipt(partition, receiptRoot, lock, current)
}

func (manager *SessionManager) ensureDeletionReceipt(
	partition *platform.OwnedDirectory,
	receipt deletionReceipt,
	registryLock *sessionlock.Lock,
) (_ *deletionReceiptRecord, resultErr error) {
	name, err := deletionReceiptEntryName(receipt)
	if err != nil {
		return nil, err
	}
	partitionRoot, err := partition.OpenRoot()
	if err != nil {
		return nil, err
	}
	_, statErr := partitionRoot.Lstat(deleteReceiptRoot)
	closeErr := partitionRoot.Close()
	var receiptRoot *platform.OwnedDirectory
	switch {
	case errors.Is(statErr, os.ErrNotExist) && closeErr == nil:
		return nil, errors.New("deletion receipt root disappeared under its registry lease")
	case statErr == nil && closeErr == nil:
		receiptRoot, err = partition.InspectPrivateChild(deleteReceiptRoot)
	default:
		return nil, errors.Join(statErr, closeErr)
	}
	if err != nil {
		return nil, err
	}
	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return nil, err
	}

	root, err := receiptRoot.OpenRoot()
	if err != nil {
		return nil, err
	}
	_, finalErr := root.Lstat(name)
	closeErr = root.Close()
	if finalErr == nil && closeErr == nil {
		owner, err := receiptRoot.InspectPrivateChild(name)
		if err != nil {
			return nil, err
		}
		record, err := manager.inspectDeletionReceipt(owner, name)
		if err != nil || !sameDeletionReceipt(record.receipt, receipt) || record.complete {
			return nil, errors.Join(errors.New("existing deletion receipt does not match"), err)
		}
		return record, nil
	}
	if !errors.Is(finalErr, os.ErrNotExist) || closeErr != nil {
		return nil, errors.Join(finalErr, closeErr)
	}

	encoded, err := deletionReceiptData(receipt)
	if err != nil {
		return nil, err
	}
	tempName := deletionReceiptTempName(receipt.SessionID, receipt.Revision)
	root, err = receiptRoot.OpenRoot()
	if err != nil {
		return nil, err
	}
	_, tempErr := root.Lstat(tempName)
	closeErr = root.Close()
	if tempErr == nil && closeErr == nil {
		tempOwner, err := receiptRoot.InspectPrivateChild(tempName)
		if err != nil {
			return nil, err
		}
		temp, err := manager.inspectDeletionReceiptTemp(
			tempOwner,
			tempName,
			receipt.SessionID,
			receipt.Revision,
		)
		if err != nil || len(temp.data) > len(encoded) ||
			!bytes.Equal(temp.data, encoded[:len(temp.data)]) {
			return nil, errors.Join(errors.New("temporary deletion receipt does not match"), err)
		}
		if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
			return nil, err
		}
		if err := tempOwner.RemoveAllExisting(); err != nil {
			return nil, err
		}
		if err := receiptRoot.Sync(); err != nil {
			return nil, err
		}
	} else if !errors.Is(tempErr, os.ErrNotExist) || closeErr != nil {
		return nil, errors.Join(tempErr, closeErr)
	}

	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return nil, err
	}
	tempOwner, err := receiptRoot.CreatePrivateChild(tempName)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup && resultErr != nil {
			verifyErr := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock)
			if verifyErr != nil {
				resultErr = errors.Join(resultErr, verifyErr)
				return
			}
			resultErr = errors.Join(resultErr, tempOwner.RemoveAllExisting(), receiptRoot.Sync())
		}
	}()
	root, err = tempOwner.OpenRoot()
	if err != nil {
		return nil, err
	}
	file, err := root.OpenFile(deleteReceiptName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	_, writeErr := file.Write(encoded)
	writeErr = errors.Join(writeErr, file.Sync(), file.Close(), root.Close())
	if writeErr != nil {
		return nil, writeErr
	}
	if err := tempOwner.Sync(); err != nil {
		return nil, err
	}
	if err := receiptRoot.PreflightPrivateChildDetach(tempOwner); err != nil {
		return nil, err
	}
	verifyPublish := func() error {
		current, err := manager.inspectDeletionReceiptTemp(
			tempOwner,
			tempName,
			receipt.SessionID,
			receipt.Revision,
		)
		if err != nil || !bytes.Equal(current.data, encoded) {
			return errors.Join(errors.New("temporary deletion receipt changed"), err)
		}
		if err := requireStageAbsent(receiptRoot, name); err != nil {
			return err
		}
		return errors.Join(
			tempOwner.Verify(),
			manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
		)
	}
	detached, detachErr := receiptRoot.DetachPrivateChildVerified(tempOwner, name, verifyPublish)
	if !detached.Committed {
		return nil, fmt.Errorf("publish deletion receipt: %w", detachErr)
	}
	cleanup = false
	committed := &deletionReceiptRecord{
		receipt: receipt,
		name:    name,
		owner:   detached.Owner,
	}
	if manager.afterReceipt != nil {
		if err := manager.afterReceipt(); err != nil {
			return committed, errors.Join(fmt.Errorf("after deletion receipt publication: %w", err), detachErr)
		}
	}
	record, err := manager.inspectDeletionReceipt(detached.Owner, name)
	if err != nil || !sameDeletionReceipt(record.receipt, receipt) || record.complete {
		return committed, errors.Join(errors.New("persisted deletion receipt is unstable"), err, detachErr)
	}
	return record, errors.Join(detachErr, receiptRoot.Verify(), partition.Verify(), manager.verify())
}

func (manager *SessionManager) verifyReceipt(
	partition *platform.OwnedDirectory,
	expected *deletionReceiptRecord,
) error {
	if expected == nil || expected.owner == nil {
		return errors.New("deletion receipt identity is unavailable")
	}
	receiptRoot, err := partition.InspectPrivateChild(deleteReceiptRoot)
	if err != nil {
		return err
	}
	currentOwner, err := receiptRoot.InspectPrivateChild(expected.name)
	if err != nil {
		return err
	}
	if err := expected.owner.Verify(); err != nil {
		return err
	}
	current, err := manager.inspectDeletionReceipt(currentOwner, expected.name)
	if err != nil || !sameDeletionReceipt(current.receipt, expected.receipt) ||
		current.complete != expected.complete {
		return errors.Join(errors.New("deletion receipt changed"), err)
	}
	return errors.Join(receiptRoot.Verify(), partition.Verify(), manager.verify())
}

func (manager *SessionManager) markReceiptComplete(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	receipt *deletionReceiptRecord,
) (resultErr error) {
	if receipt == nil {
		return errors.New("deletion receipt is unavailable")
	}
	if receipt.complete {
		return errors.Join(
			manager.verifyReceiptCompletionBoundary(partition, receiptRoot, registryLock, receipt),
			manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
		)
	}
	if err := manager.verifyReceiptCompletionBoundary(
		partition,
		receiptRoot,
		registryLock,
		receipt,
	); err != nil {
		return err
	}
	root, err := receipt.owner.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return err
	}
	file, err := root.OpenFile(deleteCompleteName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		exists, evidence, _, inspectErr := inspectOptionalRegular(root, deleteCompleteName, 0, false)
		if inspectErr != nil || !exists || evidence.size != 0 || !privateMetadataModeSafe(evidence.mode) {
			return errors.Join(errors.New("existing deletion completion marker is unsafe"), inspectErr)
		}
	} else if err != nil {
		return err
	} else {
		if err := errors.Join(file.Sync(), file.Close()); err != nil {
			return errors.Join(
				err,
				rollbackCompletionMarker(
					root,
					receipt.owner,
					func() error {
						return manager.verifyReceiptRegistryLease(
							partition,
							receiptRoot,
							registryLock,
						)
					},
				),
			)
		}
	}
	if err := receipt.owner.Sync(); err != nil {
		return errors.Join(
			err,
			rollbackCompletionMarker(
				root,
				receipt.owner,
				func() error {
					return manager.verifyReceiptRegistryLease(
						partition,
						receiptRoot,
						registryLock,
					)
				},
			),
		)
	}
	receipt.complete = true
	return manager.verifyReceiptCompletionBoundary(partition, receiptRoot, registryLock, receipt)
}

func rollbackCompletionMarker(
	root *os.Root,
	owner *platform.OwnedDirectory,
	verifyMutation func() error,
) error {
	exists, evidence, _, err := inspectOptionalRegular(root, deleteCompleteName, 0, false)
	if err != nil || !exists || evidence.size != 0 || !privateMetadataModeSafe(evidence.mode) {
		return errors.Join(errors.New("cannot safely roll back completion marker"), err)
	}
	if verifyMutation == nil {
		return errors.New("completion-marker rollback authority is unavailable")
	}
	if err := verifyMutation(); err != nil {
		return err
	}
	if err := removeVerifiedRegular(root, deleteCompleteName, evidence); err != nil {
		return err
	}
	return errors.Join(owner.Sync(), owner.Verify())
}

func (manager *SessionManager) completeReceiptAfterAbsentStage(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	receipt *deletionReceiptRecord,
) error {
	if partition == nil || receipt == nil || receipt.complete {
		return errors.New("pending deletion receipt is unavailable")
	}
	if err := partition.Sync(); err != nil {
		return fmt.Errorf("sync pending stage removal: %w", err)
	}
	if err := requireStageCleanupComplete(partition, receipt.receipt.StagingName); err != nil {
		return err
	}
	if err := manager.verifyReceiptCompletionBoundary(
		partition,
		receiptRoot,
		registryLock,
		receipt,
	); err != nil {
		return err
	}
	if err := manager.markReceiptComplete(partition, receiptRoot, registryLock, receipt); err != nil {
		return err
	}
	return manager.confirmCompletedReceipt(partition, receiptRoot, registryLock, receipt)
}

func (manager *SessionManager) confirmCompletedReceipt(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	receipt *deletionReceiptRecord,
) error {
	if partition == nil || receipt == nil || !receipt.complete {
		return errors.New("completed deletion receipt is unavailable")
	}
	if err := partition.Sync(); err != nil {
		return fmt.Errorf("sync completed stage removal: %w", err)
	}
	if err := requireStageCleanupComplete(partition, receipt.receipt.StagingName); err != nil {
		return err
	}
	if err := manager.verifyReceiptCompletionBoundary(
		partition,
		receiptRoot,
		registryLock,
		receipt,
	); err != nil {
		return err
	}
	return errors.Join(
		receipt.owner.Sync(),
		receiptRoot.Sync(),
		manager.verifyReceiptCompletionBoundary(partition, receiptRoot, registryLock, receipt),
	)
}

func (manager *SessionManager) verifyReceiptCompletionBoundary(
	partition *platform.OwnedDirectory,
	receiptRoot *platform.OwnedDirectory,
	registryLock *sessionlock.Lock,
	receipt *deletionReceiptRecord,
) error {
	if partition == nil || receipt == nil {
		return errors.New("deletion receipt completion identity is unavailable")
	}
	if err := manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock); err != nil {
		return err
	}
	if err := requireStageCleanupComplete(partition, receipt.receipt.StagingName); err != nil {
		return err
	}
	partitionIdentity, entries, err := boundedPartitionSnapshot(partition)
	if err != nil {
		return err
	}
	if err := manager.rejectNewerSessionGeneration(partition, entries, receipt); err != nil {
		return err
	}
	if err := manager.rejectMovedReceiptIdentity(
		partition,
		partitionIdentity,
		entries,
		receipt,
	); err != nil {
		return err
	}
	return errors.Join(
		manager.verifyReceipt(partition, receipt),
		manager.verifyReceiptRegistryLease(partition, receiptRoot, registryLock),
	)
}

func (manager *SessionManager) rejectNewerSessionGeneration(
	partition *platform.OwnedDirectory,
	entries []os.DirEntry,
	receipt *deletionReceiptRecord,
) (resultErr error) {
	if partition == nil || receipt == nil {
		return errors.New("deletion generation evidence is unavailable")
	}
	root, err := partition.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == receipt.receipt.SessionID:
			info, err := root.Lstat(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%w: same-ID session entry is unsafe", ErrSessionStoreUnsafe)
			}
			owner, err := partition.InspectPrivateChild(name)
			if err != nil {
				return fmt.Errorf("%w: inspect same-ID session entry: %v", ErrSessionStoreUnsafe, err)
			}
			if err := owner.Verify(); err != nil {
				return fmt.Errorf("%w: verify same-ID session entry: %v", ErrSessionStoreUnsafe, err)
			}
			return errDeletionGenerationStale
		case strings.HasPrefix(name, deleteStagePrefix):
			sessionID, _, err := parseDeletionStageName(name)
			if err != nil {
				return fmt.Errorf("%w: malformed deletion staging entry", ErrSessionStoreUnsafe)
			}
			if sessionID != receipt.receipt.SessionID {
				continue
			}
			owner, err := partition.InspectPrivateChild(name)
			if err != nil {
				return fmt.Errorf("%w: inspect same-ID deletion stage: %v", ErrSessionStoreUnsafe, err)
			}
			if err := owner.Verify(); err != nil {
				return fmt.Errorf("%w: verify same-ID deletion stage: %v", ErrSessionStoreUnsafe, err)
			}
			return errDeletionGenerationStale
		}
	}
	return errors.Join(partition.Verify(), manager.verify())
}

func boundedPartitionSnapshot(
	partition *platform.OwnedDirectory,
) (identity string, entries []os.DirEntry, resultErr error) {
	if partition == nil {
		return "", nil, errors.New("workspace partition identity is unavailable")
	}
	root, err := partition.OpenRoot()
	if err != nil {
		return "", nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	identity, err = rootedDirectoryIdentity(root)
	if err != nil {
		return "", nil, err
	}
	directory, err := root.Open(".")
	if err != nil {
		return "", nil, err
	}
	entries, readErr := directory.ReadDir(MaxWorkspaceEntries + 1)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return "", nil, errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return "", nil, closeErr
	}
	if len(entries) > MaxWorkspaceEntries {
		return "", nil, ErrResourceLimit
	}
	if err := partition.Verify(); err != nil {
		return "", nil, err
	}
	return identity, entries, nil
}

func ensureDeletionIntent(
	owner *platform.OwnedDirectory,
	intent deletionIntent,
	verifyMutation func() error,
) (resultErr error) {
	if owner == nil {
		return errors.New("session directory identity is unavailable")
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxIntentBytes {
		return errors.New("deletion intent exceeds its resource bound")
	}
	root, err := owner.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	verifyMutationBoundary := func() error {
		if verifyMutation == nil {
			return owner.Verify()
		}
		return errors.Join(verifyMutation(), owner.Verify())
	}

	finalExists, finalEvidence, finalData, err := inspectOptionalRegular(root, deleteIntentName, maxIntentBytes, true)
	if err != nil {
		return err
	}
	tempExists, tempEvidence, tempData, err := inspectOptionalRegular(root, deleteIntentTempName, maxIntentBytes, true)
	if err != nil {
		return err
	}
	if finalExists {
		existing, decodeErr := decodeDeletionIntent(finalData)
		if decodeErr == nil && sameDeletionIntent(*existing, intent) &&
			privateMetadataModeSafe(finalEvidence.mode) {
			if !tempExists {
				return errors.Join(
					syncVerifiedRegular(root, deleteIntentName, finalEvidence),
					owner.Sync(),
					owner.Verify(),
				)
			}
			temporary, tempDecodeErr := decodeDeletionIntent(tempData)
			if tempDecodeErr != nil || !sameDeletionIntent(*temporary, intent) ||
				!privateMetadataModeSafe(tempEvidence.mode) {
				return errors.Join(errors.New("temporary deletion intent does not match committed intent"), tempDecodeErr)
			}
			if err := syncVerifiedRegular(root, deleteIntentName, finalEvidence); err != nil {
				return err
			}
			if err := owner.Sync(); err != nil {
				return err
			}
			if err := verifyMutationBoundary(); err != nil {
				return err
			}
			if err := removeVerifiedRegular(root, deleteIntentTempName, tempEvidence); err != nil {
				return err
			}
			return errors.Join(owner.Sync(), owner.Verify())
		}
		// Direct exclusive publication may be interrupted after creating the
		// final inode. Recover only an exact prefix of this transaction while
		// its fully synced temporary record still proves the intended bytes.
		temporary, tempDecodeErr := decodeDeletionIntent(tempData)
		if !tempExists || tempDecodeErr != nil || !sameDeletionIntent(*temporary, intent) ||
			!privateMetadataModeSafe(tempEvidence.mode) ||
			!privateMetadataModeSafe(finalEvidence.mode) ||
			len(finalData) >= len(encoded) ||
			!bytes.Equal(finalData, encoded[:len(finalData)]) {
			return errors.Join(errors.New("committed deletion intent does not match"), decodeErr, tempDecodeErr)
		}
		if err := verifyMutationBoundary(); err != nil {
			return err
		}
		if err := rewriteVerifiedRegular(root, deleteIntentName, finalEvidence, encoded); err != nil {
			return err
		}
		finalExists, finalEvidence, finalData, err = inspectOptionalRegular(root, deleteIntentName, maxIntentBytes, true)
		if err != nil || !finalExists || !bytes.Equal(finalData, encoded) {
			return errors.Join(errors.New("recovered deletion intent is not stable"), err)
		}
		if err := verifyMutationBoundary(); err != nil {
			return err
		}
		if err := removeVerifiedRegular(root, deleteIntentTempName, tempEvidence); err != nil {
			return err
		}
		return errors.Join(owner.Sync(), owner.Verify())
	}

	var file *os.File
	if tempExists {
		if !privateMetadataModeSafe(tempEvidence.mode) {
			return errors.New("temporary deletion intent permissions are unsafe")
		}
		if len(tempData) > len(encoded) || !bytes.Equal(tempData, encoded[:len(tempData)]) {
			return errors.New("temporary deletion intent is not a prefix of this transaction")
		}
		if err := verifyMutationBoundary(); err != nil {
			return err
		}
		file, err = root.OpenFile(deleteIntentTempName, os.O_WRONLY, 0)
	} else {
		if err := verifyMutationBoundary(); err != nil {
			return err
		}
		file, err = root.OpenFile(deleteIntentTempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	}
	if err != nil {
		return err
	}
	if tempExists {
		err = rewriteOpenedVerifiedRegular(root, deleteIntentTempName, file, tempEvidence, encoded)
	} else {
		if _, err = file.Write(encoded); err == nil {
			err = file.Sync()
		}
		err = errors.Join(err, file.Close())
	}
	if err != nil {
		return err
	}
	_, _, written, err := inspectOptionalRegular(root, deleteIntentTempName, maxIntentBytes, true)
	if err != nil || !bytes.Equal(written, encoded) {
		return errors.Join(errors.New("deletion intent write was not stable"), err)
	}
	if err := owner.Sync(); err != nil {
		return fmt.Errorf("sync temporary deletion intent: %w", err)
	}
	if err := verifyMutationBoundary(); err != nil {
		return err
	}
	file, err = root.OpenFile(deleteIntentName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("publish deletion intent without replacement: %w", err)
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	if err := owner.Sync(); err != nil {
		return err
	}
	finalExists, _, finalData, err = inspectOptionalRegular(root, deleteIntentName, maxIntentBytes, true)
	if err != nil || !finalExists || !bytes.Equal(finalData, encoded) {
		return errors.Join(errors.New("committed deletion intent is not stable"), err)
	}
	_, tempEvidence, tempData, err = inspectOptionalRegular(root, deleteIntentTempName, maxIntentBytes, true)
	if err != nil {
		return err
	}
	temporary, err := decodeDeletionIntent(tempData)
	if err != nil || !sameDeletionIntent(*temporary, intent) {
		return errors.Join(errors.New("temporary deletion intent changed before cleanup"), err)
	}
	if err := verifyMutationBoundary(); err != nil {
		return err
	}
	if err := removeVerifiedRegular(root, deleteIntentTempName, tempEvidence); err != nil {
		return err
	}
	return errors.Join(owner.Sync(), owner.Verify())
}

func verifyOpenedRegular(
	root *os.Root,
	name string,
	file *os.File,
	expected fileEvidence,
) error {
	if root == nil || file == nil {
		return ErrUnsafePath
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() ||
		opened.Size() != expected.size ||
		!opened.ModTime().Equal(expected.modTime) ||
		opened.Mode() != expected.mode {
		return ErrUnsafePath
	}
	identity, links, err := openedFilesystemIdentity(file, opened)
	if err != nil || links != 1 || identity != expected.identity {
		return ErrUnsafePath
	}
	current, err := root.Lstat(name)
	if err != nil || !sameRegularSnapshot(opened, current) {
		return ErrUnsafePath
	}
	return nil
}

func removeVerifiedRegular(
	root *os.Root,
	name string,
	expected fileEvidence,
) error {
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	if err := verifyOpenedRegular(root, name, file, expected); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Remove(name)
}

func rewriteVerifiedRegular(
	root *os.Root,
	name string,
	expected fileEvidence,
	data []byte,
) error {
	file, err := root.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	return rewriteOpenedVerifiedRegular(root, name, file, expected, data)
}

func syncVerifiedRegular(
	root *os.Root,
	name string,
	expected fileEvidence,
) error {
	file, err := root.OpenFile(name, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := verifyOpenedRegular(root, name, file, expected); err != nil {
		_ = file.Close()
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func rewriteOpenedVerifiedRegular(
	root *os.Root,
	name string,
	file *os.File,
	expected fileEvidence,
	data []byte,
) error {
	if err := verifyOpenedRegular(root, name, file, expected); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Truncate(0); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return err
	}
	_, err := file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}

func requireStageAbsent(partition *platform.OwnedDirectory, stageName string) (resultErr error) {
	root, err := partition.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if _, err := root.Lstat(stageName); errors.Is(err, os.ErrNotExist) {
		return partition.Verify()
	} else if err != nil {
		return err
	}
	return errors.New("deletion staging destination already exists")
}

func requireStageCleanupComplete(
	partition *platform.OwnedDirectory,
	stageName string,
) (resultErr error) {
	root, err := partition.OpenRoot()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()
	if _, err := root.Lstat(stageName); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = errors.New("entry remains present")
		}
		return fmt.Errorf("verify detached session cleanup: %w", err)
	}
	return partition.Verify()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("session-management context is nil")
	}
	return ctx.Err()
}
