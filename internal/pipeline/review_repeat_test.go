package pipeline

import (
	"encoding/json"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func repeatFixtureRound(t *testing.T, round int, trigger, head string, findings []types.Finding, selected []string) *db.StepRound {
	t.Helper()
	encoded, err := types.MarshalFindingsJSON(types.Findings{Items: findings})
	if err != nil {
		t.Fatalf("marshal findings: %v", err)
	}
	r := &db.StepRound{Round: round, Trigger: trigger, FindingsJSON: &encoded}
	if head != "" {
		h := head
		r.ReviewedHeadSHA = &h
	}
	if selected != nil {
		ids, err := json.Marshal(selected)
		if err != nil {
			t.Fatalf("marshal selected ids: %v", err)
		}
		s := string(ids)
		r.SelectedFindingIDs = &s
	}
	return r
}

func repeatFixtureFinding(id, file string, line int) types.Finding {
	return types.Finding{ID: id, Severity: types.FindingSeverityWarning, File: file, Line: line, Description: id, Action: types.ActionAskUser}
}

func TestReviewRoundMarks_DoesNotMissARepeatOrANoChangeRound(t *testing.T) {
	rounds := []*db.StepRound{
		repeatFixtureRound(t, 1, "initial", "head-one", []types.Finding{repeatFixtureFinding("f1", "a.go", 10)}, []string{"f1"}),
		repeatFixtureRound(t, 2, "auto_fix", "head-one", []types.Finding{repeatFixtureFinding("g1", "a.go", 12)}, []string{"g1"}),
		repeatFixtureRound(t, 3, "auto_fix", "head-one", []types.Finding{repeatFixtureFinding("h1", "a.go", 13)}, nil),
	}
	repeats, noChange := ReviewRoundMarks(rounds)
	if repeats["h1"] != 2 || len(repeats) != 1 {
		t.Fatalf("repeats = %v, want map[h1:2]", repeats)
	}
	if len(noChange) != 1 || noChange[0] != 2 {
		t.Fatalf("no-change fix rounds = %v, want [2]", noChange)
	}
}

func TestReviewRoundMarks_DoesNotMarkCleanRounds(t *testing.T) {
	rounds := []*db.StepRound{
		repeatFixtureRound(t, 1, "initial", "head-one", []types.Finding{repeatFixtureFinding("f1", "a.go", 10)}, []string{"f1"}),
		repeatFixtureRound(t, 2, "auto_fix", "head-two", []types.Finding{repeatFixtureFinding("g1", "b.go", 5)}, []string{"g1"}),
		repeatFixtureRound(t, 3, "auto_fix", "head-three", []types.Finding{repeatFixtureFinding("h1", "c.go", 7)}, nil),
	}
	repeats, noChange := ReviewRoundMarks(rounds)
	if len(repeats) != 0 {
		t.Fatalf("repeats = %v, want none", repeats)
	}
	if len(noChange) != 0 {
		t.Fatalf("no-change fix rounds = %v, want none", noChange)
	}
}

func TestReviewRoundMarks_DoesNotMarkARepeatFourLinesAway(t *testing.T) {
	rounds := []*db.StepRound{
		repeatFixtureRound(t, 1, "initial", "head-one", []types.Finding{repeatFixtureFinding("f1", "a.go", 10)}, []string{"f1"}),
		repeatFixtureRound(t, 2, "auto_fix", "head-two", []types.Finding{repeatFixtureFinding("g1", "a.go", 14)}, nil),
	}
	repeats, noChange := ReviewRoundMarks(rounds)
	if len(repeats) != 0 {
		t.Fatalf("repeats = %v, want none for a finding 4 lines away", repeats)
	}
	if len(noChange) != 0 {
		t.Fatalf("no-change fix rounds = %v, want none", noChange)
	}
}
