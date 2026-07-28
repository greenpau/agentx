package attachment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"sort"
	"strconv"
	"strings"
)

type normalizedMedia struct {
	kind     Kind
	mimeType string
	bytes    []byte
	digest   string
}

func normalizeMedia(data []byte, claimedMIME string, limits Limits) (normalizedMedia, error) {
	if len(data) == 0 {
		return normalizedMedia{}, fmt.Errorf("%w: empty content", ErrMalformedMedia)
	}
	if int64(len(data)) > limits.MaxItemBytes {
		return normalizedMedia{}, ErrResourceLimit
	}
	detected, err := detectMIME(data)
	if err != nil {
		return normalizedMedia{}, err
	}
	if claimedMIME != "" {
		if _, err := kindForMIME(claimedMIME, limits); err != nil {
			return normalizedMedia{}, err
		}
		if claimedMIME != detected {
			return normalizedMedia{}, ErrMediaMismatch
		}
	}

	var normalized []byte
	var kind Kind
	switch detected {
	case MIMEPNG:
		kind = KindImage
		normalized, err = normalizePNG(data, limits)
	case MIMEJPEG:
		kind = KindImage
		normalized, err = normalizeJPEG(data, limits)
	case MIMEPDF:
		kind = KindDocument
		err = validatePDF(data, limits)
		if err == nil {
			normalized = append([]byte(nil), data...)
		}
	default:
		err = ErrUnsupportedMedia
	}
	if err != nil {
		return normalizedMedia{}, err
	}
	if len(normalized) == 0 || int64(len(normalized)) > limits.MaxItemBytes {
		return normalizedMedia{}, ErrResourceLimit
	}
	digest := sha256.Sum256(normalized)
	return normalizedMedia{
		kind: kind, mimeType: detected, bytes: normalized,
		digest: hex.EncodeToString(digest[:]),
	}, nil
}

// VerifyResolved performs the complete provider-bound validation without
// filesystem access. It is suitable for adapters consuming an AttachmentSource
// fake as well as for Store.Resolve.
func VerifyResolved(manifest Manifest, data []byte, limits Limits) error {
	normalizedLimits, err := normalizeLimits(limits)
	if err != nil {
		return err
	}
	if err := manifest.Validate(normalizedLimits); err != nil {
		return err
	}
	if int64(len(data)) != manifest.SizeBytes {
		return ErrSizeMismatch
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != manifest.SHA256 ||
		manifest.StorageID != storageID(manifest.SHA256) {
		return ErrDigestMismatch
	}
	detected, err := detectMIME(data)
	if err != nil || detected != manifest.MIMEType {
		return ErrMediaMismatch
	}
	switch detected {
	case MIMEPNG:
		canonical, err := normalizePNG(data, normalizedLimits)
		if err != nil {
			return err
		}
		if !bytes.Equal(canonical, data) {
			return fmt.Errorf("%w: PNG is not the canonical metadata-free snapshot", ErrMalformedMedia)
		}
	case MIMEJPEG:
		if err := validateNormalizedJPEG(data, normalizedLimits); err != nil {
			return err
		}
	case MIMEPDF:
		if err := validatePDF(data, normalizedLimits); err != nil {
			return err
		}
	default:
		return ErrUnsupportedMedia
	}
	return nil
}

func detectMIME(data []byte) (string, error) {
	switch {
	case len(data) >= len(pngSignature) && bytes.Equal(data[:len(pngSignature)], pngSignature):
		return MIMEPNG, nil
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return MIMEJPEG, nil
	case len(data) >= 8 && bytes.HasPrefix(data, []byte("%PDF-")):
		return MIMEPDF, nil
	default:
		return "", ErrUnsupportedMedia
	}
}

var pngSignature = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func validateImageDimensions(config image.Config, limits Limits) error {
	if config.Width <= 0 || config.Height <= 0 ||
		config.Width > limits.MaxImageDimension ||
		config.Height > limits.MaxImageDimension {
		return ErrResourceLimit
	}
	pixels := int64(config.Width) * int64(config.Height)
	if pixels <= 0 || pixels > limits.MaxImagePixels {
		return ErrResourceLimit
	}
	return nil
}

func normalizePNG(data []byte, limits Limits) ([]byte, error) {
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid PNG", ErrMalformedMedia)
	}
	if err := validateImageDimensions(config, limits); err != nil {
		return nil, err
	}
	decoded, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid PNG", ErrMalformedMedia)
	}
	if err := validateDecodedBounds(decoded.Bounds(), config); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.DefaultCompression}).Encode(&output, decoded); err != nil {
		return nil, fmt.Errorf("%w: normalize PNG", ErrMalformedMedia)
	}
	return output.Bytes(), nil
}

func normalizeJPEG(data []byte, limits Limits) ([]byte, error) {
	if !completeJPEG(data) {
		return nil, fmt.Errorf("%w: truncated JPEG", ErrMalformedMedia)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JPEG", ErrMalformedMedia)
	}
	if err := validateImageDimensions(config, limits); err != nil {
		return nil, err
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JPEG", ErrMalformedMedia)
	}
	if err := validateDecodedBounds(decoded.Bounds(), config); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, decoded, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("%w: normalize JPEG", ErrMalformedMedia)
	}
	return output.Bytes(), nil
}

