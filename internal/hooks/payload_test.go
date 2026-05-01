package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/wesm/kata/internal/db"
)

func nowZero() time.Time { return time.Date(2026, 4, 30, 14, 22, 11, 482_000_000, time.UTC) }

func sampleEvent(t string) db.Event {
	id := int64(42)
	num := int64(7)
	return db.Event{
		ID:              81237,
		Type:            t,
		Actor:           "claude-4.7-wesm-laptop",
		ProjectID:       3,
		ProjectIdentity: "github.com/wesm/kata",
		IssueID:         &id,
		IssueNumber:     &num,
		Payload:         `{"comment_id":104}`,
		CreatedAt:       nowZero(),
	}
}

func okIssue(_ context.Context, _ int64) (IssueSnapshot, error) {
	return IssueSnapshot{
		Number: 7, Title: "fix login crash on Safari", Status: "open",
		Labels: []string{"bug", "safari"}, Owner: "claude-4.7-wesm-laptop", Author: "claude-4.7-wesm-laptop",
	}, nil
}

func okComment(_ context.Context, _ int64) (CommentSnapshot, error) {
	return CommentSnapshot{ID: 104, Body: "looks like a render bug"}, nil
}

func okProject(_ context.Context, _ int64) (ProjectSnapshot, error) {
	return ProjectSnapshot{Name: "kata"}, nil
}

func okAlias(_ context.Context, _ db.Event) (AliasSnapshot, bool, error) {
	return AliasSnapshot{Identity: "github.com/wesm/kata", Kind: "git", RootPath: "/Users/wesm/code/kata"}, true, nil
}

