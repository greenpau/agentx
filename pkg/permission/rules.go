package permission

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var aliases = map[string]string{
	"Task":            "Agent",
	"KillShell":       "TaskStop",
	"AgentOutputTool": "TaskOutput",
	"BashOutputTool":  "TaskOutput",
}

// CanonicalTool normalizes only explicit compatibility aliases.
func CanonicalTool(name string) string {
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

// ParseRule parses Tool or Tool(content). Malformed siblings can be diagnosed
// independently without weakening valid rules.
func ParseRule(raw string, effect Effect, source string, managed bool) (Rule, error) {
	if len(raw) > maximumPermissionRuleBytes || len(source) > maximumPermissionSourceBytes {
		return Rule{}, errors.New("permission rule exceeds its size limit")
	}
	text := strings.TrimSpace(raw)
	if text == "" {
		return Rule{}, errors.New("permission rule is empty")
	}
	if effect != EffectAllow && effect != EffectAsk && effect != EffectDeny {
		return Rule{}, errors.New("invalid permission effect")
	}
	name := text
	pattern := ""
	if index := strings.IndexByte(text, '('); index >= 0 {
		if !strings.HasSuffix(text, ")") || index == 0 {
			return Rule{}, errors.New("malformed parenthesized permission rule")
		}
		name = text[:index]
		pattern = text[index+1 : len(text)-1]
		if strings.Contains(pattern, "(") || strings.Contains(pattern, ")") {
			return Rule{}, errors.New("unescaped parentheses are not supported in rule content")
		}
	}
	name = CanonicalTool(strings.TrimSpace(name))
	if !validToolName(name) {
		return Rule{}, errors.New("invalid permission tool name")
	}
	if strings.HasPrefix(name, "mcp__") && pattern != "" {
		return Rule{}, errors.New("MCP rules express specificity in the canonical name")
	}
	if pattern == "" || pattern == "*" {
		pattern = ""
	} else if strings.TrimSpace(pattern) == "" {
		return Rule{}, errors.New("empty rule content")
	}
	rule := Rule{Tool: name, Pattern: pattern, Effect: effect, Source: source, Managed: managed, Raw: raw}
	if !validConfiguredRule(rule) {
		return Rule{}, errors.New("invalid permission rule")
	}
	return rule, nil
}

func validConfiguredRule(rule Rule) bool {
	if len(rule.Raw) > maximumPermissionRuleBytes ||
		len(rule.Pattern) > maximumPermissionRuleBytes ||
		len(rule.Source) > maximumPermissionSourceBytes ||
		len(rule.Tool) > maximumPermissionIdentifier ||
		(rule.Effect != EffectAllow && rule.Effect != EffectAsk && rule.Effect != EffectDeny) ||
		!validToolName(rule.Tool) ||
		(strings.HasPrefix(rule.Tool, "mcp__") && rule.Pattern != "") ||
		strings.ContainsAny(rule.Pattern, "()") {
		return false
	}
	if rule.Pattern == "" {
		return true
	}
	if strings.TrimSpace(rule.Pattern) == "" {
		return false
	}
	_, _, err := wildcardRegexp(rule.Pattern)
	return err == nil
}

func validToolName(name string) bool {
	if strings.HasPrefix(name, "mcp__") {
		parts := strings.Split(strings.TrimPrefix(name, "mcp__"), "__")
		if len(parts) < 1 || len(parts) > 2 || !validMCPRulePart(parts[0], false) {
			return false
		}
		if len(parts) == 1 {
			return true
		}
		return parts[1] == "*" || validMCPRulePart(parts[1], true)
	}
	if name == "" || !unicode.IsUpper(rune(name[0])) {
		return false
	}
	for _, r := range name {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func validMCPRulePart(part string, allowDot bool) bool {
	if part == "" || strings.Contains(part, "__") {
		return false
	}
	for _, r := range part {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || allowDot && r == '.' {
			continue
		}
		return false
	}
	return true
}

func (r Rule) String() string {
	if r.Pattern == "" {
		return r.Tool
	}
	return r.Tool + "(" + r.Pattern + ")"
}

// Matches reports whether this rule applies to one canonical tool request.
// It intentionally exposes matching, not policy effect or precedence, so
// callers such as turn-local skill scopes can add a deny-only narrowing layer
// without reimplementing wildcard and compatibility-alias semantics.
func (r Rule) Matches(tool string, contents ...string) bool {
	return r.matches(tool, contents)
}

// ExactPattern reports whether a content rule denotes one complete command
// rather than a prefix/wildcard family. It is used by deny-only composed
// scopes to preserve the evaluator's whole-compound versus per-segment rules.
func (r Rule) ExactPattern() bool {
	return r.Pattern != "" && isExactCommandPattern(r.Pattern)
}

func (r Rule) matches(tool string, contents []string) bool {
	canonical := CanonicalTool(tool)
	if strings.HasSuffix(r.Tool, "__*") && strings.HasPrefix(canonical, strings.TrimSuffix(r.Tool, "*")) {
		return true
	}
	if strings.HasPrefix(r.Tool, "mcp__") && !strings.Contains(strings.TrimPrefix(r.Tool, "mcp__"), "__") && strings.HasPrefix(canonical, r.Tool+"__") {
		return true
	}
	if r.Tool != canonical {
		return false
	}
	if r.Pattern == "" {
		return true
	}
	for _, content := range contents {
		if matchCommandPattern(r.Pattern, content) {
			return true
		}
	}
	return false
}

func matchCommandPattern(pattern, candidate string) bool {
	pattern = strings.TrimSpace(pattern)
	candidate = strings.TrimSpace(candidate)
	if strings.HasSuffix(pattern, ":*") && !strings.Contains(pattern[:len(pattern)-2], "*") {
		prefix := strings.TrimSpace(pattern[:len(pattern)-2])
		return candidate == prefix || strings.HasPrefix(candidate, prefix+" ")
	}
	re, _, err := wildcardRegexp(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(candidate)
}

func isExactCommandPattern(pattern string) bool {
	if strings.HasSuffix(strings.TrimSpace(pattern), ":*") {
		return false
	}
	escaped := false
	for i := 0; i < len(pattern); i++ {
		if escaped {
			escaped = false
			continue
		}
		if pattern[i] == '\\' {
			escaped = true
			continue
		}
		if pattern[i] == '*' {
			return false
		}
	}
	return true
}

func wildcardRegexp(pattern string) (*regexp.Regexp, bool, error) {
	var expression strings.Builder
	expression.WriteString("(?s)^")
	wildcards := 0
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\\':
			if i+1 >= len(pattern) || (pattern[i+1] != '\\' && pattern[i+1] != '*') {
				return nil, false, errors.New("invalid wildcard escape")
			}
			i++
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		case '*':
			wildcards++
			// One trailing space-star treats all arguments as optional.
			if i == len(pattern)-1 && i > 0 && pattern[i-1] == ' ' && wildcards == 1 {
				built := expression.String()
				built = strings.TrimSuffix(built, regexp.QuoteMeta(" "))
				expression.Reset()
				expression.WriteString(built)
				expression.WriteString("(?: .*)?")
			} else {
				expression.WriteString(".*")
			}
		default:
			expression.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	expression.WriteByte('$')
	re, err := regexp.Compile(expression.String())
	return re, wildcards > 0, err
}
