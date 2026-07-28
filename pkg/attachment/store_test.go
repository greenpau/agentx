package attachment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestFileImportPersistsSafeManifestAndResolvesAfterRestart(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "selected screenshot.png")
	raw := testPNG(t, 3, 2)
	writeTestFile(t, source, raw, 0o600)

	store := openTestStore(t, filepath.Join(root, "session", "attachments"), Options{})
	manifest, err := store.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_screenshot",
		Path:         source,
		Name:         "screen.png",
		MIMEType:     MIMEPNG,
	})
	if err != nil {
		t.Fatalf("ImportFile() error = %v", err)
	}
	if manifest.AttachmentID != "att_screenshot" ||
		manifest.Kind != KindImage ||
		manifest.Name != "screen.png" ||
		manifest.MIMEType != MIMEPNG ||
		manifest.SizeBytes <= 0 ||
		!digestPattern.MatchString(manifest.SHA256) ||
		manifest.StorageID != storageID(manifest.SHA256) {
		t.Fatalf("manifest = %#v", manifest)
	}
	if encoded, err := json.Marshal(manifest); err != nil {
		t.Fatal(err)
	} else if bytes.Contains(encoded, []byte(source)) ||
		bytes.Contains(encoded, []byte(base64.StdEncoding.EncodeToString(raw))) {
		t.Fatalf("manifest leaked source path or bytes: %s", encoded)
	}
	gotManifest, got, err := store.Resolve(t.Context(), manifest.AttachmentID)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if gotManifest != manifest {
		t.Fatalf("Resolve() manifest = %#v, want %#v", gotManifest, manifest)
	}
	if _, err := png.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("normalized PNG is invalid: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened := openTestStore(t, filepath.Join(root, "session", "attachments"), Options{})
	reopenedManifest, reopenedBytes, err := reopened.Resolve(t.Context(), manifest.AttachmentID)
	if err != nil {
		t.Fatalf("Resolve() after restart error = %v", err)
	}
	if reopenedManifest != manifest || !bytes.Equal(reopenedBytes, got) {
		t.Fatalf("restart resolution changed immutable attachment")
	}
	assertPrivateTree(t, filepath.Join(root, "session", "attachments"))
}

func TestFileImportSupportsJPEGAndConservativePDF(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	cases := []struct {
		id   ID
		name string
		mime string
		data []byte
		kind Kind
	}{
		{"att_jpeg", "photo.jpg", MIMEJPEG, testJPEG(t, 4, 3), KindImage},
		{"att_pdf", "report.pdf", MIMEPDF, testPDF(t, 2), KindDocument},
	}
	for _, test := range cases {
		path := filepath.Join(root, test.name)
		writeTestFile(t, path, test.data, 0o600)
		manifest, err := store.ImportFile(t.Context(), FileImport{
			AttachmentID: test.id, Path: path, Name: test.name, MIMEType: test.mime,
		})
		if err != nil {
			t.Fatalf("ImportFile(%s) error = %v", test.mime, err)
		}
		if manifest.Kind != test.kind || manifest.MIMEType != test.mime {
			t.Fatalf("manifest = %#v", manifest)
		}
	}
	resolved, err := store.ResolveMany(t.Context(), []ID{"att_jpeg", "att_pdf"})
	if err != nil {
		t.Fatalf("ResolveMany() error = %v", err)
	}
	if len(resolved) != 2 ||
		resolved[0].Manifest.AttachmentID != "att_jpeg" ||
		resolved[1].Manifest.AttachmentID != "att_pdf" {
		t.Fatalf("ResolveMany() order = %#v", resolved)
	}
}

