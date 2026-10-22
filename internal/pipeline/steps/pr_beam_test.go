package steps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/scm"
)

func beamPRContent(title, body string) *agent.Result {
	raw, _ := json.Marshal(map[string]string{"title": title, "body": body})
	return &agent.Result{Output: json.RawMessage(raw)}
}

func TestParsePRDraftContent(t *testing.T) {
	t.Parallel()
	content, ok := parsePRDraftContent(beamPRContent("feat: add thing", "## What Changed\n- thing").Output)
	if !ok || content.Title != "feat: add thing" {
		t.Fatalf("content = %+v, ok = %v", content, ok)
	}
	nested, _ := json.Marshal(map[string]string{"title": "feat: add thing", "body": string(mustJSON(t, prContent{Title: "x", Body: "## What Changed\n- nested"}))})
	_ = nested
	if _, ok := parsePRDraftContent([]byte("not json")); ok {
		t.Fatal("invalid JSON parsed")
	}
	if _, ok := parsePRDraftContent(beamPRContent("", "## What Changed\n- thing").Output); ok {
		t.Fatal("empty title parsed")
	}
	if _, ok := parsePRDraftContent(beamPRContent("feat: x", "").Output); ok {
		t.Fatal("empty body parsed")
	}
}

func mustJSON(t *testing.T, v prContent) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePRDraftTitle(t *testing.T) {
	t.Parallel()
	title, ok := parsePRDraftTitle(beamPRContent("feat: add thing", "x").Output)
	if !ok || title != "feat: add thing" {
		t.Fatalf("title = %q, ok = %v", title, ok)
	}
	if _, ok := parsePRDraftTitle(beamPRContent(" ", "x").Output); ok {
		t.Fatal("blank title parsed")
	}
}

func TestBeamPRContent_ServesDraft(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("feat: add thing", "## What Changed\n- thing"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	step := &PRStep{}
	content, handled := step.draftPRContentWithBeam(sctx, "prompt", "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub)
	if !handled {
		t.Fatal("beam draft not handled")
	}
	if !strings.HasPrefix(content.Title, "feat:") {
		t.Fatalf("title = %q, want the rendered conventional title", content.Title)
	}
	if !strings.Contains(content.Body, "## What Changed") {
		t.Fatalf("body lost the draft section: %q", content.Body)
	}
}

func TestBeamPRContent_FailureDeclines(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return nil, errors.New("boom")
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	step := &PRStep{}
	if _, handled := step.draftPRContentWithBeam(sctx, "prompt", "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub); handled {
		t.Fatal("failed beam draft handled")
	}
	if ag.freeCalls != 1 || ag.fallbackCalls != 1 {
		t.Fatalf("calls = free %d fallback %d, want the free failure to reach the fallback turn", ag.freeCalls, ag.fallbackCalls)
	}
}

func TestBeamPRContent_GarbageRetriesFallback(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("", ""), nil
	}
	ag.fallbackFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("feat: add thing", "## What Changed\n- thing"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	step := &PRStep{}
	content, handled := step.draftPRContentWithBeam(sctx, "prompt", "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub)
	if !handled || !strings.HasPrefix(content.Title, "feat:") {
		t.Fatalf("content = %+v, handled = %v", content, handled)
	}
	if ag.fallbackCalls != 1 {
		t.Fatalf("fallback calls = %d, want 1", ag.fallbackCalls)
	}
}

func TestBeamPRTitle_ServesDraft(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("feat: add thing", "## What Changed\n- thing"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	step := &PRStep{}
	title, handled := step.draftConfiguredPRTitleWithBeam(sctx, "prompt")
	if !handled || !strings.HasPrefix(title, "feat:") {
		t.Fatalf("title = %q, handled = %v", title, handled)
	}
}
