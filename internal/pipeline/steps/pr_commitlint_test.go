package steps

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
)

const conventionalCommitlintJSON = `{"extends":["@commitlint/config-conventional"]}`

// title109 is 109 characters, over config-conventional's 100-character header-max-length.
const title109 = "feat(pipeline): lint the pull request title with the repository commitlint rules before opening itxxxxxxxxxxx"

func commitlintContext(t *testing.T, ag agent.Agent, files map[string]string) *pipeline.StepContext {
	t.Helper()
	dir, baseSHA, _ := setupGitRepo(t)
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) > 0 {
		gitCmd(t, dir, "add", "-A")
		gitCmd(t, dir, "commit", "-m", "add commitlint config")
	}
	head := gitCmd(t, dir, "rev-parse", "HEAD")
	return newTestContext(t, ag, dir, baseSHA, head, config.Commands{})
}

func titleAgent(titles ...string) *mockAgent {
	ag := &mockAgent{name: "test"}
	ag.runFn = func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		title := titles[min(len(ag.calls), len(titles))-1]
		payload, _ := json.Marshal(map[string]string{"title": title})
		return &agent.Result{Output: payload}, nil
	}
	return ag
}

func TestRenderPRTitle_Commitlint109CharTitleIsRewrittenNotPublishedOverTheRepoLimit(t *testing.T) {
	if len(title109) != 109 {
		t.Fatalf("fixture is %d characters, want 109", len(title109))
	}
	ag := titleAgent("feat(pipeline): lint PR titles with the repository commitlint rules")
	sctx := commitlintContext(t, ag, map[string]string{".commitlintrc.json": conventionalCommitlintJSON})

	got, err := renderPRTitle(sctx, title109)
	if err != nil {
		t.Fatal(err)
	}
	if got != "feat(pipeline): lint PR titles with the repository commitlint rules" {
		t.Fatalf("renderPRTitle() = %q, want the agent's rewrite", got)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls = %d, want exactly one rewrite", len(ag.calls))
	}
	prompt := ag.calls[0].Prompt
	for _, want := range []string{title109, "[header-max-length]", ".commitlintrc.json", "at most 90 characters"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("rewrite prompt is missing %q:\n%s", want, prompt)
		}
	}
}

