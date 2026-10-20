// A run ask is an ask whose answer is no words either: it proposes one
// yardr command that is the operator's to give, and the operator's approval
// (ApproveAsk) runs it. No shell line is ever run: the command is one of a
// fixed list (runList), parsed and checked when the ask is written, and run
// through the store function the command itself calls, under that
// function's own check, in the write that records the approval.
//
// The ask holds the command as the yard parsed it and the yard's own
// sentences for what it changes, made from the yard's records and never
// from the asker's words. An approval is held to the yard the card was
// written from: when what the sentences say, or what the command names, is
// no longer what the yard holds, nothing runs and the ask is asked again.
// Nothing else an ask has approves: a pick and words are refused on a run
// ask (answerable), and so is an answer that arrives saying what ran
// (answerShape).
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// MaxRunContext is how many lines of context a run ask takes: more would
// push the yard's part of the card out of sight.
const MaxRunContext = 5

// Source-add cards carry the declaration and its command files as text.
// Keep their bounds shared with the CLI that reads the files.
const (
	MaxRunSourceDeclaration = 8192
	MaxRunSourceCommandFile = 8192
	MaxRunSourceTexts       = 32 << 10
	MaxRunSourceCardLines   = 40
	// MaxRunPackDeclarations is the most declaration text a pack ask
	// holds together, and MaxRunPackFiles the most its command files,
	// their diffs and a depot plan's file texts and diffs hold together
	// (yardr-bht9 3.3): past either the pack is read at a desk.
	MaxRunPackDeclarations = 256 << 10
	MaxRunPackFiles        = 64 << 10
)

// runContextLines is how many lines the context holds. A context line is
// one line, and the limit counts lines, not entries.
func runContextLines(context []string) int {
	n := 0
	for _, line := range context {
		n += strings.Count(line, "\n") + 1
	}
	return n
}

// AskRun is the command a run ask proposes. Its writer sends Argv alone,
// the command as it is typed in a terminal, in one string or in words; the
// rest is the yard's, written in the write that stores the ask (askedRun)
// whatever the writer sent for it.
type AskRun struct {
	// Argv is the command as the yard parsed it, without the leading yardr
	// and in an order of the yard's own, so that two ways of typing one
	// command are stored as one.
	Argv []string `json:"argv"`
	// Description is the command's one-line Short from its help, shown
	// beneath the command so the person asked need not parse its flags.
	Description string `json:"description,omitempty"`
	// Does are the yard's sentences for what the command changes and what
	// is there now, and Back the command that takes the change back. They
	// are kept on the ask because the views render an ask from its note
	// alone.
	Does []string `json:"does,omitempty"`
	Back string   `json:"back,omitempty"`
	// Seen holds, for each thing the command names, what the yard held of
	// it when it wrote the sentences, whole: an approval is held to it,
	// since a sentence names a thing and cannot say all of it. For a peer
	// it is the link's key; for a source, a group, a crew member and a user
	// a SHA-256 over the part of it the command's effect rests on (seen).
	Seen map[string]string `json:"seen,omitempty"`
	// BackBy is who may run Back, in the yard's words, where that is not
	// anyone: to narrow what the yard holds is mostly anyone's, and a
	// source's limits and a user's subscriptions are not.
	BackBy string `json:"back_by,omitempty"`
	// Carried holds what adding a source takes, for the rows that add
	// one (source add here, pack apply in B2): each source's declaration
	// text with its command files' bytes. It is the ask's own, kept as
	// Does and Seen are because every view renders an ask from its note
	// alone, and an approval adds exactly what it holds. No other row
	// keeps it: askedRun throws a writer's Carried away with its Does
	// and Seen.
	Carried []AskSource `json:"carried,omitempty"`
	// Pack is the pack a pack apply ask names, as the server read it
	// from its own binary; set on that row alone.
	Pack *AskPack `json:"pack,omitempty"`
	// Value is the address a user key row carries: the value it writes
	// under the source's notify variable into the user's env file,
	// shown on the card in Does as the command's own words never show
	// it (yardr user key types it unseen). It comes from a delivery
	// the yard dropped (ClaimAddress), never from a command line: a
	// --run typed in a terminal carries none, and its card is refused.
	// No other row keeps it.
	Value string `json:"value,omitempty"`
}

// AskPack is a pack apply ask's pack as the yard's server read it: its
// name, its kind, the depot a depot kind would be re-applied to, and
// the build label the pack is embedded in. A depot kind carries the
// plan the server built inside the write that stored the ask.
type AskPack struct {
	Name  string   `json:"name"`
	Kind  string   `json:"kind,omitempty"`
	Depot string   `json:"depot,omitempty"`
	Label string   `json:"label"`
	Plan  *AskPlan `json:"plan,omitempty"`
}

// AskPlan is a depot pack's plan as a card holds it: every object with
// its action and changes, the governs, and what the plan still asks or
// refuses, in the store's own words so no view needs internal/pack.
type AskPlan struct {
	Objects []AskPlanObject `json:"objects,omitempty"`
	Governs []AskGovern     `json:"governs,omitempty"`
	// Missing, Asks, Refused and Edited name what keeps the plan from
	// asking nothing, each a line the refusal reads back.
	Missing []string `json:"missing,omitempty"`
	Asks    []string `json:"asks,omitempty"`
	Refused []string `json:"refused,omitempty"`
	Edited  []string `json:"edited,omitempty"`
	// NonText names a changed file whose old or new bytes are not
	// text: a card carries text only, so the row refuses it.
	NonText []string `json:"nontext,omitempty"`
}

// AskPlanObject is one object of a depot plan.
type AskPlanObject struct {
	Kind    string      `json:"kind"`
	Name    string      `json:"name"`
	Action  string      `json:"action"`
	Changes []AskChange `json:"changes,omitempty"`
	Keeps   []string    `json:"keeps,omitempty"`
	Shadows []string    `json:"shadows,omitempty"`
	// Of a file: its hashes whole, old and new, and for a create or
	// change its new text and the unified diff old against new, whole,
	// for the tap and a terminal.
	FileOldSHA string `json:"file_old_sha256,omitempty"`
	FileNewSHA string `json:"file_new_sha256,omitempty"`
	Text       string `json:"text,omitempty"`
	Diff       string `json:"diff,omitempty"`
}

// AskChange is one change of a plan object, as printPlan prints it.
type AskChange struct {
	Field string `json:"field"`
	Old   string `json:"old"`
	New   string `json:"new"`
}

// AskGovern is the flow a depot's beads would follow after the plan.
type AskGovern struct {
	Type string `json:"type,omitempty"`
	Flow string `json:"flow"`
	Now  string `json:"now,omitempty"`
}

// AskFile is one command file a run ask that adds a source carries: the
// program as the declaration names it, its whole text, and the SHA-256 of
// that text. The card renders the text's hash and the approval installs
// exactly these bytes, so what is approved is what the card showed.
type AskFile struct {
	Program string `json:"program"`
	Text    string `json:"text"`
	SHA256  string `json:"sha256"`
}

