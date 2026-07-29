//go:build windows

package core

import (
	"bytes"
	"strings"
	"testing"
)

// realInnerPrefix is the EXACT byte prefix a Windows inner pseudoconsole (conhost)
// emits at child startup, captured from scratchpad/conpty-relay/cap-fixed.txt:
//
//	ESC[?9001h ESC[?1004h ESC[?25l ESC[?9001l ESC[?1004l ESC[2J ESC[m ESC[H
//	ESC]0;<title>BEL ESC[?25h
//
// The two leaks we must strip are the leading ESC[?9001h (Win32 Input Mode) and
// ESC[?1004h (focus reporting). Everything else (cursor hide/show, clear, SGR
// reset, home, OSC title) must survive verbatim.
const realInnerPrefix = "\x1b[?9001h\x1b[?1004h\x1b[?25l\x1b[?9001l\x1b[?1004l\x1b[2J\x1b[m\x1b[H\x1b]0;C:\\Users\\Nicholas\\AppData\x07\x1b[?25h"

// boxFrame is a known cursor-addressed 3-line bordered box: a synthetic but
// representative TUI frame. The box-drawing glyphs and cursor moves must pass
// through untouched.
const boxFrame = "\x1b[2;5H┌──┐" + // ┌──┐ at row 2 col 5
	"\x1b[3;5H│OK│" + // │OK│ at row 3
	"\x1b[4;5H└──┘" // └──┘ at row 4

// filtered runs the REAL win32InputFilter over the given input (in one Write) and
// returns the produced output.
func filtered(t *testing.T, in string) string {
	t.Helper()
	var buf bytes.Buffer
	f := &win32InputFilter{w: &buf}
	if n, err := f.Write([]byte(in)); err != nil || n != len(in) {
		t.Fatalf("Write returned n=%d err=%v, want n=%d err=nil", n, err, len(in))
	}
	return buf.String()
}

func TestWin32InputFilterStripsModeSets(t *testing.T) {
	in := realInnerPrefix + boxFrame
	out := filtered(t, in)

	// The four target mode-sets must be gone.
	for _, gone := range []string{"\x1b[?9001h", "\x1b[?9001l", "\x1b[?1004h", "\x1b[?1004l"} {
		if strings.Contains(out, gone) {
			t.Errorf("filtered output still contains leaked mode-set %q", gone)
		}
	}
	// Sanity: the RAW input DID contain them (proves the filter actually did work,
	// not that the input was already clean).
	if !strings.Contains(in, "\x1b[?9001h") {
		t.Fatalf("test bug: raw input should contain ESC[?9001h")
	}

	// Everything else must survive verbatim.
	for _, keep := range []string{
		"\x1b[?25l", // cursor hide (a ?-private seq we must NOT strip)
		"\x1b[?25h", // cursor show
		"\x1b[2J",   // clear screen
		"\x1b[m",    // SGR reset
		"\x1b[H",    // cursor home
		"\x1b]0;",   // OSC title intro
		"\x07",      // BEL terminating the OSC
		"\x1b[2;5H", // cursor address
		"┌──┐",      // ┌──┐
		"│OK│",      // │OK│
		"└──┘",      // └──┘
	} {
		if !strings.Contains(out, keep) {
			t.Errorf("filtered output dropped sequence that must survive: %q", keep)
		}
	}

	// No stray win32-input key-record terminator should be introduced by us. (The
	// `_` flood is an INPUT-side artifact the mode-set strip prevents; the filter
	// must never synthesize one.)
	if strings.Contains(out, "_") {
		t.Errorf("filtered output unexpectedly contains a win32-input '_' record byte")
	}
}

// TestWin32InputFilterByteSplit feeds the input one byte per Write so every target
// mode-set is split across reads, and asserts the output is identical to the
// single-Write case. This proves the stateful parser survives arbitrary chunk
// boundaries (the real relay reads in 4 KiB chunks that can split a sequence).
func TestWin32InputFilterByteSplit(t *testing.T) {
	in := realInnerPrefix + boxFrame
	want := filtered(t, in)

	var buf bytes.Buffer
	f := &win32InputFilter{w: &buf}
	for i := 0; i < len(in); i++ {
		if _, err := f.Write([]byte{in[i]}); err != nil {
			t.Fatalf("byte %d Write err=%v", i, err)
		}
	}
	if got := buf.String(); got != want {
		t.Errorf("byte-split output != single-write output\n got=%q\nwant=%q", got, want)
	}
}

// TestWin32InputFilterSplitMidSequence splits the stream EXACTLY in the middle of
// the leading ESC[?9001h across two Writes to confirm the mode-set is still caught
// when its bytes arrive in separate reads.
func TestWin32InputFilterSplitMidSequence(t *testing.T) {
	full := "AB\x1b[?9001hCD\x1b[?1004hEF"
	// "\x1b[?9001h" occupies indexes 2..9. Split after "\x1b[?90".
	mid := strings.Index(full, "9001") + 2 // middle of the digits
	var buf bytes.Buffer
	f := &win32InputFilter{w: &buf}
	f.Write([]byte(full[:mid]))
	f.Write([]byte(full[mid:]))
	got := buf.String()
	if got != "ABCDEF" {
		t.Errorf("mid-sequence split not stripped cleanly: got %q want %q", got, "ABCDEF")
	}
}

// TestWin32InputFilterPreservesOtherPrivateModes guards against over-filtering:
// other DEC private modes (bracketed paste, alt-screen, cursor visibility) must
// pass through untouched.
func TestWin32InputFilterPreservesOtherPrivateModes(t *testing.T) {
	in := "\x1b[?2004h\x1b[?1049h\x1b[?9001h\x1b[?25lX\x1b[?9001l\x1b[?1049l\x1b[?2004l"
	out := filtered(t, in)
	want := "\x1b[?2004h\x1b[?1049h\x1b[?25lX\x1b[?1049l\x1b[?2004l" // only 9001h/9001l removed
	if out != want {
		t.Errorf("over/under-filtered private modes:\n got=%q\nwant=%q", out, want)
	}
}
