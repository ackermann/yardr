package rpc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// docs/api.md is what a program that is not yardr may rely on. This file is
// the same contract as a test: apiCalls and apiEvents name every call and
// event of the doc with the fields it promises, the test makes each call
// through a socket with the JSON the doc shows and checks that every
// promised field is there with its type, and the doc's sections on the
// calls and the events are made from what came back (TestAPIDoc). So a
// field renamed in the store fails here, and the doc cannot show a reply
// the server does not give.

// field is one promised field: where it is in the answer ("mail[].seq",
// "[].name", "" for the answer itself), its type, and what it says. A type
// ending in "?" is of a field that is absent when it has nothing to say.
type field struct{ path, typ, says string }

// apiCall is one call of the doc. Its args are named as Calls names them;
// each of its requests is made, in order, and shown with its reply.
type apiCall struct {
	method   string
	says     string
	args     []field
	requests []string
	fields   []field
	after    string // said below the fields
}

const (
	// A letter is the same wherever it is read.
	letterSeq  = "the seq of the mail's event: what an acknowledgement goes through, and what `after` is compared with"
	letterRead = "whether its receiver acknowledged it"
)

func letter(prefix string) []field {
	var out []field
	for _, f := range sentLetter(prefix) {
		if f.path != prefix+"via" {
			out = append(out, f)
		}
	}
	return out
}

// sentLetter is a letter where the user's own are read too (Conversation):
// one of those may have come through a device.
func sentLetter(prefix string) []field {
	return []field{
		{prefix + "seq", "number", letterSeq},
		{prefix + "at", "time", "when it was sent"},
		{prefix + "from", "string", "the sender: a crew member, a user, or `<name>@<peer>`"},
		{prefix + "to", "string", "the receiver, as `from`"},
		{prefix + "text", "string", "the mail"},
		{prefix + "read", "bool", letterRead},
		{prefix + "reactions", "array?", "the parties' emoji on the mail, by who put them, as [`React`](#react) put them; absent on mail without any"},
		{prefix + "reactions[].from", "string", "who put it: the sender or the receiver"},
		{prefix + "reactions[].emoji", "string", "which of 👍 👎 ❤️ 🎉 👀 ✅ it is"},
		{prefix + "as", "string?", "on mail from `<name>@<peer>` only: the user of this yard the link trusted that name as when the mail came, to be shown behind the sender, `josephine@north (as jo)`; absent on any other mail. Whom the links trust now is `trust` of [`Peers`](#peers)"},
		{prefix + "via", "string?", "the [device](#over-the-network) the mail was written through, to be shown behind the sender, `jo (via phone)`; absent on mail written on the yard's own machine"},
		{prefix + "files", "array?", "the files the mail carries, each an image, a page or a file, in the order it was sent with them; absent on mail without any"},
		{prefix + "files[].hash", "string", "the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes"},
		{prefix + "files[].type", "string", "what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page"},
		{prefix + "files[].name", "string?", "the file's base name as the sender gave it; absent on a paste and on a letter from before names"},
		{prefix + "files[].bytes", "number", "how many bytes it is"},
	}
}

// apiPNG is the image of the doc, one transparent pixel, as PutBlob takes
// it: in base64. apiPNGHash is its name.
const apiPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

var apiPNGHash = func() string {
	data, err := base64.StdEncoding.DecodeString(apiPNG)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}()

// A queue row is the same open and decided.
func queueRow(prefix string) []field {
	return []field{
		{prefix + "bead", "string", "the bead the question stands on: a handle, or `bead@peer` for a question another yard asked"},
		{prefix + "bead_title", "string", "the bead's title"},
		{prefix + "topic", "string", "the bead's topic: its first `topic:` label, `\"\"` when it has none"},
		{prefix + "ask", "number", "the ask's seq on its bead: what an answer names"},
		{prefix + "question", "string", "the question in one line, as a person would ask it"},
		{prefix + "asker", "string", "who asked: a name, or `name@peer` for a question another yard asked"},
		{prefix + "since", "time", "when the ask was written: how long it waits (as sent, for a question another yard asked)"},
	}
}

// secretAsked is what a secret ask asks for, on a row of the queue.
func secretAsked(prefix string) []field {
	return []field{
		{prefix + "secret", "object?", "what a secret ask asks for, as `Bead` says it; absent on any other ask"},
		{prefix + "secret.source", "string", "the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask"},
		{prefix + "secret.env", "array of string", "the names to ask the person a value for; with a harness-key ask, variables in that harness kind's `login_env`"},
		{prefix + "secret.user", "string?", "the user whose own address in the source is asked for; absent on an ask for the source's credential"},
		{prefix + "secret.crew", "string?", "the crew member whose own credential in the source or harness API key is asked for; absent on any other secret ask"},
		{prefix + "secret.as", "string?", "the user whose own credential in the source is asked for; absent on any other secret ask"},
		{prefix + "secret.login", "string?", "the waiting login the value goes to; absent on any other secret ask"},
		{prefix + "secret.harness", "string?", "the harness kind whose API key is asked for; absent on any other secret ask"},
		{prefix + "secret.line", "string", "what the card says is asked for, in the yard's words"},
		{prefix + "concealed", "bool?", "whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret; absent (concealed) on any other ask"},
	}
}

// runAsked is the command a run ask proposes, on a row of the queue.
func runAsked(prefix string) []field {
	return []field{
		{prefix + "run", "object?", "the command a run ask proposes, as `Bead` says it; absent on any other ask"},
		{prefix + "run.argv", "array of string", "the command in words, without the leading `yardr`"},
		{prefix + "run.command", "string", "the command whole, as it is typed in a terminal. Never shorten it"},
		{prefix + "run.does", "array of string", "what the command changes, in the yard's words"},
		{prefix + "run.back", "string", "the command that takes the change back"},
		{prefix + "run.back_by", "string?", "who may run it, as `Bead` says it; absent where that is anyone"},
	}
}

// setupAsked is the setup a setup ask proposes, on a row of the queue.
func setupAsked(prefix string) []field {
	return []field{
		{prefix + "setup", "object?", "the setup a setup ask proposes, as `Bead` says it; absent on any other ask"},
		{prefix + "setup.pack", "string", "the embedded pack to set up, of kind source"},
		{prefix + "setup.label", "string?", "the build the pack is embedded in, as the card shows it"},
		{prefix + "setup.account", "array of string", "the names to type unseen beside the preset pick"},
		{prefix + "setup.account_file", "string", "the file the value goes to, on the yard's machine"},
		{prefix + "setup.given", "bool?", "on an update, where the yard already holds the source the pack brings: `true` when its account file already holds every name of `setup.account`, so that a bare preset pick keeps it; absent on a first setup, where nothing stands yet, and where the file lacks a name (`setup.missing`)"},
		{prefix + "setup.missing", "array of string?", "on an update, the names of `setup.account` the account file that stands does not set, where the pack newly needs them (every name where the file is not there): the answer types exactly these, or takes a candidate; absent on a first setup and where the file holds every name"},
		{prefix + "setup.candidates", "array?", "yard sources whose account the card offers to reuse"},
		{prefix + "setup.candidates[].source", "string", "the source whose account the card offers"},
		{prefix + "setup.candidates[].file", "string", "the file holding the shared account inputs"},
		{prefix + "setup.candidates[].values", "object", "the shared account input names and values the file holds"},
		{prefix + "setup.does", "array of string", "what approving changes and what is there now, in the yard's words"},
		{prefix + "setup.back", "string", "the commands that take the setup back"},
		{prefix + "setup.back_by", "string?", "who may run them, as `Bead` says it; absent where that is anyone"},
		{prefix + "setup.presets", "array", "the pack's presets with their grants, in the order the ask's options count them"},
		{prefix + "setup.presets[].name", "string", "the preset's name, as the option's label shows it"},
		{prefix + "setup.presets[].grants", "array", "what of each source the preset grants"},
		{prefix + "setup.presets[].grants[].source", "string", "the source the pack declares"},
		{prefix + "setup.presets[].grants[].calls", "array of string", "`read` for every read call, calls by name"},
		{prefix + "setup.to", "array", "who the grants are for"},
		{prefix + "setup.to[].group", "string?", "the group, and so each session it starts"},
		{prefix + "setup.to[].crew", "string?", "the crew member"},
	}
}

// A flow bead is the same on a stage, off the line and travelling with no
// line: the bead with its Arrivals row.
func flowBead(prefix string) []field {
	return []field{
		{prefix + "bead", "string", "the bead's handle"},
		{prefix + "title", "string", "the bead's title"},
		{prefix + "expected", "string", "the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead"},
		{prefix + "remark", "string", "held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `\"\"` when there is none"},
		{prefix + "tone", "string", "`red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark"},
		{prefix + "held_by", "string?", "who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held`"},
		{prefix + "working", "bool", "an agent's or a command's session claimed it; a manual group's claim is a person's turn and no work"},
		{prefix + "worker", "string?", "who works it, as `yardr session list` shows the claiming session: the runner with the agent's name or pid; absent where no session claimed it, and on a manual group's claim"},
		{prefix + "entered_stage_at", "time?", "when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock"},
	}
}

// A crew member is the same in the list and alone.
func crewMember(prefix string) []field {
	return []field{
		{prefix + "name", "string", "the name mail to the crew member is addressed to"},
		{prefix + "enabled", "bool", "whether the yard keeps a session of it running"},
		{prefix + "state", "string", "`stopped`, `running` or `failed`"},
		{prefix + "status", "string?", "what a running session does, as the daemon last saw it: `working`, `idle`, `blocked` (it waits at a prompt only a person in its pane can answer) or `unknown`; absent when none runs or the daemon has not looked yet"},
		{prefix + "started_at", "time?", "when the running session started"},
		{prefix + "model", "string?", "the model its config names; absent when it names none, and the harness's own is then used"},
		{prefix + "kind", "string?", "the harness kind its config names (`claude`, `codex`, ...); absent when it names none, and the yard's `default_kind` is then used"},
		{prefix + "login_screen", "bool?", "`true` while the running session sits at its harness's login screen, where it reads no mail until the harness is logged in; `status` may read `idle` or `blocked` there, and a client says \"not logged in\" instead, as `yardr chat` does. Absent while it does not, and while no session runs"},
		{prefix + "login_led", "bool?", "`true` while the yard leads a login for the session: it asked the operator how to log in, and the operator answers at their asks ([`UserAsks`](#userasks)) until the login is made and the session starts again. Absent otherwise. Only the session the yard asked for says it: another session of the same harness at its screen is logged in by the same login, and says only `login_screen`"},
	}
}

