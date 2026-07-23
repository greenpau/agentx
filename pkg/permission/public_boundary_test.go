package permission

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAuthorizeContainsApprovalCallbackFailures(t *testing.T) {
	const secret = "permission-callback-secret-must-not-escape"
	cause := errors.New(secret)
	tests := []struct {
		name     string
		approver Approver
	}{
		{
			name: "error",
			approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
				return ApprovalResponse{}, cause
			},
		},
		{
			name: "panic",
			approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
				panic(secret)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluator, err := NewEvaluator(Config{Workspace: t.TempDir(), Approver: test.approver})
			if err != nil {
				t.Fatal(err)
			}
			_, err = evaluator.Authorize(context.Background(), Request{
				Tool: "Echo", ToolUseID: "tool", Input: json.RawMessage(`{"value":"safe"}`),
			}, nil)
			if err == nil {
				t.Fatal("approval callback failure returned nil")
			}
			if errors.Is(err, cause) {
				t.Fatal("approval callback cause remained reachable through errors.Is")
			}
			for _, rendered := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("approval callback payload escaped in %q", rendered)
				}
			}
		})
	}
}

func TestAuthorizeContainsRebuildDiagnostics(t *testing.T) {
	const secret = "permission-response-secret-must-not-escape"
	for _, test := range []struct {
		name    string
		rebuild Rebuild
	}{
		{
			name: "error",
			rebuild: func(json.RawMessage) (Request, error) {
				return Request{}, errors.New(secret)
			},
		},
		{
			name: "panic",
			rebuild: func(json.RawMessage) (Request, error) {
				panic(secret)
			},
		},
	} {
		t.Run("rebuild "+test.name, func(t *testing.T) {
			evaluator, err := NewEvaluator(Config{
				Workspace: t.TempDir(),
				Approver: func(context.Context, ApprovalRequest) (ApprovalResponse, error) {
					return ApprovalResponse{Kind: DecisionAllow, UpdatedInput: json.RawMessage(`{"value":"changed"}`)}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			decision, err := evaluator.Authorize(context.Background(), Request{
				Tool: "Echo", Input: json.RawMessage(`{"value":"safe"}`),
			}, test.rebuild)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Kind != DecisionDeny || decision.Reason != "edited input failed revalidation" {
				t.Fatalf("decision = %#v", decision)
			}
			for _, rendered := range []string{decision.Reason, fmt.Sprintf("%+v", decision), fmt.Sprintf("%#v", decision)} {
				if strings.Contains(rendered, secret) {
					t.Fatalf("rebuild payload escaped in %q", rendered)
				}
			}
		})
	}
}

func TestAuthorizeCopiesInputBeforeApprovalCallback(t *testing.T) {
	input := json.RawMessage(`{"value":"safe"}`)
	evaluator, err := NewEvaluator(Config{
		Workspace: t.TempDir(),
		Approver: func(_ context.Context, request ApprovalRequest) (ApprovalResponse, error) {
			for index := range request.Input {
				request.Input[index] = 'x'
			}
			return ApprovalResponse{Kind: DecisionAllow}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := evaluator.Authorize(context.Background(), Request{Tool: "Echo", Input: input}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != `{"value":"safe"}` || string(decision.Input) != `{"value":"safe"}` {
		t.Fatalf("approval callback mutated caller or decision input: caller=%q decision=%q", input, decision.Input)
	}
}

func TestPermissionDebugFormattingOmitsProtocolValues(t *testing.T) {
	const secret = "permission-format-secret-must-not-escape"
	values := []any{
		Config{Workspace: secret, AdditionalRoots: []string{secret}, ProtectedPaths: []string{secret}},
		PathAccess{Path: secret, Operation: PathOperation(secret)},
		PathDisposition{Reason: secret, Lexical: secret, Canonical: secret},
		Request{Tool: secret, ToolUseID: secret, Input: json.RawMessage(`"` + secret + `"`), Content: secret, MatchContents: []string{secret}},
		Decision{Kind: DecisionKind(secret), Reason: secret, Source: secret, MatchedRule: secret, Input: json.RawMessage(`"` + secret + `"`)},
		ApprovalRequest{Tool: secret, ToolUseID: secret, Input: json.RawMessage(`"` + secret + `"`), Reason: secret, MatchedRule: secret},
		ApprovalResponse{Kind: DecisionKind(secret), UpdatedInput: json.RawMessage(`"` + secret + `"`), Reason: secret},
		Rule{Tool: secret, Pattern: secret, Source: secret, Raw: secret},
		ShellAnalysis{Command: secret, Segments: []string{secret}, ReviewReason: secret, DangerReason: secret},
	}
	for _, value := range values {
		for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if rendered := fmt.Sprintf(format, value); strings.Contains(rendered, secret) {
				t.Fatalf("%T %s exposed secret in %q", value, format, rendered)
			}
		}
	}
}

func TestPermissionBoundariesRejectOversizedOrMalformedProviderProjections(t *testing.T) {
	workspace := t.TempDir()
	if _, err := NewEvaluator(Config{
		Workspace: workspace,
		Rules:     []Rule{{Tool: "Echo", Effect: Effect("invalid")}},
	}); err == nil {
		t.Fatal("malformed directly constructed rule was accepted")
	}
	if _, err := NewEvaluator(Config{
		Workspace:     workspace,
		MaxEditCycles: maximumPermissionEditCycles + 1,
	}); err == nil {
		t.Fatal("unbounded edit-cycle configuration was accepted")
	}
	evaluator, err := NewEvaluator(Config{Workspace: workspace, PromptSuppressed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluator.Authorize(context.Background(), Request{
		Tool:    "Echo",
		Content: strings.Repeat("x", maximumPermissionTextBytes+1),
	}, nil); err == nil {
		t.Fatal("oversized permission request was accepted")
	}
	if _, err := NewResolver(workspace, []string{""}); err == nil {
		t.Fatal("blank additional root was accepted as the process working directory")
	}
	var resolver *Resolver
	if decision := resolver.Inspect("safe", PathRead, false); decision.Kind != DecisionDeny {
		t.Fatalf("nil resolver decision = %#v", decision)
	}
}

func TestShellAnalyzerBoundsCommandsWordsSegmentsAndRedirections(t *testing.T) {
	if analysis, err := AnalyzeShell(strings.Repeat("x", maximumShellCommandBytes+1), t.TempDir()); err == nil || analysis.Command != "" {
		t.Fatalf("oversized command retained analysis payload: %#v, %v", analysis, err)
	}
	segments := strings.Repeat("true;", maxShellSegments+1)
	if _, err := AnalyzeShell(segments, t.TempDir()); err == nil {
		t.Fatal("oversized segment projection was accepted")
	}
	words := "cat " + strings.Repeat("x ", maximumShellWords+1)
	analysis, err := AnalyzeShell(words, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.RequiresReview || analysis.ReadOnly {
		t.Fatalf("oversized word projection was treated as safe: %#v", analysis)
	}
	redirects := "cat input " + strings.Repeat("> output ", maximumShellRedirections+1)
	analysis, err = AnalyzeShell(redirects, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !analysis.RequiresReview || analysis.ReadOnly {
		t.Fatalf("oversized redirection projection was treated as safe: %#v", analysis)
	}
}
