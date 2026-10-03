package pack

import "testing"

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