var apiCalls = []apiCall{
	{
		method:   "Head",
		says:     "The seq of the newest event, `0` in a yard that has none. Read it before the rest, then follow the stream from after it: what happens while you read comes again from the stream.",
		requests: []string{`{}`},
		fields:   []field{{"", "number", "the seq"}},
	},
	{
		method:   "Settings",
		says:     "The yard's operator, when set, and its caps on a file; to the operator's own devices, also the operator's words to their agents. `yardr chat` reads the operator only to name them when the current name is not a user of the yard; a client can omit this call when it already knows who it is for and sends no file.",
		requests: []string{`{}`},
		fields: []field{
			{"operator", "string?", "the operator's user name; absent when the yard has not set one"},
			{"operator_style", "string?", "how the operator asked agents to work with them: `guide`, `show`, `work` or `report`; absent when the operator has not set one, and to a device of any user but the operator"},
			{"operator_note", "string?", "the one line the operator asked agents to know about them; absent as `operator_style` is"},
			{"blob_max_bytes", "string?", "the most bytes `PutBlob` takes, a whole number in a string; absent when the yard has not set one, and the cap is then 8388608 (8 MiB)"},
			{"files_max", "string", "how many files one mail or note may carry, a whole number in a string; the yard's own count, so a client needs no copy of the number"},
		},
		after: "The operator's words are what every session of an agent reads when it starts (`yardr prime`), so the socket tells them to whoever calls, and every other setting with them. Over the network a device is told these keys alone: the words to a device of the operator and to no other, and to none in a yard with no operator. A `setting_changed` in a device's stream has its `value` only for a key the device is told; for any other it has the `key` and no `value`.",
	},
	{
		method:   "Users",
		says:     "The users of the yard, by name.",
		requests: []string{`{}`},
		fields: []field{
			{"[].name", "string", "the name mail to the user is addressed to"},
			{"[].cursor", "number", "the user's mail is read through this seq"},
			{"[].notification_cursor", "number", "the same for the user's notifications"},
		},
	},
	{
		method:   "GetUser",
		says:     "One user. It is how a client learns whether the name it runs as is a user of the yard (see [Who you are](#who-you-are)): one that is not is answered with the error `not-found`.",
		args:     []field{{"name", "string", "the user"}},
		requests: []string{`{"name":"jo"}`},
		fields: []field{
			{"name", "string", "the name"},
			{"cursor", "number", "the user's mail is read through this seq"},
			{"notification_cursor", "number", "the same for the user's notifications"},
		},
	},
	{
		method:   "Crew",
		says:     "The crew, running or not, by name.",
		requests: []string{`{}`},
		fields:   crewMember("[]."),
		after: "Mail to a crew member that is not running is kept, and read when it next starts. A session at its harness's login screen reads none either: " +
			"`login_screen` says it, and `login_led` that the yard leads the login. Both change with no change to the crew member's definition: read `Crew` again on a `crew_login` event, as on any other whose kind begins `crew_`.",
	},
	{
		method:   "GetCrew",
		says:     "One crew member, as `Crew` lists it; the error `not-found` for a name the crew does not have.",
		args:     []field{{"name", "string", "the crew member"}},
		requests: []string{`{"name":"yardmaster"}`, `{"name":"signalman"}`},
		fields:   crewMember(""),
	},
	{
		method:   "Peers",
		says:     "The linked yards, by name, each with the roster it last said: whom of that yard this one may see and write to. A client lists each entry as `<name>@<peer>`, which is what `Conversation` and the events call them.",
		requests: []string{`{}`},
		fields: []field{
			{"[].name", "string", "this yard's name for the link: the `<peer>` of an address"},
			{"[].send", "array of string", "the kinds of message the link carries out; mail can be written to the peer's roster only when `mail` is among them"},
			{"[].receive", "array of string", "the kinds it takes in"},
			{"[].said", "object?", "what the peer last said of itself; absent for a yard that has not said, which exposes nobody"},
			{"[].said.at", "time", "when this yard heard it"},
			{"[].said.roster", "array?", "whom the peer exposes; absent when nobody"},
			{"[].said.roster[].name", "string", "the `<name>` of the address"},
			{"[].said.roster[].kind", "string", "`crew` or `user`"},
			{"[].said.roster[].role", "string?", "one line of what they do, in the peer's words: text from another yard, to be shown as text and nothing else"},
			{"[].said.roster[].mail", "bool", "whether the peer's side of the link takes mail for them"},
			{"[].trust", "object?", "the names of the peer's roster the operator trusts as a user of this yard (`yardr peer allow north --trust josephine@north=jo`, [peers.md](peers.md#trusting-a-name-as-a-person-of-this-yard)): each key a `<name>` of the roster, its value, a string, the user. Mail from `<name>@<peer>` is that user speaking, and [`Conversation`](#conversation) with the user has it ([chat.md](chat.md#a-name-trusted-as-a-user-of-this-yard)). Absent when the link trusts nobody"},
		},
		after: "A roster changes with no event of its own, and so does `trust`: read `Peers` again on any event whose kind begins `peer_`. That is `peer_allowed` when the operator gives or takes a grant and `peer_trust_dropped` when the peer's roster has the name no more. A grant also goes with its user, on `user_removed`.\n\n" +
			"The reply carries the link whole, as `curl` shows it: `reach` ([a user's asks are also sent to the name](peers.md#reach-a-users-asks-are-also-sent-to-the-name)), `key`, the counts and the rest are in it too, but only the table above is promised. The rest is yardr's own, and a client ignores what it does not know.",
	},
	{
		method: "UserInbox",
		says:   "The mail to a user: the unread, oldest first, then the read, newest first.",
		args: []field{
			{"name", "string", "the user"},
			{"limit", "number", "at most this many letters, the unread first; `0` is all"},
		},
		requests: []string{`{"name":"jo","limit":10}`},
		fields: append(letter("mail[]."),
			field{"unread", "number", "all the unread, shown or not"},
			field{"last", "number", "the seq of the newest unread, `0` when none is"},
			field{"from", "array of string", "who the unread are from, in the order they first wrote"},
		),
	},
	{
		method:   "PutBlob",
		says:     "Gives the yard a file, so that mail or a note can carry it. `Mail`, `MailPeer` and `AddNote` name files by the hash this answers, each with a name; `yardr bead note --file` attaches a blob to a note. The bytes go in the JSON of the call, in base64. What kind it is is read from the bytes themselves: a PNG or a JPEG is an image, HTML a page, anything else a file. One over the yard's cap is refused (`blob_max_bytes` of `Settings`). The yard keeps a file once, under the SHA-256 of its bytes: putting the same bytes again answers the same and stores nothing new. A file no mail or note names is removed by `yardr store prune`, though not within an hour of being put, so put it when the mail or note is about to be written, and put it again when that was longer ago.",
		args:     []field{{"data", "string", "the file's bytes, in base64 (the standard alphabet, padded)"}},
		requests: []string{`{"data":"` + apiPNG + `"}`},
		fields: []field{
			{"hash", "string", "the file's name: the SHA-256 of its bytes, in hex"},
			{"type", "string", "what the bytes say"},
			{"bytes", "number", "how many bytes it is"},
		},
	},
	{
		method: "Mail",
		says:   "Sends mail to a crew member or a user of this yard. It answers nothing; the mail is then a `mail` event in the stream. A text over one mail's size is sent as numbered mails, each led by a line `(1/3)` that says which of how many it is, as [`yardr chat`](chat.md#a-long-text) sends a long text; past 16 mails it is refused.",
		args: []field{
			{"to", "string", "the receiver's name"},
			{"text", "string", "the mail; a text over 4096 bytes is sent as numbered parts, up to 16 (about 64 KB). It may be empty when the mail carries a file"},
			{"actor", "string", "who sends it: the user's name"},
			{"files", "array of object", "the files the mail carries, at most 64: each `{hash, name}`, the hash as `PutBlob` answered it with the file's base name. Left out, the mail carries none. One the yard does not hold, or a name no letter may carry, refuses the mail. On a text sent as several mails, the files go with the first alone"},
		},
		requests: []string{`{"to":"yardmaster","text":"Thanks. Land it.","actor":"jo"}`,
			`{"to":"yardmaster","text":"Paint the fence like this.","actor":"jo","files":[{"hash":"` + apiPNGHash + `","name":"fence.png"}]}`},
	},
	{
		method: "MailPeer",
		says:   "Queues mail to one of a linked yard, `<to>@<peer>`. It is refused when the link does not carry mail out. The answer is the message as it waits in the outbox for the peer.",
		args: []field{
			{"to", "string", "the receiver's name in the peer's yard"},
			{"peer", "string", "this yard's name for the link"},
			{"text", "string", "the mail"},
			{"actor", "string", "who sends it: the user's name"},
			{"files", "array of object", "the files the mail carries, each `{hash, name}` as for `Mail`; `text` may then be empty. Each crosses to the peer as a message of kind `file` before the mail. Refused, with nothing queued, when the link does not carry `file` out, when the peer's yardr has not said it understands `file`, and when a file is over `peer_file_max_bytes`"},
		},
		requests: []string{`{"to":"clerk","peer":"north","text":"Is the ledger closed?","actor":"jo"}`},
		fields: []field{
			{"peer", "string", "the link it waits for"},
			{"kind", "string", "`mail`"},
			{"at", "time", "when it was queued"},
		},
		after: "Where it stands afterwards is `delivery` in the conversation.",
	},
	{
		method: "React",
		says:   "Puts the party's reaction on the mail `seq`: one of 👍 👎 ❤️ 🎉 👀 ✅, or `\"\"` to take it away. Only the letter's sender or receiver may react, and only mail its sender wrote takes one. One party holds one reaction: a new one replaces the old one. It answers nothing; the reaction is then a `reaction` event in the stream, and `reactions` on the letter in [`UserInbox`](#userinbox) and [`Conversation`](#conversation).",
		args: []field{
			{"seq", "number", "the seq of the mail's event"},
			{"emoji", "string", "one of 👍 👎 ❤️ 🎉 👀 ✅; `\"\"` takes the party's reaction away"},
			{"actor", "string", "who reacts: the user's name"},
		},
		requests: []string{`{"seq":%d,"emoji":"🎉","actor":"jo"}`},
	},
	{
		method: "Conversation",
		says:   "The mail between two parties, both directions, oldest first: what `yardr mail --with <name>` prints. A user of this yard has one history, whatever name they wrote under: when a party is a user, the conversation has also the mail between the other party and each name of a linked yard that the link [trusts as that user](peers.md#trusting-a-name-as-a-person-of-this-yard), in the same order and the same pages, so a client joins nothing itself. Asking again with the last seq seen costs what is new. A long conversation is read from its newest end, a page at a time: with `limit` alone first, then with `limit` and, as `before`, the `seq` of the oldest letter of the page before, for as long as the reply says `more`.",
		args: []field{
			{"a", "string", "one party: the user"},
			{"b", "string", "the other: a crew member, a user, or `<name>@<peer>` for one of a linked yard. As `<name>@<peer>` it is the mail with that address alone, trusted or not"},
			{"after", "number", "leave out what is not newer than this seq; `0` for all"},
			{"limit", "number", "at most this many: the newest of what `after` and `before` leave, oldest first as ever; `0` for all"},
			{"before", "number", "leave out what is not older than this seq; `0` for none left out"},
		},
		requests: []string{`{"a":"jo","b":"yardmaster","after":0}`, `{"a":"jo","b":"yardmaster","limit":2}`, `{"a":"jo","b":"clerk@north","after":0}`, `{"a":"jo","b":"josephine@north","after":0}`},
		fields: append(sentLetter("[]."),
			field{"[].delivery", "string?", "on mail to a linked yard only, whose reading of it this yard never learns of: `queued` while it waits here for the peer, `sent` once the peer has it, `failed` when it was given up"},
		),
		after: "`more`, beside `result` in the reply and not in it, is `true` when `limit` left out mail older than the oldest letter answered (and newer than `after`, when that is given). It is absent otherwise: on the oldest page, and on every call without `limit`.\n\n" +
			"A letter of a trusted name says how it went, and is otherwise a letter like any other. What the user wrote under the name has the address as its `from`, and `as` when the link trusted the name as it came: the letter from `josephine@north` above, which jo wrote to the yardmaster from there. Its `read` is its receiver's, as ever. What was written to the name has the address as its `to`, and a `delivery` in place of a reading. Neither is in the user's inbox in this yard: [`UserInbox`](#userinbox) counts and lists none of it, no `mail` event of it is to the user, and [`AckUserInbox`](#ackuserinbox) through its seq marks none of it. So a client counts unread from the inbox and the events, as before, and never from `read` of a conversation.\n\n" +
			"The letters go by what the link said of the name when the mail was logged: a grant taken back later keeps them in the user's conversation, and what the name writes afterwards, or is written, is the name's alone (`b` as `<name>@<peer>`). Mail from before the grant is in it too, without `as`. Mail of a yard that is linked no more is in neither.",
	},
	{
		method: "AckUserInbox",
		says:   "Marks the user's mail read through a seq. It answers nothing; when mail was in between it is a `mail_read` event in the stream.",
		args: []field{
			{"name", "string", "the user"},
			{"through", "number", "the seq of the newest letter shown"},
		},
		requests: []string{`{"name":"jo","through":%d}`},
	},
	{
		method: "UserNotifications",
		says:   "What the yard reports to a user, apart from every conversation: the events the user subscribed to, oldest first.",
		args: []field{
			{"name", "string", "the user"},
			{"limit", "number", "at most this many; `0` is all the inbox keeps"},
		},
		requests: []string{`{"name":"jo","limit":0}`},
		fields: []field{
			{"notifications[].seq", "number", "the seq of the event it tells of"},
			{"notifications[].at", "time", "when that happened"},
			{"notifications[].kind", "string", "the event's kind"},
			{"notifications[].bead", "string?", "the bead it is about, when it is about one"},
			{"notifications[].text", "string", "the whole of it in one line"},
			{"notifications[].read", "bool", "whether the user acknowledged it"},
			{"unread", "number", "all the unread, shown or not"},
			{"last", "number", "the seq of the newest unread"},
		},
	},
	{
		method: "AckUserNotifications",
		says:   "Marks the user's notifications read through a seq: a position of its own, which the mail's does not move. It answers nothing; it is a `notifications_read` event in the stream.",
		args: []field{
			{"name", "string", "the user"},
			{"through", "number", "the seq of the newest notification shown"},
		},
		requests: []string{`{"name":"jo","through":%d}`},
	},
	{
		method:   "UserAsks",
		says:     "The person's decisions: every open ask addressed to them, on any bead in any stage and from the linked yards, oldest first, with what they have dealt with beside it. A question another yard asked stands as `bead@peer`, placed by when this yard took it; answered, put aside or declined here it is among the decided by what this yard knows of it, since the answer lands in the other yard. It is not `yardr review`'s queue, which walks beads in manual stages by bead age.",
		args:     []field{{"name", "string", "the user"}},
		requests: []string{`{"name":"jo"}`},
		fields: append(append(append(append(queueRow("open[]."), secretAsked("open[].")...), runAsked("open[].")...), setupAsked("open[].")...),
			append(append(append(append(queueRow("decided[]."), secretAsked("decided[].")...), runAsked("decided[].")...), setupAsked("decided[].")...),
				field{"decided[].given", "array of string?", "the names a secret ask was given values for, or the account names a setup ask was approved with, which reads \"gave `<names>`\"; absent on any other ask"},
				field{"decided[].ran", "object?", "the command the yard ran when a run ask was approved, which reads \"approved, ran: `<command>`\"; or the setup a setup ask approved, which reads \"approved, being carried out\", \"approved, ran\" or \"approved, failed\" with the preset and the names given; absent on any other ask and on an ask that was declined or put aside"},
				field{"decided[].ran.argv", "array of string", "the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name"},
				field{"decided[].ran.result", "string", "what the command printed; on a setup ask not yet carried out, `approved, to be carried out`, and afterwards what the carrying out printed"},
				field{"decided[].state", "string", "`answered`, `put aside`, `declined` or `taken back`. A later build may add a state: one a client does not know is still not open, and is shown by its word"},
				field{"decided[].option", "number?", "the picked option's number; absent on a words answer, an aside ask, a declined one and one taken back"},
				field{"decided[].option_label", "string?", "the picked option's label; absent with `option`"},
				field{"decided[].words", "string?", "what went with the pick, the words answer, or why the question is declined or taken back; for a question another yard asked that ended there, how it ended; absent when there are none"},
				field{"decided[].by", "string?", "who took the ask back, its asker or the operator: it reads \"taken back by `<by>`: `<words>`\", and is no answer of the person. Absent on every other state, and on a question another yard took back, whose `words` say where"},
				field{"decided[].return_at", "time?", "when a put-aside ask comes back; absent otherwise"},
			)...,
		),
		after: "Read it again on every `noted` event: a question asked, answered, put aside, declined or taken back, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then. An answer to a question another yard asked that did not land arrives as `noted` by `yard`: the question is open again, or decided with how it ended there, and the person has mail from `yard` that says which.",
	},
	{
		method:   "Bead",
		says:     "One bead whole: body, stage, labels, notes, its asks with their options, recommendation and state, and for each about-bead its id, title and stage. On a `bead@peer` ref, a question another yard asked, the view is synthesized from what that yard carried: its title, stage, topic and labels, its body under one first line saying the bead lives in the other yard and at which stage, no notes, the asks whole with their state as this yard knows it, and the about-beads as carried.",
		args:     []field{{"bead", "string", "the bead: a handle, or `bead@peer` for a question another yard asked"}},
		requests: []string{`{"bead":"yd-links"}`, `{"bead":"yd-ledger@north"}`, `{"bead":"yd-crm"}`},
		fields: []field{
			{"id", "string", "the bead's handle, or `bead@peer` for a question another yard asked"},
			{"title", "string", "the bead's title"},
			{"body", "string", "the bead's body; on a `bead@peer`, one first line saying the bead lives in the other yard and at which stage, then the body as carried"},
			{"stage", "string", "the bead's stage"},
			{"topic", "string", "the bead's topic: its first `topic:` label, `\"\"` when it has none"},
			{"labels", "array of string", "the bead's labels"},
			{"notes[].seq", "number", "the note's seq"},
			{"notes[].kind", "string", "the note's kind: `note`, `journal` or `handoff`"},
			{"notes[].author", "string", "who wrote it"},
			{"notes[].text", "string", "the note"},
			{"notes[].at", "time", "when it was written"},
			{"notes[].via", "string?", "the [device](#over-the-network) the note was written through; absent on a note written on the yard's own machine"},
			{"notes[].files", "array?", "the files the note carries, each an image, a page or a file, in the order it was written with them; absent on a note without any"},
			{"notes[].files[].hash", "string", "the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes"},
			{"notes[].files[].type", "string", "what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page"},
			{"notes[].files[].name", "string?", "the file's base name as the sender gave it; absent on a paste and on a letter from before names"},
			{"notes[].files[].bytes", "number", "how many bytes it is"},
			{"asks[].ask", "number", "the ask note's seq: what an answer names"},
			{"asks[].to", "string", "who must answer"},
			{"asks[].asker", "string", "who asked: a name, or `name@peer` for a question another yard asked"},
			{"asks[].since", "time", "when the ask was written (as sent, for a question another yard asked)"},
			{"asks[].question", "string", "the question in one line, as a person would ask it"},
			{"asks[].context", "array of string", "what the question is about, two to five lines"},
			{"asks[].options", "array", "the options in order, `you decide` last; none wants words"},
			{"asks[].options[].label", "string", "the option's label"},
			{"asks[].options[].consequence", "string", "what picking it does, in a line"},
			{"asks[].recommend", "number?", "the recommended option's number; absent when nothing is recommended"},
			{"asks[].why", "string?", "why the recommended option, in a line; absent with `recommend`"},
			{"asks[].about", "array?", "the other beads the question is about; absent when there are none"},
			{"asks[].about[].id", "string", "the bead's handle"},
			{"asks[].about[].title", "string", "the bead's title"},
			{"asks[].about[].stage", "string", "the bead's stage"},
			{"asks[].state", "string", "`open`, `answered`, `put aside`, `declined` or `taken back`. One a client does not know is not open: it takes no answer"},
			{"asks[].option", "number?", "the picked option's number; absent until one is picked"},
			{"asks[].words", "string?", "what went with the pick, the words answer, or why the question is declined or taken back; absent when there are none"},
			{"asks[].by", "string?", "who took the ask back, its asker or the operator; absent on every other state"},
			{"asks[].return_at", "time?", "when a put-aside ask comes back; absent otherwise"},
			{"asks[].picks_before", "number?", "how many picks there were before the current one, so a client can say \"changed\"; absent when there were none"},
			{"asks[].secret", "object?", "what a secret ask asks for; absent on any other ask. Such an ask has no options and takes no words: show it with a field that does not echo, and answer it with [`GiveSecret`](#for-the-operator-alone). `AnswerAsk` refuses a pick and words on it, and `DeclineAsk` words"},
			{"asks[].secret.source", "string", "the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask"},
			{"asks[].secret.env", "array of string", "the names to ask the person a value for, each a variable of the source's credential; with `user`, the one variable the source reads of a user's file; with `crew`, variables of the source's credential; with `as`, the names a call as that user mints or uses; on a login ask, the one name the input is asked for; on a harness-key ask, variables of the kind's `login_env`"},
			{"asks[].secret.user", "string?", "the user whose own address in the source is asked for (`yardr bead ask --secret <source> --for-user <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on an ask for the source's credential. A client needs nothing new for it: the field, the call and `line` are the same"},
			{"asks[].secret.crew", "string?", "the crew member whose own credential in the source or harness API key is asked for: the value goes to a file of that crew member's and is the operator's to give. A source credential takes effect when the crew member next starts; a harness key restarts sessions of that kind waiting at the login screen when given. Absent on any other secret ask"},
			{"asks[].secret.as", "string?", "the user whose own credential in the source is asked for (`yardr bead ask --secret <source> --as <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same"},
			{"asks[].secret.login", "string?", "the waiting login the value goes to (`yardr bead ask --secret-login <login>`): the reference the login's leader printed in its input need, handed the value and kept nowhere. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same"},
			{"asks[].secret.harness", "string?", "the harness kind whose API key is asked for; absent on any other secret ask"},
			{"asks[].secret.line", "string", "what the card says is asked for, in the yard's words and never the asker's: the names, the source, and where the source's calls go; with `user`, whose address it is and the command that tells them through the source; with `crew`, whose credential it is, that the env_file line is the one written, and when it takes effect; with `as`, whose credential it is and that a call made as them reads it; with `login`, the waiting login the value goes to; with `harness`, whose API key it is and when it takes effect. Show it as it is"},
			{"asks[].concealed", "bool?", "whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret, which is shown with a field that echoes; absent (concealed) on any other ask"},
			{"asks[].given", "array of string?", "the names a secret ask was given values for, or the account names a setup ask was approved with, which reads \"gave `<names>`\"; absent on any other ask and until it is given"},
			{"asks[].run", "object?", "the command a run ask proposes: one yardr command that is the operator's to give, which the yard runs when the operator approves the ask. Absent on any other ask. Such an ask has no options and takes no words: `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveAsk`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> --approve`), or from a device of the operator. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved at the yard by the one it was asked of, that user or the operator. All of `run` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text"},
			{"asks[].run.argv", "array of string", "the command in words, without the leading `yardr`, as the yard parsed it"},
			{"asks[].run.command", "string", "the command whole, as it is typed in a terminal and as it would run. Never shorten it: a view that cannot show it whole says where the ask can be read instead"},
			{"asks[].run.does", "array of string", "what the command changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it"},
			{"asks[].run.back", "string", "the command that takes the change back"},
			{"asks[].run.back_by", "string?", "who may run `back`, in words that stand before \"can take it back\": `only the operator` for a source's limits, `<user> or the operator` for a user's subscription and for what they are told of. Absent where that is anyone, which reads \"anyone can take it back\""},
			{"asks[].ran", "object?", "the command the yard ran when a run ask was approved, which reads \"approved, ran: `<command>`\" with `result` under it; or the setup a setup ask approved, which reads \"approved, being carried out\", \"approved, ran\" or \"approved, failed\" with the preset and the names given; absent on any other ask and until it is approved. The ask's `state` is then `answered`"},
			{"asks[].ran.argv", "array of string", "the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name"},
			{"asks[].ran.result", "string", "what the command printed, made by the yard: a line for each thing it changed, with a line break between them; on a setup ask not yet carried out, `approved, to be carried out`, and afterwards what the carrying out printed"},
			{"asks[].setup", "object?", "the setup a setup ask proposes: the pack with the yard's sentences for what approving it does, the account to type beside the preset pick or the candidates to take instead (on an update, the account that already stands), and the presets the ask's options count. Absent on any other ask. `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveSetup`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> <n> --secret`, `--account <source>` where the card lists a candidate, or a bare `<n>` where the card is `given`), or from a device of the operator. All of `setup` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text"},
			{"asks[].setup.pack", "string", "the embedded pack to set up, of kind source, whose `yardr pack show <pack>` prints the presets numbered"},
			{"asks[].setup.label", "string?", "the build the pack is embedded in, as the card shows it"},
			{"asks[].setup.account", "array of string", "the names the account is made of: the mint's inputs where the pack's credential is minted, else every credential name. A first setup types every one beside the preset pick; an update says by `given` and `missing` which of them the account file that stands holds. Where the card lists candidates, the answer names one of them instead"},
			{"asks[].setup.account_file", "string", "the file a typed value goes to, on the yard's machine; on an update, the account file that stands"},
			{"asks[].setup.given", "bool?", "on an update, where the yard already holds the source the pack brings: `true` when its account file already holds every name of `setup.account`, so that a bare preset pick keeps it, typing every name changes it, and a candidate may be taken instead; absent on a first setup, where nothing stands yet, and where the file lacks a name (`setup.missing`)"},
			{"asks[].setup.missing", "array of string?", "on an update, the names of `setup.account` the account file that stands does not set, where the pack newly needs them (every name where the file is not there): the answer types exactly these, and the names that stand stay, or takes a candidate; absent on a first setup and where the file holds every name"},
			{"asks[].setup.candidates", "array?", "yard sources whose account the card offers to reuse; absent where none match"},
			{"asks[].setup.candidates[].source", "string", "the source whose account the card offers"},
			{"asks[].setup.candidates[].file", "string", "the file holding the shared account inputs"},
			{"asks[].setup.candidates[].values", "object", "the shared account input names and values the file holds"},
			{"asks[].setup.does", "array of string", "what approving changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it"},
			{"asks[].setup.back", "string", "the commands that take the setup back"},
			{"asks[].setup.back_by", "string?", "who may run `back`, in words that stand before \"can take it back\". Absent where that is anyone, which reads \"anyone can take it back\""},
			{"asks[].setup.carried", "array", "what the pack brings, behind the card's sentences for checking the reading: each source's whole declaration with its command files' bytes, the inventory of the copies it would replace, and the replacement diffs"},
			{"asks[].setup.presets", "array", "the pack's presets with their grants, in the order the ask's options count them"},
			{"asks[].setup.presets[].name", "string", "the preset's name, as the option's label shows it"},
			{"asks[].setup.presets[].grants", "array", "what of each source the preset grants"},
			{"asks[].setup.presets[].grants[].source", "string", "the source the pack declares"},
			{"asks[].setup.presets[].grants[].calls", "array of string", "`read` for every read call, calls by name"},
			{"asks[].setup.to", "array", "who the grants are for"},
			{"asks[].setup.to[].group", "string?", "the group, and so each session it starts"},
			{"asks[].setup.to[].crew", "string?", "the crew member"},
		},
		after: "Read it again on every event whose `bead` is the bead: `noted` and the bead's other events, whose kinds are yardr's own.",
	},
	{
		method:   "Arrivals",
		says:     "The arrivals board: the open beads still travelling, everything but the backlog, up to 15 rows in arrival order, beside the platforms in the order the beads travel. It is what footplate draws: the yard computes the board once so the readers draw without computing. A row is the bead, its title, its platform (stage) and line (depot), the expected arrival as the yard's printed range, and the remark with its tone, and for a held bead who holds it; no via, no badge.",
		requests: []string{`{}`},
		fields: []field{
			{"rows[].bead", "string", "the bead's handle"},
			{"rows[].title", "string", "the bead's title"},
			{"rows[].platform", "string", "the stage the bead stands at"},
			{"rows[].line", "string", "the depot the bead is worked in, `\"\"` when it has none"},
			{"rows[].expected", "string", "the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead"},
			{"rows[].remark", "string", "held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `\"\"` when there is none"},
			{"rows[].tone", "string", "`red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark"},
			{"rows[].held_by", "string?", "who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held`"},
			{"platforms", "array of string", "the stages the rows stand on, in the order the beads travel: group the rows by this list"},
		},
		after: "The order is the board's: beads with a running session first, then by the low end of the expected arrival in minutes, then by how long the bead has stood at its stage, oldest first, then by id. A low that reads as no span — `under a minute` among it — sorts with the beads the yard serves no ETA for. The remark is the board's too, but for a hold's holder: held (`held by <name>`, or `held` where the yard does not know who held the bead), stalled (the latest ended session came to no good end), blocked by an open bead, waits for a person, late, in that order. A late bead says by how much where the yard can tell: its time in its stage past the usual, in the yard's span format (`late by 1h 5m`), else `late`.\n\n" +
			"Platforms lists the stages the rows stand on, in the order the beads travel: each depot's flow stage order, as the yard draws it on its flows page. Where depots' flows order the same stages differently, the depot with the most travelling beads sets the order and the others' extra stages stand where their own flows put them after the last stage they share with it, and a stage no flow has stands last, in row order.\n\n" +
			"Read it again on every event that carries a bead, and on the events without one that move its inputs: `group_set` and `group_removed`, `route_added`, `route_set` and `route_removed`, `flow_set`, `flow_removed`, `flow_pointer_set` and `flow_pointer_cleared`, `depot_updated`, and `setting_changed` whose `key` is `max_sessions` or `operator`. One minute is the ETA's grain: no timed refresh.",
	},
	{
		method:   "Flows",
		says:     "The flows view: the yard's depots as lines, each with its travelling total, its flows, each flow its stages in travel order with the open beads on each and its lamp, and its Elsewhere rows; the beads travelling with no line; and the yard-wide travelling total. It is what footplate's timetable draws: the yard computes the view once so the readers draw without computing. Each bead's row is its Arrivals row, with whether it is worked; each stage's lamp is the panel's: fault where a held, blocked or stranded bead stands on it, turn where only a person's turn does, none otherwise.",
		requests: []string{`{}`},
		fields: slices.Concat([]field{
			{"travelling", "number", "the yard's open beads outside the backlog: the depots' totals plus the unassigned count, complete, not capped"},
			{"depots[].name", "string", "the depot"},
			{"depots[].kind", "string", "`git` or `dir`, as the depot is"},
			{"depots[].travelling", "number", "the depot's open beads outside the backlog"},
			{"depots[].flows[].name", "string", "the flow"},
			{"depots[].flows[].type", "string", "the bead type it governs, `\"\"` for the depot's own"},
			{"depots[].flows[].source", "string", "where the flow comes from, as `yardr flow show` says it: `store`, `repo@<sha>`, `repo (directory)`"},
			{"depots[].flows[].stages[].stage", "string", "the stage"},
			{"depots[].flows[].stages[].group", "string", "the group a bead of the flow's type would be routed to here, `\"\"` when no route matches"},
			{"depots[].flows[].stages[].human", "bool", "the group is a manual one"},
			{"depots[].flows[].stages[].lamp", "string", "`fault` where a held, blocked or stranded bead stands here, `turn` where only a person's turn does, `none` otherwise"},
			{"depots[].flows[].stages[].terminal", "bool", "the stage closes the bead entering it"},
			{"depots[].flows[].stages[].limit", "number?", "the routed group's seat limit, `0` pausing the group; absent where no route matches"},
			{"depots[].flows[].stages[].taken", "number?", "how many of the routed group's seats live sessions hold across the whole yard, the same number on every stage routed to the group; absent where no route matches"},
		},
			flowBead("depots[].flows[].stages[].beads[]."),
			flowBead("depots[].elsewhere[]."),
			[]field{
				{"depots[].error", "string?", "why the depot's flows could not be read; its beads are then all in Elsewhere; absent when they read"},
			},
			flowBead("unassigned[].")),
		after: "Every depot stands here, travelling or not, and a removed depot's line leaves with it: `DeleteDepot` refuses while open beads remain. The unassigned rows are the open beads outside the backlog with no depot, the newest change first.\n\n" +
			"Read it again on every event that carries a bead, and on the events without one that move its inputs: `group_set` and `group_removed`, `route_added`, `route_set` and `route_removed`, `flow_set`, `flow_removed`, `flow_pointer_set` and `flow_pointer_cleared`, `depot_updated` and `depot_removed`, and `setting_changed` whose `key` is `max_sessions` or `operator`. One minute is the ETA's grain: no timed refresh.",
	},
	{
		method: "AnswerAsk",
		says:   "Answers an ask: picks an option with words alongside, or answers an ask with no options with words. It is what `yardr bead answer` does with an option or `--words`: words alone on an ask with options are refused, and go as a plain note instead. On a `bead@peer`, a question another yard asked, the answer is queued home for that yard instead of written as a note: only the person the question is for may give it, the link must carry `ask` out, and words alone on an ask with options are refused with the address to mail the asker at. It answers nothing; the answer is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"option", "number", "the picked option's number, as the bead shows it; `0` with words answers an ask with no options"},
			{"words", "string", "what goes with the pick, or the answer; left out, the pick stands alone"},
			{"actor", "string", "who answers: the user's name"},
		},
		requests: []string{`{"bead":"yd-links","ask":3,"option":1,"words":"move them tonight","actor":"jo"}`},
	},
	{
		method: "DeferAsk",
		says:   "Puts the ask aside: it leaves the person's queue for 24 hours or until the asker's next note on the bead, whichever is first. It is what `yardr bead answer` does with `--later`. On a `bead@peer` it is 24 hours by this yard's clock and nothing else, and the put-aside travels home as an answer does. It answers nothing; putting it aside is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"actor", "string", "who puts it aside: the user's name"},
		},
		requests: []string{`{"bead":"yd-links","ask":4,"actor":"jo"}`},
	},
	{
		method: "DeclineAsk",
		says:   "Ends the ask with no option picked: the person will not decide it. It is what `yardr bead answer` does with `--decline`, and reads wherever the ask is shown as \"`<user>` declined to decide this: `<words>`\". On a `bead@peer` the decline travels home as an answer does, and ends the ask in both yards. It answers nothing; declining is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"words", "string", "why they will not decide it"},
			{"actor", "string", "who declines: the user's name"},
		},
		requests: []string{`{"bead":"yd-greet","ask":5,"words":"the greeting can wait","actor":"jo"}`},
	},
	{
		method: "TakeBackAsk",
		says:   "Takes an open ask back: its asker asks it no more, and says why. It is what `yardr bead ask --take-back` does. The ask leaves the person's open decisions and stands among the decided as `taken back`, with `by` and the reason as `words`: \"taken back by `<by>`: `<words>`\", never an answer of the person. It is the asker's to do and the operator's, and is refused for anyone else; refused too on an ask that is answered, declined or taken back already, and on a `bead@peer`, which is taken back in the yard that asked. An ask put aside is taken back as an open one is. `AnswerAsk`, `DeferAsk`, `DeclineAsk` and `GiveSecret` refuse it afterwards, naming who took it back and why. Where the ask also stands in a linked yard it leaves the decisions there as well, with `it was taken back in <peer>'s yard`. It answers nothing; taking it back is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"reason", "string", "why it is asked no more, in a line: the person reads it where the question stood"},
			{"actor", "string", "who takes it back: the asker's name, or the operator's"},
		},
		requests: []string{`{"bead":"yd-greet","ask":%d,"reason":"the design settles the greeting","actor":"jo"}`},
	},
	{
		method: "AddNote",
		says:   "Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. On a `bead@peer`, a question another yard asked, it is refused: notes stay local, and words for the asker go as mail to `asker@peer`. With `files` the note carries files, as one written with `yardr bead note --file` does. It answers nothing; the note is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"text", "string", "the note; it may not be empty when the note carries files"},
			{"actor", "string", "who writes it: the user's name"},
			{"files", "array of object", "the files the note carries, at most 64: each `{hash, name}`, the hash as `PutBlob` answered it with the file's base name. Left out, the note carries none. One the yard does not hold, or a name no letter may carry, refuses the note"},
		},
		requests: []string{`{"bead":"yd-greet","text":"the newcomer arrives Monday","actor":"jo"}`,
			`{"bead":"yd-greet","text":"the newcomer arrives Monday, in this doorway","actor":"jo","files":[{"hash":"` + apiPNGHash + `","name":"doorway.png"}]}`},
	},
}

