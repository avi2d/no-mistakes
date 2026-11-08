package commitlint

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// SearchPlaces is commitlint's own config search order; the first file that
// holds a config wins.
var SearchPlaces = []string{
	"package.json",
	"package.yaml",
	".commitlintrc",
	".commitlintrc.json",
	".commitlintrc.yaml",
	".commitlintrc.yml",
	".commitlintrc.js",
	".commitlintrc.cjs",
	".commitlintrc.mjs",
	"commitlint.config.js",
	"commitlint.config.cjs",
	"commitlint.config.mjs",
	".commitlintrc.ts",
	".commitlintrc.cts",
	".commitlintrc.mts",
	"commitlint.config.ts",
	"commitlint.config.cts",
	"commitlint.config.mts",
}

const conventionalPreset = "@commitlint/config-conventional"

const severityError = 2

type rule struct {
	level int
	when  string
	value any
}

// Config holds the header rules of one repository's commitlint config.
type Config struct {
	Source  string
	rules   map[string]rule
	pattern *regexp.Regexp
}

// Violation is one error-level rule the header breaks.
type Violation struct {
	Rule    string
	Message string
}

func (v Violation) String() string {
	return v.Message + " [" + v.Rule + "]"
}

var (
	angularHeader      = regexp.MustCompile(`^(\w*)(?:\((.*)\))?: (.*)$`)
	conventionalHeader = regexp.MustCompile(`^(\w*)(?:\((.*)\))?!?: (.*)$`)
)

func conventionalRules() map[string]rule {
	return map[string]rule{
		"header-max-length": {level: severityError, when: "always", value: 100},
		"header-trim":       {level: severityError, when: "always"},
		"subject-case":      {level: severityError, when: "never", value: []string{"sentence-case", "start-case", "pascal-case", "upper-case"}},
		"subject-empty":     {level: severityError, when: "never"},
		"subject-full-stop": {level: severityError, when: "never", value: "."},
		"type-case":         {level: severityError, when: "always", value: "lower-case"},
		"type-empty":        {level: severityError, when: "never"},
		"type-enum":         {level: severityError, when: "always", value: []string{"build", "chore", "ci", "docs", "feat", "fix", "perf", "refactor", "revert", "style", "test"}},
	}
}