func TestRenderPRTitle_CommitlintRewriteStillRejectedFailsInsteadOfOpeningAFailingPR(t *testing.T) {
	ag := titleAgent(title109)
	sctx := commitlintContext(t, ag, map[string]string{".commitlintrc.json": conventionalCommitlintJSON})

	got, err := renderPRTitle(sctx, title109)
	if err == nil {
		t.Fatalf("renderPRTitle() = %q, want an error", got)
	}
	if !strings.Contains(err.Error(), "after one rewrite") || !strings.Contains(err.Error(), "header must not be longer than 100 characters") {
		t.Fatalf("error = %v, want the remaining header-max-length violation", err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("agent calls = %d, want exactly one rewrite", len(ag.calls))
	}
}

func TestRenderPRTitle_NoCommitlintConfigLeavesTheTitleAloneWithoutAnAgentCall(t *testing.T) {
	ag := titleAgent("unused")
	sctx := commitlintContext(t, ag, nil)

	got, err := renderPRTitle(sctx, title109)
	if err != nil {
		t.Fatal(err)
	}
	if got != title109 || len(ag.calls) != 0 {
		t.Fatalf("renderPRTitle() = %q with %d agent calls, want the title untouched and no call", got, len(ag.calls))
	}
}

func TestRenderPRTitle_CommitlintLimitIsTheReposRuleNotAFixed100(t *testing.T) {
	// 55 characters: within 100, but over a 60-character rule once the
	// 10-character squash-merge reserve is counted.
	title := "fix(ci): retry transient workflow failures on the queue"
	ag := titleAgent("fix(ci): retry transient workflow failures")
	sctx := commitlintContext(t, ag, map[string]string{
		"commitlint.config.mjs": "export default {\n  extends: ['@commitlint/config-conventional'],\n  rules: { 'header-max-length': [2, 'always', 60] },\n};\n",
	})

	got, err := renderPRTitle(sctx, title)
	if err != nil {
		t.Fatal(err)
	}
	if got != "fix(ci): retry transient workflow failures" || len(ag.calls) != 1 {
		t.Fatalf("renderPRTitle() = %q with %d agent calls, want one rewrite", got, len(ag.calls))
	}
	if !strings.Contains(ag.calls[0].Prompt, "at most 50 characters") {
		t.Fatalf("rewrite prompt does not carry the repo's 60-character rule:\n%s", ag.calls[0].Prompt)
	}
}

func TestRenderPRTitle_CommitlintTypeRuleTriggersARewriteNotOnlyLength(t *testing.T) {
	ag := titleAgent("chore: update pull request")
	sctx := commitlintContext(t, ag, map[string]string{
		"package.json": `{"name":"x","commitlint":{"rules":{"type-enum":[2,"always",["chore","fix"]]}}}`,
	})

	got, err := renderPRTitle(sctx, "docs: refresh the readme")
	if err != nil {
		t.Fatal(err)
	}
	if got != "chore: update pull request" || len(ag.calls) != 1 {
		t.Fatalf("renderPRTitle() = %q with %d agent calls, want one rewrite", got, len(ag.calls))
	}
	if !strings.Contains(ag.calls[0].Prompt, "type must be one of [chore, fix] [type-enum]") {
		t.Fatalf("rewrite prompt is missing the type-enum violation:\n%s", ag.calls[0].Prompt)
	}
}

func TestRenderSuppliedPRTitle_CommitlintRejectionFailsRatherThanRewritingTheVerbatimTitle(t *testing.T) {
	ag := titleAgent("unused")
	sctx := commitlintContext(t, ag, map[string]string{".commitlintrc.json": conventionalCommitlintJSON})

	if _, err := renderSuppliedPRTitle(sctx, "Update the README."); err == nil || !strings.Contains(err.Error(), "[subject-full-stop]") {
		t.Fatalf("renderSuppliedPRTitle() error = %v, want the commitlint violations", err)
	}
	if len(ag.calls) != 0 {
		t.Fatalf("agent calls = %d, want none for a supplied title", len(ag.calls))
	}
	got, err := renderSuppliedPRTitle(sctx, "docs: update the readme")
	if err != nil || got != "docs: update the readme" {
		t.Fatalf("renderSuppliedPRTitle() = %q, %v, want the passing title verbatim", got, err)
	}
}

func TestTitleAlreadyFitsWithinBudget_LiveTitleBreakingCommitlintIsNotKept(t *testing.T) {
	sctx := commitlintContext(t, titleAgent("unused"), map[string]string{".commitlintrc.json": conventionalCommitlintJSON})

	if titleAlreadyFitsWithinBudget(sctx, title109) {
		t.Fatal("titleAlreadyFitsWithinBudget() = true for a title over the repo's header-max-length")
	}
	if !titleAlreadyFitsWithinBudget(sctx, "fix(ci): retry transient workflow failures") {
		t.Fatal("titleAlreadyFitsWithinBudget() = false for a passing title")
	}
}

func TestLoadCommitlintConfig_SymlinkedConfigIsIgnoredNotFollowed(t *testing.T) {
	dir, baseSHA, _ := setupGitRepo(t)
	target := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(target, []byte(conventionalCommitlintJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, ".commitlintrc.json")); err != nil {
		t.Skip(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "symlinked config")
	sctx := newTestContext(t, nil, dir, baseSHA, gitCmd(t, dir, "rev-parse", "HEAD"), config.Commands{})

	if cfg := loadCommitlintConfig(sctx); cfg != nil {
		t.Fatalf("loadCommitlintConfig() = %+v, want nil for a symlink", cfg)
	}
}

func commitlintPRStepContext(t *testing.T, rewrite string) (*pipeline.StepContext, *mockAgent, string) {
	t.Helper()
	dir, baseSHA, _ := setupGitRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".commitlintrc.json"), []byte(conventionalCommitlintJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "add commitlint config")
	env, logFile := fakeGH(t, "")
	ag := &mockAgent{name: "test", runFn: func(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
		content := map[string]string{"title": title109, "body": "## What Changed\n\nLints the title."}
		if strings.Contains(opts.Prompt, "breaks the repository's commitlint rules") {
			content = map[string]string{"title": rewrite}
		}
		payload, _ := json.Marshal(content)
		return &agent.Result{Output: payload}, nil
	}}
	sctx := newPRTestContext(t, ag, dir, baseSHA, gitCmd(t, dir, "rev-parse", "HEAD"), config.Commands{})
	sctx.Env = env
	return sctx, ag, logFile
}

func TestPRStep_CreatesThePRWithTheCommitlintRewriteNotThe109CharDraft(t *testing.T) {
	sctx, _, logFile := commitlintPRStepContext(t, "feat(pipeline): lint PR titles with the repository commitlint rules")

	if _, err := (&PRStep{}).Execute(sctx); err != nil {
		t.Fatal(err)
	}
	logData, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logData), "--title feat(pipeline): lint PR titles with the repository commitlint rules --body") {
		t.Fatalf("gh pr create did not get the rewritten title:\n%s", logData)
	}
	if strings.Contains(string(logData), title109) {
		t.Fatalf("the 109-character draft reached gh:\n%s", logData)
	}
}

func TestPRStep_CommitlintRejectedRewriteFailsTheStepWithoutCreatingAPR(t *testing.T) {
	sctx, _, logFile := commitlintPRStepContext(t, title109)

	_, err := (&PRStep{}).Execute(sctx)
	if err == nil || !strings.Contains(err.Error(), "[header-max-length]") {
		t.Fatalf("Execute() error = %v, want the header-max-length violation", err)
	}
	logData, _ := os.ReadFile(logFile)
	if strings.Contains(string(logData), "pr create") {
		t.Fatalf("a PR was created with a title commitlint rejects:\n%s", logData)
	}
}
