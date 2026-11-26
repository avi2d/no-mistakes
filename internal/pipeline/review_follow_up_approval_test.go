package pipeline

import (
	"slices"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestExecutor_ReviewApprovalDoesNotDropNamedFollowUpsOrRecordInvalidOnes(t *testing.T) {
	const gate = `{"findings":[{"id":"R1","severity":"warning","description":"Improve naming"},{"id":"R2","severity":"info","description":"Add an example"},{"id":"R3","severity":"warning","description":"Leave unchanged unless named"}]}`
	const mixedGate = `{"findings":[{"id":"R1","severity":"warning","description":"Improve naming"},{"id":"R4","severity":"error","description":"Fix behavior"}]}`
	for _, tc := range []struct {
		name       string
		enabled    bool
		step       types.StepName
		round      int
		findings   string
		reviewed   bool
		earlierFix bool
		ids        []string
		want       []string
	}{
		{name: "later Review", enabled: true, step: types.StepReview, round: 2, findings: gate, reviewed: true, ids: []string{"R2", "R1"}, want: []string{"R1", "R2"}},
		{name: "first Review", enabled: true, step: types.StepReview, round: 1, findings: gate, reviewed: true, ids: []string{"R1"}},
		{name: "disabled", step: types.StepReview, round: 2, findings: gate, reviewed: true, ids: []string{"R1"}},
		{name: "other step", enabled: true, step: types.StepLint, round: 2, findings: gate, reviewed: true, ids: []string{"R1"}},
		{name: "unknown ID", enabled: true, step: types.StepReview, round: 2, findings: gate, reviewed: true, ids: []string{"R1", "R9"}},
		{name: "selected error", enabled: true, step: types.StepReview, round: 2, findings: mixedGate, reviewed: true, ids: []string{"R1", "R4"}},
		{name: "earlier selected error", enabled: true, step: types.StepReview, round: 2, findings: mixedGate, reviewed: true, earlierFix: true, ids: []string{"R1"}},
		{name: "missing reviewed head", enabled: true, step: types.StepReview, round: 2, findings: gate, ids: []string{"R1"}},
		{name: "unanswered question", enabled: true, step: types.StepReview, round: 2, findings: `{"findings":[{"id":"R1","severity":"warning","description":"Improve naming"},{"id":"question-q1","severity":"warning","category":"review-question","description":"Which option?"}]}`, reviewed: true, ids: []string{"R1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database, p, run, _ := setupTest(t)
			sr, err := database.InsertStepResult(run.ID, tc.step)
			if err != nil {
				t.Fatal(err)
			}
			var head string
			if tc.reviewed {
				head = "1111111111111111111111111111111111111111"
			}
			if tc.earlierFix {
				prior, err := database.InsertReviewStepRound(sr.ID, 1, "initial", &tc.findings, nil, head, 0)
				if err != nil {
					t.Fatal(err)
				}
				selection := `["R4"]`
				if err := database.SetStepRoundSelectedFindingIDs(prior.ID, &selection); err != nil {
					t.Fatal(err)
				}
			}
			round, err := database.InsertReviewStepRound(sr.ID, tc.round, "initial", &tc.findings, nil, head, 0)
			if err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{}
			cfg.Review.DeferNonErrors = tc.enabled
			exec := NewExecutor(database, p, cfg, nil, nil, nil)
			exec.waiting = true
			exec.waitingStep = tc.step
			exec.waitingStepResultID = sr.ID
			exec.waitingRoundID = round.ID
			exec.waitingFindings = tc.findings
			exec.approvalCh = make(chan approvalResponse, 1)

			got, err := exec.RespondWithOverrides(tc.step, types.ActionApprove, tc.ids, nil, nil, nil, "")
			rounds, readErr := database.GetRoundsByStep(sr.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			stored := rounds[len(rounds)-1]
			if tc.want == nil {
				if err == nil || !exec.AwaitingApproval() || len(exec.approvalCh) != 0 || stored.ReviewFollowUpsJSON != nil {
					t.Fatalf("invalid approval released the gate or wrote follow-ups: dispositions=%+v error=%v parked=%v stored=%+v", got, err, exec.AwaitingApproval(), stored)
				}
				return
			}
			if err != nil || !slices.Equal(got.FollowUps, tc.want) || len(got.Fixed) != 0 {
				t.Fatalf("approval must report only the named follow-ups: dispositions=%+v error=%v", got, err)
			}
			if stored.ReviewFollowUpsJSON == nil || !slices.Equal(findingIDList(*stored.ReviewFollowUpsJSON), tc.want) {
				t.Fatalf("follow-ups were not persisted before release: %+v", stored)
			}
			if ids := selectedIDsOf(t, stored); len(ids) != 0 {
				t.Fatalf("follow-ups were recorded as fixer selections: %v", ids)
			}
			select {
			case response := <-exec.approvalCh:
				if response.action != types.ActionApprove || len(response.findingIDs) != 0 {
					t.Fatalf("follow-ups were dispatched as fixes: %+v", response)
				}
			default:
				t.Fatal("valid approval did not release the gate")
			}
		})
	}
}