func validateNormalizedJPEG(data []byte, limits Limits) error {
	if !completeJPEG(data) {
		return fmt.Errorf("%w: truncated JPEG", ErrMalformedMedia)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: invalid JPEG", ErrMalformedMedia)
	}
	if err := validateImageDimensions(config, limits); err != nil {
		return err
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: invalid JPEG", ErrMalformedMedia)
	}
	if err := validateDecodedBounds(decoded.Bounds(), config); err != nil {
		return err
	}
	if jpegContainsMetadata(data) {
		return fmt.Errorf("%w: JPEG contains metadata", ErrMalformedMedia)
	}
	return nil
}

func completeJPEG(data []byte) bool {
	return len(data) >= 4 &&
		data[0] == 0xff && data[1] == 0xd8 &&
		data[len(data)-2] == 0xff && data[len(data)-1] == 0xd9
}

func jpegContainsMetadata(data []byte) bool {
	if len(data) < 4 || data[0] != 0xff || data[1] != 0xd8 {
		return true
	}
	offset := 2
	for offset < len(data) {
		for offset < len(data) && data[offset] == 0xff {
			offset++
		}
		if offset >= len(data) {
			return true
		}
		marker := data[offset]
		offset++
		switch {
		case marker == 0xd9:
			return false
		case marker == 0xda:
			// Metadata segments cannot begin inside entropy-coded scan data.
			return false
		case marker == 0x01 || marker >= 0xd0 && marker <= 0xd7:
			continue
		case marker == 0xfe || marker >= 0xe0 && marker <= 0xef:
			return true
		}
		if offset+2 > len(data) {
			return true
		}
		length := int(data[offset])<<8 | int(data[offset+1])
		if length < 2 || offset+length > len(data) {
			return true
		}
		offset += length
	}
	return true
}

func validateDecodedBounds(bounds image.Rectangle, config image.Config) error {
	if bounds.Empty() || bounds.Dx() != config.Width || bounds.Dy() != config.Height {
		return fmt.Errorf("%w: decoded image bounds changed", ErrMalformedMedia)
	}
	return nil
}

func validatePDF(data []byte, limits Limits) error {
	if int64(len(data)) > limits.MaxItemBytes {
		return ErrResourceLimit
	}
	if !validPDFHeader(data) {
		return fmt.Errorf("%w: invalid PDF header", ErrMalformedMedia)
	}
	headerEnd, err := pdfHeaderEnd(data)
	if err != nil {
		return err
	}
	xrefOffset, startMarker, err := parsePDFFinalTrailer(data)
	if err != nil || xrefOffset < int64(headerEnd) || xrefOffset >= int64(startMarker) {
		return fmt.Errorf("%w: invalid PDF startxref", ErrMalformedMedia)
	}
	entries, trailer, err := parsePDFClassicXRef(data, int(xrefOffset), startMarker)
	if err != nil {
		return err
	}
	objects, err := parsePDFObjects(data, headerEnd, int(xrefOffset), entries)
	if err != nil {
		return err
	}
	if err := validatePDFReferences(objects, trailer, entries); err != nil {
		return err
	}
	if err := validatePDFPageTree(objects, trailer, limits); err != nil {
		return err
	}
	return nil
}

func validPDFHeader(data []byte) bool {
	if len(data) < len("%PDF-1.0") {
		return false
	}
	header := data[:len("%PDF-1.0")]
	if !bytes.HasPrefix(header, []byte("%PDF-")) {
		return false
	}
	switch string(header[len("%PDF-"):]) {
	case "1.0", "1.1", "1.2", "1.3", "1.4", "1.5", "1.6", "1.7", "2.0":
		return true
	default:
		return false
	}
}

const (
	maxPDFObjects   = 10_000
	maxPDFTokens    = 1_000_000
	maxPDFNesting   = 64
	maxPDFNameBytes = 256
)

type pdfTokenKind uint8

const (
	pdfTokenEOF pdfTokenKind = iota
	pdfTokenName
	pdfTokenNumber
	pdfTokenKeyword
	pdfTokenString
	pdfTokenDictStart
	pdfTokenDictEnd
	pdfTokenArrayStart
	pdfTokenArrayEnd
)

type pdfToken struct {
	kind       pdfTokenKind
	value      string
	start, end int
}

type pdfLexer struct {
	data   []byte
	offset int
	tokens int
}

