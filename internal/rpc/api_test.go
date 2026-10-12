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
		{prefix + "via", "string?", "the [device](#over-the-network) the mail was written through, to be shown behind the sender, `jo (via phone)`; absent on mail written on the yard's own machine"},
		{prefix + "files", "array?", "the images the mail carries, in the order it was sent with them; absent on mail without any"},
		{prefix + "files[].hash", "string", "the image's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes"},
		{prefix + "files[].type", "string", "`image/png` or `image/jpeg`"},
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
		{prefix + "secret.source", "string", "the declared source the secret is the credential of"},
		{prefix + "secret.env", "array of string", "the names to ask the person a value for"},
		{prefix + "secret.line", "string", "what the card says is asked for, in the yard's words"},
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
		says:     "The yard's operator, when set, and its cap on an image. `yardr chat` reads the operator only to name them when the current name is not a user of the yard; a client can omit this call when it already knows who it is for and sends no image.",
		requests: []string{`{}`},
		fields: []field{
			{"operator", "string?", "the operator's user name; absent when the yard has not set one"},
			{"blob_max_bytes", "string?", "the most bytes `PutBlob` takes, a whole number in a string; absent when the yard has not set one, and the cap is then 8388608 (8 MiB)"},
		},
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
		after:    "Mail to a crew member that is not running is kept, and read when it next starts.",
	},
	{
		method:   "GetCrew",
		says:     "One crew member, as `Crew` lists it; the error `not-found` for a name the crew does not have.",
		args:     []field{{"name", "string", "the crew member"}},
		requests: []string{`{"name":"yardmaster"}`},
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
		},
		after: "A roster changes with no event of its own: read `Peers` again on any event whose kind begins `peer_`.",
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
		says:     "Gives the yard an image, so that mail can carry it: `Mail` then names it by the hash this answers. The bytes go in the JSON of the call, in base64. Only a PNG or a JPEG is taken, and which it is is read from the bytes themselves; anything else is refused, and so is one over the yard's cap (`blob_max_bytes` of `Settings`). The yard keeps an image once, under the SHA-256 of its bytes: putting the same bytes again answers the same and stores nothing new. An image no mail names is removed by `yardr store prune`, though not within an hour of being put, so put it when the mail is about to be sent, and put it again when that was longer ago.",
		args:     []field{{"data", "string", "the image's bytes, in base64 (the standard alphabet, padded)"}},
		requests: []string{`{"data":"` + apiPNG + `"}`},
		fields: []field{
			{"hash", "string", "the image's name: the SHA-256 of its bytes, in hex"},
			{"type", "string", "`image/png` or `image/jpeg`, as the bytes say"},
			{"bytes", "number", "how many bytes it is"},
		},
	},
	{
		method: "Mail",
		says:   "Sends mail to a crew member or a user of this yard. It answers nothing; the mail is then a `mail` event in the stream.",
		args: []field{
			{"to", "string", "the receiver's name"},
			{"text", "string", "the mail, 1 to 4096 bytes; it may be empty when the mail carries an image"},
			{"actor", "string", "who sends it: the user's name"},
			{"files", "array of string", "the images the mail carries, at most 8: the hash of each, as `PutBlob` answered it. Left out, the mail carries none. One the yard does not hold refuses the mail"},
		},
		requests: []string{`{"to":"yardmaster","text":"Thanks. Land it.","actor":"jo"}`,
			`{"to":"yardmaster","text":"Paint the fence like this.","actor":"jo","files":["` + apiPNGHash + `"]}`},
	},
	{
		method: "MailPeer",
		says:   "Queues mail to one of a linked yard, `<to>@<peer>`. It is refused when the link does not carry mail out. The answer is the message as it waits in the outbox for the peer.",
		args: []field{
			{"to", "string", "the receiver's name in the peer's yard"},
			{"peer", "string", "this yard's name for the link"},
			{"text", "string", "the mail"},
			{"actor", "string", "who sends it: the user's name"},
			{"files", "array of string", "the hashes of the images the mail carries, as for `Mail`; `text` may then be empty. Each crosses to the peer as a message of kind `file` before the mail. Refused, with nothing queued, when the link does not carry `file` out, when the peer's yardr has not said it understands `file`, and when an image is over `peer_file_max_bytes`"},
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
		method: "Conversation",
		says:   "The mail between two parties, both directions, oldest first: what `yardr mail --with <name>` prints. Asking again with the last seq seen costs what is new.",
		args: []field{
			{"a", "string", "one party: the user"},
			{"b", "string", "the other: a crew member, a user, or `<name>@<peer>` for one of a linked yard"},
			{"after", "number", "leave out what is not newer than this seq; `0` for all"},
		},
		requests: []string{`{"a":"jo","b":"yardmaster","after":0}`, `{"a":"jo","b":"clerk@north","after":0}`},
		fields: append(sentLetter("[]."),
			field{"[].delivery", "string?", "on mail to a linked yard only, whose reading of it this yard never learns of: `queued` while it waits here for the peer, `sent` once the peer has it, `failed` when it was given up"},
		),
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
		fields: append(append(queueRow("open[]."), secretAsked("open[].")...),
			append(append(queueRow("decided[]."), secretAsked("decided[].")...),
				field{"decided[].given", "array of string?", "the names a secret ask was given values for, which reads \"gave `<names>`\"; absent on any other ask"},
				field{"decided[].state", "string", "`answered`, `put aside` or `declined`"},
				field{"decided[].option", "number?", "the picked option's number; absent on a words answer, an aside ask and a declined one"},
				field{"decided[].option_label", "string?", "the picked option's label; absent with `option`"},
				field{"decided[].words", "string?", "what went with the pick, the words answer, or why the question is declined; for a question another yard asked that ended there, how it ended; absent when there are none"},
				field{"decided[].return_at", "time?", "when a put-aside ask comes back; absent otherwise"},
			)...,
		),
		after: "Read it again on every `noted` event: a question asked, answered, put aside or declined, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then. An answer to a question another yard asked that did not land arrives as `noted` by `yard`: the question is open again, or decided with how it ended there, and the person has mail from `yard` that says which.",
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
			{"asks[].state", "string", "`open`, `answered`, `put aside` or `declined`"},
			{"asks[].option", "number?", "the picked option's number; absent until one is picked"},
			{"asks[].words", "string?", "what went with the pick, the words answer, or why the question is declined; absent when there are none"},
			{"asks[].return_at", "time?", "when a put-aside ask comes back; absent otherwise"},
			{"asks[].picks_before", "number?", "how many picks there were before the current one, so a client can say \"changed\"; absent when there were none"},
			{"asks[].secret", "object?", "what a secret ask asks for; absent on any other ask. Such an ask has no options and takes no words: show it with a field that does not echo, and answer it with [`GiveSecret`](#on-the-socket-alone). `AnswerAsk` refuses a pick and words on it, and `DeclineAsk` words"},
			{"asks[].secret.source", "string", "the declared source the secret is the credential of"},
			{"asks[].secret.env", "array of string", "the names to ask the person a value for, each a variable of the source's credential"},
			{"asks[].secret.line", "string", "what the card says is asked for, in the yard's words and never the asker's: the names, the source, and where the source's calls go. Show it as it is"},
			{"asks[].given", "array of string?", "the names a secret ask was given values for, which reads \"gave `<names>`\"; absent on any other ask and until it is given"},
		},
		after: "Read it again on every event whose `bead` is the bead: `noted` and the bead's other events, whose kinds are yardr's own.",
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
		method: "AddNote",
		says:   "Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. On a `bead@peer`, a question another yard asked, it is refused: notes stay local, and words for the asker go as mail to `asker@peer`. It answers nothing; the note is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"text", "string", "the note"},
			{"actor", "string", "who writes it: the user's name"},
		},
		requests: []string{`{"bead":"yd-greet","text":"the newcomer arrives Monday","actor":"jo"}`},
	},
}

