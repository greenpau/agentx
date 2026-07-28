package attachment

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUploadBeginChunkCommitAndRestart(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "attachments")
	store := openTestStore(t, directory, Options{})
	raw := testPNG(t, 5, 4)
	request := BeginUpload{
		UploadID: "upl_success", AttachmentID: "att_success",
		Name: "screen.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	accepted, err := store.Begin(t.Context(), request)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	if accepted.Terminal || accepted.Status != UploadAccepted ||
		accepted.UploadID != request.UploadID ||
		accepted.AttachmentID != request.AttachmentID {
		t.Fatalf("accepted ack = %#v", accepted)
	}
	if _, _, err := store.Resolve(t.Context(), request.AttachmentID); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("uncommitted upload resolved: %v", err)
	}

	middle := len(raw) / 2
	if err := store.Chunk(
		t.Context(), request.UploadID, 0,
		base64.StdEncoding.EncodeToString(raw[:middle]),
	); err != nil {
		t.Fatalf("Chunk(0) error = %v", err)
	}
	if err := store.Chunk(
		t.Context(), request.UploadID, 1,
		base64.StdEncoding.EncodeToString(raw[middle:]),
	); err != nil {
		t.Fatalf("Chunk(1) error = %v", err)
	}
	committed, err := store.Commit(t.Context(), request.UploadID)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if !committed.Terminal || committed.Status != UploadCommitted ||
		committed.Manifest == nil ||
		committed.Manifest.AttachmentID != request.AttachmentID {
		t.Fatalf("committed ack = %#v", committed)
	}
	if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
		outcome.Status != UploadCommitted {
		t.Fatalf("UploadOutcome() = %#v, %v", outcome, exists)
	}
	if _, err := store.Commit(t.Context(), request.UploadID); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("duplicate Commit() error = %v", err)
	}
	if _, err := store.Abort(t.Context(), request.UploadID); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("Abort after commit error = %v", err)
	}
	if err := store.Chunk(t.Context(), request.UploadID, 2, "YQ=="); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("Chunk after commit error = %v", err)
	}
	if _, _, err := store.Resolve(t.Context(), request.AttachmentID); err != nil {
		t.Fatalf("Resolve committed upload error = %v", err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, directory, Options{})
	if manifest, _, err := reopened.Resolve(t.Context(), request.AttachmentID); err != nil ||
		manifest != *committed.Manifest {
		t.Fatalf("restart Resolve() = %#v, %v", manifest, err)
	}
}

