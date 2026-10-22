package pipeline

import (
	"strings"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

const extraReviewGateJSON = `
{"findings":[
{"id":"r1","severity":"error","description":"Blocking problem","action":"auto-fix"},
{"id":"question-q1","severity":"info","description":"Advisory: extra note","action":"no-op","category":"extra-review"}
]}
`

func TestDropExtraReviewFindingsJSON(t *testing.T) {
	t.Parallel()
	kept := dropExtraReviewFindingsJSON(extraReviewGateJSON)
	parsed, err := types.ParseFindingsJSON(kept)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Items) != 1 || parsed.Items[0].ID != "r1" {
		t.Fatalf("kept = %+v, want only r1", parsed.Items)
	}
	if kept := dropExtraReviewFindingsJSON(""); kept != "" {
		t.Fatalf("empty input = %q", kept)
	}
}

func TestSplitFixResponse_RefusesAdvisoryInFixSet(t *testing.T) {
	t.Parallel()
	_, err := splitFixResponse(extraReviewGateJSON, nil, []string{"question-q1"}, nil)
	refusal, ok := err.(*RespondRefusal)
	if !ok {
		t.Fatalf("err = %v, want a refusal", err)
	}
	if !strings.Contains(refusal.Message, "question-q1") || !strings.Contains(refusal.Message, "--ignore") {
		t.Fatalf("refusal = %q, want the id redirected to --ignore", refusal.Message)
	}
	dispositions, err := splitFixResponse(extraReviewGateJSON, nil, []string{"r1"}, []string{"question-q1"})
	if err != nil {
		t.Fatalf("ordinary fix plus ignored advisory refused: %v", err)
	}
	if len(dispositions.Fixed) != 1 || dispositions.Fixed[0] != "r1" {
		t.Fatalf("dispositions = %+v, want r1 fixed", dispositions)
	}
}
