package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/greenpau/agentx/pkg/permission"
)

// QuestionOption is one explicit answer route. Presentation adapters add the
// free-form Other route rather than requiring the model to fabricate it.
type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Question is a validated user question.
type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select,omitempty"`
}

// AskFunc owns the actual interaction surface. Keys are question text and
// values are selected labels or free-form answers.
type AskFunc func(context.Context, []Question) (map[string][]string, error)

type askInput struct {
	Questions []Question `json:"questions"`
}

func askUserQuestionDescriptor(ask AskFunc) Descriptor {
	return Descriptor{
		Name: "AskUserQuestion", Source: SourceBuiltin, Description: "Ask the user one to four validated structured questions.",
		Enabled: func() bool { return ask != nil },
		InputSchema: objectSchema(map[string]any{"questions": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 4,
			"items": objectSchema(map[string]any{
				"question": stringSchema("Complete question"), "header": stringSchema("Short label"),
				"options": map[string]any{"type": "array", "minItems": 2, "maxItems": 4, "items": objectSchema(map[string]any{
					"label": stringSchema("Option label"), "description": stringSchema("Option impact or tradeoff"),
				}, "label", "description")},
				"multi_select": booleanSchema("Permit several choices"),
			}, "question", "header", "options"),
		}}, "questions"),
		Validate: func(raw json.RawMessage) (any, error) {
			var input askInput
			if err := decodeStrict(raw, &input); err != nil {
				return nil, err
			}
			if len(input.Questions) < 1 || len(input.Questions) > 4 {
				return nil, errors.New("questions must contain one to four items")
			}
			questions := make(map[string]struct{}, len(input.Questions))
			for index, question := range input.Questions {
				if strings.TrimSpace(question.Question) == "" || strings.TrimSpace(question.Header) == "" || len([]rune(question.Header)) > 12 {
					return nil, fmt.Errorf("question %d has an invalid prompt or header", index)
				}
				if _, duplicate := questions[question.Question]; duplicate {
					return nil, fmt.Errorf("question %d duplicates another prompt", index)
				}
				questions[question.Question] = struct{}{}
				if len(question.Options) < 2 || len(question.Options) > 4 {
					return nil, fmt.Errorf("question %d must have two to four options", index)
				}
				labels := make(map[string]struct{}, len(question.Options))
				for _, option := range question.Options {
					if strings.TrimSpace(option.Label) == "" || strings.TrimSpace(option.Description) == "" {
						return nil, fmt.Errorf("question %d has an empty option", index)
					}
					if _, duplicate := labels[option.Label]; duplicate {
						return nil, fmt.Errorf("question %d has duplicate option %q", index, option.Label)
					}
					labels[option.Label] = struct{}{}
				}
			}
			return input, nil
		},
		Classify: func(any) permission.Classification {
			// Interaction is an ordering barrier: concurrent questions would make
			// prompt and answer correlation depend on scheduler timing.
			return permission.Classification{ReadOnly: true, Interaction: true}
		},
		ProjectPermission: func(_ any, raw json.RawMessage) (permission.Request, error) {
			return permission.Request{Input: raw, MandatoryAsk: "tool requires a synchronous question round trip"}, nil
		},
		Call: func(ctx context.Context, _ CallContext, value any) (Output, error) {
			answers, err := ask(ctx, value.(askInput).Questions)
			if err != nil {
				return Output{}, invocationError("cancelled", "question interaction ended: %v", err)
			}
			payload, err := json.Marshal(answers)
			if err != nil {
				return Output{}, invocationError("malformed_result", "encode answers: %v", err)
			}
			return Output{Content: string(payload), Metadata: map[string]any{"answers": answers}}, nil
		},
		MaxResultChars: 100_000,
	}
}