func TestUploadStrictBase64SequenceSizeAndDigestFailures(t *testing.T) {
	raw := testPNG(t, 2, 2)
	tests := []struct {
		name string
		run  func(t *testing.T, store *Store, request BeginUpload)
	}{
		{
			name: "strict base64",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				if err := store.Chunk(t.Context(), request.UploadID, 0, "YR=="); !errors.Is(err, ErrBase64) {
					t.Fatalf("Chunk() error = %v", err)
				}
				if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
					outcome.Status != UploadFailed || outcome.Reason != "chunk_rejected" {
					t.Fatalf("chunk rejection outcome = %#v, %v", outcome, exists)
				}
			},
		},
		{
			name: "reordered sequence",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				encoded := base64.StdEncoding.EncodeToString(raw)
				if err := store.Chunk(t.Context(), request.UploadID, 1, encoded); !errors.Is(err, ErrSequence) {
					t.Fatalf("reordered Chunk() error = %v", err)
				}
				if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
					outcome.Status != UploadFailed || outcome.Reason != "sequence_mismatch" {
					t.Fatalf("sequence outcome = %#v, %v", outcome, exists)
				}
			},
		},
		{
			name: "repeated sequence",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				middle := len(raw) / 2
				if err := store.Chunk(
					t.Context(), request.UploadID, 0,
					base64.StdEncoding.EncodeToString(raw[:middle]),
				); err != nil {
					t.Fatalf("Chunk(0) error = %v", err)
				}
				if err := store.Chunk(t.Context(), request.UploadID, 0, "YQ=="); !errors.Is(err, ErrSequence) {
					t.Fatalf("repeated Chunk() error = %v", err)
				}
			},
		},
		{
			name: "truncated commit",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				half := raw[:len(raw)/2]
				if err := store.Chunk(
					t.Context(), request.UploadID, 0,
					base64.StdEncoding.EncodeToString(half),
				); err != nil {
					t.Fatal(err)
				}
				ack, err := store.Commit(t.Context(), request.UploadID)
				if !errors.Is(err, ErrSizeMismatch) ||
					!ack.Terminal || ack.Status != UploadFailed ||
					ack.Reason != "size_mismatch" {
					t.Fatalf("Commit() = %#v, %v", ack, err)
				}
			},
		},
		{
			name: "oversized chunk",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				encoded := base64.StdEncoding.EncodeToString(append(raw, 0))
				if err := store.Chunk(t.Context(), request.UploadID, 0, encoded); !errors.Is(err, ErrSizeMismatch) {
					t.Fatalf("oversized Chunk() error = %v", err)
				}
				if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
					outcome.Status != UploadFailed || outcome.Reason != "size_mismatch" {
					t.Fatalf("size outcome = %#v, %v", outcome, exists)
				}
			},
		},
		{
			name: "digest mismatch",
			run: func(t *testing.T, store *Store, request BeginUpload) {
				request.SHA256 = strings.Repeat("0", 64)
				// Replace the original active state with a distinct declaration.
				if _, err := store.Abort(t.Context(), request.UploadID); err != nil {
					t.Fatal(err)
				}
				request.UploadID += "_digest"
				request.AttachmentID += "_digest"
				if _, err := store.Begin(t.Context(), request); err != nil {
					t.Fatal(err)
				}
				if err := store.Chunk(
					t.Context(), request.UploadID, 0,
					base64.StdEncoding.EncodeToString(raw),
				); err != nil {
					t.Fatal(err)
				}
				ack, err := store.Commit(t.Context(), request.UploadID)
				if !errors.Is(err, ErrDigestMismatch) ||
					ack.Status != UploadFailed || ack.Reason != "digest_mismatch" {
					t.Fatalf("Commit() = %#v, %v", ack, err)
				}
			},
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
			request := BeginUpload{
				UploadID:     UploadID(fmt.Sprintf("upl_case_%d", index)),
				AttachmentID: ID(fmt.Sprintf("att_case_%d", index)),
				Name:         "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
				SHA256: rawDigest(raw),
			}
			if _, err := store.Begin(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			test.run(t, store, request)
		})
	}
}

func TestUploadRejectsEveryNonStrictBase64FormTerminally(t *testing.T) {
	raw := testPNG(t, 1, 1)
	for index, encoded := range []string{
		"YQ",
		"YQ==\n",
		"YQ==\r\n\r\n",
		"YQ== ",
		"YR==",
		"!!!!",
		"",
	} {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
			request := BeginUpload{
				UploadID:     UploadID(fmt.Sprintf("upl_base64_%d", index)),
				AttachmentID: ID(fmt.Sprintf("att_base64_%d", index)),
				Name:         "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
				SHA256: rawDigest(raw),
			}
			if _, err := store.Begin(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if err := store.Chunk(t.Context(), request.UploadID, 0, encoded); !errors.Is(err, ErrBase64) {
				t.Fatalf("Chunk(%q) error = %v", encoded, err)
			}
			outcome, exists := store.UploadOutcome(request.UploadID)
			if !exists || !outcome.Terminal || outcome.Status != UploadFailed ||
				outcome.Reason != "chunk_rejected" {
				t.Fatalf("outcome = %#v, %v", outcome, exists)
			}
		})
	}
}

