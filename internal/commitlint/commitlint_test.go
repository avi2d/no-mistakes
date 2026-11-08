package commitlint

import (
	"slices"
	"strings"
	"testing"
)

var oracleConfigs = map[string]struct{ name, text string }{
	"A": {".commitlintrc.json", "{\"extends\":[\"@commitlint/config-conventional\"]}\n"},
	"B": {"commitlint.config.mjs", "// project rules\nexport default {\n  extends: ['@commitlint/config-conventional'],\n  rules: {\n    'header-max-length': [2, 'always', 72],\n    'subject-case': [0],\n  },\n};\n\n"},
	"C": {".commitlintrc.yaml", "rules:\n  type-enum: [2, always, [feat, fix]]\n  scope-enum: [2, always, [cli, daemon]]\n  header-max-length: [2, always, 50]\n\n"},
	"D": {"package.json", "{\"name\":\"d\",\"commitlint\":{\"extends\":[\"@commitlint/config-conventional\"],\"rules\":{\"header-max-length\":[1,\"always\",50]}}}\n"},
	"E": {"commitlint.config.ts", "import { RuleConfigSeverity, type UserConfig } from '@commitlint/types';\nconst config: UserConfig = {\n  extends: ['@commitlint/config-conventional'],\n  rules: {\n    'header-max-length': [RuleConfigSeverity.Error, 'always', 120],\n  },\n};\nexport default config;\n\n"},
}

var oracleTitles = []string{
	"feat(pipeline): lint the pull request title with the repository commitlint rules before opening itxxxxxxxxxxx",
	"fix(ci): retry transient workflow failures",
	"Fix: Add thing",
	"update pull request",
	"feat: add widget.",
	"feat(daemon): Add X",
	"feat(cli,pipeline): add x",
	"feat!: drop flag",
	"chore(deps): bump `Foo` to 2",
	"feat(cli): aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	"docs: API reference refresh",
}

func ruleNames(violations []Violation) []string {
	names := []string{}
	for _, v := range violations {
		names = append(names, v.Rule)
	}
	slices.Sort(names)
	return names
}

func TestLint_ReportsExactlyTheErrorsCommitlintReports(t *testing.T) {
	t.Parallel()

	cases := []struct {
		config string
		title  int
		want   []string
	}{
		{"A", 0, []string{"header-max-length"}},
		{"A", 1, []string{}},
		{"A", 2, []string{"subject-case", "type-case", "type-enum"}},
		{"A", 3, []string{"subject-empty", "type-empty"}},
		{"A", 4, []string{"subject-full-stop"}},
		{"A", 5, []string{"subject-case"}},
		{"A", 6, []string{}},
		{"A", 7, []string{}},
		{"A", 8, []string{}},
		{"A", 9, []string{}},
		{"A", 10, []string{"subject-case"}},
		{"B", 0, []string{"header-max-length"}},
		{"B", 1, []string{}},
		{"B", 2, []string{"type-case", "type-enum"}},
		{"B", 3, []string{"subject-empty", "type-empty"}},
		{"B", 4, []string{"subject-full-stop"}},
		{"B", 5, []string{}},
		{"B", 6, []string{}},
		{"B", 7, []string{}},
		{"B", 8, []string{}},
		{"B", 9, []string{"header-max-length"}},
		{"B", 10, []string{}},
		{"C", 0, []string{"header-max-length", "scope-enum"}},
		{"C", 1, []string{"scope-enum"}},
		{"C", 2, []string{"type-enum"}},
		{"C", 3, []string{}},
		{"C", 4, []string{}},
		{"C", 5, []string{}},
		{"C", 6, []string{"scope-enum"}},
		{"C", 7, []string{}},
		{"C", 8, []string{"scope-enum", "type-enum"}},
		{"C", 9, []string{"header-max-length"}},
		{"C", 10, []string{"type-enum"}},
		{"D", 0, []string{}},
		{"D", 1, []string{}},
		{"D", 2, []string{"subject-case", "type-case", "type-enum"}},
		{"D", 3, []string{"subject-empty", "type-empty"}},
		{"D", 4, []string{"subject-full-stop"}},
		{"D", 5, []string{"subject-case"}},
		{"D", 6, []string{}},
		{"D", 7, []string{}},
		{"D", 8, []string{}},
		{"D", 9, []string{}},
		{"D", 10, []string{"subject-case"}},
		{"E", 0, []string{}},
		{"E", 1, []string{}},
		{"E", 2, []string{"subject-case", "type-case", "type-enum"}},
		{"E", 3, []string{"subject-empty", "type-empty"}},
		{"E", 4, []string{"subject-full-stop"}},
		{"E", 5, []string{"subject-case"}},
		{"E", 6, []string{}},
		{"E", 7, []string{}},
		{"E", 8, []string{}},
		{"E", 9, []string{}},
		{"E", 10, []string{"subject-case"}},
	}
	for _, tc := range cases {
		source := oracleConfigs[tc.config]
		cfg, err := Parse(source.name, []byte(source.text))
		if err != nil {
			t.Fatalf("Parse(%s): %v", source.name, err)
		}
		var got []string
		if cfg != nil {
			got = ruleNames(cfg.Lint(oracleTitles[tc.title], 0))
		}
		if !slices.Equal(got, tc.want) && !(len(got) == 0 && len(tc.want) == 0) {
			t.Errorf("config %s title %d %q: got %v, want %v", tc.config, tc.title, oracleTitles[tc.title], got, tc.want)
		}
	}
}

