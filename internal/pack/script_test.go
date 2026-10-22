package pack

import (
	"maps"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// A script group's cmd keeps its options (the assignments before the
// command) and its arguments; only the command is the pack's.
func TestSetScript(t *testing.T) {
	for _, c := range []struct{ cmd, script, want string }{
		{"", "/h/packs/p/scripts/land", "/h/packs/p/scripts/land"},
		{"/old/land", "/new/land", "/new/land"},
		{"YARDR_PUSH=origin YARDR_NOTIFY=boss /old/land", "/new/land", "YARDR_PUSH=origin YARDR_NOTIFY=boss /new/land"},
		{"A='x y' B=\"p q\" /old/land --fast", "/new/land", "A='x y' B=\"p q\" /new/land --fast"},
		{"'/old dir/land' -v", "/new/land", "/new/land -v"},
		{"A=1", "/new/land", "A=1 /new/land"},
		{"/old/land", "/new dir/it's", `'/new dir/it'\''s'`},
		{"A=1 /new/land", "/new/land", "A=1 /new/land"},
	} {
		if got := setScript(c.cmd, c.script); got != c.want {
			t.Errorf("setScript(%q, %q) = %q, want %q", c.cmd, c.script, got, c.want)
		}
	}
}

// A pack's env goes before the script as the assignments setScript keeps:
// names in order, whatever the map's, and values quoted so that the cmd
// reads them back as they were.
func TestGroupEnvWords(t *testing.T) {
	for _, c := range []struct {
		env  map[string]string
		want []string
	}{
		{nil, nil},
		{map[string]string{"YARDR_LAND": "squash"}, []string{"YARDR_LAND=squash"}},
		{map[string]string{"B": "2", "A": "x y", "C": "it's", "D": "$HOME; ls"}, []string{"A='x y'", "B=2", `C='it'\''s'`, "D='$HOME; ls'"}},
		{map[string]string{"A": `a'b"c d\e`, "B": "*?[x]", "C": "~/#c", "D": "=eq", "E": "é ü", "F": "$(id) `id`"}, []string{`A='a'\''b"c d\e'`, "B='*?[x]'", "C='~/#c'", "D==eq", "E='é ü'", "F='$(id) `id`'"}},
	} {
		words := Group{Env: c.env}.EnvWords()
		if !slices.Equal(words, c.want) {
			t.Errorf("EnvWords of %v = %q, want %q", c.env, words, c.want)
		}
		// A group made of them is the words, then the script, and the
		// words are the cmd's assignments, each name with its value.
		cmd := setScript(strings.Join(words, " "), "/new/land")
		assigns, i, end := splitCmd(cmd)
		if !slices.Equal(assigns, c.want) || cmd[i:end] != "/new/land" {
			t.Errorf("cmd %q splits into %q and the command %q, want %q and /new/land", cmd, assigns, cmd[i:end], c.want)
		}
		// And the cmd sets every variable to the value the pack named, as
		// the shell reads it: a group made so is one the pack keeps nothing of.
		if kept := keptEnv(Group{Env: c.env}, cmd); kept != nil {
			t.Errorf("a group made from %v keeps %q", c.env, kept)
		}
		// The shell itself reads the words as the pack named them, and runs
		// nothing a value holds: a command that prints its variables, run
		// after the words, prints the values in the order of the names.
		names := slices.Sorted(maps.Keys(c.env))
		if len(names) == 0 {
			continue
		}
		show := `sh -c 'printf "%s\n"`
		var values []string
		for _, name := range names {
			show += ` "$` + name + `"`
			values = append(values, c.env[name])
		}
		out, err := exec.Command("sh", "-c", strings.Join(words, " ")+" "+show+"'").CombinedOutput()
		if got := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n"); err != nil || !slices.Equal(got, values) {
			t.Errorf("the shell reads %q as %q (%v), want %q", words, got, err, values)
		}
	}
}

// A group the yard has is told what of the pack's env its cmd lacks, by
// what the shell would set: a quoted value is its text, a name set twice
// has the last, and a word after the command is an argument.
func TestKeptEnv(t *testing.T) {
	env := map[string]string{"A": "1", "G": "hello world"}
	for _, c := range []struct {
		cmd  string
		env  map[string]string
		want string // the pairs the Keep names, "" for no Keep
	}{
		{"/s/land", nil, ""},
		{"/s/land", env, "A=1 G='hello world'"},
		{"", env, "A=1 G='hello world'"},
		{"A=1 G='hello world' /s/land", env, ""},
		{`A="1" G=hello\ world /s/land --fast`, env, ""},
		{"A=2 G='hello world' /s/land", env, "A=1"},
		{"A=2 A=1 G='hello world' /s/land", env, ""},
		{"A=1 A=2 G='hello world' /s/land", env, "A=1"},
		{"G='hello world' /s/land A=1", env, "A=1"},
		{"YARDR_PUSH=origin A=1 /s/land", map[string]string{"A": "1"}, ""},
		{`A="$HOME" G='hello world' /s/land`, map[string]string{"A": "$HOME", "G": "hello world"}, "A='$HOME'"},
		{`A="a\qb" G='hello world' /s/land`, map[string]string{"A": "aqb", "G": "hello world"}, "A=aqb"},
		{`A="\$HOME" G='hello world' /s/land`, map[string]string{"A": "$HOME", "G": "hello world"}, ""},
		{"A=~/work G='hello world' /s/land", map[string]string{"A": "~/work", "G": "hello world"}, "A='~/work'"},
	} {
		got := keptEnv(Group{Env: c.env}, c.cmd)
		var want []string
		if c.want != "" {
			want = []string{"cmd " + or(c.cmd, "(none)") + ": the pack's env " + c.want + " is set on a new group only"}
		}
		if !slices.Equal(got, want) {
			t.Errorf("keptEnv(%v, %q) = %q, want %q", c.env, c.cmd, got, want)
		}
	}
}