// AskSource is what adding one source takes, carried on a run ask: the
// source's name as its declaration gives it, the declaration's whole text,
// and each command file the declaration names by a relative program. A
// loose file's bytes are the asker's, read when the ask is written; a
// pack's are the server's, read from its own binary (B2). Either way the
// store parses the text itself and holds the approval to these bytes.
type AskSource struct {
	Name  string    `json:"name"`
	Text  string    `json:"text"`
	Files []AskFile `json:"files,omitempty"`
	// Old, Removed and Inventory are a pack ask's record of what the
	// yard's copies of a source it would replace hold as the card was
	// written: each command file's change, each entry neither
	// declaration names that the copy would remove, and the whole
	// destination inventory. A loose add replaces nothing and keeps
	// none of them.
	Old       []AskFileDiff  `json:"old,omitempty"`
	Removed   []AskRemoved   `json:"removed,omitempty"`
	Inventory []AskInventory `json:"inventory,omitempty"`
}

// AskFileDiff is one command file a pack ask would change: its program,
// the SHA-256 of the copy that stands and of the pack's file, whole,
// and the unified diff old against new. The old bytes are not carried:
// the diff is the showing, and the two hashes are what the approval is
// held to.
type AskFileDiff struct {
	Program   string `json:"program"`
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256,omitempty"`
	Diff      string `json:"diff,omitempty"`
}

// AskRemoved is one entry under a source's copies that neither the
// standing declaration nor the pack's names: applying the pack removes
// the whole destination, so the card names it with its hash. Text
// stands behind the tap where it is text within the bound.
type AskRemoved struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Symlink bool   `json:"symlink,omitempty"`
	Text    string `json:"text,omitempty"`
}

// AskInventory is one file under a source's copies as an ask found it:
// its path under <home>/sources/<name>/, its SHA-256 and its mode. A
// chmod after the ask changes the mode, and with it the approval.
type AskInventory struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Mode    string `json:"mode"`
	Size    int64  `json:"size"`
	Symlink bool   `json:"symlink,omitempty"`
}

// TakenBack is the card's line for the command that takes the change back,
// with who may run it.
func (r AskRun) TakenBack() string { return or(r.BackBy, "anyone") + " can take it back: " + r.Back }

// Command is the command whole, as it is typed in a terminal. No view
// shortens it.
func (r AskRun) Command() string { return runCommandLine(r.Argv) }

// CarryPending is what an approval of a pack apply, or of a source add
// that names commands, records until the yard's server has carried it
// out (design yardr-bht9 3.5): the ask reads as being carried out while
// its answer holds these words, and as ran or failed once the carrying
// out completes it.
const CarryPending = "approved, to be carried out"

// AskRan is what an approved run ask ran: the command, which is the one the
// ask stored, and what it prints at a terminal, made by the server.
type AskRan struct {
	Argv   []string `json:"argv"`
	Result string   `json:"result"`
	// Failed says the carrying out of an approved apply failed: Result
	// then holds the failure in words, and the card reads as failed, not
	// as ran. Absent on every approval that ran in its own write.
	Failed bool `json:"failed,omitempty"`
}

// Command is the command that ran, whole.
func (r AskRan) Command() string { return runCommandLine(r.Argv) }

func runCommandLine(argv []string) string { return "yardr " + strings.Join(argv, " ") }

// RunAsked is the command a run ask proposes, as a client shows it: the
// command whole, the yard's sentences for what it changes, and the command
// that takes it back. All of it is the yard's, and none of it the asker's.
type RunAsked struct {
	Argv        []string `json:"argv"`
	Command     string   `json:"command"`
	Description string   `json:"description,omitempty"`
	Does        []string `json:"does"`
	Back        string   `json:"back"`
	// BackBy is who may run Back, in words that stand before "can take it
	// back"; absent where that is anyone.
	BackBy string `json:"back_by,omitempty"`
	// Carried is what adding a source takes, for a client to show the
	// texts behind the card; absent on any other command.
	Carried []AskSource `json:"carried,omitempty"`
	// Pack is the pack a pack apply ask names, for a client to show
	// the plan behind the card; absent on any other command.
	Pack *AskPack `json:"pack,omitempty"`
}

// proposed is the command of the ask a as a client shows it, nil for an
// ask that proposes none.
func (a Ask) proposed() *RunAsked {
	if a.Run == nil {
		return nil
	}
	return &RunAsked{Argv: a.Run.Argv, Command: a.Run.Command(), Description: a.Run.Description,
		Does: a.Run.Does, Back: a.Run.Back, BackBy: a.Run.BackBy, Carried: a.Run.Carried, Pack: a.Run.Pack}
}

// AskApproved is what ApproveAsk did: the ask and who asked it, the command
// as it ran with the yard's sentences for it, and what it printed.
type AskApproved struct {
	Bead   string   `json:"bead"`
	Ask    uint64   `json:"ask"`
	Asker  string   `json:"asker"`
	Argv   []string `json:"argv"`
	Does   []string `json:"does"`
	Result string   `json:"result"`
}

// approvedOn is the ask a change was approved on, as the change's own event
// carries it, so that the events tie the change to what proposed it.
type approvedOn struct {
	Bead string `json:"bead"`
	Ask  uint64 `json:"ask"`
}

// runCommand is one command of the list.
type runCommand struct {
	// words begin the command, and use is how a refusal names it.
	words []string
	use   string
	// description is the command's Short in internal/cli, checked there
	// so the card keeps the help the person would see at a terminal.
	description string
	// parse reads the words after words, in whatever order they were typed,
	// into the command's one stored form. It reads no record of the yard.
	parse func(rest []string) ([]string, error)
	// check is the command's own permission check, the same the store
	// function refuses with, run as the asker: nil says the asker may run
	// the command themselves, an operator-only error says whose it is and
	// whether a run ask may propose it (AskClass), and anything else is
	// refused as the reason the command would not run. nil for the rows
	// from before checks, whose cards refuse what needs no approval in
	// their own words.
	check func(ctx context.Context, w *wtx, argv []string, actor string) error
	// card writes what the yard says of the parsed command from its records
	// as they are now: the sentences, the command that takes it back, and
	// what it saw of each thing the command names. asking is the write that
	// stores the ask, which refuses a command that would change nothing; an
	// approval reads the card again without that, and says in its own words
	// what changed (stale).
	card func(w *wtx, argv []string, asking bool) (AskRun, error)
	// stale says why an approval of the ask seq is refused, when the card it
	// was written with (was) is not the one the yard would write now. Nil
	// carries the approval on: the row held it to the card as it stands,
	// and the answer carries that card, drawn at approve time.
	stale func(w *wtx, seq uint64, was, now AskRun) error
	// run makes the change as actor through the command's own store
	// function, and answers what the command prints at a terminal.
	run func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error)
	// checkRun, cardRun and runRun are check, card and run for a row
	// whose card is made from bytes the ask carries beside argv (source
	// add: the declaration the ask holds), so they take the ask whole.
	// A row sets either the argv forms or these, never both.
	checkRun func(ctx context.Context, w *wtx, run AskRun, actor string) error
	cardRun  func(w *wtx, run AskRun, asking bool) (AskRun, error)
	runRun   func(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error)
	// whose is the user whose own the command is, where the store function
	// takes it from that user as it does from the operator: the ask is then
	// for one of the two, and the one asked approves. nil for a command
	// that is the operator's alone.
	whose func(argv []string) string
	// sole is the user who alone may be asked to approve the command,
	// with no operator leg beside them: "" where the command is not one,
	// or not in that form (a plain source grant is the operator's, one as
	// a user is that user's alone). It takes the place of whose and of
	// the operator's leg wherever it answers a name.
	sole func(argv []string) string
	// link is the peer whose link the command changes, and what a peer must
	// not do to its own link, in the words that follow "a peer must not":
	// "" and "" where the command is about no peer's link. An approval that
	// reaches the yard through that same peer's reach is refused (approveAsk,
	// WithReach), whoever is taken to approve, since the peer would then
	// change the link it speaks through on its own word.
	link func(argv []string) (peer, rule string)
	// ready reads before the write what the card needs of git, which must
	// not run inside one: the repository flows, by depot, of the depot the
	// command names (wtx.repos). nil for a command whose card reads none.
	ready func(ctx context.Context, s *Store, argv []string) (map[string]*RepoFlows, error)
	// commas says a word of the command is a list with commas between, as
	// the parts of tell= are. No other command takes the character.
	commas bool
}

