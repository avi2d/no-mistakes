package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestGateRepeatMarks_DoesNotLoseWhichRoundARepeatCameFrom(t *testing.T) {
	gate := stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r1", Severity: "warning", File: "a.go", Line: 4, Action: types.ActionAskUser, Description: "first"},
			{ID: "r2", Severity: "warning", File: "a.go", Line: 12, Action: types.ActionAskUser, Description: "second"},
		}, "2 issues"),
		RepeatMarks:       map[string]int{"r2": 2},
		NoChangeFixRounds: []int{1},
	}
	var doc struct {
		Gate struct {
			Findings []struct {
				ID     string `toon:"id"`
				Repeat string `toon:"repeat"`
			} `toon:"findings"`
			History []string `toon:"history"`
		} `toon:"gate"`
	}
	out := axiDoc(gateFields(gate)...)
	if err := toon.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode gate: %v\n%s", err, out)
	}
	if len(doc.Gate.Findings) != 2 {
		t.Fatalf("gate findings = %d rows, want 2:\n%s", len(doc.Gate.Findings), out)
	}
	for _, row := range doc.Gate.Findings {
		want := ""
		if row.ID == "r2" {
			want = "repeated from round 2"
		}
		if row.Repeat != want {
			t.Fatalf("finding %q repeat = %q, want %q:\n%s", row.ID, row.Repeat, want, out)
		}
	}
	if len(doc.Gate.History) != 1 || doc.Gate.History[0] != "fix round 1 made no change" {
		t.Fatalf("gate history = %q, want [fix round 1 made no change]:\n%s", doc.Gate.History, out)
	}
}

func TestGateRepeatMarks_DoesNotShowMarksWithoutRounds(t *testing.T) {
	gate := stepView{
		Name:   string(types.StepReview),
		Status: string(types.StepStatusAwaitingApproval),
		FindingsJSON: findingsJSON(t, []types.Finding{
			{ID: "r1", Severity: "warning", File: "a.go", Action: types.ActionAskUser, Description: "first"},
		}, "1 issue"),
	}
	out := axiDoc(gateFields(gate)...)
	for _, unwanted := range []string{"repeat", "history", "repeated from round", "made no change"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("ordinary review gate leaked %q:\n%s", unwanted, out)
		}
	}
}

func TestAxiLogsRepeatMarks_DoesNotOmitMarksFromRecordedFindings(t *testing.T) {
	repoDir, _, database, repo := setupAxiQueryRepo(t)
	chdir(t, repoDir)
	dbRun, err := database.InsertRun(repo.ID, "feature/repeat", "head", "base")
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	stepResult, err := database.InsertStepResult(dbRun.ID, types.StepReview)
	if err != nil {
		t.Fatalf("insert step: %v", err)
	}
	r1JSON := findingsJSON(t, []types.Finding{
		{ID: "f1", Severity: "warning", File: "a.go", Line: 10, Action: types.ActionAskUser, Description: "first"},
	}, "1 issue")
	r2JSON := findingsJSON(t, []types.Finding{
		{ID: "g1", Severity: "warning", File: "a.go", Line: 12, Action: types.ActionAskUser, Description: "second"},
	}, "1 issue")
	r3JSON := findingsJSON(t, []types.Finding{
		{ID: "h1", Severity: "warning", File: "a.go", Line: 13, Action: types.ActionAskUser, Description: "third"},
	}, "1 issue")
	r1, err := database.InsertReviewStepRound(stepResult.ID, 1, "initial", &r1JSON, nil, "head-one", 1)
	if err != nil {
		t.Fatalf("insert round 1: %v", err)
	}
	if err := database.SetStepRoundSelection(r1.ID, strptr(`["f1"]`), db.RoundSelectionSourceUser); err != nil {
		t.Fatalf("select round 1: %v", err)
	}
	r2, err := database.InsertReviewStepRound(stepResult.ID, 2, "auto_fix", &r2JSON, nil, "head-one", 1)
	if err != nil {
		t.Fatalf("insert round 2: %v", err)
	}
	if err := database.SetStepRoundSelection(r2.ID, strptr(`["g1"]`), db.RoundSelectionSourceUser); err != nil {
		t.Fatalf("select round 2: %v", err)
	}
	if _, err := database.InsertReviewStepRound(stepResult.ID, 3, "auto_fix", &r3JSON, nil, "head-one", 1); err != nil {
		t.Fatalf("insert round 3: %v", err)
	}
	if err := database.SetStepFindings(stepResult.ID, r3JSON); err != nil {
		t.Fatalf("set findings: %v", err)
	}
	if err := database.UpdateStepStatus(stepResult.ID, types.StepStatusCompleted); err != nil {
		t.Fatalf("complete step: %v", err)
	}
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	if err := runAxiLogs(cmd, string(types.StepReview), dbRun.ID, false); err != nil {
		t.Fatalf("axi logs: %v\n%s", err, out.String())
	}
	for _, want := range []string{"repeated from round 2", "fix round 2 made no change"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("axi logs is missing %q:\n%s", want, out.String())
		}
	}
}
