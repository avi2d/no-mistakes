//go:build e2e

package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const (
	runEnvTokenName = "NM_E2E_RUN_TOKEN"
	runEnvToken     = "sk-ant-oat01-PLACEHOLDER-e2e-run-secret"
)

type runEnvStatus struct {
	Run struct {
		ID            string `toon:"id"`
		ReviewerEntry *struct {
			Index *int   `toon:"index"`
			Name  string `toon:"name"`
		} `toon:"reviewer_entry"`
	} `toon:"run"`
}

func runEnvHarness(t *testing.T) *Harness {
	t.Helper()
	t.Setenv("FAKEAGENT_RECORD_ENV", runEnvTokenName)
	h := NewHarness(t, SetupOpts{
		Agent:    "claude",
		Scenario: cleanReviewScenario(t),
		GlobalConfigExtra: "reviewer_chain:\n" +
			"  - name: run-token\n    agent: claude\n    model: run-token-e2e\n    run_env: [" + runEnvTokenName + "]\n" +
			"  - agent: claude\n    model: fallback-e2e\n",
	})
	h.CommitChange("init-run-env", "seed.txt", "seed\n", "seed for run env")
	if out, err := h.RunInDir(h.AddWorktree("init-run-env"), "init"); err != nil {
		t.Fatalf("nm init: %v\n%s", err, out)
	}
	return h
}

func driveRunEnvBranch(t *testing.T, h *Harness, branch string, extraArgs ...string) (worktree string, status runEnvStatus, transcript string) {
	t.Helper()
	h.CommitChange(branch, "feature.txt", branch+"\n", "add "+branch)
	worktree = h.AddWorktree(branch)
	args := append([]string{"axi", "run", "--yes", "--intent", "exercise run-scoped reviewer variables"}, extraArgs...)
	runOut, err := h.RunInDir(worktree, args...)
	if err != nil || !strings.Contains(runOut, "outcome: passed") {
		t.Fatalf("axi run: %v\n%s", err, runOut)
	}
	run := h.WaitForRun(branch, 60*time.Second)
	if run.Status != types.RunCompleted {
		t.Fatalf("run status = %s, want completed", run.Status)
	}
	statusOut, err := h.RunInDir(worktree, "axi", "status", "--run", run.ID)
	if err != nil {
		t.Fatalf("axi status: %v\n%s", err, statusOut)
	}
	if err := toon.UnmarshalString(statusOut, &status); err != nil {
		t.Fatalf("decode axi status: %v\n%s", err, statusOut)
	}
	logsOut, err := h.RunInDir(worktree, "axi", "logs", "--run", run.ID, "--step", "review")
	if err != nil {
		t.Fatalf("axi logs: %v\n%s", err, logsOut)
	}
	return worktree, status, runOut + statusOut + logsOut
}

func reviewTokenDigests(h *Harness) (withToken, withoutToken int) {
	sum := sha256.Sum256([]byte(runEnvToken))
	want := hex.EncodeToString(sum[:])
	for _, inv := range h.AgentInvocations() {
		got, received := inv.EnvDigests[runEnvTokenName]
		if received && got != want {
			h.t.Fatalf("an agent received %s with a different value", runEnvTokenName)
		}
		if received && !strings.Contains(inv.Prompt, reviewTurnMarker) {
			h.t.Fatalf("a non-review turn received %s: %v", runEnvTokenName, inv.Args)
		}
		if !strings.Contains(inv.Prompt, reviewTurnMarker) {
			continue
		}
		if received {
			withToken++
		} else {
			withoutToken++
		}
	}
	return withToken, withoutToken
}

func treeFilesContaining(t *testing.T, root string, needle []byte) []string {
	t.Helper()
	var hits []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || !entry.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
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

func TestRunEnvFileServesOnlyTheNamedReviewerAndNeverReachesStatusLogsOrNMHome(t *testing.T) {
	h := runEnvHarness(t)
	envFile := filepath.Join(t.TempDir(), "run.env")
	if err := os.WriteFile(envFile, []byte(runEnvTokenName+"="+runEnvToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, status, transcript := driveRunEnvBranch(t, h, "feature/run-env", "--run-env-file", envFile)

	entry := status.Run.ReviewerEntry
	if entry == nil || entry.Index == nil || *entry.Index != 0 || entry.Name != "run-token" {
		t.Fatalf("reviewer_entry = %+v, want index 0 named run-token", entry)
	}
	if withToken, withoutToken := reviewTokenDigests(h); withToken == 0 || withoutToken != 0 {
		t.Fatalf("review turns with the token = %d, without = %d; want every review turn to carry it", withToken, withoutToken)
	}
	if strings.Contains(transcript, runEnvToken) {
		t.Fatalf("axi run, status, or logs output printed the run token:\n%s", transcript)
	}
	if hits := treeFilesContaining(t, h.NMHome, []byte("feature/run-env")); len(hits) == 0 {
		t.Fatal("the NM_HOME walk found not even the run's branch, so it cannot vouch for the token's absence")
	}
	if hits := treeFilesContaining(t, h.NMHome, []byte(runEnvToken)); len(hits) > 0 {
		t.Fatalf("run token persisted under NM_HOME: %v", hits)
	}
	if hits := treeFilesContaining(t, filepath.Dir(h.AgentLog), []byte(runEnvToken)); len(hits) > 0 {
		t.Fatalf("run token reached the test tree outside its env file: %v", hits)
	}
}

func TestRunWithoutRunEnvFileFallsBackPastTheRunTokenEntryOnRealBinary(t *testing.T) {
	h := runEnvHarness(t)

	_, status, _ := driveRunEnvBranch(t, h, "feature/no-run-env")

	entry := status.Run.ReviewerEntry
	if entry == nil || entry.Index == nil || *entry.Index != 1 || entry.Name != "" {
		t.Fatalf("reviewer_entry = %+v, want the unnamed fallback at index 1", entry)
	}
	if withToken, withoutToken := reviewTokenDigests(h); withToken != 0 || withoutToken == 0 {
		t.Fatalf("review turns with the token = %d, without = %d; want none to carry it", withToken, withoutToken)
	}
}