// runList are the commands a run ask may name: every operator-only,
// file-free command that is one write of the store. Whether each is the
// operator's, and whether a run ask may propose it, is the command's own
// check (runCommand.check), which refuses with the same words and the
// same mark the store function refuses with: the list never says whose
// a command is twice. What is the operator's alone is refused past the
// list (aloneList). A test holds each row to the command of internal/cli
// it names.
var runList = []runCommand{{
	words:       []string{"peer", "allow"},
	use:         "yardr peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>",
	description: "Set which messages a peer link carries and its mail limit",
	parse:       parsePeerAllow,
	check:       checkPeerAllow,
	card:        (*wtx).peerAllowCard,
	stale:       (*wtx).peerAllowStale,
	run:         runPeerAllow,
	link:        func(argv []string) (string, string) { return argv[2], "widen its own link" },
	commas:      true,
}, {
	words:       []string{"peer", "intake"},
	use:         "yardr peer intake <peer> --depot <depot> --flow <flow> [--limit <n>] [--default]",
	description: "Say where the beads a peer sends go: the depot and the flow",
	parse:       parsePeerIntake,
	check:       checkPeerIntake,
	card:        (*wtx).peerIntakeCard,
	stale:       (*wtx).peerIntakeStale,
	run:         runPeerIntake,
	link:        func(argv []string) (string, string) { return argv[2], "say where its own beads go" },
	ready:       readyPeerIntake,
}, {
	words:       []string{"peer", "identity"},
	use:         "yardr peer identity <peer> <crew> --source <source> --id <id>",
	description: "Who a sender is in a source, when a crew member of a peer's yard (no <crew>: list)",
	parse:       parsePeerIdentity,
	check:       checkPeerIdentity,
	card:        (*wtx).peerIdentityCard,
	stale:       (*wtx).peerIdentityStale,
	run:         runPeerIdentity,
	link:        func(argv []string) (string, string) { return argv[2], "say who its own crew is here" },
}, {
	words:       []string{"source", "set"},
	use:         "yardr source set <source> calls_per_hour=<n>|updates_per_hour=<n>",
	description: "Change a source's credential or per-hour call limits",
	parse:       parseSourceLimit,
	check:       checkSourceLimit,
	card:        (*wtx).sourceLimitCard,
	stale:       (*wtx).sourceLimitStale,
	run: func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
		name, key, n := sourceLimitOf(argv)
		src, err := w.setSource(ctx, name, key, strconv.Itoa(n), n, actor, by)
		if err != nil {
			return "", err
		}
		return "source " + src.Name + ": " + src.LimitsLine(), nil
	},
}, {
	words:       []string{"source", "grant"},
	use:         "yardr source grant <source> read|<call>|--preset <preset> --group <group>|--crew <crew member> [--as <user>]",
	description: "Let a group's sessions or a crew member make calls of a source",
	parse:       parseGrant,
	check:       checkGrant,
	card:        (*wtx).grantCard,
	stale:       (*wtx).grantStale,
	run:         runGrant,
	sole: func(argv []string) string {
		_, _, _, _, as := grantOf(argv)
		return as
	},
}, {
	words:       []string{"source", "add"},
	use:         "yardr source add <file>",
	description: "Add the source a file declares, or replace the one of its name",
	parse:       parseSourceAdd,
	checkRun:    checkSourceAdd,
	cardRun:     (*wtx).sourceAddCard,
	stale:       (*wtx).sourceAddStale,
	runRun:      runSourceAdd,
}, {
	words:       []string{"source", "ignore"},
	use:         "yardr source ignore <source> --rm <id>",
	description: "List a source's ignored senders, or stop ignoring one",
	parse:       parseSourceIgnore,
	check:       checkSourceIgnore,
	card:        (*wtx).sourceIgnoreCard,
	stale:       (*wtx).sourceIgnoreStale,
	run:         runSourceIgnore,
}, {
	words:       []string{"pack", "apply"},
	use:         "yardr pack apply <name>",
	description: "Import a pack into a depot: set the yard to what the pack says",
	parse:       parsePackApply,
	checkRun:    checkPackApply,
	cardRun:     (*wtx).packApplyCard,
	stale:       (*wtx).packApplyStale,
	runRun:      runPackApply,
}, {
	words:       []string{"hook", "add"},
	use:         "yardr hook add <name> --source <declared source> [--unknown unknown|allow|ignore]",
	description: "Create a webhook endpoint POST /hooks/<name> (a replacement stays in a terminal)",
	parse:       parseHookAdd,
	check:       checkHookAdd,
	card:        (*wtx).hookAddCard,
	stale:       (*wtx).hookAddStale,
	run:         runHookAdd,
}, {
	words:       []string{"subscribe"},
	use:         "yardr subscribe <user> yardr [--done] [--where <key>=<value>]",
	description: "Deliver a source's events to a crew member's inbox, or a user's as notifications (no arguments: who is told what)",
	parse:       parseSubscribe,
	card:        (*wtx).subscribeCard,
	stale:       (*wtx).subscribeStale,
	run:         runSubscribe,
	whose:       func(argv []string) string { return argv[1] },
}, {
	words:       []string{"user", "set"},
	use:         "yardr user set <user> tell=<parts>",
	description: "Set where, how and what a source tells a user",
	parse:       parseUserTell,
	card:        (*wtx).userTellCard,
	stale:       (*wtx).userTellStale,
	run:         runUserTell,
	whose:       func(argv []string) string { return argv[2] },
	commas:      true,
}, {
	words:       []string{"user", "key"},
	use:         "yardr user key <user> <source>",
	description: "Give a user's own address in a source: typed without echo, or a NAME=value line on standard input",
	parse:       parseUserKey,
	checkRun:    checkUserKey,
	cardRun:     (*wtx).userKeyCard,
	stale:       (*wtx).userKeyStale,
	runRun:      runUserKey,
	whose:       func(argv []string) string { return argv[2] },
}, {
	words:       []string{"user", "add"},
	use:         "yardr user add <name>",
	description: "Add a user",
	parse:       parseUserAdd,
	check:       checkUserAdd,
	card:        (*wtx).userAddCard,
	stale:       (*wtx).userAddStale,
	run:         runUserAdd,
}, {
	words:       []string{"user", "rm"},
	use:         "yardr user rm <name>",
	description: "Remove a user (the mail stays in the log)",
	parse:       parseUserRm,
	check:       checkUserRm,
	card:        (*wtx).userRmCard,
	stale:       (*wtx).userRmStale,
	run:         runUserRm,
}, {
	words:       []string{"crew", "set"},
	use:         "yardr crew set <name> open_threads=<sources> | set_state=<sources> | peers=<peers>",
	description: "Change some settings of a crew member and keep the rest",
	parse:       parseCrewGrant,
	check:       checkCrewGrant,
	card:        (*wtx).crewGrantCard,
	stale:       (*wtx).crewGrantStale,
	run:         runCrewGrant,
	commas:      true,
}, {
	words:       []string{"crew", "identity"},
	use:         "yardr crew identity <name> --source <source> [--id <id>] [--key <key>]",
	description: "Who a crew member is in a source (no --source: list)",
	parse:       parseCrewIdentity,
	check:       checkCrewIdentity,
	card:        (*wtx).crewIdentityCard,
	stale:       (*wtx).crewIdentityStale,
	run:         runCrewIdentity,
}, {
	words:       []string{"user", "identity"},
	use:         "yardr user identity <user> --source <source> [--id <id>] [--key <key>]",
	description: "Who a user is in a source (no --source: list)",
	parse:       parseUserIdentity,
	check:       checkUserIdentity,
	card:        (*wtx).userIdentityCard,
	stale:       (*wtx).userIdentityStale,
	run:         runUserIdentity,
	whose:       func(argv []string) string { return argv[2] },
}, {
	words:       []string{"settings", "set"},
	use:         "yardr settings set <key> <value>",
	description: "Change a setting, as the operator; an empty value unsets it",
	parse:       parseSettingSet,
	check:       checkSettingSet,
	card:        (*wtx).settingSetCard,
	stale:       (*wtx).settingSetStale,
	run:         runSettingSet,
}, {
	words:       []string{"peer", "rename"},
	use:         "yardr peer rename <old> <new>",
	description: "Change the name this yard has for a peer",
	parse:       parsePeerRename,
	check:       checkPeerRename,
	card:        (*wtx).peerRenameCard,
	stale:       (*wtx).peerRenameStale,
	run:         runPeerRename,
}, {
	words:       []string{"bead", "update"},
	use:         "yardr bead update <bead> --originator <user>",
	description: "Change a bead's fields (use advance to change stage)",
	parse:       parseOriginator,
	check:       checkOriginator,
	card:        (*wtx).originatorCard,
	stale:       (*wtx).originatorStale,
	run: func(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
		if err := w.setOriginator(ctx, argv[2], argv[4], Expect{}, actor, by); err != nil {
			return "", err
		}
		return "originator: " + argv[4], nil
	},
}, {
	words:       []string{"timer", "rm"},
	use:         "yardr timer rm <id>",
	description: "Remove a timer: one you set or that tells you, or as the operator any",
	parse:       parseTimerRm,
	check:       checkTimerRm,
	card:        (*wtx).timerRmCard,
	stale:       (*wtx).timerRmStale,
	run:         runTimerRm,
}}

