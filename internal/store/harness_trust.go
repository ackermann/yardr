package store

import (
	"fmt"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Trust is one dialog yardr answers itself from a harness file: the text
// that marks it on the pane's screen, the answers that accept it, and the
// mark of the selected answer, as the harness prints them. Folder trust,
// a use-this-API-key confirmation and an onboarding screen share this
// shape. The dialog is answered only when the mark shows, one answer
// shows exactly, and the cursor is seen on that answer
// (internal/runner/trust.go). An entry with no cursor, an onboarding
// screen with nothing to choose, is answered with Enter alone when its
// mark and one answer show. There is one
// answer per harness version that renamed it: Kimi 2.2.0 renamed 'Trust
// this folder' to 'Trust and continue', so its file names both. Answers
// arrive as one string or a list, and the file's old `answer` still reads
// as one. Env are entries that let a harness skip folder trust; the runner
// sets their active values only when it is allowed to answer that dialog.
// EnvNames exposes their names without values in JSON. The cursor is the
// dialog's own mark: Kimi's is ❯ though its input mark is ">".
type Trust struct {
	Dialog   string   `yaml:"dialog" json:"dialog"`
	Answers  []string `yaml:"answers" json:"answers"`
	Cursor   string   `yaml:"cursor" json:"cursor"`
	Env      []string `yaml:"env" json:"-"`
	EnvNames []string `yaml:"-" json:"env,omitempty"`
}

// UnmarshalYAML reads a trust dialog: answers arrive as one string or a
// list (a renamed answer is one added line), and the file's old `answer`
// still reads as one. Both keys at once is refused, and so is any other
// key, as the loader's KnownFields would.
func (t *Trust) UnmarshalYAML(value *yaml.Node) error {
	var keys map[string]any
	if err := value.Decode(&keys); err != nil {
		return err
	}
	for k := range keys {
		switch k {
		case "dialog", "answer", "answers", "cursor", "env":
		default:
			return fmt.Errorf("field %s not found in type store.Trust", k)
		}
	}
	var raw struct {
		Dialog  string   `yaml:"dialog"`
		Answer  any      `yaml:"answer"`
		Answers any      `yaml:"answers"`
		Cursor  string   `yaml:"cursor"`
		Env     []string `yaml:"env"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	answers, err := trustAnswers(raw.Answer, raw.Answers)
	if err != nil {
		return err
	}
	t.Dialog, t.Answers, t.Cursor, t.Env = raw.Dialog, answers, raw.Cursor, raw.Env
	return nil
}

// trustAnswers is what a dialog's answer and answers name: one string or a
// list of strings each, the two never together.
func trustAnswers(answer, answers any) ([]string, error) {
	one := func(v any) ([]string, error) {
		switch v := v.(type) {
		case nil:
			return nil, nil
		case string:
			return []string{v}, nil
		case []any:
			out := make([]string, 0, len(v))
			for _, e := range v {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("trust: answers wants a string or a list of strings")
				}
				out = append(out, s)
			}
			return out, nil
		default:
			return nil, fmt.Errorf("trust: answers wants a string or a list of strings")
		}
	}
	a, err := one(answer)
	if err != nil {
		return nil, err
	}
	b, err := one(answers)
	if err != nil {
		return nil, err
	}
	if a != nil && b != nil {
		return nil, fmt.Errorf("trust: answer and answers are the same answers under two names; keep one")
	}
	return append(a, b...), nil
}

// ShowsDialog reports whether line, one line of a pane, shows t's dialog
// mark: the start of its question, matched at either edge, since what
// follows it wraps wherever the pane wraps.
func (t Trust) ShowsDialog(line string) bool {
	l := strings.TrimSpace(line)
	return strings.HasPrefix(l, t.Dialog) || strings.HasSuffix(l, t.Dialog)
}

// ShowsAnswer reports whether line is one of t's accepting answers: the
// dialog's cursor taken off, the rest exact. selected says the cursor is
// on it. With no cursor nothing is ever selected: such an entry is
// answered with Enter alone.
func (t Trust) ShowsAnswer(line string) (answer string, selected bool) {
	l := strings.TrimSpace(line)
	answer = strings.TrimSpace(strings.TrimPrefix(l, t.Cursor))
	if !slices.Contains(t.Answers, answer) {
		return "", false
	}
	return answer, t.Cursor != "" && strings.HasPrefix(l, t.Cursor)
}
