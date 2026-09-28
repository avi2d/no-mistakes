package steps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

func TestChangedTestAssertions_ParsesRemovedAssertions(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/app_test.go b/app_test.go\n" +
		"--- a/app_test.go\n" +
		"+++ b/app_test.go\n" +
		"@@ -5,7 +5,7 @@ func TestAdd(t *testing.T) {\n" +
		"     if got := Add(1, 1); got != 2 {\n" +
		"-        t.Errorf(\"Add(1, 1) = %d, want 2\", got)\n" +
		"+        t.Errorf(\"Add(1, 1) = %d, want 3\", got)\n" +
		"     }\n" +
		" }\n" +
		"diff --git a/main.go b/main.go\n" +
		"--- a/main.go\n" +
		"+++ b/main.go\n" +
		"@@ -1,3 +1,3 @@ func main() {\n" +
		"-    fmt.Println(\"hello\")\n" +
		"+    fmt.Println(\"goodbye\")\n" +
		" }\n"
	got := changedTestAssertions(diff)
	if len(got) != 1 {
		t.Fatalf("changed assertions = %v, want exactly one", got)
	}
	if !strings.Contains(got[0], "app_test.go:6:") || !strings.Contains(got[0], "want 2") {
		t.Fatalf("changed assertion = %q, want app_test.go:7 with the old expectation", got[0])
	}
}

func TestChangedTestAssertions_IgnoresAddedAndNonAssertionLines(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/web.test.ts b/web.test.ts\n" +
		"--- a/web.test.ts\n" +
		"+++ b/web.test.ts\n" +
		"@@ -1,4 +1,4 @@ test(\"renders\", () => {\n" +
		"-    // expect(title).toBe(\"old\")\n" +
		"+    expect(title).toBe(\"new\")\n" +
		"-    const label = \"plain assignment\";\n" +
		"+    const label = \"renamed\";\n" +
		" });\n"
	if got := changedTestAssertions(diff); len(got) != 0 {
		t.Fatalf("changed assertions = %v, want none: added lines and comments are not existing assertions", got)
	}
}

func TestChangedTestAssertions_DetectsBunExpectRemoval(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/checks.test.ts b/checks.test.ts\n" +
		"--- a/checks.test.ts\n" +
		"+++ b/checks.test.ts\n" +
		"@@ -10,3 +10,2 @@ test(\"header length\", () => {\n" +
		"-    expect(lint(header101)).toBe(1);\n" +
		"     expect(lint(header40)).toBe(0);\n" +
		"-    // trailing comment\n" +
		" });\n"
	got := changedTestAssertions(diff)
	if len(got) != 1 || !strings.Contains(got[0], "checks.test.ts:10:") {
		t.Fatalf("changed assertions = %v, want the removed expect at checks.test.ts:12", got)
	}
}

func TestChangedTestAssertions_TruncatesLongLists(t *testing.T) {
	t.Parallel()
	dir, _, _ := setupGitRepo(t)
	var before, after strings.Builder
	before.WriteString("package app\n\nfunc TestMany(t *testing.T) {\n")
	after.WriteString("package app\n\nfunc TestMany(t *testing.T) {\n")
	for i := 1; i <= 60; i++ {
		fmt.Fprintf(&before, "    require.Equal(t, 1, got%d)\n", i)
		fmt.Fprintf(&after, "    require.Equal(t, 2, got%d)\n", i)
	}
	before.WriteString("}\n")
	after.WriteString("}\n")
	base := commitTestFixture(t, dir, "big_test.go", before.String())
	if err := os.WriteFile(filepath.Join(dir, "big_test.go"), []byte(after.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	section := testAssertionChangesSection(context.Background(), dir, base)
	if !strings.Contains(section, "Changed test assertions in fix rounds:") {
		t.Fatalf("expected an assertion list, got %q", section)
	}
	if !strings.Contains(section, "further changed assertions omitted") {
		t.Fatalf("long assertion list is not truncated:\n%s", section)
	}
}

const testFixIntentFixture = `package app

import "testing"

func TestAdd(t *testing.T) {
	if got := Add(1, 1); got != 2 {
		t.Errorf("Add(1, 1) = %d, want 2", got)
	}
}
`

func commitTestFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "add fixture")
	return gitCmd(t, dir, "rev-parse", "HEAD")
}