func TestLint_109CharacterTitleBreaksA100CharacterHeaderLimitNotPasses(t *testing.T) {
	t.Parallel()

	cfg, err := Parse(".commitlintrc.json", []byte(`{"extends":["@commitlint/config-conventional"]}`))
	if err != nil || cfg == nil {
		t.Fatalf("Parse() = %v, %v", cfg, err)
	}
	title := oracleTitles[0]
	if len(title) != 109 {
		t.Fatalf("fixture title is %d characters, want 109", len(title))
	}
	got := cfg.Lint(title, 0)
	if len(got) != 1 || got[0].String() != "header must not be longer than 100 characters, current length is 109 [header-max-length]" {
		t.Fatalf("Lint() = %v", got)
	}
	if cfg.MaxHeaderLength() != 100 {
		t.Fatalf("MaxHeaderLength() = %d, want 100", cfg.MaxHeaderLength())
	}
}

func TestLint_LandedSuffixCountsOnlyAgainstHeaderLengthNotOtherRules(t *testing.T) {
	t.Parallel()

	cfg, _ := Parse(".commitlintrc.json", []byte(`{"extends":["@commitlint/config-conventional"]}`))
	title := "feat(cli): " + strings.Repeat("a", 84)
	if got := cfg.Lint(title, 0); len(got) != 0 {
		t.Fatalf("Lint(%d chars, no suffix) = %v, want none", len(title), got)
	}
	got := cfg.Lint(title, 10)
	if len(got) != 1 || got[0].Rule != "header-max-length" || !strings.Contains(got[0].Message, "current length is 105") {
		t.Fatalf("Lint(%d chars, 10-char suffix) = %v, want only header-max-length at 105", len(title), got)
	}
}

func TestParse_ConfigWithoutErrorRulesIsNoConfigNotAnEmptyGate(t *testing.T) {
	t.Parallel()

	for name, text := range map[string]string{
		"package.json":         `{"name":"x","scripts":{}}`,
		".commitlintrc.json":   `{"extends":["./local-preset"]}`,
		"commitlint.config.js": "module.exports = require('./shared')\n",
	} {
		cfg, err := Parse(name, []byte(text))
		if err != nil || cfg != nil {
			t.Errorf("Parse(%s) = %v, %v, want no config", name, cfg, err)
		}
	}
}

func TestParse_MalformedJSONIsAnErrorNotSilence(t *testing.T) {
	t.Parallel()

	if _, err := Parse(".commitlintrc.json", []byte(`{"extends": [`)); err == nil {
		t.Fatal("Parse() of malformed JSON returned no error")
	}
}

func TestLint_DefaultIgnoredTitlesPassAsCommitlintWaivesThemNotFailOnTypeRules(t *testing.T) {
	waived := []string{
		"Revert \"feat: add x\"",
		"Merge branch 'main' into feature",
		"fixup! feat: add x",
		"v1.2.3",
		"Automatic merge from main",
		"Reapply \"feat: add x\"",
		"Merged PR 12: thing",
	}
	on, err := Parse(".commitlintrc.json", []byte(`{"extends":["@commitlint/config-conventional"]}`))
	if err != nil {
		t.Fatal(err)
	}
	off, err := Parse(".commitlintrc.json", []byte(`{"extends":["@commitlint/config-conventional"],"defaultIgnores":false}`))
	if err != nil {
		t.Fatal(err)
	}
	offScript, err := Parse("commitlint.config.mjs", []byte("export default { extends: ['@commitlint/config-conventional'], defaultIgnores: false };"))
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range waived {
		if got := on.Lint(title, 0); len(got) != 0 {
			t.Errorf("Lint(%q) = %v, want commitlint's default ignore to waive it", title, got)
		}
		if got := off.Lint(title, 0); len(got) == 0 {
			t.Errorf("Lint(%q) with defaultIgnores false = no violations, want the rules applied", title)
		}
		if got := offScript.Lint(title, 0); len(got) == 0 {
			t.Errorf("script Lint(%q) with defaultIgnores false = no violations, want the rules applied", title)
		}
	}
	if got := on.Lint("Update the README.", 0); len(got) == 0 {
		t.Error("Lint(\"Update the README.\") = no violations, want a title no wildcard matches still linted")
	}
}