func TestFileImportRejectsUnsafeSourcesAndNeverLeaksPath(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	valid := testPNG(t, 1, 1)
	regular := filepath.Join(root, "secret-source.png")
	writeTestFile(t, regular, valid, 0o600)

	tests := []struct {
		name string
		path string
		want error
	}{
		{"empty", "", ErrUnsafeSource},
		{"directory", root, ErrUnsafeSource},
		{"missing", filepath.Join(root, "private-missing.png"), ErrUnsafeSource},
	}
	empty := filepath.Join(root, "empty.png")
	writeTestFile(t, empty, nil, 0o600)
	tests = append(tests, struct {
		name string
		path string
		want error
	}{"empty file", empty, ErrUnsafeSource})

	symlink := filepath.Join(root, "linked.png")
	if err := os.Symlink(regular, symlink); err == nil {
		tests = append(tests, struct {
			name string
			path string
			want error
		}{"symlink", symlink, ErrUnsafeSource})
	}
	hardlink := filepath.Join(root, "hardlinked.png")
	if err := os.Link(regular, hardlink); err == nil {
		tests = append(tests, struct {
			name string
			path string
			want error
		}{"hard link", hardlink, ErrUnsafeSource})
	}
	unreadable := filepath.Join(root, "unreadable.png")
	writeTestFile(t, unreadable, valid, 0o000)
	defer os.Chmod(unreadable, 0o600)
	if _, err := os.Open(unreadable); err != nil {
		tests = append(tests, struct {
			name string
			path string
			want error
		}{"unreadable", unreadable, ErrUnsafeSource})
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: ID(fmt.Sprintf("att_unsafe_%d", index)),
				Path:         test.path, Name: "safe.png", MIMEType: MIMEPNG,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("ImportFile() error = %v, want %v", err, test.want)
			}
			if test.path != "" && strings.Contains(fmt.Sprint(err), test.path) {
				t.Fatalf("error leaked source path: %v", err)
			}
		})
	}
}

func TestFileImportDetectsReplacementGrowthAndTruncation(t *testing.T) {
	for _, test := range []struct {
		name string
		hook func(t *testing.T, path string) func()
	}{
		{
			name: "replacement",
			hook: func(t *testing.T, path string) func() {
				return func() {
					backup := path + ".old"
					if err := os.Rename(path, backup); err != nil {
						t.Error(err)
						return
					}
					writeTestFile(t, path, testPNG(t, 2, 2), 0o600)
				}
			},
		},
		{
			name: "growth",
			hook: func(t *testing.T, path string) func() {
				return func() {
					file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = file.Write([]byte("growth"))
					_ = file.Close()
				}
			},
		},
		{
			name: "truncation",
			hook: func(t *testing.T, path string) func() {
				return func() {
					if err := os.Truncate(path, 8); err != nil {
						t.Error(err)
					}
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "source.png")
			writeTestFile(t, path, testPNG(t, 4, 4), 0o600)
			store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
			if test.name == "replacement" {
				store.beforeSourceRead = test.hook(t, path)
			} else {
				store.afterSourceRead = test.hook(t, path)
			}
			_, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: "att_race", Path: path, Name: "safe.png", MIMEType: MIMEPNG,
			})
			if !errors.Is(err, ErrUnsafeSource) && !errors.Is(err, ErrSizeMismatch) {
				t.Fatalf("ImportFile() error = %v", err)
			}
			if strings.Contains(fmt.Sprint(err), path) {
				t.Fatalf("error leaked source path: %v", err)
			}
			if _, _, resolveErr := store.Resolve(t.Context(), "att_race"); !errors.Is(resolveErr, ErrNotCommitted) {
				t.Fatalf("failed import became visible: %v", resolveErr)
			}
		})
	}
}

