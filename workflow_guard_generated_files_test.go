package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestGuardGeneratedFilesWorkflowCoversReleasePleaseArtifacts pins the list of
// guarded paths. If release-please starts managing more files, add them here
// and to the workflow together.
func TestGuardGeneratedFilesWorkflowCoversReleasePleaseArtifacts(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/guard-generated-files.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	content := string(data)

	guarded := []string{
		"CHANGELOG.md",
		".release-please-manifest.json",
	}
	for _, path := range guarded {
		if !strings.Contains(content, path) {
			t.Errorf("workflow must guard %q", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Errorf("guarded path %q not present in repo: %v", path, err)
		}
	}
}

// TestGuardGeneratedFilesWorkflowExemptsReleasePlease ensures the release
// pipeline's own PR (which legitimately modifies the generated files) is
// always allowed through.
func TestGuardGeneratedFilesWorkflowExemptsReleasePlease(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/guard-generated-files.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	content := string(data)

	for _, login := range []string{"github-actions[bot]", "release-please[bot]"} {
		needle := "github.event.pull_request.user.login != '" + login + "'"
		if !strings.Contains(content, needle) {
			t.Errorf("workflow must exempt %q via %q", login, needle)
		}
	}
}

// TestGuardGeneratedFilesWorkflowUsesGitDiffWithFullHistory pins the
// git-based file-diff approach. Using the API would add a permission surface
// (pull-requests: read), rate-limit exposure, and pagination concerns; the
// git three-dot diff matches exactly what GitHub shows in "Files changed".
func TestGuardGeneratedFilesWorkflowUsesGitDiffWithFullHistory(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/guard-generated-files.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "actions/checkout") {
		t.Errorf("workflow must check out the repo to run git diff locally")
	}
	if !strings.Contains(content, "fetch-depth: 0") {
		t.Errorf("workflow must use fetch-depth: 0 so merge-base for base...head is available")
	}
	if !strings.Contains(content, `git diff --name-only "${BASE_SHA}...${HEAD_SHA}"`) {
		t.Errorf("workflow must use 'git diff --name-only base...head' (three-dot) for PR file list")
	}
	if strings.Contains(content, "gh api") {
		t.Errorf("workflow must not fall back to the GitHub API for file listing")
	}
	if strings.Contains(content, "pull-requests:") {
		t.Errorf("workflow must not request pull-requests permission once switched to git diff")
	}
}

// TestGuardGeneratedFilesWorkflowTriggersOnPushedCommits ensures the guard
// re-runs when new commits are pushed to a PR (the synchronize event), so a
// contributor cannot open a clean PR then push a commit that edits CHANGELOG.md.
func TestGuardGeneratedFilesWorkflowTriggersOnPushedCommits(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/guard-generated-files.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	content := string(data)

	for _, typ := range []string{"opened", "synchronize", "reopened"} {
		if !strings.Contains(content, typ) {
			t.Errorf("workflow must trigger on pull_request type %q", typ)
		}
	}
}

func TestGuardGeneratedFilesWorkflowAllowsAForkToMergeUpstreamReleaseOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the guard step runs on ubuntu-latest")
	}
	upstream := t.TempDir()
	fixtureGit(t, upstream, "init", "--quiet", "--initial-branch=main")
	writeFixtureFile(t, upstream, "CHANGELOG.md", "## 1.0.0\n")
	writeFixtureFile(t, upstream, ".release-please-manifest.json", `{".": "1.0.0"}`+"\n")
	writeFixtureFile(t, upstream, "main.go", "package main\n")
	fixtureGit(t, upstream, "add", "-A")
	fixtureGit(t, upstream, "commit", "--quiet", "-m", "chore(main): release 1.0.0")

	fork := t.TempDir()
	fixtureGit(t, fork, "clone", "--quiet", upstream, ".")
	writeFixtureFile(t, fork, "fork.go", "package main\n")
	fixtureGit(t, fork, "add", "-A")
	fixtureGit(t, fork, "commit", "--quiet", "-m", "fix: a fork-only fix")
	forkMain := fixtureGit(t, fork, "rev-parse", "HEAD")

	writeFixtureFile(t, upstream, "CHANGELOG.md", "## 1.1.0\n\n## 1.0.0\n")
	writeFixtureFile(t, upstream, ".release-please-manifest.json", `{".": "1.1.0"}`+"\n")
	fixtureGit(t, upstream, "commit", "--quiet", "-am", "chore(main): release 1.1.0")

	fixtureGit(t, fork, "fetch", "--quiet", upstream, "main")
	fixtureGit(t, fork, "merge", "--quiet", "--no-ff", "--no-edit", "FETCH_HEAD")
	upstreamMerge := fixtureGit(t, fork, "rev-parse", "HEAD")

	writeFixtureFile(t, fork, "CHANGELOG.md", "## 1.1.0\n\nhand-written note\n\n## 1.0.0\n")
	fixtureGit(t, fork, "commit", "--quiet", "--amend", "--no-edit", "-a")
	editedMerge := fixtureGit(t, fork, "rev-parse", "HEAD")

	fixtureGit(t, fork, "checkout", "--quiet", "--detach", forkMain)
	writeFixtureFile(t, fork, "CHANGELOG.md", "## 1.0.0\n\nhand-written note\n")
	fixtureGit(t, fork, "commit", "--quiet", "-am", "docs: hand-edit the changelog")
	handEdit := fixtureGit(t, fork, "rev-parse", "HEAD")

	for _, tc := range []struct {
		name        string
		head        string
		upstreamURL string
		wantPass    bool
	}{
		{name: "fork merging an upstream release", head: upstreamMerge, upstreamURL: upstream, wantPass: true},
		{name: "fork hand-editing inside the merge", head: editedMerge, upstreamURL: upstream},
		{name: "fork hand-editing the changelog", head: handEdit, upstreamURL: upstream},
		{name: "upstream itself", head: upstreamMerge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passed, out := runGuardGeneratedFilesStep(t, fork, forkMain, tc.head, tc.upstreamURL)
			if passed != tc.wantPass {
				t.Fatalf("guard passed = %v, want %v\n%s", passed, tc.wantPass, out)
			}
		})
	}
}

func runGuardGeneratedFilesStep(t *testing.T, dir, baseSHA, headSHA, upstreamURL string) (bool, string) {
	t.Helper()
	data, err := os.ReadFile(".github/workflows/guard-generated-files.yml")
	if err != nil {
		t.Fatalf("read workflow: %v", err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Env  map[string]string `yaml:"env"`
				Run  string            `yaml:"run"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatalf("parse workflow: %v", err)
	}
	for _, step := range workflow.Jobs["check"].Steps {
		if step.Name != "Check PR does not modify release-please-generated files" {
			continue
		}
		if !strings.Contains(step.Env["UPSTREAM_URL"], "github.repository != 'kunchenguid/no-mistakes'") {
			t.Fatalf("UPSTREAM_URL must be set only on forks, got %q", step.Env["UPSTREAM_URL"])
		}
		cmd := exec.Command("bash", "-c", step.Run)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "BASE_SHA="+baseSHA, "HEAD_SHA="+headSHA, "UPSTREAM_URL="+upstreamURL)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		err := cmd.Run()
		if _, failed := err.(*exec.ExitError); err != nil && !failed {
			t.Fatalf("run guard step: %v", err)
		}
		return err == nil, out.String()
	}
	t.Fatal("guard step not found in workflow")
	return false, ""
}

func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
		"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFixtureFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