// Parse reads the commitlint config in the file named name. It returns nil
// when the file holds no config commitlint would load, or none whose rules can
// be read without running it: a JavaScript or TypeScript config is never
// executed, so only its literal extends and rule entries count.
func Parse(name string, data []byte) (*Config, error) {
	switch {
	case name == "package.json" || name == "package.yaml":
		var pkg map[string]any
		if err := yaml.Unmarshal(data, &pkg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		raw, ok := pkg["commitlint"].(map[string]any)
		if !ok {
			return nil, nil
		}
		return fromMap(name, raw), nil
	case isScript(name):
		return fromScript(name, string(data)), nil
	default:
		var raw map[string]any
		if err := yaml.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if raw == nil {
			return nil, nil
		}
		return fromMap(name, raw), nil
	}
}

// IsConfigHolder reports whether a file in SearchPlaces can be passed over
// when it holds no commitlint config, as commitlint passes over a
// package.json without a commitlint key.
func IsConfigHolder(name string) bool {
	return name == "package.json" || name == "package.yaml"
}

func isScript(name string) bool {
	for _, ext := range []string{".js", ".cjs", ".mjs", ".ts", ".cts", ".mts"} {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

func fromMap(source string, raw map[string]any) *Config {
	conventional := false
	switch ext := raw["extends"].(type) {
	case string:
		conventional = ext == conventionalPreset
	case []any:
		for _, item := range ext {
			if item == conventionalPreset {
				conventional = true
			}
		}
	}
	preset, _ := raw["parserPreset"].(string)
	cfg := newConfig(source, conventional, preset)
	if rules, ok := raw["rules"].(map[string]any); ok {
		for name, entry := range rules {
			if r, ok := ruleFromList(entry); ok {
				cfg.rules[name] = r
			}
		}
	}
	return cfg.orNil()
}

func newConfig(source string, conventional bool, preset string) *Config {
	cfg := &Config{Source: source, rules: map[string]rule{}, pattern: angularHeader}
	if conventional {
		cfg.rules = conventionalRules()
	}
	if conventional || strings.Contains(preset, "conventionalcommits") {
		cfg.pattern = conventionalHeader
	}
	return cfg
}

func (c *Config) orNil() *Config {
	for _, r := range c.rules {
		if r.level == severityError {
			return c
		}
	}
	return nil
}

func ruleFromList(entry any) (rule, bool) {
	list, ok := entry.([]any)
	if !ok || len(list) == 0 {
		return rule{}, false
	}
	level, ok := list[0].(int)
	if !ok {
		return rule{}, false
	}
	r := rule{level: level}
	if len(list) > 1 {
		if when, ok := list[1].(string); ok {
			r.when = when
		}
	}
	if len(list) > 2 {
		switch v := list[2].(type) {
		case int, string:
			r.value = v
		case []any:
			var items []string
			for _, item := range v {
				if s, ok := item.(string); ok {
					items = append(items, s)
				}
			}
			r.value = items
		}
	}
	return r, true
}

var (
	scriptComment = regexp.MustCompile(`/\*(?s:.*?)\*/|(?m:^\s*//.*$)`)
	scriptRule    = regexp.MustCompile(`["']?([a-z]+(?:-[a-z]+)+)["']?\s*:\s*\[\s*([012]|RuleConfigSeverity\.(?:Disabled|Warning|Error))\s*(?:,\s*["'](always|never)["']\s*(?:,\s*(\d+|"[^"]*"|'[^']*'|\[[^\]]*\]))?)?\s*,?\s*\]`)
	scriptString  = regexp.MustCompile(`"([^"]*)"|'([^']*)'`)
)

func fromScript(source, text string) *Config {
	text = scriptComment.ReplaceAllString(text, "")
	conventional := strings.Contains(text, "\""+conventionalPreset+"\"") || strings.Contains(text, "'"+conventionalPreset+"'")
	cfg := newConfig(source, conventional, text)
	for _, m := range scriptRule.FindAllStringSubmatch(text, -1) {
		r := rule{level: scriptSeverity(m[2]), when: m[3]}
		switch value := m[4]; {
		case value == "":
		case strings.HasPrefix(value, "["):
			var items []string
			for _, s := range scriptString.FindAllStringSubmatch(value, -1) {
				items = append(items, s[1]+s[2])
			}
			r.value = items
		case value[0] == '"' || value[0] == '\'':
			r.value = value[1 : len(value)-1]
		default:
			n, _ := strconv.Atoi(value)
			r.value = n
		}
		cfg.rules[m[1]] = r
	}
	return cfg.orNil()
}

func scriptSeverity(token string) int {
	switch token {
	case "2", "RuleConfigSeverity.Error":
		return 2
	case "1", "RuleConfigSeverity.Warning":
		return 1
	}
	return 0
}

// MaxHeaderLength is the error-level header-max-length, or zero when the
// config sets none.
func (c *Config) MaxHeaderLength() int {
	r, ok := c.rules["header-max-length"]
	if !ok || r.level != severityError {
		return 0
	}
	n, _ := r.value.(int)
	return n
}

// Lint returns the error-level rules header breaks. landedSuffix is the
// length the forge adds to the header when it lands, counted only against
// header-max-length. Rules this package cannot evaluate exactly are skipped,
// never guessed, so a reported violation is one commitlint reports too.
func (c *Config) Lint(header string, landedSuffix int) []Violation {
	parsed := c.parse(header)
	var out []Violation
	for _, name := range ruleOrder {
		r, ok := c.rules[name]
		if !ok || r.level != severityError {
			continue
		}
		if message, broken := evaluate(name, r, parsed, landedSuffix); broken {
			out = append(out, Violation{Rule: name, Message: message})
		}
	}
	return out
}

var ruleOrder = []string{
	"header-trim", "header-max-length", "header-min-length", "header-full-stop",
	"type-empty", "type-enum", "type-case",
	"scope-empty", "scope-enum",
	"subject-empty", "subject-case", "subject-full-stop",
}

type header struct {
	raw, typ, scope, subject string
}

func (c *Config) parse(raw string) header {
	h := header{raw: raw}
	if m := c.pattern.FindStringSubmatch(raw); m != nil {
		h.typ, h.scope, h.subject = m[1], m[2], m[3]
	}
	return h
}

// jsLength is String.prototype.length, which commitlint's length rules count.
func jsLength(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func evaluate(name string, r rule, h header, landedSuffix int) (string, bool) {
	if r.when == "" {
		r.when = "always"
		if name == "scope-empty" {
			r.when = "never"
		}
	}
	negated := r.when == "never"
	switch name {
	case "header-trim":
		if strings.TrimSpace(h.raw) != h.raw {
			return "header must not be surrounded by whitespace", true
		}
	case "header-max-length":
		limit, ok := r.value.(int)
		length := jsLength(h.raw) + landedSuffix
		if ok && length > limit {
			if landedSuffix > 0 {
				return fmt.Sprintf("header must not be longer than %d characters once the %d-character squash-merge suffix is counted, current length is %d", limit, landedSuffix, length), true
			}
			return fmt.Sprintf("header must not be longer than %d characters, current length is %d", limit, length), true
		}
	case "header-min-length":
		if limit, ok := r.value.(int); ok && jsLength(h.raw) < limit {
			return fmt.Sprintf("header must not be shorter than %d characters, current length is %d", limit, jsLength(h.raw)), true
		}
	case "header-full-stop":
		stop := fullStop(r)
		if strings.HasSuffix(h.raw, stop) == negated {
			return "header " + mayOrMust(negated) + " end with full stop", true
		}
	case "type-empty":
		if (h.typ != "") != negated {
			return "type " + mayOrMust(negated) + " be empty", true
		}
	case "scope-empty":
		if negated && h.scope == "" {
			return "scope may not be empty", true
		}
		if !negated && h.scope != "" {
			return "scope must be empty", true
		}
	case "type-enum":
		values, _ := r.value.([]string)
		if h.typ != "" && contains(values, h.typ) == negated {
			return "type must " + notIf(negated) + "be one of [" + strings.Join(values, ", ") + "]", true
		}
	case "scope-enum":
		values, _ := r.value.([]string)
		if h.scope != "" && len(values) > 0 && scopeInEnum(h.scope, values, negated) == negated {
			return "scope must " + notIf(negated) + "be one of [" + strings.Join(values, ", ") + "]", true
		}
	case "type-case":
		if h.typ != "" {
			return caseViolation("type", h.typ, r, negated)
		}
	case "subject-case":
		if startsWithCasedLetter(h.subject) {
			return caseViolation("subject", h.subject, r, negated)
		}
	case "subject-empty":
		if (h.subject != "") != negated {
			return "subject " + mayOrMust(negated) + " be empty", true
		}
	case "subject-full-stop":
		if i := strings.Index(h.raw, ":"); i > 0 && i == len(h.raw)-1 {
			return "", false
		}
		stop := fullStop(r)
		hasStop := strings.HasSuffix(h.raw, stop) && !strings.HasSuffix(h.raw, "...")
		if hasStop == negated {
			return "subject " + mayOrMust(negated) + " end with full stop", true
		}
	}
	return "", false
}

func fullStop(r rule) string {
	if s, ok := r.value.(string); ok && s != "" {
		return s
	}
	return "."
}

func mayOrMust(negated bool) string {
	if negated {
		return "may not"
	}
	return "must"
}

func notIf(negated bool) string {
	if negated {
		return "not "
	}
	return ""
}

func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

var scopeDelimiter = regexp.MustCompile(`/|\\|, ?`)

func scopeInEnum(scope string, values []string, negated bool) bool {
	parts := scopeDelimiter.Split(scope, -1)
	if negated {
		for _, p := range parts {
			if contains(values, p) {
				return true
			}
		}
		return contains(values, scope)
	}
	for _, p := range parts {
		if !contains(values, p) {
			return contains(values, scope)
		}
	}
	return true
}

func startsWithCasedLetter(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.In(r, unicode.Ll, unicode.Lu, unicode.Lt)
}

var quoted = regexp.MustCompile("`.*?`|\".*?\"|'.*?'")

// caseViolation evaluates a type-case or subject-case rule. A case whose
// transform this package does not reproduce exactly counts as not matching:
// under "never" that can only miss a violation, and under "always" a rule
// naming one is skipped whole.
func caseViolation(field, value string, r rule, negated bool) (string, bool) {
	var cases []string
	switch v := r.value.(type) {
	case string:
		cases = []string{v}
	case []string:
		cases = v
	}
	var matched []string
	for _, c := range cases {
		ok, known := ensureCase(value, c)
		if !known {
			if !negated {
				return "", false
			}
			continue
		}
		if ok {
			matched = append(matched, c)
		}
	}
	result := len(matched) > 0
	if result != negated {
		return "", false
	}
	if negated {
		return field + " must not be " + strings.Join(matched, ", "), true
	}
	return field + " must be " + strings.Join(cases, ", "), true
}

func ensureCase(raw, target string) (matches, known bool) {
	input := strings.TrimSpace(quoted.ReplaceAllString(raw, ""))
	var transformed string
	switch target {
	case "upper-case", "uppercase":
		transformed = strings.ToUpper(input)
	case "lower-case", "lowercase", "lowerCase":
		transformed = strings.ToLower(input)
	case "sentence-case", "sentencecase":
		first, size := utf8.DecodeRuneInString(input)
		transformed = string(unicode.ToUpper(first)) + input[size:]
		if input == "" {
			transformed = ""
		}
	default:
		return false, false
	}
	if transformed == "" || (transformed[0] >= '0' && transformed[0] <= '9') {
		return true, true
	}
	return transformed == input, true
}
