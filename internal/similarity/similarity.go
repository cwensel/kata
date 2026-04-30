// Package similarity provides text-normalization primitives used by both the
// idempotency fingerprint (Canonical only) and the look-alike soft-block
// pipeline (full Tokenize → Jaccard → Score). All functions are pure; no DB,
// no I/O, no goroutines.
package similarity

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// Canonical normalizes a string for fingerprinting: NFC, trim leading/trailing
// whitespace, collapse runs of any Unicode whitespace into a single ASCII
// space. Case is preserved — fingerprint is case-sensitive per spec §3.6.
func Canonical(s string) string {
	s = norm.NFC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := true // suppresses leading whitespace
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteByte(' ')
				prevSpace = true
			}
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	out := b.String()
	return strings.TrimRight(out, " ")
}

// stopWords is the fixed v1 list. Membership check is O(1) via map.
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "and": {}, "are": {}, "as": {}, "at": {}, "be": {},
	"by": {}, "for": {}, "from": {}, "has": {}, "have": {}, "in": {}, "is": {},
	"it": {}, "of": {}, "on": {}, "or": {}, "that": {}, "the": {}, "this": {},
	"to": {}, "was": {}, "were": {}, "will": {}, "with": {},
}

// Tokenize lowercases s, splits on non-letter-or-digit boundaries, drops
// stop-words and tokens shorter than 2 runes, and applies a simple
// suffix-stripper (ing → "", ed → "", es → "", s → "").
func Tokenize(s string) []string {
	s = norm.NFC.String(s)
	s = strings.ToLower(s)

	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		tok := stem(cur.String())
		cur.Reset()
		if len(tok) < 2 {
			return
		}
		if _, isStop := stopWords[tok]; isStop {
			return
		}
		out = append(out, tok)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// stem strips a small set of English suffixes. Order matters: longer suffixes
// before shorter ones so "testing" → "test" not "testin".
func stem(t string) string {
	for _, suf := range []string{"ing", "ed", "es", "s"} {
		if len(t) > len(suf)+1 && strings.HasSuffix(t, suf) {
			return t[:len(t)-len(suf)]
		}
	}
	return t
}

// Jaccard returns |A ∩ B| / |A ∪ B| treating inputs as sets. Empty inputs
// (either side) return 0 — a deliberate choice so a brand-new issue's empty
// body doesn't inflate similarity against another issue with an empty body.
func Jaccard(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[string]struct{}, len(a))
	for _, t := range a {
		setA[t] = struct{}{}
	}
	setB := make(map[string]struct{}, len(b))
	for _, t := range b {
		setB[t] = struct{}{}
	}
	intersect := 0
	for t := range setA {
		if _, ok := setB[t]; ok {
			intersect++
		}
	}
	union := len(setA) + len(setB) - intersect
	if union == 0 {
		return 0
	}
	return float64(intersect) / float64(union)
}

// Score returns the weighted similarity between two issues:
//
//	0.6 * Jaccard(title tokens) + 0.4 * Jaccard(body[:500] tokens)
//
// Body slicing is rune-based: the first 500 Unicode codepoints of each body
// are tokenized. Spec §3.7.
func Score(titleA, bodyA, titleB, bodyB string) float64 {
	titleScore := Jaccard(Tokenize(titleA), Tokenize(titleB))
	bodyScore := Jaccard(Tokenize(firstRunes(bodyA, 500)), Tokenize(firstRunes(bodyB, 500)))
	return 0.6*titleScore + 0.4*bodyScore
}

// firstRunes returns the first n runes of s. If s has fewer than n runes,
// it's returned unchanged.
func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}
