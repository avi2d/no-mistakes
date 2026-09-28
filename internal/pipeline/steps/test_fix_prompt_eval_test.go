package steps

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
)

const evalFixtureCalcGo = `package app

func Add(a, b int) int {
	return a + b
}
`

const evalFixtureCalcTestGo = `package app

import "testing"

func TestAddReturnsTheSum(t *testing.T) {
	if got := Add(1, 1); got != 3 {
		t.Errorf("Add(1, 1) = %d, want 3", got)
	}
}
`

const evalFixtureGoMod = `module evalfixture

go 1.25.0
`

const supersededTestFixInstruction = `Fix the failing tests in this repository. Reproduce the specific failure, identify the root cause, and fix either the tests or the code so that failure passes.`

type contradictingTestStub struct {
	dir        string
	fixPrompt  string
	fixSummary string
	editedTest bool
}

func (s *contradictingTestStub) fixTurn(prompt string) []byte {
	s.fixPrompt = prompt
	if strings.Contains(prompt, "If a test contradicts the stated intent, stop") {
		s.fixSummary = "test contradicts the stated intent; left untouched"
		return []byte(`{"summary":"test contradicts the stated intent; left untouched"}`)
	}
	if strings.Contains(prompt, "fix either the tests or the code") {
		raw, err := os.ReadFile(filepath.Join(s.dir, "calc_test.go"))
		if err != nil {
			return []byte(`{"summary":"could not read test"}`)
		}
		fixed := strings.Replace(string(raw), "got != 3", "got != 2", 1)
		fixed = strings.Replace(fixed, "want 3", "want 2", 1)
		if err := os.WriteFile(filepath.Join(s.dir, "calc_test.go"), []byte(fixed), 0o644); err != nil {
			return []byte(`{"summary":"could not write test"}`)
		}
		s.editedTest = true
		s.fixSummary = "fix contradicting test expectation"
		return []byte(`{"summary":"fix contradicting test expectation"}`)
	}
	s.fixSummary = "no recognized instruction"
	return []byte(`{"summary":"no recognized instruction"}`)
}

func (s *contradictingTestStub) runFn(_ context.Context, opts agent.RunOpts) (*agent.Result, error) {
	if strings.Contains(opts.Prompt, `Return JSON with a single "summary" field`) {
		return &agent.Result{Output: s.fixTurn(opts.Prompt)}, nil
	}
	return &agent.Result{Output: []byte(`{"summary":"contradicting test left untouched","findings":[],"tested":["go test ./..."],"testing_summary":"the covering test contradicts the stated intent and was left untouched","artifacts":[],"scenarios":[{"name":"Add returns the sum","result":"untested","live":false,"evidence":"","reason":"the covering test asserts 3 while the stated intent requires 2; left untouched per fix policy"}],"verdict":"inconclusive"}`)}, nil
}

func setupContradictingFixture(t *testing.T) (dir, baseSHA, headSHA string) {
	t.Helper()
	dir, baseSHA, _ = setupGitRepo(t)
	for name, content := range map[string]string{
		"calc.go":      evalFixtureCalcGo,
		"calc_test.go": evalFixtureCalcTestGo,
		"go.mod":       evalFixtureGoMod,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, dir, "add", "-A")
	gitCmd(t, dir, "commit", "-m", "add contradicting fixture")
	headSHA = gitCmd(t, dir, "rev-parse", "HEAD")
	gitCmd(t, dir, "checkout", "--detach", headSHA)
	if out, err := goTestIn(dir); err == nil {
		t.Fatalf("fixture test passes before any fix, want it failing: %s", out)
	} else if !strings.Contains(string(out), "want 3") {
		t.Fatalf("fixture failure is not the contradicting assertion: %s", out)
	}
	return dir, baseSHA, headSHA
}

func goTestIn(dir string) (string, error) {
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestTestFixPromptEval_ContradictingTest(t *testing.T) {
	t.Parallel()
	if strings.Contains(supersededTestFixInstruction, "contradicts the stated intent, stop") {
		t.Fatal("baseline instruction must predate the stop rule")
	}

	dirA, _, _ := setupContradictingFixture(t)
	stubA := &contradictingTestStub{dir: dirA}
	stubA.fixTurn(supersededTestFixInstruction)
	if !stubA.editedTest {
		t.Fatal("old instruction did not edit the contradicting test")
	}
	if out, err := goTestIn(dirA); err != nil {
		t.Fatalf("edited fixture still fails: %s", out)
	}

	dirB, baseSHA, headSHA := setupContradictingFixture(t)
	stubB := &contradictingTestStub{dir: dirB}
	ag := &mockAgent{name: "test", runFn: stubB.runFn}
	sctx := newTestContextWithDBRecords(t, ag, dirB, baseSHA, headSHA, config.Commands{Test: "go test ./..."})
	sctx.UserIntent = "Add returns the sum of its arguments"
	sctx.Fixing = true
	sctx.PreviousFindings = `{"findings":[{"id":"test-1","severity":"error","description":"TestAddReturnsTheSum failed","action":"auto-fix"}],"summary":"FAIL: TestAddReturnsTheSum"}`

	before, err := os.ReadFile(filepath.Join(dirB, "calc_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := (&TestStep{}).Execute(sctx)
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dirB, "calc_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("new prompt edited the contradicting test")
	}
	if !strings.Contains(stubB.fixSummary, "contradicts the stated intent") {
		t.Fatalf("fix turn did not report the contradiction, summary = %q", stubB.fixSummary)
	}
	if !strings.Contains(stubB.fixPrompt, "If a test contradicts the stated intent, stop") {
		t.Fatalf("executed fix prompt lacks the stop rule:\n%s", stubB.fixPrompt)
	}
	if outcome.ExitCode == 0 {
		t.Fatal("expected the genuinely failing baseline to stay red")
	}
}
