package agentconfig

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// QA deep-dive (Round 2): independent, property-style verification of the new
// production lineDelimitedReader. This file is deliberately separate from both
// run_test.go (engineer-owned) and run_e2e_qa_test.go (QA's integration repro)
// so no one else's assertions are touched.

// TestQA_LineDelimitedReaderReconstructsExactly is a property-style test: for
// every (input, buffer size) pair, reading the whole stream through the reader
// and concatenating the chunks must be byte-for-byte identical to the input;
// no single Read may span two lines (any '\n' must be the chunk's last byte);
// and the read loop must always terminate (no (0, nil) spin).
func TestQA_LineDelimitedReaderReconstructsExactly(t *testing.T) {
	longLine := strings.Repeat("x", 9000) + "\n"
	utf8 := "中文\n✓ 选择 1\néèê\n末行无换行✓"
	inputs := map[string]string{
		"empty":               "",
		"single newline":      "\n",
		"consecutive blanks":  "\n\n\n",
		"typical":             "1\n0\n0\ny\n",
		"no trailing newline": "1\n0\n0\ny",
		"single unterminated": "yes",
		"long line":           longLine,
		"utf8 multibyte":      utf8,
		"mixed":               "a\n\nbbb\n中文x\nz",
	}
	bufSizes := []int{1, 2, 3, 7, 16, 4096, 65536}
	for name, in := range inputs {
		for _, size := range bufSizes {
			t.Run(name+"/buf="+strconv.Itoa(size), func(t *testing.T) {
				r := newLineDelimitedReader(strings.NewReader(in))
				buf := make([]byte, size)
				var got bytes.Buffer
				reads := 0
				for {
					n, err := r.Read(buf)
					chunk := append([]byte(nil), buf[:n]...)
					got.Write(chunk)
					// One line per Read: if a chunk contains '\n', it must be
					// the LAST byte (no bytes belonging to the next line).
					if i := bytes.IndexByte(chunk, '\n'); i >= 0 && i != len(chunk)-1 {
						t.Fatalf("Read crossed a line boundary: chunk=%q", chunk)
					}
					reads++
					if reads > 10*len(in)+1000 {
						t.Fatal("Read loop did not terminate (infinite loop / (0,nil) spin)")
					}
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatalf("unexpected error: %v", err)
					}
				}
				if got.String() != in {
					t.Fatalf("byte-exact reconstruction failed: got %d bytes, want %d bytes", got.Len(), len(in))
				}
			})
		}
	}
}

// TestQA_LineDelimitedReaderEOFIsStable: once EOF is reached it must be
// returned on every subsequent Read (the done flag must prevent re-reading).
func TestQA_LineDelimitedReaderEOFIsStable(t *testing.T) {
	r := newLineDelimitedReader(strings.NewReader("y\n"))
	buf := make([]byte, 8)
	if n, err := r.Read(buf); n != 2 || err != nil || string(buf[:n]) != "y\n" {
		t.Fatalf("first Read = (%d, %v, %q), want (2, nil, %q)", n, err, buf[:n], "y\n")
	}
	for i := 0; i < 5; i++ {
		if n, err := r.Read(buf); n != 0 || err != io.EOF {
			t.Fatalf("Read #%d after EOF = (%d, %v), want (0, EOF)", i+2, n, err)
		}
	}
}

// TestQA_LineDelimitedReaderZeroLenP: a zero-length buffer must return
// (0, nil) and consume nothing.
func TestQA_LineDelimitedReaderZeroLenP(t *testing.T) {
	r := newLineDelimitedReader(strings.NewReader("1\n0\n"))
	if n, err := r.Read(nil); n != 0 || err != nil {
		t.Fatalf("Read(nil) = (%d, %v), want (0, nil)", n, err)
	}
	if n, err := r.Read([]byte{}); n != 0 || err != nil {
		t.Fatalf("Read(empty) = (%d, %v), want (0, nil)", n, err)
	}
	buf := make([]byte, 4)
	if n, err := r.Read(buf); n != 2 || err != nil || string(buf[:n]) != "1\n" {
		t.Fatalf("zero-length reads consumed data: (%d, %v, %q)", n, err, buf[:n])
	}
}

