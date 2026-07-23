package redact

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestLiteralNeverEmitsAnySingleByteSecret(t *testing.T) {
	for value := 0; value <= 255; value++ {
		literal := string([]byte{byte(value)})
		input := "prefix-" + literal + "-middle-" + literal + "-suffix"
		got := Literal(input, literal)
		if strings.Contains(got, literal) {
			t.Fatalf("byte 0x%02x survived redaction in %q", value, got)
		}
		mask := Mask(literal)
		if mask == "" || strings.Contains(mask, literal) {
			t.Fatalf("byte 0x%02x mask is unsafe: %q", value, mask)
		}
		for _, formatted := range []string{
			fmt.Sprintf("%s", NewLiteralStream(literal)),
			fmt.Sprintf("%#v", NewLiteralStream(literal)),
			fmt.Sprintf("%s", New(literal)),
			fmt.Sprintf("%v", New(literal)),
			fmt.Sprintf("%+v", New(literal)),
			fmt.Sprintf("%#v", New(literal)),
			fmt.Sprintf("%v", *New(literal)),
			fmt.Sprintf("%+v", *New(literal)),
			fmt.Sprintf("%#v", *New(literal)),
		} {
			if strings.Contains(formatted, literal) {
				t.Fatalf("byte 0x%02x survived stream formatting in %q", value, formatted)
			}
		}
	}
}

func TestLiteralUsesConventionalMaskWhenSetSafe(t *testing.T) {
	const secret = "ordinary-production-credential"
	if got := Mask(secret); got != conventionalMask {
		t.Fatalf("ordinary mask = %q, want %q", got, conventionalMask)
	}
	for _, collision := range []string{"R", "[REDACTED]", "prefix-[REDACTED]-suffix", "D]"} {
		if got := Mask(collision); got == conventionalMask {
			t.Fatalf("collision %q selected unsafe conventional mask", collision)
		}
	}
}

func TestSetCoversRequiresEveryExactLiteral(t *testing.T) {
	frozen := New("alpha", "beta", "gamma")
	for name, test := range map[string]struct {
		candidate *Set
		want      bool
	}{
		"nil":        {candidate: nil, want: true},
		"empty":      {candidate: New(), want: true},
		"subset":     {candidate: New("gamma", "alpha"), want: true},
		"equal":      {candidate: New("beta", "alpha", "gamma"), want: true},
		"additional": {candidate: New("alpha", "delta"), want: false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := frozen.Covers(test.candidate); got != test.want {
				t.Fatalf("Covers(%s) = %v, want %v", name, got, test.want)
			}
		})
	}
	if (*Set)(nil).Covers(New("alpha")) {
		t.Fatal("nil set covered a nonempty candidate")
	}
}

func TestContainsAcrossPermutationsHasFailClosedWorkBound(t *testing.T) {
	set := New("secret")
	if !set.ContainsAcrossPermutations([]string{"ret", "one", "sec", "two", "three"}) {
		t.Fatal("bounded permutation search missed a reconstructed literal")
	}
	if set.ContainsAcrossPermutations([]string{"safe", "one", "two", "three", "four"}) {
		t.Fatal("bounded safe record was rejected")
	}

	oversized := make([]string, maximumPermutationValues+1)
	for index := range oversized {
		oversized[index] = strings.Repeat("safe", 1<<12)
	}
	if !set.ContainsAcrossPermutations(oversized) {
		t.Fatal("oversized permutation record did not fail closed")
	}
}

func TestLiteralUnicodeClassesAndHistoricalMarkerOverlap(t *testing.T) {
	for _, literal := range []string{
		"[REDACTED]",
		"*",
		"**********",
		"R",
		"é",
		"界",
		"🙂",
		"\u2028",
		"\x00",
	} {
		input := "left-" + literal + "-right-" + literal
		got := Literal(input, literal)
		if strings.Contains(got, literal) {
			t.Fatalf("literal %q survived in %q", literal, got)
		}
		if utf8.ValidString(input) && !utf8.ValidString(got) {
			t.Fatalf("valid UTF-8 input for %q produced invalid output %q", literal, got)
		}
	}
}

