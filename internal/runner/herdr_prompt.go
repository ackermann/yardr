package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// startAgent starts the agent and gives it its first prompt. An agent that
// is not ready yet (blocked on a question a human must answer, or just
// slow) is not a failure: the session is kept and the prompt is left on the
// handle as pending, for the dispatcher to deliver once the agent is idle.
// So is a prompt that was sent and did not arrive (see arrived): one that
// never showed is sent again, one left in the agent's input is submitted,
// and one taken as arrived without its echo is looked at again.
// With trust, the questions yardr answers itself are the folder-trust
// dialog the kind's file names and, with key, its use-this-API-key dialog
// (see acceptTrust); the handle then says which.
func (h *Herdr) startAgent(ctx context.Context, args []string, name, prompt string, handle map[string]any, trust, key *agents.Trust) (map[string]any, error) {
	var err error
	// A pane created a moment ago may not have its shell prompt up yet.
	for attempt := 0; ; attempt++ {
		_, err = h.run(ctx, args...)
		var herr *herdrError
		if err == nil || attempt == 20 || !errors.As(err, &herr) || herr.Code != "agent_pane_busy" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	var herr *herdrError
	if errors.As(err, &herr) && herr.Code == "agent_not_ready" {
		pane, _ := handle["pane"].(string)
		answered := ""
		if pane != "" {
			switch {
			case trust != nil && h.acceptTrust(ctx, pane, *trust):
				answered = dispatch.TrustAccepted
			case key != nil && h.acceptTrust(ctx, pane, *key):
				answered = dispatch.KeyAccepted
			}
		}
		if answered == "" {
			handle[store.PendingPrompt] = prompt
			return handle, nil
		}
		handle[answered] = true
		if !h.ready(ctx, handle) {
			// Slow, or at a next question: the dispatcher takes it from here.
			handle[store.PendingPrompt] = prompt
			return handle, nil
		}
	} else if err != nil {
		return handle, err
	}
	if up, known := h.HarnessScreen(ctx, handle); known && !up {
		// Started, and not the harness's own screen yet: keys typed now go
		// to whatever runs before it. The dispatcher types the prompt once
		// the screen is up.
		handle[store.PendingPrompt] = prompt
		handle[dispatch.PromptLook] = PromptNotTyped
		return handle, nil
	}
	pane, _ := handle["pane"].(string)
	took, err := h.prompt(ctx, name, pane, prompt)
	if err != nil {
		return handle, err
	}
	if took == tookRefused {
		// At a dialog, and nothing was typed: as an agent that was not ready.
		handle[store.PendingPrompt] = prompt
		return handle, nil
	}
	saw := h.arrived(ctx, handle, prompt)
	handle[dispatch.PromptLook] = saw + ", herdr " + took
	switch {
	case saw == sawShown:
	case took == tookBlocked:
		// Sent, and herdr saw a dialog. A readable pane with no echo
		// says the agent did not take it; without that proof, keep the
		// send guarded so a later trust answer cannot enter a question
		// reached by the prompt.
		handle[store.PendingPrompt] = prompt
		if saw != sawAbsent {
			handle[dispatch.PromptUnconfirmed] = true
		}
	case saw == sawNoPane:
	case took == tookWorking, saw == sawUnreadable, saw == sawUnknown:
		// Taken, as far as herdr, a pane that cannot be read, or a prompt
		// beyond the inspected lines can tell, and any can be wrong: the
		// dispatcher looks again before the prompt leaves the handle.
		handle[store.PendingPrompt] = prompt
		handle[dispatch.PromptUnconfirmed] = true
		if took == tookWorking {
			handle[dispatch.PromptStarted] = true
		}
	default:
		// Sent, and nothing in the pane to show that it was taken: the
		// dispatcher sends it again once the agent is idle, or submits the
		// text that is in its input, unless it shows up meanwhile.
		handle[store.PendingPrompt] = prompt
	}
	return handle, nil
}

// promptBound bounds herdr's wait for an agent to start on a prompt
// (`agent prompt --wait --timeout`): a little over the five seconds herdr
// itself gives a prompt before it calls it stalled, so that herdr's answer
// comes first. Like trustWait it is spent inside the dispatcher's pass, and
// only by a prompt the agent did not start on. A variable so tests can
// shorten it.
var promptBound = 6 * time.Second

// What herdr said of a prompt it was given to wait on (see prompt). The
// first three are kept on the handle next to what the pane showed
// (dispatch.PromptLook).
const (
	tookWorking = "working" // typed, and herdr saw the agent working
	tookBlocked = "blocked" // typed, and herdr saw the agent at a dialog
	tookStalled = "stalled" // typed, and herdr saw the agent start on nothing
	tookRefused = "refused" // not typed: the agent was at a dialog already
)

// prompt has herdr type text into the agent called name, submit it and wait
// until it sees the agent working or at a dialog, and says which. herdr
// gives a prompt sent to an agent that is not working five seconds to show
// either and then says agent_prompt_stalled; an agent already at a dialog
// is refused before anything is typed (agent_blocked), since the keys would
// answer the dialog.
//
// Found live (yardr-7y16): the yardmaster's first prompt stalled and nothing
// said so for seven minutes. yardr sent prompts without --wait and learned
// from its own looks at the pane, over ten seconds, what herdr says in
// five. The wait is herdr's now. The look is still yardr's (see arrived):
// stalled does not say the text never reached the agent, so what is done
// next is decided by where the pane shows it.
//
// A timeout, and any error herdr gives other than agent_blocked, is read as
// a stall: the text may be in the pane, and the look tells. So is an answer
// that names neither state. An error that is no answer of herdr's (it is
// down, or missing) is returned: nothing is known of the prompt then.
func (h *Herdr) prompt(ctx context.Context, name, pane, text string) (took string, err error) {
	raw, err := h.run(ctx, "agent", "prompt", name, text, "--wait", "--until", "working", "--until", "blocked",
		"--timeout", strconv.FormatInt(promptBound.Milliseconds(), 10))
	var herr *herdrError
	if errors.As(err, &herr) && herr.Code == "agent_blocked" {
		// Refused before anything was typed: the snapshot stands.
		return tookRefused, nil
	}
	// Typed, or an error that does not say it was not: the pass's snapshot
	// of the pane no longer stands, and a later check reads the pane again.
	// Let the pane decide, as it does for a stall.
	dropPane(ctx, pane)
	if err != nil {
		return tookStalled, nil
	}
	var info struct {
		Agent struct {
			Status string `json:"agent_status"`
		} `json:"agent"`
	}
	_ = json.Unmarshal(raw, &info)
	switch info.Agent.Status {
	case "working":
		return tookWorking, nil
	case "blocked":
		return tookBlocked, nil
	}
	return tookStalled, nil
}

// promptScan is how far back in a pane a first prompt is looked for.
const promptScan = 200

// What arrived saw of a first prompt in the agent's pane. It is kept on the
// handle with what herdr said of the prompt (dispatch.PromptLook).
const (
	sawShown      = "shown"      // echoed above the agent's input: it was taken
	sawNoPane     = "no pane"    // the handle names no pane to look at
	sawUnreadable = "unreadable" // the pane could not be read
	sawUnknown    = "unknown"    // the prompt may be above the lines herdr returned
	sawTyped      = "typed"      // in the agent's input, not submitted
	sawNoInput    = "no input"   // in a pane whose harness has drawn no input
	sawAbsent     = "absent"     // nowhere in the pane
	sawNoScreen   = "no screen"  // not typed: the harness had not drawn its screen (see HarnessScreen)
)

// PromptNotTyped is the dispatch.PromptLook of a first prompt that was not
// typed because the harness had not drawn its screen yet (see startAgent);
// the dispatcher types it once the screen is up. The onboarding's wait for
// the yardmaster reads it to say the harness waits in its pane.
const PromptNotTyped = sawNoScreen + ", not typed"

// arrived says what the pane of the agent shows of the prompt herdr just
// sent it. It is one look: herdr has waited for the agent to start on the
// prompt (see prompt), and what the look leaves open the dispatcher's
// passes close. Found live (yardr-ykbt):
// `agent prompt` succeeding says the keys were sent, not that they were
// taken. A Claude Code still coming up after its trust dialog was reported
// idle and dropped them, and the session sat there without its first prompt.
// Found live (yardr-cxsb): a prompt whose text is in the agent's input did
// not arrive either. The Enter after it was not taken as a submit, and the
// crew member sat idle on its own prompt until a person pressed Return. The
// dispatcher submits that one (see PromptTyped); here it is only told from
// one that arrived.
//
// Only the echo is proof. Found live (yardr-myyv): two builders on a
// loaded machine sat idle with the prompt in their input, and their
// handles had no pending prompt: one look had taken it as arrived, and
// nothing looked again. An agent that herdr saw working, and a pane
// that cannot be read, are each one reading at one moment; startAgent
// leaves such a prompt to the dispatcher as unconfirmed, which a pane that
// can never be read then costs one pass.
//
// Found live (yardr-7y16): the yardmaster's handle said "shown, look 1" and
// its prompt sat in Claude Code's input for seven minutes. The keys had
// come while Claude Code was still starting: the pane echoed them, with no
// input drawn yet to tell them from a message it took, and that text was
// read as the echo. For a harness whose input mark yardr knows, a pane
// without the mark is one that is still coming up and proves nothing: a
// prompt that shows in no other way is left to the dispatcher (see
// promptPlace).
func (h *Herdr) arrived(ctx context.Context, handle map[string]any, prompt string) (saw string) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return sawNoPane
	}
	switch taken, typed, bare, err := h.lookFor(ctx, pane, handle, prompt); {
	case err != nil:
		if errors.Is(err, errBeyond) {
			return sawUnknown
		}
		return sawUnreadable
	case taken:
		return sawShown
	case typed:
		return sawTyped
	case bare:
		return sawNoInput
	}
	return sawAbsent
}