// RunCommands are the commands of the list, each as a refusal and the help
// of yardr bead ask name it.
func RunCommands() []string {
	var uses []string
	for _, c := range runList {
		uses = append(uses, c.use)
	}
	return uses
}

// errNotRow is when the words name no row of the list: askedRun then
// tries what is the operator's alone, and approveAsk refuses what the
// yard did not write.
var errNotRow = errors.New("is no command the yard runs from a card")

// runNoneChar refuses a character no run command takes.
func runNoneChar(c rune) error {
	return fmt.Errorf("--run takes one yardr command of names, numbers and flags, split at spaces: %q is none of their characters, and nothing is quoted. A command that needs it is the operator's to type in a terminal", c)
}

// unknownRun refuses words that are no row of the list and nothing the
// yard names as alone or the asker's own to run.
func unknownRun() error {
	return fmt.Errorf("--run takes a command of the list (%s): anything else is run directly, or typed by the operator in a terminal", strings.Join(RunCommands(), ", "))
}

// parseRun reads the command a run ask names, as its writer sent it, and
// answers its row of the list with the command in its stored form. The
// line is split at white space and nowhere else, and is read by nothing
// after this: a character that no name, number or flag of the list has is
// refused, so there is no quoting, and no second reading of the line.
func parseRun(sent []string) (*runCommand, []string, error) {
	c, argv, err := parseRow(sent)
	if err == errNotRow {
		return nil, nil, unknownRun()
	}
	return c, argv, err
}

// parseRow is parseRun for a row of the list, and errNotRow for anything
// else: a command the asker may run themselves, one that is the
// operator's alone, or none the yard runs from a card. askedRun tells
// those apart; approveAsk only ever sees what askedRun stored.
func parseRow(sent []string) (*runCommand, []string, error) {
	line := strings.Join(sent, " ")
	none := func(c rune) error {
		return fmt.Errorf("--run takes one yardr command of names, numbers and flags, split at spaces: %q is none of their characters, and nothing is quoted. A command that needs it is the operator's to type in a terminal", c)
	}
	for _, c := range line {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.ContainsRune(" \t._/=-", c):
		case c == '@' || c == ':':
			// A trust is a name at a peer, and an address is a host and a
			// port: the row that reads them says which.
		case c == ',':
			// Held to the command that takes a list, below.
		default:
			return nil, nil, none(c)
		}
	}
	comma := strings.ContainsRune(line, ',')
	words := strings.Fields(line)
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
	}
	for i := range runList {
		c := &runList[i]
		if len(words) < len(c.words) || !slices.Equal(words[:len(c.words)], c.words) {
			continue
		}
		if comma && !c.commas {
			return nil, nil, none(',')
		}
		rest, err := c.parse(words[len(c.words):])
		if err != nil {
			return nil, nil, err
		}
		return c, append(slices.Clone(c.words), rest...), nil
	}
	return nil, nil, errNotRow
}

// peerAllowChange is a parsed peer allow: the kinds it carries each way
// and stops, who it shows and stops showing, and the limit it sets.
type peerAllowChange struct {
	name                              string
	send, receive, nosend, noreceive  []string
	expose, noexpose, trust, notreach []string
	notrust, reach                    []string
	limit                             int
	hasLimit                          bool
}