func (l *pdfLexer) next() (pdfToken, error) {
	if err := l.skipSpaceAndComments(); err != nil {
		return pdfToken{}, err
	}
	if l.offset >= len(l.data) {
		return pdfToken{kind: pdfTokenEOF, start: l.offset, end: l.offset}, nil
	}
	if l.tokens >= maxPDFTokens {
		return pdfToken{}, ErrResourceLimit
	}
	l.tokens++
	start := l.offset
	switch l.data[l.offset] {
	case '/':
		l.offset++
		nameStart := l.offset
		for l.offset < len(l.data) && !pdfWhitespace(l.data[l.offset]) &&
			!pdfDelimiter(l.data[l.offset]) {
			l.offset++
		}
		name, err := decodePDFName(l.data[nameStart:l.offset])
		if err != nil {
			return pdfToken{}, err
		}
		return pdfToken{kind: pdfTokenName, value: name, start: start, end: l.offset}, nil
	case '(':
		if err := l.skipLiteralString(); err != nil {
			return pdfToken{}, err
		}
		return pdfToken{kind: pdfTokenString, start: start, end: l.offset}, nil
	case '<':
		if l.offset+1 < len(l.data) && l.data[l.offset+1] == '<' {
			l.offset += 2
			return pdfToken{kind: pdfTokenDictStart, start: start, end: l.offset}, nil
		}
		if err := l.skipHexString(); err != nil {
			return pdfToken{}, err
		}
		return pdfToken{kind: pdfTokenString, start: start, end: l.offset}, nil
	case '>':
		if l.offset+1 >= len(l.data) || l.data[l.offset+1] != '>' {
			return pdfToken{}, fmt.Errorf("%w: unmatched PDF dictionary delimiter", ErrMalformedMedia)
		}
		l.offset += 2
		return pdfToken{kind: pdfTokenDictEnd, start: start, end: l.offset}, nil
	case '[':
		l.offset++
		return pdfToken{kind: pdfTokenArrayStart, start: start, end: l.offset}, nil
	case ']':
		l.offset++
		return pdfToken{kind: pdfTokenArrayEnd, start: start, end: l.offset}, nil
	case ')':
		return pdfToken{}, fmt.Errorf("%w: unmatched PDF string delimiter", ErrMalformedMedia)
	}

	for l.offset < len(l.data) && !pdfWhitespace(l.data[l.offset]) &&
		!pdfDelimiter(l.data[l.offset]) {
		l.offset++
	}
	if l.offset == start {
		return pdfToken{}, fmt.Errorf("%w: invalid PDF token", ErrMalformedMedia)
	}
	value := string(l.data[start:l.offset])
	if validPDFNumber(value) {
		return pdfToken{kind: pdfTokenNumber, value: value, start: start, end: l.offset}, nil
	}
	return pdfToken{kind: pdfTokenKeyword, value: value, start: start, end: l.offset}, nil
}

func (l *pdfLexer) skipSpaceAndComments() error {
	for l.offset < len(l.data) {
		if pdfWhitespace(l.data[l.offset]) {
			l.offset++
			continue
		}
		if l.data[l.offset] != '%' {
			return nil
		}
		for l.offset < len(l.data) && l.data[l.offset] != '\r' && l.data[l.offset] != '\n' {
			l.offset++
		}
	}
	return nil
}

func (l *pdfLexer) skipLiteralString() error {
	l.offset++
	depth := 1
	for l.offset < len(l.data) {
		switch l.data[l.offset] {
		case '\\':
			l.offset++
			if l.offset >= len(l.data) {
				return fmt.Errorf("%w: unterminated PDF string escape", ErrMalformedMedia)
			}
			if l.data[l.offset] == '\r' && l.offset+1 < len(l.data) &&
				l.data[l.offset+1] == '\n' {
				l.offset += 2
			} else {
				l.offset++
			}
		case '(':
			depth++
			if depth > maxPDFNesting {
				return ErrResourceLimit
			}
			l.offset++
		case ')':
			depth--
			l.offset++
			if depth == 0 {
				return nil
			}
		default:
			l.offset++
		}
	}
	return fmt.Errorf("%w: unterminated PDF string", ErrMalformedMedia)
}

func (l *pdfLexer) skipHexString() error {
	l.offset++
	digits := 0
	for l.offset < len(l.data) {
		value := l.data[l.offset]
		if value == '>' {
			l.offset++
			if digits%2 != 0 {
				return fmt.Errorf("%w: odd-length PDF hex string", ErrMalformedMedia)
			}
			return nil
		}
		if !pdfWhitespace(value) {
			if !isHex(value) {
				return fmt.Errorf("%w: invalid PDF hex string", ErrMalformedMedia)
			}
			digits++
		}
		l.offset++
	}
	return fmt.Errorf("%w: unterminated PDF hex string", ErrMalformedMedia)
}

func pdfWhitespace(value byte) bool {
	switch value {
	case 0, '\t', '\n', '\f', '\r', ' ':
		return true
	default:
		return false
	}
}

func pdfDelimiter(value byte) bool {
	switch value {
	case '(', ')', '<', '>', '[', ']', '{', '}', '/', '%':
		return true
	default:
		return false
	}
}

func decodePDFName(data []byte) (string, error) {
	decoded := make([]byte, 0, len(data))
	for index := 0; index < len(data); index++ {
		if data[index] != '#' {
			decoded = append(decoded, data[index])
			continue
		}
		if index+2 >= len(data) || !isHex(data[index+1]) || !isHex(data[index+2]) {
			return "", fmt.Errorf("%w: malformed PDF name escape", ErrMalformedMedia)
		}
		decoded = append(decoded, hexByte(data[index+1], data[index+2]))
		index += 2
	}
	if len(decoded) == 0 || len(decoded) > maxPDFNameBytes || bytes.IndexByte(decoded, 0) >= 0 {
		return "", fmt.Errorf("%w: invalid PDF name", ErrMalformedMedia)
	}
	return string(decoded), nil
}

func isHex(value byte) bool {
	return value >= '0' && value <= '9' ||
		value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}

func hexByte(high, low byte) byte {
	return hexNibble(high)<<4 | hexNibble(low)
}

