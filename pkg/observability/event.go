// Package observability consumes privacy-filtered copies of semantic events.
// Sink state never determines semantic success, permissions, persistence, or
// process exit status.
package observability

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	CurrentEventVersion  = 1
	DefaultMaxAttributes = 64
	DefaultMaxValueRunes = 512
)

type Destination string

const (
	DestinationLocal     Destination = "local"
	DestinationAnalytics Destination = "analytics"
	DestinationEssential Destination = "essential"
)

type TrafficClass string

const (
	TrafficOptional  TrafficClass = "optional"
	TrafficEssential TrafficClass = "essential"
)

type PrivacyClass string

const (
	PrivacyPublic      PrivacyClass = "public"
	PrivacyOperational PrivacyClass = "operational"
	PrivacySensitive   PrivacyClass = "sensitive"
	PrivacySecret      PrivacyClass = "secret"
)

type Cardinality string

const (
	CardinalityLow     Cardinality = "low"
	CardinalityBounded Cardinality = "bounded"
	CardinalityHigh    Cardinality = "high"
)

type AttributeType string

const (
	AttributeString  AttributeType = "string"
	AttributeInteger AttributeType = "integer"
	AttributeFloat   AttributeType = "float"
	AttributeBoolean AttributeType = "boolean"
)

// Attribute declares privacy, cardinality, and destination eligibility with
// its value. Zero is an explicit value for numeric and Boolean types.
type Attribute struct {
	Type         AttributeType `json:"type"`
	String       string        `json:"string,omitempty"`
	Integer      int64         `json:"integer,omitempty"`
	Float        float64       `json:"float,omitempty"`
	Boolean      bool          `json:"boolean,omitempty"`
	Privacy      PrivacyClass  `json:"privacy"`
	Cardinality  Cardinality   `json:"cardinality"`
	Destinations []Destination `json:"destinations,omitempty"`
}

func StringAttribute(value string, privacy PrivacyClass, cardinality Cardinality, destinations ...Destination) Attribute {
	return Attribute{Type: AttributeString, String: value, Privacy: privacy, Cardinality: cardinality, Destinations: append([]Destination(nil), destinations...)}
}

func IntegerAttribute(value int64, privacy PrivacyClass, cardinality Cardinality, destinations ...Destination) Attribute {
	return Attribute{Type: AttributeInteger, Integer: value, Privacy: privacy, Cardinality: cardinality, Destinations: append([]Destination(nil), destinations...)}
}

func BooleanAttribute(value bool, privacy PrivacyClass, cardinality Cardinality, destinations ...Destination) Attribute {
	return Attribute{Type: AttributeBoolean, Boolean: value, Privacy: privacy, Cardinality: cardinality, Destinations: append([]Destination(nil), destinations...)}
}

// Event is a canonical observation request. It must contain measurements and
// bounded classifications, never raw prompts, tool payloads, or file content.
type Event struct {
	Version      int                  `json:"version"`
	ID           string               `json:"id"`
	Name         string               `json:"name"`
	Timestamp    time.Time            `json:"timestamp"`
	Traffic      TrafficClass         `json:"traffic"`
	Source       string               `json:"source"`
	Profile      string               `json:"profile,omitempty"`
	Destinations []Destination        `json:"destinations"`
	Attributes   map[string]Attribute `json:"attributes,omitempty"`
}

// Record is the only shape an exporter receives. Privacy metadata and rejected
// fields have already been consumed by admission.
type Record struct {
	Version    int            `json:"version"`
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Timestamp  time.Time      `json:"timestamp"`
	Source     string         `json:"source"`
	Profile    string         `json:"profile,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

type Policy struct {
	OptionalEnabled     bool
	EssentialEnabled    bool
	ManagedAllowed      bool
	AllowSensitiveLocal bool
	MaxAttributes       int
	MaxValueRunes       int
}

func (p Policy) normalized() Policy {
	if p.MaxAttributes <= 0 {
		p.MaxAttributes = DefaultMaxAttributes
	}
	if p.MaxValueRunes <= 0 {
		p.MaxValueRunes = DefaultMaxValueRunes
	}
	return p
}

// EnabledFor lets a producer avoid constructing an optional sensitive event
// that policy will discard.
func (p Policy) EnabledFor(traffic TrafficClass, destination Destination) bool {
	if !p.ManagedAllowed {
		return false
	}
	if traffic == TrafficEssential {
		return p.EssentialEnabled && destination == DestinationEssential
	}
	return p.OptionalEnabled && destination != DestinationEssential
}

type AdmissionStatus string

const (
	AdmissionAccepted AdmissionStatus = "accepted"
	AdmissionFiltered AdmissionStatus = "filtered"
	AdmissionInvalid  AdmissionStatus = "invalid"
)

type Admission struct {
	Status         AdmissionStatus `json:"status"`
	Reason         string          `json:"reason,omitempty"`
	FilteredFields int             `json:"filtered_fields"`
}

var (
	eventNamePattern    = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)
	attributeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)
	bearerPattern       = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]+`)
	assignmentPattern   = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|token|password|secret|subscription[_-]?key|authorization|cookie)\s*[:=]\s*("[^"]*"|'[^']*'|[^\s,;]+)`)
	userInfoPattern     = regexp.MustCompile(`(://)[^/@\s:]+:[^/@\s]+@`)
	unixHomePathPattern = regexp.MustCompile(`(?:/Users|/home)/[^/\s:,;]+(?:/[^\s,;]*)?`)
	windowsPathPattern  = regexp.MustCompile(`[A-Za-z]:\\(?:[^\s,;]+\\?)+`)
)

