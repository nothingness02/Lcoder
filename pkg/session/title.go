package session

import (
	"encoding/json"
	"strings"

	"github.com/lcoder/lcoder/pkg/models"
)

// titleCustomType is the custom-entry type that carries an explicit,
// user-assigned session title. The latest entry on the active branch wins.
const titleCustomType = "lcoder/title"

// titleMaxRunes caps a derived title (explicit titles are stored as-is).
const titleMaxRunes = 40

// SetTitle persists an explicit session title. Appending a new entry
// supersedes earlier ones (the latest on the active branch wins).
func (s *Session) SetTitle(title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return nil
	}
	data, err := json.Marshal(title)
	if err != nil {
		return err
	}
	return s.AppendCustomEntry(titleCustomType, data)
}

// Title returns the explicit, user-assigned title, or "" when none was set.
func (s *Session) Title() string {
	entries := s.CustomEntries(titleCustomType)
	if len(entries) == 0 {
		return ""
	}
	var title string
	if err := json.Unmarshal(entries[len(entries)-1].Data, &title); err != nil {
		return ""
	}
	return title
}

// DisplayTitle is the picker-facing label: an explicit title when set,
// otherwise the latest user message on the active branch (whitespace
// collapsed, truncated), otherwise the session ID.
func (s *Session) DisplayTitle() string {
	if t := s.Title(); t != "" {
		return t
	}
	if t := latestUserText(s.ActiveMessages()); t != "" {
		return truncateRunes(t, titleMaxRunes)
	}
	return s.ID
}

// latestUserText returns the whitespace-collapsed text of the last user
// message, or "" when there is none.
func latestUserText(msgs []models.AgentMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != models.RoleUser {
			continue
		}
		if t := collapseWhitespace(msgs[i].Text()); t != "" {
			return t
		}
	}
	return ""
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateRunes truncates s to n runes, appending an ellipsis when cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
