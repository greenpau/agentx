package attachment

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
)

func TestVerifyResolvedReappliesExactProviderBoundaryChecks(t *testing.T) {
	limits := DefaultLimits()
	tests := []struct {
		id   ID
		name string
		mime string
		kind Kind
		raw  []byte
	}{
		{"att_png_verify", "safe.png", MIMEPNG, KindImage, testPNG(t, 2, 2)},
		{"att_jpeg_verify", "safe.jpg", MIMEJPEG, KindImage, testJPEG(t, 2, 2)},
		{"att_pdf_verify", "safe.pdf", MIMEPDF, KindDocument, testPDF(t, 1)},
	}
	for _, test := range tests {
		t.Run(test.mime, func(t *testing.T) {
			normalized, err := normalizeMedia(test.raw, test.mime, limits)
			if err != nil {
				t.Fatal(err)
			}
			manifest := manifestForBytes(test.id, test.name, test.mime, test.kind, normalized.bytes)
			if err := VerifyResolved(manifest, normalized.bytes, limits); err != nil {
				t.Fatalf("VerifyResolved() error = %v", err)
			}
			tampered := append([]byte(nil), normalized.bytes...)
			tampered[len(tampered)/2] ^= 1
			if VerifyResolved(manifest, tampered, limits) == nil {
				t.Fatal("tampered bytes were accepted")
			}
		})
	}
}

func TestVerifyResolvedRejectsMetadataBearingImagesFromFakeSource(t *testing.T) {
	limits := DefaultLimits()
	pngWithText := addPNGTextChunk(t, testPNG(t, 1, 1))
	pngManifest := manifestForBytes(
		"att_png_metadata", "metadata.png", MIMEPNG, KindImage, pngWithText,
	)
	if err := VerifyResolved(pngManifest, pngWithText, limits); err == nil {
		t.Fatal("metadata-bearing PNG was accepted as normalized")
	}

	jpegRaw := testJPEG(t, 1, 1)
	jpegWithAPP1 := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0x00, 0x04, 'x', 'y'}, jpegRaw[2:]...)
	jpegManifest := manifestForBytes(
		"att_jpeg_metadata", "metadata.jpg", MIMEJPEG, KindImage, jpegWithAPP1,
	)
	if err := VerifyResolved(jpegManifest, jpegWithAPP1, limits); err == nil {
		t.Fatal("metadata-bearing JPEG was accepted as normalized")
	}
}

func TestVerifyResolvedRejectsDimensionsPagesActivePDFAndManifestMismatch(t *testing.T) {
	imageRaw, err := normalizePNG(testPNG(t, 2, 1), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	imageManifest := manifestForBytes(
		"att_dimensions", "safe.png", MIMEPNG, KindImage, imageRaw,
	)
	limits := DefaultLimits()
	limits.MaxImageDimension = 1
	limits.MaxImagePixels = 1
	if err := VerifyResolved(imageManifest, imageRaw, limits); err == nil {
		t.Fatal("dimension limit was not applied at provider boundary")
	}

	pdfRaw := testPDF(t, 2)
	pdfManifest := manifestForBytes(
		"att_pages", "safe.pdf", MIMEPDF, KindDocument, pdfRaw,
	)
	limits = DefaultLimits()
	limits.MaxPDFPages = 1
	if err := VerifyResolved(pdfManifest, pdfRaw, limits); err == nil {
		t.Fatal("page limit was not applied at provider boundary")
	}

	active := bytes.Replace(
		testPDF(t, 1), []byte("/MediaBox"),
		[]byte("/OpenAction 4 0 R /MediaBox"), 1,
	)
	activeManifest := manifestForBytes(
		"att_active", "active.pdf", MIMEPDF, KindDocument, active,
	)
	if err := VerifyResolved(activeManifest, active, DefaultLimits()); err == nil {
		t.Fatal("active PDF was accepted")
	}

	mismatch := imageManifest
	mismatch.MIMEType = MIMEJPEG
	mismatch.Kind = KindImage
	if err := VerifyResolved(mismatch, imageRaw, DefaultLimits()); err == nil {
		t.Fatal("MIME/magic mismatch was accepted")
	}
}

func TestNormalizePDFRejectsEncryptionFormsNoPagesAndInvalidCrossReference(t *testing.T) {
	base := testPDF(t, 1)
	tests := map[string][]byte{
		"encrypted": bytes.Replace(base, []byte("/Root"), []byte("/Encrypt 9 0 R /Root"), 1),
		"form":      bytes.Replace(base, []byte("/Root"), []byte("/AcroForm 9 0 R /Root"), 1),
		"no pages":  bytes.ReplaceAll(base, []byte("/Page "), []byte("/Leaf ")),
		"bad xref":  bytes.Replace(base, []byte("startxref\n"), []byte("startxref\n999999"), 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
				t.Fatal("unsafe or malformed PDF was accepted")
			}
		})
	}
}