func TestUploadRejectsMIMEAndMagicMismatchWithTerminalFailure(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
	raw := testPNG(t, 1, 1)
	request := BeginUpload{
		UploadID: "upl_mismatch", AttachmentID: "att_mismatch",
		Name: "claimed.jpg", SizeBytes: int64(len(raw)), MIMEType: MIMEJPEG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.Chunk(
		t.Context(), request.UploadID, 0,
		base64.StdEncoding.EncodeToString(raw),
	); err != nil {
		t.Fatal(err)
	}
	ack, err := store.Commit(t.Context(), request.UploadID)
	if !errors.Is(err, ErrMediaMismatch) ||
		ack.Status != UploadFailed || ack.Reason != "media_rejected" {
		t.Fatalf("Commit() = %#v, %v", ack, err)
	}
	if _, _, err := store.Resolve(t.Context(), request.AttachmentID); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("rejected media became visible: %v", err)
	}
}

func TestUploadDuplicateIDsReservationsAndCountLimits(t *testing.T) {
	now := time.Unix(1_000, 0)
	limits := DefaultLimits()
	limits.MaxConcurrentUploads = 1
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{
		Limits: limits, Now: func() time.Time { return now },
	})
	raw := testPNG(t, 1, 1)
	first := BeginUpload{
		UploadID: "upl_first", AttachmentID: "att_first",
		Name: "first.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	for _, duplicate := range []BeginUpload{
		first,
		{
			UploadID: "upl_second", AttachmentID: first.AttachmentID,
			Name: "second.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
			SHA256: rawDigest(raw),
		},
		{
			UploadID: "upl_second", AttachmentID: "att_second",
			Name: "second.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
			SHA256: rawDigest(raw),
		},
	} {
		if _, err := store.Begin(t.Context(), duplicate); err == nil {
			t.Fatalf("duplicate/reservation was accepted: %#v", duplicate)
		}
	}
	if _, err := store.Abort(t.Context(), first.UploadID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(t.Context(), BeginUpload{
		UploadID: "upl_second", AttachmentID: "att_second",
		Name: "second.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}); err != nil {
		t.Fatalf("reservation was not released: %v", err)
	}
}

func TestUploadAttemptLedgerReservesTerminalCapacityIndependently(t *testing.T) {
	root := t.TempDir()
	now := time.Unix(1_000, 0)
	limits := DefaultLimits()
	limits.MaxConcurrentUploads = 2
	limits.MaxUploadsPerSession = 3
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{
		Limits: limits, Now: func() time.Time { return now },
	})
	raw := testPNG(t, 1, 1)
	request := func(ordinal int) BeginUpload {
		return BeginUpload{
			UploadID:     UploadID(fmt.Sprintf("upl_ledger_%d", ordinal)),
			AttachmentID: ID(fmt.Sprintf("att_ledger_%d", ordinal)),
			Name:         fmt.Sprintf("%d.png", ordinal),
			SizeBytes:    int64(len(raw)),
			MIMEType:     MIMEPNG,
			SHA256:       rawDigest(raw),
		}
	}

	first := request(0)
	second := request(1)
	for _, value := range []BeginUpload{first, second} {
		if _, err := store.Begin(t.Context(), value); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Abort(t.Context(), first.UploadID); err != nil {
		t.Fatal(err)
	}
	third := request(2)
	if _, err := store.Begin(t.Context(), third); err != nil {
		t.Fatalf("exact attempt-ledger Begin() error = %v", err)
	}
	if _, err := store.Begin(t.Context(), request(3)); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("over-bound attempt-ledger Begin() error = %v, want ErrResourceLimit", err)
	}
	if acknowledgements := store.AbortAll(AbortCancellation); len(acknowledgements) != 2 {
		t.Fatalf("AbortAll() acknowledgements = %#v", acknowledgements)
	}

	store.mu.Lock()
	active := len(store.uploadStates)
	terminal := len(store.uploadTerminal)
	manifests := len(store.entries)
	store.mu.Unlock()
	if active != 0 || terminal != limits.MaxUploadsPerSession || manifests != 0 {
		t.Fatalf(
			"attempt ledger state active=%d terminal=%d manifests=%d",
			active, terminal, manifests,
		)
	}

	source := filepath.Join(root, "source.png")
	writeTestFile(t, source, raw, 0o600)
	if _, err := store.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_independent_manifest",
		Path:         source,
		Name:         "source.png",
		MIMEType:     MIMEPNG,
	}); err != nil {
		t.Fatalf("terminal upload ledger blocked independent manifest import: %v", err)
	}
}