func fixFindingsForTest() string {
	raw, _ := json.Marshal(Findings{Items: []Finding{{
		ID:          "review-1",
		Severity:    "error",
		Action:      "auto-fix",
		File:        "app_test.go",
		Description: "assertion does not match the stated intent",
	}}, Summary: "one finding"})
	return string(raw)
}

func rereviewWithCoverage(t *testing.T, dir, baseSHA string) func(context.Context, agent.RunOpts) (*agent.Result, error) {
	t.Helper()
	return func(context.Context, agent.RunOpts) (*agent.Result, error) {
		rereview := cleanReviewFindings()
		rereview.ReviewedPaths = fullReviewCoverage(t, dir, baseSHA)
		out, _ := json.Marshal(rereview)
		return &agent.Result{Output: out}, nil
	}
}

func TestReviewStep_RereviewListsChangedAssertions(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitTestFixture(t, dir, "app_test.go", testFixIntentFixture)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	var prompts []string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompts = append(prompts, opts.Prompt)
			if len(prompts) == 1 {
				fixed := strings.Replace(testFixIntentFixture, "want 2", "want 3", 1)
				if err := os.WriteFile(filepath.Join(dir, "app_test.go"), []byte(fixed), 0o644); err != nil {
					return nil, err
				}
				return &agent.Result{Output: json.RawMessage(`{"summary":"fix assertion"}`)}, nil
			}
			return rereviewWithCoverage(t, dir, baseSHA)(ctx, opts)
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Fixing = true
	sctx.ReviewStartingHeadSHA = headSHA
	sctx.PreviousFindings = fixFindingsForTest()

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 {
		t.Fatalf("agent calls = %d, want fix then rereview", len(prompts))
	}
	rereview := prompts[1]
	if !strings.Contains(rereview, "Changed test assertions in fix rounds:") {
		t.Fatalf("rereview prompt carries no changed-assertion list:\n%s", rereview)
	}
	if !strings.Contains(rereview, "app_test.go") || !strings.Contains(rereview, "want 2") {
		t.Fatalf("rereview prompt does not name the changed assertion:\n%s", rereview)
	}
}

func TestReviewStep_RereviewWithoutAssertionChangesListsNothing(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitTestFixture(t, dir, "app_test.go", testFixIntentFixture)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	var prompts []string
	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			prompts = append(prompts, opts.Prompt)
			if len(prompts) == 1 {
				if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("touched\n"), 0o644); err != nil {
					return nil, err
				}
				return &agent.Result{Output: json.RawMessage(`{"summary":"touch fixture"}`)}, nil
			}
			return rereviewWithCoverage(t, dir, baseSHA)(ctx, opts)
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})
	sctx.Fixing = true
	sctx.ReviewStartingHeadSHA = headSHA
	sctx.PreviousFindings = fixFindingsForTest()

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(prompts) != 2 {
		t.Fatalf("agent calls = %d, want fix then rereview", len(prompts))
	}
	if strings.Contains(prompts[1], "Changed test assertions in fix rounds:") {
		t.Fatalf("rereview prompt lists changed assertions when none changed:\n%s", prompts[1])
	}
}

func TestReviewStep_InitialReviewListsNoAssertionSection(t *testing.T) {
	t.Parallel()
	dir, baseSHA, _ := setupGitRepo(t)
	headSHA := commitTestFixture(t, dir, "app_test.go", testFixIntentFixture)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	ag := &mockAgent{
		name:  "test",
		runFn: rereviewWithCoverage(t, dir, baseSHA),
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{})

	if _, err := (&ReviewStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ag.calls[0].Prompt, "Changed test assertions in fix rounds:") {
		t.Fatalf("initial review prompt carries a fix-round assertion list:\n%s", ag.calls[0].Prompt)
	}
}

