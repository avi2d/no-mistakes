package pipeline

import (
	"encoding/json"
	"sort"

	"github.com/kunchenguid/no-mistakes/internal/db"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

func ReviewRoundMarks(rounds []*db.StepRound) (map[string]int, []int) {
	ordered := make([]*db.StepRound, 0, len(rounds))
	for _, r := range rounds {
		if r != nil {
			ordered = append(ordered, r)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Round < ordered[j].Round })
	return repeatMarks(ordered), noChangeFixRounds(ordered)
}

func repeatMarks(ordered []*db.StepRound) map[string]int {
	if len(ordered) < 2 {
		return nil
	}
	prev := ordered[len(ordered)-2]
	cur := ordered[len(ordered)-1]
	prevFindings, err := types.ParseFindingsJSON(roundString(prev.FindingsJSON))
	if err != nil {
		return nil
	}
	curFindings, err := types.ParseFindingsJSON(roundString(cur.FindingsJSON))
	if err != nil {
		return nil
	}
	prevByID := make(map[string]types.Finding, len(prevFindings.Items))
	for _, f := range prevFindings.Items {
		prevByID[f.ID] = f
	}
	type locator struct {
		file string
		line int
	}
	var anchors []locator
	for _, id := range roundIDs(prev.SelectedFindingIDs) {
		f, ok := prevByID[id]
		if !ok || f.File == "" {
			continue
		}
		// A no-op finding never parks a gate, so its repeat carries no stop signal.
		if f.ActionOrDefault() == types.ActionNoOp {
			continue
		}
		anchors = append(anchors, locator{f.File, f.Line})
	}
	if len(anchors) == 0 {
		return nil
	}
	var out map[string]int
	for _, f := range curFindings.Items {
		if f.File == "" {
			continue
		}
		for _, a := range anchors {
			if f.File == a.file && roundAbs(f.Line-a.line) <= 3 {
				if out == nil {
					out = make(map[string]int)
				}
				out[f.ID] = prev.Round
				break
			}
		}
	}
	return out
}

func noChangeFixRounds(ordered []*db.StepRound) []int {
	var out []int
	for i := 1; i+1 < len(ordered); i++ {
		if !ordered[i].IsFixRound() {
			continue
		}
		before := roundString(ordered[i-1].ReviewedHeadSHA)
		after := roundString(ordered[i+1].ReviewedHeadSHA)
		if before != "" && before == after {
			out = append(out, ordered[i].Round)
		}
	}
	return out
}

func roundIDs(raw *string) []string {
	if raw == nil || *raw == "" {
		return nil
	}
	var ids []string
	if json.Unmarshal([]byte(*raw), &ids) != nil {
		return nil
	}
	return ids
}

func roundString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func roundAbs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
