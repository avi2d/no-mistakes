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
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const nightlyWorkflow = `name: nightly
on:
  schedule:
    - cron: '0 3 * * *'
  workflow_dispatch:
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: example/cron-action@v1
        with:
          cron: '0 0 * * *'
`

// compactWorkflow writes its schedule items at the schedule key's own indent,
// which YAML allows for a block sequence.
const compactWorkflow = `on:
  schedule:
  - cron: "15 4 * * 1"
jobs: {}
`

func docsAndSchedulesRepo(t *testing.T, base map[string]string, change func(dir string)) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-q", "-b", "main")
	files := map[string]string{
		"README.md":                      "# Example\n",
		"docs/guide.md":                  "Guide.\n",
		"main.go":                        "package main\n",
		"AGENTS.md":                      "Agents.\n",
		".github/workflows/nightly.yml":  nightlyWorkflow,
		".github/workflows/compact.yaml": compactWorkflow,
	}
	for name, body := range base {
		files[name] = body
	}
	for name, body := range files {
		writeRepoFile(t, dir, name, body)
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "-m", "base")
	baseSHA := gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "-q", "-b", "feature")
	change(dir)
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-q", "--allow-empty", "-m", "head")
	return dir, baseSHA, gitCmd(t, dir, "rev-parse", "HEAD")
}

func writeRepoFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func replaceInRepoFile(t *testing.T, dir, name, old, replacement string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(name))
	body, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(body), old) != 1 {
		t.Fatalf("%s does not contain %q exactly once", name, old)
	}
	updated := strings.Replace(string(body), old, replacement, 1)
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

