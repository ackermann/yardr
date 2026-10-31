package store

import "strings"

// AskRan is what an approved run ask ran: the command, which is the one the
// ask stored, and what it prints at a terminal, made by the server.
type AskRan struct {
	Argv   []string `json:"argv"`
	Result string   `json:"result"`
	// PendingReason says why a carrying out still waits, when one of its
	// external reports was refused with ask again later. It is cleared
	// when the carrying out finishes.
	PendingReason string `json:"pending_reason,omitempty"`
	// Failed says the carrying out of an approved apply failed: Result
	// then holds the failure in words, and the card reads as failed, not
	// as ran. Absent on every approval that ran in its own write.
	Failed bool `json:"failed,omitempty"`
}

// Command is the command that ran, whole.
func (r AskRan) Command() string { return runCommandLine(r.Argv) }

func (r AskRan) resultLines() []string {
	result := r.Result
	if result == CarryPending {
		result = r.PendingReason
		if result == "" {
			return nil
		}
	}
	lines := strings.Split(result, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if r.Result == CarryPending && line == "" {
			continue
		}
		out = append(out, "      "+NoteLetters(line))
	}
	return out
}
