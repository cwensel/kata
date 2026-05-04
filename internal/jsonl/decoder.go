package jsonl

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// Decoder reads ordered JSONL envelopes.
type Decoder struct {
	r io.Reader
}

// NewDecoder returns a decoder over r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r}
}

// ReadAll reads every envelope, enforcing the fixed kind order and requiring
// meta.export_version as the first record.
func (d *Decoder) ReadAll(ctx context.Context) ([]Envelope, error) {
	scanner := bufio.NewScanner(d.r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var (
		out      []Envelope
		lineNo   int
		lastRank = -1
	)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lineNo++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var env Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			return nil, fmt.Errorf("line %d: decode envelope: %w", lineNo, err)
		}
		rank, ok := kindRank(env.Kind)
		if !ok {
			return nil, fmt.Errorf("line %d: %w %q", lineNo, ErrUnknownKind, env.Kind)
		}
		if len(out) == 0 {
			if env.Kind != KindMeta || !isExportVersion(env.Data) {
				return nil, fmt.Errorf("line %d: %w", lineNo, ErrMissingExportVersion)
			}
		}
		if rank < lastRank {
			return nil, fmt.Errorf("line %d: %w: %s after later kind", lineNo, ErrKindOrderViolation, env.Kind)
		}
		lastRank = rank
		out = append(out, env)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan jsonl: %w", err)
	}
	if len(out) == 0 {
		return nil, ErrMissingExportVersion
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func isExportVersion(data json.RawMessage) bool {
	var meta struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return false
	}
	return meta.Key == "export_version"
}
