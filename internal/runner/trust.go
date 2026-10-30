package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// A harness may ask whether a folder is trusted the first time it is
// started in it. Nobody watches an agent yardr starts, and the operator
// already said the folder is theirs by making it a depot or a crew
// member's directory, so yardr answers the dialog of a kind whose harness
// file names it (`trust`: the text that marks it, the answers that accept
// it, the cursor that selects it), and no other question. Answering the
// dialog leaves the harness's own trust record for the harness to write.
//
// Claude Code has no flag, setting or environment variable that skips the
// dialog in an interactive session (2.1.288). The other way is to write
// projects.<path>.hasTrustDialogAccepted into ~/.claude.json, an
// undocumented file that every running Claude session rewrites under a lock
// of its own; a second writer would race them.
//
// One directory is seed-only: the session's own home. An accept there is
// honoured for the session but never written back, so every start draws the
// dialog again, while a true written beforehand is honoured and kept (both
// probed live, yardr-88nb). Before starting a crew member's Claude session
// in its home, yardr writes the true itself (see maybeSeedHomeTrust), once:
// later starts find it true and write nothing.
//
// A harness may also ask whether to use the API key in its session's
// environment, on its first start with one set and no login. The key is
// only ever there by the operator's hand (a herdr pane never inherits the
// daemon's environment), so yardr answers that dialog too, of a kind whose
// file names it (`api_key`), with the accepting answer. It is answered
// where folder trust is, at the start and on later passes, and kept on the
// handle under its own mark; trust=false, which is folder trust's, leaves
// it answered.
//
// A harness may draw onboarding screens on its first start in a fresh
// home, before any login and before any input. yardr answers those too,
// of a kind whose file names them (`onboarding`), each entry in turn
// wherever it shows: at the start and on later passes, each kept on the
// handle under its own mark, since the entries stand at different times.
// An entry with no cursor is answered with Enter alone.
//
// After a login the yard led, the onboarding is not answered but marked
// done (see MarkOnboarded), so the sessions that start again on the login
// do not meet it. A screen the harness draws behind it, and stops a start
// the same way, has an entry of the same list: Claude Code's offer of its
// fullscreen renderer is the last entry of its file, answered Not now.

// trustWait bounds the wait for an agent to be ready after its trust dialog
// was answered. Start runs inside the dispatcher's pass, so it is short; an
// agent that takes longer keeps its prompt pending. A variable so tests can
// shorten it.
var trustWait = 20 * time.Second

// trustRedrawWait bounds waiting for a harness to repaint the dialog after a
// cursor key. A terminal redraw can be delayed under load; sending another
// key against the old screen could move the cursor back to the first answer.
// A variable so a test whose dialog does redraw can wait for it however busy
// the machine is.
var trustRedrawWait = 2 * time.Second

// trustEntry is the folder-trust dialog yardr answers for an agent with
// this config: the one its kind's harness file names, and none when the
// file names none or the group or crew member says `trust=false`.
func trustEntry(cfg map[string]any) *store.Trust {
	kind, _ := agents.Lookup(agents.Kind(cfg))
	if kind.Trust == nil || cfg["trust"] == false {
		return nil
	}
	return kind.Trust
}

// keyEntry is the use-this-API-key dialog yardr answers for an agent with
// this config: the one its kind's harness file names, and none when the
// file names none. `trust=false` leaves it answered: that flag is folder
// trust's, and the key in the session's environment is the operator's own
// consent to use it.
func keyEntry(cfg map[string]any) *store.Trust {
	kind, _ := agents.Lookup(agents.Kind(cfg))
	if kind.ApiKey == nil {
		return nil
	}
	return kind.ApiKey
}

// onboardingEntries are the onboarding screens yardr answers for an agent
// with this config, in the order its kind's harness file names them, and
// none when the file names none. `trust=false` leaves them answered, as
// it leaves the API-key dialog: that flag is folder trust's.
func onboardingEntries(cfg map[string]any) []store.Trust {
	kind, _ := agents.Lookup(agents.Kind(cfg))
	return kind.Onboarding
}

