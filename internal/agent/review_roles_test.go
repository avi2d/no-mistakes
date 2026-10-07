package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type roleRecorder struct {
	name               string
	calls              []RunOpts
	closed             int
	resumable, neutral bool
	err                error
}

func (a *roleRecorder) Name() string { return a.name }
func (a *roleRecorder) Run(_ context.Context, opts RunOpts) (*Result, error) {
	a.calls = append(a.calls, opts)
	return &Result{SessionID: "fix-session"}, a.err
}
func (a *roleRecorder) Close() error                      { a.closed++; return nil }
func (a *roleRecorder) SupportsSessionResume() bool       { return a.resumable }
func (a *roleRecorder) NeutralizesGateInstructions() bool { return a.neutral }

func TestReviewAgentsRouteAndPreserveCapabilities(t *testing.T) {
	primary := &roleRecorder{name: "codex", neutral: true}
	reviewer := &roleRecorder{name: "claude", neutral: true, resumable: true}
	fixer := &roleRecorder{name: "pi", neutral: true, resumable: true}
	ag := WithReviewAgents(primary, reviewer, fixer)
	var attempts []string
	for _, purpose := range []string{"review", "review-fix", "review", "test-evidence", "review-fix"} {
		result, err := ag.Run(context.Background(), RunOpts{Purpose: purpose, Session: &SessionRef{ID: "prior", Agent: "pi"}, OnAttempt: func(a Attempt) { attempts = append(attempts, a.Agent) }})
		if err != nil {
			t.Fatal(err)
		}
		if result.Provider == "" {
			t.Fatal("concrete provider was lost")
		}
	}
	if len(primary.calls) != 1 || len(reviewer.calls) != 2 || len(fixer.calls) != 2 {
		t.Fatal("incorrect role routing")
	}
	for _, call := range reviewer.calls {
		if call.Session != nil {
			t.Fatal("review inherited a session")
		}
	}
	for _, call := range fixer.calls {
		if call.Session == nil || call.Session.ID != "prior" {
			t.Fatal("fix session lost")
		}
	}
	if len(attempts) != 5 || attempts[0] != "claude" || attempts[1] != "pi" || attempts[3] != "codex" {
		t.Fatalf("attempts = %v", attempts)
	}
	if !SupportsSessionResume(ag) || !SupportsSessionProvider(ag, "pi") || SupportsSessionProvider(ag, "claude") {
		t.Fatal("sessions must follow fixer capability only")
	}
	if !NeutralizesGateInstructions(ag) {
		t.Fatal("neutralization not forwarded")
	}
	reviewer.neutral = false
	if NeutralizesGateInstructions(ag) {
		t.Fatal("unsafe reviewer accepted")
	}
	if err := ag.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.closed != 1 || reviewer.closed != 1 || fixer.closed != 1 {
		t.Fatal("agents must close exactly once")
	}
}

func TestReviewAgentsDefaults(t *testing.T) {
	primary := &roleRecorder{name: "pi", resumable: true}
	if WithReviewAgents(primary, nil, nil) != primary {
		t.Fatal("absent roles must preserve original agent")
	}
	reviewer := &roleRecorder{name: "claude"}
	ag := WithReviewAgents(primary, reviewer, nil)
	if !SupportsSessionProvider(ag, "pi") {
		t.Fatal("default fixer capability lost")
	}
	_, err := ag.Run(context.Background(), RunOpts{Purpose: "review-fix"})
	if err != nil || len(primary.calls) != 1 {
		t.Fatal("unset fixer did not use primary")
	}
	ag = WithReviewAgents(primary, nil, &roleRecorder{name: "cold"})
	if SupportsSessionResume(ag) {
		t.Fatal("nonresumable fixer inherited primary capability")
	}
}

func TestReviewRoleFallsBackOnQuotaError(t *testing.T) {
	primary := &roleRecorder{name: "sol", err: errors.New("provider quota exceeded")}
	firstFallback := &roleRecorder{name: "opus-4", err: errors.New("usage limit reached")}
	secondFallback := &roleRecorder{name: "opus-5-5"}
	ag := WithReviewRoles(primary, ReviewRoles{Reviewer: RoundedRole{Agent: primary, Fallback: []Agent{firstFallback, secondFallback}, AgentLabel: "Sol", FallbackLabels: []string{"Opus 4", "Opus 5.5"}}})
	result, err := ag.Run(context.Background(), RunOpts{Purpose: "review", Round: 1})
	if err != nil || result.Provider != "opus-5-5" || result.AgentIdentity != "Opus 5.5" || len(primary.calls) != 1 || len(firstFallback.calls) != 1 || len(secondFallback.calls) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, len(primary.calls), len(firstFallback.calls), len(secondFallback.calls))
	}
}

