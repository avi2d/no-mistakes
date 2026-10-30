package daemon

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/pipeline"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const fakeRunToken = "sk-ant-oat01-FAKE-run-secret-value"

type reviewerTraceProbeStep struct{}

func (s *reviewerTraceProbeStep) Name() types.StepName { return types.StepReview }

func (s *reviewerTraceProbeStep) Execute(sctx *pipeline.StepContext) (*pipeline.StepOutcome, error) {
	result, err := sctx.RunAgentContext(sctx.Ctx, agent.RunOpts{Purpose: "review", Prompt: "review", CWD: sctx.WorkDir})
	if err != nil {
		return nil, err
	}
	sctx.ReviewerAgent = result.AgentIdentity
	if sctx.ReviewerAgent == "" {
		sctx.ReviewerAgent = result.Provider
	}
	sctx.ReviewerChainTrace = result.ReviewerChainTrace
	return &pipeline.StepOutcome{}, nil
}

func writeRunTokenCapturingPi(t *testing.T, dir, capturePath string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the capturing fake reviewer is a POSIX shell script")
	}
	const response = `{"type":"agent_end","messages":[{"role":"assistant","content":[{"type":"text","text":"ok"}]}]}`
	bin := filepath.Join(dir, "pi")
	script := "#!/bin/sh\nprintf '%s|%s\\n' \"$*\" \"$RUN_TOKEN\" >> " + shellQuoteForTest(capturePath) + "\ncat >/dev/null\nprintf '%s\\n' '" + response + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func runEnvChainConfig(piBin string) string {
	return "agent: pi\n" +
		"agent_path_override:\n  pi: " + piBin + "\n" +
		"reviewer_chain:\n" +
		"  - name: run-token\n    agent: pi\n    model: claude-bridge/claude-haiku-5-5\n    run_env: [RUN_TOKEN]\n" +
		"  - agent: pi\n    model: fallback-model\n"
}

func filesContaining(t *testing.T, root string, needle []byte) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, needle) {
			hits = append(hits, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func pushAndWaitForReview(t *testing.T, stageToken bool) (capture string, trace string, nmHome string) {
	t.Helper()
	capture = filepath.Join(t.TempDir(), "reviewer.log")
	piBin := writeRunTokenCapturingPi(t, t.TempDir(), capture)
	p, d := startTestDaemonWithSteps(t, func() []pipeline.Step {
		return []pipeline.Step{&reviewerTraceProbeStep{}}
	})
	if err := os.WriteFile(p.ConfigFile(), []byte(runEnvChainConfig(piBin)), 0o644); err != nil {
		t.Fatal(err)
	}
	const repoID = "run-env-repo"
	_, headSHA := setupTestGitRepo(t, p, d, repoID)
	client, err := ipc.Dial(p.Socket())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if stageToken {
		if err := client.Call(ipc.MethodStageRunEnv, &ipc.StageRunEnvParams{
			RepoID: repoID, Branch: "main", HeadSHA: headSHA, Vars: map[string]string{"RUN_TOKEN": fakeRunToken},
		}, nil); err != nil {
			t.Fatal(err)
		}
	}
	var result ipc.PushReceivedResult
	if err := client.Call(ipc.MethodPushReceived, &ipc.PushReceivedParams{
		Gate: p.RepoDir(repoID), Ref: "refs/heads/main", Old: "0000000000000000000000000000000000000000", New: headSHA,
	}, &result); err != nil {
		t.Fatal(err)
	}
	run := waitForRunTerminalState(t, d, result.RunID)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %q, error = %v", run.Status, run.Error)
	}
	steps, err := d.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range steps {
		if step.StepName != types.StepReview {
			continue
		}
		rounds, err := d.GetRoundsByStep(step.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(rounds) == 0 || rounds[len(rounds)-1].ReviewerChainTrace == nil {
			t.Fatalf("review recorded no reviewer chain trace: %+v", rounds)
		}
		trace = *rounds[len(rounds)-1].ReviewerChainTrace
	}
	return capture, trace, p.Root()
}

func TestRunEnvFileTokenReachesOnlyTheRunTokenReviewerAndNeverTheDaemonsRecords(t *testing.T) {
	capture, rawTrace, nmHome := pushAndWaitForReview(t, true)
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(got))
	if !strings.Contains(line, "--model claude-bridge/claude-haiku-5-5") || !strings.HasSuffix(line, "|"+fakeRunToken) || strings.Contains(line, "fallback-model") {
		t.Fatalf("reviewer invocation = %q, want the run-token entry with the token in its environment", line)
	}
	trace, ok := agent.ParseReviewerTrace(rawTrace)
	if !ok || trace.Selected != 0 || trace.Name != "run-token" || len(trace.Skipped) != 0 {
		t.Fatalf("trace = %q, want the run-token entry serving with nothing skipped", rawTrace)
	}
	if hits := filesContaining(t, nmHome, []byte(`"name":"run-token"`)); len(hits) == 0 {
		t.Fatal("the search never reached the database that recorded this run's trace")
	}
	if hits := filesContaining(t, nmHome, []byte(fakeRunToken)); len(hits) > 0 {
		t.Fatalf("the run token reached the daemon's home: %v", hits)
	}
}

func TestRunWithoutRunEnvFileSkipsTheRunTokenEntryInsteadOfLaunchingItTokenless(t *testing.T) {
	capture, rawTrace, _ := pushAndWaitForReview(t, false)
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(got))
	if strings.Contains(line, "claude-haiku-5-5") || !strings.Contains(line, "--model fallback-model") || !strings.HasSuffix(line, "|") {
		t.Fatalf("reviewer invocation = %q, want only the fallback entry with no token", line)
	}
	trace, ok := agent.ParseReviewerTrace(rawTrace)
	if !ok || trace.Selected != 1 || trace.Name != "" || len(trace.Skipped) != 1 || trace.Skipped[0].Reason != "run carries no RUN_TOKEN" {
		t.Fatalf("trace = %q, want reviewer_chain[1] serving after skipping the run-token entry", rawTrace)
	}
}