func TestPDFValidatorDecodesNamesAndIgnoresComments(t *testing.T) {
	t.Run("escaped active name", func(t *testing.T) {
		raw := testPDFObjects(t, []string{
			"<< /Type /Catalog /Pages 2 0 R /Open#41ction 3 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		})
		if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
			t.Fatal("escaped active-content name was accepted")
		}
	})

	t.Run("escaped duplicate dictionary key", func(t *testing.T) {
		raw := testPDFObjects(t, []string{
			"<< /Type /Catalog /Pages 2 0 R /Pa#67es 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		})
		if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
			t.Fatal("escaped duplicate dictionary key was accepted")
		}
	})

	t.Run("active-name comment is inert", func(t *testing.T) {
		raw := testPDFObjects(t, []string{
			"<< /Type /Catalog\n% /Open#41ction 9 0 R\n/Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R\n% /AA 9 0 R\n/MediaBox [0 0 100 100] >>",
		})
		if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err != nil {
			t.Fatalf("safe comments changed PDF meaning: %v", err)
		}
	})

	t.Run("comment cannot supply catalog pages", func(t *testing.T) {
		raw := testPDFObjects(t, []string{
			"<< /Type /Catalog\n% /Pages 2 0 R\n>>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		})
		if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
			t.Fatal("comment-spoofed catalog page tree was accepted")
		}
	})
}

func TestPDFValidatorRejectsUnsupportedFeaturesWithValidStructure(t *testing.T) {
	baseObjects := func(catalogExtra string) []string {
		return []string{
			"<< /Type /Catalog /Pages 2 0 R " + catalogExtra + " >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		}
	}
	encrypted := testPDFObjects(t, baseObjects(""))
	encrypted = bytes.Replace(
		encrypted,
		[]byte("/Root 1 0 R >>"),
		[]byte("/Root 1 0 R /Encr#79pt 3 0 R >>"),
		1,
	)
	tests := map[string][]byte{
		"action": testPDFObjects(t, baseObjects("/J#53 (alert)")),
		"embedded content": testPDFObjects(
			t, baseObjects("/Embedded#46iles 3 0 R"),
		),
		"form":       testPDFObjects(t, baseObjects("/Acro#46orm 3 0 R")),
		"encryption": encrypted,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := normalizeMedia(raw, MIMEPDF, DefaultLimits())
			if !errors.Is(err, ErrUnsupportedMedia) {
				t.Fatalf("normalizeMedia() error = %v, want ErrUnsupportedMedia", err)
			}
		})
	}
}

func TestPDFValidatorRequiresCompleteAccurateClassicXRef(t *testing.T) {
	base := testPDF(t, 1)
	xref := bytes.Index(base, []byte("xref\n"))
	if xref < 0 {
		t.Fatal("fixture lacks xref")
	}
	firstEntry := bytes.Index(base[xref:], []byte("0000000009 00000 n"))
	if firstEntry < 0 {
		t.Fatal("fixture first object offset changed")
	}
	firstEntry += xref

	badOffset := append([]byte(nil), base...)
	copy(badOffset[firstEntry:firstEntry+10], []byte("0000000010"))

	incomplete := bytes.Replace(
		base,
		[]byte("xref\n0 4\n"),
		[]byte("xref\n0 3\n"),
		1,
	)
	tests := map[string][]byte{
		"object offset points inside header": badOffset,
		"incomplete object table":            incomplete,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
				t.Fatal("invalid classic xref was accepted")
			}
		})
	}
}

