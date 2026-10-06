package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// The operator says once, in the onboarding, how agents should work with
// them, and every session reads the answer when it starts (yardr prime). It
// is a setting, not text in the role files: a change reaches the next
// session with no init, and a role a depot committed needs no edit.

// The settings the answer is stored in.
const (
	styleSetting = "operator_style"
	noteSetting  = "operator_note"
)

// defaultStyle is where the form's question starts. It is the form's alone:
// a yard without the setting has no style, and its primes say nothing.
const defaultStyle = "show"

// workStyle is one answer to "How should agents work with you?".
type workStyle struct {
	key   string // the setting's value
	label string // the answer as the form words it
	// means is the answer's meaning in the operator's words, on the form
	// page and in yardr settings.
	means string
	// tells is the same meaning as instructions to an agent, in a prime.
	// Both live here so that the form and the primes cannot drift apart.
	tells string
}

// workStyles are the answers, from the most explaining to the least.
var workStyles = []workStyle{
	{"guide", "Guide me",
		"explain each step and why; show no raw commands unless asked; ask before anything.",
		"Explain each step and why, in plain words, before you take it. Show no raw commands unless asked: " +
			"say what a command does instead. Ask before anything you do on their behalf."},
	{"show", "Show me",
		"explain briefly; show the command being run; ask on decisions.",
		"Explain briefly what you do. Show the command you run. Ask when there is a decision to make."},
	{"work", "Work with me",
		"terse; assume I read code and git; decide small things and tell me.",
		"Be terse. Assume they read code and git: no explaining of either. Decide small things yourself " +
			"and say what you decided."},
	{"report", "Just report",
		"decide what is yours; lead with the result; come to me only for decisions that are mine.",
		"Decide what is yours to decide. Lead with the result, then how you got there, if at all. Come to them " +
			"only for decisions that are theirs."},
}

// styleKeys are the styles' names, in order.
func styleKeys() []string {
	out := make([]string, len(workStyles))
	for i, s := range workStyles {
		out[i] = s.key
	}
	return out
}

// styleOf is the style of that name.
func styleOf(key string) (workStyle, bool) {
	for _, s := range workStyles {
		if s.key == key {
			return s, true
		}
	}
	return workStyle{}, false
}

// checkStyle refuses what is no style, naming the four.
func checkStyle(v string) error {
	if _, ok := styleOf(v); !ok {
		return fmt.Errorf("%s %q: there is %s", styleSetting, v, strings.Join(styleKeys(), ", "))
	}
	return nil
}

// checkNote keeps the note to the one line it is printed as.
func checkNote(v string) error {
	if strings.ContainsAny(v, "\n\r") {
		return errors.New(noteSetting + ": one line")
	}
	return nil
}

// operatorPrime is what a prime says about the operator.
type operatorPrime struct {
	Name string `json:"name"`
	// Style and Label are the operator_style setting and the answer it
	// stands for; Tells is what it asks of the agent. All three are absent
	// when only the note is set.
	Style string `json:"style,omitempty"`
	Label string `json:"label,omitempty"`
	Tells string `json:"tells,omitempty"`
	Note  string `json:"note,omitempty"`
}

// primeOperator reads what a prime says about the operator; nil on a yard
// with neither setting, which primes as it did before there were any. A
// stored style this build does not know (a newer build's) is passed over
// rather than guessed at.
func primeOperator(ctx context.Context, st store.API) (*operatorPrime, error) {
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	style, ok := styleOf(all[styleSetting])
	note := strings.TrimSpace(all[noteSetting])
	if !ok && note == "" {
		return nil, nil
	}
	return &operatorPrime{Name: or(all["operator"], defaultOperator()),
		Style: style.key, Label: style.label, Tells: style.tells, Note: note}, nil
}

// lines are the section's lines under its heading: how to work with the
// operator, then the note. The instructions are about dealing with the
// person, so they say so: most sessions never write to the operator, and
// "ask before anything" is not a rule for their own work.
func (o *operatorPrime) lines() []string {
	var out []string
	if o.Style != "" {
		out = append(out, fmt.Sprintf("%s asked agents to work with them in the style `%s` (%q). "+
			"In what you write to %s and what you bring to them: %s", o.Name, o.Style, o.Label, o.Name, o.Tells))
	}
	if o.Note != "" {
		out = append(out, fmt.Sprintf("What %s wants agents to know: %s", o.Name, o.Note))
	}
	return out
}

// guided reports whether the operator asked to be guided: the setup's pages
// then show no command lines.
func guided(ctx context.Context, st store.API) bool {
	all, err := st.Settings(ctx)
	return err == nil && all[styleSetting] == "guide"
}
