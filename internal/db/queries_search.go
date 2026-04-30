package db

import (
	"context"
	"fmt"
	"strings"
)

// SearchFTS runs an FTS5 BM25-ranked query against issues_fts, joins back to
// issues, and returns the top `limit` rows scoped to the given project. When
// includeDeleted is false, soft-deleted issues are filtered. The returned
// Score is the negated raw BM25 (so higher = better match), matched_in is the
// list of FTS columns that contributed to the row's score.
func (d *DB) SearchFTS(ctx context.Context, projectID int64, q string, limit int, includeDeleted bool) ([]SearchCandidate, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}

	// Wrap the user query as a single FTS5 phrase. Embedded double quotes are
	// doubled per FTS5 quoting rules so the whole query is opaque to FTS's
	// special characters (`:`, `*`, parens, `OR`/`AND`/`NOT` as bare words).
	phrase := `"` + strings.ReplaceAll(q, `"`, `""`) + `"`

	// highlight(table, col, ...) returns the matched text wrapped in markers;
	// when the column didn't match it returns the empty matched substring.
	// We use a sentinel pair to detect non-empty highlights.
	const (
		startMark = "\x01"
		endMark   = "\x02"
	)
	deletedFilter := "AND i.deleted_at IS NULL"
	if includeDeleted {
		deletedFilter = ""
	}
	// COALESCE around highlight() because FTS5 returns NULL for empty column
	// content (e.g. an issue with no body or no comments) and database/sql
	// can't scan NULL into a Go string.
	query := fmt.Sprintf(`
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at,
		       bm25(issues_fts),
		       COALESCE(highlight(issues_fts, 0, ?, ?), ''),
		       COALESCE(highlight(issues_fts, 1, ?, ?), ''),
		       COALESCE(highlight(issues_fts, 2, ?, ?), '')
		FROM issues_fts
		JOIN issues i ON i.id = issues_fts.rowid
		WHERE issues_fts MATCH ?
		  AND i.project_id = ?
		  %s
		ORDER BY bm25(issues_fts) ASC
		LIMIT %d`, deletedFilter, limit)

	rows, err := d.QueryContext(ctx, query,
		startMark, endMark, startMark, endMark, startMark, endMark,
		phrase, projectID)
	if err != nil {
		return nil, fmt.Errorf("search fts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchCandidate
	for rows.Next() {
		var (
			i                           Issue
			rawScore                    float64
			titleHL, bodyHL, commentsHL string
		)
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt,
			&rawScore, &titleHL, &bodyHL, &commentsHL); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		matched := make([]string, 0, 3)
		if strings.Contains(titleHL, startMark) {
			matched = append(matched, "title")
		}
		if strings.Contains(bodyHL, startMark) {
			matched = append(matched, "body")
		}
		if strings.Contains(commentsHL, startMark) {
			matched = append(matched, "comments")
		}
		// FTS5 BM25 returns negative numbers; invert so callers compare with
		// "higher = better" semantics.
		out = append(out, SearchCandidate{
			Issue:     i,
			Score:     -rawScore,
			MatchedIn: matched,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
