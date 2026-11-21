//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
	toon "github.com/toon-format/toon-go"
)

func TestReviewGateKeepsQuestionActionsAndNeverDefaultsToNone(t *testing.T) {
	var questions, findings strings.Builder
	want := map[string]string{}
	for _, tc := range []struct{ id, input, recorded string }{
		{"delete", `,"question_action":"delete"`, "delete"},
		{"merge", `,"question_action":"merge"`, "merge"},
		{"release", `,"question_action":"release"`, "release"},
		{"login", `,"question_action":"login"`, "login"},
		{"push", `,"question_action":"push"`, "push"},
		{"none", `,"question_action":"none"`, "none"},
		{"missing", "", "unknown"},
		{"invalid", `,"question_action":"deploy"`, "unknown"},
		{"wrong-type", `,"question_action":true`, "unknown"},
	} {
		fmt.Fprintf(&findings, "        - {\"id\":\"finding-%s\",\"severity\":\"warning\",\"description\":\"Which option?\",\"action\":\"ask-user\",\"review_scope\":\"source\"%s}\n", tc.id, tc.input)
		fmt.Fprintf(&questions, "      - '{\"id\":\"%s\",\"question\":\"Which option?\",\"options\":[\"one\",\"two\"]%s}'\n", tc.id, strings.Replace(tc.input, "question_action", "action", 1))
		want["finding-"+tc.id] = tc.recorded
		want["question-"+tc.id] = tc.recorded
	}
	scenario := filepath.Join(t.TempDir(), "question-actions.yaml")
	if err := os.WriteFile(scenario, []byte(`actions:
  - match: "Review the code changes and return structured findings"
    ask_questions:
`+questions.String()+`    structured:
      findings:
`+findings.String()+`      summary: Decisions needed
      risk_level: medium
      risk_rationale: The behavior needs decisions
      risk_scope: source-or-external
`), 0600); err != nil {
		t.Fatal(err)
	}
	h := NewHarness(t, SetupOpts{Agent: "claude", Scenario: scenario})
	pushMainRepoConfig(t, h, trustedRepoConfigWithReviewConversation)
	if out, err := h.Run("init"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	const branch = "feature/question-actions"
	h.CommitChange(branch, "feature.txt", "flag = false\n", "add a feature flag")
	rendered, err := h.Run("axi", "run", "--intent", "Add a feature flag")
	if err != nil {
		t.Fatalf("axi run: %v\n%s", err, rendered)
	}

	type finding struct {
		ID             string `json:"id" toon:"id"`
		Action         string `json:"action" toon:"action"`
		QuestionAction string `json:"question_action" toon:"question_action"`
	}
	type findingsRecord struct {
		Findings []finding `json:"findings" toon:"findings"`
	}
	assertActions := func(surface string, rows []finding) {
		t.Helper()
		got := make(map[string]string, len(rows))
		for _, f := range rows {
			if f.Action != "ask-user" {
				t.Errorf("%s: %s changed gate classification to %q", surface, f.ID, f.Action)
			}
			got[f.ID] = f.QuestionAction
		}
		if len(rows) != len(want) || !maps.Equal(got, want) {
			t.Errorf("%s: got %v, want %v", surface, got, want)
		}
	}
	var gate struct {
		Gate findingsRecord `toon:"gate"`
	}
	start := strings.Index(rendered, "run:\n")
	if start < 0 {
		t.Fatalf("no run document:\n%s", rendered)
	}
	if err := toon.UnmarshalString(rendered[start:], &gate); err != nil {
		t.Fatalf("decode AXI gate: %v\n%s", err, rendered)
	}
	assertActions("AXI gate", gate.Gate.Findings)

	run := h.ActiveRun(branch)
	if run == nil {
		t.Fatal("review did not park")
	}
	for _, step := range run.Steps {
		if step.StepName != types.StepReview || step.Status != types.StepStatusAwaitingApproval || step.FindingsJSON == nil {
			continue
		}
		var record findingsRecord
		if err := json.Unmarshal([]byte(*step.FindingsJSON), &record); err != nil {
			t.Fatal(err)
		}
		assertActions("recorded gate findings", record.Findings)

		out, err := h.Run("axi", "rounds", "--step", "review", "--run", run.ID, "--json")
		if err != nil {
			t.Fatalf("axi rounds: %v\n%s", err, out)
		}
		var history struct {
			Rounds []findingsRecord `json:"rounds"`
		}
		if err := json.Unmarshal([]byte(out), &history); err != nil || len(history.Rounds) != 1 {
			t.Fatalf("expected one review round: %v\n%s", err, out)
		}
		assertActions("AXI rounds JSON", history.Rounds[0].Findings)
		return
	}
	t.Fatal("no findings recorded at the parked review gate")
}
