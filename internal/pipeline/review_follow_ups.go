package pipeline

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func (e *Executor) normalizeLaterReviewFix(response *approvalResponse, rounds []*db.StepRound) ([]string, error) {
	if e.config == nil || !e.config.Review.DeferNonErrors {
		return nil, nil
	}
	var parkedRound *db.StepRound
	for _, round := range rounds {
		if round.ID == e.waitingRoundID {
			parkedRound = round
			break
		}
	}
	if parkedRound == nil || parkedRound.Round < 2 {
		return nil, nil
	}

	gate, err := types.ParseFindingsJSON(e.waitingFindings)
	if err != nil {
		return nil, err
	}
	previousFollowUps := types.Findings{}
	if parkedRound.ReviewFollowUpsJSON != nil {
		previousFollowUps, err = types.ParseFindingsJSON(*parkedRound.ReviewFollowUpsJSON)
		if err != nil {
			return nil, err
		}
	}
	recordedKeys := make(reviewFollowUpKeys)
	recordedKeys.add(previousFollowUps.Items)
	selected := make(map[string]bool, len(response.findingIDs))
	for _, id := range response.findingIDs {
		selected[id] = true
	}
	var errorsSelected []string
	var addedErrors []types.Finding
	var deferredIDs []string
	deferFinding := func(finding types.Finding) {
		if !isReviewFollowUp(finding) {
			return
		}
		deferredIDs = append(deferredIDs, finding.ID)
		if key := findingKey(finding); !recordedKeys[key] {
			previousFollowUps.Items = append(previousFollowUps.Items, finding)
			recordedKeys[key] = true
		}
	}
	for _, finding := range gate.Items {
		if !selected[finding.ID] {
			continue
		}
		if finding.Severity == "error" {
			errorsSelected = append(errorsSelected, finding.ID)
		} else {
			deferFinding(finding)
		}
	}
	for _, finding := range response.addedFindings {
		if finding.Severity == "error" {
			addedErrors = append(addedErrors, finding)
		} else {
			deferFinding(finding)
		}
	}
	if len(errorsSelected) == 0 && len(addedErrors) == 0 {
		if err := e.validateReviewFollowUpApproval(gate, rounds, parkedRound); err != nil {
			return nil, err
		}
		response.action = types.ActionApprove
	}
	response.findingIDs = errorsSelected
	response.addedFindings = addedErrors
	raw, err := types.MarshalFindingsJSON(previousFollowUps)
	if err != nil {
		return nil, err
	}
	response.reviewFollowUps = &raw
	return deferredIDs, nil
}

func (e *Executor) validateReviewFollowUpApproval(gate types.Findings, rounds []*db.StepRound, parkedRound *db.StepRound) error {
	if e.waitingApprovalRefusal != "" {
		return fmt.Errorf("%s", e.waitingApprovalRefusal)
	}
	if HasUnansweredReviewQuestion(e.waitingFindings) || HasUnreadableReviewQuestionHistory(e.waitingFindings) {
		return fmt.Errorf("cannot approve a Review gate with unresolved questions through a fix response")
	}
	if len(gate.Items) == 0 || parkedRound.ReviewedHeadSHA == nil || *parkedRound.ReviewedHeadSHA == "" {
		return fmt.Errorf("no selected Review error to fix and no findings gate to approve")
	}
	chosen := earlierChosenToFixIDs(rounds, e.waitingFindings)
	for _, finding := range gate.Items {
		if finding.Severity == "error" && chosen[finding.ID] {
			return fmt.Errorf("Review finding %s was selected earlier and remains unresolved; select it again or explicitly approve the gate", finding.ID)
		}
	}
	return nil
}

func isReviewFollowUp(finding types.Finding) bool {
	return (finding.Severity == "warning" || finding.Severity == "info") &&
		!IsReservedFindingID(finding.ID) && finding.Category != types.FindingCategoryReviewQuestion
}

type reviewFollowUpKeys map[types.Finding]bool

func (keys reviewFollowUpKeys) add(findings []types.Finding) {
	for _, finding := range findings {
		keys[findingKey(finding)] = true
	}
}

func (keys reviewFollowUpKeys) addJSON(raw *string) {
	if raw != nil {
		findings, _ := types.ParseFindingsJSON(*raw)
		keys.add(findings.Items)
	}
}

func (keys reviewFollowUpKeys) excludeJSON(raw string) string {
	if len(keys) == 0 || raw == "" {
		return raw
	}
	findings, err := types.ParseFindingsJSON(raw)
	if err != nil {
		return raw
	}
	kept := findings.Items[:0]
	for _, finding := range findings.Items {
		if !keys[findingKey(finding)] {
			kept = append(kept, finding)
		}
	}
	findings.Items = kept
	encoded, err := types.MarshalFindingsJSON(findings)
	if err != nil {
		return raw
	}
	return encoded
}
