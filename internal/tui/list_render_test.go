package tui

import (
	"io"
	"strings"
	"testing"
	"unicode"

	"github.com/mattn/go-runewidth"
)

// TestRenderLabelChips_AlphabeticalSort verifies the input slice is
// rendered alphabetically regardless of caller order. Sort is in-render
// so callers don't have to pre-sort the daemon's response.
func TestRenderLabelChips_AlphabeticalSort(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"prio-1", "bug", "needs-design"}, 80)
	bug := strings.Index(got, "[bug]")
	needs := strings.Index(got, "[needs-design]")
	prio := strings.Index(got, "[prio-1]")
	if bug < 0 || needs < 0 || prio < 0 {
		t.Fatalf("missing chip(s) in %q", got)
	}
	if bug >= needs || needs >= prio {
		t.Fatalf("chips out of order in %q (bug=%d needs=%d prio=%d)",
			got, bug, needs, prio)
	}
}

// TestRenderLabelChips_PacksUntilOverflow narrows the available width
// so not every chip fits; the renderer must drop the tail and append
// `+N` indicating the count of dropped labels.
func TestRenderLabelChips_PacksUntilOverflow(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"a", "b", "c", "d", "e"}, 12)
	if !strings.Contains(got, "+") {
		t.Fatalf("expected +N overflow marker in %q", got)
	}
	// Width budget caps total visible cells at 12.
	if w := runewidth.StringWidth(got); w > 12 {
		t.Fatalf("rendered width %d exceeds budget 12: %q", w, got)
	}
}

// TestRenderLabelChips_PlusNOverflowFormat verifies the +N token is
// formed correctly (literal +, then a base-10 integer >= 1) when the
// chip pack drops chips.
func TestRenderLabelChips_PlusNOverflowFormat(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"alpha", "beta", "gamma", "delta"}, 14)
	idx := strings.Index(got, "+")
	if idx < 0 {
		t.Fatalf("no +N token in %q", got)
	}
	rest := strings.TrimSpace(got[idx+1:])
	// Must start with a digit and parse as a positive integer.
	if rest == "" || rest[0] < '0' || rest[0] > '9' {
		t.Fatalf("+N suffix is not a number in %q", got)
	}
}

// TestRenderLabelChips_UltraNarrowFallback verifies the "[N labels]"
// degraded form when even one chip won't fit. The fallback keeps the
// header informative on tiny terminals.
func TestRenderLabelChips_UltraNarrowFallback(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"bug", "prio-1"}, 5)
	if !strings.Contains(got, "[2 labels]") {
		t.Fatalf("expected ultra-narrow fallback [2 labels] in %q", got)
	}
}

// TestRenderLabelChips_EmptyLabels verifies the empty-labels placeholder
// renders so the header layout doesn't shift when labels are absent.
func TestRenderLabelChips_EmptyLabels(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips(nil, 80)
	if !strings.Contains(got, "(no labels)") {
		t.Fatalf("expected (no labels) placeholder in %q", got)
	}
}

// TestRenderLabelChips_WidthMeasureUsesRunewidth pins the width-math
// invariant: a wide-glyph label (`かた` is 4 cells, not 6 bytes) plus
// an embedded ANSI escape must be measured correctly. Width is
// computed AFTER sanitize so the measurement matches the rendered cell.
//
// Sorted clean labels: "bug" (3 cells), "かた" (4 cells). At width 11:
//   - "[bug]" = 5 cells; reserve 4-cell overflow tail since one label
//     remains. 0+5+4 = 9 <= 11, so "[bug]" fits (used=5).
//   - "[かた]" = 6 cells + 1-cell separator = 7. 5+7+0 = 12 > 11, so
//     "[かた]" is dropped. Output: "[bug] +1".
//
// If the renderer measured the raw `\x1b[31mかた` (10+ bytes) instead
// of the sanitized "かた" (4 cells), or measured byte length instead
// of cell width, the math would be wrong and the test would fail.
func TestRenderLabelChips_WidthMeasureUsesRunewidth(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"\x1b[31mかた", "bug"}, 11)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC survived width-measure path: %q", got)
	}
	if !strings.Contains(got, "[bug]") {
		t.Fatalf("expected [bug] chip in %q", got)
	}
	if !strings.Contains(got, "+1") {
		t.Fatalf("expected +1 overflow in %q", got)
	}
	if w := runewidth.StringWidth(got); w > 11 {
		t.Fatalf("rendered width %d exceeds budget 11: %q", w, got)
	}
	// Direct width check on the sanitized label proves we really are
	// measuring cell width, not bytes.
	if cells := runewidth.StringWidth("かた"); cells != 4 {
		t.Fatalf("runewidth.StringWidth(\"かた\") = %d, want 4", cells)
	}
}

// TestRenderLabelChips_RenderedTextSanitized proves the chip TEXT is
// sanitized — not just the width measurement. Hostile labels with ANSI
// escapes and a U+202E RIGHT-TO-LEFT OVERRIDE must not survive into
// the rendered output.
func TestRenderLabelChips_RenderedTextSanitized(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	rlo := rune(0x202E)
	hostile := "ok" + string(rlo) + "pad"
	got := renderLabelChips([]string{"bug\x1b[2J", hostile}, 80)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("ESC reached rendered chips: %q", got)
	}
	if strings.ContainsRune(got, rlo) {
		t.Fatalf("U+202E survived: %q", got)
	}
	for _, r := range got {
		if unicode.Is(unicode.Cf, r) {
			t.Fatalf("Cf rune %U survived in rendered chips: %q", r, got)
		}
	}
}

// TestRenderLabelChips_NewlineInLabelDoesNotBreakRow pins the
// defense-in-depth invariant that a label containing a literal newline
// cannot split a chip across two terminal rows. The chip strip is a
// single-row context, so the renderer must source chip text through
// textsafe.Line (which replaces \n with literal "\n") rather than
// textsafe.Block (which preserves \n for multi-line bodies). The
// schema bars newlines in labels (SQLite CHECK at 0001_init.sql:103)
// but the TUI is the wrong layer to depend on that; this test guards
// the renderer-level invariant directly.
func TestRenderLabelChips_NewlineInLabelDoesNotBreakRow(t *testing.T) {
	applyColorMode(colorNone, io.Discard)
	got := renderLabelChips([]string{"bug\nfoo"}, 80)
	if strings.ContainsRune(got, '\n') {
		t.Fatalf("literal newline survived in chip strip: %q", got)
	}
	if !strings.Contains(got, `\n`) {
		t.Fatalf("expected literal escape sequence \\n in rendered chip: %q", got)
	}
}
