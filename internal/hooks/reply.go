package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// The outbound side. yard never speaks for anyone: a crew member answers a
// delivery in its source with its own credential, and the CLI in its pane
// makes the call. The server is never asked to speak and never holds the
// credential; it only hands out the hook event that is being answered.

// Message is one message of a source's thread: a comment.
type Message struct {
	ID     string    `json:"id"`
	Parent string    `json:"parent,omitempty"` // the message it answers, in a threaded source
	Author Party     `json:"author"`
	At     time.Time `json:"at"`
	Body   string    `json:"body"`
	URL    string    `json:"url,omitempty"`
}

// Replier is what an Adapter whose source can be answered in adds.
type Replier interface {
	// Credential is the caller's credential for the source, read from the
	// calling process's environment (a crew member's env_file or env_command sets it).
	Credential(ctx context.Context) (string, error)
	// Reply posts body in the thread of the delivery env, under the
	// conversation of message to ("" for the delivery's own), marked with
	// key (a UUID v4 the store chose for this reply: the comment's own id
	// where the source takes one, a marker in the body elsewhere), and
	// returns what it posted: at least the source's id of it and its URL.
	Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error)
	// Posted finds the comment an earlier Reply with key left in the
	// thread of env, or nil when there is none: a retry asks before it
	// posts, so an attempt whose answer was lost is not posted twice.
	Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error)
	// Thread lists the messages of the delivery's thread, oldest first.
	Thread(ctx context.Context, env Envelope, cred string) ([]Message, error)
}

// ReplierFor is the source's Replier, if it can be answered in: the one
// built from d, the yard's declared source of that name, when it has
// the keys a reply takes, else the builtin one of that name. No name is
// both, so at most one applies. d is nil when the yard declares no such
// source. home is the yard's home, which a relative command program runs
// from; mint is the source's mint_command setting, "" when the yard set
// none.
func ReplierFor(source string, d *source.Declaration, home, mint string) (Replier, bool) {
	if dec, ok := declared(source, d, home, mint); ok {
		if dec.repliable() {
			return dec, true
		}
		return nil, false
	}
	if r, ok := Adapters[source].(Replier); ok {
		return r, true
	}
	return nil, false
}

// Repliable lists the sources that can be answered in, sorted: the builtin
// ones, and the yard's declared sources among decls with the keys a reply
// takes.
func Repliable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := ReplierFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := ReplierFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Filer is what a Replier whose source takes files on a reply adds.
type Filer interface {
	Replier
	// ReplyFiles posts body with the local files paths in the thread of
	// the delivery env, under the conversation of message to ("" for the
	// delivery's own), marked with key, and returns what it posted: at
	// least the source's id of it and its URL. The paths are files the
	// caller keeps, read by the call.
	ReplyFiles(ctx context.Context, env Envelope, to, body, key string, files []string, cred string) (Message, error)
}

// FilerFor is the source's Filer, if a reply there takes files: the one
// built from d, the yard's declared source of that name, when it has
// the keys a reply takes and says its replies take files, else the
// builtin one of that name. No name is both, so at most one applies. d
// is nil when the yard declares no such source. home is the yard's
// home, which a relative command program runs from; mint is the
// source's mint_command setting, "" when the yard set none.
func FilerFor(source string, d *source.Declaration, home, mint string) (Filer, bool) {
	if dec, ok := declared(source, d, home, mint); ok {
		if dec.fileable() {
			return dec, true
		}
		return nil, false
	}
	if f, ok := Adapters[source].(Filer); ok {
		return f, true
	}
	return nil, false
}