// apiLocalCalls are the calls of the doc that LocalCalls names: documented
// and held to the server as apiCalls are, and made through the socket
// alone, since the listener does not serve them.
var apiLocalCalls = []apiCall{
	{
		method: "GiveSecret",
		says:   "Answers a secret ask (an ask of `Bead` with `secret`): the values go into the file the source's credential is read from, and the answer on the bead holds the names and never a value. A value is kept in that file and nowhere else: not in the store, an event, a note or the log, and no call reads one back. It is what `yardr bead answer --secret` does. It is the operator's to give, and is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says. Refused too: an ask that is declined, names that are not exactly `asks[].secret.env`, an empty value, and one with a line break or white space at either end; a refusal never repeats a value. Giving again replaces what was given. The answer is then a `noted` event in the stream.",
		args: []field{
			{"bead", "string", "the bead"},
			{"ask", "number", "the ask note's seq"},
			{"values", "object", "each name of `asks[].secret.env` to the value the person typed"},
			{"actor", "string", "who gives: the operator's name"},
		},
		requests: []string{
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
			`{"bead":"yd-crm","ask":%d,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}`,
		},
		fields: []field{
			{"given", "array of string", "the names that were written"},
			{"file", "string", "the file they are in, on the yard's machine"},
			{"credential", "string?", "where every variable of the source's credential is set now, in words; absent while one is still missing"},
			{"missing", "string?", "what the credential still wants, in words; absent when it is whole"},
		},
		after: "The file and the store are two writes. When the yard wrote the file and could not record it, the error's message says so with the names and the file, and the ask is open still: give it again. When the call ends without a reply (the server went away), the value may be written: say so, never \"sent\", and give it again.",
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
		says:  "Mail was sent within the yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.",
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
			{"data.files", "array?", "the images the mail carries, as a letter's `files`; absent on mail without any"},
			{"data.files[].hash", "string", "the image's name: `GET /blobs/<hash>` answers the bytes"},
			{"data.files[].type", "string", "`image/png` or `image/jpeg`"},
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
		says:  "A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, a secret given, put aside, declined). A question another yard asked arrives as `noted` too, on `bead@peer`. It says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.",
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
	st, err := store.Open(filepath.Join(t.TempDir(), "yard.db"))
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
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo")
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
		Roster: []store.PeerRosterEntry{{Name: "clerk", Kind: store.RosterCrew, Role: "Keeps the books", Mail: true}}}))

	must(st.AddSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, "jo"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Paint the shed", ID: "yd-shed"}, "yardmaster")
	must(err)
	must(st.AddNote(ctx, b.ID, "primed", "yardmaster"))
	png, err := base64.StdEncoding.DecodeString(apiPNG)
	must(err)
	shed, err := st.PutBlob(ctx, png)
	must(err)
	must(st.SetSetting(ctx, store.BlobMaxSetting, "8388608", "jo"))
	must(st.Mail(ctx, "jo", "The shed is painted. Shall I land it?", "yardmaster", shed.Hash))

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
	_ = train
	must(st.AddNote(ctx, links.ID, "the test passed on a copy", "yardmaster"))
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
	apiSecretAsks = [2]uint64{secret("The CRM's token for updates", "crm", "CRM_UPDATE_TOKEN"), secret("The API key for the CRM", "crm", "CRM_TOKEN")}
	return st, d.Token
}