func hexNibble(value byte) byte {
	switch {
	case value >= '0' && value <= '9':
		return value - '0'
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10
	default:
		return value - 'A' + 10
	}
}

func validPDFNumber(value string) bool {
	if value == "" {
		return false
	}
	index := 0
	if value[0] == '+' || value[0] == '-' {
		index++
	}
	digits := 0
	dot := false
	for ; index < len(value); index++ {
		switch {
		case value[index] >= '0' && value[index] <= '9':
			digits++
		case value[index] == '.' && !dot:
			dot = true
		default:
			return false
		}
	}
	return digits > 0
}

type pdfValueKind uint8

const (
	pdfValueNull pdfValueKind = iota
	pdfValueBool
	pdfValueNumber
	pdfValueName
	pdfValueString
	pdfValueArray
	pdfValueDict
	pdfValueRef
)

type pdfReference struct {
	object     int
	generation int
}

type pdfValue struct {
	kind  pdfValueKind
	raw   string
	name  string
	array []*pdfValue
	dict  map[string]*pdfValue
	ref   pdfReference
}

type pdfValueParser struct {
	tokens []pdfToken
	offset int
	nodes  int
}

func (p *pdfValueParser) parse(depth int) (*pdfValue, error) {
	if depth > maxPDFNesting || p.nodes >= maxPDFTokens {
		return nil, ErrResourceLimit
	}
	if p.offset >= len(p.tokens) {
		return nil, fmt.Errorf("%w: incomplete PDF object value", ErrMalformedMedia)
	}
	p.nodes++
	token := p.tokens[p.offset]
	p.offset++
	switch token.kind {
	case pdfTokenName:
		return &pdfValue{kind: pdfValueName, name: token.value}, nil
	case pdfTokenString:
		return &pdfValue{kind: pdfValueString}, nil
	case pdfTokenNumber:
		if p.offset+1 < len(p.tokens) &&
			p.tokens[p.offset].kind == pdfTokenNumber &&
			p.tokens[p.offset+1].kind == pdfTokenKeyword &&
			p.tokens[p.offset+1].value == "R" {
			object, err := parsePDFUnsignedInteger(token.value, maxPDFObjects-1)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid PDF reference", ErrMalformedMedia)
			}
			generation, err := parsePDFUnsignedInteger(p.tokens[p.offset].value, 65_535)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid PDF reference", ErrMalformedMedia)
			}
			p.offset += 2
			return &pdfValue{
				kind: pdfValueRef,
				ref:  pdfReference{object: object, generation: generation},
			}, nil
		}
		return &pdfValue{kind: pdfValueNumber, raw: token.value}, nil
	case pdfTokenKeyword:
		switch token.value {
		case "null":
			return &pdfValue{kind: pdfValueNull}, nil
		case "true", "false":
			return &pdfValue{kind: pdfValueBool, raw: token.value}, nil
		default:
			return nil, fmt.Errorf("%w: unsupported PDF object keyword", ErrMalformedMedia)
		}
	case pdfTokenArrayStart:
		value := &pdfValue{kind: pdfValueArray}
		for {
			if p.offset >= len(p.tokens) {
				return nil, fmt.Errorf("%w: unterminated PDF array", ErrMalformedMedia)
			}
			if p.tokens[p.offset].kind == pdfTokenArrayEnd {
				p.offset++
				return value, nil
			}
			item, err := p.parse(depth + 1)
			if err != nil {
				return nil, err
			}
			value.array = append(value.array, item)
		}
	case pdfTokenDictStart:
		value := &pdfValue{kind: pdfValueDict, dict: make(map[string]*pdfValue)}
		for {
			if p.offset >= len(p.tokens) {
				return nil, fmt.Errorf("%w: unterminated PDF dictionary", ErrMalformedMedia)
			}
			if p.tokens[p.offset].kind == pdfTokenDictEnd {
				p.offset++
				return value, nil
			}
			key := p.tokens[p.offset]
			p.offset++
			if key.kind != pdfTokenName {
				return nil, fmt.Errorf("%w: PDF dictionary key is not a name", ErrMalformedMedia)
			}
			if _, exists := value.dict[key.value]; exists {
				return nil, fmt.Errorf("%w: duplicate PDF dictionary key", ErrMalformedMedia)
			}
			item, err := p.parse(depth + 1)
			if err != nil {
				return nil, err
			}
			value.dict[key.value] = item
		}
	default:
		return nil, fmt.Errorf("%w: invalid PDF object value", ErrMalformedMedia)
	}
}

func parsePDFValueTokens(tokens []pdfToken) (*pdfValue, error) {
	if err := validatePDFNames(tokens); err != nil {
		return nil, err
	}
	parser := &pdfValueParser{tokens: tokens}
	value, err := parser.parse(0)
	if err != nil {
		return nil, err
	}
	if parser.offset != len(tokens) {
		return nil, fmt.Errorf("%w: multiple PDF object values", ErrMalformedMedia)
	}
	return value, nil
}

