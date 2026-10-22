package agent

import (
	"context"
	"errors"
	"testing"
)

type stubFreeAgent struct {
	name    string
	results []*Result
	errs    []error
	calls   []RunOpts
	closed  int
}

func (s *stubFreeAgent) Name() string { return s.name }

func (s *stubFreeAgent) Run(_ context.Context, opts RunOpts) (*Result, error) {
	s.calls = append(s.calls, opts)
	if opts.Session != nil {
		return nil, errors.New("free turn must run session-free")
	}
	var result *Result
	if len(s.results) > 0 {
		result = s.results[0]
		s.results = s.results[1:]
	}
	var err error
	if len(s.errs) > 0 {
		err = s.errs[0]
		s.errs = s.errs[1:]
	}
	return result, err
}

func (s *stubFreeAgent) Close() error {
	s.closed++
	return nil
}

func freeModelTestAgent(free, fallback *stubFreeAgent) *freeModelAgent {
	return &freeModelAgent{
		primary: &stubFreeAgent{name: "primary"},
		cfg: FreeModelConfig{
			Minimax: FreeModelSpec{Enabled: true, Model: "free", FallbackModel: "fallback"},
		},
		built: map[FreeModelKind]map[string]Agent{
			FreeModelMinimax: {"free": free, "fallback": fallback},
		},
	}
}

func TestFreeModel_DisabledReturnsPrimary(t *testing.T) {
	primary := &stubFreeAgent{name: "primary"}
	if got := WithFreeModels(primary, FreeModelConfig{}); got != primary {
		t.Fatal("disabled free models wrapped the primary agent")
	}
}

func TestFreeModel_FreeSuccessNeedsNoFallback(t *testing.T) {
	free := &stubFreeAgent{name: "free", results: []*Result{{Text: "notes"}}}
	fallback := &stubFreeAgent{name: "fallback"}
	a := freeModelTestAgent(free, fallback)
	result, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{})
	if err != nil || result.Text != "notes" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(fallback.calls) != 0 {
		t.Fatal("fallback ran after a successful free turn")
	}
}

func TestFreeModel_FreeErrorFallsBack(t *testing.T) {
	free := &stubFreeAgent{name: "free", errs: []error{errors.New("boom")}}
	fallback := &stubFreeAgent{name: "fallback", results: []*Result{{Text: "fallback notes"}}}
	a := freeModelTestAgent(free, fallback)
	result, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{})
	if err != nil || result.Text != "fallback notes" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestFreeModel_EmptyAnswerRetriesOnce(t *testing.T) {
	free := &stubFreeAgent{name: "free", results: []*Result{{}, {Text: "second try"}}}
	fallback := &stubFreeAgent{name: "fallback"}
	a := freeModelTestAgent(free, fallback)
	result, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{})
	if err != nil || result.Text != "second try" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(free.calls) != 2 {
		t.Fatalf("free calls = %d, want exactly one retry", len(free.calls))
	}
	if len(fallback.calls) != 0 {
		t.Fatal("fallback ran after the retry served")
	}
}

func TestFreeModel_PersistentlyEmptyFallsBack(t *testing.T) {
	free := &stubFreeAgent{name: "free", results: []*Result{{}, {}}}
	fallback := &stubFreeAgent{name: "fallback", results: []*Result{{Text: "fallback notes"}}}
	a := freeModelTestAgent(free, fallback)
	result, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{})
	if err != nil || result.Text != "fallback notes" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if len(free.calls) != 2 {
		t.Fatalf("free calls = %d, want one attempt plus one retry", len(free.calls))
	}
}

func TestFreeModel_AllEmptyReportsEmpty(t *testing.T) {
	free := &stubFreeAgent{name: "free", results: []*Result{{}, {}}}
	fallback := &stubFreeAgent{name: "fallback", results: []*Result{{}}}
	a := freeModelTestAgent(free, fallback)
	if _, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{}); !errors.Is(err, ErrFreeModelEmpty) {
		t.Fatalf("err = %v, want ErrFreeModelEmpty", err)
	}
}

func TestFreeModel_FallbackErrorSurfaces(t *testing.T) {
	fallbackErr := errors.New("fallback down")
	free := &stubFreeAgent{name: "free", errs: []error{errors.New("boom")}}
	fallback := &stubFreeAgent{name: "fallback", errs: []error{fallbackErr}}
	a := freeModelTestAgent(free, fallback)
	if _, err := a.RunFreeModel(context.Background(), FreeModelMinimax, RunOpts{}); !errors.Is(err, fallbackErr) {
		t.Fatalf("err = %v, want the fallback error", err)
	}
}

func TestFreeModel_CancelledSkipsFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	free := &stubFreeAgent{name: "free", errs: []error{errors.New("boom")}}
	fallback := &stubFreeAgent{name: "fallback"}
	a := freeModelTestAgent(free, fallback)
	if _, err := a.RunFreeModel(ctx, FreeModelMinimax, RunOpts{}); err == nil {
		t.Fatal("cancelled turn reported success")
	}
	if len(fallback.calls) != 0 {
		t.Fatal("fallback ran after cancellation")
	}
}

func TestFreeModel_CloseClosesBuilt(t *testing.T) {
	primary := &stubFreeAgent{name: "primary"}
	free := &stubFreeAgent{name: "free"}
	a := freeModelTestAgent(free, &stubFreeAgent{name: "fallback"})
	a.primary = primary
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if primary.closed != 1 || free.closed != 1 {
		t.Fatalf("closed = primary %d free %d", primary.closed, free.closed)
	}
}