// apiSecretAsks are the two secret asks GiveSecret answers in the doc, by
// seq, as the fixture last made them.
var apiSecretAsks [2]uint64

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
	events   []any            // parsed, in the stream's order
	refused  []string         // the reply to each of apiErrors
	local    bool             // made through the socket: apiLocalCalls too
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
	run := &apiRun{requests: map[string][]string{}, replies: map[string][]any{}, local: !network}
	calls := apiCalls
	if run.local {
		calls = append(slices.Clone(calls), apiLocalCalls...)
	}
	for _, c := range calls {
		for i, body := range c.requests {
			if c.method == "GiveSecret" {
				body = fmt.Sprintf(body, apiSecretAsks[i])
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
			run.requests[c.method] = append(run.requests[c.method], body)
			run.replies[c.method] = append(run.replies[c.method], result)
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
// of LocalCalls and no other, each a method of the store's API with as
// many arguments as it has names, and each in the doc's table with those
// names: Calls under "The calls", LocalCalls under "On the socket alone".
func TestCallsAreTheDocs(t *testing.T) {
	t.Run("Calls", func(t *testing.T) { callsAreTheDocs(t, "Calls", Calls, apiCalls) })
	t.Run("LocalCalls", func(t *testing.T) { callsAreTheDocs(t, "LocalCalls", LocalCalls, apiLocalCalls) })
	// What the listener serves a device is Calls, as it was before there
	// was a second list: a call is added to it on purpose, here too.
	if len(Calls) != 21 {
		t.Errorf("Calls has %d calls, want 21", len(Calls))
	}
	for name := range LocalCalls {
		if _, both := Calls[name]; both {
			t.Errorf("%s is in Calls and in LocalCalls: a call is served to a device or it is not", name)
		}
	}
}

// A call of LocalCalls is served on the socket and on nothing else: the
// listener answers a device as it answers a method the yard does not have,
// before it reads the body.
func TestLocalCallsAreNotServedOverTheNetwork(t *testing.T) {
	st, token := apiStore(t)
	hc := overNetwork(t, st, token)
	for name := range LocalCalls {
		got := string(post(t, hc, name, `{"bead":"yd-crm","ask":1,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}`))
		if want := string(post(t, hc, "Sing", `{}`)); got != strings.ReplaceAll(want, "Sing", name) {
			t.Errorf("%s over the network = %s, want what a method the yard does not have answers: %s", name, got, want)
		}
	}
	q, err := st.UserAsks(context.Background(), "jo")
	if err != nil {
		t.Fatal(err)
	}
	open := 0
	for _, a := range q.Open {
		if a.Secret != nil {
			open++
		}
	}
	if open != 2 {
		t.Fatalf("%d secret asks are open, want the 2 of the fixture: one was given over the network", open)
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
	calls := apiCalls
	if run.local {
		calls = append(slices.Clone(calls), apiLocalCalls...)
	}
	for _, c := range calls {
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
		{"UserInbox", `{"name":"jo"}`, `"unread":1`},
		{"UserInbox", `["jo", 0]`, `"unread":1`},
		// The array of a yardr from before the call had its last argument.
		{"UserInbox", `["jo"]`, `"unread":1`},
		{"Mail", `["yardmaster", "as before", "jo"]`, `{}`},
		{"Mail", `["yardmaster"]`, `"message":"mail must be 1..4096 bytes"`},
		{"Sessions", `[]`, `"message":"want 1 arguments, got 0"`},
		{"UserInbox", ` {"name":"jo","limit":1} `, `"unread":1`},
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
	sections := map[string][]string{"calls": callsDoc(run, apiCalls), "local": callsDoc(run, apiLocalCalls), "events": eventsDoc(run), "errors": errorsDoc(run)}
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

var homeSources = regexp.MustCompile(`/[^" ]*/sources/`)

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
			out = append(out, fenced("", "POST /call/"+c.method, body, "", homeSources.ReplaceAllString(render(reply, ""), exampleHome+"/sources/"))...)
		}
		if len(c.fields) > 0 {
			out = append(out, fieldTable("field of the result", c.fields)...)
		}
		if c.after != "" {
			out = append(out, c.after, "")
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