// apiOperatorCalls are the calls of the doc that OperatorCalls names:
// documented and held to the server as apiCalls are, on the socket and on
// the listener, where the device of the doc is the operator's.
var apiOperatorCalls = []apiCall{
	{
		method: "GiveSecret",
		says:   "Answers a secret ask (an ask of `Bead` with `secret`): the values go into the file the source's credential is read from, and the answer on the bead holds the names and never a value. A value is kept in that file and nowhere else: not in the store, an event, a note or the log, and no call reads one back. It is what `yardr bead answer --secret` does. It is the operator's to give: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. An ask with `secret.user` asks for that user's own address in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and it does not set the user's `notify`. An ask with `secret.crew` asks for that crew member's own credential in the source instead: the value goes into the crew member's `env_file`, or with none set into `crew/<name>.env` in the yard's home, which is then set; it is the operator's to give, on the socket and from a device of the operator, restarts nothing, and takes effect when the crew member next starts. An ask with `secret.harness` asks for that crew member's harness API key instead: the names come from the harness kind's `login_env`; the values go into the crew member's `env_file`, or with none set into `crew/<name>.env` in the yard's home, which is then set. It is the operator's to give, on the socket and from a device of the operator; it is kept in that file and not in the store, an event, a note or the log. Giving it restarts sessions of that kind waiting at the login screen, and new sessions use the key. An ask with `secret.as` asks for that user's own credential in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and the source needs no `notify` key. An ask with `secret.login` asks for the input of the harness login that waits under that reference instead: the value is handed to the waiting login and kept nowhere — not in a file, the store, an event, a note or the log — and the answer holds the names as any giving does; it is the operator's to give, on the socket and from a device of the operator, and is refused when no login waits under the reference. Refused too: an ask that is declined, names that are not exactly `asks[].secret.env`, an empty value, and one with a line break or white space at either end; a refusal never repeats a value. Giving again replaces what was given. The answer is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"values", "object", "each name of `asks[].secret.env` to the value the person typed"},
			{"actor", "string", "who gives: the operator's name, or for an ask with `secret.user` or `secret.as` that user's"},
		},
		requests: []string{
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"PHONE_USER":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"INPUT":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"ANTHROPIC_API_KEY":"what-the-person-typed"},"actor":"jo"}`,
		},
		fields: []field{
			{"given", "array of string", "the names that were written"},
			{"file", "string", "the file they are in, on the yard's machine; empty when the value went to a waiting login instead"},
			{"login", "string?", "the waiting login the value was handed to; absent for a source's credential"},
			{"credential", "string?", "where every variable of the source's credential is set now, in words; absent while one is still missing. With `user`, where the address is set; with `crew` or `as`, where the credential is set"},
			{"missing", "string?", "what the credential still wants, in words; absent when it is whole"},
			{"user", "string?", "whose own address was given, and whose file `file` is; absent for a source's credential"},
			{"crew", "string?", "whose own credential or harness key was given, and whose file `file` is; absent for a source's credential and for a user's address"},
			{"as", "string?", "whose own credential was given, and whose file `file` is; absent for a source's credential and for a user's address"},
			{"next", "string?", "with `user`: the command that comes next, in a line to show as it is: the one that tells the user through the source, or once that is set the one that sends a test message; with `crew`, including a harness key: when the value takes effect, in a line to show as it is; with `as`: the command that tries it as that user, in a line to show as it is; absent for a source's credential"},
		},
		after: "The file and the store are two writes. When the yard wrote the file and could not record it, the error's message says so with the names and the file, and the ask is open still: give it again. When the call ends without a reply (the server went away), the value may be written: say so, never \"sent\", and give it again.",
	},
	{
		method: "ApproveAsk",
		says:   "Approves a run ask (an ask of `Bead` with `run`), and the yard runs the command the ask holds, as the one who approves: `run.command`, whole, and nothing else. It is what `yardr bead answer --approve` does. In one write the yard checks that it would write the same card now, makes the change under the command's own check, and records the answer, which holds `ran`. A refused approval changes nothing, records nothing and leaves the ask open. It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved by the one it was asked of, that user or the operator, on the socket; a device of that user is not served the call. Refused too: an ask that proposes no command; one that was declined, taken back or approved already, since a command runs once for one ask; a `bead@peer`, since a command is approved in the yard that asked and never over a link; and an ask whose card is from before the yard changed, where what the command names was replaced under its name or what it changes is no longer what `run.does` says. That one is declined, and asked again if it is still wanted. The answer is then a `noted` event in the stream, and the change is the command's own event, which carries `approved` with the bead and the ask.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"actor", "string", "who approves: the operator's name, or for a command that is a user's own the name of the one it was asked of"},
		},
		requests: []string{`{"bead":"yd-crm","ask":%d,"actor":"jo"}`},
		fields: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask that was approved"},
			{"asker", "string", "who proposed the command"},
			{"argv", "array of string", "the command as it ran, in words, without the leading `yardr`: the one the ask proposed"},
			{"does", "array of string", "what the command changed, in the yard's words, as the card had them"},
			{"result", "string", "what the command printed, made by the yard: a line for each thing it changed, with a line break between them"},
		},
		after: "The change and the answer are one write: the command ran and the ask is approved, or neither. When the call ends without a reply (the server went away), a client cannot know which: say \"the yard did not answer; read the card again\", never \"approved\", and read `Bead` again, where `asks[].ran` is there once the command ran.",
	},
	{
		method: "ApproveSetup",
		says: "Approves a setup ask (an ask of `Bead` with `setup`), and the yard then applies the pack, takes the account, and grants the preset picked, as the operator: `setup.pack`, the account `setup.account`, and the preset's grants to `setup.to`. " +
			"It is what `yardr bead answer <n> --secret` (or `--account <source>`, or a bare `<n>` where the card is `given`) does. " +
			"The request settles the account in one of three ways: it types it, takes a candidate the card lists, or keeps the account that stands. What the card says (`setup.given`, `setup.missing`, `setup.candidates`) decides which are open to it, and the paragraph under the reply's fields says how. The examples are, in order, a first setup with the account typed, a first setup with a candidate taken, an update that keeps the account that stands, and an update that types the names it is missing. " +
			"In one write the yard checks that it would write the same card now and records the approval, which holds the preset, the names given, the candidate taken or the account kept, and `ran`; the server carries it out after the call. Typed values live in its memory alone until the account's write; a picked candidate hands none. A refused approval changes nothing, records nothing and leaves the ask open. " +
			"It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. Picking `you decide` grants the asker's recommended preset, and is refused where none is recommended. " +
			"Refused too: an ask that sets up no pack; one that was declined, taken back or approved already, since a setup runs once for one ask; a `bead@peer`, since a setup is approved in the yard that asked and never over a link; values and a candidate together; values that are not exactly the names the card asks for, and a candidate the card lists none of; and an ask whose card is from before the yard changed, where the build, the pack's bytes or presets, a source, the account's owed-ness, a candidate or a grantee moved. That one is declined, and asked again if it is still wanted. The answer is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"option", "number", "the preset pick, counting `asks[].options` as shown, `you decide` last"},
			{"values", "object", "each name the person typed, with its value: every name of `setup.account` on a first setup, the names of `setup.missing` alone on an update that has any, and on an update that is `given` every name of `setup.account`, to change the account that stands; left out where a candidate is taken or the account is kept"},
			{"account", "string", "the candidate taken instead of typing, one the card lists (`setup.candidates[].source`); left out where the values are typed or the account is kept"},
			{"actor", "string", "who approves: the operator's name"},
		},
		requests: []string{
			`{"bead":"yd-crm","ask":%d,"option":1,"values":{"COURIER_ACCOUNT":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"option":1,"account":"courier-existing","actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"option":1,"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"option":1,"values":{"PARCEL_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
		},
		fields: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask that was approved"},
			{"asker", "string", "who proposed the setup"},
			{"pack", "string", "the pack set up"},
			{"preset", "string", "the preset picked"},
			{"given", "array of string?", "the account names that were typed; absent when a candidate was taken or the account was kept"},
			{"account", "string?", "the candidate taken: its `source` in `setup.candidates`; absent when the account was typed or kept"},
			{"keep", "bool?", "`true` when the account that stands was kept with a bare preset pick; absent when it was typed or a candidate was taken"},
			{"result", "string", "`approved, to be carried out`: the server carries the setup out after the call"},
		},
		after: "What the card holds decides the account. On a first setup, neither `setup.given` nor `setup.missing`, nothing stands yet: the request types every name of `setup.account` in `values`, or names a candidate in `account`. " +
			"On an update the account file that stands is `setup.account_file`. When it holds every name (`setup.given`), the request sends neither `values` nor `account` to keep it, which the reply says as `keep`, or types every name of `setup.account` to change it, or names a candidate. " +
			"When it lacks some (`setup.missing`), the request types exactly those names and the others stay where they are, or names a candidate. " +
			"The reply has one of `given` (the names typed), `account` (the candidate taken) and `keep`.\n\n" +
			"The approval and the answer are one write: the ask is approved, or neither. The carrying out follows after the call: while `ran.result` is `approved, to be carried out` the setup reads as being carried out, and as ran or failed once the server finishes it; read `Bead` again for it. When the call ends without a reply (the server went away), a client cannot know whether the approval landed: say \"the yard did not answer; read the card again\", never \"approved\", and read `Bead` again. An open ask is approved again; an approved one is left for the server, whose failure names the account asked again.",
	},
}

