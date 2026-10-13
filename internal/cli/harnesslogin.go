package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
)

// stdin is where a command a program leads reads its answers; tests replace
// it.
var stdin io.Reader = os.Stdin

// loginTimeout is how long a login a program leads may take, from the start
// of the harness's login command to its end: a person signs in on a web
// page meanwhile. A login nobody finishes ends then, and with it the port
// the command listens on. Tests shorten it.
var loginTimeout = 15 * time.Minute

// The needs of a login a program leads (loginNeed.Need).
const (
	needChoose  = "choose"
	needOpenURL = "open_url"
	needInput   = "input"
	needConfirm = "confirm"
)

// loginNeed is one line of yardr harness login --json: what the login needs
// of the person next, for the program that leads it to show.
type loginNeed struct {
	Need string `json:"need"` // choose open_url input confirm
	// Text is the question, for choose and confirm, and the line the
	// harness waits on, for input.
	Text string `json:"text,omitempty"`
	// Options are the methods to choose from, by their labels.
	Options []string `json:"options,omitempty"`
	// URL is the page to sign in on: always an https address.
	URL string `json:"url,omitempty"`
	// Secret says of an input that it is kept nowhere: yardr passes it to
	// the harness and writes it down in no place, and so must its asker.
	Secret bool `json:"secret,omitempty"`
}

// loginAnswer is one line a program answers with: the one field of the need
// it answers.
type loginAnswer struct {
	Choose  *string `json:"choose"`
	Input   *string `json:"input"`
	Confirm *bool   `json:"confirm"`
	// err is a line that is no answer.
	err error
}

// loginDone is the last line of a login that was made.
type loginDone struct {
	Done bool   `json:"done"`
	Kind string `json:"kind"`
	// Proved is whether one real call to the harness answered: the
	// harness's own login command ending well is not that (see
	// tryHarnessCall).
	Proved bool `json:"proved"`
	// Try is what the call came to, or why it was not made.
	Try tryCheck `json:"try"`
}

func (a *app) harnessLoginCmd() *cobra.Command {
	return &cobra.Command{
		Use: "login <kind>", Short: "Lead a harness's login for a program, as JSON lines (--json)", Args: cobra.ExactArgs(1),
		Long: `A harness is logged in with its own login, in a terminal. For a person
who has none, a program (a window such as footplate) leads the login with
--json: yardr runs the harness's own login command, as the kind's file has
it (login_recipe; yardr harness show <kind>), on pipes it holds, and prints
what the login needs next, one JSON object a line:

  {"need":"choose","text":"...","options":["...","..."]}   answered {"choose":"<an option>"}
  {"need":"open_url","url":"https://..."}                  the page to sign in on; no answer
  {"need":"input","text":"...","secret":true}              answered {"input":"<what the page gave>"}
  {"need":"confirm","text":"..."}                          answered {"confirm":true} or false
  {"done":true,"kind":"claude","proved":true,"try":{...}}  the end

The answers are read from stdin, one JSON object a line. The harness opens
the sign-in page itself where it can, and a login made there ends the run
with no input given. Once the login command has ended well, confirm asks
whether to make the one real call yardr doctor --try makes, for this kind
only: it costs a model call, so it is made on a true only, and "proved" says
whether it answered. A login that fails ends with {"error":"..."} and
status 1; so does stdin closing before the login has ended, which stops the
login command. What is put in is passed to the harness and kept nowhere.

Without --json it says what to run in a terminal. Only a kind whose file has
a recipe is led: the recipe is read from what one version of the harness
printed, and may stop matching when the harness prints something else.`,
		Example: `  yardr harness login claude          # what to run in a terminal
  yardr harness login claude --json   # for a program: needs on stdout, answers on stdin`,
		RunE: func(c *cobra.Command, args []string) error {
			h, ok := agents.Lookup(args[0])
			if !ok {
				if !agents.IsKind(args[0]) {
					return fmt.Errorf("%s is no harness kind; herdr starts: %s", args[0], strings.Join(agents.Kinds(), ", "))
				}
				h.Kind, h.Label = args[0], args[0]
			}
			exe := agents.Executable(h.Kind)
			if !a.json {
				if r := h.LoginRecipe; r == nil {
					fmt.Printf("yardr knows no login command of %s: start %s in a terminal and log in on its own screen.\n", h.Label, exe)
				} else {
					fmt.Printf("%s is logged in with its own command, in a terminal:\n  %s %s\n", h.Label, exe, strings.Join(r.Argv, " "))
					fmt.Printf("A program leads the same login for a person without one: yardr harness login %s --json\n", h.Kind)
				}
				return nil
			}
			if h.LoginRecipe == nil {
				return fmt.Errorf("yardr has no login recipe for %s: it is logged in in a terminal, on its own screen", h.Kind)
			}
			path, err := lookPath(exe)
			if err != nil {
				return fmt.Errorf("%s is not on PATH, so there is nothing to log in: %s", exe, agents.InstallHint(h.Kind))
			}
			return leadLogin(c.Context(), h, path, stdin, os.Stdout)
		},
	}
}

