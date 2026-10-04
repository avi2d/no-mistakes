package agent

import (
	"context"
	"errors"
	"time"
)

// RoundedRole retries only usage-limit errors through Fallback.
type RoundedRole struct {
	Agent          Agent
	Late           Agent
	LateFrom       int
	Fallback       []Agent
	AgentLabel     string
	LateLabel      string
	FallbackLabels []string
}

// ReviewRoles carries both review-loop roles with their optional later-round
// overlays.
type ReviewRoles struct{ Reviewer, Fixer RoundedRole }

// agents lists every non-nil agent this role owns, primary first.
func (r RoundedRole) agents() []Agent {
	var out []Agent
	if r.Agent != nil {
		out = append(out, r.Agent)
	}
	if r.Late != nil {
		out = append(out, r.Late)
	}
	out = append(out, r.Fallback...)
	return out
}

// pick selects the agent serving round, falling back to the default agent when
// the role configures none. The overlay applies only to a known round, so an
// invocation the pipeline did not number keeps the primary selection rather
// than guessing.
func (r RoundedRole) pick(round int, fallback Agent) Agent {
	if r.Late != nil && r.LateFrom > 0 && round >= r.LateFrom {
		return r.Late
	}
	if r.Agent != nil {
		return r.Agent
	}
	return fallback
}

func (r RoundedRole) label(round int) string {
	if r.Late != nil && r.LateFrom > 0 && round >= r.LateFrom {
		return r.LateLabel
	}
	return r.AgentLabel
}

func (r RoundedRole) fallbackLabel(index int, fallback Agent) string {
	if index < len(r.FallbackLabels) && r.FallbackLabels[index] != "" {
		return r.FallbackLabels[index]
	}
	return fallback.Name()
}

// WithReviewAgents routes only review and review-fix invocations to dedicated
// role agents for every round. It is the round-agnostic form of
// WithReviewRoles.
func WithReviewAgents(primary, reviewer, fixer Agent) Agent {
	return WithReviewRoles(primary, ReviewRoles{
		Reviewer: RoundedRole{Agent: reviewer},
		Fixer:    RoundedRole{Agent: fixer},
	})
}

// WithReviewRoles routes only review and review-fix invocations. Empty roles
// keep the default agent (including its fallback chain). Ownership of all
// supplied agents transfers to the wrapper; every supplied agent must be an
// independent instance. Only the fixer resumes sessions; review turns remain
// fresh.
func WithReviewRoles(primary Agent, roles ReviewRoles) Agent {
	if len(roles.Reviewer.agents()) == 0 && len(roles.Fixer.agents()) == 0 {
		return primary
	}
	return &reviewAgents{primary: primary, reviewer: roles.Reviewer, fixer: roles.Fixer}
}

type reviewAgents struct {
	primary  Agent
	reviewer RoundedRole
	fixer    RoundedRole
}

func (a *reviewAgents) Name() string { return a.primary.Name() }

// fixAgents lists every agent that can serve a fix turn in this run. Session
// capability is reported for the whole set, not for the first round's fixer:
// a later-round fixer that cannot resume must not inherit an earlier one's
// capability, because the session it would be handed is not resumable.
func (a *reviewAgents) fixAgents() []Agent {
	candidates := a.fixer.agents()
	if len(candidates) == 0 {
		return []Agent{a.primary}
	}
	if a.fixer.Agent == nil {
		return append([]Agent{a.primary}, candidates...)
	}
	return candidates
}

func (a *reviewAgents) SupportsSessionResume() bool {
	for _, current := range a.fixAgents() {
		if !SupportsSessionResume(current) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) SupportsSessionProvider(provider string) bool {
	for _, current := range a.fixAgents() {
		if !SupportsSessionProvider(current, provider) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) ReportsAgentAttempts() bool { return true }

func (a *reviewAgents) all() []Agent {
	return append([]Agent{a.primary}, append(a.reviewer.agents(), a.fixer.agents()...)...)
}

func (a *reviewAgents) NeutralizesGateInstructions() bool {
	for _, current := range a.all() {
		if !NeutralizesGateInstructions(current) {
			return false
		}
	}
	return true
}

func (a *reviewAgents) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	selected := a.primary
	switch opts.Purpose {
	case "review", "review-coverage":
		// "review-coverage" is the review step's focused coverage completion
		// turn: it is a review turn (same schema, same pass, no code change in
		// between) and must route through the same reviewer chain.
		selected = a.reviewer.pick(opts.Round, a.primary)
		opts.Session = nil
	case "review-fix":
		selected = a.fixer.pick(opts.Round, a.primary)
	}
	var label string
	if opts.Purpose == "review" || opts.Purpose == "review-coverage" {
		label = a.reviewer.label(opts.Round)
	}
	started := time.Now()
	result, err := selected.Run(ctx, opts)
	if err != nil && (opts.Purpose == "review" || opts.Purpose == "review-coverage") && IsUsageLimitError(err) {
		for i, fallback := range a.reviewer.Fallback {
			if ctx.Err() != nil {
				break
			}
			selected = fallback
			label = a.reviewer.fallbackLabel(i, fallback)
			result, err = selected.Run(ctx, opts)
			if err == nil || !IsUsageLimitError(err) {
				break
			}
		}
	}
	if !ReportsAgentAttempts(selected) {
		emitAgentAttempt(opts, selected.Name(), result, err, started, time.Now())
	}
	if result != nil {
		if label != "" {
			result.AgentIdentity = label
		} else {
			result.AgentIdentity = selected.Name()
		}
		if result.Provider == "" {
			result.Provider = selected.Name()
		}
	}
	return result, err
}

func (a *reviewAgents) Close() error {
	var errs []error
	for _, current := range a.all() {
		errs = append(errs, current.Close())
	}
	return errors.Join(errs...)
}