func TestLiteralExhaustiveSmallAlphabetCannotReconstructAcrossMask(t *testing.T) {
	literals := words("*ab", 1, 4)
	inputs := words("*ab", 0, 6)
	for _, literal := range literals {
		for _, input := range inputs {
			got := Literal(input, literal)
			if strings.Contains(got, literal) {
				t.Fatalf("literal %q reconstructed from input %q as %q", literal, input, got)
			}
		}
	}
}

func TestLiteralsNeverReintroducesAnotherConfiguredValue(t *testing.T) {
	tests := []struct {
		name     string
		literals []string
		input    string
	}{
		{
			name:     "nested and marker overlap",
			literals: []string{"credential-long", "credential", "[REDACTED]", "*", "R"},
			input:    "credential-long|credential|[REDACTED]|*|R",
		},
		{
			name:     "replacement boundary candidates",
			literals: []string{"abc", "cde", "a", "e"},
			input:    "ababcdecdexabc",
		},
		{
			name:     "duplicates and empty",
			literals: []string{"secret", "", "secret", "ret"},
			input:    "secret-ret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Literals(test.input, test.literals...)
			for _, literal := range test.literals {
				if literal != "" && strings.Contains(got, literal) {
					t.Fatalf("literal %q survived multi-redaction in %q", literal, got)
				}
			}
		})
	}
}

func TestLiteralsCanonicalAcrossOrderAndIdempotent(t *testing.T) {
	literals := []string{"earlier-secret", "*", "R", "[REDACTED]"}
	input := strings.Join(literals, "|")
	permutations := [][]string{
		literals,
		{literals[3], literals[2], literals[1], literals[0]},
		{literals[1], literals[3], literals[0], literals[2]},
	}
	want := Literals(input, permutations[0]...)
	for _, permutation := range permutations {
		got := Literals(input, permutation...)
		if got != want {
			t.Fatalf("order %q produced %q, want %q", permutation, got, want)
		}
		if second := Literals(got, permutation...); second != got {
			t.Fatalf("redaction was not idempotent: first=%q second=%q", got, second)
		}
	}
}

func TestLiteralSetBoundsLongAndShortReplacementAmplification(t *testing.T) {
	long := strings.Repeat("long-credential-", 1024)
	input := strings.Repeat("x", 32<<10)
	got := Literals(input, long, "x")
	if len(got) > len(input)*len(conventionalMask) {
		t.Fatalf("redaction amplified %d bytes to %d", len(input), len(got))
	}
	if strings.Contains(got, long) || strings.Contains(got, "x") {
		t.Fatal("mixed long/short set leaked a configured literal")
	}
}

func TestLiteralsSuppressesMatchWhenEveryPrintableGuardIsConfigured(t *testing.T) {
	literals := make([]string, 0, len(guardCandidates))
	for index := 0; index < len(guardCandidates); index++ {
		literals = append(literals, guardCandidates[index:index+1])
	}
	if mask := Mask(literals...); mask != "" {
		t.Fatalf("guard-exhausted mask = %q, want suppression", mask)
	}
	if got := Literals("visible "+guardCandidates, literals...); got != "" {
		t.Fatalf("guard-exhausted redaction = %q, want empty", got)
	}
	if got := Literals("\u2603", literals...); got == "" {
		t.Fatal("safe value was suppressed without a configured literal")
	}
}

func TestLiteralStreamGuardExhaustionSuppressesCompleteStream(t *testing.T) {
	var literal strings.Builder
	literal.WriteString(conventionalMask)
	for index := 0; index < len(guardCandidates); index++ {
		literal.WriteString(strings.Repeat(guardCandidates[index:index+1], minimumMaskWidth))
	}
	secret := literal.String()
	if mask := Mask(secret); mask != "" {
		t.Fatalf("constructed literal unexpectedly has mask %q", mask)
	}
	input := secret[:1] + secret + secret[1:]
	for split := 0; split <= len(input); split++ {
		stream := NewLiteralStream(secret)
		got := stream.Write(input[:split]) + stream.Write(input[split:]) + stream.Flush()
		if got != "" || strings.Contains(got, secret) {
			t.Fatalf("split %d guard-exhausted stream = %q, want complete suppression", split, got)
		}
	}
}

