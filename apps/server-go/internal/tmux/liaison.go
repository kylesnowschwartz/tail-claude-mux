package tmux

import "regexp"

// The liaison link options. The liaison's hand-out command sets them on the
// pane of a conversation it starts. tcm reads them and writes none.
const (
	LiaisonWorkOption   = "@liaison-work"
	LiaisonParentOption = "@liaison-parent"
	LiaisonWorkerOption = "@liaison-worker"
)

var (
	// liaisonWorkIDForm is a work ID: the letter w and 1 to 6 digits.
	liaisonWorkIDForm = regexp.MustCompile(`\Aw[0-9]{1,6}\z`)
	// conversationIDForm is a Claude Code conversation ID: a UUID.
	conversationIDForm = regexp.MustCompile(`\A[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\z`)
)

// validOr returns value when it has the form, and "" otherwise.
func validOr(value string, form *regexp.Regexp) string {
	if form.MatchString(value) {
		return value
	}
	return ""
}