func TestUploadTimeoutAbortEOFAndCancellationCleanup(t *testing.T) {
	now := time.Unix(1_000, 0)
	limits := DefaultLimits()
	limits.UploadTimeout = time.Second
	limits.UploadTimeoutMillis = 0
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{
		Limits: limits, Now: func() time.Time { return now },
	})
	raw := testPNG(t, 1, 1)
	begin := func(id string) BeginUpload {
		request := BeginUpload{
			UploadID: UploadID("upl_" + id), AttachmentID: ID("att_" + id),
			Name: id + ".png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
			SHA256: rawDigest(raw),
		}
		if _, err := store.Begin(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		return request
	}
	expiring := begin("expires")
	now = now.Add(time.Second)
	expired := store.Expire(now)
	if len(expired) != 1 || expired[0].UploadID != expiring.UploadID ||
		expired[0].Status != UploadExpired || !expired[0].Terminal {
		t.Fatalf("Expire() = %#v", expired)
	}
	if _, err := store.Commit(t.Context(), expiring.UploadID); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("Commit after expire error = %v", err)
	}

	first := begin("eof_a")
	second := begin("eof_b")
	acks := store.AbortAll(AbortEOF)
	if len(acks) != 2 ||
		acks[0].UploadID != first.UploadID ||
		acks[1].UploadID != second.UploadID ||
		acks[0].Reason != string(AbortEOF) ||
		acks[1].Reason != string(AbortEOF) {
		t.Fatalf("AbortAll() = %#v", acks)
	}
	uploadNames, err := listOwnedDirectory(store.uploadDir)
	if err != nil || len(uploadNames) != 0 {
		t.Fatalf("upload temporary files = %v, %v", uploadNames, err)
	}

	cancelled := begin("cancel")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := store.Chunk(ctx, cancelled.UploadID, 0, "YQ=="); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Chunk() error = %v", err)
	}
	if outcome, exists := store.UploadOutcome(cancelled.UploadID); !exists ||
		outcome.Status != UploadAborted || outcome.Reason != string(AbortCancellation) {
		t.Fatalf("cancellation outcome = %#v, %v", outcome, exists)
	}
	if _, err := store.Abort(t.Context(), cancelled.UploadID); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("Abort after cancellation error = %v", err)
	}
}

