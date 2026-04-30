package similarity_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/wesm/kata/internal/similarity"
)

func TestCanonical(t *testing.T) {
	// Decomposed "café" = e + combining acute (U+0065 U+0301).
	decomposed := string([]rune{'c', 'a', 'f', 'e', '́'})
	// Precomposed "café" = U+00E9.
	precomposed := string([]rune{'c', 'a', 'f', 'é'})
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"trim", "  hello  ", "hello"},
		{"collapse_internal_runs", "fix\t\nlogin   bug", "fix login bug"},
		{"preserves_case", "Fix Login Bug", "Fix Login Bug"},
		{"nfc_normalizes_combining_marks", precomposed, precomposed},
		{"nfc_normalizes_decomposed_form", decomposed, precomposed},
		{"non_ascii_whitespace_is_collapsed", "foo bar", "foo bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, similarity.Canonical(tc.in))
		})
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single_word", "fix", []string{"fix"}},
		{"drops_stop_words", "the bug is in login", []string{"bug", "login"}},
		{"lowercases", "Fix Login", []string{"fix", "login"}},
		{"stems_simple_suffixes",
			"fixing crashes for testing",
			[]string{"fix", "crash", "test"}},
		{"drops_short_tokens", "a b is fix", []string{"fix"}},
		{"splits_on_punctuation", "fix-login: crash!", []string{"fix", "login", "crash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, similarity.Tokenize(tc.in))
		})
	}
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a, b []string
		want float64
	}{
		{"both_empty", nil, nil, 0.0},
		{"one_empty", []string{"x"}, nil, 0.0},
		{"identical", []string{"a", "b"}, []string{"a", "b"}, 1.0},
		{"half_overlap", []string{"a", "b"}, []string{"b", "c"}, 1.0 / 3.0},
		{"dedupes_inputs", []string{"a", "a", "b"}, []string{"a", "b", "b"}, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.InDelta(t, tc.want, similarity.Jaccard(tc.a, tc.b), 1e-9)
		})
	}
}

func TestScore_WeightedSum(t *testing.T) {
	got := similarity.Score("fix login crash", "stack trace here",
		"fix login crash", "stack trace here")
	assert.InDelta(t, 1.0, got, 1e-9)

	got = similarity.Score("fix login crash", "stack trace here",
		"fix login crash", "completely different body")
	assert.InDelta(t, 0.6, got, 1e-9)

	got = similarity.Score("fix login crash", "shared body text",
		"unrelated title", "shared body text")
	assert.InDelta(t, 0.4, got, 1e-9)

	got = similarity.Score("alpha", "beta", "gamma", "delta")
	assert.InDelta(t, 0.0, got, 1e-9)
}

func TestScore_Body500CharLimit(t *testing.T) {
	prefix := strings.Repeat("x", 500)
	got := similarity.Score("same", prefix+" alpha-divergent",
		"same", prefix+" beta-divergent")
	assert.InDelta(t, 1.0, got, 1e-9, "divergence past 500 chars must not affect the score")
}

// TestTokenize_AllStopWordsAreFiltered guards against stopword/stem ordering
// regressions. Spec §3.7: stopword removal must come BEFORE stemming so
// "has" doesn't stem to "ha" and slip through.
func TestTokenize_AllStopWordsAreFiltered(t *testing.T) {
	stopWords := []string{
		"a", "an", "and", "are", "as", "at", "be", "by", "for", "from",
		"has", "have", "in", "is", "it", "of", "on", "or", "that", "the",
		"this", "to", "was", "were", "will", "with",
	}
	for _, w := range stopWords {
		t.Run(w, func(t *testing.T) {
			got := similarity.Tokenize(w)
			assert.Empty(t, got, "stopword %q must be filtered, got %v", w, got)
		})
	}
}

// TestTokenize_StopWordsBeforeStem verifies the spec's documented order: a
// phrase containing only stopwords (some of which would stem to nonsense
// tokens if processed in the wrong order) yields an empty token slice.
func TestTokenize_StopWordsBeforeStem(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"this_was_a_bug", "this was a bug", []string{"bug"}},
		{"all_stopwords_strippable", "this was has", nil},
		{"mixed_phrase", "this issue has the login crash", []string{"issue", "login", "crash"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, similarity.Tokenize(tc.in))
		})
	}
}
