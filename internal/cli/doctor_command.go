package cli

import (
	"fmt"
	"io"
)

const tagCommand = "command"

// commandLine gives doctor a copyable line for a missing program's installer.
// The fault keeps its one-line shape, and this line keeps the command easy to copy.
func (n need) commandLine() []docLine {
	if n.Found || n.LoginPath != "" || n.cmd == "" {
		return nil
	}
	return []docLine{{tagCommand, "", n.cmd}}
}

// printDoctorLine leaves a copyable command by itself instead of adding the
// report's tags to the command a person may paste into a terminal.
func printDoctorLine(w io.Writer, l docLine) {
	if l.tag == tagCommand {
		fmt.Fprintf(w, "    %s\n", l.text)
		return
	}
	fmt.Fprintf(w, "  %-5s  %-10s  %s\n", l.tag, l.name, l.text)
}
