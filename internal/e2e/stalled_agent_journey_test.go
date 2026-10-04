//go:build e2e

package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestSilentAgentIsReportedAsStalledWhileTheRunWaits drives a review agent that
// starts and then prints nothing, through the stock `axi` surfaces, and
// requires every one of them to name the stall before the budget ends it.
func TestSilentAgentIsReportedAsStalledWhileTheRunWaits(t *testing.T) {
	h := NewHarness(t, SetupOpts{
		Agent:    "claude",
		Scenario: silentAgentScenario(t),
		GlobalConfigExtra: strings.Join([]string{
			`step_quiet_warning: "1s"`,
			`agent_timeout: "15s"`,
			`review_agent_timeout: "15s"`,
		}, "\n"),
	})

	h.CommitChange("init-stall", "seed.txt", "seed\n", "seed stall init")
	initWorktree := h.AddWorktree("init-stall")
	if out, err := h.RunInDir(initWorktree, "init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	h.CommitChange("feature/stalled-agent", "feature.txt", "value\n", "add feature")
	operator := h.AddWorktree("feature/stalled-agent")

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := h.RunInDir(operator, "axi", "run", "--intent", "validate the feature while the agent is wedged")
		done <- result{out, err}
	}()

	var stalledStatus string
	deadline := time.Now().Add(45 * time.Second)
	for stalledStatus == "" {
		select {
		case r := <-done:
			t.Fatalf("run ended before axi status reported the stall:\n%s", r.out)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("axi status never reported the stalled review agent")
		}
		if out, err := h.RunInDir(operator, "axi", "status"); err == nil && strings.Contains(out, "stalled_agents[1]") {
			stalledStatus = out
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Logf("stock axi status surface during the stall:\n%s", stalledStatus)
	if !strings.Contains(stalledStatus, "review,claude,") {
		t.Fatalf("axi status did not name the stalled review agent:\n%s", stalledStatus)
	}
	if !strings.Contains(stalledStatus, "claude has produced no output in review for") {
		t.Fatalf("axi status help did not explain the stall:\n%s", stalledStatus)
	}

	r := <-done
	t.Logf("stock axi run surface:\n%s", r.out)
	if !strings.Contains(r.out, "review: claude has produced no output for") {
		t.Fatalf("axi run did not announce the stall while it waited:\n%s", r.out)
	}

	logs, err := h.RunInDir(operator, "axi", "logs", "--step", "review")
	if err != nil {
		t.Fatalf("axi logs: %v\n%s", err, logs)
	}
	if !strings.Contains(logs, "claude has produced no output for") {
		t.Fatalf("review step log has no stall notice:\n%s", logs)
	}
	if status, _ := h.RunInDir(operator, "axi", "status"); strings.Contains(status, "stalled_agents") {
		t.Fatalf("a finished run still reports a stalled agent:\n%s", status)
	}
}
