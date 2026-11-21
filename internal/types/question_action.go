package types

import "encoding/json"

type QuestionAction string

const (
	QuestionActionDelete  QuestionAction = "delete"
	QuestionActionMerge   QuestionAction = "merge"
	QuestionActionRelease QuestionAction = "release"
	QuestionActionLogin   QuestionAction = "login"
	QuestionActionPush    QuestionAction = "push"
	QuestionActionNone    QuestionAction = "none"
	QuestionActionUnknown QuestionAction = "unknown"
)

func (a QuestionAction) OrUnknown() QuestionAction {
	switch a {
	case QuestionActionDelete, QuestionActionMerge, QuestionActionRelease, QuestionActionLogin, QuestionActionPush, QuestionActionNone:
		return a
	default:
		return QuestionActionUnknown
	}
}

func (a *QuestionAction) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		*a = QuestionActionUnknown
		return nil
	}
	*a = QuestionAction(value).OrUnknown()
	return nil
}