func TestBuildStdin_HappyPath(t *testing.T) {
	evt := sampleEvent("issue.commented")
	out, truncated := buildStdinJSON(context.Background(), evt, okProject, okIssue, okComment, okAlias, nopLog())
	if truncated {
		t.Fatal("happy path should not truncate")
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["kata_hook_version"].(float64) != 1 {
		t.Fatalf("kata_hook_version: %v", got["kata_hook_version"])
	}
	if got["type"] != "issue.commented" {
		t.Fatalf("type: %v", got["type"])
	}
	proj := got["project"].(map[string]any)
	if proj["name"] != "kata" {
		t.Fatalf("project.name: %v", proj["name"])
	}
	issue := got["issue"].(map[string]any)
	if issue["title"] != "fix login crash on Safari" {
		t.Fatalf("issue.title: %v", issue["title"])
	}
	pld := got["payload"].(map[string]any)
	if pld["comment_body"] != "looks like a render bug" {
		t.Fatalf("comment_body: %v", pld["comment_body"])
	}
	alias := got["alias"].(map[string]any)
	if alias["alias_identity"] != "github.com/wesm/kata" {
		t.Fatalf("alias_identity: %v", alias["alias_identity"])
	}
}

func TestBuildStdin_AliasMissing_OmitsBlock(t *testing.T) {
	evt := sampleEvent("issue.created")
	noAlias := func(_ context.Context, _ db.Event) (AliasSnapshot, bool, error) { return AliasSnapshot{}, false, nil }
	out, _ := buildStdinJSON(context.Background(), evt, okProject, okIssue, okComment, noAlias, nopLog())
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["alias"]; ok {
		t.Fatal("alias block should be omitted")
	}
}

func TestBuildStdin_AliasError_OmitsBlockLogs(t *testing.T) {
	evt := sampleEvent("issue.created")
	errAlias := func(_ context.Context, _ db.Event) (AliasSnapshot, bool, error) {
		return AliasSnapshot{}, false, errors.New("boom")
	}
	logged := []string{}
	logger := func(format string, _ ...any) { logged = append(logged, format) }
	out, _ := buildStdinJSON(context.Background(), evt, okProject, okIssue, okComment, errAlias, logger)
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["alias"]; ok {
		t.Fatal("alias block should be omitted on resolver error")
	}
	if len(logged) == 0 {
		t.Fatal("alias resolver error should be logged")
	}
}

func TestBuildStdin_IssueResolverError_OmitsIssueBlock(t *testing.T) {
	evt := sampleEvent("issue.created")
	bad := func(_ context.Context, _ int64) (IssueSnapshot, error) { return IssueSnapshot{}, errors.New("db down") }
	out, _ := buildStdinJSON(context.Background(), evt, okProject, bad, okComment, okAlias, nopLog())
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if _, ok := got["issue"]; ok {
		t.Fatal("issue block should be omitted when IssueResolver errors")
	}
}

func TestBuildStdin_ProjectResolverError_KeepsIDAndIdentity(t *testing.T) {
	evt := sampleEvent("issue.created")
	bad := func(_ context.Context, _ int64) (ProjectSnapshot, error) {
		return ProjectSnapshot{}, errors.New("db down")
	}
	out, _ := buildStdinJSON(context.Background(), evt, bad, okIssue, okComment, okAlias, nopLog())
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	proj := got["project"].(map[string]any)
	if proj["id"].(float64) != 3 {
		t.Fatalf("project.id should still be present: %v", proj["id"])
	}
	if _, ok := proj["name"]; ok {
		t.Fatal("project.name should be omitted when ProjectResolver errors")
	}
}

func TestBuildStdin_NonCommentEvent_SkipsCommentResolver(t *testing.T) {
	evt := sampleEvent("issue.created")
	called := false
	cr := func(_ context.Context, _ int64) (CommentSnapshot, error) {
		called = true
		return CommentSnapshot{}, nil
	}
	_, _ = buildStdinJSON(context.Background(), evt, okProject, okIssue, cr, okAlias, nopLog())
	if called {
		t.Fatal("CommentResolver must not be invoked for non-issue.commented events")
	}
}

func TestBuildStdin_TitleTruncated(t *testing.T) {
	evt := sampleEvent("issue.created")
	bigTitle := strings.Repeat("A", 2*1024)
	bigIssue := func(_ context.Context, _ int64) (IssueSnapshot, error) {
		return IssueSnapshot{Number: 1, Title: bigTitle, Status: "open"}, nil
	}
	out, _ := buildStdinJSON(context.Background(), evt, okProject, bigIssue, okComment, okAlias, nopLog())
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	issue := got["issue"].(map[string]any)
	if issue["_truncated"] != true {
		t.Fatal("issue._truncated should be true for 2KB title")
	}
	if int(issue["_full_size"].(float64)) != len(bigTitle) {
		t.Fatalf("_full_size = %v, want %d", issue["_full_size"], len(bigTitle))
	}
}

func TestBuildStdin_TitleTruncation_RuneBoundary(t *testing.T) {
	evt := sampleEvent("issue.created")
	// 4-byte rune (😀 = U+1F600) repeated to overflow the 1KB title cap
	// at a non-aligned offset. 257 runes = 1028 bytes, cap = 1024 means
	// the cut lands inside the 257th rune.
	bigTitle := strings.Repeat("😀", 257)
	bigIssue := func(_ context.Context, _ int64) (IssueSnapshot, error) {
		return IssueSnapshot{Number: 1, Title: bigTitle, Status: "open"}, nil
	}
	out, _ := buildStdinJSON(context.Background(), evt, okProject, bigIssue, okComment, okAlias, nopLog())
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	issue := got["issue"].(map[string]any)
	title := issue["title"].(string)
	if !utf8.ValidString(title) {
		t.Fatalf("truncated title is not valid UTF-8: %q", title)
	}
	// Truncated string must be ≤ limit and end on a rune boundary, so it
	// should contain a whole number of 4-byte runes (≤ 256).
	if len(title) > 1024 {
		t.Fatalf("truncated length %d exceeds 1024", len(title))
	}
}

func TestBuildStdin_TopLevelTruncation_DropsOptionalFields(t *testing.T) {
	evt := sampleEvent("issue.commented")
	bigBody := strings.Repeat("X", 16*1024)
	bigIssue := func(_ context.Context, _ int64) (IssueSnapshot, error) {
		return IssueSnapshot{Number: 1, Title: strings.Repeat("T", 600), Status: "open"}, nil
	}
	bigComment := func(_ context.Context, _ int64) (CommentSnapshot, error) {
		return CommentSnapshot{ID: 1, Body: bigBody}, nil
	}
	bigPayload := strings.Repeat("Y", 250*1024)
	evt.Payload = `{"comment_id":104,"big":"` + bigPayload + `"}`
	out, truncated := buildStdinJSON(context.Background(), evt, okProject, bigIssue, bigComment, okAlias, nopLog())
	if !truncated {
		t.Fatal("oversize payload should set top-level payload_truncated:true")
	}
	if len(out) > 256*1024 {
		t.Fatalf("output size %d exceeds 256KB cap", len(out))
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	if got["payload_truncated"] != true {
		t.Fatal("payload_truncated must be true")
	}
}

func nopLog() func(string, ...any) { return func(string, ...any) {} }
