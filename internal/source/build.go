package source

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
)

// MaxBuildBody bounds what a call's build command may print: one request
// body, as large as the largest answer a call may read.
const MaxBuildBody = AnswerLimit

// RunBuild runs a call's build command and reads the request body it prints.
// Its argv, input, environment, directory, timeout and process-group rules
// are Run's; its answer is any one JSON value, at most MaxBuildBody. A
// non-zero exit is a refusal and returns the command's bounded stderr;
// anything else that prevents a body being sent is an error. Neither holds
// the command's stdout.
func RunBuild(ctx context.Context, what string, argv []string, in map[string]any, env []string, dir string, timeout time.Duration) (json.RawMessage, string, error) {
	out, stderr, refusal, err := runRefusing(ctx, what, argv, in, env, dir, timeout, MaxBuildBody)
	if err != nil || refusal != "" {
		return nil, refusal, err
	}
	if err := oneValue(what, out); err != nil {
		return nil, "", fmt.Errorf("%w%s", err, stderr)
	}
	return json.RawMessage(out), "", nil
}

// runRefusing is what RunBuild and RunCall share: the command run as Run
// runs it, printing at most bound bytes. A non-zero exit is a refusal, and
// the command's own bounded stderr is all it says (the exit's own words
// where it said nothing); anything else that stops it answering is an
// error, ending with that stderr as Run's do. stderr is the same tail for
// a command that answered, for whoever finds its answer wanting.
func runRefusing(ctx context.Context, what string, argv []string, in map[string]any, env []string, dir string, timeout time.Duration, bound int) (out []byte, stderr, refusal string, err error) {
	var stdin []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, "", "", fmt.Errorf("%s: its input is no JSON object: %v", what, err)
		}
		stdin = b
	}
	if timeout <= 0 {
		timeout = Timeout
	}
	out, stderr, err = config.Command{What: what, Argv: argv, Dir: dir, Env: env, Stdin: stdin, Timeout: timeout, Max: bound}.OutputWithStderr(ctx)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			refusal = strings.TrimPrefix(stderr, "; its stderr ended: ")
			if refusal == "" {
				refusal = err.Error()
			}
			return nil, "", refusal, nil
		}
		return nil, "", "", err
	}
	return out, stderr, "", nil
}

// oneValue refuses output that is not exactly one JSON value, saying nothing
// of what was printed: a build command may have printed anything.
func oneValue(what string, out []byte) error {
	if len(bytes.TrimSpace(out)) == 0 {
		return fmt.Errorf("%s: it printed nothing: a build's answer is one JSON value", what)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("%s: what it printed is not one JSON value", what)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("%s: it printed more than one JSON value", what)
	} else if err != io.EOF {
		return fmt.Errorf("%s: what it printed is not one JSON value", what)
	}
	return nil
}

// BuildBody runs the build command of the call name of d with params and
// credential, from its installed copy under home. The command receives the
// typed parameters as {"params": ...}, the credential under the variable
// the call's kind uses, and the declaration's base. Its refusal is a
// CallError of BuildRefused; breakage is one of BuildFailed. readSecret
// is Do's.
func BuildBody(ctx context.Context, d *Declaration, name string, params map[string]string, credential, home string, readSecret func(path string) (secret []byte, resolved string, err error)) (json.RawMessage, error) {
	fail := func(reason, format string, args ...any) error {
		return &CallError{Source: d.Name, Call: name, Reason: reason, msg: fmt.Sprintf(format, args...)}
	}
	redact := func(text string) string {
		if credential != "" {
			text = strings.ReplaceAll(text, credential, "[credential redacted]")
		}
		return text
	}
	c, ok := d.Call(name)
	if !ok {
		return nil, fail(NoCall, "the source has no call of that name")
	}
	if len(c.Build) == 0 {
		return nil, fail(BuildFailed, "the call names no build command")
	}
	if d.Credential != nil {
		switch {
		case credential == "":
			return nil, fail(NoCredential, "no credential: the source's env file does not set %s", d.Credential.EnvOf(c.Kind))
		case strings.ContainsFunc(credential, func(r rune) bool { return r < ' ' || r == 0x7f }):
			return nil, fail(NoCredential, "the value of %s has a control character in it, a line break perhaps: it is one line", d.Credential.EnvOf(c.Kind))
		}
	}
	vals, err := c.values(params, nil, readSecret)
	if err != nil {
		return nil, fail(BadParam, "%v", err)
	}
	typed := make(map[string]any, len(vals))
	for name, val := range vals {
		typed[name] = val.typed
	}
	command := Command{Key: "call." + name + ".build", Argv: c.Build}
	argv := slices.Clone(c.Build)
	argv[0] = command.Program(home, d.Name)
	what := command.Key + " of source " + d.Name
	body, refusal, err := RunBuild(ctx, what, argv, map[string]any{"params": typed}, d.CommandEnvOf(c.Kind, credential), home, 0)
	switch {
	case err != nil:
		return nil, fail(BuildFailed, "%s", redact(err.Error()))
	case refusal != "":
		return nil, fail(BuildRefused, "%s", redact(refusal))
	default:
		return body, nil
	}
}