// Admit validates and privacy-filters an event for one destination. Invalid or
// filtered observations never become errors in the semantic caller.
func Admit(event Event, destination Destination, policy Policy) (Record, Admission) {
	policy = policy.normalized()
	if !policy.EnabledFor(event.Traffic, destination) {
		return Record{}, Admission{Status: AdmissionFiltered, Reason: "traffic disabled by privacy or managed policy"}
	}
	if event.Version != CurrentEventVersion || !eventNamePattern.MatchString(event.Name) || event.ID == "" || event.Timestamp.IsZero() {
		return Record{}, Admission{Status: AdmissionInvalid, Reason: "invalid event envelope"}
	}
	if !containsDestination(event.Destinations, destination) {
		return Record{}, Admission{Status: AdmissionFiltered, Reason: "destination is not eligible"}
	}
	if len(event.Attributes) > policy.MaxAttributes {
		return Record{}, Admission{Status: AdmissionInvalid, Reason: "attribute count exceeds bound"}
	}
	record := Record{
		Version: event.Version, ID: truncateRunes(RedactText(event.ID), 128), Name: event.Name,
		Timestamp: event.Timestamp.UTC(), Source: truncateRunes(RedactText(event.Source), 64),
		Profile: truncateRunes(RedactText(event.Profile), 64), Attributes: make(map[string]any),
	}
	keys := make([]string, 0, len(event.Attributes))
	for key := range event.Attributes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	filtered := 0
	for _, key := range keys {
		attribute := event.Attributes[key]
		if !attributeKeyPattern.MatchString(key) || !attributeAllowed(attribute, destination, policy) {
			filtered++
			continue
		}
		value, ok := sanitizeAttribute(attribute, policy.MaxValueRunes)
		if !ok {
			filtered++
			continue
		}
		record.Attributes[key] = value
	}
	if len(record.Attributes) == 0 {
		record.Attributes = nil
	}
	return record, Admission{Status: AdmissionAccepted, FilteredFields: filtered}
}

func attributeAllowed(attribute Attribute, destination Destination, policy Policy) bool {
	if len(attribute.Destinations) > 0 && !containsDestination(attribute.Destinations, destination) {
		return false
	}
	if attribute.Privacy == PrivacySecret {
		return false
	}
	if attribute.Privacy == PrivacySensitive && !(destination == DestinationLocal && policy.AllowSensitiveLocal) {
		return false
	}
	if attribute.Cardinality == CardinalityHigh && destination != DestinationLocal {
		return false
	}
	return attribute.Privacy == PrivacyPublic || attribute.Privacy == PrivacyOperational || attribute.Privacy == PrivacySensitive
}

func sanitizeAttribute(attribute Attribute, limit int) (any, bool) {
	switch attribute.Type {
	case AttributeString:
		return truncateRunes(RedactText(attribute.String), limit), true
	case AttributeInteger:
		return attribute.Integer, true
	case AttributeFloat:
		if math.IsNaN(attribute.Float) || math.IsInf(attribute.Float, 0) {
			return nil, false
		}
		return attribute.Float, true
	case AttributeBoolean:
		return attribute.Boolean, true
	default:
		return nil, false
	}
}

func containsDestination(values []Destination, wanted Destination) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// RedactText removes common credential and exact-home-path forms before
// truncation. It complements, rather than replaces, privacy classification.
func RedactText(value string) string {
	if !utf8.ValidString(value) {
		value = strings.ToValidUTF8(value, "�")
	}
	upper := strings.ToUpper(value)
	if strings.Contains(upper, "-----BEGIN PRIVATE KEY-----") || strings.Contains(upper, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		return "[REDACTED PRIVATE KEY MATERIAL]"
	}
	value = bearerPattern.ReplaceAllString(value, "Bearer [REDACTED]")
	value = assignmentPattern.ReplaceAllString(value, "$1=[REDACTED]")
	value = userInfoPattern.ReplaceAllString(value, "$1[REDACTED]@")
	value = unixHomePathPattern.ReplaceAllString(value, "<path>")
	value = windowsPathPattern.ReplaceAllString(value, "<path>")
	return value
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

func cloneRecord(record Record) Record {
	result := record
	if record.Attributes != nil {
		result.Attributes = make(map[string]any, len(record.Attributes))
		for key, value := range record.Attributes {
			result.Attributes[key] = value
		}
	}
	return result
}

func cloneRecords(records []Record) []Record {
	result := make([]Record, len(records))
	for index, record := range records {
		result[index] = cloneRecord(record)
	}
	return result
}

// MarshalRecord verifies that a sink payload remains ordinary bounded JSON.
func MarshalRecord(record Record) ([]byte, error) { return json.Marshal(record) }