func TestPDFValidatorRejectsObjectXRefStreamsAndIncrementalUpdates(t *testing.T) {
	objectStream := testPDFObjects(t, []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		"<< /Type /ObjStm /N 0 /First 0 /Length 0 >>\nstream\n\nendstream",
	})

	var xrefStream bytes.Buffer
	xrefStream.WriteString("%PDF-1.7\n")
	offset := xrefStream.Len()
	xrefStream.WriteString("1 0 obj\n<< /Type /XRef /Size 2 /Root 2 0 R /Length 0 >>\n")
	xrefStream.WriteString("stream\n\nendstream\nendobj\n")
	fmt.Fprintf(&xrefStream, "startxref\n%d\n%%%%EOF\n", offset)

	base := testPDF(t, 1)
	previousXRef := bytes.Index(base, []byte("xref\n"))
	var incremental bytes.Buffer
	incremental.Write(base)
	newObjectOffset := incremental.Len()
	incremental.WriteString("4 0 obj\n<< /Producer (incremental) >>\nendobj\n")
	newXRef := incremental.Len()
	incremental.WriteString("xref\n4 1\n")
	fmt.Fprintf(&incremental, "%010d 00000 n \n", newObjectOffset)
	fmt.Fprintf(
		&incremental,
		"trailer\n<< /Size 5 /Root 1 0 R /Prev %d >>\n",
		previousXRef,
	)
	fmt.Fprintf(&incremental, "startxref\n%d\n%%%%EOF\n", newXRef)

	tests := map[string][]byte{
		"object stream":      objectStream,
		"xref stream":        xrefStream.Bytes(),
		"incremental update": incremental.Bytes(),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
				t.Fatal("unsupported PDF structure was accepted")
			}
		})
	}
}

func TestPDFValidatorChecksCatalogPagesGraphAndCounts(t *testing.T) {
	tests := map[string][]byte{
		"page count mismatch": testPDFObjects(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 2 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		}),
		"wrong parent": testPDFObjects(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 1 0 R /MediaBox [0 0 100 100] >>",
		}),
		"unreachable page": testPDFObjects(t, []string{
			"<< /Type /Catalog /Pages 2 0 R >>",
			"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 100 100] >>",
		}),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeMedia(raw, MIMEPDF, DefaultLimits()); err == nil {
				t.Fatal("inconsistent PDF catalog/pages graph was accepted")
			}
		})
	}
}

func manifestForBytes(id ID, name, mime string, kind Kind, data []byte) Manifest {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return Manifest{
		AttachmentID: id, Kind: kind, Name: name, MIMEType: mime,
		SizeBytes: int64(len(data)), SHA256: digest, StorageID: storageID(digest),
	}
}

func addPNGTextChunk(t *testing.T, raw []byte) []byte {
	t.Helper()
	if len(raw) < 12 || !bytes.Equal(raw[:8], pngSignature) {
		t.Fatal("invalid PNG fixture")
	}
	// Insert one valid ancillary tEXt chunk immediately before IEND.
	iend := bytes.LastIndex(raw, []byte("IEND"))
	if iend < 4 {
		t.Fatal("PNG fixture lacks IEND")
	}
	start := iend - 4
	payload := []byte("Comment\x00private")
	chunkType := []byte("tEXt")
	chunk := make([]byte, 12+len(payload))
	binary.BigEndian.PutUint32(chunk[:4], uint32(len(payload)))
	copy(chunk[4:8], chunkType)
	copy(chunk[8:8+len(payload)], payload)
	binary.BigEndian.PutUint32(chunk[8+len(payload):], crc32.ChecksumIEEE(append(chunkType, payload...)))
	output := make([]byte, 0, len(raw)+len(chunk))
	output = append(output, raw[:start]...)
	output = append(output, chunk...)
	output = append(output, raw[start:]...)
	return output
}

func TestDisplayNameAndMIMEBoundsUseBytesAndRejectControlAuthority(t *testing.T) {
	limits := DefaultLimits()
	for _, name := range []string{
		"", " leading.png", "trailing.png ", ".", "..", "a/b.png", `a\b.png`,
		"line\nbreak.png", "bidi\u202etxt.png", strings.Repeat("é", 128),
	} {
		if validateDisplayName(name, limits) == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	if validateDisplayName(strings.Repeat("a", limits.MaxDisplayNameBytes), limits) != nil {
		t.Fatal("exact display-name byte bound was rejected")
	}
	if _, err := kindForMIME(strings.Repeat("x", limits.MaxMIMETypeBytes+1), limits); err == nil {
		t.Fatal("oversize MIME was accepted")
	}
}
