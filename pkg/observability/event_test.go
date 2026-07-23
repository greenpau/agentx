package observability

import (
	"strings"
	"testing"
	"time"
)

func validEvent() Event {
	return Event{
		Version: CurrentEventVersion, ID: "event-1", Name: "tool.completed", Timestamp: time.Now(),
		Traffic: TrafficOptional, Source: "runtime", Destinations: []Destination{DestinationAnalytics, DestinationLocal},
		Attributes: map[string]Attribute{
			"status": StringAttribute("success", PrivacyPublic, CardinalityLow),
		},
	}
}

func TestAdmissionFiltersPrivacyAndCardinalityBeforeExport(t *testing.T) {
	event := validEvent()
	event.Attributes["secret"] = StringAttribute("AZURE_OPENAI_SUBSCRIPTION_KEY=abcdef", PrivacySecret, CardinalityLow)
	event.Attributes["email"] = StringAttribute("person@example.test", PrivacySensitive, CardinalityBounded)
	event.Attributes["exact_path"] = StringAttribute("failed at /Users/alice/private/repo: api_key=hunter2", PrivacyOperational, CardinalityBounded)
	event.Attributes["request_body"] = StringAttribute("arbitrary", PrivacyOperational, CardinalityHigh)
	policy := Policy{OptionalEnabled: true, ManagedAllowed: true}
	record, admission := Admit(event, DestinationAnalytics, policy)
	if admission.Status != AdmissionAccepted || admission.FilteredFields != 3 {
		t.Fatalf("admission = %+v", admission)
	}
	if _, exists := record.Attributes["secret"]; exists {
		t.Fatal("secret attribute survived admission")
	}
	if _, exists := record.Attributes["email"]; exists {
		t.Fatal("sensitive attribute survived analytics admission")
	}
	if _, exists := record.Attributes["request_body"]; exists {
		t.Fatal("high-cardinality attribute survived analytics admission")
	}
	diagnostic := record.Attributes["exact_path"].(string)
	if strings.Contains(diagnostic, "alice") || strings.Contains(diagnostic, "hunter2") || !strings.Contains(diagnostic, "[REDACTED]") {
		t.Fatalf("diagnostic was not redacted: %q", diagnostic)
	}

	record, admission = Admit(event, DestinationLocal, Policy{OptionalEnabled: true, ManagedAllowed: true, AllowSensitiveLocal: true})
	if admission.Status != AdmissionAccepted {
		t.Fatal(admission)
	}
	if _, exists := record.Attributes["email"]; !exists {
		t.Fatal("explicitly allowed local sensitive field was removed")
	}
	if _, exists := record.Attributes["secret"]; exists {
		t.Fatal("secret field survived local admission")
	}
}

func TestPolicyOptOutAndEnvelopeValidation(t *testing.T) {
	event := validEvent()
	if _, admission := Admit(event, DestinationAnalytics, Policy{ManagedAllowed: true}); admission.Status != AdmissionFiltered {
		t.Fatalf("opt-out admission = %+v", admission)
	}
	event.ID = ""
	if _, admission := Admit(event, DestinationAnalytics, Policy{OptionalEnabled: true, ManagedAllowed: true}); admission.Status != AdmissionInvalid {
		t.Fatalf("invalid envelope admission = %+v", admission)
	}
	if got := RedactText("-----BEGIN PRIVATE KEY-----\nsecret"); got != "[REDACTED PRIVATE KEY MATERIAL]" {
		t.Fatalf("private key redaction = %q", got)
	}
}