// Fileable lists the sources a reply takes files in, sorted: the builtin
// ones, and the yard's declared sources among decls with the keys a
// reply takes that say their replies take files.
func Fileable(decls ...*source.Declaration) []string {
	var out []string
	for _, s := range Sources() {
		if _, ok := FilerFor(s, nil, "", ""); ok {
			out = append(out, s)
		}
	}
	for _, d := range decls {
		if d == nil {
			continue
		}
		if _, ok := FilerFor(d.Name, d, "", ""); ok {
			out = append(out, d.Name)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// MissingCredential is what CredentialFrom fails with when nothing it was
// given sets the source's credential. Names says what would, for the caller
// to say where.
type MissingCredential struct{ Names string }

func (e *MissingCredential) Error() string { return e.Names + " is not set" }

// CredentialWay is one way a source's credential is given: the names that
// together set it in a crew member's env_file, and what kind of credential
// they make.
type CredentialWay struct {
	Kind  string   `json:"kind"`
	Names []string `json:"names"`
}

// credentialWays are the ways of each source, the one preferred first.
// credentialWays are the ways a builtin source's credential reads: no
// builtin is answered in, so none reads one. A declared source's ways
// come from its declaration (CredentialWays).
var credentialWays = map[string][]CredentialWay{}

// CredentialWays are the ways source's credential is given, the one used
// when several are set first. They are names only: what `yardr crew
// show` and a pack's plan read of an env_file. d is the yard's declared
// source of that name, used when the yard has it; a declared source has
// the one way, the variable its credential.env names, and where it names
// a mint, the minted way first.
func CredentialWays(source string, d *source.Declaration) []CredentialWay {
	if d != nil && d.Name == source && d.Credential != nil {
		if d.Credential.Minted() {
			return []CredentialWay{{Kind: "minted", Names: d.Credential.Mint}, {Kind: "API key", Names: []string{d.Credential.Env}}}
		}
		return []CredentialWay{{Kind: "API key", Names: []string{d.Credential.Env}}}
	}
	if w, ok := credentialWays[source]; ok {
		return w
	}
	return nil
}

// CredentialSet is the way of source that keys, the names an env_file
// sets, give in full: the one a call would use.
func CredentialSet(source string, keys []string, d *source.Declaration) (CredentialWay, bool) {
	for _, w := range CredentialWays(source, d) {
		if !slices.ContainsFunc(w.Names, func(n string) bool { return !slices.Contains(keys, n) }) {
			return w, true
		}
	}
	return CredentialWay{}, false
}

// CredentialNames says in words what an env_file sets for source's
// credential: "LINEAR_API_KEY", or for a minted one the App's two names
// or its token.
func CredentialNames(source string, d *source.Declaration) string {
	var ways []string
	for _, w := range CredentialWays(source, d) {
		ways = append(ways, strings.Join(w.Names, " and "))
	}
	return strings.Join(ways, ", or ")
}

// declaredCredential is the credential get sets for the declared source d:
// the static value, or the mint's inputs as a reference for the call to
// resolve with the caller's scope, by the precedence Select keeps. A name
// nothing sets fails with a *MissingCredential, for the caller to say
// where.
func declaredCredential(d *source.Declaration, get func(string) string) (string, error) {
	value, inputs, err := d.Credential.Select(d.Credential.Env, get)
	if err != nil {
		var missing *source.Missing
		if errors.As(err, &missing) {
			return "", &MissingCredential{Names: missing.Names}
		}
		return "", err
	}
	if inputs != nil {
		return source.MintRef(inputs), nil
	}
	return value, nil
}

func sortMessages(ms []Message) {
	slices.SortStableFunc(ms, func(a, b Message) int { return a.At.Compare(b.At) })
}

// Declared

// Credential is the caller's credential for the source: the mint's inputs
// where the calling process's environment sets every name, else the
// variable its credential.env names. The core hands the value to every
// thread command; the commands look nothing up themselves.
func (d Declared) Credential(context.Context) (string, error) {
	cred, err := declaredCredential(d.D, os.Getenv)
	if err != nil {
		var missing *MissingCredential
		if errors.As(err, &missing) {
			return "", fmt.Errorf("%s is not set in this environment: answering in a source takes your own credential (a crew member's env_file or env_command sets it)", missing.Names)
		}
	}
	return cred, err
}

// declaredThread is the thread of the delivery env: its thread's id, or why
// a reply has no comments to answer in.
func declaredThread(source string, env Envelope) (string, error) {
	if env.Thread.ID == "" || (env.Type == "comment" && env.Thread.ID == env.Message) {
		return "", fmt.Errorf("this %s %s is not on a thread, so it has no comments to answer in", source, env.Type)
	}
	return env.Thread.ID, nil
}

// Reply posts body in the thread of the delivery env, under the
// conversation of message to ("" for the delivery's own), by
// thread.reply: in {thread, parent, body, key}, out {id, url}. The parent
// is the root of the conversation, listed to find it where the envelope
// does not carry it; the key is the comment's own id where the source
// takes one, so a retry finds it with Posted.
func (d Declared) Reply(ctx context.Context, env Envelope, to, body, key, cred string) (Message, error) {
	return d.reply(ctx, env, to, body, key, nil, cred)
}

// ReplyFiles posts body with the local files paths in the thread of the
// delivery env, by thread.reply: in {thread, parent, body, key, files},
// out {id, url}. The command reads the files. Only a source whose file
// says its replies take files is asked (FilerFor): one that ignores
// unknown keys would send the words and drop the files.
func (d Declared) ReplyFiles(ctx context.Context, env Envelope, to, body, key string, files []string, cred string) (Message, error) {
	return d.reply(ctx, env, to, body, key, files, cred)
}

func (d Declared) reply(ctx context.Context, env Envelope, to, body, key string, files []string, cred string) (Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return Message{}, err
	}
	parent, err := d.replyParent(ctx, env, to, cred)
	if err != nil {
		return Message{}, err
	}
	in := map[string]any{"thread": thread, "parent": parent, "body": body, "key": key}
	if len(files) > 0 {
		in["files"] = files
	}
	var m Message
	err = d.runThread(ctx, "thread.reply", d.D.Thread.Reply, in, cred, func(out []byte) error {
		var o struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, url}: %v", err)
		}
		m = Message{ID: firstOf(o.ID, key), Body: body, URL: o.URL}
		return nil
	})
	if err != nil {
		return Message{}, err
	}
	return m, nil
}