// peerAllowOf reads a peer allow in its stored form: the peer, then each
// flag with its value.
func peerAllowOf(argv []string) peerAllowChange {
	al := peerAllowChange{name: argv[2]}
	for i := 3; i+1 < len(argv); i += 2 {
		switch value := argv[i+1]; argv[i] {
		case "--send":
			al.send = append(al.send, value)
		case "--receive":
			al.receive = append(al.receive, value)
		case "--no-send":
			al.nosend = append(al.nosend, value)
		case "--no-receive":
			al.noreceive = append(al.noreceive, value)
		case "--expose":
			al.expose = append(al.expose, value)
		case "--no-expose":
			al.noexpose = append(al.noexpose, value)
		case "--trust":
			al.trust = append(al.trust, value)
		case "--no-trust":
			al.notrust = append(al.notrust, value)
		case "--reach":
			al.reach = append(al.reach, value)
		case "--no-reach":
			al.notreach = append(al.notreach, value)
		case "--mail-limit":
			al.limit, _ = strconv.Atoi(value)
			al.hasLimit = true
		}
	}
	return al
}

// peerAllowUse is how a refusal names the peer allow a run ask takes.
const peerAllowUse = "--run takes yardr peer allow <peer> --send <kinds> | --receive <kinds> | --mail-limit <n>"

// parsePeerAllow reads the words of peer allow <peer> with the flags a
// run ask takes: the kinds it carries each way and stops, and the limit.
// Who the peer sees and is trusted and reached as is read too, and
// refused as the operator's alone by the check, not here. Narrowing one
// of those is anyone's, and is run directly. The limit and the kinds are
// two asks: what each changes is carded apart.
func parsePeerAllow(rest []string) ([]string, error) {
	args, values, err := runWords(peerAllowUse, "peer allow", "the operator's", rest, map[string]bool{
		"--send": true, "--receive": true, "--no-send": true, "--no-receive": true,
		"--expose": true, "--no-expose": true, "--trust": true, "--no-trust": true,
		"--reach": true, "--no-reach": true, "--mail-limit": true,
	})
	if err != nil {
		return nil, err
	}
	if len(args) != 1 {
		if len(args) == 0 {
			return nil, fmt.Errorf("%s: it names no peer", peerAllowUse)
		}
		return nil, fmt.Errorf("%s: one peer, not %s", peerAllowUse, strings.Join(args, " and "))
	}
	for _, flag := range []string{"--no-expose", "--no-trust", "--no-reach"} {
		if len(values[flag]) > 0 {
			return nil, fmt.Errorf("%s: %s narrows one, and is run directly", peerAllowUse, flag)
		}
	}
	limit := ""
	if len(values["--mail-limit"]) > 1 {
		return nil, fmt.Errorf("%s: --mail-limit once", peerAllowUse)
	} else if len(values["--mail-limit"]) == 1 {
		limit = values["--mail-limit"][0]
	}
	for _, flag := range []string{"--send", "--receive", "--no-send", "--no-receive", "--expose", "--trust", "--reach"} {
		if limit != "" && len(values[flag]) > 0 {
			return nil, fmt.Errorf("%s, and peer allow with --mail-limit and %s is two asks: propose the limit and the rest apart", peerAllowUse, flag)
		}
	}
	changed := limit != ""
	for flag, vs := range values {
		if flag != "--mail-limit" && len(vs) > 0 {
			changed = true
		}
	}
	if !changed {
		return nil, fmt.Errorf("%s: it says nothing to change", peerAllowUse)
	}
	n := 0
	if limit != "" {
		var err error
		if n, err = strconv.Atoi(limit); err != nil {
			return nil, fmt.Errorf("%s: %q is no number", peerAllowUse, limit)
		}
		if n < 1 || n > MaxPeerMailLimit {
			return nil, fmt.Errorf("peer %s: a link takes 1 to %d mails an hour, not %d", args[0], MaxPeerMailLimit, n)
		}
	}
	split := func(vs []string) []string {
		var out []string
		for _, v := range vs {
			out = append(out, strings.Split(v, ",")...)
		}
		slices.Sort(out)
		return slices.Compact(out)
	}
	send, receive := split(values["--send"]), split(values["--receive"])
	nosend, noreceive := split(values["--no-send"]), split(values["--no-receive"])
	if err := checkKinds(send, receive, nosend, noreceive); err != nil {
		return nil, err
	}
	out := []string{args[0]}
	for _, fv := range []struct {
		flag string
		vs   []string
	}{
		{"--send", send}, {"--receive", receive}, {"--no-send", nosend}, {"--no-receive", noreceive},
		{"--expose", split(values["--expose"])}, {"--trust", split(values["--trust"])}, {"--reach", split(values["--reach"])},
	} {
		for _, v := range fv.vs {
			out = append(out, fv.flag, v)
		}
	}
	if limit != "" {
		out = append(out, "--mail-limit", strconv.Itoa(n))
	}
	return out, nil
}

// checkPeerAllow is the peer allow's own checks as the asker, shared with
// AllowPeer so the same command has the same operator-only refusal.
func checkPeerAllow(ctx context.Context, w *wtx, argv []string, actor string) error {
	al := peerAllowOf(argv)
	_, err := w.checkAllowPeer(ctx, al.name, peerAllowPermission{
		send: al.send, receive: al.receive,
		expose: len(al.expose) > 0, trust: len(al.trust) > 0, reach: len(al.reach) > 0,
		mailLimit: al.limit,
	}, actor)
	return err
}

// peerAs names the peer as the yard knows it, with what it calls itself
// and the start of its key, since two peers may have names that look
// alike.
func peerAs(p *Peer) string {
	as := "key " + keyStart(p.Key)
	if p.Calls != "" {
		as = "it calls itself " + p.Calls + ", " + as
	}
	return as
}

// kindsSay is the kinds a link carries one way as a card names them:
// "nothing" for none.
func kindsSay(kinds []string) string { return or(strings.Join(kinds, ", "), "nothing") }