var unsupportedPDFNames = map[string]struct{}{
	"Encrypt": {}, "Crypt": {}, "Prev": {}, "XRefStm": {}, "XRef": {}, "ObjStm": {},
	"JavaScript": {}, "JS": {}, "OpenAction": {}, "AA": {}, "A": {}, "Action": {},
	"Launch": {}, "URI": {}, "SubmitForm": {}, "ResetForm": {}, "ImportData": {},
	"GoToR": {}, "GoToE": {}, "Rendition": {}, "RichMedia": {}, "Movie": {}, "Sound": {},
	"EmbeddedFile": {}, "EmbeddedFiles": {}, "FileAttachment": {}, "Filespec": {},
	"EF": {}, "AF": {}, "Collection": {}, "AcroForm": {}, "XFA": {}, "Annots": {},
	"Names": {}, "Outlines": {}, "Threads": {},
}

func validatePDFNames(tokens []pdfToken) error {
	for _, token := range tokens {
		if token.kind != pdfTokenName {
			continue
		}
		if _, unsupported := unsupportedPDFNames[token.value]; unsupported {
			return fmt.Errorf(
				"%w: PDF contains unsupported active, embedded, encrypted, form, or compressed-structure content",
				ErrUnsupportedMedia,
			)
		}
	}
	return nil
}

type pdfXRefEntry struct {
	offset     int
	generation int
	inUse      bool
}

func parsePDFFinalTrailer(data []byte) (int64, int, error) {
	eof := bytes.LastIndex(data, []byte("%%EOF"))
	if eof < 0 || len(data)-(eof+len("%%EOF")) > 1024 ||
		!onlyPDFWhitespace(data[eof+len("%%EOF"):]) {
		return 0, 0, errors.New("invalid PDF EOF")
	}
	start := bytes.LastIndex(data[:eof], []byte("startxref"))
	if start < 0 || start > 0 && !pdfWhitespace(data[start-1]) {
		return 0, 0, errors.New("missing PDF startxref")
	}
	position := start + len("startxref")
	if position >= eof || !pdfWhitespace(data[position]) {
		return 0, 0, errors.New("invalid PDF startxref")
	}
	for position < eof && pdfWhitespace(data[position]) {
		position++
	}
	numberStart := position
	for position < eof && data[position] >= '0' && data[position] <= '9' {
		position++
	}
	if numberStart == position || position-numberStart > 20 ||
		!onlyPDFWhitespace(data[position:eof]) {
		return 0, 0, errors.New("invalid PDF startxref")
	}
	offset, err := strconv.ParseInt(string(data[numberStart:position]), 10, 64)
	if err != nil {
		return 0, 0, err
	}
	return offset, start, nil
}

func parsePDFClassicXRef(
	data []byte,
	xrefOffset int,
	startMarker int,
) (map[int]pdfXRefEntry, *pdfValue, error) {
	line, position, ok := readPDFLine(data, xrefOffset)
	if !ok || string(bytes.TrimSpace(line)) != "xref" {
		return nil, nil, fmt.Errorf("%w: PDF requires a classic cross-reference table", ErrUnsupportedMedia)
	}
	entries := make(map[int]pdfXRefEntry)
	for {
		line, next, ok := readPDFLine(data, position)
		if !ok {
			return nil, nil, fmt.Errorf("%w: incomplete PDF cross-reference", ErrMalformedMedia)
		}
		position = next
		if string(bytes.TrimSpace(line)) == "trailer" {
			break
		}
		fields := strings.Fields(string(line))
		if len(fields) != 2 {
			return nil, nil, fmt.Errorf("%w: invalid PDF cross-reference subsection", ErrMalformedMedia)
		}
		first, err := parsePDFUnsignedInteger(fields[0], maxPDFObjects-1)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid PDF cross-reference subsection", ErrMalformedMedia)
		}
		count, err := parsePDFUnsignedInteger(fields[1], maxPDFObjects)
		if err != nil || count == 0 || first > maxPDFObjects-count {
			return nil, nil, fmt.Errorf("%w: invalid PDF cross-reference subsection", ErrMalformedMedia)
		}
		for index := 0; index < count; index++ {
			line, next, ok = readPDFLine(data, position)
			if !ok {
				return nil, nil, fmt.Errorf("%w: incomplete PDF cross-reference", ErrMalformedMedia)
			}
			position = next
			entry, err := parsePDFXRefEntry(line)
			if err != nil {
				return nil, nil, err
			}
			object := first + index
			if _, duplicate := entries[object]; duplicate {
				return nil, nil, fmt.Errorf("%w: duplicate PDF cross-reference entry", ErrMalformedMedia)
			}
			entries[object] = entry
		}
	}
	if len(entries) == 0 || len(entries) > maxPDFObjects {
		return nil, nil, ErrResourceLimit
	}
	trailerTokens, err := lexPDFSegment(data[position:startMarker])
	if err != nil {
		return nil, nil, err
	}
	trailer, err := parsePDFValueTokens(trailerTokens)
	if err != nil {
		return nil, nil, err
	}
	if trailer.kind != pdfValueDict {
		return nil, nil, fmt.Errorf("%w: invalid PDF trailer dictionary", ErrMalformedMedia)
	}
	size, ok := pdfInteger(trailer.dict["Size"])
	if !ok || size < 1 || size > maxPDFObjects || len(entries) != size {
		return nil, nil, fmt.Errorf("%w: incomplete PDF cross-reference", ErrMalformedMedia)
	}
	for object := 0; object < size; object++ {
		if _, exists := entries[object]; !exists {
			return nil, nil, fmt.Errorf("%w: incomplete PDF cross-reference", ErrMalformedMedia)
		}
	}
	zero := entries[0]
	if zero.inUse || zero.offset != 0 || zero.generation != 65_535 {
		return nil, nil, fmt.Errorf("%w: invalid PDF free-object head", ErrMalformedMedia)
	}
	return entries, trailer, nil
}

