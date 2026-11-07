package source

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// RunCall answers the call name of d, which names run in place of a request,
// with the command it names, run from its installed copy under home: a
// service no request reaches has its program carried by the declaration, as a
// build's is, and the yard sends nothing to it. The command is handed the
// typed parameters as {"params": ...}, the credential under the variable the
// call's kind uses and the declaration's base, as BuildBody's is, and prints
// one JSON object of at most MaxCommandOutput bytes within Timeout. That
// object, after the call's select and held to its answer.max as an HTTP
// answer is, is the answer, under a status of 200. What the command reaches
// it reaches itself, with the credential it was handed.
//
// A non-zero exit is the command's refusal, a CallError of RunRefused that
// says its last stderr lines; whatever else keeps it from one bounded object
// (no program, no answer in time, too much, no object) is one of RunFailed.
// Neither holds what the command printed on stdout, and the credential's
// literal value is cut from what they say. readSecret is Do's.
func RunCall(ctx context.Context, d *Declaration, name string, params map[string]string, credential, home string, readSecret func(path string) (secret []byte, resolved string, err error)) (*Result, error) {
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
	if len(c.Run) == 0 {
		return nil, fail(RunFailed, "the call names no run command")
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
	for p, val := range vals {
		typed[p] = val.typed
	}
	command := Command{Key: "call." + name + ".run", Argv: c.Run}
	argv := slices.Clone(c.Run)
	argv[0] = command.Program(home, d.Name)
	what := command.Key + " of source " + d.Name
	start := time.Now()
	out, stderr, refusal, err := runRefusing(ctx, what, argv, map[string]any{"params": typed}, d.CommandEnvOf(c.Kind, credential), home, 0, MaxCommandOutput)
	switch {
	case err != nil:
		return nil, fail(RunFailed, "%s", redact(err.Error()))
	case refusal != "":
		return nil, fail(RunRefused, "%s", redact(refusal))
	}
	if err := oneObject(what, out); err != nil {
		return nil, fail(RunFailed, "%s", redact(err.Error()+stderr))
	}
	answer := selected(out, c.Answer.Select)
	if len(answer) > c.Answer.Max {
		return nil, fail(TooLarge, "the answer is %d bytes, over the call's bound of %d (answer.max in the declaration raises it, to %d at most): it is refused, not cut, since half a JSON document misleads",
			len(answer), c.Answer.Max, MaxCommandOutput)
	}
	return &Result{Status: http.StatusOK, Answer: answer, Bytes: len(out), Took: time.Since(start)}, nil
}