// peerAllowCard is the card of peer allow: the limit it sets, and the
// kinds it carries each way and stops, with what the link carries in and
// out after the command beside what it carries now. The peer is named with
// what it calls itself and the start of its key, and the approval is held
// to the key whole and to what the link carries (Seen). Asking refuses a
// limit the link has already, and one below it: to take less mail is
// anyone's, and needs no approval. It refuses kinds that change nothing the
// same way.
func (w *wtx) peerAllowCard(argv []string, asking bool) (AskRun, error) {
	al := peerAllowOf(argv)
	p, err := loadPeer(w.Tx, al.name)
	if err != nil {
		return AskRun{}, err
	}
	var does []string
	back := []string{"peer", "allow", al.name}
	backBy := ""
	if al.hasLimit {
		has := p.MailsAnHour()
		switch {
		case !asking:
		case al.limit == has:
			return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour already, so the command would change nothing", al.name, has)
		case al.limit < has:
			return AskRun{}, fmt.Errorf("peer %s: the link takes %d mails and asks an hour, and to take less needs no approval: anyone runs yardr peer allow %s --mail-limit %d", al.name, has, al.name, al.limit)
		}
		does = append(does, fmt.Sprintf("the link with %s (%s) takes %d mails and asks an hour from it. It takes %d now.", al.name, peerAs(p), al.limit, has))
		back = append(back, "--mail-limit", strconv.Itoa(has))
	}
	afterSend := changeList(p.Send, al.send, al.nosend)
	afterReceive := changeList(p.Receive, al.receive, al.noreceive)
	addedSend := slices.DeleteFunc(slices.Clone(afterSend), func(k string) bool { return slices.Contains(p.Send, k) })
	addedReceive := slices.DeleteFunc(slices.Clone(afterReceive), func(k string) bool { return slices.Contains(p.Receive, k) })
	removedSend := slices.DeleteFunc(slices.Clone(p.Send), func(k string) bool { return slices.Contains(afterSend, k) })
	removedReceive := slices.DeleteFunc(slices.Clone(p.Receive), func(k string) bool { return slices.Contains(afterReceive, k) })
	kinds := len(al.send)+len(al.receive)+len(al.nosend)+len(al.noreceive) > 0
	if asking && kinds && len(addedSend)+len(addedReceive)+len(removedSend)+len(removedReceive) == 0 {
		return AskRun{}, fmt.Errorf("peer %s: the link carries %s to it and takes %s from it already, so the command would change nothing", al.name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"))
	}
	if kinds {
		// What the link carries each way after the command and before it,
		// whole, so the person reads the link as it will be beside the
		// link as it is, then which kinds the command adds and stops.
		does = append(does, fmt.Sprintf("the link with %s (%s) will carry %s to it and take %s from it. It carries %s to it and takes %s from it now.",
			al.name, peerAs(p),
			kindsSay(afterSend), kindsSay(afterReceive),
			kindsSay(p.Send), kindsSay(p.Receive)))
		var delta []string
		if len(addedSend) > 0 {
			delta = append(delta, "adds "+strings.Join(addedSend, ", ")+" to what it carries to it")
		}
		if len(addedReceive) > 0 {
			delta = append(delta, "adds "+strings.Join(addedReceive, ", ")+" to what it takes from it")
		}
		if len(removedSend) > 0 {
			delta = append(delta, "stops carrying "+strings.Join(removedSend, ", ")+" to it")
		}
		if len(removedReceive) > 0 {
			delta = append(delta, "stops taking "+strings.Join(removedReceive, ", ")+" from it")
		}
		// None left when the link changed since the card was written: the
		// approval is then refused as stale, in the card's first sentence.
		if len(delta) > 0 {
			does = append(does, "That "+andList(delta)+".")
		}
	}
	for _, k := range addedSend {
		back = append(back, "--no-send", k)
	}
	for _, k := range addedReceive {
		back = append(back, "--no-receive", k)
	}
	for _, k := range removedSend {
		back = append(back, "--send", k)
		backBy = "only the operator"
	}
	for _, k := range removedReceive {
		back = append(back, "--receive", k)
		backBy = "only the operator"
	}
	finger := map[string]string{"peer " + al.name: p.Key}
	if kinds {
		finger["peer "+al.name+" carries"] = seen(p.Send, p.Receive)
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: backBy,
		Seen:   finger,
	}, nil
}

// keyStart is the start of a key as a card shows it. It names the key to a
// person and proves nothing: what is compared is the key whole.
func keyStart(key string) string {
	if len(key) > 8 {
		return key[:8] + "…"
	}
	return key
}

// peerKeyStale says why the ask seq about the peer name is approved no
// more when the link is another one under the same name, and nil when it
// is the one the card was written from.
func (w *wtx) peerKeyStale(seq uint64, name string, was, now AskRun) error {
	const nothing = "Nothing ran. Decline it; it is asked again if it is still wanted."
	seen := "peer " + name
	if was.Seen[seen] == now.Seen[seen] {
		return nil
	}
	added := ""
	if p, err := loadPeer(w.Tx, name); err == nil {
		added = " (the link there now was added " + p.AddedAt.Local().Format("2 Jan 15:04") + ")"
	}
	return fmt.Errorf("ask %d was written when the link with %s was another: it was removed and made again since%s. %s", seq, name, added, nothing)
}

// mailLimitStale says why the ask seq for peer allow <peer> --mail-limit
// <n> is approved no more: the link is another one under the same name, or
// it takes another number of mails than the card says.
func (w *wtx) mailLimitStale(seq uint64, was, now AskRun) error {
	al := peerAllowOf(was.Argv)
	name, n := al.name, al.limit
	const nothing = "Nothing ran. Decline it; it is asked again if it is still wanted."
	if err := w.peerKeyStale(seq, name, was, now); err != nil {
		return err
	}
	// The limit the card was written at is in the command that takes it
	// back, which is the same command with that number.
	had := peerAllowOf(strings.Fields(was.Back)[1:]).limit
	has := peerAllowOf(strings.Fields(now.Back)[1:]).limit
	switch {
	case had == has:
		return fmt.Errorf("ask %d says %q, and the yard would say %q now: the card is from before that changed. %s", seq, strings.Join(was.Does, " "), strings.Join(now.Does, " "), nothing)
	case has == n:
		return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d already: nothing is left to change. %s", seq, name, had, has, nothing)
	}
	return fmt.Errorf("ask %d says the link with %s takes %d mails an hour now, and it takes %d: the card is from before that changed. %s", seq, name, had, has, nothing)
}

// peerAllowStale says why the ask seq for peer allow is approved no more:
// the limit's own reasons when it sets one, and the key and the sentences
// when it carries kinds.
func (w *wtx) peerAllowStale(seq uint64, was, now AskRun) error {
	al := peerAllowOf(was.Argv)
	if al.hasLimit {
		return w.mailLimitStale(seq, was, now)
	}
	if err := w.peerKeyStale(seq, al.name, was, now); err != nil {
		return err
	}
	return staleCard(seq, was, now)
}

// runPeerAllow makes a peer allow as actor through allowPeer, and answers
// what the command prints at a terminal.
func runPeerAllow(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	al := peerAllowOf(argv)
	ch := PeerAllow{Send: al.send, Receive: al.receive, NoSend: al.nosend, NoReceive: al.noreceive}
	if al.hasLimit {
		ch.MailLimit = al.limit
	}
	p, err := w.allowPeer(ctx, al.name, ch, actor, by)
	if err != nil {
		return "", err
	}
	if al.hasLimit && len(al.send)+len(al.receive)+len(al.nosend)+len(al.noreceive) == 0 {
		return "peer " + p.Name + ": " + p.MailTakes(), nil
	}
	return fmt.Sprintf("peer %s: send %s; receive %s; expose %s", p.Name, or(strings.Join(p.Send, ", "), "nothing"), or(strings.Join(p.Receive, ", "), "nothing"), or(strings.Join(p.Expose, ", "), "nobody")), nil
}

// askedRun checks the command a run ask for the user to proposes, written
// by actor, and answers it as the ask stores it: the command in the yard's
// own form, with the yard's sentences, the command that takes it back and
// what the yard saw of what it names, whatever the writer sent for them.
//
// It refuses a command that is not on the list (telling apart one the
// asker may run themselves, one that is the operator's alone, and none
// the yard runs from a card), one the yard would refuse or that would
// change nothing, anyone but the required approver as the one asked
// (for a command that is a user's own, that user or the operator:
// runCommand.whose; for one user alone, only that user: runCommand.sole),
// and a writer that is no crew member and no user of the yard: a bead's
// agent reads text from outside the yard and is the one most likely to be
// talked into proposing something, so the store knows it by its pane,
// whatever actor it names. Asking gives the asker nothing: the ask is words
// until its required approver approves it.
func (w *wtx) askedRun(ctx context.Context, run AskRun, to, actor string) (*AskRun, error) {
	c, argv, err := parseRow(run.Argv)
	if err != nil && err != errNotRow {
		return nil, err
	}
	notRow := err == errNotRow
	const whose = "a command for the operator to approve is proposed by a crew member or a user of the yard"
	sess, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return nil, err
	}
	if sess != nil && !sess.Manual() {
		return nil, fmt.Errorf("%s; this is the session %s of a bead's agent: say what is needed in a note on the bead (yardr bead note)", whose, sess.ID)
	}
	if err := startingRefused(w.Tx, sessionOf(ctx)); err != nil {
		return nil, err
	}
	if w.Bucket(bCrew).Get([]byte(actor)) == nil && w.Bucket(bUsers).Get([]byte(actor)) == nil {
		return nil, fmt.Errorf("%s; this is %s: say what is needed in a note on the bead (yardr bead note)", whose, or(actor, "nobody"))
	}
	if notRow {
		return nil, w.checkAlone(ctx, run.Argv, actor)
	}
	// The address on a user key claim comes only from the hook's dropped
	// delivery path (ClaimAddress), which writes its own ask. AddAsk is a
	// local API too, so do not let a caller supply an arbitrary address and
	// present it as though the hook had brought it.
	if slices.Equal(c.words, []string{"user", "key"}) && run.Value != "" {
		return nil, fmt.Errorf("user key run asks may carry an address only from a dropped delivery")
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return nil, err
	}
	// The command in the yard's own form, with what the writer carried
	// beside it: the carried bytes are the row's to keep or throw away,
	// as Does and Seen are.
	sent := AskRun{Argv: argv, Carried: run.Carried}
	check := c.check
	if c.checkRun != nil {
		check = func(ctx context.Context, w *wtx, _ []string, actor string) error {
			return c.checkRun(ctx, w, sent, actor)
		}
	}
	sole := ""
	if c.sole != nil {
		sole = c.sole(argv)
	}
	if check != nil {
		// The command's own check as the asker: nothing says run it
		// yourself better than the yard allowing it, and nothing says
		// it is the operator's better than the yard refusing it.
		if err := check(ctx, w, argv, actor); err != nil {
			if ask, ok := IsOperatorOnly(err); !ok {
				return nil, err
			} else if ask == Alone {
				return nil, aloneRefusal(err)
			}
			// The one who would give it must be allowed it: the ask
			// proposes what they would run. Their leg runs without the
			// asker's pane, which is the asker's, not theirs. For a
			// command one user alone gives, that leg is theirs, and
			// there is no operator's beside it.
			giver, asWho := operator, "the operator"
			if sole != "" {
				giver, asWho = sole, sole
			}
			if err := check(context.Background(), w, argv, giver); err != nil {
				return nil, fmt.Errorf("running it as %s would be refused: %w", asWho, err)
			}
		} else {
			return nil, fmt.Errorf("%s needs no approval: run it yourself", runCommandLine(argv))
		}
	}
	user := ""
	if c.whose != nil {
		user = c.whose(argv)
	}
	switch {
	case sole != "" && to == sole:
	case sole != "":
		return nil, fmt.Errorf("this command is %s's to approve alone, and nobody else's: the yard runs it as them, so %s cannot be asked to", sole, to)
	case user != "" && to == user:
	case user != "" && to != operator:
		whose := user + "'s to approve"
		if operator != "" && operator != user {
			whose += ", or the operator's (" + operator + ") for them"
		}
		return nil, fmt.Errorf("this command is %s: the yard runs it as the one who approves, so %s cannot be asked to", whose, to)
	case operator == "":
		return nil, fmt.Errorf("a command is the operator's to approve, and this yard has none: yardr settings set operator <name>")
	case to != operator:
		return nil, fmt.Errorf("a command is the operator's to approve (%s): the yard runs it as them, so %s cannot be asked to", operator, to)
	}
	var card AskRun
	if c.cardRun != nil {
		card, err = c.cardRun(w, sent, true)
	} else {
		card, err = c.card(w, argv, true)
	}
	if err != nil {
		return nil, err
	}
	// A row whose card knows its own wording (pack apply, by the pack's
	// kind) keeps it; the rest take the command's help line.
	if card.Description == "" {
		card.Description = c.description
	}
	return &card, nil
}

