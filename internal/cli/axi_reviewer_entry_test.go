package cli

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func TestAxiStatusNamesTheServingReviewerChainEntryNotJustItsAgent(t *testing.T) {
	for _, tc := range []struct {
		name  string
		trace string
		want  string
	}{
		{name: "named entry", trace: `{"selected":0,"name":"run-token"}`, want: "reviewer_entry: run-token"},
		{name: "unnamed entry after a skip", trace: `{"selected":1,"skipped":[{"entry":0,"reason":"run carries no ANTHROPIC_API_KEY"}]}`, want: `reviewer_entry: "reviewer_chain[1]"`},
		{name: "no reviewer chain", trace: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := openTestDB(t)
			repo, err := database.InsertRepo(t.TempDir(), "origin", "main")
			if err != nil {
				t.Fatal(err)
			}
			run, err := database.InsertRun(repo.ID, "feature", "abcdef1234567890", "base")
			if err != nil {
				t.Fatal(err)
			}
			step, err := database.InsertStepResult(run.ID, types.StepReview)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.UpdateStepStatus(step.ID, types.StepStatusCompleted); err != nil {
				t.Fatal(err)
			}
			round, err := database.InsertStepRound(step.ID, 1, "initial", nil, nil, 10)
			if err != nil {
				t.Fatal(err)
			}
			if err := database.SetStepRoundReviewerAgent(round.ID, "pi/claude-bridge/claude-haiku-5-5"); err != nil {
				t.Fatal(err)
			}
			if err := database.SetStepRoundReviewerChainTrace(round.ID, tc.trace); err != nil {
				t.Fatal(err)
			}
			steps, err := database.GetStepsByRun(run.ID)
			if err != nil {
				t.Fatal(err)
			}
			rv := runViewFromDB(run, steps, database)
			annotateRunView(&axiEnv{d: database, p: paths.WithRoot(t.TempDir())}, &rv)
			out := axiDoc(runObjectField(rv))
			if !strings.Contains(out, "reviewer_agent: pi/claude-bridge/claude-haiku-5-5") {
				t.Fatalf("status lost the reviewer agent:\n%s", out)
			}
			if tc.want == "" {
				if strings.Contains(out, "reviewer_entry") {
					t.Fatalf("status named an entry for a run without a reviewer chain:\n%s", out)
				}
				return
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("status = \n%s\nwant %q", out, tc.want)
			}
		})
	}
}