// startupShown reports whether pane shows one of kind's startup dialogs
// yardr answers itself (folder trust, API key, onboarding): the dialog's
// mark, drawn or drawing. The dialog's cursor carries the mark of the
// harness's input (Claude Code's ❯), so a showing dialog reads as the
// harness's screen (HarnessScreen): startAgent asks this before it delivers
// the first prompt, and answers the dialog instead of talking over it.
// Found live (yardr-gkyy): the trust dialog drew before the start looked,
// the prompt was typed into it, and its Enter selected "No, exit", so the
// session died three seconds old. It is asked of the kind's own entries,
// not the session's: with trust=false the dialog still gates the delivery,
// and the answer is then nothing to give (the human answers it in the pane).
func (h *Herdr) startupShown(ctx context.Context, pane, kind string) bool {
	hn, ok := agents.Lookup(kind)
	if !ok || pane == "" {
		return false
	}
	entries := make([]store.Trust, 0, len(hn.Onboarding)+2)
	if hn.Trust != nil {
		entries = append(entries, *hn.Trust)
	}
	if hn.ApiKey != nil {
		entries = append(entries, *hn.ApiKey)
	}
	entries = append(entries, hn.Onboarding...)
	if len(entries) == 0 {
		return false
	}
	screen, err := h.readPane(ctx, pane, 0)
	if err != nil {
		return false
	}
	for _, l := range strings.Split(screen, "\n") {
		for _, e := range entries {
			if e.ShowsDialog(l) {
				return true
			}
		}
	}
	return false
}

// answerStartup answers the startup dialog showing in pane, if it is one
// yardr answers itself (folder trust, API key, onboarding, in that order),
// and reports the handle mark for the answer, "" when none landed.
func (h *Herdr) answerStartup(ctx context.Context, pane string, trust, key *store.Trust, onboarding []store.Trust) string {
	switch {
	case trust != nil && h.acceptTrust(ctx, pane, *trust):
		return dispatch.TrustAccepted
	case key != nil && h.acceptTrust(ctx, pane, *key):
		return dispatch.KeyAccepted
	default:
		if i := h.acceptOnboarding(ctx, pane, onboarding); i >= 0 {
			return dispatch.OnboardingAccepted(i)
		}
	}
	return ""
}

// maybeSeedHomeTrust records dir as trusted in the Claude trust store before
// a session starts in it, when dir is the home the session's harness will
// see: the session's HOME entry when its environment sets one, else this
// process's home, which is the harness's on a local yard (the pane inherits
// it; yardr sets YARDR_HOME, not HOME). Any other directory is left for the
// harness to record on the accept, as is a session whose config redirects
// its store (CLAUDE_CONFIG_DIR moves .claude.json too, and which directory
// is seed-only there was not probed). A store that is missing is left for
// the harness to write, and one already true is left untouched, so in the
// steady state this only reads. It never fails the start: at worst the
// dialog draws and is answered as before.
func (h *Herdr) maybeSeedHomeTrust(cfg map[string]any, dir string, sessionEnv []string) {
	if agents.Kind(cfg) != "claude" || trustEntry(cfg) == nil {
		return
	}
	home := ""
	for _, e := range sessionEnv {
		k, v, _ := strings.Cut(e, "=")
		switch k {
		case "HOME":
			home = v
		case "CLAUDE_CONFIG_DIR":
			if v != "" {
				return
			}
		}
	}
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	if home == "" || realPath(dir) != realPath(home) {
		return
	}
	seeded, err := seedClaudeHomeTrust(filepath.Join(home, ".claude.json"), realPath(dir))
	switch {
	case err != nil:
		if h.Log != nil {
			h.Log.Warn("seed folder trust", "dir", dir, "err", err)
		}
	case seeded && h.Log != nil:
		h.Log.Info("seeded folder trust", "dir", dir)
	}
}

var _ dispatch.OnboardingMarker = (*Herdr)(nil)

