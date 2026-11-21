package pipeline

import (
	"context"
	"errors"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
)

func TestFixerTrialDoesNotReplayAFailedColdTurnAsAResumeFallback(t *testing.T) {
	ctx := context.Background()
	base, late := newFakeSessionAgent(), newFakeSessionAgent()
	ag := agent.WithReviewRoles(base, agent.ReviewRoles{
		Fixer: agent.RoundedRole{Late: late, LateFrom: 2, TrialModel: "trial-model"},
	})
	sessions := NewRunSessions(nil, "trial", ag, true)
	if _, err := sessions.Run(ctx, ag, SessionRoleFixer, agent.RunOpts{Purpose: "review-fix", Round: 1}, nil); err != nil {
		t.Fatal(err)
	}
	if sessions.id(SessionRoleFixer).ID == "" {
		t.Fatal("base fixer did not leave a session to protect against")
	}

	failed := errors.New("cold fixer failed")
	late.failNext = failed
	if _, err := sessions.Run(ctx, ag, SessionRoleFixer, agent.RunOpts{Purpose: "review-fix", Round: 2}, nil); !errors.Is(err, failed) {
		t.Fatalf("trial failure = %v, want %v", err, failed)
	}
	if len(late.calls) != 1 || late.calls[0].session != nil {
		t.Fatalf("trial invoked again or received a session: %+v", late.calls)
	}
}
