package cli

import (
	"testing"

	toon "github.com/toon-format/toon-go"

	"github.com/kunchenguid/no-mistakes/internal/paths"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type reviewerEntryStatus struct {
	Run struct {
		ReviewerAgent string `toon:"reviewer_agent"`
		ReviewerEntry *struct {
			Index *int   `toon:"index"`
			Name  string `toon:"name"`
		} `toon:"reviewer_entry"`
	} `toon:"run"`
}

func statusForReviewerChainTrace(t *testing.T, trace string) (string, reviewerEntryStatus) {
	t.Helper()
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
	if err := database.SetStepRoundReviewerChainTrace(round.ID, trace); err != nil {
		t.Fatal(err)
	}
	steps, err := database.GetStepsByRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	rv := runViewFromDB(run, steps, database)
	annotateRunView(&axiEnv{d: database, p: paths.WithRoot(t.TempDir())}, &rv)
	out := axiDoc(runObjectField(rv))
	var doc reviewerEntryStatus
	if err := toon.UnmarshalString(out, &doc); err != nil {
		t.Fatalf("status is not TOON: %v\n%s", err, out)
	}
	if doc.Run.ReviewerAgent != "pi/claude-bridge/claude-haiku-5-5" {
		t.Fatalf("status lost the reviewer agent:\n%s", out)
	}
	return out, doc
}

func TestAxiStatusKeepsTheServingEntrysIndexWhenTheEntryIsNamed(t *testing.T) {
	out, doc := statusForReviewerChainTrace(t, `{"selected":1,"name":"run-token","skipped":[{"entry":0,"reason":"floor below 20"}]}`)
	entry := doc.Run.ReviewerEntry
	if entry == nil || entry.Index == nil || *entry.Index != 1 || entry.Name != "run-token" {
		t.Fatalf("status = \n%s\nwant reviewer_entry index 1 and name run-token", out)
	}
}

func TestAxiStatusNamesAnUnnamedServingEntryByIndexAlone(t *testing.T) {
	out, doc := statusForReviewerChainTrace(t, `{"selected":1,"skipped":[{"entry":0,"reason":"run carries no ANTHROPIC_API_KEY"}]}`)
	entry := doc.Run.ReviewerEntry
	if entry == nil || entry.Index == nil || *entry.Index != 1 || entry.Name != "" {
		t.Fatalf("status = \n%s\nwant reviewer_entry index 1 with no name", out)
	}
}

func TestAxiStatusNamesNoEntryForAReviewWithoutAChain(t *testing.T) {
	out, doc := statusForReviewerChainTrace(t, "")
	if doc.Run.ReviewerEntry != nil {
		t.Fatalf("status named an entry for a run without a reviewer chain:\n%s", out)
	}
}

func TestAxiStatusDoesNotReadATraceWithoutASelectionAsTheFirstEntry(t *testing.T) {
	out, doc := statusForReviewerChainTrace(t, `{"skipped":[{"entry":0,"reason":"floor below 20"}]}`)
	if doc.Run.ReviewerEntry != nil {
		t.Fatalf("status claimed an entry the trace never selected:\n%s", out)
	}
}
