package db

import (
	"context"
	"fmt"
	"strings"
)

// SearchFTS runs an FTS5 BM25-ranked query against issues_fts, joins back to
// issues, and returns the top `limit` rows scoped to the given project. When
// includeDeleted is false, soft-deleted issues are filtered. The returned
// Score is the negated raw BM25 (so higher = better match); MatchedIn is
// derived from per-column MATCH subqueries since FTS5 highlight() returns
// NULL on contentless tables.
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

	deletedFilter := "AND i.deleted_at IS NULL"
	if includeDeleted {
		deletedFilter = ""
	}
	// Per-column MATCH subqueries replace highlight() because issues_fts is
	// declared content='' (contentless), and highlight() returns NULL for every
	// column on contentless tables. Each subquery returns 1 if the row's
	// title/body/comments column matches the phrase, 0 otherwise.
	query := fmt.Sprintf(`
		SELECT i.id, i.project_id, i.number, i.title, i.body, i.status,
		       i.closed_reason, i.owner, i.author, i.created_at, i.updated_at,
		       i.closed_at, i.deleted_at,
		       bm25(issues_fts),
		       (issues_fts.rowid IN (SELECT rowid FROM issues_fts WHERE title    MATCH ?)) AS in_title,
		       (issues_fts.rowid IN (SELECT rowid FROM issues_fts WHERE body     MATCH ?)) AS in_body,
		       (issues_fts.rowid IN (SELECT rowid FROM issues_fts WHERE comments MATCH ?)) AS in_comments
		FROM issues_fts
		JOIN issues i ON i.id = issues_fts.rowid
		WHERE issues_fts MATCH ?
		  AND i.project_id = ?
		  %s
		ORDER BY bm25(issues_fts) ASC
		LIMIT %d`, deletedFilter, limit)

	rows, err := d.QueryContext(ctx, query, phrase, phrase, phrase, phrase, projectID)
	if err != nil {
		return nil, fmt.Errorf("search fts: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SearchCandidate
	for rows.Next() {
		var (
			i                           Issue
			rawScore                    float64
			inTitle, inBody, inComments bool
		)
		if err := rows.Scan(&i.ID, &i.ProjectID, &i.Number, &i.Title, &i.Body, &i.Status,
			&i.ClosedReason, &i.Owner, &i.Author, &i.CreatedAt, &i.UpdatedAt,
			&i.ClosedAt, &i.DeletedAt,
			&rawScore, &inTitle, &inBody, &inComments); err != nil {
			return nil, fmt.Errorf("scan search row: %w", err)
		}
		matched := make([]string, 0, 3)
		if inTitle {
			matched = append(matched, "title")
		}
		if inBody {
			matched = append(matched, "body")
		}
		if inComments {
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