func TestTestStep_FixMode_ContradictingTestStopsInsteadOfEditing(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	gitCmd(t, dir, "checkout", "--detach", headSHA)

	ag := &mockAgent{
		name: "test",
		runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
			return &agent.Result{Output: json.RawMessage(`{"summary":"leave contradicting test untouched","findings":[],"tested":["go test ./..."],"testing_summary":"test contradicts the stated intent so it was left alone","artifacts":[],"scenarios":[{"name":"the stated intent holds for a user","result":"untested","live":false,"evidence":"","reason":"the covering test contradicts the stated intent"}],"verdict":"inconclusive"}`)}, nil
		},
	}
	sctx := newTestContextWithDBRecords(t, ag, dir, baseSHA, headSHA, config.Commands{Test: "exit 0"})
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"test-1","severity":"error","description":"tests failed with exit code 1","action":"auto-fix"}],"summary":"FAIL: TestFoo"}`

	if _, err := (&TestStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) == 0 {
		t.Fatal("expected the test fixer to be invoked")
	}
	fixPrompt := ag.calls[0].Prompt
	for _, want := range []string{
		"fix the code so that failure passes",
		"Change an existing assertion only when the requested intent changed it",
		"name each changed assertion in the summary",
		"If a test contradicts the stated intent, stop",
		"leave the test untouched",
	} {
		if !strings.Contains(fixPrompt, want) {
			t.Errorf("expected test fixer prompt to contain %q, got:\n%s", want, fixPrompt)
		}
	}
	if strings.Contains(fixPrompt, "fix either the tests or the code") {
		t.Errorf("test fixer prompt still allows editing tests to pass:\n%s", fixPrompt)
	}
}

func TestChangedTestAssertions_IgnoresNonTestFiles(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/src/hooks.ts b/src/hooks.ts\n" +
		"--- a/src/hooks.ts\n" +
		"+++ b/src/hooks.ts\n" +
		"@@ -4,3 +4,2 @@ export function format(names: string[]) {\n" +
		"-    expect(names).toContain(pending);\n" +
		"     return names.join();\n"
	if got := changedTestAssertions(diff); len(got) != 0 {
		t.Fatalf("changed assertions = %v, want none: a real assertion outside a test file is not a fix-round test edit", got)
	}
}

func TestChangedTestAssertions_IgnoresCallsInsideLongerNames(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/ui.test.ts b/ui.test.ts\n" +
		"--- a/ui.test.ts\n" +
		"+++ b/ui.test.ts\n" +
		"@@ -8,5 +8,4 @@ test(\"renders\", () => {\n" +
		"-    const render = useHook(label);\n" +
		"-    if (isEqual(render, prev)) {\n" +
		"-    if (protoEqual(render, prev)) {\n" +
		"     expect(render.text).toBe(label);\n" +
		" });\n"
	if got := changedTestAssertions(diff); len(got) != 0 {
		t.Fatalf("changed assertions = %v, want none: useHook holds ok( and protoEqual holds toEqual( without calling either", got)
	}
}

func TestChangedTestAssertions_CountsDeletedTestFiles(t *testing.T) {
	t.Parallel()
	diff := "diff --git a/old_test.go b/old_test.go\n" +
		"--- a/old_test.go\n" +
		"+++ /dev/null\n" +
		"@@ -3,3 +3,0 @@ func TestOld(t *testing.T) {\n" +
		"-    require.Equal(t, 1, got)\n" +
		" }\n"
	got := changedTestAssertions(diff)
	if len(got) != 1 || !strings.Contains(got[0], "old_test.go") {
		t.Fatalf("changed assertions = %v, want the deleted test file assertion", got)
	}
}
