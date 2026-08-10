package session

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Mapping limits — kept in lockstep with the service-layer validation caps
// (internal/service: maxTitleLen=200, maxDescriptionLen=2000,
// maxRootContentLen=100000, maxContentLen=65536) so mapped input always
// passes service validation on import.
const (
	maxTitleLen       = 200
	maxDescriptionLen = 2000
	maxRootContentLen = 100000
	maxNodeContentLen = 4000 // role-tagged body cap (WIRE-003 spec)

	truncationSuffix = "… (truncated)"
)

// TreeSpec is the mapped shape of one session, ready for import.
type TreeSpec struct {
	Title       string
	Description string
	RootContent string
	Messages    []NodeSpec // child nodes in (timestamp, id) order; root excluded
}

// NodeSpec is one mapped child node.
type NodeSpec struct {
	Content  string
	Metadata map[string]any
}

// MapSession maps one session + its messages onto a TreeSpec. The input
// message order is irrelevant — messages are sorted by (timestamp, id)
// internally. Returns a zero-value spec only for a zero-value Session.
func MapSession(s Session, msgs []Message) TreeSpec {
	ordered := make([]Message, len(msgs))
	copy(ordered, msgs)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Timestamp.Equal(ordered[j].Timestamp) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].Timestamp.Before(ordered[j].Timestamp)
	})

	spec := TreeSpec{
		Title:       sessionTitle(s),
		Description: sessionDescription(s),
	}

	// Root = first non-empty user message; fall back to the derived title.
	rootID := int64(-1)
	for i := range ordered {
		if ordered[i].Role == "user" && strings.TrimSpace(ordered[i].Content) != "" {
			rootID = ordered[i].ID
			spec.RootContent = truncate(ordered[i].Content, maxRootContentLen, true)
			break
		}
	}
	if rootID < 0 {
		spec.RootContent = truncate(spec.Title, maxRootContentLen, false)
	}

	// Remaining messages become child nodes in (timestamp, id) order.
	for i := range ordered {
		m := &ordered[i]
		if m.ID == rootID {
			continue
		}
		spec.Messages = append(spec.Messages, NodeSpec{
			Content:  roleTaggedContent(m, maxNodeContentLen),
			Metadata: messageMetadata(s.ID, m),
		})
	}
	return spec
}

// sessionTitle derives the tree title: session title, else display_name,
// else "Hermes session <id>".
func sessionTitle(s Session) string {
	switch {
	case strings.TrimSpace(s.Title) != "":
		return truncate(strings.TrimSpace(s.Title), maxTitleLen, false)
	case strings.TrimSpace(s.DisplayName) != "":
		return truncate(strings.TrimSpace(s.DisplayName), maxTitleLen, false)
	default:
		return "Hermes session " + s.ID
	}
}

// sessionDescription builds the tree description: model + source +
// started_at, prefixed with the source session id for traceability.
func sessionDescription(s Session) string {
	desc := fmt.Sprintf("Imported Hermes session %s · model=%s · source=%s · started=%s",
		s.ID, s.Model, s.Source, s.StartedAt.UTC().Format(time.RFC3339))
	return truncate(desc, maxDescriptionLen, false)
}

// roleTaggedContent prefixes a message body with its role tag
// (**user:** / **assistant:** / **tool (name):** / **system:**) and caps
// the body at maxBody runes with a truncation suffix when cut.
func roleTaggedContent(m *Message, maxBody int) string {
	tag := "**" + m.Role + ":**"
	if m.Role == "tool" && strings.TrimSpace(m.ToolName) != "" {
		tag = fmt.Sprintf("**tool (%s):**", strings.TrimSpace(m.ToolName))
	}
	body := truncate(m.Content, maxBody, true)
	// PG text columns reject NUL bytes (0x00) and invalid UTF-8 — real
	// session content contains both (binary tool output). Sanitize the
	// imported copy; the source state.db stays untouched (BUG-037).
	body = strings.ReplaceAll(body, "\x00", "")
	body = strings.ToValidUTF8(body, "\uFFFD")
	if body == "" {
		return tag
	}
	return tag + " " + body
}

// messageMetadata builds the node metadata JSON object. tool_name and
// token_count are omitted when absent.
func messageMetadata(sessionID string, m *Message) map[string]any {
	meta := map[string]any{
		"session_id": sessionID,
		"role":       m.Role,
		"message_id": m.ID,
	}
	if strings.TrimSpace(m.ToolName) != "" {
		meta["tool_name"] = m.ToolName
	}
	if m.TokenCount != nil {
		meta["token_count"] = *m.TokenCount
	}
	return meta
}

// truncate caps s at maxRunes. When withSuffix is true and the input was
// cut, the result ends with the truncation suffix while still staying
// within maxRunes (the suffix shares the budget).
func truncate(s string, maxRunes int, withSuffix bool) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	if !withSuffix {
		return string(r[:maxRunes])
	}
	room := maxRunes - utf8.RuneCountInString(truncationSuffix) - 1
	if room < 1 {
		room = 1
	}
	return string(r[:room]) + " " + truncationSuffix
}
