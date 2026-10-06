package source

import (
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/config"
)

// ReadCredential reads the credential of d as a call needs it: the value of
// the variable d names, from envFile, the file the operator set for the
// source, read now. state says in words where it is, for showing, and never
// holds the value; err says why there is none, with the command that sets
// it up.
func (d *Declaration) ReadCredential(envFile string) (value, state string, err error) {
	if d.Credential == nil {
		return "", "none: the file names no credential", nil
	}
	name := d.Credential.Env
	if envFile == "" {
		return "", "", fmt.Errorf("%s is not set up: yardr source set %s env_file=<a file of mode 0600 that sets %s>", name, d.Name, name)
	}
	f, err := config.ReadEnvFile(envFile)
	if err != nil {
		return "", "", err
	}
	if value = Lookup(f.Env, name); value == "" {
		return "", "", fmt.Errorf("env_file %s does not set %s", f.Path, name)
	}
	return value, name + " is set in " + f.Path, nil
}

// Lookup is the value of name in env, the KEY=VALUE lines of an env file:
// of a name set twice the last, as in a pane's environment.
func Lookup(env []string, name string) string {
	value := ""
	for _, e := range env {
		if k, v, _ := strings.Cut(e, "="); k == name {
			value = v
		}
	}
	return value
}