// MarkOnboarded marks the onboarding of kind's harness done in the home its
// sessions start in (dispatch.OnboardingMarker): the dispatcher asks for it
// when a login the yard led has been made, before the sessions at the login
// screen start again. The login command the yard leads writes no
// hasCompletedOnboarding on its browser path (read in Claude Code 2.1.310),
// so a session started on the new login would draw the harness's first-start
// flow again, which can include its login menu, and the crew start would wait
// on a person at it.
//
// The home is this process's: the leader of the login ran in it, as a child
// of the daemon, and the panes inherit it. As for folder trust, a config
// that redirects the store (CLAUDE_CONFIG_DIR moves .claude.json too) is
// left alone, as is a store that is missing, which the harness writes
// itself, and one already true is left untouched. It never fails the
// restart that follows: at worst the screens draw and are answered as before.
func (h *Herdr) MarkOnboarded(_ context.Context, kind string) {
	if kind != "claude" || os.Getenv("CLAUDE_CONFIG_DIR") != "" {
		return
	}
	home, _ := os.UserHomeDir()
	if home == "" {
		return
	}
	marked, err := seedClaudeOnboarding(filepath.Join(home, ".claude.json"))
	switch {
	case err != nil:
		if h.Log != nil {
			h.Log.Warn("mark onboarding done", "kind", kind, "err", err)
		}
	case marked && h.Log != nil:
		h.Log.Info("marked onboarding done", "kind", kind)
	}
}

// seedClaudeHomeTrust sets projects.dir.hasTrustDialogAccepted in the Claude
// trust store at storePath, creating the projects map or dir's entry when
// missing, and reports whether it wrote. Every other key is kept as it was.
// A store that is missing, or one already true, is left alone, byte for
// byte: the write happens once, on the first seeded start, so the race with
// a Claude session rewriting the file at the same moment is a one-time
// event, and a side whose update is lost re-reads it on the next accept or
// start. The write itself is atomic, a rename over the store.
func seedClaudeHomeTrust(storePath, dir string) (bool, error) {
	return editClaudeStore(storePath, func(store map[string]any) (bool, error) {
		projects, ok := store["projects"].(map[string]any)
		if store["projects"] != nil && !ok {
			return false, fmt.Errorf("read %s: projects is not an object", storePath)
		}
		if projects == nil {
			projects = map[string]any{}
			store["projects"] = projects
		}
		entry, ok := projects[dir].(map[string]any)
		if projects[dir] != nil && !ok {
			return false, fmt.Errorf("read %s: the entry for %s is not an object", storePath, dir)
		}
		if entry == nil {
			entry = map[string]any{}
			projects[dir] = entry
		}
		if entry["hasTrustDialogAccepted"] == true {
			return false, nil
		}
		entry["hasTrustDialogAccepted"] = true
		return true, nil
	})
}

// seedClaudeOnboarding sets hasCompletedOnboarding in the Claude store at
// storePath and reports whether it wrote, as seedClaudeHomeTrust does for a
// folder: every other key is kept, and a store that is missing, or one
// already true, is left alone, byte for byte. The bare true is all the
// harness itself writes where it logs in with no screen to draw (the
// refresh-token form of `claude auth login`, read in Claude Code 2.1.310),
// so it is all that is written here.
func seedClaudeOnboarding(storePath string) (bool, error) {
	return editClaudeStore(storePath, func(store map[string]any) (bool, error) {
		if store["hasCompletedOnboarding"] == true {
			return false, nil
		}
		store["hasCompletedOnboarding"] = true
		return true, nil
	})
}

// editClaudeStore reads the Claude store at storePath, lets edit change it,
// and writes it back when edit says it changed something: it reports
// whether it wrote. A store that is missing is left alone, and an error of
// edit's leaves the file as it was. Numbers the edit does not touch keep
// their exact digits, and the file keeps its mode.
func editClaudeStore(storePath string, edit func(store map[string]any) (changed bool, err error)) (bool, error) {
	body, err := os.ReadFile(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", storePath, err)
	}
	var store map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&store); err != nil {
		return false, fmt.Errorf("read %s: %w", storePath, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return false, fmt.Errorf("read %s: multiple JSON values", storePath)
		}
		return false, fmt.Errorf("read %s: %w", storePath, err)
	}
	if store == nil {
		store = map[string]any{}
	}
	if changed, err := edit(store); err != nil || !changed {
		return false, err
	}
	out, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	info, err := os.Stat(storePath)
	if err != nil {
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(storePath), ".claude.json.yardr.*")
	if err != nil {
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // a no-op after the rename
	if err := tmp.Chmod(info.Mode()); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	if err := os.Rename(tmpName, storePath); err != nil {
		return false, fmt.Errorf("write %s: %w", storePath, err)
	}
	return true, nil
}

