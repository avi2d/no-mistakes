package pipeline

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/ipc"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_FixRoundWithNoDiffParksInsteadOfStartingAnotherRound(t *testing.T) {
	for _, tc := range []struct {
		name      string
		step      types.StepName
		landsDiff bool
		wantCalls int
	}{
		{"review no diff", types.StepReview, false, 2},
		{"lint no diff", types.StepLint, false, 2},
		{"review lands diff", types.StepReview, true, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, p, run, repo := setupTest(t)
			cfg := &config.Config{AutoFix: config.AutoFix{Review: 3, Lint: 3}}
			parks := make(chan string, 4)
			calls := 0
			step := &adaptiveCallStep{name: tc.step, fn: func(sctx *StepContext) (*StepOutcome, error) {
				calls++
				if tc.landsDiff {
					landFixDiff(sctx)
				}
				return &StepOutcome{NeedsApproval: true, AutoFixable: true, Findings: roundLimitFinding}, nil
			}}
			exec := NewExecutor(database, p, cfg, nil, []Step{step}, func(event ipc.Event) {
				if event.Type == ipc.EventStepCompleted && event.Status != nil && *event.Status == string(types.StepStatusFixReview) {
					parks <- *event.Findings
				}
			})
			done, _ := startExecutor(t, exec, run, repo, t.TempDir())
			raw := waitReviewBudgetPark(t, parks, done)
			if calls != tc.wantCalls {
				t.Fatalf("step calls=%d, want %d", calls, tc.wantCalls)
			}
			if HasFixRoundNoDiff(raw) == tc.landsDiff {
				t.Fatalf("no-diff notice present=%v after a round that landed a diff=%v: %s", HasFixRoundNoDiff(raw), tc.landsDiff, raw)
			}
			findings, err := types.ParseFindingsJSON(raw)
			if err != nil {
				t.Fatal(err)
			}
			byID := map[string]types.Finding{}
			for _, f := range findings.Items {
				byID[f.ID] = f
			}
			if _, ok := byID["R1"]; !ok {
				t.Fatalf("the unfixed finding left the gate: %s", raw)
			}
			if notice, ok := byID[types.FindingIDFixRoundNoDiff]; ok {
				if notice.ActionOrDefault() != types.ActionAskUser || !strings.Contains(notice.Description, "landed no diff") {
					t.Fatalf("notice does not escalate with its reason: %+v", notice)
				}
			}
			if err := exec.Respond(tc.step, types.ActionApprove, nil); err != nil {
				t.Fatal(err)
			}
			waitExecutorDone(t, done)
		})
	}
}

func TestExecutor_HumanFixAfterNoDiffParkDoesNotKeepAStaleNotice(t *testing.T) {
	database, p, run, repo := setupTest(t)
	cfg := &config.Config{AutoFix: config.AutoFix{Review: 3}}
	parks := make(chan string, 4)
	calls := 0
	step := &adaptiveCallStep{name: types.StepReview, fn: func(sctx *StepContext) (*StepOutcome, error) {
		calls++
		if calls >= 3 {
			landFixDiff(sctx)
		}
		return &StepOutcome{NeedsApproval: true, AutoFixable: true, Findings: roundLimitFinding}, nil
	}}
	exec := NewExecutor(database, p, cfg, nil, []Step{step}, func(event ipc.Event) {
		if event.Type == ipc.EventStepCompleted && event.Status != nil && *event.Status == string(types.StepStatusFixReview) {
			parks <- *event.Findings
		}
	})
	done, _ := startExecutor(t, exec, run, repo, t.TempDir())
	if raw := waitReviewBudgetPark(t, parks, done); !HasFixRoundNoDiff(raw) {
		t.Fatalf("no-diff round did not park with the notice: %s", raw)
	}
	if err := respondFixPartial(t, exec, types.StepReview, "R1"); err != nil {
		t.Fatal(err)
	}
	if raw := waitReviewBudgetPark(t, parks, done); HasFixRoundNoDiff(raw) {
		t.Fatalf("a fix round that landed a diff kept the no-diff notice: %s", raw)
	}
	if err := exec.Respond(types.StepReview, types.ActionApprove, nil); err != nil {
		t.Fatal(err)
	}
	waitExecutorDone(t, done)
}

func TestFixRoundLandedNoDiff_BudgetCutReplayIsNotAFixerRound(t *testing.T) {
	budgetCut := `{"findings":[{"id":"` + types.FindingIDTestAgentTimeout + `","severity":"warning","action":"auto-fix","description":"budget cut"}]}`
	for _, tc := range []struct {
		name     string
		fixing   bool
		skipFix  bool
		selected string
		headNow  string
		want     bool
	}{
		{"code finding, head unchanged", true, false, roundLimitFinding, "start", true},
		{"code finding, head moved", true, false, roundLimitFinding, "moved", false},
		{"budget cut only", true, false, budgetCut, "start", false},
		{"answer replay", true, true, roundLimitFinding, "start", false},
		{"not a fix round", false, false, roundLimitFinding, "start", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sctx := &StepContext{Run: &db.Run{HeadSHA: tc.headNow}, Fixing: tc.fixing, SkipFixExecution: tc.skipFix, PreviousFindings: tc.selected}
			if got := fixRoundLandedNoDiff(sctx, "start"); got != tc.want {
				t.Fatalf("fixRoundLandedNoDiff=%v, want %v", got, tc.want)
			}
		})
	}
}
