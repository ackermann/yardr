package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// loginTimeout is how long a login a program leads may take, from the start
// of the harness's login command to its end: a person signs in on a web
// page meanwhile. A login nobody finishes ends then, and with it the port
// the command listens on. Tests shorten it.
var loginTimeout = 15 * time.Minute

// loginInputTimeout is how long one input of a led login waits for its
// answer through a secret ask: a person reads the ask and types where
// nothing shows it meanwhile. An input nobody answers ends the login then,
// with a JSON error. Tests shorten it.
var loginInputTimeout = 10 * time.Minute

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
	// Login is the reference the input waits under in the yard: a secret
	// ask naming it (`yardr bead ask --secret-login`) answers this input,
	// and the value is handed to the login and kept nowhere. Absent where
	// the yard has none to wait in: the input is answered on stdin alone.
	Login string `json:"login,omitempty"`
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

// loginSecrets are inputs yardr has passed to the harness. A harness may
// echo one in a prompt or an error; neither is a line yardr should repeat.
type loginSecrets struct {
	mu     sync.RWMutex
	values []string
}

func (s *loginSecrets) add(value string) {
	if value == "" {
		return
	}
	s.mu.Lock()
	s.values = append(s.values, value)
	s.mu.Unlock()
}

func (s *loginSecrets) redact(text string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, value := range s.values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	return text
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
	var group string
	cmd := &cobra.Command{
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

The answers are read from stdin, one JSON object a line. Where the yard
runs, an input also waits under the reference its need names as login: a
secret ask naming it (yardr bead ask --secret-login) answers the input the
same way, for a person with no stdin to the leader, and the value is handed
to the login and kept nowhere. The harness opens
the sign-in page itself where it can, and a login made there ends the run
with no input given. Once the login command has ended well, confirm asks
whether to make the one real call yardr doctor --try makes, for this kind
only: it costs a model call, so it is made on a true only, and "proved" says
whether it answered. A login that fails ends with {"error":"..."} and
status 1; so does stdin closing before the login has ended, which stops the
login command. What is put in is passed to the harness and kept nowhere.

Without --json it says what to run in a terminal. Only a kind whose file has
a recipe is led: the recipe is read from what one version of the harness
printed, and may stop matching when the harness prints something else.

With --group the login is led with that group's env: its env_file is read
as the runner reads it for the group's sessions, and the login command and
the proving call run with its entries joined, so the login lands in the
folder the group's env names (CLAUDE_CONFIG_DIR for Claude Code). A kind
with no recipe is logged in in a terminal with the group's env set
instead. A login made for a group sits in a folder on this machine, which
whoever runs the machine can read: bringing a subscription trusts the
operator the way the machine's own login does.`,
		Example: `  yardr harness login claude          # what to run in a terminal
  yardr harness login claude --json   # for a program: needs on stdout, answers on stdin
  yardr harness login claude --group hal-builders --json   # into that group's folder`,
		RunE: func(c *cobra.Command, args []string) error {
			h, ok := agents.Lookup(args[0])
			if !ok {
				if !agents.IsKind(args[0]) {
					return fmt.Errorf("%s is no harness kind; herdr starts: %s", args[0], strings.Join(agents.Kinds(), ", "))
				}
				h.Kind, h.Label = args[0], args[0]
			}
			var env []string
			var file string
			if group != "" {
				st, err := a.store(c.Context())
				if err != nil {
					return err
				}
				if env, file, err = groupLoginEnv(c.Context(), st, group); err != nil {
					return err
				}
			}
			exe := agents.Executable(h.Kind)
			if !a.json {
				if r := h.LoginRecipe; r == nil {
					if group != "" {
						fmt.Fprintf(c.OutOrStdout(), "yardr knows no login command of %s: log it in in a terminal with group %s's env set (%s).\n", h.Label, group, file)
					} else {
						fmt.Fprintf(c.OutOrStdout(), "yardr knows no login command of %s: start %s in a terminal and log in on its own screen.\n", h.Label, exe)
					}
				} else {
					fmt.Fprintf(c.OutOrStdout(), "%s is logged in with its own command, in a terminal:\n  %s %s\n", h.Label, exe, strings.Join(r.Argv, " "))
					fmt.Fprintf(c.OutOrStdout(), "A program leads the same login for a person without one: yardr harness login %s --json\n", h.Kind)
					if group != "" {
						fmt.Fprintf(c.OutOrStdout(), "With --group %s the same login is led with that group's env, so it lands in the folder its env names.\n", group)
					}
				}
				return nil
			}
			if h.LoginRecipe == nil {
				if group != "" {
					return fmt.Errorf("yardr has no login recipe for %s: log it in in a terminal with group %s's env set (%s)", h.Kind, group, file)
				}
				return fmt.Errorf("yardr has no login recipe for %s: it is logged in in a terminal, on its own screen", h.Kind)
			}
			path, err := lookPath(exe)
			if err != nil {
				return fmt.Errorf("%s is not on PATH, so there is nothing to log in: %s", exe, agents.InstallHint(h.Kind))
			}
			// The yard an input waits in, for a person with no stdin to the
			// leader: the store looked up for the group, or handed in. A
			// login led with none waits on stdin alone, and leading one
			// makes none: without a store file there is nothing to connect
			// to, and a server that does not answer leaves stdin to lead.
			loginSt := a.st
			if loginSt == nil {
				if cfg, err := a.config(); err == nil {
					if _, err := os.Stat(cfg.DBPath()); err == nil {
						loginSt, _ = a.store(c.Context())
					}
				}
			}
			return leadLogin(c.Context(), h, path, a.stdin, c.OutOrStdout(), env, loginSt)
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "lead the login with the group's env, so it lands in that group's folder")
	return cmd
}

// loginSecretResult is what one input's wait in the yard came to: the value
// a secret ask gave it, or why there is none.
type loginSecretResult struct {
	id    string
	value string
	err   error
}

// newLoginID is the reference one input waits under: the kind, and enough
// of the random to never meet another wait's.
func newLoginID(kind string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return kind + "-" + hex.EncodeToString(b[:])
}

// leadLogin plays h's login recipe with the harness at path: it starts the
// harness's own login command on pipes, prints each need of it on out as a
// JSON line and takes the answers from in, a JSON line each, until the
// command ends. Then, on a yes, it makes the one real call that proves the
// login (tryHarnessCall) and prints the end. An answer to a need that was
// not asked, or is over, is passed by: the login may end in the browser
// while the person still types. env, when given, joins the environment of
// both the login command and the proving call, over the process's own: a
// login led for a group lands in the folder its env names.
//
// With st, each input also waits in the yard under the reference its need
// names: a secret ask naming it answers the input as a stdin answer does,
// for a person with no stdin to the leader. An input answered on stdin
// leaves its wait, and one answered through the yard leaves stdin's; an
// input neither answers within loginInputTimeout ends the login. With none
// the login is led on stdin alone.
func leadLogin(ctx context.Context, h agents.Harness, path string, in io.Reader, out io.Writer, env []string, st store.API) error {
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
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	ownGroup(cmd)
	typed, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Why a login failed is on stderr; a harness that says it on stdout is
	// read there.
	var printed, errs lockedBuffer
	secrets := &loginSecrets{}
	// prompts are the lines the login command waits on, as its screen
	// reads them: the loop below says each as the input need, with the
	// reference it waits under where the yard has one.
	prompts := make(chan string, 4)
	cmd.Stdout, cmd.Stderr = io.MultiWriter(&loginScreen{recipe: r, say: say, secrets: secrets, prompts: prompts, over: over}, &printed), &errs
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
	given := make(chan loginSecretResult, 4)
	waiting, leave := "", func() {}
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
				// Answered on stdin: its wait, if it still waits, is left,
				// and what it may still hand over is passed by as a wait
				// that is over.
				leave()
				waiting, leave = "", func() {}
				// A harness that ended meanwhile takes nothing: its end is
				// read next.
				secrets.add(line)
				_, _ = io.WriteString(typed, line+"\n")
			}
		case text := <-prompts:
			if st == nil {
				say(loginNeed{Need: needInput, Text: text, Secret: true})
				break
			}
			// A prompt past one still waiting supersedes it: the harness
			// waits on this line now.
			leave()
			id := newLoginID(h.Kind)
			waitCtx, stopWait := context.WithTimeout(loginCtx, loginInputTimeout)
			readyCtx, stopReady := context.WithCancel(waitCtx)
			waiting, leave = id, func() { stopReady(); stopWait() }
			go func() {
				value, err := st.LoginWait(waitCtx, id)
				select {
				case given <- loginSecretResult{id: id, value: value, err: err}:
				case <-over:
				}
				if err != nil {
					stopReady()
				}
			}()
			if err := st.LoginWaitReady(readyCtx, id); err != nil {
				stopWait()
				stopReady()
				_ = halt()
				select {
				case res := <-given:
					if res.id == id && res.err != nil && !errors.Is(res.err, context.Canceled) {
						err = res.err
					}
				default:
				}
				if errors.Is(err, context.DeadlineExceeded) {
					return fmt.Errorf("no input was given within %s: the login was stopped", loginInputTimeout)
				}
				return fmt.Errorf("registering the login's input wait: %v: the login was stopped", err)
			}
			say(loginNeed{Need: needInput, Text: text, Secret: true, Login: id})
		case res := <-given:
			if res.id != waiting {
				// A wait that is over: answered on stdin, or superseded.
				break
			}
			switch {
			case errors.Is(res.err, context.Canceled):
				// The login as a whole ended first: its end is read next.
				break
			case res.err != nil:
				_ = halt()
				if errors.Is(res.err, context.DeadlineExceeded) {
					return fmt.Errorf("no input was given within %s: the login was stopped", loginInputTimeout)
				}
				return fmt.Errorf("waiting for the login's input: %v: the login was stopped", res.err)
			default:
				line := strings.TrimSpace(res.value)
				if strings.ContainsAny(line, "\r\n") {
					_ = halt()
					return fmt.Errorf("an input is one line, and the one given had several: the login was stopped")
				}
				leave()
				waiting, leave = "", func() {}
				// As a stdin answer: to the harness, and nowhere printed.
				secrets.add(line)
				_, _ = io.WriteString(typed, line+"\n")
			}
		}
	}
	leave()
	switch {
	case err == nil:
	case errors.Is(loginCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil:
		return fmt.Errorf("%s did not end within %s: the login was stopped", command, loginTimeout)
	default:
		said := lastLines(secrets.redact(errs.String()), 3)
		if said == "" {
			said = lastLines(secrets.redact(printed.String()), 3)
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
			try = tryHarnessCall(ctx, h.Kind, path, env)
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
// its recipe names: an address is said as a need, and the line the command
// waits on is handed to the loop on prompts, which says it as the input
// need once the wait is registered. Only the command's stdout is written
// here, by one goroutine, and the command has ended before anything else is
// said.
type loginScreen struct {
	recipe  *agents.LoginRecipe
	say     func(any)
	secrets *loginSecrets
	// prompts carries the lines the command waits on to the loop; over ends
	// the handing when the run is over.
	prompts chan<- string
	over    <-chan struct{}
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
			s.say(loginNeed{Need: needOpenURL, URL: s.secrets.redact(page)})
		}
		s.wait()
		s.line, s.waits = s.line[:0], false
	}
	// The line the command waits on does not end: it is a prompt.
	s.wait()
	return len(p), nil
}

// wait hands the line being printed, once, to the loop as the one the
// command waits on.
func (s *loginScreen) wait() {
	if line := string(s.line); !s.waits && s.recipe.Waits(line) {
		s.waits = true
		text := s.secrets.redact(lastLines(line, 1))
		select {
		case s.prompts <- text:
		case <-s.over:
		}
	}
}
