package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/runenv"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

type FreeModelKind string

const (
	FreeModelMinimax FreeModelKind = "minimax"
	FreeModelBeam    FreeModelKind = "beam"
)

var ErrFreeModelEmpty = errors.New("free model returned an empty answer")

var ErrFreeModelUnsupported = errors.New("agent chain has no free model runner")

func ForwardFreeModel(a Agent, ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error) {
	runner, ok := a.(FreeModelRunner)
	if !ok {
		return nil, ErrFreeModelUnsupported
	}
	return runner.RunFreeModel(ctx, kind, opts)
}

func ForwardFallbackModel(a Agent, ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error) {
	runner, ok := a.(FreeModelRunner)
	if !ok {
		return nil, ErrFreeModelUnsupported
	}
	return runner.RunFallbackModel(ctx, kind, opts)
}

type FreeModelSpec struct {
	Enabled       bool
	Model         string
	FallbackModel string
}

type FreeModelConfig struct {
	Harness                types.AgentName
	Bin                    string
	Args                   []string
	BaseProfile            agentcfg.Profile
	ACPRegistryOverrides   map[string]string
	DisableProjectSettings bool
	Environment            runenv.Overlay
	SteeringRoot           string
	Minimax                FreeModelSpec
	Beam                   FreeModelSpec
}

func (c FreeModelConfig) spec(kind FreeModelKind) FreeModelSpec {
	if kind == FreeModelBeam {
		return c.Beam
	}
	return c.Minimax
}

type FreeModelRunner interface {
	RunFreeModel(ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error)
	RunFallbackModel(ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error)
}

func WithFreeModels(primary Agent, cfg FreeModelConfig) Agent {
	if primary == nil || (!cfg.Minimax.Enabled && !cfg.Beam.Enabled) {
		return primary
	}
	return &freeModelAgent{primary: primary, cfg: cfg}
}

type freeModelAgent struct {
	primary Agent
	cfg     FreeModelConfig
	mu      sync.Mutex
	built   map[FreeModelKind]map[string]Agent
}

func (a *freeModelAgent) Name() string { return a.primary.Name() }

func (a *freeModelAgent) Run(ctx context.Context, opts RunOpts) (*Result, error) {
	return a.primary.Run(ctx, opts)
}

func (a *freeModelAgent) SupportsSessionResume() bool {
	return SupportsSessionResume(a.primary)
}

func (a *freeModelAgent) SupportsSessionProvider(provider string) bool {
	return SupportsSessionProvider(a.primary, provider)
}

func (a *freeModelAgent) ReportsAgentAttempts() bool {
	return ReportsAgentAttempts(a.primary)
}

func (a *freeModelAgent) NeutralizesGateInstructions() bool {
	return NeutralizesGateInstructions(a.primary)
}

func (a *freeModelAgent) Close() error {
	a.mu.Lock()
	built := a.built
	a.built = nil
	a.mu.Unlock()
	errs := []error{a.primary.Close()}
	for _, models := range built {
		for _, ag := range models {
			errs = append(errs, ag.Close())
		}
	}
	return errors.Join(errs...)
}

func (a *freeModelAgent) RunFreeModel(ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error) {
	spec := a.cfg.spec(kind)
	if !spec.Enabled {
		return nil, fmt.Errorf("free model %q is not enabled", string(kind))
	}
	opts.Session = nil
	result, err := a.runModel(ctx, kind, spec.Model, opts, true)
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return nil, err
	}
	fallback, fallbackErr := a.runModel(ctx, kind, spec.FallbackModel, opts, false)
	if fallbackErr != nil {
		return nil, fallbackErr
	}
	return fallback, nil
}

func (a *freeModelAgent) RunFallbackModel(ctx context.Context, kind FreeModelKind, opts RunOpts) (*Result, error) {
	spec := a.cfg.spec(kind)
	if !spec.Enabled {
		return nil, fmt.Errorf("free model %q is not enabled", string(kind))
	}
	opts.Session = nil
	return a.runModel(ctx, kind, spec.FallbackModel, opts, false)
}

func (a *freeModelAgent) runModel(ctx context.Context, kind FreeModelKind, model string, opts RunOpts, retryEmpty bool) (*Result, error) {
	ag, err := a.agentFor(kind, model)
	if err != nil {
		return nil, err
	}
	result, err := ag.Run(ctx, opts)
	if err != nil {
		return nil, err
	}
	if retryEmpty && freeModelAnswerEmpty(result) {
		result, err = ag.Run(ctx, opts)
		if err != nil {
			return nil, err
		}
	}
	if freeModelAnswerEmpty(result) {
		return nil, fmt.Errorf("%s model %q: %w", string(kind), model, ErrFreeModelEmpty)
	}
	return result, nil
}

func (a *freeModelAgent) agentFor(kind FreeModelKind, model string) (Agent, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if ag := a.built[kind][model]; ag != nil {
		return ag, nil
	}
	profile := a.cfg.BaseProfile
	profile.Model = strings.TrimSpace(model)
	built, err := NewWithOptions(a.cfg.Harness, a.cfg.Bin, a.cfg.Args, Options{
		ACPRegistryOverrides:   a.cfg.ACPRegistryOverrides,
		Environment:            a.cfg.Environment,
		DisableProjectSettings: a.cfg.DisableProjectSettings,
		Profile:                profile,
	})
	if err != nil {
		return nil, fmt.Errorf("free model %q (%s): %w", string(kind), model, err)
	}
	built = WithSteering(built, a.cfg.SteeringRoot)
	if a.cfg.DisableProjectSettings && !NeutralizesGateInstructions(built) {
		_ = built.Close()
		return nil, fmt.Errorf("free model %q (%s) has no verified project-settings suppression", string(kind), model)
	}
	if a.built == nil {
		a.built = map[FreeModelKind]map[string]Agent{}
	}
	if a.built[kind] == nil {
		a.built[kind] = map[string]Agent{}
	}
	a.built[kind][model] = built
	return built, nil
}

func freeModelAnswerEmpty(result *Result) bool {
	if result == nil {
		return true
	}
	return len(bytes.TrimSpace(result.Output)) == 0 && strings.TrimSpace(result.Text) == ""
}
