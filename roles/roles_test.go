package roles

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// Every yard installs these, so they must not carry one yard's person, rig,
// language or tools: those belong in the rig's own .yardr/roles/.
func TestTemplatesAreGeneric(t *testing.T) {
	specific := regexp.MustCompile(`(?i)christopher|mckean|\byard\b(?:[^/]|$)|rig check yard|go vet|go test|go install|\(Go\)|herdr|XDG_|YARDR_HOME|\bmain\b|unix.socket`)
	files, err := fs.Glob(FS, "*.md")
	if err != nil || len(files) != 4 {
		t.Fatalf("embedded roles = %v, %v", files, err)
	}
	for _, name := range files {
		body, _ := FS.ReadFile(name)
		for i, line := range strings.Split(string(body), "\n") {
			// "this yard" and "the yard" are yardr's word for the installation,
			// not a rig's name.
			l := regexp.MustCompile(`(?i)\b(this|the) yard\b`).ReplaceAllString(line, "")
			// The mayor's setup names the runner group add needs; the
			// herdr rules that are one yard's are what this keeps out.
			l = strings.ReplaceAll(l, "--runner herdr", "")
			if m := specific.FindString(l); m != "" {
				t.Errorf("%s:%d names %q: %s", name, i+1, m, line)
			}
		}
	}
	for _, name := range []string{"mayor.md", "builder.md", "reviewer.md"} {
		if body, _ := FS.ReadFile(name); !strings.Contains(string(body), "{{operator}}") {
			t.Errorf("%s does not name {{operator}}", name)
		}
	}
}