// PromptShown reports whether the pane of the session shows prompt as one
// the agent took: a first prompt left pending because it had not shown in
// time (see arrived) may have been taken since, and is then not sent a
// second time; one the dispatcher delivered is pending until it shows. Text
// that is only in the agent's input is not shown (see PromptTyped). An error
// says the pane could not be read, which tells nothing either way (see
// arrived).
func (h *Herdr) PromptShown(ctx context.Context, handle map[string]any, prompt string) (bool, error) {
	taken, _, err := h.promptPlace(ctx, handle, prompt)
	return taken, err
}

// PromptTyped reports whether prompt is in the input of the session's agent
// and nowhere above it: typed, and not submitted.
func (h *Herdr) PromptTyped(ctx context.Context, handle map[string]any, prompt string) (bool, error) {
	_, typed, err := h.promptPlace(ctx, handle, prompt)
	return typed, err
}

// SubmitPrompt presses Enter in the session's pane: what a prompt that is
// typed in the agent's input still needs. The text is not sent again, which
// would put it in the input twice.
func (h *Herdr) SubmitPrompt(ctx context.Context, handle map[string]any) error {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return fmt.Errorf("handle has no pane")
	}
	return h.sendKey(ctx, pane, "enter")
}

func (h *Herdr) promptPlace(ctx context.Context, handle map[string]any, prompt string) (taken, typed bool, err error) {
	pane, _ := handle["pane"].(string)
	if pane == "" {
		return false, false, fmt.Errorf("handle has no pane")
	}
	taken, typed, _, err = h.lookFor(ctx, pane, handle, prompt)
	return taken, typed, err
}