func parsePDFXRefEntry(line []byte) (pdfXRefEntry, error) {
	if len(line) == 19 && line[18] == ' ' {
		line = line[:18]
	}
	if len(line) != 18 || line[10] != ' ' || line[16] != ' ' ||
		(line[17] != 'n' && line[17] != 'f') {
		return pdfXRefEntry{}, fmt.Errorf("%w: invalid PDF cross-reference entry", ErrMalformedMedia)
	}
	offset, err := parseFixedPDFDecimal(line[:10])
	if err != nil {
		return pdfXRefEntry{}, fmt.Errorf("%w: invalid PDF cross-reference offset", ErrMalformedMedia)
	}
	generation, err := parseFixedPDFDecimal(line[11:16])
	if err != nil || generation > 65_535 {
		return pdfXRefEntry{}, fmt.Errorf("%w: invalid PDF cross-reference generation", ErrMalformedMedia)
	}
	return pdfXRefEntry{
		offset: offset, generation: generation, inUse: line[17] == 'n',
	}, nil
}

func parseFixedPDFDecimal(data []byte) (int, error) {
	for _, value := range data {
		if value < '0' || value > '9' {
			return 0, errors.New("not decimal")
		}
	}
	value, err := strconv.ParseUint(string(data), 10, 31)
	return int(value), err
}