// apiEvent is one kind of event of the doc: is picks it from the stream.
type apiEvent struct {
	title  string
	says   string
	is     func(e store.Event) bool
	fields []field
}

func kindOf(kind string) func(store.Event) bool {
	return func(e store.Event) bool { return e.Kind == kind }
}

var apiEvents = []apiEvent{
	{
		title: "Mail to a user or a crew member: `mail`",
		says:  "Mail was sent within the yard, or came from a linked yard for one of this yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.",
		is: func(e store.Event) bool {
			peer, _ := e.Data["to_peer"].(string)
			return e.Kind == "mail" && peer == ""
		},
		fields: []field{
			{"seq", "number", "the letter's seq"},
			{"kind", "string", "`mail`"},
			{"actor", "string", "the sender"},
			{"at", "time", "when"},
			{"data.to", "string", "the receiver"},
			{"data.text", "string", "the mail"},
			{"data.to_user", "bool?", "true when the receiver is a user; absent when it is a crew member"},
			{"data.as", "string?", "on mail that came from a linked yard, whose `actor` is `<name>@<peer>`: the user of this yard the link trusted that name as when it came, as a letter's `as`; absent on any other mail"},
			{"data.files", "array?", "the files the mail carries, each an image, a page or a file, as a letter's `files`; absent on mail without any"},
			{"data.files[].hash", "string", "the file's name: `GET /blobs/<hash>` answers the bytes"},
			{"data.files[].type", "string", "what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page"},
			{"data.files[].name", "string?", "the file's base name as the sender gave it; absent on a paste and on a letter from before names"},
			{"data.files[].bytes", "number", "how many bytes it is"},
			{"data.via", "string?", "the [device](#over-the-network) the sender wrote through; absent on mail written on the yard's own machine. Every event a device's call writes as its user carries it, a `noted` and a `mail_read` too"},
		},
	},
	{
		title: "Mail to a linked yard: `mail` with `to_peer`",
		says:  "Mail was queued for one of a linked yard: the conversation with `<data.to>@<data.to_peer>`, of the user `actor`. It is `queued` until a `peer_message_sent` or `peer_message_failed` event with that `data.peer` says otherwise; read the conversation again then.",
		is: func(e store.Event) bool {
			peer, _ := e.Data["to_peer"].(string)
			return e.Kind == "mail" && peer != ""
		},
		fields: []field{
			{"seq", "number", "the letter's seq"},
			{"kind", "string", "`mail`"},
			{"actor", "string", "the sender"},
			{"at", "time", "when"},
			{"data.to", "string", "the receiver's name in the peer's yard"},
			{"data.to_peer", "string", "the link"},
			{"data.text", "string", "the mail"},
		},
	},
	{
		title: "Mail was read: `mail_read`",
		says:  "A user or a crew member acknowledged its mail. Every letter to `data.reader` with a seq up to `data.through` is read: your user's own, when another client acknowledged them, and the letters your user sent a crew member.",
		is:    kindOf("mail_read"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`mail_read`"},
			{"data.reader", "string", "who read"},
			{"data.through", "number", "through which seq"},
		},
	},
	{
		title: "A reaction on mail: `reaction`",
		says:  "A party to a letter put an emoji on it, or took theirs away. Read the conversation with the other party again then: `reactions` on the letter of `data.seq` is what it now holds.",
		is:    kindOf("reaction"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`reaction`"},
			{"actor", "string", "who reacted"},
			{"at", "time", "when"},
			{"data.seq", "number", "the seq of the mail's event"},
			{"data.emoji", "string", "which of 👍 👎 ❤️ 🎉 👀 ✅ it is; `\"\"` when the reaction was taken away"},
			{"data.to", "string", "the other party to the mail, whom this reaction tells in a crew inbox"},
			{"data.preview", "string", "the mail's first words for the crew inbox line, whitespace folded and at most 80 characters"},
		},
	},
	{
		title: "A crew member's status: `crew_status`",
		says:  "The daemon saw a crew member's session do something else than before. Any other event whose kind begins `crew_` says that a session started, stopped or failed, or that the list changed, and carries less than what changed: read `Crew` again on it.",
		is:    kindOf("crew_status"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`crew_status`"},
			{"data.crew", "string", "the crew member"},
			{"data.status", "string", "`working`, `idle`, `blocked` or `unknown`"},
			{"data.was", "string", "what it was, `\"\"` for a session the daemon had not looked at"},
		},
	},
	{
		title: "A crew member's login: `crew_login`",
		says: "A crew member's session sat down at its harness's login screen or left it, or the yard began or stopped leading that login: `data.login_screen` and `data.login_led` are the two facts as they stand now, as `Crew` says them. " +
			"It is raised once for the change, not for every write the yard makes of the session's record. A session that starts or stops has the event of that, `crew_started` or `crew_stopped`, and this one is not raised besides. " +
			"Like the others whose kind begins `crew_` it carries less than what changed: read `Crew` again on it. " +
			"An event with `data.user` is of that user's conversation session, which `Crew` does not list: a client that shows the crew member's own session passes it over.",
		is: kindOf("crew_login"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`crew_login`"},
			{"data.crew", "string", "the crew member"},
			{"data.login_screen", "bool", "whether the session now sits at its harness's login screen, as `login_screen` of `Crew`"},
			{"data.login_led", "bool", "whether the yard now leads a login for it, as `login_led` of `Crew`"},
		},
	},
	{
		title: "A notification: any event with `notified`",
		says:  "An event became a notification for the users `data.notified` names, each with the reason. The event is not the notification's text: read `UserNotifications` again when your user is among them. Nothing else of such an event is for a chat client.",
		is: func(e store.Event) bool {
			_, ok := e.Data["notified"]
			return ok
		},
		fields: []field{
			{"seq", "number", "the notification's seq"},
			{"data.notified", "object", "user to reason"},
		},
	},
	{
		title: "Notifications were read: `notifications_read`",
		says:  "A user acknowledged their notifications, in this client or another.",
		is:    kindOf("notifications_read"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`notifications_read`"},
			{"data.reader", "string", "who read"},
			{"data.through", "number", "through which seq"},
		},
	},
	{
		title: "A note on a bead: `noted`",
		says:  "A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, a secret given, a command approved, put aside, declined), or its asker taking a question back. A question another yard asked arrives as `noted` too, on `bead@peer`. It says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.",
		is:    kindOf("noted"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`noted`"},
			{"bead", "string", "the bead the note is on: a handle, or `bead@peer` for a question another yard asked"},
			{"actor", "string", "who wrote it: a name, `name@peer` for a question another yard asked, or `yard` when an answer to one did not land"},
			{"at", "time", "when"},
			{"data.note", "number?", "the note's seq; absent on a question another yard asked"},
			{"data.ask", "number?", "the ask's seq on its bead, for a question another yard asked; absent otherwise"},
		},
	},
	{
		title: "A bead was closed: `closed`",
		says:  "A bead closed, by an explicit close or by entering a terminal stage of its flow. It says no more than that the bead is done: read `Bead` again when `bead` is one shown.",
		is:    kindOf("closed"),
		fields: []field{
			{"seq", "number", "the event's seq"},
			{"kind", "string", "`closed`"},
			{"bead", "string", "the bead that closed: a handle"},
			{"actor", "string", "who closed it"},
			{"at", "time", "when"},
			{"data.reason", "string", "why: the reason the close named, or the terminal stage it entered"},
			{"data.group", "string?", "the group whose work ended; absent when no group held the bead"},
			{"data.originator", "string?", "whose bead it was, the user the bead was for; absent when the bead names none"},
		},
	},
}