// errBeyond says a prompt is not in the lines read of a pane, and that herdr
// holds more above them: it may be there, and nothing is known.
var errBeyond = errors.New("the prompt is not in the pane's last lines, and the pane has more above them")

// lookFor reads pane and finds prompt in it (see promptPlace). An error
// says nothing is known: the pane could not be read, or the prompt is
// nowhere in a read as long as was asked for, which a longer one might show
// it above (errBeyond). A shorter read is all herdr holds; of an agent on
// the alternate screen that is its screen alone (see paneForm), and a
// prompt the agent took and printed a screenful after is not in it. That
// one the read cannot tell from a prompt that was lost: the dispatcher
// goes by whether herdr saw the agent start on it (dispatch.PromptStarted).
func (h *Herdr) lookFor(ctx context.Context, pane string, handle map[string]any, prompt string) (taken, typed, bare bool, err error) {
	text, err := h.readPane(ctx, pane, promptScan)
	if err != nil {
		return false, false, false, err
	}
	kind, _ := handle[harnessKind].(string)
	taken, typed, bare = promptPlace(text, prompt, kind)
	if lines := strings.Split(text, "\n"); !taken && !typed && len(lines) >= promptScan {
		// Only of a pane that shows the harness's input: one without it is
		// of a harness still coming up, which is told as that.
		rows, _ := rowsOf(kind, lines, nil)
		if input, _ := inputLine(rows, kind); input >= 0 {
			return false, false, false, errBeyond
		}
	}
	return taken, typed, bare, nil
}

// harnessKind is a handle key: the kind of harness the session's agent was
// started as (claude, codex, ...), which says how its pane is read (see
// promptPlace). A handle from before the key was written has none, and its
// pane is read as one of a kind yardr does not know.
const harnessKind = "kind"

// firstPrompt is a handle key: what a crew member's agent was first told.
// A bead's session is told where its brief is, which its handle says; a
// crew member's prompt is made of what the handle does not hold, so the
// dispatcher, which looks for it in the agent's input later, reads it here.
const firstPrompt = "first_prompt"

// InputMarked reports whether the agent of handle is of a kind whose input
// mark yardr knows (dispatch.InputMarker): only in the pane of such a one
// is the last line that begins with a mark known to be its input.
func (h *Herdr) InputMarked(handle map[string]any) bool {
	kind, _ := handle[harnessKind].(string)
	_, ok := marksOf(kind)
	return ok
}