// trustAnswer finds t on a pane's screen: found when the dialog is up,
// which takes its mark and one accepting answer on separate lines,
// selected when the dialog's cursor is on the exact answer line.
func trustAnswer(screen string, t store.Trust) (found, selected bool) {
	dialog, answer := false, false
	for _, l := range strings.Split(screen, "\n") {
		dialog = dialog || t.ShowsDialog(l)
		if !answer {
			if a, on := t.ShowsAnswer(l); a != "" {
				answer, selected = true, on
			}
		}
	}
	return dialog && answer, selected
}

// AcceptTrust answers the folder-trust dialog of an agent with this config
// when that is what its pane shows, and reports whether it did
// (dispatch.TrustAcceptor): the dispatcher's later passes answer what drew
// only after the start looked, by the same rule as the start.
func (h *Herdr) AcceptTrust(ctx context.Context, handle map[string]any, cfg map[string]any) bool {
	t := trustEntry(cfg)
	pane, _ := handle["pane"].(string)
	if t == nil || pane == "" {
		return false
	}
	return h.acceptTrust(ctx, pane, *t)
}

// AcceptKey answers the use-this-API-key dialog of an agent with this
// config when that is what its pane shows, and reports whether it did
// (dispatch.TrustAcceptor): the dispatcher's later passes answer what drew
// only after the start looked, by the same rule as the start.
func (h *Herdr) AcceptKey(ctx context.Context, handle map[string]any, cfg map[string]any) bool {
	t := keyEntry(cfg)
	pane, _ := handle["pane"].(string)
	if t == nil || pane == "" {
		return false
	}
	return h.acceptTrust(ctx, pane, *t)
}

// AcceptOnboarding answers the onboarding screen of entry entry of an agent
// with this config when that is what its pane shows, and reports whether
// it did (dispatch.TrustAcceptor): the dispatcher's later passes answer
// what drew only after the start looked, by the same rule as the start.
// An entry out of range answers nothing.
func (h *Herdr) AcceptOnboarding(ctx context.Context, handle map[string]any, cfg map[string]any, entry int) bool {
	entries := onboardingEntries(cfg)
	pane, _ := handle["pane"].(string)
	if pane == "" || entry < 0 || entry >= len(entries) {
		return false
	}
	return h.acceptTrust(ctx, pane, entries[entry])
}

// acceptOnboarding answers the first of entries whose screen pane shows,
// and reports its index, -1 when none is up.
func (h *Herdr) acceptOnboarding(ctx context.Context, pane string, entries []store.Trust) int {
	for i, e := range entries {
		if h.acceptTrust(ctx, pane, e) {
			return i
		}
	}
	return -1
}

// acceptTrust answers the dialog t in pane, if that is what the pane
// shows, and reports whether it did. The dialog may open with its cursor
// off the accepting answer, so the cursor is moved and Enter is only sent
// once the screen shows it on the answer: a dialog that changed shape is
// left for a human, as any other question is. An entry with no cursor is
// answered with Enter alone when found, without moving one: without a
// cursor nothing can be seen selected.
func (h *Herdr) acceptTrust(ctx context.Context, pane string, t store.Trust) bool {
	screen, err := h.readPane(ctx, pane, 0)
	for moves := 0; err == nil; moves++ {
		found, selected := trustAnswer(screen, t)
		if !found || (!selected && moves == 4) {
			return false
		}
		if selected || t.Cursor == "" {
			return h.sendKey(ctx, pane, "enter") == nil
		}
		if err := h.sendKey(ctx, pane, "down"); err != nil {
			return false
		}
		// Found live: the dialog takes a moment to redraw, and a second
		// `down` sent meanwhile wraps the cursor back to "No, exit". So the
		// next key waits for the screen to change.
		before := screen
		for deadline := time.Now().Add(trustRedrawWait); screen == before && err == nil; {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(trustWait / 200)
			screen, err = h.readPane(ctx, pane, 0)
		}
	}
	return false
}