// replyParent is the parent thread.reply takes for a reply to env, under
// the conversation of to ("" for the delivery's own): the root of the
// thread's conversation. A delivery that is not a comment gets a new
// top-level comment.
func (d Declared) replyParent(ctx context.Context, env Envelope, to, cred string) (string, error) {
	switch {
	case to != "":
		ms, err := d.Thread(ctx, env, cred)
		if err != nil {
			return "", err
		}
		for _, m := range ms {
			if m.ID == to {
				return firstOf(m.Parent, m.ID), nil
			}
		}
		return "", fmt.Errorf("%s message %s is not on the delivery's thread %s", d.D.Name, to, env.Thread.ID)
	case env.Type == "comment" && env.Message != "":
		return firstOf(env.Parent, env.Message), nil
	case env.Type == "comment":
		// A delivery recorded before envelopes carried the comment's id
		// still names it in its URL; without the id the reply would land
		// at the top level, outside the conversation it answers.
		ms, err := d.Thread(ctx, env, cred)
		if err != nil {
			return "", err
		}
		_, frag, _ := strings.Cut(env.URL, "#comment-")
		if frag == "" {
			return "", fmt.Errorf("this %s comment delivery names no comment (url %q): answer with --to <message-id> (ids from yardr thread show)", d.D.Name, env.URL)
		}
		var found []Message
		for _, m := range ms {
			if strings.HasPrefix(m.ID, frag) {
				found = append(found, m)
			}
		}
		if len(found) != 1 {
			return "", fmt.Errorf("%s comment %s: %d comments of the thread match, not one: answer with --to <message-id> (ids from yardr thread show)", d.D.Name, frag, len(found))
		}
		return firstOf(found[0].Parent, found[0].ID), nil
	default:
		return "", nil
	}
}

// Posted finds the comment an earlier Reply with key left in the thread of
// env, by thread.posted: in {thread, key}, out {id, url} or {} for none.
// A retry asks before it posts, so an attempt whose answer was lost is not
// posted twice.
func (d Declared) Posted(ctx context.Context, env Envelope, key, cred string) (*Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return nil, err
	}
	in := map[string]any{"thread": thread, "key": key}
	var found *Message
	err = d.runThread(ctx, "thread.posted", d.D.Thread.Posted, in, cred, func(out []byte) error {
		var o struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		}
		if err := json.Unmarshal(out, &o); err != nil {
			return fmt.Errorf("its answer is no {id, url}: %v", err)
		}
		if o.ID != "" {
			found = &Message{ID: o.ID, URL: o.URL}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// Thread lists the messages of the delivery's thread, oldest first, by
// thread.read: in {thread}, out {messages: [...]}, each a Message as it
// marshals.
func (d Declared) Thread(ctx context.Context, env Envelope, cred string) ([]Message, error) {
	thread, err := declaredThread(d.D.Name, env)
	if err != nil {
		return nil, err
	}
	var out []Message
	err = d.runThread(ctx, "thread.read", d.D.Thread.Read, map[string]any{"thread": thread}, cred, func(raw []byte) error {
		var o struct {
			Messages []Message `json:"messages"`
		}
		if err := json.Unmarshal(raw, &o); err != nil {
			return fmt.Errorf("its answer is no {messages: [...]}: %v", err)
		}
		out = o.Messages
		return nil
	})
	if err != nil {
		return nil, err
	}
	sortMessages(out)
	return out, nil
}