func TestReviewerCandidatesSkipBelowFloorAndRecordTrace(t *testing.T) {
	first := &roleRecorder{name: "codex"}
	second := &roleRecorder{name: "claude-pro", err: errors.New("quota exceeded")}
	last := &roleRecorder{name: "claude-max"}
	floorChecks := 0
	role := RoundedRole{Candidates: []ReviewerCandidate{
		{Agent: first, Label: "Codex Pro", CheckFloor: func(context.Context) (string, error) {
			floorChecks++
			return "remaining 20% is below the 30% minimum", nil
		}},
		{Agent: second, Label: "Claude Pro", CheckFloor: func(context.Context) (string, error) {
			floorChecks++
			return "", nil
		}},
		{Agent: last, Label: "Claude Max", CheckFloor: func(context.Context) (string, error) {
			t.Fatal("last candidate floor must not run")
			return "", nil
		}},
	}}
	ag := WithReviewRoles(&roleRecorder{name: "default"}, ReviewRoles{Reviewer: role})
	result, err := ag.Run(context.Background(), RunOpts{Purpose: "review"})
	if err != nil || result.AgentIdentity != "Claude Max" || len(first.calls) != 0 || len(second.calls) != 1 || len(last.calls) != 1 || floorChecks != 2 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d floorChecks=%d", result, err, len(first.calls), len(second.calls), len(last.calls), floorChecks)
	}
	var trace reviewerTrace
	if err := json.Unmarshal([]byte(result.ReviewerChainTrace), &trace); err != nil {
		t.Fatalf("trace %q: %v", result.ReviewerChainTrace, err)
	}
	if trace.Selected != 2 || len(trace.Skipped) != 2 || trace.Skipped[0].Entry != 0 || trace.Skipped[1].Entry != 1 {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestReviewerCandidatesSkipRefusedAccountAndRecordTrace(t *testing.T) {
	quota := &roleRecorder{name: "codex", err: errors.New("quota exceeded")}
	refused := &roleRecorder{name: "claude-pro", err: errors.New("pi exited: exit status 1: Your organization has disabled Claude subscription access for Claude Code \u00b7 Use an Anthropic API key instead, or ask your admin to enable access")}
	last := &roleRecorder{name: "claude-max"}
	role := RoundedRole{Candidates: []ReviewerCandidate{
		{Agent: quota, Label: "Codex"},
		{Agent: refused, Label: "Claude Pro"},
		{Agent: last, Label: "Claude Max"},
	}}
	ag := WithReviewRoles(&roleRecorder{name: "default"}, ReviewRoles{Reviewer: role})
	result, err := ag.Run(context.Background(), RunOpts{Purpose: "review"})
	if err != nil || result.AgentIdentity != "Claude Max" || len(quota.calls) != 1 || len(refused.calls) != 1 || len(last.calls) != 1 {
		t.Fatalf("result=%+v err=%v calls=%d/%d/%d", result, err, len(quota.calls), len(refused.calls), len(last.calls))
	}
	var trace reviewerTrace
	if err := json.Unmarshal([]byte(result.ReviewerChainTrace), &trace); err != nil {
		t.Fatalf("trace %q: %v", result.ReviewerChainTrace, err)
	}
	if trace.Selected != 2 || len(trace.Skipped) != 2 || trace.Skipped[0].Entry != 0 || trace.Skipped[1].Entry != 1 || !strings.Contains(trace.Skipped[1].Reason, "disabled Claude subscription access") {
		t.Fatalf("trace = %+v", trace)
	}
}

func TestReviewerCandidatesAllUnusableFailsWithEveryReason(t *testing.T) {
	first := &roleRecorder{name: "codex", err: errors.New("quota exceeded")}
	second := &roleRecorder{name: "claude-pro", err: errors.New("claude exited: exit status 1: provider authentication required")}
	role := RoundedRole{Candidates: []ReviewerCandidate{
		{Agent: first, Label: "Codex"},
		{Agent: second, Label: "Claude Pro"},
	}}
	ag := WithReviewRoles(&roleRecorder{name: "default"}, ReviewRoles{Reviewer: role})
	_, err := ag.Run(context.Background(), RunOpts{Purpose: "review"})
	if err == nil || !strings.Contains(err.Error(), "quota exceeded") || !strings.Contains(err.Error(), "provider authentication required") {
		t.Fatalf("err=%v, want every link's reason", err)
	}
	if len(first.calls) != 1 || len(second.calls) != 1 {
		t.Fatalf("calls=%d/%d, want 1/1", len(first.calls), len(second.calls))
	}
}

func TestReviewerCandidatesDoNotSkipOrdinaryErrors(t *testing.T) {
	broken := &roleRecorder{name: "codex", err: errors.New("invalid response")}
	next := &roleRecorder{name: "claude"}
	role := RoundedRole{Candidates: []ReviewerCandidate{
		{Agent: broken, Label: "Codex"},
		{Agent: next, Label: "Claude"},
	}}
	ag := WithReviewRoles(&roleRecorder{name: "default"}, ReviewRoles{Reviewer: role})
	_, err := ag.Run(context.Background(), RunOpts{Purpose: "review"})
	if err == nil || len(next.calls) != 0 {
		t.Fatalf("err=%v next calls=%d", err, len(next.calls))
	}
}

func TestReviewRoleDoesNotFallbackOnNonQuotaError(t *testing.T) {
	primary := &roleRecorder{name: "sol", err: errors.New("invalid response")}
	fallback := &roleRecorder{name: "opus"}
	ag := WithReviewRoles(primary, ReviewRoles{Reviewer: RoundedRole{Agent: primary, Fallback: []Agent{fallback}}})
	_, err := ag.Run(context.Background(), RunOpts{Purpose: "review", Round: 1})
	if err == nil || len(fallback.calls) != 0 {
		t.Fatalf("err=%v fallback calls=%d", err, len(fallback.calls))
	}
}

func TestReviewRolesHandOverAtConfiguredRound(t *testing.T) {
	primary := &roleRecorder{name: "codex", neutral: true, resumable: true}
	fixer := &roleRecorder{name: "pi", neutral: true, resumable: true}
	lateFixer := &roleRecorder{name: "cheap", neutral: true, resumable: true}
	lateReviewer := &roleRecorder{name: "late-review", neutral: true}
	ag := WithReviewRoles(primary, ReviewRoles{
		Reviewer: RoundedRole{Late: lateReviewer, LateFrom: 2},
		Fixer:    RoundedRole{Agent: fixer, Late: lateFixer, LateFrom: 3},
	})
	for _, round := range []int{1, 2, 3, 4} {
		for _, purpose := range []string{"review", "review-fix"} {
			if _, err := ag.Run(context.Background(), RunOpts{Purpose: purpose, Round: round}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// Reviewer: round 1 has no configured base role, so it stays on the
	// default agent; rounds 2+ move to the overlay.
	if len(lateReviewer.calls) != 3 {
		t.Fatalf("late reviewer served %d rounds, want 3", len(lateReviewer.calls))
	}
	// Fixer: rounds 1-2 on the configured fixer, rounds 3-4 on the overlay.
	if len(fixer.calls) != 2 || len(lateFixer.calls) != 2 {
		t.Fatalf("fix rounds split %d/%d, want 2/2", len(fixer.calls), len(lateFixer.calls))
	}
	// One primary call: the round-1 review that has no base reviewer.
	if len(primary.calls) != 1 {
		t.Fatalf("primary served %d invocations, want 1", len(primary.calls))
	}
	// An invocation the pipeline did not number must not be routed by a
	// round the router had to guess.
	if _, err := ag.Run(context.Background(), RunOpts{Purpose: "review-fix"}); err != nil {
		t.Fatal(err)
	}
	if len(fixer.calls) != 3 || len(lateFixer.calls) != 2 {
		t.Fatal("unnumbered invocation left the primary role")
	}
	// A later-round fixer that cannot resume must close the capability for
	// the whole run rather than letting round 1 advertise it.
	if !SupportsSessionResume(ag) {
		t.Fatal("all-resumable fixers lost session capability")
	}
	lateFixer.resumable = false
	if SupportsSessionResume(ag) || SupportsSessionProvider(ag, "pi") {
		t.Fatal("nonresumable later-round fixer inherited the early fixer capability")
	}
	if !NeutralizesGateInstructions(ag) {
		t.Fatal("neutralization not forwarded across overlays")
	}
	lateReviewer.neutral = false
	if NeutralizesGateInstructions(ag) {
		t.Fatal("unsafe later-round reviewer accepted")
	}
	if err := ag.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.closed != 1 || fixer.closed != 1 || lateFixer.closed != 1 || lateReviewer.closed != 1 {
		t.Fatal("overlay agents must close exactly once")
	}
}

func TestReviewRolesWithoutOverlaysIgnoreRounds(t *testing.T) {
	primary := &roleRecorder{name: "codex"}
	fixer := &roleRecorder{name: "pi"}
	ag := WithReviewAgents(primary, nil, fixer)
	for _, round := range []int{1, 2, 9} {
		if _, err := ag.Run(context.Background(), RunOpts{Purpose: "review-fix", Round: round}); err != nil {
			t.Fatal(err)
		}
	}
	if len(fixer.calls) != 3 || len(primary.calls) != 0 {
		t.Fatal("unconfigured overlay changed routing by round")
	}
	if WithReviewRoles(primary, ReviewRoles{}) != primary {
		t.Fatal("empty roles must preserve the original agent")
	}
	// An overlay agent with no round to take over from never serves.
	late := &roleRecorder{name: "late"}
	ag = WithReviewRoles(primary, ReviewRoles{Fixer: RoundedRole{Agent: fixer, Late: late}})
	if _, err := ag.Run(context.Background(), RunOpts{Purpose: "review-fix", Round: 7}); err != nil {
		t.Fatal(err)
	}
	if len(late.calls) != 0 || len(fixer.calls) != 4 {
		t.Fatal("overlay without a takeover round served a turn")
	}
}