func TestFileImportRejectsMediaMismatchMalformedAndBounds(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	cases := []struct {
		id   ID
		name string
		mime string
		data []byte
		want error
	}{
		{"att_mismatch", "wrong.jpg", MIMEJPEG, testPNG(t, 1, 1), ErrMediaMismatch},
		{"att_svg", "vector.svg", "image/svg+xml", []byte("<svg/>"), ErrUnsupportedMedia},
		{"att_text", "text.txt", "", []byte("plain text"), ErrUnsupportedMedia},
		{"att_png_truncated", "bad.png", MIMEPNG, pngSignature, ErrMalformedMedia},
		{"att_jpeg_truncated", "bad.jpg", MIMEJPEG, []byte{0xff, 0xd8, 0xff}, ErrMalformedMedia},
		{"att_pdf_truncated", "bad.pdf", MIMEPDF, []byte("%PDF-1.7"), ErrMalformedMedia},
		{"att_pdf_active", "active.pdf", MIMEPDF, append(testPDF(t, 1), []byte("/JavaScript")...), ErrMalformedMedia},
	}
	for _, test := range cases {
		t.Run(string(test.id), func(t *testing.T) {
			path := filepath.Join(root, string(test.id))
			writeTestFile(t, path, test.data, 0o600)
			_, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: test.id, Path: path, Name: test.name, MIMEType: test.mime,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("ImportFile() error = %v, want %v", err, test.want)
			}
		})
	}

	limits := DefaultLimits()
	limits.MaxImageDimension = 1
	limits.MaxImagePixels = 1
	bounded := openTestStore(t, filepath.Join(root, "bounded"), Options{Limits: limits})
	path := filepath.Join(root, "large.png")
	writeTestFile(t, path, testPNG(t, 2, 1), 0o600)
	if _, err := bounded.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_large", Path: path, Name: "large.png", MIMEType: MIMEPNG,
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("oversize image error = %v", err)
	}

	limits = DefaultLimits()
	limits.MaxPDFPages = 1
	pdfBounded := openTestStore(t, filepath.Join(root, "pdf-bounded"), Options{Limits: limits})
	pdfPath := filepath.Join(root, "many.pdf")
	writeTestFile(t, pdfPath, testPDF(t, 2), 0o600)
	if _, err := pdfBounded.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_many_pages", Path: pdfPath, Name: "many.pdf", MIMEType: MIMEPDF,
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("PDF page bound error = %v", err)
	}
}

func TestStoreDeduplicatesContentCopiesForksAndCollectsSafely(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "same.png")
	writeTestFile(t, source, testPNG(t, 2, 2), 0o600)
	store := openTestStore(t, filepath.Join(root, "source-store"), Options{})
	first, err := store.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_first", Path: source, Name: "first.png", MIMEType: MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_second", Path: source, Name: "second.png", MIMEType: MIMEPNG,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 || first.StorageID != second.StorageID ||
		first.AttachmentID == second.AttachmentID || first.Name == second.Name {
		t.Fatalf("content-addressed manifests = %#v %#v", first, second)
	}
	blobNames, err := listOwnedDirectory(store.blobDir)
	if err != nil || len(blobNames) != 1 {
		t.Fatalf("blob entries = %v, %v", blobNames, err)
	}

	destination := openTestStore(t, filepath.Join(root, "fork-store"), Options{})
	if err := store.CopyTo(t.Context(), destination, []ID{"att_second", "att_first"}); err != nil {
		t.Fatalf("CopyTo() error = %v", err)
	}
	for _, manifest := range []Manifest{first, second} {
		if err := destination.Verify(t.Context(), manifest); err != nil {
			t.Fatalf("fork Verify(%s) error = %v", manifest.AttachmentID, err)
		}
	}

	result, err := store.Collect(t.Context(), []ID{"att_second"})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if result.ManifestsRemoved != 1 || result.BlobsRemoved != 0 || result.BytesRemoved != 0 {
		t.Fatalf("Collect() = %#v", result)
	}
	if err := store.Verify(t.Context(), second); err != nil {
		t.Fatalf("retained reference was deleted: %v", err)
	}
	if _, _, err := store.Resolve(t.Context(), first.AttachmentID); !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("removed manifest still resolved: %v", err)
	}
	result, err = store.Collect(t.Context(), nil)
	if err != nil || result.BlobsRemoved != 1 || result.BytesRemoved != second.SizeBytes {
		t.Fatalf("final Collect() = %#v, %v", result, err)
	}
}

