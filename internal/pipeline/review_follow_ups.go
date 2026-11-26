package pipeline

import (
	"fmt"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func (e *Executor) ReviewGateDeferral(stepResultID, roundID string) *bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.waiting || e.waitingStep != types.StepReview || e.waitingStepResultID != stepResultID ||
		roundID == "" || e.waitingRoundID != roundID || e.db == nil {
		return nil
	}
	rounds, err := e.db.GetRoundsByStep(stepResultID)
	if err != nil {
		return nil
	}
	round := e.parkedReviewRound(rounds)
	if round == nil {
		return nil
	}
	applies := e.reviewDeferralApplies(round)
	return &applies
}

func (e *Executor) parkedReviewRound(rounds []*db.StepRound) *db.StepRound {
	for _, round := range rounds {
		if round.ID == e.waitingRoundID {
			return round
		}
	}
	return nil
}

func (e *Executor) reviewDeferralApplies(round *db.StepRound) bool {
	return round != nil && round.Round >= 2 && e.config != nil && e.config.Review.DeferNonErrors
}

func (e *Executor) normalizeLaterReviewFix(response *approvalResponse, rounds []*db.StepRound) ([]string, error) {
	parkedRound := e.parkedReviewRound(rounds)
	if !e.reviewDeferralApplies(parkedRound) {
		return nil, nil
	}

	gate, err := types.ParseFindingsJSON(e.waitingFindings)
	if err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(response.findingIDs))
	for _, id := range response.findingIDs {
		selected[id] = true
	}
	var errorsSelected []string
	var addedErrors []types.Finding
	var deferred []types.Finding
	for _, finding := range gate.Items {
		if !selected[finding.ID] {
			continue
		}
		if finding.Severity == "error" {
			errorsSelected = append(errorsSelected, finding.ID)
		} else if isReviewFollowUp(finding) {
			deferred = append(deferred, finding)
		}
	}
	for _, finding := range response.addedFindings {
		if finding.Severity == "error" {
			addedErrors = append(addedErrors, finding)
		} else if isReviewFollowUp(finding) {
			deferred = append(deferred, finding)
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
	return attachReviewFollowUps(response, parkedRound, deferred)
}

func (e *Executor) prepareReviewFollowUpApproval(step types.StepName, response *approvalResponse) ([]string, error) {
	if step != types.StepReview {
		return nil, fmt.Errorf("approval with follow-ups applies only to Review")
	}
	rounds, refusal := e.roundsForFixValidation()
	if refusal != nil {
		return nil, refusal
	}
	round := e.parkedReviewRound(rounds)
	if !e.reviewDeferralApplies(round) {
		return nil, fmt.Errorf("approval with follow-ups requires review.defer_non_errors and at least two Review rounds")
	}
	gate, err := types.ParseFindingsJSON(e.waitingFindings)
	if err != nil {
		return nil, fmt.Errorf("read parked Review findings: %w", err)
	}
	if err := e.validateReviewFollowUpApproval(gate, rounds, round); err != nil {
		return nil, err
	}
	if round.SelectedFindingIDs != nil && len(findingIDsFromSelectionJSON(*round.SelectedFindingIDs)) > 0 {
		return nil, fmt.Errorf("this Review round already has a fix selection; resend the fix response or approve without follow-ups")
	}
	ids := uniqueNonEmpty(response.findingIDs)
	if len(ids) == 0 {
		return nil, fmt.Errorf("approval with follow-ups requires at least one finding ID")
	}
	requested := make(map[string]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	var deferred []types.Finding
	for _, finding := range gate.Items {
		if !requested[finding.ID] {
			continue
		}
		if !isReviewFollowUp(finding) {
			return nil, fmt.Errorf("Review finding %s cannot be recorded as a follow-up", finding.ID)
		}
		deferred = append(deferred, finding)
		delete(requested, finding.ID)
	}
	for _, id := range ids {
		if requested[id] {
			return nil, fmt.Errorf("Review finding %s is not shown at this gate", id)
		}
	}
	response.findingIDs = nil
	return attachReviewFollowUps(response, round, deferred)
}

func attachReviewFollowUps(response *approvalResponse, round *db.StepRound, deferred []types.Finding) ([]string, error) {
	recorded := types.Findings{}
	if round.ReviewFollowUpsJSON != nil {
		var err error
		recorded, err = types.ParseFindingsJSON(*round.ReviewFollowUpsJSON)
		if err != nil {
			return nil, err
		}
	}
	recordedKeys := make(reviewFollowUpKeys)
	recordedKeys.add(recorded.Items)
	var deferredIDs []string
	for _, finding := range deferred {
		deferredIDs = append(deferredIDs, finding.ID)
		key := findingKey(finding)
		if !recordedKeys[key] {
			recorded.Items = append(recorded.Items, finding)
			recordedKeys[key] = true
		}
	}
	raw, err := types.MarshalFindingsJSON(recorded)
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
		return fmt.Errorf("cannot approve a Review gate with unresolved questions through deferral")
	}
	if len(gate.Items) == 0 || parkedRound.ReviewedHeadSHA == nil || *parkedRound.ReviewedHeadSHA == "" {
		return fmt.Errorf("deferral approval requires a Review findings gate with a reviewed head")
	}
	chosen := earlierChosenToFixIDs(rounds, e.waitingFindings)
	for _, finding := range gate.Items {
		if finding.Severity == "error" && chosen[finding.ID] {
			return fmt.Errorf("Review finding %s was selected earlier and remains unresolved; select it again for a fix or approve without deferral", finding.ID)
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
