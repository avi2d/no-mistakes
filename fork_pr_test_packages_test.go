package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// The fixture module's import graph: a imports b, d's test imports b, c stands alone.
func newForkPRTestFixture(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fork PR test workflow runs on ubuntu-latest")
	}
	dir := t.TempDir()
	fixtureGit(t, dir, "init", "--quiet", "--initial-branch=main")
	for name, content := range map[string]string{
		"go.mod":      "module example.com/fixture\n\ngo 1.21\n",
		"README.md":   "fixture\n",
		"a/a.go":      "package a\n\nimport \"example.com/fixture/b\"\n\nfunc A() int { return b.B() }\n",
		"a/a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestA(t *testing.T) {}\n",
		"b/b.go":      "package b\n\nfunc B() int { return 1 }\n",
		"b/b_test.go": "package b\n\nimport \"testing\"\n\nfunc TestB(t *testing.T) {}\n",
		"c/c.go":      "package c\n\nfunc C() int { return 1 }\n",
		"c/helper.go": "package c\n\nfunc helper() int {\n\treturn 1 + 2 + 3 + 4 + 5 + 6 + 7 + 8 + 9\n}\n",
		"c/c_test.go": "package c\n\nimport \"testing\"\n\nfunc TestC(t *testing.T) {}\n",
		"d/d.go":      "package d\n",
		"d/d_test.go": "package d\n\nimport (\n\t\"testing\"\n\n\t\"example.com/fixture/b\"\n)\n\nfunc TestD(t *testing.T) { _ = b.B() }\n",
	} {
		writeForkPRFixtureFile(t, dir, name, content)
	}
	fixtureGit(t, dir, "add", "-A")
	fixtureGit(t, dir, "commit", "--quiet", "-m", "base")
	return dir
}

func writeForkPRFixtureFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitForkPRFixtureBranch(t *testing.T, dir, branch string, change func()) string {
	t.Helper()
	fixtureGit(t, dir, "checkout", "--quiet", "-b", branch, "main")
	change()
	fixtureGit(t, dir, "add", "-A")
	fixtureGit(t, dir, "commit", "--quiet", "-m", branch)
	head := fixtureGit(t, dir, "rev-parse", "HEAD")
	fixtureGit(t, dir, "checkout", "--quiet", "main")
	return head
}

func runForkPRTestPackages(t *testing.T, dir, base, head string) []string {
	t.Helper()
	script, err := filepath.Abs("scripts/fork-pr-test-packages.sh")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", script, base, head)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		t.Fatalf("fork-pr-test-packages.sh: %v\n%s", err, stderr)
	}
	return strings.Fields(string(out))
}

func TestForkPRTestPackages_SelectsWhatTheDiffReaches(t *testing.T) {
	dir := newForkPRTestFixture(t)
	edit := func(name, content string) func() {
		return func() { writeForkPRFixtureFile(t, dir, name, content) }
	}
	for _, tc := range []struct {
		name   string
		change func()
		want   []string
	}{
		{
			name:   "an imported package's change selects its importers, not only itself",
			change: edit("b/b.go", "package b\n\nfunc B() int { return 2 }\n"),
			want:   []string{"example.com/fixture/a", "example.com/fixture/b", "example.com/fixture/d"},
		},
		{
			name:   "a test file's change selects only its own package, not the module",
			change: edit("c/c_test.go", "package c\n\nimport \"testing\"\n\nfunc TestC2(t *testing.T) {}\n"),
			want:   []string{"example.com/fixture/c"},
		},
		{
			name:   "a testdata file's change selects its package, not nothing",
			change: edit("b/testdata/input.txt", "data\n"),
			want:   []string{"example.com/fixture/a", "example.com/fixture/b", "example.com/fixture/d"},
		},
		{
			name: "a file moved between packages selects the package it left, not only the new one",
			change: func() {
				fixtureGit(t, dir, "mv", "c/helper.go", "b/helper.go")
				content, err := os.ReadFile(filepath.Join(dir, "b/helper.go"))
				if err != nil {
					t.Fatal(err)
				}
				edit("b/helper.go", strings.Replace(string(content), "package c", "package b", 1))()
			},
			want: []string{"example.com/fixture/a", "example.com/fixture/b", "example.com/fixture/c", "example.com/fixture/d"},
		},
		{
			name:   "a go.mod change selects the whole module, not the root package",
			change: edit("go.mod", "module example.com/fixture\n\ngo 1.22\n"),
			want:   []string{"./..."},
		},
		{
			name:   "a go.sum change selects the whole module, not nothing",
			change: edit("go.sum", ""),
			want:   []string{"./..."},
		},
		{
			name:   "a change outside every package selects nothing, not the module",
			change: edit("README.md", "changed\n"),
			want:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			head := commitForkPRFixtureBranch(t, dir, "case-"+strings.ReplaceAll(tc.name, " ", "-"), tc.change)
			got := runForkPRTestPackages(t, dir, "main", head)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("selected %q, want %q", got, tc.want)
			}
		})
	}
}

func TestForkPRTestPackages_DiffsFromTheMergeBaseNotTheBaseTip(t *testing.T) {
	dir := newForkPRTestFixture(t)
	head := commitForkPRFixtureBranch(t, dir, "pr", func() {
		writeForkPRFixtureFile(t, dir, "a/a_test.go", "package a\n\nimport \"testing\"\n\nfunc TestA2(t *testing.T) {}\n")
	})
	writeForkPRFixtureFile(t, dir, "c/c.go", "package c\n\nfunc C() int { return 2 }\n")
	fixtureGit(t, dir, "commit", "--quiet", "-am", "main moves on")

	got := runForkPRTestPackages(t, dir, "main", head)
	if want := []string{"example.com/fixture/a"}; !slices.Equal(got, want) {
		t.Fatalf("selected %q, want %q", got, want)
	}
}