// apiErrors are the requests the doc shows refused, each with why.
var apiErrors = []struct{ method, request, says string }{
	{"GetUser", `{"name":"nobody"}`, "What the yard does not have:"},
	{"Mail", `{"to":"yardmaster","body":"hello","actor":"jo"}`, "An argument the call does not have (one misspelt would otherwise be a call that means another):"},
	{"GetUser", `{"name":7}`, "An argument of another type:"},
	{"Sing", `{}`, "A method it does not have:"},
}

// apiYard is a yard with all a chat client meets: the user jo, the crew
// member yardmaster with a running session, the linked yard north that
// exposes clerk, a notification for jo and a letter jo has not read. It
// returns the socket the yard is served on.
func apiYard(t *testing.T) string {
	t.Helper()
	st, _ := apiStore(t)
	sock := socket(t)
	serve(t, sock, Handler(st, nil).ServeHTTP)
	return sock
}

// apiDevice is the device jo has in the yard of the doc.
const apiDevice = "phone"

// apiStore is the store of apiYard, and the token of jo's device.
func apiStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	st, err := store.Open(filepath.Join(home, "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close(context.Background()) })
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = st.AddUser(ctx, "jo", "jo")
	must(err)
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude", "model": "opus"}}, "jo")
	must(err)
	must(st.CrewStarted(ctx, "yardmaster", map[string]any{"pane": "p1"}))
	must(st.CrewStatus(ctx, "yardmaster", "working"))

	key, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	must(err)
	_, err = st.AddPeer(ctx, store.Peer{Name: "north", Key: yardkey.Public(key), URL: "http://north.test"}, "jo")
	must(err)
	_, err = st.AllowPeer(ctx, "north", store.PeerAllow{Send: []string{store.PeerMail}, Receive: []string{store.PeerMail, store.PeerAsk}, Expose: []string{"jo"}}, "jo")
	must(err)
	must(st.PeerHeard(ctx, "north", yardkey.Public(key), &store.PeerSays{Understands: []string{store.PeerMail}, Yardr: "v1.4.0",
		Roster: []store.PeerRosterEntry{{Name: "clerk", Kind: store.RosterCrew, Role: "Keeps the books", Mail: true},
			{Name: "josephine", Kind: store.RosterUser, Mail: true}}}))

	must(st.AddSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, "jo"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Paint the shed", ID: "yd-shed"}, "yardmaster")
	must(err)
	must(st.AddNote(ctx, b.ID, "primed", "yardmaster"))
	png, err := base64.StdEncoding.DecodeString(apiPNG)
	must(err)
	shed, err := st.PutBlob(ctx, png)
	must(err)
	must(st.SetSetting(ctx, store.BlobMaxSetting, "8388608", "jo"))
	must(st.Mail(ctx, "jo", "The shed is painted. Shall I land it?", "yardmaster", store.BlobFile{Hash: shed.Hash, Name: "shed.png"}))
	// Jo reacts to the mail, so the letters the doc shows carry reactions.
	joBox, err := st.UserInbox(ctx, "jo", 1)
	must(err)
	must(st.React(ctx, joBox.Mail[0].Seq, "👍", "jo"))

	// Jo hears of the shed alone from here on: the decisions below are
	// asked and answered on other beads, and would otherwise stand in
	// the notifications example, which is about the call, not the queue.
	// The narrowing is here, after the mail, so the seqs above stay as
	// the doc shows them.
	must(st.RemoveSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, "jo"))
	must(st.AddSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"bead": "yd-shed"}}, "jo"))

	// The decisions: questions for jo on two beads, one with a topic and
	// one without, each state once, so every field the queue and the
	// bead calls promise has something to say. The writes the doc shows
	// answer the three that stay open here.
	train, err := st.CreateBead(ctx, store.NewBead{Title: "Release train", ID: "yd-train"}, "yardmaster")
	must(err)
	links, err := st.CreateBead(ctx, store.NewBead{Title: "Move the links", ID: "yd-links",
		Body: "The links file declares the new source.", Labels: []string{"topic:linear-out"}}, "yardmaster")
	must(err)
	greet, err := st.CreateBead(ctx, store.NewBead{Title: "The greeting", ID: "yd-greet"}, "yardmaster")
	must(err)
	_, err = st.AddNotedAs(ctx, links.ID, "the test passed on a copy", "yardmaster", "", store.BlobFile{Hash: shed.Hash, Name: "shed.png"})
	must(err)
	ask := func(id string, a store.Ask) uint64 {
		seq, err := st.AddAsk(ctx, id, a, "yardmaster")
		must(err)
		return seq
	}
	answer := func(id string, a store.Answer) {
		must(st.AddAnswer(ctx, id, a, "jo"))
	}
	apiAsks := map[string]uint64{}
	apiAsks["move"] = ask(links.ID, store.Ask{Question: "Move this yard's links to the declared source now?",
		To: "jo", Context: []string{"The links file declares the new source; 64 links still point at the old one.",
			"The move was tested on a copy just now and passed."},
		Options:   []store.AskOption{{Label: "Move them now", Consequence: "all 64 links point at the new source tonight"}, {Label: "After Friday", Consequence: "keep the old source until the release is out"}},
		Recommend: 1, Why: "the test passed", About: []string{train.ID}})
	apiAsks["order"] = ask(links.ID, store.Ask{Question: "This design before that one?", To: "jo",
		Context: []string{"both designs are shaped."},
		Options: []store.AskOption{{Label: "This first", Consequence: "that one waits a day"}}})
	apiAsks["tone"] = ask(greet.ID, store.Ask{Question: "What should the tone of the greeting be?", To: "jo",
		Context: []string{"the greeting."}})
	picked := ask(links.ID, store.Ask{Question: "Send yardr-3msx and yardr-vgjp to build?", To: "jo",
		Context: []string{"both beads are shaped."},
		Options: []store.AskOption{{Label: "Send them", Consequence: "both beads move to build tonight"}}})
	answer(links.ID, store.Answer{Ask: picked, Option: 1, Words: "send them tonight"})
	refused := ask(links.ID, store.Ask{Question: "Close sketch PR #3?", To: "jo",
		Context: []string{"the sketch landed yesterday."},
		Options: []store.AskOption{{Label: "Close it", Consequence: "PR #3 closes"}}})
	answer(links.ID, store.Answer{Ask: refused, Words: "not mine to take", Decline: true})
	worded := ask(greet.ID, store.Ask{Question: "Who greets the newcomer?", To: "jo", Context: []string{"they arrive Monday."}})
	answer(greet.ID, store.Answer{Ask: worded, Words: "warmly"})
	aside := ask(links.ID, store.Ask{Question: "The web view cleanup: build?", To: "jo",
		Context: []string{"the cleanup is shaped."},
		Options: []store.AskOption{{Label: "Build it", Consequence: "the cleanup moves to build"}}})
	answer(links.ID, store.Answer{Ask: aside, Later: true})
	// A newer pick supersedes: the bead shows how many came before it, so
	// a client can say "changed".
	answer(links.ID, store.Answer{Ask: picked, Option: 1, Words: "send them tonight"})
	// The write calls below name these asks by seq; the fixture fails
	// loudly when they move.
	if apiAsks["move"] != 3 || apiAsks["order"] != 4 || apiAsks["tone"] != 5 {
		t.Fatalf("the open asks are %d, %d, %d, want 3, 4, 5", apiAsks["move"], apiAsks["order"], apiAsks["tone"])
	}
	// A question another yard asked jo, so the queue and the noted event
	// each have a foreign row to show: bead@peer, asked by name@peer.
	foreign, err := json.Marshal(map[string]any{
		"v": 1, "to": "jo", "from": "clerk",
		"bead": "yd-ledger", "title": "Close the ledger", "topic": "topic:books", "stage": "build",
		"labels": []string{"topic:books"}, "body": "The ledger is ready to close.",
		"seq": 9, "since": time.Now().UTC(),
		"ask": map[string]any{
			"question": "Close the ledger tonight?", "to": "jo@south",
			"context": []string{"The bookkeeper signed off."},
			"options": []map[string]string{
				{"label": "Close it", "consequence": "the ledger closes tonight"},
				{"label": "You decide", "consequence": "leave it to clerk"},
			},
			"recommend": 1, "why": "signed off", "about": []string{},
		},
		"about": []map[string]string{},
	})
	must(err)
	_, err = st.PeerReceive(ctx, "north", yardkey.Public(key), []store.PeerMessage{{ID: "f1", Kind: store.PeerAsk, Body: foreign}})
	must(err)

	// Jo has a device, and wrote through it once: what came that way says
	// so. Last, so that the seqs above stay as the doc shows them.
	d, err := st.AddDevice(ctx, "jo", apiDevice, "jo")
	must(err)
	away := store.WithVia(ctx, "jo", apiDevice)
	must(st.Mail(away, "yardmaster", "On the train. Land the shed when it is green.", "jo"))
	must(st.AddNote(away, links.ID, "read on the train: the test on a copy is enough for me", "jo"))

	// Secret asks, each state the doc shows once: one given here, and two
	// that GiveSecret answers in the doc. After everything else, so that
	// the seqs above stay as the doc shows them.
	_, err = st.PutSource(ctx, "crm.yaml", apiSource("crm", "CRM_TOKEN", "CRM_UPDATE_TOKEN"), "jo")
	must(err)
	_, err = st.PutSource(ctx, "books.yaml", apiSource("books", "BOOKS_TOKEN", ""), "jo")
	must(err)
	crm, err := st.CreateBead(ctx, store.NewBead{Title: "Read the CRM", ID: "yd-crm"}, "yardmaster")
	must(err)
	secret := func(question, source string, env ...string) uint64 {
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"Create one at https://" + source + ".example.com/account/api (read only)."},
			Secret:  &store.AskSecret{Source: source, Env: env}})
	}
	books := secret("The API key for the books", "books")
	_, err = st.GiveSecret(ctx, crm.ID, books, map[string]string{"BOOKS_TOKEN": "typed-in-the-fixture"}, "jo")
	must(err)
	updates, token := secret("The CRM's token for updates", "crm", "CRM_UPDATE_TOKEN"), secret("The API key for the CRM", "crm", "CRM_TOKEN")

	// Asks their asker asks no more: one taken back here, so the queue and
	// the bead have that state to show, and one TakeBackAsk takes back in
	// the doc. Last again, for the seqs above.
	// On beads with nothing put aside: the asker's next note brings such an
	// ask back, and the doc shows one that is still aside.
	withdrawn := ask(crm.ID, store.Ask{Question: "Read the CRM's archive too?", To: "jo",
		Context: []string{"the archive holds the accounts closed before 2024."},
		Options: []store.AskOption{{Label: "Read it", Consequence: "the closed accounts are read as well"}}})
	must(st.TakeBackAsk(ctx, crm.ID, withdrawn, "the archive is not in the CRM's API", "yardmaster"))
	apiTakeBack = ask(greet.ID, store.Ask{Question: "Flowers for the newcomer?", To: "jo",
		Context: []string{"the greeting."}})

	// Jo is josephine at north, and the operator says so: the link trusts
	// that name as jo, and mail from it carries whom it speaks as. Last
	// again, for the seqs above.
	_, err = st.AllowPeer(ctx, "north", store.PeerAllow{Trust: map[string]string{"josephine": "jo"}}, "jo")
	must(err)
	said, err := json.Marshal(map[string]any{"to": "jo", "from": "josephine", "text": "I am at the other machine until four."})
	must(err)
	_, err = st.PeerReceive(ctx, "north", yardkey.Public(key), []store.PeerMessage{{ID: "m1", Kind: store.PeerMail, Body: said}})
	must(err)

	// And for jo's own address in a source that tells people, as for a
	// source's credential: one given here, one that GiveSecret answers in the
	// doc. Last again, for the seqs above.
	address := func(question, source string) uint64 {
		_, err := st.PutSource(ctx, source+".yaml", apiNotifySource(source), "jo")
		must(err)
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"It is on the first page of " + source + ".example.com after login."},
			Secret:  &store.AskSecret{Source: source, User: "jo"}})
	}
	_, err = st.GiveSecret(ctx, crm.ID, address("Your id at the pager", "pager"), map[string]string{"PAGER_USER": "typed-in-the-fixture"}, "jo")
	must(err)
	apiSecretAsks = [7]uint64{updates, token, address("Your id at the phone service", "phone"), 0, 0, 0, 0}

	// And what jo writes the yardmaster from there is in jo's conversation
	// with the yardmaster. Last again, for the seqs above.
	_, err = st.AllowPeer(ctx, "north", store.PeerAllow{Expose: []string{"yardmaster"}}, "jo")
	must(err)
	said, err = json.Marshal(map[string]any{"to": "yardmaster", "from": "josephine", "text": "And the fence after the shed."})
	must(err)
	_, err = st.PeerReceive(ctx, "north", yardkey.Public(key), []store.PeerMessage{{ID: "m2", Kind: store.PeerMail, Body: said}})
	must(err)

	// Run asks, each state the doc shows once: one the operator approved
	// here, at the yard, and one that waits. Last again, for the seqs above.
	more := func(n string) uint64 {
		return ask(crm.ID, store.Ask{Question: "Take more mail from north?", To: "jo",
			Context: []string{"north's yard was refused 4 mails in the last hour."},
			Run:     &store.AskRun{Argv: []string{"peer allow north --mail-limit " + n}}})
	}
	_, err = st.ApproveAsk(ctx, crm.ID, more("100"), "jo")
	must(err)
	apiRunAsk = more("300")
	// And of a row whose change not anyone takes back, the same two.
	often := func(n string) uint64 {
		return ask(crm.ID, store.Ask{Question: "May the CRM be called more often?", To: "jo",
			Context: []string{"The import was refused its 61st call of the hour."},
			Run:     &store.AskRun{Argv: []string{"source set crm calls_per_hour=" + n}}})
	}
	_, err = st.ApproveAsk(ctx, crm.ID, often("120"), "jo")
	must(err)
	often("240")

	// And for a waiting login's input, as for a source's credential: one
	// given here, one that GiveSecret answers in the doc. The waits live in
	// the server's memory: one is answered here, the other waits for the
	// doc's call. Last again, for the seqs above.
	login := func(question, ref string) uint64 {
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"The login waits for what the sign-in page showed."},
			Secret:  &store.AskSecret{Login: ref}})
	}
	waited := make(chan string, 1)
	go func() {
		value, err := st.LoginWait(context.Background(), "claude-example1")
		if err == nil {
			waited <- value
		}
	}()
	// The wait registers in its own goroutine: the giving lands once it
	// has, and fails loudly when it never does.
	given := login("The code the sign-in page showed", "claude-example1")
	deadline := time.Now().Add(10 * time.Second)
	for {
		_, err = st.GiveSecret(ctx, crm.ID, given, map[string]string{"INPUT": "typed-in-the-fixture"}, "jo")
		if err == nil {
			break
		}
		if !strings.Contains(err.Error(), "no login waits") || time.Now().After(deadline) {
			t.Fatalf("GiveSecret for the waited login: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := <-waited; got != "typed-in-the-fixture" {
		t.Fatalf("the waited login was handed %q", got)
	}
	docLogin := login("The code the second sign-in showed", "claude-example2")
	docCtx, docCancel := context.WithCancel(context.Background())
	t.Cleanup(docCancel)
	go func() { _, _ = st.LoginWait(docCtx, "claude-example2") }()
	apiSecretAsks[3] = docLogin

	// And for a crew member's own credential in a source, as for a
	// source's: one given here, one that GiveSecret answers in the doc.
	// Last again, for the seqs above.
	own := func(question, source string, env ...string) uint64 {
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"Create one at https://" + source + ".example.com/account/api (read only)."},
			Secret:  &store.AskSecret{Source: source, Env: env, Crew: "yardmaster"}})
	}
	_, err = st.GiveSecret(ctx, crm.ID, own("The token it reads with", "crm", "CRM_TOKEN"), map[string]string{"CRM_TOKEN": "typed-in-the-fixture"}, "jo")
	must(err)
	apiSecretAsks[4] = own("The token it updates with", "crm", "CRM_UPDATE_TOKEN")

	// And for a user's own credential in a source, as for a source's:
	// one given here, one that GiveSecret answers in the doc. Last
	// again, for the seqs above.
	theirs := func(question, source string, env ...string) uint64 {
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"Create one at https://" + source + ".example.com/account/api (read only)."},
			Secret:  &store.AskSecret{Source: source, Env: env, As: "jo"}})
	}
	_, err = st.GiveSecret(ctx, crm.ID, theirs("Your CRM token for updates", "crm", "CRM_UPDATE_TOKEN"), map[string]string{"CRM_UPDATE_TOKEN": "typed-in-the-fixture"}, "jo")
	must(err)
	apiSecretAsks[5] = theirs("Your CRM token", "crm", "CRM_TOKEN")

	// And for a name the source marks as no secret, so the queue and the
	// bead have concealed false to show, open and decided. Last again, for
	// the seqs above.
	_, err = st.PutSource(ctx, "gog.yaml", "name: gog\nbase: https://gog.example.com/api\n"+
		"credential:\n  env: GOG_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n"+
		"  mint:\n    - name: GOG_ACCOUNT\n      secret: false\n"+
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n", "jo")
	must(err)
	secret("The yard's account for gog", "gog", "GOG_ACCOUNT")
	_, err = st.GiveSecret(ctx, crm.ID, secret("The yard's other account for gog", "gog", "GOG_ACCOUNT"), map[string]string{"GOG_ACCOUNT": "typed-in-the-fixture"}, "jo")
	must(err)

	// The flows view: a depot with a store flow, routes and groups, holding
	// one bead on its first stage, and a depot whose flow file reads as no
	// flow, holding another off the line. Last again, for the seqs above;
	// the Arrivals rows of the two moved beads gain their lines.
	must(st.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"))
	flow, err := store.ParseFlow("new -> shipped\nshipped terminal\n")
	must(err)
	flow.Name = "default"
	_, err = st.PutFlow(ctx, *flow, "jo")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "default", "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 5}, "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "elders", Runner: "manual", Limit: 5, Members: []string{"jo"}}, "jo"))
	_, err = st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "jo")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "shipped", Group: "elders"}, "jo")
	must(err)
	online := "yard"
	_, err = st.UpdateBead(ctx, train.ID, store.BeadPatch{Depot: &online}, store.Expect{}, "jo")
	must(err)
	drafts := filepath.Join(t.TempDir(), "drafts")
	must(os.MkdirAll(filepath.Join(drafts, ".yardr", "flows"), 0o755))
	must(st.PutDepot(ctx, store.Depot{Name: "drafts", Path: drafts, Kind: store.DepotDir}, "jo"))
	offline := "drafts"
	_, err = st.UpdateBead(ctx, greet.ID, store.BeadPatch{Depot: &offline}, store.Expect{}, "jo")
	must(err)
	must(os.WriteFile(filepath.Join(drafts, ".yardr", "flows", "default.flow"), []byte("this is no flow\n"), 0o644))

	// A bead worked and closed, so the stream has a closed event with a
	// group and an originator to show. Last again, for the seqs above.
	swept, err := st.CreateBead(ctx, store.NewBead{Title: "Sweep the platform", ID: "yd-sweep"}, "yardmaster")
	must(err)
	_, err = st.Claim(ctx, swept.ID, "builders", "new", "yard")
	must(err)
	_, err = st.CloseBead(ctx, swept.ID, "done", store.Expect{}, "jo")
	must(err)
	// The pack a setup ask of the doc sets up: one minted source the yard
	// does not hold, with two presets over its calls. An existing matching
	// source gives the card a candidate for the API contract to show.
	courierMint := []byte("#!/bin/sh\nexit 1\n")
	courierMintHash := sha256.Sum256(courierMint)
	if _, err = st.PutSource(ctx, "courier-existing.yaml", apiSetupSource("courier-existing"), "jo"); err != nil {
		t.Fatal(err)
	}
	existingDir := filepath.Join(home, "sources", "courier-existing")
	if err := os.MkdirAll(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "courier-mint"), courierMint, 0o700); err != nil {
		t.Fatal(err)
	}
	existingAccount := filepath.Join(home, "sources", "courier-existing.env")
	if _, err = st.SetSource(ctx, "courier-existing", store.SourceEnvFile, existingAccount, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingAccount, []byte("COURIER_ACCOUNT=jo@example.test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	presets := func(source string) []store.AskPreset {
		return []store.AskPreset{
			{Name: "read", Grants: []store.AskPresetGrant{{Source: source, Calls: []string{"read"}}}},
			{Name: "all", Grants: []store.AskPresetGrant{{Source: source, Calls: []string{"read", "put"}}}},
		}
	}
	packs := map[string]*store.PackReading{"courier": {Name: "courier", Kind: "source", Label: "embedded in yardr doctest",
		Sources: []store.AskSource{{Name: "courier", Text: apiSetupSource("courier"), Files: []store.AskFile{{
			Program: "courier-mint", Text: string(courierMint), SHA256: hex.EncodeToString(courierMintHash[:]),
		}}}},
		Presets: presets("courier")}}
	// Two packs the yard applied before and the build has updated since,
	// so that their cards are the update's: each source stands in the yard
	// as it was, with the account file the yard keeps for it. ledger's file
	// holds every name the updated source needs, so its card reads given
	// and a bare pick keeps the account; parcel's lacks the name its update
	// call newly needs, so its card reads missing and the answer types it.
	ledger := apiSource("ledger", "LEDGER_TOKEN", "LEDGER_UPDATE_TOKEN")
	for _, u := range []struct{ name, was, now, account string }{
		{"ledger", ledger, strings.Replace(ledger, "/api", "/v2", 1), "LEDGER_TOKEN=held-by-the-fixture\nLEDGER_UPDATE_TOKEN=held-by-the-fixture\n"},
		{"parcel", apiSource("parcel", "PARCEL_TOKEN", ""), apiSource("parcel", "PARCEL_TOKEN", "PARCEL_UPDATE_TOKEN"), "PARCEL_TOKEN=held-by-the-fixture\n"},
	} {
		if _, err = st.PutSource(ctx, u.name+".yaml", u.was, "jo"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "sources", u.name+".env"), []byte(u.account), 0o600); err != nil {
			t.Fatal(err)
		}
		packs[u.name] = &store.PackReading{Name: u.name, Kind: "source", Label: "embedded in yardr doctest",
			Sources: []store.AskSource{{Name: u.name, Text: u.now}}, Presets: presets(u.name)}
	}
	st.SetPackReader(func(ctx context.Context, name string, yard store.API) (*store.PackReading, error) {
		if reading, ok := packs[name]; ok {
			return reading, nil
		}
		return nil, fmt.Errorf("pack %s: yardr embeds no pack of that name", name)
	})
	// The setup asks ApproveSetup approves in the doc, in the order of its
	// requests: a first setup with the account typed, one with a candidate
	// taken, an update that keeps the account that stands, and one that
	// types the name it is missing.
	setup := func(question, why, pack string, to ...store.Grantee) uint64 {
		seq, err := st.AddAsk(ctx, crm.ID, store.Ask{Question: question, To: "jo", Context: []string{why},
			Setup: &store.AskSetup{Pack: pack, To: to}}, "yardmaster")
		must(err)
		return seq
	}
	setupTo := []store.Grantee{{Group: "builders"}, {Crew: "yardmaster"}}
	apiSetupAsks[0], err = st.AddAsk(ctx, crm.ID, store.Ask{Question: "Set up the courier source?", To: "jo",
		Context: []string{"The nightly run pages through it."}, Recommend: 1, Why: "reading is the job",
		Setup: &store.AskSetup{Pack: "courier", To: setupTo}}, "yardmaster")
	must(err)
	apiSetupAsks[1] = setup("Set up the courier source for the night crew?", "The night run pages through it.", "courier", store.Grantee{Crew: "yardmaster"})
	apiSetupAsks[2] = setup("Update the ledger source?", "The build brings the ledger's new base.", "ledger", setupTo...)
	apiSetupAsks[3] = setup("Update the parcel source?", "The build gives the parcel's put call a token of its own.", "parcel", setupTo...)
	// Setups declined, a first setup and the two updates: the queue's
	// decided rows hold a setup too, of each kind of card.
	for _, d := range []struct{ question, why, pack string }{
		{"Set up the courier source for the elders too?", "They read the same pages.", "courier"},
		{"Update the ledger source for the elders too?", "They read the same pages.", "ledger"},
		{"Update the parcel source for the elders too?", "They read the same pages.", "parcel"},
	} {
		declined := setup(d.question, d.why, d.pack, store.Grantee{Group: "elders"}, store.Grantee{Crew: "yardmaster"})
		must(st.AddAnswer(ctx, crm.ID, store.Answer{Ask: declined, Decline: true, Words: "the builders' card covers it"}, "jo"))
	}

	// The flows view's worked beads: one on a stage, one off the line and
	// one with no line, each claimed with its agent, so every bead row's
	// worker and stage entry has something to say. Last, for the seqs
	// above, and after the groups it claims with.
	claim := func(id, agent string) {
		sess, err := st.Claim(ctx, id, "builders", "new", "yard")
		must(err)
		must(st.MarkRunning(ctx, sess.ID, map[string]any{"agent": agent}))
	}
	claim(train.ID, "train-new")
	claim(greet.ID, "greet-new")
	claim(links.ID, "links-new")

	// The held beads of the Arrivals rows and the flows view: one on a
	// stage, one off the line (it pins a flow of its own) and one with no
	// line, held by a person, a crew member and the yard, so every row's
	// held_by has something to say. A hold ends the session, so these are
	// not the worked beads above, whose workers the rows keep; the one on
	// a stage lights its stage's lamp. Last again, for the seqs above.
	held := func(nb store.NewBead, holder string) {
		nb.Stage = "new"
		_, err := st.CreateBead(ctx, nb, "yardmaster")
		must(err)
		_, err = st.SetHold(ctx, nb.ID, true, store.Expect{}, holder)
		must(err)
	}
	held(store.NewBead{Title: "Mend the gate", ID: "yd-gate", Depot: "yard"}, "jo")
	held(store.NewBead{Title: "Paint the fence", ID: "yd-fence", Depot: "yard", Metadata: map[string]any{"flow": "default"}}, "yardmaster")
	held(store.NewBead{Title: "Clear the siding", ID: "yd-siding"}, "yard")

	// A harness key stands in the queue, the bead and the GiveSecret
	// request: one ask stays open for the request, and one is given here so
	// the doc also shows its decided form. Keep them after the other fixture
	// writes, whose sequence numbers the doc names.
	harnessKey := func(question string) uint64 {
		return ask(crm.ID, store.Ask{Question: question, To: "jo",
			Context: []string{"Create an API key for Claude Code usage."},
			Secret:  &store.AskSecret{Harness: "claude", Crew: "yardmaster"}})
	}
	apiSecretAsks[6] = harnessKey("The API key Claude Code will use")
	_, err = st.GiveSecret(ctx, crm.ID, harnessKey("The API key Claude Code needs at first login"),
		map[string]string{"ANTHROPIC_API_KEY": "typed-in-the-fixture"}, "jo")
	must(err)

	// The operator's words to the agents, so that Settings has them to
	// show. Last again, for the seqs above.
	must(st.SetSetting(ctx, store.StyleSetting, "show", "jo"))
	must(st.SetSetting(ctx, store.NoteSetting, "I read diffs, not prose", "jo"))

	// A crew member whose session sits at its harness's login screen, the
	// yard leading the login: Crew, GetCrew and the stream each have the
	// two facts to show, and the stream the two changes that made them.
	// Last again, for the seqs above.
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "signalman", Runner: "herdr", Config: map[string]any{"kind": "claude", "model": "sonnet"}}, "jo")
	must(err)
	handle := map[string]any{"pane": "p2"}
	must(st.CrewStarted(ctx, "signalman", handle))
	must(st.CrewStatus(ctx, "signalman", "idle"))
	handle = map[string]any{"pane": "p2", store.HarnessLoginKey: "Select login method:"}
	must(st.SetCrewHandle(ctx, "signalman", handle))
	handle = map[string]any{"pane": "p2", store.HarnessLoginKey: "Select login method:",
		store.HarnessLoginAskKey: map[string]any{"journal": "signalman-journal", "ask": 1, "step": "pick", "kind": "claude"}}
	must(st.SetCrewHandle(ctx, "signalman", handle))
	return st, d.Token
}