func TestUploadAutomaticTimeoutCleansAndEmitsOneTerminalNotice(t *testing.T) {
	limits := DefaultLimits()
	limits.UploadTimeout = 20 * time.Millisecond
	limits.UploadTimeoutMillis = 0
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{Limits: limits})
	raw := testPNG(t, 1, 1)
	request := BeginUpload{
		UploadID: "upl_auto_timeout", AttachmentID: "att_auto_timeout",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	select {
	case ack := <-store.UploadNotifications():
		if ack.UploadID != request.UploadID || ack.Status != UploadExpired ||
			!ack.Terminal || ack.Reason != "timeout" {
			t.Fatalf("timeout acknowledgement = %#v", ack)
		}
	case <-time.After(time.Second):
		t.Fatal("automatic upload timeout did not settle")
	}
	select {
	case duplicate := <-store.UploadNotifications():
		t.Fatalf("duplicate terminal acknowledgement = %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}
	if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
		outcome.Status != UploadExpired {
		t.Fatalf("timeout outcome = %#v, %v", outcome, exists)
	}
	if _, _, err := store.Resolve(t.Context(), request.AttachmentID); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("timed-out upload resolved: %v", err)
	}
	names, err := listOwnedDirectory(store.uploadDir)
	if err != nil || len(names) != 0 {
		t.Fatalf("timeout temporary files = %v, %v", names, err)
	}
}

func TestUploadTimeoutNoticesSurviveDelayedConsumerAcrossConcurrentWaves(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxConcurrentUploads = 2
	limits.MaxUploadsPerSession = 6
	limits.UploadTimeout = 20 * time.Millisecond
	limits.UploadTimeoutMillis = 0
	store := openTestStore(
		t,
		filepath.Join(t.TempDir(), "attachments"),
		Options{Limits: limits},
	)
	raw := testPNG(t, 1, 1)
	requests := make([]BeginUpload, 0, limits.MaxUploadsPerSession)

	for wave := 0; wave < limits.MaxUploadsPerSession/limits.MaxConcurrentUploads; wave++ {
		waveRequests := make([]BeginUpload, limits.MaxConcurrentUploads)
		beginErrors := make(chan error, len(waveRequests))
		var wait sync.WaitGroup
		for index := range waveRequests {
			ordinal := wave*limits.MaxConcurrentUploads + index
			request := BeginUpload{
				UploadID:     UploadID(fmt.Sprintf("upl_delayed_%02d", ordinal)),
				AttachmentID: ID(fmt.Sprintf("att_delayed_%02d", ordinal)),
				Name:         fmt.Sprintf("delayed-%02d.png", ordinal),
				SizeBytes:    int64(len(raw)),
				MIMEType:     MIMEPNG,
				SHA256:       rawDigest(raw),
			}
			waveRequests[index] = request
			wait.Add(1)
			go func() {
				defer wait.Done()
				_, err := store.Begin(t.Context(), request)
				beginErrors <- err
			}()
		}
		wait.Wait()
		close(beginErrors)
		for err := range beginErrors {
			if err != nil {
				t.Fatalf("wave %d Begin() error = %v", wave, err)
			}
		}
		requests = append(requests, waveRequests...)

		deadline := time.Now().Add(2 * time.Second)
		for {
			expired := 0
			for _, request := range waveRequests {
				if outcome, exists := store.UploadOutcome(request.UploadID); exists &&
					outcome.Status == UploadExpired {
					expired++
				}
			}
			if expired == len(waveRequests) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("wave %d timed out waiting for terminal outcomes", wave)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	if _, err := store.Begin(t.Context(), BeginUpload{
		UploadID: "upl_delayed_overflow", AttachmentID: "att_delayed_overflow",
		Name: "overflow.png", SizeBytes: int64(len(raw)),
		MIMEType: MIMEPNG, SHA256: rawDigest(raw),
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("post-ledger-limit Begin() error = %v, want ErrResourceLimit", err)
	}

	seen := make(map[UploadID]int, len(requests))
	for range len(requests) {
		select {
		case ack := <-store.UploadNotifications():
			if !ack.Terminal || ack.Status != UploadExpired || ack.Reason != "timeout" {
				t.Fatalf("delayed timeout acknowledgement = %#v", ack)
			}
			seen[ack.UploadID]++
		case <-time.After(2 * time.Second):
			t.Fatalf("delayed consumer received %d/%d timeout notices", len(seen), len(requests))
		}
	}
	if len(seen) != len(requests) {
		t.Fatalf("unique delayed timeout notices = %d, want %d: %#v", len(seen), len(requests), seen)
	}
	for _, request := range requests {
		if seen[request.UploadID] != 1 {
			t.Fatalf("upload %s notices = %d, want 1", request.UploadID, seen[request.UploadID])
		}
		outcome, exists := store.UploadOutcome(request.UploadID)
		if !exists || outcome.Status != UploadExpired ||
			outcome.Reason != "timeout" || !outcome.Terminal {
			t.Fatalf("upload %s outcome = %#v, %v", request.UploadID, outcome, exists)
		}
	}
	select {
	case duplicate := <-store.UploadNotifications():
		t.Fatalf("duplicate delayed terminal acknowledgement = %#v", duplicate)
	case <-time.After(30 * time.Millisecond):
	}

	names, err := listOwnedDirectory(store.uploadDir)
	if err != nil || len(names) != 0 {
		t.Fatalf("delayed timeout temporary files = %v, %v", names, err)
	}
	store.mu.Lock()
	active := len(store.uploadStates)
	reservations := len(store.reservedIDs)
	reservedBytes := store.reservedSize
	terminal := len(store.uploadTerminal)
	store.mu.Unlock()
	if active != 0 || reservations != 0 || reservedBytes != 0 ||
		terminal != limits.MaxUploadsPerSession {
		t.Fatalf(
			"settled delayed timeout state active=%d reservations=%d bytes=%d terminal=%d",
			active, reservations, reservedBytes, terminal,
		)
	}
}

func TestUploadOrphanCleanupOnOpen(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "attachments")
	store := openTestStore(t, directory, Options{})
	orphan := filepath.Join(store.uploadDir.Path(), "upload_upl_orphan")
	writeTestFile(t, orphan, []byte("untrusted partial bytes"), 0o600)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, directory, Options{})
	names, err := listOwnedDirectory(reopened.uploadDir)
	if err != nil || len(names) != 0 {
		t.Fatalf("orphan cleanup = %v, %v", names, err)
	}
}

func TestUploadChunkBoundariesAndAggregateReservations(t *testing.T) {
	raw := testPNG(t, 1, 1)
	limits := DefaultLimits()
	limits.MaxChunkDecodedBytes = len(raw)
	limits.MaxChunkEncodedBytes = base64.StdEncoding.EncodedLen(len(raw))
	limits.MaxItemBytes = int64(len(raw))
	limits.MaxAggregateBytes = int64(len(raw))
	limits.MaxStorageBytes = int64(len(raw))
	limits.MaxModelRequestMediaBytes = int64(len(raw))
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{Limits: limits})
	request := BeginUpload{
		UploadID: "upl_boundary", AttachmentID: "att_boundary",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatalf("exact Begin() error = %v", err)
	}
	if _, err := store.Begin(t.Context(), BeginUpload{
		UploadID: "upl_over_aggregate", AttachmentID: "att_over_aggregate",
		Name: "other.png", SizeBytes: 1, MIMEType: MIMEPNG,
		SHA256: strings.Repeat("0", 64),
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("aggregate reservation error = %v", err)
	}
	if err := store.Chunk(
		t.Context(), request.UploadID, 0,
		base64.StdEncoding.EncodeToString(raw),
	); err != nil {
		t.Fatalf("exact Chunk() error = %v", err)
	}
	if _, err := store.Commit(t.Context(), request.UploadID); err != nil {
		t.Fatalf("exact Commit() error = %v", err)
	}
}

func TestUploadConcurrentDuplicateChunkSettlesOneSequence(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
	raw := testPNG(t, 1, 1)
	request := BeginUpload{
		UploadID: "upl_concurrent", AttachmentID: "att_concurrent",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(raw)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- store.Chunk(t.Context(), request.UploadID, 0, encoded)
		}()
	}
	wg.Wait()
	close(results)
	var success, sequence int
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrSequence):
			sequence++
		default:
			t.Fatalf("unexpected Chunk() error = %v", err)
		}
	}
	if success != 1 || sequence != 1 {
		t.Fatalf("success=%d sequence=%d", success, sequence)
	}
	if _, err := store.Commit(t.Context(), request.UploadID); !errors.Is(err, ErrUploadTerminal) {
		t.Fatalf("Commit after duplicate chunk error = %v", err)
	}
	if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
		outcome.Status != UploadFailed || outcome.Reason != "sequence_mismatch" {
		t.Fatalf("duplicate chunk outcome = %#v, %v", outcome, exists)
	}
}