// qaDataThenError returns its payload together with a non-EOF error on the
// first Read, then keeps returning that error.
type qaDataThenError struct {
	data []byte
	err  error
	sent bool
}

func (r *qaDataThenError) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, r.data), r.err
	}
	return 0, r.err
}

// TestQA_LineDelimitedReaderReadError: a read error other than io.EOF must not
// swallow the buffered data. The data is delivered first (with a nil error),
// the error is surfaced on the next Read, and stays stable.
func TestQA_LineDelimitedReaderReadError(t *testing.T) {
	wantErr := errors.New("qa: simulated device read failure")
	r := newLineDelimitedReader(&qaDataThenError{data: []byte("abc"), err: wantErr})
	buf := make([]byte, 8)
	n, err := r.Read(buf)
	if n != 3 || err != nil || string(buf[:n]) != "abc" {
		t.Fatalf("data lost before error: (%d, %v, %q)", n, err, buf[:n])
	}
	for i := 0; i < 3; i++ {
		if n, err := r.Read(buf); n != 0 || !errors.Is(err, wantErr) {
			t.Fatalf("Read #%d = (%d, %v), want (0, %v)", i+2, n, err, wantErr)
		}
	}
}

// TestQA_UnterminatedConfirmAccepted is the end-to-end proof that the deferred
// EOF logic really cooperates with huh's accessible PromptBool: a final "y"
// with NO trailing newline must still be consumed as the confirmation rather
// than being dropped as EOF (which would fall back to "N" -> Cancelled.).
func TestQA_UnterminatedConfirmAccepted(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	if err := Run(strings.NewReader("1\n0\n0\ny"), &out, Options{Port: 3001}); err != nil {
		t.Fatalf("Run errored: %v", err)
	}
	if strings.Contains(out.String(), "Cancelled.") {
		t.Fatalf("unterminated 'y' was treated as EOF/cancel:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Configuration written.") {
		t.Fatalf("expected Configuration written.:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "claude", "settings.json")); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

// TestQA_BlankLinesDoNotHang: consecutive empty lines are consumed as huh's
// invalid-number re-prompts (one line per Read), and the flow still completes
// with valid trailing input, without an infinite loop.
func TestQA_BlankLinesDoNotHang(t *testing.T) {
	home := isolatedHome(t)
	var out bytes.Buffer
	// 3 blank lines -> 3 invalid-number re-prompts inside the agent form;
	// then 1 selects, 0 ends; model 0 keeps defaults; confirm y writes.
	if err := Run(strings.NewReader("\n\n\n1\n0\n0\ny\n"), &out, Options{Port: 3001}); err != nil {
		t.Fatalf("Run errored: %v", err)
	}
	if !strings.Contains(out.String(), "Configuration written.") {
		t.Fatalf("blank lines broke the flow:\n%s", out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "claude", "settings.json")); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

// TestQA_OnlyNewlinesCancelsWithoutHang: an input made solely of blank lines
// must terminate gracefully (Cancelled., nil error), not hang.
func TestQA_OnlyNewlinesCancelsWithoutHang(t *testing.T) {
	isolatedHome(t)
	var out bytes.Buffer
	if err := Run(strings.NewReader("\n\n\n"), &out, Options{Port: 3001}); err != nil {
		t.Fatalf("Run must not error: %v", err)
	}
	if !strings.Contains(out.String(), "Cancelled.") {
		t.Fatalf("expected Cancelled.:\n%s", out.String())
	}
	if strings.Contains(out.String(), "Configuration written.") {
		t.Fatal("must not write config on empty selection")
	}
}