// apiTakeBack is the ask TakeBackAsk takes back in the doc, by seq, as the
// fixture last made it.
var apiTakeBack uint64

// apiRunAsk is the run ask ApproveAsk approves in the doc, by seq, as the
// fixture last made it.
var apiRunAsk uint64

// apiSetupAsks are the four setup asks ApproveSetup approves in the doc, by
// seq, as the fixture last made them: a first setup whose account is typed,
// a first setup whose account is a candidate's, an update that keeps the
// account that stands, and an update that types the name it is missing.
var apiSetupAsks [4]uint64

// apiSecretAsks are the seven secret asks GiveSecret answers in the doc, by
// seq, as the fixture last made them: two for a source's credential, one
// for a user's own address in a source, one for a waiting login's input,
// one for a crew member's credential, one for a user's own credential in a
// source, and one for a harness key.
var apiSecretAsks [7]uint64

// apiNotifySource is the declaration of a source of the doc's yard that
// tells people: <NAME>_USER is a user's own address in it.
func apiNotifySource(name string) string {
	up := strings.ToUpper(name)
	return "name: " + name + "\nbase: https://" + name + ".example.com/api\n" +
		"credential: { env: " + up + "_TOKEN }\n" +
		"calls:\n  message:\n    kind: update\n    params: { user: { type: string }, text: { type: string } }\n" +
		"    request: { method: POST, path: /messages, body: { token: \"{credential}\", user: \"{user}\", message: \"{text}\" } }\n" +
		"notify: { call: message, address: { param: user, env: " + up + "_USER }, text: text }\n"
}

