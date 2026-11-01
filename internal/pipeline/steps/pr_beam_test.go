package steps

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/agent"
	"github.com/kunchenguid/no-mistakes/internal/config"
	"github.com/kunchenguid/no-mistakes/internal/scm"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func beamPRContent(title, body string) *agent.Result {
	raw, _ := json.Marshal(map[string]string{"title": title, "body": body})
	return &agent.Result{Output: json.RawMessage(raw)}
}

func TestParsePRDraftContent(t *testing.T) {
	t.Parallel()
	content, ok := parsePRDraftContent(nil, beamPRContent("feat: add thing", "## What Changed\n- thing").Output)
	if !ok || content.Title != "feat: add thing" {
		t.Fatalf("content = %+v, ok = %v", content, ok)
	}
	nested, _ := json.Marshal(map[string]string{"title": "feat: add thing", "body": string(mustJSON(t, prContent{Title: "x", Body: "## What Changed\n- nested"}))})
	_ = nested
	if _, ok := parsePRDraftContent(nil, []byte("not json")); ok {
		t.Fatal("invalid JSON parsed")
	}
	if _, ok := parsePRDraftContent(nil, beamPRContent("", "## What Changed\n- thing").Output); ok {
		t.Fatal("empty title parsed")
	}
	if _, ok := parsePRDraftContent(nil, beamPRContent("feat: x", "").Output); ok {
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
	title, ok := parsePRDraftTitle(nil, beamPRContent("feat: add thing", "x").Output)
	if !ok || title.Title != "feat: add thing" {
		t.Fatalf("title = %q, ok = %v", title.Title, ok)
	}
	if _, ok := parsePRDraftTitle(nil, beamPRContent(" ", "x").Output); ok {
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
	content, handled, err := step.draftPRContentWithBeam(sctx, "prompt", prContentSchema, "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
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
	if _, handled, _ := step.draftPRContentWithBeam(sctx, "prompt", prContentSchema, "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub); handled {
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
	content, handled, err := step.draftPRContentWithBeam(sctx, "prompt", prContentSchema, "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub)
	if err != nil {
		t.Fatal(err)
	}
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
	title, handled := step.draftConfiguredPRTitleWithBeam(sctx, "prompt", prTitleSchema)
	if !handled || !strings.HasPrefix(title, "feat:") {
		t.Fatalf("title = %q, handled = %v", title, handled)
	}
}

func beamPRContentWithBrief(t *testing.T, title, body string) *agent.Result {
	t.Helper()
	return &agent.Result{Output: mustJSON(t, prContent{Title: title, Body: body, IntentBrief: &prIntentBrief{
		Goal:       "Validate the submitted change",
		Why:        []string{"Catch mistakes before execution"},
		Scope:      []string{"Check configured terms"},
		OutOfScope: []string{"Keep the default behavior unchanged"},
	}})}
}

func TestBeamPRContent_IntentBriefIsRequestedAndPublishedNotDropped(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(_ context.Context, _ agent.FreeModelKind, opts agent.RunOpts) (*agent.Result, error) {
		if !hasPRIntentBriefSchema(t, opts.JSONSchema) {
			t.Fatalf("beam draft schema lacks the Intent brief contract: %s", opts.JSONSchema)
		}
		return beamPRContentWithBrief(t, "feat: add thing", "## What Changed\n- thing"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.UserIntent = "private raw intent"
	sctx.Config.PR.Intent.Format = "brief"
	content, handled, err := (&PRStep{}).draftPRContentWithBeam(sctx, "prompt", prContentBriefSchema, "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub)
	if err != nil || !handled {
		t.Fatalf("handled = %v, err = %v", handled, err)
	}
	if !strings.Contains(sctx.PublicIntentBrief, "Validate the submitted change") {
		t.Fatalf("public Intent brief = %q, want the beam draft's brief", sctx.PublicIntentBrief)
	}
	if strings.Contains(content.Body, "private raw intent") {
		t.Fatalf("raw intent leaked into the body:\n%s", content.Body)
	}
}

func TestBeamPRContent_MissingIntentBriefDeclinesInsteadOfPublishingWithoutIt(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	noBrief := func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("feat: add thing", "## What Changed\n- thing"), nil
	}
	ag.freeFn, ag.fallbackFn = noBrief, noBrief
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.UserIntent = "private raw intent"
	sctx.Config.PR.Intent.Format = "brief"
	if _, handled, err := (&PRStep{}).draftPRContentWithBeam(sctx, "prompt", prContentBriefSchema, "stat", "risk", "testing", "pipeline", 0, scm.ProviderGitHub); handled || err != nil {
		t.Fatalf("handled = %v, err = %v; want the configured model to draft", handled, err)
	}
	if sctx.PublicIntentBrief != "" {
		t.Fatalf("public Intent brief = %q, want none from a declined draft", sctx.PublicIntentBrief)
	}
}

func TestBeamPRContent_KeepModeStillCarriesTheAttestation(t *testing.T) {
	t.Parallel()
	dir, baseSHA, headSHA := setupGitRepo(t)
	ag := newMockFreeModelAgent(nil)
	ag.freeFn = func(context.Context, agent.FreeModelKind, agent.RunOpts) (*agent.Result, error) {
		return beamPRContent("feat: add thing", "## What Changed\n- thing"), nil
	}
	sctx := freeModelStepContext(t, ag, dir, baseSHA, headSHA)
	sctx.Config.PR.UpdateText = config.PRUpdateTextKeep
	insertCompletedStep(t, sctx, types.StepReview, "", "")
	content, handled, err := (&PRStep{}).draftPRContentWithBeam(sctx, "prompt", prContentSchema, "stat", "", "", "", 0, scm.ProviderGitHub)
	if err != nil || !handled {
		t.Fatalf("handled = %v, err = %v", handled, err)
	}
	if publishedAttestationHead(t, content.Body) != headSHA {
		t.Fatalf("kept beam draft lacks the head attestation:\n%s", content.Body)
	}
}