func TestLiteralStreamMatchesWholeValueAtEveryBoundary(t *testing.T) {
	tests := []struct {
		literal string
		input   string
	}{
		{literal: "R", input: "before R after R"},
		{literal: "[REDACTED]", input: "before [REDACTED] after"},
		{literal: "abab", input: "xababababy"},
		{literal: "credential-boundary", input: "before credential-boundary after"},
		{literal: "🙂界", input: "a🙂界b🙂界c"},
	}
	for _, test := range tests {
		want := Literal(test.input, test.literal)
		for split := 0; split <= len(test.input); split++ {
			redactor := NewLiteralStream(test.literal)
			got := redactor.Write(test.input[:split]) + redactor.Write(test.input[split:]) + redactor.Flush()
			if got != want || strings.Contains(got, test.literal) {
				t.Fatalf("literal %q split %d = %q, want %q", test.literal, split, got, want)
			}
		}

		redactor := NewLiteralStream(test.literal)
		var got strings.Builder
		for index := 0; index < len(test.input); index++ {
			got.WriteString(redactor.Write(test.input[index : index+1]))
		}
		got.WriteString(redactor.Flush())
		if got.String() != want || strings.Contains(got.String(), test.literal) {
			t.Fatalf("literal %q byte chunks = %q, want %q", test.literal, got.String(), want)
		}
	}
}

func TestSetStreamRedactsEveryLiteralAcrossEveryWriteBoundary(t *testing.T) {
	literals := []string{"alpha-secret", "beta", "🙂界"}
	input := "before alpha-secret middle beta after 🙂界 done"
	set := New(literals...)
	want := set.Apply(input)
	for split := 0; split <= len(input); split++ {
		stream := NewSetStream(set)
		got := stream.Write(input[:split]) + stream.Write(input[split:]) + stream.Flush()
		if got != want {
			t.Fatalf("split %d = %q, want %q", split, got, want)
		}
		for _, literal := range literals {
			if strings.Contains(got, literal) {
				t.Fatalf("split %d retained literal %q in %q", split, literal, got)
			}
		}
	}
}

func TestSetStreamSuppressesEverythingWhenNoSharedMarkerIsSafe(t *testing.T) {
	literals := make([]string, 0, len(guardCandidates))
	for index := 0; index < len(guardCandidates); index++ {
		literals = append(literals, guardCandidates[index:index+1])
	}
	set := New(literals...)
	stream := NewSetStream(set)
	if got := stream.Write("ordinary output") + stream.Flush(); got != "" {
		t.Fatalf("guard-exhausted set stream = %q, want suppression", got)
	}
}

func TestLiteralStreamFlushesOnlyIncompleteNonsecretSuffix(t *testing.T) {
	redactor := NewLiteralStream("secret")
	if got := redactor.Write("safe-secre"); got != "safe-" {
		t.Fatalf("stream prefix = %q", got)
	}
	if got := redactor.Flush(); got != "secre" {
		t.Fatalf("stream flush = %q", got)
	}
	if got := redactor.Write("t-after-flush"); got != "" {
		t.Fatalf("post-flush write = %q, want suppression", got)
	}
	if got := redactor.Flush(); got != "" {
		t.Fatalf("second flush = %q, want empty", got)
	}
}

func TestLiteralStreamFormattingByValueDoesNotExposeState(t *testing.T) {
	const secret = "formatting-secret"
	redactor := NewLiteralStream(secret)
	_ = redactor.Write("prefix-" + secret[:8])
	for _, value := range []string{
		fmt.Sprintf("%v", *redactor),
		fmt.Sprintf("%+v", *redactor),
		fmt.Sprintf("%#v", *redactor),
		fmt.Sprintf("%v", redactor),
		fmt.Sprintf("%+v", redactor),
		fmt.Sprintf("%#v", redactor),
	} {
		if strings.Contains(value, secret) || strings.Contains(value, secret[:8]) {
			t.Fatalf("stream formatting exposed state: %q", value)
		}
	}
}