func TestStoreRejectsTamperedMissingAndUnknownManifestData(t *testing.T) {
	for _, test := range []struct {
		name   string
		tamper func(t *testing.T, store *Store, manifest Manifest)
		want   error
	}{
		{
			name: "missing blob",
			tamper: func(t *testing.T, store *Store, manifest Manifest) {
				t.Helper()
				if err := os.Remove(filepath.Join(store.blobDir.Path(), blobFilename(manifest.SHA256))); err != nil {
					t.Fatal(err)
				}
			},
			want: ErrTampered,
		},
		{
			name: "tampered blob",
			tamper: func(t *testing.T, store *Store, manifest Manifest) {
				t.Helper()
				writeTestFile(t, filepath.Join(store.blobDir.Path(), blobFilename(manifest.SHA256)), []byte("tampered"), 0o600)
			},
			want: ErrTampered,
		},
		{
			name: "unknown manifest member",
			tamper: func(t *testing.T, store *Store, manifest Manifest) {
				t.Helper()
				path := filepath.Join(store.manifestDir.Path(), manifestFilename(manifest.AttachmentID))
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"unknown":true`), 1)
				writeTestFile(t, path, raw, 0o600)
			},
			want: ErrInvalidManifest,
		},
		{
			name: "duplicate manifest member",
			tamper: func(t *testing.T, store *Store, manifest Manifest) {
				t.Helper()
				path := filepath.Join(store.manifestDir.Path(), manifestFilename(manifest.AttachmentID))
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1)
				writeTestFile(t, path, raw, 0o600)
			},
			want: ErrInvalidManifest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "source.png")
			writeTestFile(t, source, testPNG(t, 1, 1), 0o600)
			directory := filepath.Join(root, "attachments")
			store := openTestStore(t, directory, Options{})
			manifest, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: "att_tamper", Path: source, Name: "safe.png", MIMEType: MIMEPNG,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, store, manifest)
			if _, err := OpenStore(directory, Options{}); !errors.Is(err, test.want) {
				t.Fatalf("OpenStore() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveManyLimitsDuplicatesCancellationAndOrder(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	for index := 0; index < 2; index++ {
		path := filepath.Join(root, fmt.Sprintf("%d.png", index))
		writeTestFile(t, path, testPNG(t, index+1, 1), 0o600)
		if _, err := store.ImportFile(t.Context(), FileImport{
			AttachmentID: ID(fmt.Sprintf("att_order_%d", index)),
			Path:         path, Name: fmt.Sprintf("%d.png", index), MIMEType: MIMEPNG,
		}); err != nil {
			t.Fatal(err)
		}
	}
	resolved, err := store.ResolveMany(t.Context(), []ID{"att_order_1", "att_order_0"})
	if err != nil || resolved[0].Manifest.AttachmentID != "att_order_1" ||
		resolved[1].Manifest.AttachmentID != "att_order_0" {
		t.Fatalf("ordered resolve = %#v, %v", resolved, err)
	}
	if _, err := store.ResolveMany(t.Context(), []ID{"att_order_0", "att_order_0"}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate error = %v", err)
	}
	if _, err := store.ResolveMany(t.Context(), nil); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("empty error = %v", err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.ResolveMany(cancelled, []ID{"att_order_0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestResolveManyExactCountAndAggregateBoundaries(t *testing.T) {
	t.Run("count", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "same.png")
		writeTestFile(t, path, testPNG(t, 1, 1), 0o600)
		store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
		ids := make([]ID, 0, DefaultMaxAttachmentsPerMessage+1)
		for index := 0; index <= DefaultMaxAttachmentsPerMessage; index++ {
			id := ID(fmt.Sprintf("att_count_%d", index))
			if _, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: id, Path: path, Name: fmt.Sprintf("%d.png", index), MIMEType: MIMEPNG,
			}); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		if _, err := store.ResolveMany(
			t.Context(), ids[:DefaultMaxAttachmentsPerMessage],
		); err != nil {
			t.Fatalf("exact count error = %v", err)
		}
		if _, err := store.ResolveMany(t.Context(), ids); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("over-count error = %v", err)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		root := t.TempDir()
		rawA := testPNG(t, 1, 1)
		rawB := testPNG(t, 2, 1)
		normalizedA, err := normalizeMedia(rawA, MIMEPNG, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		normalizedB, err := normalizeMedia(rawB, MIMEPNG, DefaultLimits())
		if err != nil {
			t.Fatal(err)
		}
		itemLimit := int64(max(
			len(rawA), len(rawB), len(normalizedA.bytes), len(normalizedB.bytes),
		))
		limits := DefaultLimits()
		limits.MaxItemBytes = itemLimit
		limits.MaxAggregateBytes = itemLimit
		limits.MaxModelRequestMediaBytes = itemLimit
		limits.MaxStorageBytes = itemLimit * 2
		limits.MaxChunkDecodedBytes = int(itemLimit)
		limits.MaxChunkEncodedBytes = base64.StdEncoding.EncodedLen(int(itemLimit))
		store := openTestStore(t, filepath.Join(root, "attachments"), Options{Limits: limits})
		for index, raw := range [][]byte{rawA, rawB} {
			path := filepath.Join(root, fmt.Sprintf("%d.png", index))
			writeTestFile(t, path, raw, 0o600)
			if _, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: ID(fmt.Sprintf("att_aggregate_%d", index)),
				Path:         path, Name: fmt.Sprintf("%d.png", index), MIMEType: MIMEPNG,
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := store.ResolveMany(
			t.Context(), []ID{"att_aggregate_0"},
		); err != nil {
			t.Fatalf("single attachment error = %v", err)
		}
		if _, err := store.ResolveMany(
			t.Context(), []ID{"att_aggregate_0", "att_aggregate_1"},
		); !errors.Is(err, ErrResourceLimit) {
			t.Fatalf("over-aggregate error = %v", err)
		}
	})
}

func TestFileImportRejectsExactPerItemOverflowBeforeReading(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversize.png")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(DefaultMaxItemBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	if _, err := store.ImportFile(t.Context(), FileImport{
		AttachmentID: "att_oversize", Path: path, Name: "oversize.png", MIMEType: MIMEPNG,
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("ImportFile() error = %v", err)
	}
}

func TestCapabilityAndManifestValidationAreClosedAndExact(t *testing.T) {
	capability, err := CapabilityFor(Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if capability.ProtocolVersion != 1 ||
		len(capability.Sources) != 2 ||
		capability.Sources[0] != (SourceCapability{
			Source: SourceFilePath, Scope: SourceScopeInitialCLI,
		}) ||
		capability.Sources[1] != (SourceCapability{
			Source: SourceStreamJSON, Scope: SourceScopePerTurn,
		}) ||
		len(capability.MediaTypes) != 3 {
		t.Fatalf("capability = %#v", capability)
	}
	gotMIMEs := []string{
		capability.MediaTypes[0].MIMEType,
		capability.MediaTypes[1].MIMEType,
		capability.MediaTypes[2].MIMEType,
	}
	if strings.Join(gotMIMEs, ",") != "image/png,image/jpeg,application/pdf" {
		t.Fatalf("MIME types = %v", gotMIMEs)
	}
	if capability.Limits.MaxChunkEncodedBytes > 8<<20 ||
		capability.Limits.MaxModelRequestMediaBytes > capability.Limits.MaxAggregateBytes {
		t.Fatalf("limits = %#v", capability.Limits)
	}
	tooLarge := DefaultLimits()
	tooLarge.MaxStorageBytes++
	if _, err := CapabilityFor(tooLarge); !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("broadened hard limit error = %v", err)
	}
	for _, id := range []ID{"", "att_", "bad_value", "att_has.dot", ID("att_" + strings.Repeat("x", 64))} {
		if ValidateAttachmentID(id) == nil {
			t.Fatalf("invalid attachment ID accepted: %q", id)
		}
	}
	for _, id := range []UploadID{"", "upl_", "att_wrong", "upl_has.dot", UploadID("upl_" + strings.Repeat("x", 64))} {
		if ValidateUploadID(id) == nil {
			t.Fatalf("invalid upload ID accepted: %q", id)
		}
	}
	manifest := Manifest{
		AttachmentID: "att_valid", Kind: KindImage, Name: "safe.png",
		MIMEType: MIMEPNG, SizeBytes: 1, SHA256: strings.Repeat("a", 64),
		StorageID: storageID(strings.Repeat("a", 64)),
	}
	if err := manifest.Validate(DefaultLimits()); err != nil {
		t.Fatalf("valid manifest error = %v", err)
	}
	for _, mutate := range []func(*Manifest){
		func(value *Manifest) { value.Kind = "audio" },
		func(value *Manifest) { value.Name = "../unsafe" },
		func(value *Manifest) { value.MIMEType = "image/svg+xml" },
		func(value *Manifest) { value.SizeBytes = 0 },
		func(value *Manifest) { value.SHA256 = strings.Repeat("A", 64) },
		func(value *Manifest) { value.StorageID = "path:/tmp/blob" },
	} {
		value := manifest
		mutate(&value)
		if value.Validate(DefaultLimits()) == nil {
			t.Fatalf("invalid manifest accepted: %#v", value)
		}
	}
}

func TestConcurrentImportsReserveAttachmentIDExactlyOnce(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.png")
	writeTestFile(t, source, testPNG(t, 2, 2), 0o600)
	store := openTestStore(t, filepath.Join(root, "attachments"), Options{})
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.ImportFile(t.Context(), FileImport{
				AttachmentID: "att_concurrent", Path: source, Name: "safe.png", MIMEType: MIMEPNG,
			})
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	var successes, duplicates int
	for err := range errorsSeen {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDuplicate):
			duplicates++
		default:
			t.Fatalf("unexpected error = %v", err)
		}
	}
	if successes != 1 || duplicates != 1 {
		t.Fatalf("successes=%d duplicates=%d", successes, duplicates)
	}
}

func TestStoreDurableManifestCeilingExactBoundaryAndReopen(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "attachments")
	source := filepath.Join(root, "source.png")
	raw := testPNG(t, 2, 2)
	writeTestFile(t, source, raw, 0o600)

	limits := DefaultLimits()
	limits.MaxConcurrentUploads = 1
	limits.MaxUploadsPerSession = 2
	options := Options{Limits: limits}
	store := openTestStore(t, directory, options)
	importFile := func(store *Store, id ID) (Manifest, error) {
		return store.ImportFile(t.Context(), FileImport{
			AttachmentID: id,
			Path:         source,
			Name:         string(id) + ".png",
			MIMEType:     MIMEPNG,
		})
	}

	for _, id := range []ID{"att_limit_0", "att_limit_1"} {
		if _, err := importFile(store, id); err != nil {
			t.Fatalf("exact-bound ImportFile(%s) error = %v", id, err)
		}
	}
	sourceRead := false
	store.beforeSourceRead = func() {
		sourceRead = true
	}
	if _, err := importFile(store, "att_limit_over"); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("over-bound ImportFile() error = %v, want ErrResourceLimit", err)
	}
	if sourceRead {
		t.Fatal("over-bound import read caller-selected source bytes")
	}
	store.beforeSourceRead = nil

	if _, err := store.Begin(t.Context(), BeginUpload{
		UploadID: "upl_limit_over", AttachmentID: "att_upload_over",
		Name: "upload.png", SizeBytes: int64(len(raw)), MIMEType: MIMEPNG,
		SHA256: rawDigest(raw),
	}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("over-bound Begin() error = %v, want ErrResourceLimit", err)
	}
	uploadNames, err := listOwnedDirectory(store.uploadDir)
	if err != nil || len(uploadNames) != 0 {
		t.Fatalf("over-bound Begin() temporary files = %v, %v", uploadNames, err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openTestStore(t, directory, options)
	for _, id := range []ID{"att_limit_0", "att_limit_1"} {
		if _, _, err := reopened.Resolve(t.Context(), id); err != nil {
			t.Fatalf("exact-bound reopen Resolve(%s) error = %v", id, err)
		}
	}
	if _, err := importFile(reopened, "att_limit_over"); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("reopened over-bound ImportFile() error = %v, want ErrResourceLimit", err)
	}

	cleanup, err := reopened.Collect(t.Context(), []ID{"att_limit_1"})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if cleanup.ManifestsRemoved != 1 {
		t.Fatalf("Collect() = %#v, want one removed manifest", cleanup)
	}
	if _, err := importFile(reopened, "att_limit_2"); err != nil {
		t.Fatalf("ImportFile() after collection error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedAgain := openTestStore(t, directory, options)
	for _, id := range []ID{"att_limit_1", "att_limit_2"} {
		if _, _, err := reopenedAgain.Resolve(t.Context(), id); err != nil {
			t.Fatalf("post-collection reopen Resolve(%s) error = %v", id, err)
		}
	}
}

func TestStoreReopenRejectsConfiguredDurableManifestOverflow(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "attachments")
	source := filepath.Join(root, "source.png")
	writeTestFile(t, source, testPNG(t, 1, 1), 0o600)

	higher := DefaultLimits()
	higher.MaxConcurrentUploads = 1
	higher.MaxUploadsPerSession = 3
	store := openTestStore(t, directory, Options{Limits: higher})
	for index := range 3 {
		if _, err := store.ImportFile(t.Context(), FileImport{
			AttachmentID: ID(fmt.Sprintf("att_reopen_limit_%d", index)),
			Path:         source,
			Name:         fmt.Sprintf("%d.png", index),
			MIMEType:     MIMEPNG,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	lower := higher
	lower.MaxUploadsPerSession = 2
	if _, err := OpenStore(directory, Options{Limits: lower}); !errors.Is(err, ErrResourceLimit) {
		t.Fatalf("OpenStore() error = %v, want ErrResourceLimit", err)
	}
}

func openTestStore(t *testing.T, directory string, options Options) *Store {
	t.Helper()
	store, err := OpenStore(directory, options)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func testPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.SetNRGBA(x, y, color.NRGBA{
				R: uint8(30 + x), G: uint8(40 + y), B: 50, A: uint8(200 + (x+y)%55),
			})
		}
	}
	var output bytes.Buffer
	if err := png.Encode(&output, value); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	value := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := range height {
		for x := range width {
			value.Set(x, y, color.RGBA{R: uint8(20 + x), G: uint8(30 + y), B: 40, A: 255})
		}
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, value, &jpeg.Options{Quality: 70}); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testPDF(t *testing.T, pages int) []byte {
	t.Helper()
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>"}
	kids := make([]string, 0, pages)
	for page := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+page))
	}
	objects = append(objects, fmt.Sprintf(
		"<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), pages,
	))
	for range pages {
		objects = append(objects,
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		)
	}
	return testPDFObjects(t, objects)
}

func testPDFObjects(t *testing.T, objects []string) []byte {
	t.Helper()
	if len(objects) == 0 {
		t.Fatal("PDF fixture requires at least one object")
	}
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n")
	offsets := make([]int, len(objects)+1)
	for index, body := range objects {
		offsets[index+1] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n%s\nendobj\n", index+1, body)
	}
	xref := output.Len()
	fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R >>\n", len(offsets))
	output.WriteString(fmt.Sprintf("startxref\n%d\n%%%%EOF\n", xref))
	return output.Bytes()
}

func assertPrivateTree(t *testing.T, root string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("directory mode %o", info.Mode().Perm())
			}
		} else if info.Mode().Perm() != 0o600 {
			return fmt.Errorf("file mode %o", info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("private tree validation failed: %v", err)
	}
}

func rawDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