// HarnessVersion is the version the installed harness of kind reports
// (dispatch.Versioner): what a session_harness_dialog names beside the
// dialog it does not know. "" when nothing answers.
func (h *Herdr) HarnessVersion(ctx context.Context, kind string) string {
	return agents.HarnessVersion(ctx, kind)
}

// ready waits, up to trustWait, for the agent to take a prompt.
func (h *Herdr) ready(ctx context.Context, handle map[string]any) bool {
	for deadline := time.Now().Add(trustWait); ; {
		if s, err := h.Status(ctx, handle); err == nil && (s == "idle" || s == "done") {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			return false
		}
		time.Sleep(trustWait / 40)
	}
}

// sendKey presses key in pane. Found live: send-keys prints nothing when it
// worked, which run would take for a herdr that does not answer, so only
// the exit status is read.
func (h *Herdr) sendKey(ctx context.Context, pane, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	// The pane's text changed, or may have: the pass's snapshot of it no
	// longer stands, whether the key worked or not.
	defer dropPane(ctx, pane)
	if err := h.followSetting(ctx); err != nil {
		return err
	}
	if out, err := herdrCommand(ctx, h.Bin, "pane", "send-keys", pane, key).CombinedOutput(); err != nil {
		return fmt.Errorf("herdr pane send-keys: %w: %s", err, bytes.TrimSpace(out))
	}
	return nil
}

// dropPane forgets the pass's snapshot of pane (see dispatch.PaneCache):
// keys were typed into it. A context without a cache is nothing to forget.
func dropPane(ctx context.Context, pane string) {
	if c := dispatch.PaneCacheFrom(ctx); c != nil {
		c.Drop(pane)
	}
}

// How yardr reads a pane, said here and nowhere else (paneReadArgs builds
// every `herdr pane read`).
//
// herdr (0.9.0, "Alternate-screen history reads") answers a text read of
// `recent` or `recent-unwrapped` that asks for more lines than the screen
// has, of an idle agent that runs on the alternate screen (Claude Code in
// fullscreen, OpenCode), by scrolling the agent's transcript with its mouse
// wheel and back: the pane moves under whoever looks at it. Reads of
// `visible` and `detection`, and every read with --format ansi, are passive:
// they move nothing. So every read yardr makes by itself is a styled one,
// and a reader of plain text gets it with the SGR escapes stripped
// (paneView). The one read that fetches history is readHistory, for a
// command a person runs, and never in a pass of the daemon.
//
// A passive read of `recent-unwrapped` gives what herdr holds without
// asking the agent: the screen and herdr's scrollback above it. A harness
// on the normal screen has its transcript there, up to the lines asked for.
// One on the alternate screen has no scrollback, and the read is its
// screen: read live, the styled `recent-unwrapped --lines 200` of a
// fullscreen Claude Code and its `--source visible --format ansi` were the
// same 58 rows, byte for byte. A check must not count on more than the
// screen (yardr-dsmt).
type paneForm int

const (
	paneScreen  paneForm = iota // the displayed screen alone: passive
	paneRecent                  // the screen and herdr's scrollback above it, its last lines: passive
	paneHistory                 // as paneRecent, and herdr may scroll the agent's transcript to fetch more
)

// paneReadArgs is the `herdr pane read` of pane in form, lines of it for
// the forms that count lines.
func paneReadArgs(pane string, form paneForm, lines int) []string {
	args := []string{"pane", "read", pane}
	switch form {
	case paneScreen:
		return append(args, "--source", "visible", "--format", "ansi")
	case paneRecent:
		return append(args, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines), "--format", "ansi")
	}
	return append(args, "--source", "recent-unwrapped", "--lines", fmt.Sprint(lines))
}

// readPane is the text of pane's terminal, read passively: its last lines
// lines, or its screen when lines is 0. pane read prints the text itself,
// not a JSON result.
func (h *Herdr) readPane(ctx context.Context, pane string, lines int) (string, error) {
	return h.paneRead(ctx, pane, lines, false)
}

// readPaneStyled is readPane with the terminal's styling left in, as SGR
// escapes: what tells a harness's dimmed placeholder from typed text (see
// inputText).
func (h *Herdr) readPaneStyled(ctx context.Context, pane string, lines int) (string, error) {
	return h.paneRead(ctx, pane, lines, true)
}