// apiSource is the declaration of a source of the doc's yard, with the
// names its credential has; update is "" for none.
func apiSource(name, env, update string) string {
	credential, put := "env: "+env, ""
	if update != "" {
		credential += ", update_env: " + update
		put = "  put: { kind: update, request: { method: POST, path: /it } }\n"
	}
	return "name: " + name + "\nbase: https://" + name + ".example.com/api\n" +
		"credential: { " + credential + ", header: \"Authorization: Bearer {credential}\" }\n" +
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" + put
}

// apiSetupSource is the minted source of the setup ask example: courier's
// account is its mint input, shared with the existing courier source.
func apiSetupSource(name string) string {
	return "name: " + name + "\nbase: https://" + name + ".example.com/api\n" +
		"credential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n" +
		"  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\n" +
		"calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" +
		"  put: { kind: update, request: { method: POST, path: /it } }\n"
}

// overNetwork is an HTTP client for the yard of st as jo's device has one:
// the same requests as overSocket's, sent under /user/ to the handler of
// the yard's listener with the device's token.
func overNetwork(t *testing.T, st *store.Store, token string) *http.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(UserPath, NewUserServer(st, func(context.Context) string { return "south" }, nil).Handler())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &http.Client{Transport: asDevice{srv.URL, token}}
}

// asDevice sends a request for the socket to the listener at base instead.
type asDevice struct{ base, token string }

func (d asDevice) RoundTrip(r *http.Request) (*http.Response, error) {
	u, err := url.Parse(d.base + strings.TrimSuffix(UserPath, "/") + r.URL.RequestURI())
	if err != nil {
		return nil, err
	}
	out := r.Clone(r.Context())
	out.URL, out.Host = u, ""
	out.Header.Set("Authorization", "Bearer "+d.token)
	return http.DefaultTransport.RoundTrip(out)
}

// overSocket is an HTTP client for the yard at sock, as a program that is
// not yardr has one: no rpc.Client, and no revision.
func overSocket(sock string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
}

// post makes one call with the body as given and returns the reply as the
// server wrote it.
func post(t *testing.T, hc *http.Client, method, body string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://yard/call/"+method, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(ClientHeader, "footplate/0.1")
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, body, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("%s %s: status %s, type %q", method, body, resp.Status, resp.Header.Get("Content-Type"))
	}
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// apiRun is what the yard answered: each call's requests with their
// replies, the events of the stream, and the refused requests.
type apiRun struct {
	requests map[string][]string
	replies  map[string][]any // the result of each request, parsed
	more     map[string][]any // what each reply says beside it as more, nil for nothing
	events   []any            // parsed, in the stream's order
	refused  []string         // the reply to each of apiErrors
}

// runAPI makes every call of apiCalls against a fresh apiYard, in order,
// then reads the stream from its beginning: through the socket, or with
// network through the listener's handler as jo's device.
func runAPI(t *testing.T, network bool) *apiRun {
	t.Helper()
	var hc *http.Client
	if network {
		st, token := apiStore(t)
		hc = overNetwork(t, st, token)
	} else {
		hc = overSocket(apiYard(t))
	}
	run := &apiRun{requests: map[string][]string{}, replies: map[string][]any{}, more: map[string][]any{}}
	calls := append(slices.Clone(apiCalls), apiOperatorCalls...)
	for _, c := range calls {
		for i, body := range c.requests {
			if c.method == "GiveSecret" {
				body = fmt.Sprintf(body, apiSecretAsks[i])
			} else if c.method == "ApproveAsk" {
				body = fmt.Sprintf(body, apiRunAsk)
			} else if c.method == "ApproveSetup" {
				body = fmt.Sprintf(body, apiSetupAsks[i])
			} else if c.method == "TakeBackAsk" {
				body = fmt.Sprintf(body, apiTakeBack)
			} else if c.method == "React" {
				// The first mail of the inbox read above.
				seqs, _ := at(run.replies["UserInbox"][0], "mail[].seq")
				body = fmt.Sprintf(body, mustInt(t, seqs[0]))
			} else if strings.Contains(body, "%d") {
				// An acknowledgement goes through the newest of what was read.
				from := map[string]string{"AckUserInbox": "UserInbox", "AckUserNotifications": "UserNotifications"}[c.method]
				last, _ := at(run.replies[from][0], "last")
				body = fmt.Sprintf(body, mustInt(t, last[0]))
			}
			var reply object
			raw := post(t, hc, c.method, body)
			reply, ok := parse(t, raw).(object)
			if !ok {
				t.Fatalf("%s %s: the reply is no object: %s", c.method, body, raw)
			}
			if e, ok := reply.get("error"); ok {
				t.Fatalf("%s %s: refused: %v", c.method, body, e)
			}
			result, _ := reply.get("result")
			more, _ := reply.get("more")
			run.requests[c.method] = append(run.requests[c.method], body)
			run.replies[c.method] = append(run.replies[c.method], result)
			run.more[c.method] = append(run.more[c.method], more)
		}
	}
	for _, e := range apiErrors {
		run.refused = append(run.refused, strings.TrimSpace(string(post(t, hc, e.method, e.request))))
	}

	// The stream, from the beginning: everything above is in it, and it
	// stays open, so it is read up to the newest event and no further.
	head := mustInt(t, parse(t, post(t, hc, "Head", `{}`)).(object)[0].val)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://yard/events?after=0", nil)
	resp, err := hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/x-ndjson" {
		t.Fatalf("GET /events: status %s, type %q", resp.Status, resp.Header.Get("Content-Type"))
	}
	sc := bufio.NewScanner(resp.Body)
	for want := int64(1); want <= head && sc.Scan(); want++ {
		e := parse(t, sc.Bytes())
		seq, _ := at(e, "seq")
		if len(seq) != 1 || mustInt(t, seq[0]) != want {
			t.Fatalf("the stream after 0: line %d is %s, want the event of seq %d: one event a line, in order, none left out", want, sc.Bytes(), want)
		}
		run.events = append(run.events, e)
	}
	if int64(len(run.events)) != head {
		t.Fatalf("the stream ended after %d of %d events: %v", len(run.events), head, sc.Err())
	}

	// A blob, by the name PutBlob answered.
	resp, err = hc.Get("http://yard/blobs/" + apiPNGHash)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	png, _ := io.ReadAll(resp.Body)
	if want, _ := base64.StdEncoding.DecodeString(apiPNG); resp.StatusCode != http.StatusOK ||
		resp.Header.Get("Content-Type") != "image/png" || !bytes.Equal(png, want) {
		t.Fatalf("GET /blobs/%s: status %s, type %q, %d bytes", apiPNGHash, resp.Status, resp.Header.Get("Content-Type"), len(png))
	}
	return run
}

// The calls a program that is not yardr may rely on are those of Calls and
// of OperatorCalls and no other, each a method of the store's API with as
// many arguments as it has names, and each in the doc's table with those
// names: Calls under "The calls", OperatorCalls under "For the operator
// alone".
func TestCallsAreTheDocs(t *testing.T) {
	t.Run("Calls", func(t *testing.T) { callsAreTheDocs(t, "Calls", Calls, apiCalls) })
	t.Run("OperatorCalls", func(t *testing.T) { callsAreTheDocs(t, "OperatorCalls", OperatorCalls, apiOperatorCalls) })
	// What the listener serves every device is Calls, as it was before
	// there was a second list, and a device of the operator the three calls
	// of OperatorCalls besides: a call is added to either on purpose, here
	// too.
	if len(Calls) != 25 || len(OperatorCalls) != 3 {
		t.Errorf("Calls has %d calls and OperatorCalls %d, want 25 and 3", len(Calls), len(OperatorCalls))
	}
	for name := range OperatorCalls {
		if _, both := Calls[name]; both {
			t.Errorf("%s is in Calls and in OperatorCalls: a call is served to every device or to the operator's", name)
		}
	}
}

func callsAreTheDocs(t *testing.T, list string, Calls map[string][]string, apiCalls []apiCall) {
	api := reflect.TypeOf((*store.API)(nil)).Elem()
	documented := map[string]bool{}
	for _, c := range apiCalls {
		documented[c.method] = true
		names, ok := Calls[c.method]
		if !ok {
			t.Errorf("%s is in the doc and not in %s: it cannot be called by name", c.method, list)
			continue
		}
		var args []string
		for _, a := range c.args {
			args = append(args, a.path)
		}
		if !slices.Equal(args, names) {
			t.Errorf("%s: the doc names its arguments %v, %s %v", c.method, args, list, names)
		}
		m, ok := api.MethodByName(c.method)
		if !ok {
			t.Errorf("%s is no method of store.API", c.method)
		} else if m.Type.NumIn()-1 != len(names) { // less the context
			t.Errorf("%s takes %d arguments and %s names %d", c.method, m.Type.NumIn()-1, list, len(names))
		}
	}
	for name := range Calls {
		if !documented[name] {
			t.Errorf("%s is in %s and not in the doc: a call by name is a promise, and docs/api.md is where it is made", name, list)
		}
	}
}

