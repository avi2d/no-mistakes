package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type chainFreeStub struct {
	freeServed     int
	fallbackServed int
}

func (s *chainFreeStub) Name() string { return "chain-stub" }

func (s *chainFreeStub) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	return &agent.Result{}, nil
}

func (s *chainFreeStub) Close() error { return nil }

func (s *chainFreeStub) RunFreeModel(ctx context.Context, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	s.freeServed++
	return &agent.Result{}, nil
}

func (s *chainFreeStub) RunFallbackModel(ctx context.Context, kind agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
	s.fallbackServed++
	return &agent.Result{}, nil
}

type chainPlainStub struct{}

func (s *chainPlainStub) Name() string { return "chain-plain-stub" }

func (s *chainPlainStub) Run(ctx context.Context, opts agent.RunOpts) (*agent.Result, error) {
	return &agent.Result{}, nil
}

func (s *chainPlainStub) Close() error { return nil }

func wrapProductionChain(inner agent.Agent) agent.Agent {
	ag := agent.WithReviewRoles(inner, agent.ReviewRoles{Reviewer: agent.RoundedRole{Agent: inner}})
	ag = &invocationFailureAgent{inner: ag}
	ag = &timeoutAgent{inner: ag, timeout: time.Minute, working: time.Minute}
	ag = &gateStepBoundaryAgent{inner: ag, phase: types.StepName("review")}
	ag = &lifecycleAgent{inner: ag}
	ag = &perfRecordingAgent{inner: ag}
	return &roundStampingAgent{inner: ag, round: func() int { return 1 }}
}

func TestProductionChainServesFreeModel(t *testing.T) {
	stub := &chainFreeStub{}
	top := wrapProductionChain(stub)
	runner, ok := top.(agent.FreeModelRunner)
	if !ok {
		t.Fatal("production wrapper chain hides the free model runner")
	}
	ctx := context.Background()
	if _, err := runner.RunFreeModel(ctx, agent.FreeModelMinimax, agent.RunOpts{}); err != nil {
		t.Fatalf("free turn through the chain: %v", err)
	}
	if stub.freeServed != 1 || stub.fallbackServed != 0 {
		t.Fatalf("free turn served free=%d fallback=%d, want 1 and 0", stub.freeServed, stub.fallbackServed)
	}
	if _, err := runner.RunFallbackModel(ctx, agent.FreeModelBeam, agent.RunOpts{}); err != nil {
		t.Fatalf("fallback turn through the chain: %v", err)
	}
	if stub.fallbackServed != 1 {
		t.Fatalf("fallback turn served fallback=%d, want 1", stub.fallbackServed)
	}
}

func TestProductionChainWithoutRunnerReportsUnsupported(t *testing.T) {
	top := wrapProductionChain(&chainPlainStub{})
	runner, ok := top.(agent.FreeModelRunner)
	if !ok {
		t.Fatal("wrappers must forward the free model interface even with no runner inside")
	}
	_, err := runner.RunFreeModel(context.Background(), agent.FreeModelMinimax, agent.RunOpts{})
	if !errors.Is(err, agent.ErrFreeModelUnsupported) {
		t.Fatalf("missing runner must report unsupported, got %v", err)
	}
}