func TestStagedRunEnvIsClaimedOnceByTheExactBranchAndHead(t *testing.T) {
	var staged stagedRunEnv
	key := runEnvKey{repoID: "repo", branch: "main", headSHA: "abc"}
	if err := staged.stage(key, map[string]string{"RUN_TOKEN": fakeRunToken}); err != nil {
		t.Fatal(err)
	}
	if vars := staged.claim(runEnvKey{repoID: "repo", branch: "other", headSHA: "abc"}); vars != nil {
		t.Fatalf("another branch claimed the variables: %v", vars)
	}
	if vars := staged.claim(runEnvKey{repoID: "repo", branch: "main", headSHA: "def"}); vars != nil {
		t.Fatalf("another head claimed the variables: %v", vars)
	}
	if vars := staged.claim(key); vars["RUN_TOKEN"] != fakeRunToken {
		t.Fatalf("claim = %v", vars)
	}
	if vars := staged.claim(key); vars != nil {
		t.Fatalf("a second run claimed the same variables: %v", vars)
	}
}

func TestStagedRunEnvReleaseDropsUnclaimedVariables(t *testing.T) {
	var staged stagedRunEnv
	key := runEnvKey{repoID: "repo", branch: "main", headSHA: "abc"}
	if err := staged.stage(key, map[string]string{"RUN_TOKEN": fakeRunToken}); err != nil {
		t.Fatal(err)
	}
	staged.release(key)
	if vars := staged.claim(key); vars != nil {
		t.Fatalf("released variables were still claimable: %v", vars)
	}
}

func TestStageRunEnvRefusalNeverQuotesTheValue(t *testing.T) {
	var staged stagedRunEnv
	err := staged.stage(runEnvKey{repoID: "repo", branch: "main", headSHA: "abc"}, map[string]string{"bad name": fakeRunToken, "OK": fakeRunToken + "\n"})
	if err == nil {
		t.Fatal("stage accepted an invalid variable")
	}
	if strings.Contains(err.Error(), fakeRunToken) {
		t.Fatalf("refusal quoted the value: %v", err)
	}
}

func TestTakeRunEnvNamesMissingVariablesNotValues(t *testing.T) {
	taken, reason := takeRunEnv([]string{"RUN_TOKEN", "OTHER"}, map[string]string{"RUN_TOKEN": fakeRunToken})
	if taken != nil || reason != "run carries no OTHER" {
		t.Fatalf("taken=%v reason=%q", taken, reason)
	}
	taken, reason = takeRunEnv([]string{"RUN_TOKEN"}, map[string]string{"RUN_TOKEN": fakeRunToken, "UNNAMED": "x"})
	if reason != "" || len(taken) != 1 || taken["RUN_TOKEN"] != fakeRunToken {
		t.Fatalf("taken=%v reason=%q, want only the named variable", taken, reason)
	}
}
