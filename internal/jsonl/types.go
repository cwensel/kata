// Package jsonl exports and imports kata database state as ordered NDJSON.
package jsonl

import (
	"encoding/json"
	"errors"
)

// Kind is the fixed record kind tag in a JSONL envelope.
type Kind string

const (
	KindMeta           Kind = "meta"
	KindProject        Kind = "project"
	KindProjectAlias   Kind = "project_alias"
	KindIssue          Kind = "issue"
	KindComment        Kind = "comment"
	KindIssueLabel     Kind = "issue_label"
	KindLink           Kind = "link"
	KindEvent          Kind = "event"
	KindPurgeLog       Kind = "purge_log"
	KindSQLiteSequence Kind = "sqlite_sequence"
)

var (
	ErrMissingExportVersion = errors.New("missing export_version")
	ErrUnknownKind          = errors.New("unknown kind")
	ErrKindOrderViolation   = errors.New("kind order violation")
)

var kindOrder = map[Kind]int{
	KindMeta:           0,
	KindProject:        1,
	KindProjectAlias:   2,
	KindIssue:          3,
	KindComment:        4,
	KindIssueLabel:     5,
	KindLink:           6,
	KindEvent:          7,
	KindPurgeLog:       8,
	KindSQLiteSequence: 9,
}

// Envelope is one NDJSON record.
type Envelope struct {
	Kind Kind            `json:"kind"`
	Data json.RawMessage `json:"data"`
}

func kindRank(k Kind) (int, bool) {
	rank, ok := kindOrder[k]
	return rank, ok
}