// Every call of the doc, made through the socket with the JSON the doc
// shows, answers every field the doc promises, with its type; and so does
// every event of the doc, read from the stream: on the socket, and under
// /user/ on the yard's listener for a device, which is the same page over
// TCP.
func TestAPIContract(t *testing.T) {
	t.Run("socket", func(t *testing.T) { checkContract(t, runAPI(t, false)) })
	t.Run("network", func(t *testing.T) { checkContract(t, runAPI(t, true)) })
}

func checkContract(t *testing.T, run *apiRun) {
	for _, c := range append(slices.Clone(apiCalls), apiOperatorCalls...) {
		checkFields(t, c.method, c.fields, run.replies[c.method])
		if len(c.fields) == 0 {
			for i, r := range run.replies[c.method] {
				if r != nil {
					t.Errorf("%s %s answers %v, and the doc says nothing", c.method, run.requests[c.method][i], r)
				}
			}
		}
	}
	for _, ev := range apiEvents {
		got := eventsOf(run, ev)
		if len(got) == 0 {
			t.Errorf("%s: no such event in the stream", ev.title)
			continue
		}
		checkFields(t, ev.title, ev.fields, got)
	}
}

func eventsOf(run *apiRun, ev apiEvent) []any {
	var out []any
	for _, tree := range run.events {
		var e store.Event
		if json.Unmarshal([]byte(render(tree, "")), &e) == nil && ev.is(e) {
			out = append(out, tree)
		}
	}
	return out
}

// An argument left out is the zero value of its type, as in every JSON
// yardr reads; yardr's own array of arguments is served as before; and an
// object is no way to call a method the doc does not name.
func TestCallByName(t *testing.T) {
	hc := overSocket(apiYard(t))
	for _, c := range []struct{ method, body, want string }{
		{"UserInbox", `{"name":"jo"}`, `"unread":2`},
		{"UserInbox", `["jo", 0]`, `"unread":2`},
		// The array of a yardr from before the call had its last argument.
		{"UserInbox", `["jo"]`, `"unread":2`},
		{"Mail", `["yardmaster", "as before", "jo"]`, `{}`},
		{"Mail", `["yardmaster"]`, `"message":"mail must be 1..4096 bytes"`},
		{"Sessions", `[]`, `"message":"want 1 arguments, got 0"`},
		{"UserInbox", ` {"name":"jo","limit":1} `, `"unread":2`},
		{"UserInbox", `{"name":"jo","limit":"ten"}`, `{"error":{"code":"error","message":"argument \"limit\": json: cannot unmarshal string into Go value of type int"}}`},
		{"UserInbox", `{"name":"jo","most":1,"all":true}`, `{"error":{"code":"error","message":"argument \"all\": UserInbox has none of that name (it takes name, limit)"}}`},
		{"Users", `{"name":"jo"}`, `{"error":{"code":"error","message":"argument \"name\": Users has none of that name (it takes none)"}}`},
		{"Sessions", `{"all":true}`, `{"error":{"code":"error","message":"arguments: Sessions takes them as a JSON array: only the calls docs/api.md names take them by name"}}`},
		{"Sessions", `[true]`, `{"result":`},
	} {
		if got := string(post(t, hc, c.method, c.body)); !strings.Contains(got, c.want) {
			t.Errorf("%s %s = %s, want %s", c.method, c.body, got, c.want)
		}
	}
	// A list added to the end of a call the doc does not name is one a
	// daemon from before leaves out: Claim's via. The call is made (and
	// refused for the bead, which is nobody's to claim), not counted short.
	for _, body := range []string{`["no-bead", "g", "new", "yard"]`, `["no-bead", "g", "new", "yard", [{"route":"r"}]]`} {
		if got := string(post(t, hc, "Claim", body)); strings.Contains(got, "arguments") || !strings.Contains(got, `"error"`) {
			t.Errorf("Claim %s = %s, want the claim refused for the bead, not for its arguments", body, got)
		}
	}
	if got := string(post(t, hc, "Claim", `["no-bead", "g", "new"]`)); !strings.Contains(got, "want 5 arguments, got 3") {
		t.Errorf("Claim with three arguments = %s, want it counted short", got)
	}
}

// docs/api.md shows each call with its arguments, its request, the reply
// and the promised fields, and each event likewise. Those sections are made
// from apiCalls and apiEvents and from what the yard of this test answered,
// and stand between the marker lines `<!-- <name>: ... -->` and
// `<!-- /<name> -->`. This fails when one is not what the test says;
// YARDR_UPDATE_GOLDEN=1 writes them again instead.
func TestAPIDoc(t *testing.T) {
	const file = "../../docs/api.md"
	run := runAPI(t, false)
	sections := map[string][]string{"calls": callsDoc(run, apiCalls), "local": callsDoc(run, apiOperatorCalls), "events": eventsDoc(run), "errors": errorsDoc(run)}
	body, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	var out []string
	found := map[string]bool{}
	stale := false
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		name, _, ok := strings.Cut(strings.TrimPrefix(lines[i], "<!-- "), ":")
		want := sections[name]
		if !ok || want == nil || !strings.HasPrefix(lines[i], "<!-- ") {
			continue
		}
		found[name] = true
		end := i + 1
		for end < len(lines) && lines[end] != "<!-- /"+name+" -->" {
			end++
		}
		if end == len(lines) {
			t.Fatalf("%s: no `<!-- /%s -->` after line %d", file, name, i+1)
		}
		if got := lines[i+1 : end]; !slices.Equal(got, want) {
			stale = true
			if os.Getenv("YARDR_UPDATE_GOLDEN") == "" {
				t.Errorf("%s: the section %q is not what the yard answers; write it again with\n"+
					"  YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc\ngot:\n%s\nwant:\n%s",
					file, name, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
		}
		out = append(out, want...)
		i = end - 1
	}
	for name := range sections {
		if !found[name] {
			t.Errorf("%s has no `<!-- %s: ... -->` marker: the section is gone", file, name)
		}
	}
	if stale && os.Getenv("YARDR_UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(file, []byte(strings.Join(out, "\n")), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fieldTable(head string, fields []field) []string {
	rows := []string{"| " + head + " | type | |", "|---|---|---|"}
	for _, f := range fields {
		path, typ := f.path, f.typ
		if path == "" {
			path = "(the result)"
		}
		if opt, ok := strings.CutSuffix(typ, "?"); ok {
			typ = opt + ", may be absent"
		}
		rows = append(rows, fmt.Sprintf("| `%s` | %s | %s |", path, typ, f.says))
	}
	return append(rows, "")
}

func fenced(lang string, text ...string) []string {
	out := []string{"```" + lang}
	for _, s := range text {
		out = append(out, strings.Split(s, "\n")...)
	}
	return append(out, "```", "")
}

// exampleHome stands for the yard's home in the doc, which is a temporary
// directory when the test runs: the file a secret was written to is under
// it, and the doc is the same on every run.
const exampleHome = "/home/jo/.yardr"

var homeSources = regexp.MustCompile(`/[^" ]*/(sources|users|crew)/`)

// exampleKey stands for the start of a peer's key in the doc, where a run
// ask's sentence names its peer by it: the key of the test's peer is made
// anew on every run.
const exampleKey = "key 6lHks9_z…"

var keyStart = regexp.MustCompile(`key [A-Za-z0-9_-]{8}…`)

func callsDoc(run *apiRun, calls []apiCall) []string {
	out := []string{""}
	for _, c := range calls {
		out = append(out, "### `"+c.method+"`", "", c.says, "")
		if len(c.args) > 0 {
			out = append(out, fieldTable("argument", c.args)...)
		}
		for i, body := range run.requests[c.method] {
			reply := object{}
			if r := run.replies[c.method][i]; r != nil {
				reply = object{{"result", prune(r, "", c.fields)}}
			}
			if more := run.more[c.method][i]; more != nil {
				reply = append(reply, member{"more", more})
			}
			out = append(out, fenced("", "POST /call/"+c.method, body, "", keyStart.ReplaceAllString(homeSources.ReplaceAllString(render(reply, ""), exampleHome+"/$1/"), exampleKey))...)
		}
		if len(c.fields) > 0 {
			out = append(out, fieldTable("field of the result", c.fields)...)
		}
		if c.after != "" {
			// One line a paragraph, as the page has them.
			for _, para := range strings.Split(c.after, "\n\n") {
				out = append(out, para, "")
			}
		}
	}
	return out
}

func eventsDoc(run *apiRun) []string {
	out := []string{""}
	for _, ev := range apiEvents {
		out = append(out, "### "+ev.title, "", ev.says, "")
		if got := eventsOf(run, ev); len(got) > 0 {
			out = append(out, fenced("json", render(prune(got[0], "", ev.fields), ""))...)
		}
		out = append(out, fieldTable("field", ev.fields)...)
	}
	return out
}

func errorsDoc(run *apiRun) []string {
	out := []string{""}
	for i, e := range apiErrors {
		out = append(out, e.says, "")
		out = append(out, fenced("", "POST /call/"+e.method, e.request, "", run.refused[i])...)
	}
	return out
}

// What follows reads JSON keeping the order of an object's members, so a
// reply is shown as the server wrote it.

type member struct {
	key string
	val any
}

// object is a JSON object; an array is []any, a number json.Number.
type object []member

func (o object) get(key string) (any, bool) {
	for _, m := range o {
		if m.key == key {
			return m.val, true
		}
	}
	return nil, false
}

func parse(t *testing.T, raw []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		t.Fatalf("no JSON: %v: %s", err, raw)
	}
	return v
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok {
	case json.Delim('{'):
		o := object{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return nil, err
			}
			val, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			o = append(o, member{key.(string), val})
		}
		_, err := dec.Token()
		return o, err
	case json.Delim('['):
		a := []any{}
		for dec.More() {
			val, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, val)
		}
		_, err := dec.Token()
		return a, err
	}
	return tok, nil
}

// render is v as indented JSON.
func render(v any, indent string) string {
	switch v := v.(type) {
	case object:
		if len(v) == 0 {
			return "{}"
		}
		var parts []string
		for _, m := range v {
			key, _ := json.Marshal(m.key)
			parts = append(parts, indent+"  "+string(key)+": "+render(m.val, indent+"  "))
		}
		return "{\n" + strings.Join(parts, ",\n") + "\n" + indent + "}"
	case []any:
		if len(v) == 0 {
			return "[]"
		}
		scalars := true
		var parts []string
		for _, e := range v {
			switch e.(type) {
			case object, []any:
				scalars = false
			}
			parts = append(parts, render(e, indent+"  "))
		}
		if scalars {
			return "[" + strings.Join(parts, ", ") + "]"
		}
		return "[\n" + indent + "  " + strings.Join(parts, ",\n"+indent+"  ") + "\n" + indent + "]"
	case json.Number:
		return v.String()
	}
	out, _ := json.Marshal(v)
	return string(out)
}

// exampleTime stands for every time in the doc: a time is when the test
// ran, and the doc is the same on every run.
const exampleTime = "2026-10-08T09:30:00Z"

// prune is v with the promised fields only: what a client must not read is
// not shown. prefix is where v is, as a field's path says it.
func prune(v any, prefix string, fields []field) any {
	promised := func(path string) (exact bool, typ string, deeper bool) {
		for _, f := range fields {
			if f.path == path {
				exact, typ = true, f.typ
			}
			if strings.HasPrefix(f.path, path+".") || strings.HasPrefix(f.path, path+"[]") {
				deeper = true
			}
		}
		return
	}
	switch v := v.(type) {
	case object:
		out := object{}
		for _, m := range v {
			path := m.key
			if prefix != "" {
				path = prefix + "." + m.key
			}
			exact, typ, deeper := promised(path)
			switch {
			case deeper:
				out = append(out, member{m.key, prune(m.val, path, fields)})
			case exact && strings.HasPrefix(typ, "time"):
				out = append(out, member{m.key, exampleTime})
			case exact:
				out = append(out, m)
			}
		}
		return out
	case []any:
		out := []any{}
		for _, e := range v {
			out = append(out, prune(e, prefix+"[]", fields))
		}
		return out
	}
	return v
}

// at are the values at path in v, and how many places that should have one
// have none (an object without the key).
func at(v any, path string) (vals []any, missing int) {
	if path == "" {
		return []any{v}, 0
	}
	if rest, ok := strings.CutPrefix(path, "[]"); ok {
		a, isArray := v.([]any)
		if !isArray {
			return nil, 1
		}
		for _, e := range a {
			got, m := at(e, strings.TrimPrefix(rest, "."))
			vals, missing = append(vals, got...), missing+m
		}
		return vals, missing
	}
	key, rest := path, ""
	if i := strings.IndexAny(path, ".["); i >= 0 {
		key, rest = path[:i], strings.TrimPrefix(path[i:], ".")
	}
	o, _ := v.(object)
	val, ok := o.get(key)
	if !ok {
		return nil, 1
	}
	return at(val, rest)
}

func mustInt(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%v is no number", v)
	}
	i, err := n.Int64()
	if err != nil {
		t.Fatal(err)
	}
	return i
}

// checkFields fails for every promised field that the answers do not have
// with its type. A field that may be absent must still be in one of them:
// the yard of the test is made so that each has something to say. One
// inside a field that may be absent is looked for where that field is.
func checkFields(t *testing.T, what string, fields []field, answers []any) {
	t.Helper()
	optional := func(path string) bool {
		for _, f := range fields {
			if strings.HasSuffix(f.typ, "?") && (strings.HasPrefix(path, f.path+".") || strings.HasPrefix(path, f.path+"[]")) {
				return true
			}
		}
		return false
	}
	for _, f := range fields {
		typ, may := strings.CutSuffix(f.typ, "?")
		var vals []any
		missing := 0
		for _, a := range answers {
			got, m := at(a, f.path)
			vals, missing = append(vals, got...), missing+m
		}
		if len(vals) == 0 {
			t.Errorf("%s: the field %q the doc promises is in no answer", what, f.path)
			continue
		}
		if missing > 0 && !may && !optional(f.path) {
			t.Errorf("%s: the field %q is absent in %d places, and the doc does not say it may be", what, f.path, missing)
		}
		for _, v := range vals {
			if !isType(v, typ) {
				t.Errorf("%s: the field %q is %s, and the doc says %s", what, f.path, render(v, ""), typ)
			}
		}
	}
}

func isType(v any, typ string) bool {
	switch typ {
	case "string":
		_, ok := v.(string)
		return ok
	case "time":
		s, ok := v.(string)
		_, err := time.Parse(time.RFC3339Nano, s)
		return ok && err == nil
	case "number":
		n, ok := v.(json.Number)
		if !ok {
			return false
		}
		_, err := n.Int64()
		return err == nil
	case "bool":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(object)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "array of string":
		a, ok := v.([]any)
		for _, e := range a {
			if _, isString := e.(string); !isString {
				return false
			}
		}
		return ok
	}
	return false
}