func parsePDFUnsignedInteger(value string, maximum int) (int, error) {
	if value == "" {
		return 0, errors.New("empty integer")
	}
	for _, digit := range []byte(value) {
		if digit < '0' || digit > '9' {
			return 0, errors.New("not an unsigned integer")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 31)
	if err != nil || parsed > uint64(maximum) {
		return 0, errors.New("integer outside bound")
	}
	return int(parsed), nil
}

func parsePDFObjects(
	data []byte,
	headerEnd int,
	xrefOffset int,
	entries map[int]pdfXRefEntry,
) (map[int]*pdfValue, error) {
	type locatedObject struct {
		object int
		entry  pdfXRefEntry
	}
	located := make([]locatedObject, 0, len(entries)-1)
	seenOffsets := make(map[int]struct{})
	for object, entry := range entries {
		if !entry.inUse {
			continue
		}
		if object == 0 || entry.generation != 0 ||
			entry.offset < headerEnd || entry.offset >= xrefOffset {
			return nil, fmt.Errorf("%w: invalid PDF object cross-reference", ErrMalformedMedia)
		}
		if _, duplicate := seenOffsets[entry.offset]; duplicate {
			return nil, fmt.Errorf("%w: duplicate PDF object offset", ErrMalformedMedia)
		}
		seenOffsets[entry.offset] = struct{}{}
		located = append(located, locatedObject{object: object, entry: entry})
	}
	if len(located) == 0 {
		return nil, fmt.Errorf("%w: PDF has no indirect objects", ErrMalformedMedia)
	}
	sort.Slice(located, func(i, j int) bool {
		return located[i].entry.offset < located[j].entry.offset
	})
	if err := requirePDFGap(data[headerEnd:located[0].entry.offset]); err != nil {
		return nil, err
	}
	objects := make(map[int]*pdfValue, len(located))
	for index, item := range located {
		end := xrefOffset
		if index+1 < len(located) {
			end = located[index+1].entry.offset
		}
		value, consumed, err := parsePDFIndirectObject(
			data[item.entry.offset:end],
			item.object,
			item.entry.generation,
		)
		if err != nil {
			return nil, err
		}
		if err := requirePDFGap(data[item.entry.offset+consumed : end]); err != nil {
			return nil, err
		}
		objects[item.object] = value
	}
	return objects, nil
}

func parsePDFIndirectObject(
	data []byte,
	expectedObject int,
	expectedGeneration int,
) (*pdfValue, int, error) {
	lexer := &pdfLexer{data: data}
	objectToken, err := lexer.next()
	if err != nil || objectToken.kind != pdfTokenNumber || objectToken.start != 0 {
		return nil, 0, fmt.Errorf("%w: cross-reference does not point to an object", ErrMalformedMedia)
	}
	generationToken, err := lexer.next()
	if err != nil || generationToken.kind != pdfTokenNumber {
		return nil, 0, fmt.Errorf("%w: invalid PDF object header", ErrMalformedMedia)
	}
	objToken, err := lexer.next()
	if err != nil || objToken.kind != pdfTokenKeyword || objToken.value != "obj" {
		return nil, 0, fmt.Errorf("%w: invalid PDF object header", ErrMalformedMedia)
	}
	object, err := parsePDFUnsignedInteger(objectToken.value, maxPDFObjects-1)
	if err != nil || object != expectedObject {
		return nil, 0, fmt.Errorf("%w: PDF object number does not match cross-reference", ErrMalformedMedia)
	}
	generation, err := parsePDFUnsignedInteger(generationToken.value, 65_535)
	if err != nil || generation != expectedGeneration {
		return nil, 0, fmt.Errorf("%w: PDF object generation does not match cross-reference", ErrMalformedMedia)
	}

	tokens := make([]pdfToken, 0, 32)
	var terminal pdfToken
	for {
		token, err := lexer.next()
		if err != nil {
			return nil, 0, err
		}
		if token.kind == pdfTokenEOF {
			return nil, 0, fmt.Errorf("%w: unterminated PDF object", ErrMalformedMedia)
		}
		if token.kind == pdfTokenKeyword &&
			(token.value == "stream" || token.value == "endobj") {
			terminal = token
			break
		}
		tokens = append(tokens, token)
	}
	value, err := parsePDFValueTokens(tokens)
	if err != nil {
		return nil, 0, err
	}
	if terminal.value == "endobj" {
		return value, terminal.end, nil
	}
	if value.kind != pdfValueDict {
		return nil, 0, fmt.Errorf("%w: PDF stream object is not a dictionary", ErrMalformedMedia)
	}
	length, ok := pdfIntegerBounded(value.dict["Length"], len(data))
	if !ok || length < 0 {
		return nil, 0, fmt.Errorf("%w: PDF stream requires a direct bounded length", ErrUnsupportedMedia)
	}
	streamStart, ok := consumePDFEOL(data, terminal.end)
	if !ok || length > len(data)-streamStart {
		return nil, 0, fmt.Errorf("%w: invalid PDF stream length", ErrMalformedMedia)
	}
	streamEnd := streamStart + length
	endstreamStart, ok := consumePDFEOL(data, streamEnd)
	if !ok || !bytes.HasPrefix(data[endstreamStart:], []byte("endstream")) {
		return nil, 0, fmt.Errorf("%w: invalid PDF stream boundary", ErrMalformedMedia)
	}
	endstreamEnd := endstreamStart + len("endstream")
	if endstreamEnd < len(data) && !pdfWhitespace(data[endstreamEnd]) {
		return nil, 0, fmt.Errorf("%w: invalid PDF endstream boundary", ErrMalformedMedia)
	}
	lexer = &pdfLexer{data: data, offset: endstreamEnd}
	endobj, err := lexer.next()
	if err != nil || endobj.kind != pdfTokenKeyword || endobj.value != "endobj" {
		return nil, 0, fmt.Errorf("%w: PDF stream lacks endobj", ErrMalformedMedia)
	}
	return value, endobj.end, nil
}

func validatePDFReferences(
	objects map[int]*pdfValue,
	trailer *pdfValue,
	entries map[int]pdfXRefEntry,
) error {
	validate := func(reference pdfReference) error {
		entry, exists := entries[reference.object]
		if !exists || !entry.inUse || entry.generation != reference.generation {
			return fmt.Errorf("%w: dangling PDF object reference", ErrMalformedMedia)
		}
		if _, exists := objects[reference.object]; !exists {
			return fmt.Errorf("%w: missing PDF object", ErrMalformedMedia)
		}
		return nil
	}
	var walk func(*pdfValue, int) error
	walk = func(value *pdfValue, depth int) error {
		if value == nil {
			return nil
		}
		if depth > maxPDFNesting {
			return ErrResourceLimit
		}
		switch value.kind {
		case pdfValueRef:
			return validate(value.ref)
		case pdfValueArray:
			for _, item := range value.array {
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
		case pdfValueDict:
			for _, item := range value.dict {
				if err := walk(item, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(trailer, 0); err != nil {
		return err
	}
	for _, value := range objects {
		if err := walk(value, 0); err != nil {
			return err
		}
	}
	return nil
}

func validatePDFPageTree(
	objects map[int]*pdfValue,
	trailer *pdfValue,
	limits Limits,
) error {
	root, ok := pdfReferenceValue(trailer.dict["Root"])
	if !ok {
		return fmt.Errorf("%w: PDF trailer has no catalog root", ErrMalformedMedia)
	}
	catalog := objects[root.object]
	if !pdfDictionaryType(catalog, "Catalog") {
		return fmt.Errorf("%w: PDF root is not a catalog", ErrMalformedMedia)
	}
	pagesRoot, ok := pdfReferenceValue(catalog.dict["Pages"])
	if !ok {
		return fmt.Errorf("%w: PDF catalog has no page tree", ErrMalformedMedia)
	}

	visited := make(map[int]struct{})
	pageObjects := make(map[int]struct{})
	var walk func(pdfReference, *pdfReference, int) (int, error)
	walk = func(reference pdfReference, parent *pdfReference, depth int) (int, error) {
		if depth > maxPDFNesting {
			return 0, ErrResourceLimit
		}
		if _, duplicate := visited[reference.object]; duplicate {
			return 0, fmt.Errorf("%w: cyclic or repeated PDF page-tree object", ErrMalformedMedia)
		}
		visited[reference.object] = struct{}{}
		value := objects[reference.object]
		if value == nil || value.kind != pdfValueDict {
			return 0, fmt.Errorf("%w: invalid PDF page-tree object", ErrMalformedMedia)
		}
		objectParent, hasParent := pdfReferenceValue(value.dict["Parent"])
		if parent == nil {
			if hasParent {
				return 0, fmt.Errorf("%w: root PDF pages object has a parent", ErrMalformedMedia)
			}
		} else if !hasParent || objectParent != *parent {
			return 0, fmt.Errorf("%w: PDF page parent does not match page tree", ErrMalformedMedia)
		}

		switch pdfNameValue(value.dict["Type"]) {
		case "Page":
			pageObjects[reference.object] = struct{}{}
			if len(pageObjects) > limits.MaxPDFPages {
				return 0, ErrResourceLimit
			}
			return 1, nil
		case "Pages":
			kids := value.dict["Kids"]
			if kids == nil || kids.kind != pdfValueArray || len(kids.array) == 0 {
				return 0, fmt.Errorf("%w: PDF pages node has no children", ErrMalformedMedia)
			}
			total := 0
			for _, child := range kids.array {
				childRef, ok := pdfReferenceValue(child)
				if !ok {
					return 0, fmt.Errorf("%w: PDF page-tree child is not a reference", ErrMalformedMedia)
				}
				count, err := walk(childRef, &reference, depth+1)
				if err != nil {
					return 0, err
				}
				total += count
				if total > limits.MaxPDFPages {
					return 0, ErrResourceLimit
				}
			}
			declared, ok := pdfInteger(value.dict["Count"])
			if !ok || declared != total {
				return 0, fmt.Errorf("%w: PDF page count does not match page tree", ErrMalformedMedia)
			}
			return total, nil
		default:
			return 0, fmt.Errorf("%w: invalid PDF page-tree type", ErrMalformedMedia)
		}
	}
	total, err := walk(pagesRoot, nil, 0)
	if err != nil {
		return err
	}
	if total == 0 {
		return fmt.Errorf("%w: PDF has no pages", ErrMalformedMedia)
	}
	for object, value := range objects {
		switch pdfNameValue(value.dictValue("Type")) {
		case "Catalog":
			if object != root.object {
				return fmt.Errorf("%w: multiple PDF catalogs", ErrMalformedMedia)
			}
		case "Page":
			if _, reached := pageObjects[object]; !reached {
				return fmt.Errorf("%w: unreachable PDF page object", ErrMalformedMedia)
			}
		case "Pages":
			if _, reached := visited[object]; !reached {
				return fmt.Errorf("%w: unreachable PDF pages object", ErrMalformedMedia)
			}
		}
	}
	return nil
}

func (v *pdfValue) dictValue(key string) *pdfValue {
	if v == nil || v.kind != pdfValueDict {
		return nil
	}
	return v.dict[key]
}

func pdfDictionaryType(value *pdfValue, expected string) bool {
	return value != nil && value.kind == pdfValueDict &&
		pdfNameValue(value.dict["Type"]) == expected
}

func pdfNameValue(value *pdfValue) string {
	if value == nil || value.kind != pdfValueName {
		return ""
	}
	return value.name
}

func pdfReferenceValue(value *pdfValue) (pdfReference, bool) {
	if value == nil || value.kind != pdfValueRef {
		return pdfReference{}, false
	}
	return value.ref, true
}

func pdfInteger(value *pdfValue) (int, bool) {
	return pdfIntegerBounded(value, maxPDFObjects)
}

func pdfIntegerBounded(value *pdfValue, maximum int) (int, bool) {
	if value == nil || value.kind != pdfValueNumber {
		return 0, false
	}
	parsed, err := parsePDFUnsignedInteger(value.raw, maximum)
	return parsed, err == nil
}

func lexPDFSegment(data []byte) ([]pdfToken, error) {
	lexer := &pdfLexer{data: data}
	var tokens []pdfToken
	for {
		token, err := lexer.next()
		if err != nil {
			return nil, err
		}
		if token.kind == pdfTokenEOF {
			return tokens, nil
		}
		tokens = append(tokens, token)
	}
}

func requirePDFGap(data []byte) error {
	tokens, err := lexPDFSegment(data)
	if err != nil {
		return err
	}
	if len(tokens) != 0 {
		return fmt.Errorf("%w: unreferenced data between PDF objects", ErrMalformedMedia)
	}
	return nil
}

func pdfHeaderEnd(data []byte) (int, error) {
	if !validPDFHeader(data) || len(data) == len("%PDF-1.0") {
		return 0, fmt.Errorf("%w: invalid PDF header", ErrMalformedMedia)
	}
	position, ok := consumePDFEOL(data, len("%PDF-1.0"))
	if !ok {
		return 0, fmt.Errorf("%w: invalid PDF header line", ErrMalformedMedia)
	}
	return position, nil
}

func consumePDFEOL(data []byte, position int) (int, bool) {
	if position >= len(data) {
		return position, false
	}
	switch data[position] {
	case '\n':
		return position + 1, true
	case '\r':
		position++
		if position < len(data) && data[position] == '\n' {
			position++
		}
		return position, true
	default:
		return position, false
	}
}

func readPDFLine(data []byte, position int) ([]byte, int, bool) {
	if position < 0 || position >= len(data) {
		return nil, position, false
	}
	start := position
	for position < len(data) && data[position] != '\r' && data[position] != '\n' {
		position++
	}
	if position == len(data) {
		return nil, position, false
	}
	line := data[start:position]
	next, ok := consumePDFEOL(data, position)
	return line, next, ok
}

func onlyPDFWhitespace(data []byte) bool {
	for _, value := range data {
		if !pdfWhitespace(value) {
			return false
		}
	}
	return true
}