// ApproveAsk approves the run ask ask on the bead id as actor, and the yard
// runs the command the ask holds: in one write it checks the ask and who
// approves, holds the approval to the yard the card was written from, makes
// the change through the command's own store function as the operator, and
// records an answer that holds the command and what it printed.
//
// It refuses an ask that is not on the bead, proposes no command, or was
// declined, taken back or approved already: a command runs once for one
// ask. It refuses anyone but the operator, and every call from the pane of
// an agent whatever actor it names (mayApprove), also for a command whose
// own check goes by the name alone, so that an agent cannot approve its own
// ask by naming the operator. A command that is a user's own, as that
// user's subscriptions and what their phone is told of are, is approved by
// the one it was asked of, that
// user or the operator, and by nobody else (mayApproveOwn); a command one
// user alone gives, a grant as them, is approved by that user alone, with
// no operator leg (mayApproveSole). It refuses when
// the yard would not write the
// same card now: what the command names was replaced under its name (Seen),
// or what it changes is not what the card says it is (Does). And what the
// command's own function refuses is refused here, in its words. A refused
// approval records nothing and changes nothing, and leaves the ask open.
//
// It is served on the yard's socket, and on the listener to a device of the
// operator and to no other caller (rpc.OperatorCalls): the answer and the
// events of an approval that came so say how it came, as a device's other
// writes do (writeVia). No answer that comes over a link approves
// (answerable), and an approval that a door took through the reach of a
// peer is refused where it changes that peer's own link, or its own
// intake, or says who its own crew is (runCommand.link, WithReach): the
// path is the context's, set by the door.
func (s *Store) ApproveAsk(ctx context.Context, id string, ask uint64, actor string) (*AskApproved, error) {
	// A bead's handle never holds an "@" (validID).
	if strings.Contains(id, "@") {
		return nil, fmt.Errorf("ask %d on %s is a command of another yard's operator: it is approved in the yard that asked, never over a link", ask, id)
	}
	if name, ok := s.hookRegisterTarget(id, ask); ok {
		unlockAsk := s.askLocks.lock(askLockKey(id, ask))
		defer unlockAsk()
		unlockHook := s.askLocks.lock(hookLockKey(name))
		defer unlockHook()
		return s.approveHookRegister(ctx, id, ask, actor)
	}
	// What the ask's card needs of git is read before the write.
	repos, err := s.approvalRepos(ctx, id, ask)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", ask, err, nothingRan)
	}
	var out *AskApproved
	err = s.writeVia(ctx, func(w *wtx) (err error) {
		w.repos = repos
		out, err = w.approveAsk(ctx, id, ask, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// approvalRepos reads, before the write that approves the run ask seq on
// the bead id, what its row's card needs of git (runCommand.ready). An ask
// that cannot be read here (no bead, no run) is left to the write, which
// refuses it in its own words.
func (s *Store) approvalRepos(ctx context.Context, id string, seq uint64) (map[string]*RepoFlows, error) {
	notes, err := s.Notes(ctx, id)
	if err != nil {
		return nil, nil
	}
	for _, n := range notes {
		if n.Seq != seq || n.Kind != NoteAsk {
			continue
		}
		ask, err := ParseAsk(n)
		if err != nil || ask.Run == nil {
			return nil, nil
		}
		if c, argv, err := parseRow(ask.Run.Argv); err == nil && c.ready != nil {
			return c.ready(ctx, s, argv)
		}
	}
	return nil, nil
}

// approveAsk is ApproveAsk inside the transaction. A batch may run it twice
// for one call (write): it reads the yard's records anew each time and ends
// the same.
func (w *wtx) approveAsk(ctx context.Context, id string, seq uint64, actor string) (*AskApproved, error) {
	who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = who
	row, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return nil, err
	}
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", row.ID, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Run == nil:
		return nil, fmt.Errorf("ask %d proposes no command to approve: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, row.ID, seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	case r.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a command runs once for one ask", seq, r.Current.Note.Author)
	}
	was := *r.Ask.Run
	c, argv, err := parseRun(was.Argv)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w", seq, err)
	}
	// Before anything is written: an agent's approval changes nothing, not
	// even what the transaction would take back.
	if c.sole != nil && c.sole(argv) != "" {
		err = w.mayApproveSole(ctx, actor, r.Ask.To, c.sole(argv))
	} else if c.whose != nil {
		err = w.mayApproveOwn(ctx, actor, r.Ask.To, c.whose(argv))
	} else {
		err = w.mayApprove(ctx, actor)
	}
	if err != nil {
		return nil, err
	}
	if !slices.Equal(argv, was.Argv) {
		return nil, fmt.Errorf("ask %d holds a command the yard did not write (%s): nothing ran", seq, was.Command())
	}
	// A peer does not change its own link on its own word. The path the
	// approval came by is on the context, set by the door (WithReach): one
	// that came through the reach of the peer the command is about is
	// refused, whoever it takes the approver to be, before the card is read.
	// This yard's own approval of the same card, in a terminal here or from
	// a device paired with this yard directly, carries no such mark.
	if c.link != nil {
		if peer, rule := c.link(argv); peer != "" && reachOf(ctx) == peer {
			return nil, fmt.Errorf("ask %d: this approval arrived through the reach of %s (a name of %s taken as %s), and a peer must not %s. Approve it from this yard, in a terminal here or in footplate paired with this yard directly. Nothing ran, and the ask stays open", seq, peer, peer, actor, rule)
		}
	}
	var card AskRun
	if c.cardRun != nil {
		card, err = c.cardRun(w, was, false)
	} else {
		card, err = c.card(w, argv, false)
	}
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if !sameMap(card.Seen, was.Seen) || !slices.Equal(card.Does, was.Does) || card.Back != was.Back || card.BackBy != was.BackBy {
		if err := c.stale(w, seq, was, card); err != nil {
			return nil, err
		}
		// The row held the approval to the card as it stands now: the
		// answer carries that card, drawn at approve time.
		was.Does = card.Does
	}
	var result string
	if c.runRun != nil {
		result, err = c.runRun(ctx, w, was, actor, &approvedOn{Bead: row.ID, Ask: seq})
	} else {
		result, err = c.run(ctx, w, argv, actor, &approvedOn{Bead: row.ID, Ask: seq})
	}
	if err != nil {
		return nil, err
	}
	// An approval the yard carries out after this write leaves its
	// carrying record in it, for the server to take up (carry.go).
	if needsCarry(was) {
		if err := w.recordCarry(row.ID, seq, was, actor); err != nil {
			return nil, err
		}
	}
	text, err := json.Marshal(Answer{Ask: seq, Ran: &AskRan{Argv: argv, Result: result}})
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return nil, err
	}
	return &AskApproved{Bead: row.ID, Ask: seq, Asker: r.Note.Author, Argv: argv, Does: was.Does, Result: result}, nil
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// mayApprove refuses an approval by anyone but the operator. It is the
// firmest check the store has, as maySource is: who asks is told by the
// store's own record of the calling process before the caller's word, so a
// call from the pane of a bead's session or of a crew member is refused
// whatever actor it names. A yard without an operator has nobody whose
// command it would be.
func (w *wtx) mayApprove(ctx context.Context, actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if operator == "" {
		return fmt.Errorf("approving a command is the operator's, and this yard has none: yardr settings set operator <name>")
	}
	why := "approving a command is the operator's (" + operator + "): the yard runs it as them"
	pane, err := agentPane(ctx, w.Tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != operator {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	return nil
}

// mayApproveOwn refuses an approval of a command that is the user whose own
// to give, as it is the operator's for them, by anyone but the one the ask
// was put to, who is one of the two: the card says the yard runs the
// command as the one asked, and so it does. An agent's pane is refused
// first, whatever actor the call names, as mayApprove refuses it. Whether
// the one asked may still make the change is the command's own check,
// which runs after this one.
func (w *wtx) mayApproveOwn(ctx context.Context, actor, to, whose string) error {
	why := "approving this command is " + to + "'s, whom it was asked of: the yard runs it as them"
	pane, err := agentPane(ctx, w.Tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != to {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if to != whose && to != operator {
		return fmt.Errorf("this command is %s's own to approve, or the operator's for them, and %s is neither now", whose, to)
	}
	return nil
}

// mayApproveSole is mayApproveOwn for a command one user alone gives:
// the one asked must be that user, and the operator has no leg beside
// them, so an approval by the operator (or by anyone else) is refused
// whoever the ask was put to. An agent's pane is refused first, as in
// mayApproveOwn.
func (w *wtx) mayApproveSole(ctx context.Context, actor, to, sole string) error {
	why := "approving this command is " + sole + "'s alone, and nobody else's: the yard runs it as them"
	pane, err := agentPane(ctx, w.Tx)
	if err != nil {
		return err
	}
	if pane != "" {
		return fmt.Errorf("%s; this is %s", why, pane)
	}
	if w.Bucket(bCrew).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s; %s is a crew member", why, actor)
	}
	if actor != to {
		return fmt.Errorf("%s; this is %s", why, or(actor, "nobody"))
	}
	if to != sole {
		return fmt.Errorf("this command is %s's own to approve alone, and %s is not them", sole, to)
	}
	return nil
}
