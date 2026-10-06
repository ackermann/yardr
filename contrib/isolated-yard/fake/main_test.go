package main

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// screen is as much of a terminal as the stand-in draws on: lines of text
// and a cursor, with the carriage return, the line feed and the three
// sequences the stand-in sends (cursor up, cursor right, erase below).
type screen struct {
	rows     [][]rune
	row, col int
}

func (s *screen) write(text string) {
	r := []rune(text)
	for i := 0; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\r':
			s.col = 0
		case c == '\n':
			s.row++
		case c == '\x1b' && i+1 < len(r) && r[i+1] == '[':
			j := i + 2
			for j < len(r) && r[j] >= '0' && r[j] <= '9' {
				j++
			}
			n, _ := strconv.Atoi(string(r[i+2 : j]))
			switch r[j] {
			case 'A':
				s.row = max(0, s.row-max(1, n))
			case 'C':
				s.col += max(1, n)
			case 'J':
				if s.row < len(s.rows) {
					s.rows[s.row] = s.rows[s.row][:min(s.col, len(s.rows[s.row]))]
					s.rows = s.rows[:s.row+1]
				}
			}
			i = j
		default:
			for len(s.rows) <= s.row {
				s.rows = append(s.rows, nil)
			}
			for len(s.rows[s.row]) <= s.col {
				s.rows[s.row] = append(s.rows[s.row], ' ')
			}
			s.rows[s.row][s.col] = c
			s.col++
		}
	}
}

// lines is the screen's text, without what trails each line and the screen.
func (s *screen) lines() []string {
	var out []string
	for _, r := range s.rows {
		out = append(out, strings.TrimRight(string(r), " "))
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

// standIn is a stand-in started as isolated-yard installs it (D/bin/claude),
// its output drawn on a screen.
type standIn struct {
	t   *testing.T
	d   string
	in  io.WriteCloser
	out *bufio.Reader
	scr screen
}

func start(t *testing.T, loginNeeded bool) *standIn {
	t.Helper()
	d := t.TempDir()
	exe := filepath.Join(d, "bin", "claude")
	if out, err := exec.Command("go", "build", "-o", exe, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if loginNeeded {
		if err := os.WriteFile(filepath.Join(d, "login-needed"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(exe)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		_ = cmd.Wait()
	})
	return &standIn{t: t, d: d, in: in, out: bufio.NewReader(out)}
}

// draw reads the stand-in's output onto the screen up to the next thing that
// ends with end.
func (s *standIn) draw(end string) {
	s.t.Helper()
	var got strings.Builder
	done := make(chan error, 1)
	go func() {
		for !strings.HasSuffix(got.String(), end) {
			c, _, err := s.out.ReadRune()
			if err != nil {
				done <- err
				return
			}
			got.WriteRune(c)
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			s.t.Fatalf("the stand-in's output ended at %q: %v", got.String(), err)
		}
	case <-time.After(10 * time.Second):
		s.t.Fatalf("no %q from the stand-in in 10s", end)
	}
	// As the pane's terminal passes it on: a line feed returns the carriage.
	s.scr.write(strings.ReplaceAll(got.String(), "\n", "\r\n"))
}

// boxEnd is what the stand-in's box ends with: the cursor put after the mark.
const boxEnd = "\x1b[2A\x1b[2C"

func (s *standIn) want(what string, want ...string) {
	s.t.Helper()
	got := s.scr.lines()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		s.t.Errorf("%s: the screen shows\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Text typed into the stand-in's pane shows on the line of its mark, where
// yardr reads an agent's input, and a line it took shows above a new, empty
// box. Found building yardr-ps05: the cursor was left under the box, so the
// terminal echoed typed text below the bottom rule and the input read as
// empty while text waited in it.
func TestTypedTextShowsInTheInput(t *testing.T) {
	s := start(t, false)
	s.draw(boxEnd)
	s.want("at start", rule, "❯", rule)

	// What the pane's terminal does with typed text: it echoes it where the
	// cursor is.
	s.scr.write("abc")
	s.want("typed", rule, "❯ abc", rule)

	// Enter: the terminal's echo of it, and the line reaches the stand-in.
	s.scr.write("\r\n")
	if _, err := io.WriteString(s.in, "abc\n"); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.want("taken", rule, "❯ abc", rule, "❯", rule)

	s.scr.write("de")
	s.want("typed again", rule, "❯ abc", rule, "❯ de", rule)
}

// A prompt held at the login screen was never typed into the box: it is
// drawn as taken from where the box left the cursor, on the mark's line, and
// not from a row further up.
func TestHeldPromptIsDrawnFromTheInputLine(t *testing.T) {
	s := start(t, true)
	s.draw("/login\n")
	// Sent while the terminal's echo is off: nothing shows.
	if _, err := io.WriteString(s.in, "first prompt\n"); err != nil {
		t.Fatal(err)
	}
	// The stand-in holds a line it has read; give it the time to read it.
	time.Sleep(500 * time.Millisecond)
	if err := os.Remove(filepath.Join(s.d, "login-needed")); err != nil {
		t.Fatal(err)
	}
	s.draw(boxEnd)
	s.draw(boxEnd)
	s.want("after login", "Not logged in · Run /login", "Login successful",
		rule, "❯ first prompt", rule, "❯", rule)

	s.scr.write("abc")
	s.want("typed", "Not logged in · Run /login", "Login successful",
		rule, "❯ first prompt", rule, "❯ abc", rule)
}

func TestWrappedRows(t *testing.T) {
	for _, c := range []struct{ n, cols, want int }{
		{0, 80, 1},
		{3, 80, 1},
		{78, 80, 1}, // fills the row: no wrap until another character comes
		{79, 80, 2},
		{158, 80, 2},
		{159, 80, 3},
	} {
		if got := wrappedRows(c.n, c.cols); got != c.want {
			t.Errorf("wrappedRows(%d, %d) = %d, want %d", c.n, c.cols, got, c.want)
		}
	}
}