// The cases come from the skip definition: doc files by path, and workflow
// hunks whose only changed lines are cron values under on.schedule. Anything
// else, including a workflow change beside a cron line, keeps the review.
func TestDocsAndSchedulesOnly(t *testing.T) {
	cronChange := func(dir string) {
		replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "- cron: '0 3 * * *'", "- cron: '30 5 * * *'")
	}
	cases := []struct {
		name   string
		base   map[string]string
		change func(dir string)
		want   bool
	}{
		{name: "readme edit", want: true, change: func(dir string) {
			writeRepoFile(t, dir, "README.md", "# Example\n\nMore words.\n")
		}},
		{name: "doc added and doc deleted", want: true, change: func(dir string) {
			writeRepoFile(t, dir, "docs/new.rst", "New.\n")
			gitCmd(t, dir, "rm", "-q", "docs/guide.md")
		}},
		{name: "cron value change", want: true, change: cronChange},
		{name: "cron value in a compact sequence", want: true, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/compact.yaml", `"15 4 * * 1"`, `"0 6 * * 1"`)
		}},
		{name: "docs beside a cron value change", want: true, change: func(dir string) {
			writeRepoFile(t, dir, "docs/guide.md", "Guide, revised.\n")
			cronChange(dir)
		}},
		{name: "workflow change beside a cron line", want: false, change: func(dir string) {
			cronChange(dir)
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "runs-on: ubuntu-latest", "runs-on: windows-latest")
		}},
		{name: "docs beside code", want: false, change: func(dir string) {
			writeRepoFile(t, dir, "README.md", "# Example, revised\n")
			writeRepoFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
		}},
		{name: "agent instruction file", want: false, change: func(dir string) {
			writeRepoFile(t, dir, "AGENTS.md", "Agents, revised.\n")
		}},
		{name: "markdown under a hidden directory", want: false, change: func(dir string) {
			writeRepoFile(t, dir, ".github/pull_request_template.md", "Template.\n")
		}},
		{name: "markdown test fixture", want: false, change: func(dir string) {
			writeRepoFile(t, dir, "internal/testdata/case.md", "Fixture.\n")
		}},
		{name: "doc made executable", want: false, change: func(dir string) {
			gitCmd(t, dir, "update-index", "--chmod=+x", "README.md")
		}},
		{name: "cron input of a step", want: false, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "cron: '0 0 * * *'", "cron: '0 1 * * *'")
		}},
		{name: "schedule entry added", want: false, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "    - cron: '0 3 * * *'\n", "    - cron: '0 3 * * *'\n    - cron: '0 9 * * *'\n")
		}},
		{name: "schedule entry removed", want: false, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "  schedule:\n    - cron: '0 3 * * *'\n", "")
		}},
		{name: "unquoted cron value", want: false, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "- cron: '0 3 * * *'", "- cron: 0 3 * * 1")
		}},
		{name: "cron line reindented", want: false, change: func(dir string) {
			replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "    - cron: '0 3 * * *'", "    -  cron: '0 4 * * *'")
		}},
		{name: "new workflow with a schedule", want: false, change: func(dir string) {
			writeRepoFile(t, dir, ".github/workflows/weekly.yml", nightlyWorkflow)
		}},
		{name: "cron value in a workflow outside .github/workflows", want: false,
			base: map[string]string{"ci/nightly.yml": nightlyWorkflow},
			change: func(dir string) {
				replaceInRepoFile(t, dir, "ci/nightly.yml", "- cron: '0 3 * * *'", "- cron: '30 5 * * *'")
			}},
		{name: "no change", want: false, change: func(string) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, base, head := docsAndSchedulesRepo(t, tc.base, tc.change)
			if got := docsAndSchedulesOnly(context.Background(), dir, base, head); got != tc.want {
				t.Fatalf("docsAndSchedulesOnly = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReviewStep_DocsAndSchedulesOnlyCompletesWithoutTheAgent(t *testing.T) {
	dir, base, head := docsAndSchedulesRepo(t, nil, func(dir string) {
		writeRepoFile(t, dir, "README.md", "# Example\n\nMore words.\n")
		replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "- cron: '0 3 * * *'", "- cron: '30 5 * * *'")
	})
	ag := &mockAgent{name: "test"}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 0 {
		t.Fatalf("review agent ran %d times, want none", len(ag.calls))
	}
	if outcome.NeedsApproval || outcome.Skipped {
		t.Fatalf("outcome = %#v, want a completed review", outcome)
	}
	if outcome.ReviewApprovedHeadSHA != head {
		t.Fatalf("review-approved head = %q, want %q", outcome.ReviewApprovedHeadSHA, head)
	}
	if outcome.SkipReason != docsAndSchedulesOnlyReason {
		t.Fatalf("skip reason = %q, want %q", outcome.SkipReason, docsAndSchedulesOnlyReason)
	}
	findings, err := types.ParseFindingsJSON(outcome.Findings)
	if err != nil || len(findings.Items) != 0 {
		t.Fatalf("findings = %q (%v), want none", outcome.Findings, err)
	}
}

func TestReviewStep_WorkflowChangeBesideACronLineRunsTheAgent(t *testing.T) {
	dir, base, head := docsAndSchedulesRepo(t, nil, func(dir string) {
		replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "- cron: '0 3 * * *'", "- cron: '30 5 * * *'")
		replaceInRepoFile(t, dir, ".github/workflows/nightly.yml", "runs-on: ubuntu-latest", "runs-on: windows-latest")
	})
	ag := &mockAgent{name: "test", runFn: func(context.Context, agent.RunOpts) (*agent.Result, error) {
		findings := cleanReviewFindings()
		findings.ReviewedPaths = []string{".github/workflows/nightly.yml"}
		j, _ := json.Marshal(findings)
		return &agent.Result{Output: j}, nil
	}}
	sctx := newTestContextWithDBRecords(t, ag, dir, base, head, config.Commands{})

	outcome, err := (&ReviewStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ag.calls) != 1 {
		t.Fatalf("review agent ran %d times, want once", len(ag.calls))
	}
	if outcome.SkipReason != "" {
		t.Fatalf("skip reason = %q on a reviewed change", outcome.SkipReason)
	}
}
