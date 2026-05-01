package tui

import (
	"fmt"
	"strings"
	"time"
)

// eventDescriber returns the human-readable description for an event of
// a given type. The describer can read payload fields (label name,
// to_number, owner). Functions instead of plain strings keep "labeled
// %s" / "linked %s #N" / closed-with-reason all expressible without a
// branchy switch in eventDescription.
type eventDescriber func(e EventLogEntry) string

// staticDesc returns an eventDescriber that always emits s. Used for
// the simple cases (created, reopened, etc.) that don't need payload.
func staticDesc(s string) eventDescriber {
	return func(EventLogEntry) string { return s }
}

// payloadDesc returns an eventDescriber that emits "<prefix> <field>"
// where field is read out of the payload as a string.
func payloadDesc(prefix, field string) eventDescriber {
	return func(e EventLogEntry) string {
		return prefix + " " + payloadString(e, field)
	}
}

// eventDescribers is the per-type dispatch table for eventDescription.
// Unknown types fall through to a stripped "issue." prefix in
// eventDescription so the column always carries something readable.
var eventDescribers = map[string]eventDescriber{
	"issue.created":      staticDesc("created"),
	"issue.closed":       func(e EventLogEntry) string { return "closed" + reasonSuffix(e) },
	"issue.reopened":     staticDesc("reopened"),
	"issue.commented":    staticDesc("added comment"),
	"issue.labeled":      payloadDesc("labeled", "label"),
	"issue.unlabeled":    payloadDesc("unlabeled", "label"),
	"issue.linked":       func(e EventLogEntry) string { return "linked " + linkPayloadDesc(e) },
	"issue.unlinked":     func(e EventLogEntry) string { return "unlinked " + linkPayloadDesc(e) },
	"issue.assigned":     payloadDesc("assigned", "owner"),
	"issue.unassigned":   staticDesc("unassigned"),
	"issue.updated":      staticDesc("updated"),
	"issue.soft_deleted": staticDesc("deleted"),
	"issue.restored":     staticDesc("restored"),
}

// eventDescription returns the type-specific short description used in
// the events tab. Unknown types fall back to a stripped "issue." prefix
// so the column always carries something readable.
func eventDescription(e EventLogEntry) string {
	if d, ok := eventDescribers[e.Type]; ok {
		return d(e)
	}
	return strings.TrimPrefix(e.Type, "issue.")
}

// reasonSuffix renders " (reason)" for closed events that carry one.
func reasonSuffix(e EventLogEntry) string {
	if r := payloadString(e, "reason"); r != "" {
		return " (" + r + ")"
	}
	return ""
}

// linkPayloadDesc formats "type #to_number" from a link.added/removed
// payload. Missing fields degrade gracefully — type alone, or just "?".
func linkPayloadDesc(e EventLogEntry) string {
	t := payloadString(e, "type")
	to, ok := readEventTargetNumber(e)
	if !ok {
		if t == "" {
			return "?"
		}
		return t
	}
	if t == "" {
		return fmt.Sprintf("#%d", to)
	}
	return fmt.Sprintf("%s #%d", t, to)
}

// payloadString reads a string field out of the event payload. Missing
// keys, non-string values, and a nil payload all return "".
func payloadString(e EventLogEntry, key string) string {
	if e.Payload == nil {
		return ""
	}
	if v, ok := e.Payload[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// fmtTime is the compact timestamp used in tab content. The zero value
// renders as a dash so empty fixtures don't show "0001-01-01 00:00".
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02 15:04")
}

// eventJumpTarget reads the issue number that a jumpable event refers
// to. link.added/link.removed carry to_number; we also accept
// issue_number for forward-compat.
func eventJumpTarget(events []EventLogEntry, idx int) (int64, bool) {
	if idx < 0 || idx >= len(events) {
		return 0, false
	}
	return readEventTargetNumber(events[idx])
}

// readEventTargetNumber pulls an int64 issue number out of e.Payload.
// JSON decodes numbers as float64 by default; int64/int are accepted so
// hand-built test fixtures don't need to round-trip through json.
func readEventTargetNumber(e EventLogEntry) (int64, bool) {
	if e.Payload == nil {
		return 0, false
	}
	for _, k := range []string{"to_number", "issue_number"} {
		if v, ok := e.Payload[k]; ok {
			if n, ok := numberFromAny(v); ok {
				return n, true
			}
		}
	}
	return 0, false
}

// numberFromAny widens a JSON-decoded number to int64.
func numberFromAny(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

// linkJumpTarget returns the link's ToNumber.
func linkJumpTarget(links []LinkEntry, idx int) (int64, bool) {
	if idx < 0 || idx >= len(links) {
		return 0, false
	}
	return links[idx].ToNumber, true
}
