// Package managers installs the stand-in service managers that the
// server-yard script is run against: systemctl and loginctl for a Linux
// server, launchctl for the Mac a test runs on. Each is a script under
// contrib/server-yard/fake that keeps its units as files in a state
// directory and logs every call there. The server-yard suite and the
// yard-shape matrix of contrib/isolated-yard both run the script in a yard
// of their own, so this is the one place that knows which stand-ins there
// are and which variable tells each where its state is.
package managers

import (
	"os"
	"path/filepath"
)

// standIns are the managers, each with the variable it reads its state
// directory from: the scripts fail loudly without it.
var standIns = []struct{ name, stateVar string }{
	{"systemctl", "SYSTEMCTL_STATE"},
	{"loginctl", "LOGINCTL_STATE"},
	{"launchctl", "LAUNCHCTL_STATE"},
}

// Install copies the stand-ins of the repository at root into bin, makes a
// state directory for each as state/<name>, and returns the environment
// (NAME=directory) that tells each where its state is. Every command that
// asks a manager something needs that environment, not only the script
// that sets the services up: yardr status asks launchctl whether a service
// is loaded, and without its state directory the stand-in answers that it
// is not. bin and state are the yard's own; nothing outside them is
// written.
func Install(root, bin, state string) ([]string, error) {
	var env []string
	for _, m := range standIns {
		dir := filepath.Join(state, m.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		body, err := os.ReadFile(filepath.Join(root, "contrib/server-yard/fake", m.name))
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(bin, m.name), body, 0o755); err != nil {
			return nil, err
		}
		env = append(env, m.stateVar+"="+dir)
	}
	return env, nil
}