// PaneScreen is the styled screen of pane: its visible screen with the
// terminal's styling left in, and nothing of herdr's scrollback. The read
// is passive (see paneForm): of an idle agent on the alternate screen it
// moves nothing. It is what the store's SessionScreen answers with.
func (h *Herdr) PaneScreen(ctx context.Context, pane string) (string, error) {
	return h.readPaneStyled(ctx, pane, 0)
}

// readHistory is the plain text of pane's last lines lines, history
// included: the one read for which herdr may scroll an idle agent's
// transcript, so only what a person runs calls it (see Herdr.History). In a
// pass of the daemon it is the passive read all the same.
func (h *Herdr) readHistory(ctx context.Context, pane string, lines int) (string, error) {
	if dispatch.PaneCacheFrom(ctx) != nil {
		return h.readPane(ctx, pane, lines)
	}
	return h.herdrPaneRead(ctx, pane, paneHistory, lines)
}

func (h *Herdr) paneRead(ctx context.Context, pane string, lines int, styled bool) (string, error) {
	if lines <= 0 {
		snap, err := h.herdrPaneRead(ctx, pane, paneScreen, 0)
		if err != nil || styled {
			return snap, err
		}
		return stripSGR(snap), nil
	}
	if c := dispatch.PaneCacheFrom(ctx); c != nil {
		return h.cachedPaneRead(ctx, c, pane, lines, styled)
	}
	snap, err := h.herdrPaneRead(ctx, pane, paneRecent, lines)
	if err != nil {
		return "", err
	}
	return paneView(snap, lines, styled), nil
}

// cachedPaneRead is paneRead through the pass's snapshot cache: the first
// check of the pass to read pane takes one styled snapshot, and every later
// check reads from it. The snapshot covers every check of a pass, so one
// read serves them whatever order they come in.
func (h *Herdr) cachedPaneRead(ctx context.Context, c *dispatch.PaneCache, pane string, lines int, styled bool) (string, error) {
	want := max(lines, promptScan)
	if snap, ok := c.Lookup(pane, want); ok {
		return paneView(snap, lines, styled), nil
	}
	snap, err := h.herdrPaneRead(ctx, pane, paneRecent, want)
	if err != nil {
		return "", err
	}
	c.Store(pane, snap, want)
	return paneView(snap, lines, styled), nil
}

// paneView is what a read of the pane's last lines lines, styled or plain,
// sees in snap, its styled snapshot: the plain text is the styled one
// without its SGR escapes, and a shorter read is its end.
func paneView(snap string, lines int, styled bool) string {
	if !styled {
		snap = stripSGR(snap)
	}
	return snapLines(snap, lines)
}

// snapLines is the last n lines of a snapshot: what herdr's --lines n shows
// of the pane. A snapshot with fewer lines is read whole.
func snapLines(snap string, n int) string {
	lines := strings.Split(snap, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1] // the empty tail a trailing newline leaves is no line
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// stripSGR is a styled snapshot without its SGR escapes: the plain text the
// snapshot holds. Only SGR (ESC [ ... m) is taken off; every other byte is
// kept as herdr printed it.
func stripSGR(styled string) string {
	i := strings.IndexByte(styled, 0x1b)
	if i < 0 {
		return styled
	}
	var b strings.Builder
	b.Grow(len(styled))
	b.WriteString(styled[:i])
	rest := styled[i:]
	for len(rest) > 0 {
		if len(rest) >= 2 && rest[0] == 0x1b && rest[1] == '[' {
			j := 2
			for j < len(rest) && (rest[j] < 0x40 || rest[j] > 0x7e) {
				j++
			}
			if j < len(rest) && rest[j] == 'm' {
				rest = rest[j+1:]
				continue
			}
		}
		b.WriteByte(rest[0])
		rest = rest[1:]
	}
	return b.String()
}

func (h *Herdr) herdrPaneRead(ctx context.Context, pane string, form paneForm, lines int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := h.followSetting(ctx); err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := herdrCommand(ctx, h.Bin, paneReadArgs(pane, form, lines)...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("herdr pane read: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