func TestUploadCloseSettlesAndRemovesTemporaryFilesExactlyOnce(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
	raw := testPNG(t, 1, 1)
	request := BeginUpload{
		UploadID: "upl_close", AttachmentID: "att_close",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if outcome, exists := store.UploadOutcome(request.UploadID); !exists ||
		outcome.Status != UploadAborted || outcome.Reason != "store_closed" {
		t.Fatalf("close outcome = %#v, %v", outcome, exists)
	}
	if _, err := store.Begin(t.Context(), request); !errors.Is(err, ErrClosed) {
		t.Fatalf("Begin after close error = %v", err)
	}
}

func TestUploadConcurrentCloseCannotOverwriteTerminalOutcomeOrPublish(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
	raw := testPNG(t, 4, 4)
	request := BeginUpload{
		UploadID: "upl_close_race", AttachmentID: "att_close_race",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	if _, err := store.Begin(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if err := store.Chunk(
		t.Context(), request.UploadID, 0,
		base64.StdEncoding.EncodeToString(raw),
	); err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	store.beforeUploadPublish = func() {
		close(reached)
		<-release
	}
	type result struct {
		ack UploadAcknowledgement
		err error
	}
	done := make(chan result, 1)
	go func() {
		ack, err := store.Commit(t.Context(), request.UploadID)
		done <- result{ack: ack, err: err}
	}()
	<-reached
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(release)
	commit := <-done
	if !errors.Is(commit.err, ErrUploadTerminal) ||
		commit.ack.Status != UploadAborted ||
		commit.ack.Reason != "store_closed" {
		t.Fatalf("Commit race = %#v, %v", commit.ack, commit.err)
	}
	outcome, exists := store.UploadOutcome(request.UploadID)
	if !exists || outcome != commit.ack {
		t.Fatalf("terminal outcome changed: %#v, %v", outcome, exists)
	}
	if _, exists := store.entries[request.AttachmentID]; exists {
		t.Fatal("close-raced upload published a manifest")
	}
}

func TestUploadCommitRechecksDeadlineAtPublicationBoundary(t *testing.T) {
	start := time.Unix(1_000, 0)
	for index, test := range []struct {
		name       string
		publishNow time.Time
		wantStatus UploadStatus
		wantErr    error
	}{
		{
			name:       "one nanosecond before deadline commits",
			publishNow: start.Add(time.Second - time.Nanosecond),
			wantStatus: UploadCommitted,
		},
		{
			name:       "at deadline expires",
			publishNow: start.Add(time.Second),
			wantStatus: UploadExpired,
			wantErr:    ErrUploadExpired,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := start
			limits := DefaultLimits()
			limits.UploadTimeout = time.Second
			limits.UploadTimeoutMillis = 0
			store := openTestStore(
				t,
				filepath.Join(t.TempDir(), "attachments"),
				Options{Limits: limits, Now: func() time.Time { return now }},
			)
			raw := testPNG(t, 2, 2)
			request := BeginUpload{
				UploadID:     UploadID(fmt.Sprintf("upl_deadline_%d", index)),
				AttachmentID: ID(fmt.Sprintf("att_deadline_%d", index)),
				Name:         "deadline.png",
				SizeBytes:    int64(len(raw)),
				MIMEType:     MIMEPNG,
				SHA256:       rawDigest(raw),
			}
			if _, err := store.Begin(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			if err := store.Chunk(
				t.Context(),
				request.UploadID,
				0,
				base64.StdEncoding.EncodeToString(raw),
			); err != nil {
				t.Fatal(err)
			}
			store.beforeUploadPublish = func() {
				now = test.publishNow
			}

			ack, err := store.Commit(t.Context(), request.UploadID)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Commit() error = %v, want %v", err, test.wantErr)
			}
			if !ack.Terminal || ack.Status != test.wantStatus {
				t.Fatalf("Commit() acknowledgement = %#v", ack)
			}
			outcome, exists := store.UploadOutcome(request.UploadID)
			if !exists || outcome.Status != ack.Status ||
				outcome.Reason != ack.Reason || outcome.Terminal != ack.Terminal {
				t.Fatalf("UploadOutcome() = %#v, %v; commit = %#v", outcome, exists, ack)
			}

			if test.wantStatus == UploadCommitted {
				if ack.Manifest == nil {
					t.Fatal("committed acknowledgement has no manifest")
				}
				if _, _, err := store.Resolve(t.Context(), request.AttachmentID); err != nil {
					t.Fatalf("Resolve() committed attachment error = %v", err)
				}
				return
			}
			if ack.Manifest != nil || ack.Reason != "timeout" {
				t.Fatalf("expired acknowledgement = %#v", ack)
			}
			if _, _, err := store.Resolve(
				t.Context(), request.AttachmentID,
			); !errors.Is(err, ErrNotCommitted) {
				t.Fatalf("expired upload resolved: %v", err)
			}
			if _, err := store.Commit(
				t.Context(), request.UploadID,
			); !errors.Is(err, ErrUploadTerminal) {
				t.Fatalf("second Commit() error = %v, want ErrUploadTerminal", err)
			}
			uploadNames, err := listOwnedDirectory(store.uploadDir)
			if err != nil || len(uploadNames) != 0 {
				t.Fatalf("expired upload temporary files = %v, %v", uploadNames, err)
			}
			store.mu.Lock()
			active := len(store.uploadStates)
			reservations := len(store.reservedIDs)
			reservedBytes := store.reservedSize
			manifests := len(store.entries)
			terminal := len(store.uploadTerminal)
			store.mu.Unlock()
			if active != 0 || reservations != 0 || reservedBytes != 0 ||
				manifests != 0 || terminal != 1 {
				t.Fatalf(
					"expired publication state active=%d reservations=%d bytes=%d manifests=%d terminal=%d",
					active, reservations, reservedBytes, manifests, terminal,
				)
			}
		})
	}
}

func TestUploadValidationRejectsUnknownMediaNamesIDsSizesAndDigests(t *testing.T) {
	raw := testPNG(t, 1, 1)
	base := BeginUpload{
		UploadID: "upl_valid", AttachmentID: "att_valid",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	tests := []struct {
		name   string
		mutate func(*BeginUpload)
	}{
		{"upload id", func(value *BeginUpload) { value.UploadID = "bad" }},
		{"attachment id", func(value *BeginUpload) { value.AttachmentID = "bad" }},
		{"name traversal", func(value *BeginUpload) { value.Name = "../safe.png" }},
		{"name control", func(value *BeginUpload) { value.Name = "bad\nname.png" }},
		{"unknown MIME", func(value *BeginUpload) { value.MIMEType = "image/webp" }},
		{"zero size", func(value *BeginUpload) { value.SizeBytes = 0 }},
		{"oversize", func(value *BeginUpload) { value.SizeBytes = DefaultMaxItemBytes + 1 }},
		{"short digest", func(value *BeginUpload) { value.SHA256 = "abcd" }},
		{"uppercase digest", func(value *BeginUpload) { value.SHA256 = strings.Repeat("A", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "attachments"), Options{})
			value := base
			test.mutate(&value)
			if _, err := store.Begin(t.Context(), value); err == nil {
				t.Fatalf("invalid Begin was accepted: %#v", value)
			}
			names, err := listOwnedDirectory(store.uploadDir)
			if err != nil || len(names) != 0 {
				t.Fatalf("invalid begin left bytes: %v, %v", names, err)
			}
		})
	}
}

func TestUploadTemporaryBytesAreNeverManifestOrOutputData(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	raw := testPNG(t, 1, 1)
	encoded := base64.StdEncoding.EncodeToString(raw)
	request := BeginUpload{
		UploadID: "upl_privacy", AttachmentID: "att_privacy",
		Name: "safe.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}
	accepted, err := store.Begin(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", accepted), encoded) {
		t.Fatal("accepted acknowledgement exposed base64")
	}
	if err := store.Chunk(t.Context(), request.UploadID, 0, encoded); err != nil {
		t.Fatal(err)
	}
	committed, err := store.Commit(t.Context(), request.UploadID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(fmt.Sprintf("%#v", committed), encoded) ||
		strings.Contains(fmt.Sprintf("%#v", committed), store.root.Path()) {
		t.Fatal("terminal acknowledgement exposed base64 or runtime path")
	}
	manifestData, err := os.ReadFile(filepath.Join(
		store.manifestDir.Path(), manifestFilename(request.AttachmentID),
	))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(manifestData), encoded) ||
		strings.Contains(string(manifestData), store.root.Path()) {
		t.Fatal("durable manifest exposed base64 or runtime path")
	}
}
