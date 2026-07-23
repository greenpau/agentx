package redact

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestJSONSanitizesSemanticEscapeAliases(t *testing.T) {
	for _, test := range []struct {
		name    string
		secret  string
		wire    string
		wantKey string
	}{
		{name: "solidus", secret: "a/b", wire: `{"value":"a\/b"}`, wantKey: "value"},
		{name: "unicode", secret: "secret", wire: `{"value":"\u0073ecret"}`, wantKey: "value"},
		{name: "key", secret: "secret", wire: `{"\u0073ecret":"value"}`, wantKey: conventionalMask},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := New(test.secret)
			safe, err := set.JSON([]byte(test.wire))
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(safe, &value); err != nil {
				t.Fatal(err)
			}
			encoded := fmt.Sprint(value)
			if strings.Contains(encoded, test.secret) {
				t.Fatalf("semantic value retained %q in %s", test.secret, safe)
			}
			if _, ok := value[test.wantKey]; !ok {
				t.Fatalf("sanitized object keys = %#v, want %q", value, test.wantKey)
			}
		})
	}
}

func TestJSONContainsInspectsDecodedAliasesAndScalars(t *testing.T) {
	tests := []struct {
		secret string
		wire   string
	}{
		{secret: "secret", wire: `{"value":"\u0073ecret"}`},
		{secret: "a/b", wire: `{"a\/b":"value"}`},
		{secret: "1", wire: `{"value":1}`},
		{secret: "true", wire: `{"value":true}`},
		{secret: "null", wire: `{"value":null}`},
	}
	for _, test := range tests {
		matched, err := New(test.secret).JSONContains([]byte(test.wire))
		if err != nil {
			t.Fatal(err)
		}
		if !matched {
			t.Fatalf("credential %q was not found in %s", test.secret, test.wire)
		}
	}
	matched, err := New("secret").JSONContains([]byte(`{"value":"safe"}`))
	if err != nil || matched {
		t.Fatalf("safe JSON inspection = %t, %v", matched, err)
	}
}

func TestJSONRejectsDuplicateMembersBeforeLastWriteWins(t *testing.T) {
	for _, wire := range []string{
		`{"value":"\u0073ecret","value":"safe"}`,
		`{"\u0076alue":"earlier","value":"safe"}`,
	} {
		raw := []byte(wire)
		set := New("secret")
		if matched, err := set.JSONContains(raw); err == nil {
			t.Fatalf("duplicate-member inspection = %t, nil for %s; want fail closed", matched, wire)
		}
		if safe, err := set.JSON(raw); err == nil || safe != nil {
			t.Fatalf("duplicate-member sanitization = %s, %v for %s; want fail closed", safe, err, wire)
		}
		if safe, err := set.JSONBounded(raw, 1024); err == nil || safe != nil {
			t.Fatalf("bounded duplicate-member sanitization = %s, %v for %s; want fail closed", safe, err, wire)
		}
	}
}

func TestJSONContainsInspectsExactPhysicalLineFrame(t *testing.T) {
	body := []byte(`{"value":"safe"}`)
	secret := "safe\"}\n"
	set := New(secret)
	matched, err := set.JSONContains(body)
	if err != nil {
		t.Fatal(err)
	}
	if matched {
		t.Fatalf("unframed body unexpectedly contained %q", secret)
	}
	frame := append(append([]byte(nil), body...), '\n')
	matched, err = set.JSONContains(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatalf("physical JSONL frame did not detect %q in %q", secret, frame)
	}
}

func TestJSONFailsClosedForReflectedScalarSpellings(t *testing.T) {
	for _, test := range []struct {
		secret string
		wire   string
	}{
		{secret: "1", wire: `{"value":1}`},
		{secret: "true", wire: `{"value":true}`},
		{secret: "false", wire: `{"value":false}`},
		{secret: "null", wire: `{"value":null}`},
	} {
		if safe, err := New(test.secret).JSON([]byte(test.wire)); err == nil || safe != nil {
			t.Fatalf("secret %q scalar sanitization = %s, %v; want fail closed", test.secret, safe, err)
		}
	}
}

func TestJSONBoundedPreventsSemanticRedactionAmplification(t *testing.T) {
	set := New("q")
	wire := []byte(`{"values":["q","q","q"]}`)
	if safe, err := set.JSONBounded(wire, 24); err == nil || safe != nil {
		t.Fatalf("amplified bounded JSON = %s, %v; want fail closed", safe, err)
	}
	safe, err := set.JSONBounded(wire, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(safe) > 64 || strings.Contains(string(safe), "q") {
		t.Fatalf("bounded JSON retained credential or exceeded limit: %q", safe)
	}
}

func TestJSONBoundedAccountsForCanonicalEscapingAndStructure(t *testing.T) {
	wire := []byte(`{"value":"<"}`)
	if safe, err := New().JSONBounded(wire, len(wire)-1); err == nil || safe != nil {
		t.Fatalf("undersized canonical JSON = %s, %v; want fail closed", safe, err)
	}
	safe, err := New().JSONBounded(wire, len(`{"value":"\u003c"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(safe), `{"value":"\u003c"}`; got != want {
		t.Fatalf("canonical JSON = %q, want %q", got, want)
	}
}

func TestJSONFailsClosedWhenStructureReconstructsCredential(t *testing.T) {
	for _, test := range []struct {
		secret string
		wire   string
	}{
		{secret: `left","right`, wire: `["left","right"]`},
		{secret: `left","right`, wire: `[ "left" , "right" ]`},
		{secret: `left":"right`, wire: `{"left":"right"}`},
	} {
		set := New(test.secret)
		matched, err := set.JSONContains([]byte(test.wire))
		if err != nil {
			t.Fatal(err)
		}
		if !matched {
			t.Fatalf("structural credential was not detected in %s", test.wire)
		}
		if safe, err := set.JSON([]byte(test.wire)); err == nil || safe != nil {
			t.Fatalf("structural JSON sanitization = %s, %v; want fail closed", safe, err)
		}
		if safe, err := set.JSONBounded([]byte(test.wire), 1024); err == nil || safe != nil {
			t.Fatalf("bounded structural JSON sanitization = %s, %v; want fail closed", safe, err)
		}
	}
}

func TestSetFormattingAndUnionDoNotExposeLiterals(t *testing.T) {
	const secret = "union-format-secret"
	left := New(secret)
	union := Union(left, New("*"))
	for _, formatted := range []string{
		fmt.Sprintf("%v", *union),
		fmt.Sprintf("%+v", *union),
		fmt.Sprintf("%#v", *union),
		fmt.Sprintf("%v", union),
		fmt.Sprintf("%+v", union),
		fmt.Sprintf("%#v", union),
	} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("set formatting exposed literal: %q", formatted)
		}
	}
	if got := union.Apply(secret + "*"); union.Contains(got) {
		t.Fatalf("union output retained configured literal: %q", got)
	}
}