func TestSetStreamFormattingByValueDoesNotExposeState(t *testing.T) {
	const secret = "set-stream-formatting-secret"
	redactor := NewSetStream(New(secret, "second-formatting-secret"))
	_ = redactor.Write("prefix-" + secret[:10])
	for _, value := range []string{
		fmt.Sprintf("%v", *redactor),
		fmt.Sprintf("%+v", *redactor),
		fmt.Sprintf("%#v", *redactor),
		fmt.Sprintf("%v", redactor),
		fmt.Sprintf("%+v", redactor),
		fmt.Sprintf("%#v", redactor),
	} {
		if strings.Contains(value, secret) || strings.Contains(value, secret[:10]) {
			t.Fatalf("set stream formatting exposed state: %q", value)
		}
	}
}

func TestLiteralStreamTerminalTruncationMarkerIsSafeAtEveryPrefix(t *testing.T) {
	for _, test := range []struct {
		name    string
		literal string
		input   string
	}{
		{name: "conventional", literal: "ordinary-production-credential", input: "before ordinary-production-credential after"},
		{name: "fallback", literal: "R", input: "before R after"},
		{name: "historical marker", literal: "[REDACTED]", input: "before [REDACTED] after"},
		{name: "reviewer counterexample", literal: "a*\n", input: "aa*\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			safe := Literal(test.input, test.literal)
			marker := NewLiteralStream(test.literal).TruncationMarker()
			if marker == "" {
				t.Fatal("configured literal had no terminal marker")
			}
			for cut := 0; cut <= len(safe); cut++ {
				got := safe[:cut] + marker
				if strings.Contains(got, test.literal) {
					t.Fatalf("cut %d reconstructed %q in %q", cut, test.literal, got)
				}
			}
		})
	}
}

func TestRedactBoundedIsSafeAtEveryLimit(t *testing.T) {
	for _, test := range []struct {
		name     string
		literals []string
		input    string
	}{
		{name: "conventional", literals: []string{"ordinary-production-credential"}, input: "before ordinary-production-credential after"},
		{name: "fallback union", literals: []string{"R", "*"}, input: "R--*--R"},
		{name: "overlap", literals: []string{"abab", "R"}, input: "xababababyR"},
	} {
		t.Run(test.name, func(t *testing.T) {
			set := New(test.literals...)
			full := set.Apply(test.input)
			for limit := 0; limit <= len(full)+1; limit++ {
				got, truncated, suppressed := set.RedactBounded(test.input, limit)
				if suppressed {
					t.Fatalf("limit %d unexpectedly suppressed", limit)
				}
				for _, literal := range test.literals {
					if strings.Contains(got, literal) {
						t.Fatalf("limit %d retained %q in %q", limit, literal, got)
					}
				}
				if truncated != (len(full) > limit) {
					t.Fatalf("limit %d truncated=%t, full bytes=%d", limit, truncated, len(full))
				}
				if len(got) > limit+len(set.TerminalMarker()) {
					t.Fatalf("limit %d returned %d bytes", limit, len(got))
				}
			}
		})
	}
}

func TestRedactBoundedGuardExhaustionFailsClosedBeforeFraming(t *testing.T) {
	literals := make([]string, 0, len(guardCandidates)+1)
	for index := 0; index < len(guardCandidates); index++ {
		literals = append(literals, guardCandidates[index:index+1])
	}
	literals = append(literals, "[]")
	set := New(literals...)
	if set.TerminalMarker() != "" {
		t.Fatalf("guard-exhausted terminal marker = %q", set.TerminalMarker())
	}
	got, truncated, suppressed := set.RedactBounded("\u2603", 1)
	if got != "" || !truncated || !suppressed {
		t.Fatalf("guard-exhausted bound = %q, truncated=%t suppressed=%t", got, truncated, suppressed)
	}
	// A caller that ignored suppression and framed got would recreate "[]".
	if framed := "[" + got + "]"; framed != "[]" {
		t.Fatalf("counterexample framing = %q", framed)
	}
}

func words(alphabet string, minimum, maximum int) []string {
	result := make([]string, 0)
	var build func(string)
	build = func(prefix string) {
		if len(prefix) >= minimum {
			result = append(result, prefix)
		}
		if len(prefix) == maximum {
			return
		}
		for index := 0; index < len(alphabet); index++ {
			build(prefix + alphabet[index:index+1])
		}
	}
	build("")
	return result
}