// leadLogin plays h's login recipe with the harness at path: it starts the
// harness's own login command on pipes, prints each need of it on out as a
// JSON line and takes the answers from in, a JSON line each, until the
// command ends. Then, on a yes, it makes the one real call that proves the
// login (tryHarnessCall) and prints the end. An answer to a need that was
// not asked, or is over, is passed by: the login may end in the browser
// while the person still types.
func leadLogin(ctx context.Context, h agents.Harness, path string, in io.Reader, out io.Writer) error {
	r := h.LoginRecipe
	enc := json.NewEncoder(out)
	// An address is printed as it is, not with its & written as \u0026.
	enc.SetEscapeHTML(false)
	say := func(v any) { _ = enc.Encode(v) }

	over := make(chan struct{})
	defer close(over)
	answers := make(chan loginAnswer)
	go func() {
		defer close(answers)
		lines := bufio.NewScanner(in)
		for lines.Scan() {
			var ans loginAnswer
			if strings.TrimSpace(lines.Text()) == "" {
				continue
			}
			// The line is not repeated in the error: it may be the code.
			if err := json.Unmarshal(lines.Bytes(), &ans); err != nil {
				ans = loginAnswer{err: fmt.Errorf("an answer is one JSON object a line, and a line on stdin was none")}
			}
			select {
			case answers <- ans:
			case <-over:
				return
			}
		}
	}()

	args := slices.Clone(r.Argv)
	if len(r.Methods) > 0 {
		var labels []string
		for _, m := range r.Methods {
			labels = append(labels, m.Label)
		}
		say(loginNeed{Need: needChoose, Text: "How do you log in to " + h.Label + "?", Options: labels})
		var label string
		for chosen := false; !chosen; {
			ans, ok := <-answers
			switch {
			case !ok:
				return fmt.Errorf("stdin was closed before a login method was chosen: the login is led by answers on stdin, a JSON object a line, and nothing was started")
			case ans.err != nil:
				return ans.err
			case ans.Choose != nil:
				label, chosen = *ans.Choose, true
			}
		}
		m, ok := r.Method(label)
		if !ok {
			return fmt.Errorf("%q is no login method of %s: %s", label, h.Kind, strings.Join(labels, ", "))
		}
		args = append(args, m.Args...)
	}

	// In an empty directory of its own, as the one real call is: a login
	// is nobody's project's.
	dir, err := os.MkdirTemp("", "yardr-login")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	loginCtx, stop := context.WithTimeout(ctx, loginTimeout)
	defer stop()
	cmd := exec.CommandContext(loginCtx, path, args...)
	cmd.Dir = dir
	ownGroup(cmd)
	typed, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Why a login failed is on stderr; a harness that says it on stdout is
	// read there.
	var printed, errs lockedBuffer
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&loginScreen{recipe: r, say: say}, &printed), &errs
	command := agents.Executable(h.Kind) + " " + strings.Join(args, " ")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", command, err)
	}
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	// halt stops the login command, its whole process group, and waits for
	// it: nothing of it is left listening. It returns how the command
	// ended, nil for one that had ended well by itself.
	halt := func() error { stop(); return <-ended }
	for running := true; running; {
		select {
		case err = <-ended:
			running = false
		case ans, ok := <-answers:
			switch {
			case !ok:
				if halt() != nil {
					return fmt.Errorf("stdin was closed before %s ended: the login was stopped", command)
				}
				running = false
			case ans.err != nil:
				_ = halt()
				return ans.err
			case ans.Input != nil:
				line := strings.TrimSpace(*ans.Input)
				if strings.ContainsAny(line, "\r\n") {
					_ = halt()
					return fmt.Errorf("an input is one line, and the one given had several: the login was stopped")
				}
				// A harness that ended meanwhile takes nothing: its end is
				// read next.
				_, _ = io.WriteString(typed, line+"\n")
			}
		}
	}
	switch said := lastLines(errs.String(), 3); {
	case err == nil:
	case errors.Is(loginCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf("%s did not end within %s: the login was stopped", command, loginTimeout)
	default:
		if said == "" {
			said = lastLines(printed.String(), 3)
		}
		return fmt.Errorf("%s: %v: %s", command, err, said)
	}

	say(loginNeed{Need: needConfirm, Text: fmt.Sprintf("Check that %s answers? That makes one real call to it, which costs a model call on your account with it.", h.Label)})
	try := tryCheck{State: tryNotTried, Output: "stdin was closed before the check was answered"}
	for ans := range answers {
		if ans.err != nil {
			return ans.err
		}
		if ans.Confirm == nil {
			continue
		}
		if try.Output = "the check was declined"; *ans.Confirm {
			try = tryHarnessCall(ctx, h.Kind, path)
		}
		break
	}
	say(loginDone{Done: true, Kind: h.Kind, Proved: try.State == tryOK, Try: try})
	if try.State != tryOK && try.State != tryNotTried {
		return errCheckFailed
	}
	return nil
}

// loginScreen reads what a login command prints, as it comes, for the lines
// its recipe names, and says each as a need. Only the command's stdout is
// written here, by one goroutine, and the command has ended before anything
// else is said.
type loginScreen struct {
	recipe *agents.LoginRecipe
	say    func(any)
	// line is the line being printed, which has not ended.
	line []byte
	// waits is whether line was said as the one the command waits on.
	waits bool
	// pages are the addresses that were said.
	pages []string
}

// loginLineMax is as much of one line as is read: an address is far
// shorter.
const loginLineMax = 16 << 10

func (s *loginScreen) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != '\n' {
			if len(s.line) < loginLineMax {
				s.line = append(s.line, b)
			}
			continue
		}
		// An address is read from a whole line only: a line still being
		// printed may hold half of it.
		if page := s.recipe.Page(string(s.line)); page != "" && !slices.Contains(s.pages, page) {
			s.pages = append(s.pages, page)
			s.say(loginNeed{Need: needOpenURL, URL: page})
		}
		s.wait()
		s.line, s.waits = s.line[:0], false
	}
	// The line the command waits on does not end: it is a prompt.
	s.wait()
	return len(p), nil
}

// wait says the line being printed as the one the command waits on, once.
func (s *loginScreen) wait() {
	if line := string(s.line); !s.waits && s.recipe.Waits(line) {
		s.waits = true
		s.say(loginNeed{Need: needInput, Text: lastLines(line, 1), Secret: true})
	}
}
