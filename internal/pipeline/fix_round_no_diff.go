package pipeline

import (
	"fmt"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/types"
)

// rederivedNoticeIDs name the gate notices each round derives afresh. They
// carry no file anchor, so carrying one forward would block positive
// verification of every selected finding.
var rederivedNoticeIDs = []string{types.FindingIDReviewFixRoundsExhausted, types.FindingIDFixRoundNoDiff}

// HasFixRoundNoDiff identifies a gate parked because a fix round landed no
// diff. Automatic resolvers must leave it for a human: another automatic fix
// is the round that already changed nothing, and approval would pass the
// findings it left unfixed.
func HasFixRoundNoDiff(raw string) bool {
	return hasFindingID(raw, types.FindingIDFixRoundNoDiff)
}

// fixRoundLandedNoDiff reports whether the round that just ran dispatched
// code findings to a fixer and left the run's head where the round started.
// A round whose selection is only pipeline notices, such as a Test budget
// cut, re-validates without a fixer, so its unchanged head proves nothing.
func fixRoundLandedNoDiff(sctx *StepContext, roundStartHead string) bool {
	if !sctx.Fixing || sctx.SkipFixExecution || strings.TrimSpace(sctx.Run.HeadSHA) != strings.TrimSpace(roundStartHead) {
		return false
	}
	selected, err := types.ParseFindingsJSON(sctx.PreviousFindings)
	if err != nil {
		return false
	}
	for _, f := range selected.Items {
		if !IsReservedFindingID(f.ID) {
			return true
		}
	}
	return false
}

func fixRoundNoDiffMessage(step types.StepName, round int) string {
	return fmt.Sprintf("fix_round_no_diff: the %s fix round in round %d landed no diff, so no further automatic fix round starts; decide the remaining findings: fix with instructions, approve, or skip", step, round)
}

func appendFixRoundNoDiff(raw string, step types.StepName, round int) (string, error) {
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return "", err
	}
	findings.Items = append(findings.Items, types.Finding{
		ID:          types.FindingIDFixRoundNoDiff,
		Severity:    "warning",
		Action:      types.ActionAskUser,
		Description: fixRoundNoDiffMessage(step, round),
	})
	return types.MarshalFindingsJSON(findings)
}
