package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// readKeys reads the values yardr source key gives the credential of d,
// by name. typed reads one from the terminal without echo, and is nil when
// standard input is none: in is then read as NAME=value lines. names are
// the ones the command was given; with none, a terminal is asked for every
// variable of the credential and enter alone passes one over, and a pipe
// gives the ones its lines name. Which names the declaration has is the
// store's to check. No error holds a value.
//
// as is the user whose own credential in the source is given, "" for the
// source's: the prompt then says whose, and with no names a terminal is
// asked for the mint's inputs where the declaration names a mint, else the
// static ones, rather than every variable of the credential.
func readKeys(d *source.Declaration, names []string, in io.Reader, typed func(prompt string) (string, error), as string) (map[string]string, error) {
	values := map[string]string{}
	if typed == nil {
		env, err := config.ParseEnv(in, "standard input")
		if err != nil {
			return nil, err
		}
		for _, e := range env {
			k, v, _ := strings.Cut(e, "=")
			if len(names) > 0 && !slices.Contains(names, k) {
				// The input is intended to be NAME=value lines, but do not
				// repeat an unexpected name: a caller may have put the
				// secret there by mistake.
				return nil, fmt.Errorf("source %s: standard input sets a name that was not asked for (%s)", d.Name, strings.Join(names, ", "))
			}
			values[k] = v
		}
		for _, n := range names {
			if _, ok := values[n]; !ok {
				return nil, fmt.Errorf("source %s: standard input has no line %s=<value>", d.Name, n)
			}
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("source %s: standard input has no NAME=value line (its declaration names %s), and nothing is written", d.Name, d.Credential.Names())
		}
		return values, nil
	}
	ask, wanted := names, true
	if len(ask) == 0 {
		ask, wanted = d.Credential.Variables(), false
		if as != "" {
			ask = d.Credential.MintOrStatic()
		}
	}
	holder := "the source " + d.Name
	if as != "" {
		holder = as + "'s credential for the source " + d.Name
	}
	for _, n := range ask {
		prompt := fmt.Sprintf("%s for %s (nothing is shown as you type", n, holder)
		if !wanted && len(ask) > 1 {
			prompt += "; enter alone leaves it as it is"
		}
		v, err := typed(prompt + "): ")
		if err != nil {
			return nil, fmt.Errorf("source %s: reading %s: %w", d.Name, n, err)
		}
		// What a paste brings along at its ends is no part of a key.
		if v = strings.TrimSpace(v); v != "" {
			values[n] = v
		} else if wanted {
			return nil, fmt.Errorf("source %s: nothing was typed for %s, and nothing is written", d.Name, n)
		}
	}
	if len(values) == 0 {
		return nil, fmt.Errorf("source %s: nothing was typed, and nothing is written", d.Name)
	}
	return values, nil
}

// secretErr is the error of a giving as the person is told it: a call that
// ended without the yard's answer is the words every client says, never
// the transport's error, since on which side of the file's write it broke
// is not known.
func secretErr(err error) error {
	if errors.Is(err, store.ErrNoAnswer) {
		//lint:ignore ST1005 the words are sentences for the person, the same in every client and quoted in docs/chat.md, and are never wrapped
		return errors.New(store.SecretNoAnswer)
	}
	return err
}

// typedUnseen reads one value from the terminal in is, with nothing shown
// as it is typed; nil when in is no terminal, which readKeys then reads as
// NAME=value lines. The prompt goes to the command's error stream, so what
// the command prints stays the result alone.
func typedUnseen(in io.Reader, warn io.Writer) func(prompt string) (string, error) {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	return func(prompt string) (string, error) {
		fmt.Fprint(warn, prompt)
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(warn)
		return string(b), err
	}
}

func (a *app) sourceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Services outside the yard, each declared by one file",
		Long: `A declared source is a service outside the yard that was added by a file:
its address, the credential it takes and the requests the yard may make of
it, each with its parameters. No Go is written for one. What a file cannot
say (a request that must be signed, an answer in pages, anything that is no
JSON over HTTPS) is not a declared source.

  name: crm
  about: The customer system.
  base: https://crm.example.com/api/v2
  credential:
    env: CRM_TOKEN                                # the name, never the value
    header: "Authorization: Bearer {credential}"
  check: ping                                     # a read call without parameters
  calls:
    ping: { kind: read, request: { method: GET, path: /me } }
    note:
      kind: update
      about: Add a note to a customer
      params:
        id:   { type: string, pattern: "^[0-9]{1,12}$" }
        text: { type: string, max: 2000 }
      request: { method: POST, path: "/customers/{id}/notes", body: { text: "{text}" } }
      answer: { select: [id, created] }

A call is read or update. Its parameters are string (with pattern and max),
int, bool or enum (with values), all required unless optional: true. A
{param} in the path, the query or the body is replaced by the value, escaped
by where it stands. The body is sent as JSON, and a value that is exactly
{param} is sent as the parameter's type; one whose optional parameter was
not given is left out. The answer is JSON: select keeps the named top-level
keys, and it is at most 64 KB (answer.max raises that, to 1 MiB at most).

The file holds no secret. {credential} stands for the value of the variable
credential.env names, read at each call from the file the operator gives it
in (yardr source key <name>, which asks for the value and has the server
write the file) or names (yardr source set <name> env_file=<file>). It goes in the credential's header,
or as a whole value of a body for a service that takes it there, and never
in a path or a query. A service that takes it in the path says so with
in_path: true, and then {credential} may stand in a path, where it goes as
it is; a credential there is in the logs of every server on the way, and a
query refuses one either way. credential.update_env names a second variable
of that file, which the update calls are sent with instead, so that the read
calls run on a credential that may only read.

A credential that expires is minted at each call instead of kept: the file
names under credential.mint the variables that together select it, names
only, and the command that turns their values into a token is set on the
source (yardr source set <name> mint_command=<program> [args...]), run with
{inputs, scope} on stdin for a {token, expires_at}. The file may bring that
command itself, credential.mint_command, a program beside the file that the
yard copies and hashes as it does every command's: it mints where the
source's mint_command is unset, and one the operator sets wins over it. The
token is handed to the call under credential.env, and kept in memory alone.
An env that sets the mint's names is minted from; one of them alone is
refused; otherwise the variable is used as it is set.

  credential:
    env: CRM_TOKEN
    mint: [CRM_APP_ID, CRM_APP_KEY]
    mint_command: [crm-mint]          # optional: a file beside this one
    header: "Authorization: Bearer {credential}"

  credential:
    env: CRM_READ_TOKEN
    update_env: CRM_WRITE_TOKEN
    header: "Authorization: Bearer {credential}"
  idempotency: Idempotency-Key

A service whose address is its secret, a webhook URL such as Zapier, Make
and n8n give, says so with url: true. The credential is then that address:
the file names no base, and a call has no path of its own. The value is held
to what a base is when a call is made (https, or plain http to this machine;
no user, query or fragment), and no error, event or command repeats any of
it. An answer that is no JSON is the endpoint saying it took the call (Make
says Accepted), and is not kept.

  credential:
    env: WEBHOOK_URL
    url: true

idempotency names the header a service takes a key in, by which it tells an
update it was sent before from a new one. yardr call then sends each update
with a key, and the same key when the same caller makes the same call with
the same values again after one that did not end in a success: a call run
again after a timeout writes once. After a success the same call is a new
one. A key nobody tried again is kept for ` + store.SourceKeyKept.String() + `.

Refused when the file is added: a base that is not https:// (plain http://
only to this machine, for a stand-in), or one beside credential.url; a path
that is an address, has '..' or a query; a {param} the call does not
declare; a read call with PUT, PATCH or DELETE; a name yardr has built in
(` + strings.Join(source.BuiltIn, ", ") + `). When a call is made,
no redirect is followed, the call ends after ` + source.Timeout.String() + `, and an answer over its
bound is refused, not cut.

A source can tell a person outside the yard when its file has a notify key:
the update call that does it, and which of the call's parameters take what
the yard has to say.

  notify:
    call: message
    address: { param: user, env: PHONE_USER }  # where the person is reached
    title: title                                  # the yard's value: the call's parameter
    text: text
    link: link

address.env is the name of a variable in the env file of the user who is
told, not of the source's: the address is one person's. title, text and link
are each left out for a call that has no place for one; one of title and
text is there. Refused: a call that is not in the file or is a read call; a
parameter the call does not have, or one that is no string; a required
parameter of the call that nothing here fills; a link parameter that is
required. A title or a text longer than its parameter's max is cut to it; a
link that is too long is left out. A user is given the source with
yardr user set <name> notify=<source> env_file=<file>.

A source can say what the subscription of a harness kind still carries (the
windows of its usage limit, how far each is used, when it resets) when its
file has a limits key: a command, and the harness kinds it answers for.
Such a file needs no base and no calls, and with neither has no credential,
check or notify either.

  name: aiquokka
  about: Usage limits of the subscriptions the agents' CLIs are logged in to.
  limits:
    command: [/Users/me/go/bin/aiquokka, "{provider}", --json]
    kinds:              # harness kind: the provider's own name for it
      claude: claude
      kimi: kimi

With no kinds the source answers for itself instead: its command takes no
{provider} and is asked by the source's name, for the windows it reports.
contrib/machine-limits has one, whose command says the load, swap and disk
of this machine as windows (docs/integrations.md):

  name: machine
  limits:
    command: [machine/limits, --target, load=80, --target, swap=85, --target, disk=90]

The command is run by yardr itself, as the user yardr runs as: without a
shell (each word is one element of the list), its stdin closed, in the
yard's home, with {provider} replaced by the name the kind maps to, for at
most ` + limits.Timeout.String() + `, after which it and everything it started are killed. Name the
program by its whole path (the daemon's PATH need not be your shell's), or
as a file beside the declaration, which add copies into the yard. It
answers with exit status 0 and one JSON object on stdout, at most 64 KB:

  {"provider": "Kimi", "plan": "…", "windows": [
    {"label": "5h", "used_percent": 100, "target_percent": 60, "resets_at": "2026-10-07T00:02:32Z", "used": 100, "limit": 100}]}

provider and windows are required, and each window's label; used_percent is
a number, target_percent a number from 0 to 100 (the share of the window
that has passed), used and limit whole numbers, resets_at a time as RFC
3339 writes one, model and plan strings, each left out when the provider
does not know it. Other keys are passed over. A window is used up at
used_percent 100 or more, or when used has reached limit; against
target_percent a route's quota condition compares the pace instead of a
number (yardr route add --help). model is the model a window counts for
alone, where the provider caps one model beside what its models share
("Fable"), and is left out of a window they all draw from: a group with no
condition does not wait for a window that has one. Anything else (another
exit status, no answer in time, other output) is no answer: the limits are
then not known, and the reason is the last lines the command printed on
stderr.

Refused when the file is added: a command that is no list of strings, one
with no {provider} among its arguments where the file names kinds, or with
it in the program; a {provider} where the file names no kinds; a kind herdr
starts no agent of; a kind another source of the yard already answers for.
No pack brings a limits key: it is a command the yard runs, so it is the
operator's. What runs the command: yardr limits, yardr source check and
yardr doctor --try, each when a person asks, and the daemon when a harness
stopped on a usage limit, for the window and its reset in the
session_harness_error, once more at that reset before it prompts the agent,
and for a ready bead whose route puts a quota condition on a group or
names a group that starts agents (docs/groups.md). yardr limits --help says
more.

A file can also name the commands by which the yard speaks in the source's
threads, and how the source's issues are a bead's link:

  thread:
    reply:  [linear/thread, reply]      # in {thread, parent, body, key}  out {id, url}
    posted: [linear/thread, posted]     # in {thread, key}                out {id, url} or {}
    read:   [linear/thread, read]       # in {thread}                     out {messages: [...]}
    place:  team                        # what --in is called in this source
    open:   [linear/thread, open]       # in {in, title, body, key}       out {id, title, url}
    opened: [linear/thread, opened]     # in {in, key}                    out {id, title, url} or {}
    state:  [linear/thread, state]      # in {thread, state, forward?}    out {status, moved, was?}
    states: [started, review, done]     # the words thread.state takes
  link:
    host: linear.app
    issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'
    fixed: "https://linear.app/{1}/issue/{2}"   # templates over the
    thread: "{2}"                               # expression's groups

Each command is run as the limits command is: a list without a shell, one
JSON object on stdin, one on stdout, exit 0. posted and opened answer {}
for none: a second attempt asks first, so nothing is posted twice.
thread.state picks the source's status for the word and moves exactly:
only a thread already in the status is reported as not moved. A stage
map's move calls the same command with forward, which never moves a
thread backwards, and reports what a moved thread was in as was.
A program with no / at its start is a file beside the declaration's file:
source add copies it under ~/.yardr/sources/<name>/ (the relative path
kept, mode 0700) and the command runs from there, source show lists it with
its sha256, and source rm removes the directory; an absolute program is the
operator's own and is not copied. A command's environment is PATH, HOME,
USER, LOGNAME, the proxy and CA variables the process that runs it has
(HTTPS_PROXY, HTTP_PROXY, NO_PROXY, their lower-case forms, SSL_CERT_FILE,
SSL_CERT_DIR, NODE_EXTRA_CA_CERTS), the variable credential.env names and
YARDR_SOURCE_BASE, the declaration's base: nothing else of the yard's
environment. The core hands the command
the credential's value: the caller's own. Refused: a command that is no
list of strings or has no program, a thread.open without thread.place, a
thread.state without thread.states, a state that is none of started,
review, done, a link.issue that does not compile, a template naming a group
the expression has not. A pack for a depot brings no thread or link key;
a pack of kind source may, with the commands' files.
What runs the commands: yardr reply, yardr thread show, yardr thread open,
yardr thread state and yardr link, each with the calling process's
credential, and the daemon for a hook's stage map, with the crew member's.

A file can also say what the service sends in: a hook key makes the source
one a hook receives, as linear and github are, with the secret in the
hook's --secret-file verified as the file says.

  hook:
    verify:
      scheme: hmac-sha256
      signed: body
      signature: { header: Linear-Signature, encoding: hex }
      delivery:  { header: Linear-Delivery }
      timestamp: { body: webhookTimestamp, unit: ms, window: 1m }
    bodies: unique
    normalize: [linear/normalize]
    keys: [team, project, label]

  hook:
    verify:
      scheme: secret
      signature: { header: X-Service-Secret }
    bodies: unique
    normalize: [service/normalize]

verify is data, checked by the core: with scheme hmac-sha256 the
HMAC-SHA256 of what signed names (the word body for the raw body, or a
template over {timestamp}, {body} and {delivery}:
"v0:{timestamp}:{body}") with the hook's secret, in hex or base64, past
the prefix (prefix: "sha256=", or none). signature.list is the separator
of the several signatures a service may send in one header (list: " "),
any one of which may match; key says the secret writes the key, not the
key's own bytes (key: { prefix: "whsec_", encoding: base64 }). With
scheme secret the header holds the secret itself, compared as it
arrives. delivery is where the service's id of the delivery arrives: in
a header, or in the body at the dot path (delivery: { body: update_id });
with scheme secret it is left out for a service that sends none. After
the check comes the timestamp, in the body at the dot path or in a
header, in ms or s, within window of the yard's clock. The signing
secret reaches no command, and an unverified body reaches none. bodies
is unique (no two deliveries share a body), unique-per-hook (no two
deliveries to one hook share a body) or repeat (a body may come again).
normalize turns a verified body into the envelope: stdin {"headers":
{...}, "body": ...}, stdout the envelope, which the core refuses without
a type and sets the source and the delivery of itself. It runs as the
limits command does and is handed no credential. keys are what the
events' fields may carry, which a subscription's --where may then name
beside type, action, team, project, repo and label. Such a file needs no
base and no calls, and with neither has no credential, check or notify
either. Refused: a scheme that is neither hmac-sha256 nor secret,
signed, encoding, prefix, list or key with scheme secret, a signed
template with a missing or repeated {body} or {timestamp}, a repeated
{delivery}, an unknown placeholder, or {delivery} with no delivery in a
header, signed: body with the timestamp in a header, an encoding that is
neither hex nor base64, a verify key with no encoding, a delivery id in
a header and in the body, bodies that repeat with no delivery, a
timestamp in the body and in a header, or in neither, a unit that is not
ms or s, a window that is no positive duration, bodies that are none of
unique, unique-per-hook and repeat, a command that is no list of strings
or has no program, a key that is not lowercase letters, digits and _. A
pack for a depot brings no hook key; a pack of kind source may, with the
normalize file.

A source that tells people and receives can hear only its addresses
back: a hear key drops a delivery from anything but an address a user's
env file sets, after verification, whatever the user is told through.

  hear:
    field: chat
    address: { env: PHONE_CHAT }
    from: operator

fields[field] must hold the address a user's env file sets under
address.env, read as telling reads it; setting the address is the consent
to be heard, and telling is a separate choice. from narrows whom the
source hears to the operator, and is left out for every user whose env
file sets the address. A drop is
answered 200, recorded nowhere and counted beside refusals, so a
stranger's burst is one bounded entry; a delivery that matches is recorded
and routed as today. Refused: a hear without a hook key or a notify key; a
field that is no key of hook.keys; an address that is not the one notify
reads; a from that is not operator. A pack of kind source may bring the
key; a pack for a depot brings no hear key, as it brings no hook key.

A parameter marked log: true has its value kept in the record of each call
(the source_call event), which otherwise has the parameter's name alone: for
what an audit asks by, a customer's id, and never for free text.

Adding, setting and removing a source is the operator's, and is refused from
the pane of an agent: a source is where a credential of the yard is sent,
and its limits command is run by the yard. What calls a declared source is
yardr source check, the daemon for a source a user names as their notify (it
sends the user what landed in their inbox, yardr user set --help), and
yardr call: a call for a session or a crew member the operator granted it,
made by the yard's server.

  yardr source grant crm read --group builders --group reviewers   # every read call
  yardr source grant crm customer --crew yardmaster                # one read call
  yardr source grant crm note --group support                      # one update call
  yardr source grant crm read --crew yardmaster --as ada           # read, made with ada's credential
  yardr source grant google-mail --preset drafts --crew yardmaster # the preset's calls of the source
  yardr source revoke crm read --group reviewers

A grant is kept on its own, by source and grantee, and not in a group's
definition, which yardr group add replaces whole. read is every read call of
the source, one a later file adds included: yardr source add says who gets a
call that is new or changed. An update call is granted by its name and no
other way: there is no word for every update. --preset <name> grants one
preset's calls of the source instead, from the embedded pack that declares
it (yardr pack show <pack> prints a pack's). Granting plainly is the
operator's, and is refused from the pane of an agent whatever --actor says;
a grant --as <user> is that user's to give alone, and the operator is
refused it. Anyone may revoke, by the same words. Removing a source removes
its grants, plain and as a user alike.`,
	}

	add := &cobra.Command{
		Use:   "add <file>",
		Short: "Add the source a file declares, or replace the one of its name",
		Long: `Reads the declaration and keeps it in the store under the name the file
gives it. Everything wrong with the file is named at once, by line and key,
and nothing is kept. A source of that name is replaced and keeps its
env_file; what the new file changes of its calls is printed.`,
		Example: `  yardr source add crm.yaml
  yardr source key crm
  yardr source check crm`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			text, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			// Read here first, so a file that is refused is refused with
			// its lines whoever asks, before the store is asked who may.
			d, err := source.Parse(args[0], text)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// The files the declaration's commands name are the operator's
			// to add, as the declaration is: they are looked at before the
			// store is asked, and copied after it has answered.
			declDir := ""
			if d.HasCommandFiles() {
				abs, err := filepath.Abs(args[0])
				if err != nil {
					return err
				}
				declDir = filepath.Dir(abs)
				if err := d.CheckCommandFiles(declDir); err != nil {
					return err
				}
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			// Take the apply lock before reading the standing source:
			// its command files decide whether this add must replace the
			// copies directory. The server's carrying out and source rm
			// hold the same lock, so that decision cannot go stale while
			// this add waits (yardr-bht9 3.5).
			release, err := config.AcquireApplyLock(cfg.Home)
			if err != nil {
				return err
			}
			defer release()
			var before *source.Declaration
			if was, err := st.GetSource(c.Context(), d.Name); err == nil {
				before, _ = was.Declaration()
			}
			copies := d.HasCommandFiles() || (before != nil && before.HasCommandFiles())
			saved, err := st.PutSource(c.Context(), args[0], string(text), a.who())
			if err != nil {
				return err
			}
			if copies {
				// CopyCommandFiles makes the directory anew, so the files of
				// the declaration this one replaces are gone with it.
				if err := d.CopyCommandFiles(declDir, cfg.Home); err != nil {
					return fmt.Errorf("source %s saved, but its commands' files were not copied: %w", d.Name, err)
				}
			}
			changes := callChanges(before, d)
			grants, err := st.SourceGrants(c.Context(), d.Name)
			if err != nil {
				return err
			}
			if w := widened(before, d, grants); w != "" {
				changes = append(changes, w)
			}
			changes = append(changes, stillGranted(before, d, grants)...)
			return a.out(c.OutOrStdout(), map[string]any{"source": saved, "declaration": d, "changes": changes}, func(w io.Writer) {
				if len(d.Calls) == 0 {
					fmt.Fprintf(w, "source %s saved: no call, its limits command alone\n", d.Name)
				} else {
					fmt.Fprintf(w, "source %s saved: %s under %s\n", d.Name, plural(len(d.Calls), "call"), d.Address())
				}
				for _, l := range changes {
					fmt.Fprintln(w, "  "+l)
				}
				if d.Notify != nil {
					fmt.Fprintln(w, "  "+notifyLine(d))
				}
				if d.Limits != nil {
					fmt.Fprintln(w, "  "+limitsLine(d))
					asks := "it asks for each of those kinds"
					if d.Limits.Self() {
						asks = "it asks the source for itself"
					}
					fmt.Fprintf(w, "next: yardr source check %s (%s)\n", d.Name, asks)
				}
				var copied []string
				for _, f := range d.CommandFiles(cfg.Home) {
					if f.Declared && !slices.Contains(copied, f.Program) {
						copied = append(copied, f.Program)
					}
				}
				if len(copied) > 0 {
					fmt.Fprintf(w, "  the commands' files are under %s, each mode 0700: %s\n", filepath.Join(cfg.Home, "sources", d.Name), strings.Join(copied, ", "))
				}
				if d.Credential != nil && saved.EnvFile == "" {
					// Source add is the operator's, refused from an agent's pane,
					// so this names the operator's ways alone: the command that
					// writes the file first, then the file they keep themselves.
					fmt.Fprintf(w, "next: yardr source key %s gives %s, or yardr source set %s env_file=<file> names a file the operator keeps themselves\n", d.Name, d.Credential.Names(), d.Name)
				}
			})
		},
	}

	list := &cobra.Command{
		Use: "list", Short: "List the declared sources", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			srcs, err := st.Sources(c.Context())
			if err != nil {
				return err
			}
			users, err := st.Users(c.Context())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), srcs, func(w io.Writer) {
				rows := [][]string{}
				for _, src := range srcs {
					d, err := src.Declaration()
					if err != nil {
						rows = append(rows, []string{src.Name, "-", "not read by this yardr: yardr source show " + src.Name, "-", "-", "-", "-"})
						continue
					}
					names := make([]string, len(d.Calls))
					for i, c := range d.Calls {
						names[i] = c.Name + " (" + c.Kind + ")"
					}
					cred := "-"
					if d.Credential != nil {
						// What the file holds and what is minted, plainly: a
						// minted token is never in the file, so the static
						// name is not named as in it.
						if d.Credential.Minted() {
							cred = strings.Join(d.Credential.Mint, ", ") + " in " + or(src.EnvFile, "no env_file yet") + ", token minted per call"
						} else {
							cred = strings.Join(d.Credential.Envs(), ", ") + " in " + or(src.EnvFile, "no env_file yet")
						}
						if n := usersHolding(d, users); n == 1 {
							cred += " +1 user"
						} else if n > 1 {
							cred += fmt.Sprintf(" +%d users", n)
						}
					}
					notify := "-"
					if d.Notify != nil {
						notify = d.Notify.Call
					}
					kinds := "-"
					if d.Limits != nil {
						kinds = strings.Join(d.Limits.KindNames(), ", ")
						if d.Limits.Self() {
							kinds = "itself"
						}
					}
					rows = append(rows, []string{d.Name, or(d.Address(), "-"), or(strings.Join(names, ", "), "-"), cred, or(d.Check, "-"), notify, kinds})
				}
				table(w, "NAME\tBASE\tCALLS\tCREDENTIAL\tCHECK\tNOTIFY\tLIMITS", rows)
			})
		},
	}

	show := &cobra.Command{
		Use: "show <name>", Short: "Print a source: its calls, and whether its credential is set up", Args: cobra.ExactArgs(1),
		Long: `Prints what the source's file declares, and for its credential the name of
the variable and whether the env_file sets it, and for each user's own
credential in it whether their file sets that. The value is never printed.
--json has the file as it was added, under source.declaration.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			grants, err := st.SourceGrants(c.Context(), d.Name)
			if err != nil {
				return err
			}
			users, err := st.Users(c.Context())
			if err != nil {
				return err
			}
			told := slices.ContainsFunc(users, func(u store.User) bool { return u.Notify == d.Name })
			state, credErr := d.CredentialState(src.EnvFile, src.Mint)
			if d.Credential != nil && src.EnvFileUnset != "" {
				// A file where yardr source key writes one, which no
				// setting names: a giving that was written and not
				// recorded (store.KeyNotRecorded), or a file put there by
				// hand. No call reads it, and the next giving keeps its
				// other lines.
				state = fmt.Sprintf("no credential; %s exists and is not set: yardr source key %s, or remove it", tilde(src.EnvFileUnset), d.Name)
			} else if credErr != nil {
				// A file nobody needs is not a note to act on: with no
				// grant or notification the server makes no call of the
				// source, and a crew member holds the key its commands
				// run with. Only its check call wants the source's own
				// file then.
				if holder, names := pack.KeyHolder(c.Context(), st, d); src.EnvFile == "" && len(grants) == 0 && !told && holder != "" {
					state = fmt.Sprintf("none set: nobody is granted a call of %s, and crew member %s holds %s", d.Name, holder, strings.Join(names, " and "))
				} else {
					state = credErr.Error()
				}
			}
			home := ""
			if d.HasCommandFiles() {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				home = cfg.Home
			}
			files := map[string]source.CommandFile{}
			for _, f := range d.CommandFiles(home) {
				files[f.Key] = f
			}
			out := map[string]any{"source": src, "declaration": d, "credential": state, "grants": grants}
			var as map[string]string
			if d.Credential != nil {
				// Each user's own credential in the source, read for
				// whether the names are set, as the source's is, and
				// never a value; a user with no file, or a file without
				// the names, is said so with the card that gives one.
				as = make(map[string]string, len(users))
				for _, u := range users {
					astate, err := d.UserCredentialState(u.Name, u.EnvFile)
					if err != nil {
						astate = err.Error()
					}
					as[u.Name] = astate
				}
				out["as"] = as
			}
			return a.out(c.OutOrStdout(), out, func(w io.Writer) {
				fmt.Fprintf(w, "%s  %s\n", d.Name, or(d.Address(), "no calls"))
				if d.About != "" {
					fmt.Fprintf(w, "  %s\n", d.About)
				}
				fmt.Fprintf(w, "  credential: %s\n", state)
				if d.Credential.Minted() {
					fmt.Fprintln(w, "  "+mintLine(d, src, files[source.MintCommandKey]))
				}
				for _, u := range users {
					if astate, ok := as[u.Name]; ok {
						fmt.Fprintf(w, "  credential as %s: %s\n", u.Name, astate)
					}
				}
				switch {
				case d.Check != "":
					fmt.Fprintf(w, "  check: the call %s (yardr source check %s)\n", d.Check, d.Name)
				case len(d.Calls) > 0:
					fmt.Fprintln(w, "  check: the file names no call for it")
				}
				for _, call := range d.Calls {
					line := fmt.Sprintf("  call %s  %s  %s", call.Name, call.Kind, call.Request.Line())
					if call.About != "" {
						line += "  " + call.About
					}
					fmt.Fprintln(w, line)
					for _, p := range call.Params {
						fmt.Fprintln(w, "    "+paramLine(p))
					}
					if len(call.Build) > 0 {
						fmt.Fprintf(w, "    build: %s\n", strings.Join(call.Build, " "))
						fmt.Fprintln(w, "    "+commandFileLine(files["call."+call.Name+".build"]))
					}
					if len(call.Answer.Select) > 0 {
						fmt.Fprintf(w, "    answers %s\n", strings.Join(call.Answer.Select, ", "))
					}
					if who := grantedCall(grants, &call); len(who) > 0 {
						fmt.Fprintf(w, "    may be called by %s\n", strings.Join(who, ", "))
					}
				}
				if len(d.Calls) > 0 {
					fmt.Fprintf(w, "  calls: %d an hour for each caller, %d of them updates at most\n", src.CallLimit(), src.UpdateLimit())
				}
				if d.Idempotency != "" {
					fmt.Fprintf(w, "  updates: each is sent with a key in the header %s, the same when it is tried again\n", d.Idempotency)
				}
				if d.Notify != nil {
					fmt.Fprintln(w, "  "+notifyLine(d))
					if len(d.Notify.Image) > 0 {
						fmt.Fprintf(w, "  command notify.image: %s\n", strings.Join(d.Notify.Image, " "))
						fmt.Fprintln(w, "    "+commandFileLine(files["notify.image"]))
					}
				} else {
					fmt.Fprintln(w, "  notify: the file has no notify key, so no user is told through it")
				}
				if d.Limits != nil {
					fmt.Fprintln(w, "  "+limitsLine(d))
					fmt.Fprintf(w, "    command: %s\n", strings.Join(d.Limits.Command, " "))
					if f, ok := files["limits.command"]; ok {
						fmt.Fprintln(w, "    "+commandFileLine(f))
					}
					for _, kind := range d.Limits.KindNames() {
						fmt.Fprintf(w, "    kind %s is asked as %s\n", kind, d.Limits.Kinds[kind])
					}
				}
				if d.Thread != nil {
					for _, cmd := range d.Commands() {
						if !strings.HasPrefix(cmd.Key, "thread.") {
							continue
						}
						fmt.Fprintf(w, "  command %s: %s\n", cmd.Key, strings.Join(cmd.Argv, " "))
						fmt.Fprintln(w, "    "+commandFileLine(files[cmd.Key]))
					}
					if d.Thread.Files {
						fmt.Fprintln(w, "  thread.files: replies take files (yardr reply --file)")
					}
					if d.Thread.Place != "" {
						fmt.Fprintf(w, "  thread.place: %s\n", d.Thread.Place)
					}
					if len(d.Thread.States) > 0 {
						fmt.Fprintf(w, "  thread.states: %s\n", strings.Join(d.Thread.States, ", "))
					}
					if d.Thread.Labels {
						fmt.Fprintf(w, "  thread.labels: label:<name> adds one, never removes\n")
					}
					if d.Thread.Assignee != "" {
						fmt.Fprintf(w, "  thread.assignee: open takes one, by a person's %s there\n", d.Thread.Assignee)
					}
				}
				if d.Link != nil {
					fmt.Fprintf(w, "  link: issues of %s (%s)\n", d.Link.Host, d.Link.Issue)
					fmt.Fprintf(w, "    fixed: %s\n", d.Link.Fixed)
					fmt.Fprintf(w, "    thread: %s\n", d.Link.Thread)
					if d.Link.Fold != "" {
						fmt.Fprintf(w, "    fold: %s\n", d.Link.Fold)
					}
					if d.Link.Pull != nil {
						fmt.Fprintf(w, "    pull: %s (%s)\n", d.Link.Pull.Fixed, d.Link.Pull.Issue)
						fmt.Fprintf(w, "    pull thread: %s\n", d.Link.Pull.Thread)
					}
					if d.Link.Repos != "" {
						fmt.Fprintf(w, "    repos: %s\n", d.Link.Repos)
					}
				}
				if d.Identity != nil {
					fmt.Fprintf(w, "  identity: keys fold %s\n", d.Identity.Fold)
				}
				if d.Hook != nil {
					fmt.Fprintf(w, "  command hook.normalize: %s\n", strings.Join(d.Hook.Normalize, " "))
					fmt.Fprintln(w, "    "+commandFileLine(files["hook.normalize"]))
					fmt.Fprintln(w, "  "+verifyLine(d))
					fmt.Fprintf(w, "  hook.bodies: %s\n", d.Hook.Bodies)
					if len(d.Hook.Keys) > 0 {
						fmt.Fprintf(w, "  hook.keys: %s\n", strings.Join(d.Hook.Keys, ", "))
					}
				}
				if d.Hear != nil {
					fmt.Fprintln(w, "  "+hearLine(d))
				}
				if d.HearsByIdentity() {
					fmt.Fprintln(w, "  "+ignoredLine(d.Name, src.Ignored))
				}
				if d.Events != nil {
					for _, cmd := range d.Commands() {
						if !strings.HasPrefix(cmd.Key, "events.") {
							continue
						}
						fmt.Fprintf(w, "  command %s: %s\n", cmd.Key, strings.Join(cmd.Argv, " "))
						fmt.Fprintln(w, "    "+commandFileLine(files[cmd.Key]))
					}
					if len(d.Events.Runs) > 0 {
						fmt.Fprintf(w, "  events.runs: %s\n", strings.Join(d.Events.Runs, ", "))
					}
					if len(d.Events.HeadFrom) > 0 {
						fmt.Fprintf(w, "  events.head_from: %s\n", strings.Join(d.Events.HeadFrom, ", "))
					}
				}
				fmt.Fprintf(w, "  added %s by %s\n", since(src.AddedAt), src.AddedBy)
			})
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Short: "Remove a source and the files of its commands (the file its credential is in stays)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// The apply lock (config.ApplyLock) before the source is
			// read and its command files decided on, held through the
			// delete and the files' removal: a remove that read
			// files=false before a carrying out gave the source
			// command files would delete the source and leave the
			// copies the carrying out wrote (yardr-bht9 3.5). The
			// carrying out holds the same lock, as do a terminal pack
			// apply and source add.
			cfg, err := a.config()
			if err != nil {
				return err
			}
			release, err := config.AcquireApplyLock(cfg.Home)
			if err != nil {
				return err
			}
			defer release()
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			left, err := sourceRmReferences(c.Context(), st, args[0])
			if err != nil {
				return err
			}
			// The files the commands were copied from go with the source.
			files := false
			if d, err := src.Declaration(); err == nil {
				files = d.HasCommandFiles()
			}
			if err := st.DeleteSource(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			if files {
				if err := source.RemoveCommandFiles(cfg.Home, args[0]); err != nil {
					return err
				}
			}
			return a.out(c.OutOrStdout(), map[string]any{
				"source": args[0], "rm": "ok",
				"subscriptions": left.subscriptions, "participations": left.participations,
				"remove_subscriptions": left.unsubscribe, "remove_participations": left.unfollow,
			}, func(w io.Writer) {
				fmt.Fprintf(w, "%s: removed; %s and %s still name this source.\n", args[0], countPhrase(left.subscriptions, "subscription", "subscriptions"), countPhrase(left.participations, "participation", "participations"))
				if len(left.unsubscribe) > 0 {
					fmt.Fprintln(w, "  Remove subscriptions with:")
					for _, command := range left.unsubscribe {
						fmt.Fprintf(w, "    %s\n", printable(command))
					}
				}
				if len(left.unfollow) > 0 {
					fmt.Fprintln(w, "  Remove participations with:")
					for _, command := range left.unfollow {
						fmt.Fprintf(w, "    %s\n", printable(command))
					}
				}
			})
		},
	}

	ignore := &cobra.Command{
		Use: "ignore <source>", Short: "List a source's ignored senders, or stop ignoring one", Args: cobra.ExactArgs(1),
		Long: `Lists the sender ids of a source that hears by identity which the
operator answered nobody for on their mapping cards: a delivery from one
is dropped without a further card under any hook word. With --rm <id> it
removes one entry, so the sender's next message follows the hook's word
again: under unknown a fresh mapping card is offered, under allow it is
heard as an outsider. Mapping the sender clears their entry the same way.

Adding to the list happens only by picking nobody on a mapping card,
never here. Listing is anyone's; removing is the operator's, and is
refused from an agent's pane. A crew member that needs an entry removed
proposes the command with a run ask. Refused for a source with no
identity hearing, which ignores nothing.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			if !d.HearsByIdentity() {
				return fmt.Errorf("source %s hears no sender by identity, so nothing of it is ignored", args[0])
			}
			if rm, _ := c.Flags().GetString("rm"); c.Flags().Changed("rm") {
				if rm == "" {
					return fmt.Errorf("give the ignored id to remove: yardr source ignore %s --rm <id>", args[0])
				}
				if err := st.UnignoreSource(c.Context(), args[0], rm, a.who()); err != nil {
					return err
				}
				src, err = st.GetSource(c.Context(), args[0])
				if err != nil {
					return err
				}
				ignored := sortedIgnored(src.Ignored)
				return a.out(c.OutOrStdout(), map[string]any{"source": args[0], "ignored": ignored}, func(w io.Writer) {
					fmt.Fprintf(w, "%s: %s is no longer ignored\n", args[0], rm)
				})
			}
			ignored := sortedIgnored(src.Ignored)
			return a.out(c.OutOrStdout(), map[string]any{"source": args[0], "ignored": ignored}, func(w io.Writer) {
				if len(ignored) == 0 {
					fmt.Fprintf(w, "%s: no sender is ignored\n", args[0])
					return
				}
				for _, id := range ignored {
					fmt.Fprintln(w, id)
				}
			})
		},
	}
	ignore.Flags().String("rm", "", "stop ignoring this sender id")

	set := &cobra.Command{
		Use:   "set <name> env_file=<file> | mint_command=<program> [args...] | calls_per_hour=<n> | updates_per_hour=<n>",
		Short: "Change a source's credential or per-hour call limits",
		Long: `env_file is a file of KEY=VALUE lines that sets the variable the source's
declaration names (credential.env, and credential.update_env where it names
one). It is held to the rule of every env_file:
a regular file of mode 0600 or stricter, read at each call, so rotating the
credential is rewriting the file. The store keeps its path, never what is in
it. A file that is open to others or does not set the name is refused here.
env_file= with nothing after it takes the setting away.

mint_command is the command that turns the mint's inputs into a short-lived
token, where the declaration names a mint (credential.mint): one line, split
on spaces without a shell (quote it for arguments), the program absolute or
on PATH. Name the program by its whole path: the daemon's PATH need not be
your shell's. It is run without a shell, with {inputs, scope} on stdin, and
answers {token, expires_at}; its environment is PATH, HOME, USER, LOGNAME,
the proxy and CA variables the process that runs it has, and
YARDR_SOURCE_BASE, and nothing else of the yard's. The store keeps the line
to be shown, as a crew member's env_command is, so it holds no secret. A
program that is not there is refused here, as is a declaration that names no
mint. A declaration may bring a command of its own (credential.mint_command),
which mints while this is unset: this setting wins over it, and a pack
applied again keeps it. mint_command= with nothing after it takes the
setting away, and the declaration's own mints again.

calls_per_hour is how many calls of the source one session or crew member
makes with yardr call within an hour (` + fmt.Sprint(store.SourceCallLimit) + ` unless set); the call after is
refused until the hour that began with the first has passed.
calls_per_hour= with nothing after it is ` + fmt.Sprint(store.SourceCallLimit) + ` again.

updates_per_hour is how many of those calls are updates (` + fmt.Sprint(store.SourceUpdateLimit) + ` unless set):
an update is counted as a call and as an update, and is refused when either
is used up. updates_per_hour= with nothing after it is ` + fmt.Sprint(store.SourceUpdateLimit) + ` again.`,
		Example: `  umask 077
  IFS= read -rs v && printf 'CRM_TOKEN=%s\n' "$v" > ~/.yardr/sources/crm.env; unset v
  chmod 600 ~/.yardr/sources/crm.env
  yardr source set crm env_file=~/.yardr/sources/crm.env
  yardr source set crm mint_command=/Users/me/bin/crm-mint
  yardr source set crm calls_per_hour=20
  yardr source set crm updates_per_hour=3`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			key, value, ok := strings.Cut(args[1], "=")
			if !ok || !slices.Contains(store.SourceSettings, key) {
				return fmt.Errorf("%q: want %s=<file>, %s=<program> [args...], %s=<n> or %s=<n>, the settings a source has", args[1], store.SourceEnvFile, store.SourceMintCommand, store.SourceCallsPerHour, store.SourceUpdatesPerHour)
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			state := "no " + key
			if key == store.SourceEnvFile && value != "" {
				d, err := src.Declaration()
				if err != nil {
					return err
				}
				if d.Credential == nil {
					return fmt.Errorf("source %s: its declaration names no credential, so there is nothing an %s would set", d.Name, store.SourceEnvFile)
				}
				if value, err = config.ExpandPath(value); err != nil {
					return err
				}
				state, err = d.CredentialState(value, src.Mint)
				if err != nil {
					var noCommand *source.NoMintCommand
					if !errors.As(err, &noCommand) {
						return err
					}
					// The file is set before the command: it is
					// validated for the names, and the command is the
					// next step.
					state += "; " + err.Error()
				}
			} else if key == store.SourceMintCommand && value != "" {
				d, err := src.Declaration()
				if err != nil {
					return err
				}
				if !d.Credential.Minted() {
					return fmt.Errorf("source %s: its declaration names no mint (credential.mint), so there is nothing a %s would mint", d.Name, store.SourceMintCommand)
				}
				argv := strings.Fields(value)
				if len(argv) == 0 {
					return fmt.Errorf("source %s: %s=%q: want a program, absolute or on PATH", src.Name, key, value)
				}
				if prog := argv[0]; strings.ContainsRune(prog, filepath.Separator) && !filepath.IsAbs(prog) {
					return fmt.Errorf("source %s: %s=%q: the program is %s: want an absolute path, or a bare name on PATH", src.Name, key, value, prog)
				}
				if err := (config.Command{What: "mint_command " + value, Argv: argv}).Missing(); err != nil {
					return fmt.Errorf("source %s: %w", src.Name, err)
				}
				state = "mint command " + value
			}
			saved, err := st.SetSource(c.Context(), args[0], key, value, a.who())
			if err != nil {
				return err
			}
			if key != store.SourceEnvFile && key != store.SourceMintCommand {
				state = saved.LimitsLine()
			}
			return a.out(c.OutOrStdout(), saved, func(w io.Writer) { fmt.Fprintf(w, "source %s: %s\n", saved.Name, state) })
		},
	}

	var keyAs string
	key := &cobra.Command{
		Use:   "key <name> [NAME...]",
		Short: "Give a source's credential: typed without echo, or NAME=value lines on standard input",
		Long: `Writes the value of each variable the source's declaration names for its
credential (credential.env, credential.update_env, the inputs under
credential.mint) into the file the credential is read from, so that nobody
writes that file by hand. The file is the source's env_file, or with none
set ~/.yardr/sources/<name>.env, which is then set as yardr source set
sets it. The yard's server writes it: the line of each name given is
replaced or added, every other line and comment is kept, and the file is
replaced whole, by a rename, with mode 0600. A file that is open to others,
or is a symbolic link, is refused and not repaired.

On a terminal each value is asked for and nothing is shown as it is typed.
Without a NAME every variable of the credential is asked for, and enter
alone leaves one as it is; with NAMEs those are asked for, and each wants a
value. When standard input is no terminal it is read as NAME=value lines, as
an env file's are, which is how a script gives one; with NAMEs the lines
set exactly those. A value is never an argument and never a variable of the
command's environment: every process of the same OS user can read both.

The value is kept in that file and nowhere else: not in the store, an event
or the log, and no command prints it. What is printed is where the
credential is set now, as yardr source show says it. Refused: a name the
declaration does not have, an empty value, one with a line break in it or
white space at either end. It is the operator's, and is refused from the
pane of an agent, as yardr source set is.

With --as <user> it is that user's own credential in the source that is
given, which a call made as them reads: the value goes into the user's
env_file, or with none set into ~/.yardr/users/<user>.env, which is then
set as yardr user set sets it, and every other line is kept. Without a
NAME every name under credential.mint is asked for where the declaration
names a mint, else the static ones, and enter alone leaves one as it is;
with NAMEs those are asked for, and each wants a value. The user gives
their own, or the operator does it for them; it is refused from the pane
of an agent, and for a user the yard does not have. The source needs no
notify key. What is printed names the check that tries it as them.

The file and the store are two writes. When the yard wrote the file and
could not record it, it says so, with the names and the file: give it
again, and it ends as one giving that worked.`,
		Example: `  yardr source key crm                      # asks for each variable of its credential
  yardr source key crm CRM_UPDATE_TOKEN     # one of them
  yardr source key crm --as ada             # ada's own credential in it
  op read op://yard/crm/token | sed 's/^/CRM_TOKEN=/' | yardr source key crm`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			if d.Credential == nil {
				return fmt.Errorf("source %s: its declaration names no credential, so there is nothing to give it", d.Name)
			}
			// Refused before anything is typed.
			if keyAs != "" {
				if _, err := st.GetUser(c.Context(), keyAs); err != nil {
					return err
				}
			}
			for _, n := range args[1:] {
				if !slices.Contains(d.Credential.Variables(), n) {
					return fmt.Errorf("source %s: %s is no variable of its credential (its declaration names %s)", d.Name, n, d.Credential.Names())
				}
			}
			values, err := readKeys(d, args[1:], c.InOrStdin(), typedUnseen(c.InOrStdin(), c.ErrOrStderr()), keyAs)
			if err != nil {
				return err
			}
			if keyAs != "" {
				keyed, err := st.KeyUserCredential(c.Context(), keyAs, d.Name, values, a.who())
				if err != nil {
					return secretErr(err)
				}
				return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
					fmt.Fprintf(w, "%s gave %s · it is set in %s · %s\n", keyed.As, strings.Join(keyed.Given, " and "), keyed.File, keyed.Next)
				})
			}
			keyed, err := st.KeySource(c.Context(), d.Name, values, a.who())
			if err != nil {
				return secretErr(err)
			}
			return a.out(c.OutOrStdout(), keyed, func(w io.Writer) {
				if keyed.Missing == "" {
					fmt.Fprintf(w, "source %s: %s\n", d.Name, keyed.State)
					return
				}
				state := keyed.Missing
				if keyed.State != "" {
					state = keyed.State + "; " + state
				}
				fmt.Fprintf(w, "source %s: %s written to %s; %s\n", d.Name, strings.Join(keyed.Given, " and "), keyed.File, state)
			})
		},
	}
	key.Flags().StringVar(&keyAs, "as", "", "give this user's own credential in the source instead of the source's, kept in the user's env_file and read by a call made --as <user>")

	var groups, crew []string
	// grantees are the groups and crew members the flags name.
	grantees := func() ([]store.Grantee, error) {
		var to []store.Grantee
		for _, g := range groups {
			to = append(to, store.Grantee{Group: g})
		}
		for _, m := range crew {
			to = append(to, store.Grantee{Crew: m})
		}
		if len(to) == 0 {
			return nil, errors.New("want --group <group> or --crew <crew member>, who the grant is for")
		}
		return to, nil
	}
	var grantAs, grantPreset string
	grant := &cobra.Command{
		Use:   "grant <name> read|<call>|--preset <preset> --group <group> | --crew <crew member> [--as <user>]",
		Short: "Let a group's sessions or a crew member make calls of a source",
		Long: `read is every read call of the source, one a later file adds included; a
call's name is that one call. An update call is granted by its name alone:
read does not cover it, and there is no word for every update. Both flags
repeat. A plain grant is the operator's to give, and is refused from the
pane of an agent whatever --actor says.

With --preset <name> the source alone is named, and the preset's calls
of it are granted, each as if by name: the preset of the embedded pack
that declares the source (yardr pack show <pack> prints a pack's). An
unknown preset, a source no embedded pack declares, and two packs
declaring one source are refused, to grant by name instead. A grantee
who comes later gets the same preset on one card, as a run ask.

With --as <user> the call is made with that user's credential, from their
env_file: a grant of it is that user's to give alone, and the operator is
refused it, at a terminal as on a card. It covers no plain call, and a
plain grant no call as them.

A session whose bead came from another yard makes no call, whatever its
group is granted.`,
		Example: `  yardr source grant crm read --group builders --group reviewers
  yardr source grant crm customer --crew yardmaster
  yardr source grant crm note --group support
  yardr source grant crm read --crew yardmaster --as ada
  yardr source grant google-mail --preset drafts --crew yardmaster`,
		Args: func(cmd *cobra.Command, args []string) error {
			if grantPreset != "" {
				if len(args) != 1 {
					return errors.New("want the source alone with --preset <name>: the preset names the calls (yardr source grant <source> --preset <name> --group <group> | --crew <crew member>)")
				}
				return nil
			}
			return cobra.ExactArgs(2)(cmd, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			to, err := grantees()
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if grantPreset != "" {
				// Expanded from the embedded pack, as pack show
				// reads it: the one pack of kind source declaring
				// the source. Each call is granted as if by name,
				// through the same check.
				packName, calls, err := pack.PresetOfSource(packs.FS, args[0], grantPreset)
				if err != nil {
					return err
				}
				var saved []*store.SourceGrant
				for _, g := range to {
					var s *store.SourceGrant
					for _, call := range calls {
						s, err = st.GrantSource(c.Context(), args[0], call, g, grantAs, a.who())
						if err != nil {
							return fmt.Errorf("preset %s of pack %s: %w", grantPreset, packName, err)
						}
					}
					saved = append(saved, s)
				}
				words := grantWords(c.Context(), st)
				return a.out(c.OutOrStdout(), saved, func(w io.Writer) {
					for _, s := range saved {
						fmt.Fprintf(w, "%s may call %s: %s\n", s.Holder(), s.Source, words(*s))
					}
				})
			}
			var saved []*store.SourceGrant
			for _, g := range to {
				s, err := st.GrantSource(c.Context(), args[0], args[1], g, grantAs, a.who())
				if err != nil {
					return err
				}
				saved = append(saved, s)
			}
			words := grantWords(c.Context(), st)
			return a.out(c.OutOrStdout(), saved, func(w io.Writer) {
				for _, s := range saved {
					fmt.Fprintf(w, "%s may call %s: %s\n", s.Holder(), s.Source, words(*s))
				}
			})
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke <name> read|<call> --group <group> | --crew <crew member> [--as <user>]",
		Short: "Take a grant of a source's calls back",
		Long: `Takes back what yardr source grant gave, by the same words: read, or a
call's name, and --as <user> where the grant was given as that user. A
grantee that has read and a call by name keeps the one that is not named
here, and a plain grant and one as a user are taken back apart. Anyone
may revoke.`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			to, err := grantees()
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			var left []*store.SourceGrant
			for _, g := range to {
				s, err := st.RevokeSource(c.Context(), args[0], args[1], g, grantAs, a.who())
				if err != nil {
					return err
				}
				left = append(left, s)
			}
			words := grantWords(c.Context(), st)
			return a.out(c.OutOrStdout(), left, func(w io.Writer) {
				for _, s := range left {
					fmt.Fprintf(w, "%s may call %s: %s\n", s.Holder(), args[0], or(words(*s), "nothing"))
				}
			})
		},
	}
	for _, cmd := range []*cobra.Command{grant, revoke} {
		cmd.Flags().StringArrayVar(&groups, "group", nil, "a group: each session it starts (repeatable)")
		cmd.Flags().StringArrayVar(&crew, "crew", nil, "a crew member (repeatable)")
		cmd.Flags().StringVar(&grantAs, "as", "", "make the call with this user's credential, from their env_file; a grant of it is theirs alone to give")
	}
	grant.Flags().StringVar(&grantPreset, "preset", "", "grant the preset's calls of the source, from the embedded pack that declares it (yardr pack show <pack> prints a pack's)")

	var checkUser, checkAs string
	check := &cobra.Command{
		Use:   "check <name>",
		Short: "See that a source's credential is set up, make its check call, and ask its limits command",
		Long: `Reads the source's env_file as a call would, and makes the one call its
declaration names as check: a read call without parameters. Prints the
status the service answered with, and fails on anything but a success.
Where the check call names answer.select, a successful check also prints
each selected top-level field the answer carries, one per line under the
status line (and carries the same object under answer with --json); a
field the answer does not carry prints no line. A
source whose file names no check call is sent nothing: its credential is
looked at and that is all. Where the file names a mint (credential.mint),
the token is minted first, with the source's mint_command, and the line says
what it was minted from and how long it is good.

With --as <user> it looks at that user's credential instead, the names a
call as them reads from their env file, and makes the check call as them,
saying so. It is the operator's, as check is.

A source that says limits (a limits key) has its command run once for each
harness kind its file lists, one after the other, and what each answered is
printed as yardr limits prints it. It fails when a kind got no answer, so a
kind whose harness is not logged in on this machine is better left out of
the file. A source that answers for itself has its command run once, for
the source. Each run may take up to ` + limits.Timeout.String() + `.

Of every other command the file names (thread's), check says whether its
program is there and can be run, and fails when one cannot; it runs none.

With --user it tells that user through the source instead: one real
message, a line that says it is a test, sent as everything the yard tells
them will be, with the address their env_file sets. It is the user's own to
ask for, or the operator's, and works before the user names the source as
their notify, to try it first. A crew member that needs one sent proposes
it with a run ask. A source yardr has built in is checked this
way too, once one can tell a person.

The call is made by this command, with the credential read into this
process for it. Nothing of the request is written anywhere.`,
		Example: `  yardr source check crm
  yardr source check crm --as ada
  yardr source check phone --user mckean`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if checkUser != "" && checkAs != "" {
				return errors.New("--user tells through the source, and --as reads a user's credential: one of the two")
			}
			if checkUser != "" {
				return a.checkTells(c.OutOrStdout(), c.Context(), st, args[0], checkUser)
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			// With --as the credential looked at and read is the user's,
			// from the file their record names, and the check call is made
			// as them.
			stateOf := func() (string, error) { return d.CredentialState(src.EnvFile, src.Mint) }
			readOf := func(ctx context.Context) (string, string, error) {
				return d.ReadCredential(ctx, src.EnvFile, src.Mint, source.Read)
			}
			saidAs := ""
			if checkAs != "" {
				u, err := st.GetUser(c.Context(), checkAs)
				if err != nil {
					return err
				}
				stateOf = func() (string, error) { return d.UserCredentialState(u.Name, u.EnvFile) }
				readOf = func(ctx context.Context) (string, string, error) {
					return d.ReadUserCredential(ctx, u.Name, u.EnvFile, src.Mint, source.Read)
				}
				saidAs = " as " + u.Name
			}
			// The check is a read call, and what is looked at is every
			// variable the file names, the one of its updates too. A mint
			// is made only for the check call below.
			state, err := stateOf()
			if err != nil {
				return fmt.Errorf("source %s: %w", d.Name, err)
			}
			out := map[string]any{"source": d.Name, "credential": state, "sent": false}
			if checkAs != "" {
				out["as"] = checkAs
			}
			// said is the part on the calls, for a person; the commands and
			// limits follow it.
			said := func(io.Writer) {}
			switch {
			case len(d.Calls) == 0:
			case d.Check == "":
				said = func(w io.Writer) {
					fmt.Fprintf(w, "%s: credential: %s\n", d.Name, state)
					fmt.Fprintf(w, "%s: its file names no check call (check: <a read call without parameters>), so nothing was sent%s\n", d.Name, saidAs)
				}
			default:
				cred, mstate, err := readOf(c.Context())
				if err != nil {
					return fmt.Errorf("source %s: %w", d.Name, err)
				}
				state, out["credential"] = mstate, mstate
				res, err := source.Do(c.Context(), d, d.Check, nil, cred)
				if err != nil {
					return err
				}
				call, _ := d.Call(d.Check)
				out["sent"], out["call"], out["status"], out["ms"], out["bytes"] = true, d.Check, res.Status, res.Took.Milliseconds(), res.Bytes
				if !res.OK() {
					return fmt.Errorf("source %s: the check call %s (%s)%s was answered %d%s", d.Name, d.Check, call.Request.Line(), saidAs, res.Status, statusHint(res.Status, d))
				}
				// The check call's selection is what setup may read: a
				// successful check shows the selected top-level fields the
				// answer carries, one per line under the status line, and
				// carries the same selected answer under answer with --json.
				// A selected field the answer does not carry prints no line;
				// the check succeeds or fails on the status alone, as without
				// a selection, and no other call's answer is ever shown.
				var selected []string
				if len(call.Answer.Select) > 0 && len(res.Answer) > 0 {
					var anyJSON any
					if json.Unmarshal(res.Answer, &anyJSON) == nil {
						out["answer"] = anyJSON
					}
					var obj map[string]json.RawMessage
					if json.Unmarshal(res.Answer, &obj) == nil {
						for _, name := range call.Answer.Select {
							if raw, ok := obj[name]; ok {
								selected = append(selected, name+": "+checkField(raw))
							}
						}
					}
				}
				said = func(w io.Writer) {
					fmt.Fprintf(w, "%s: credential: %s\n", d.Name, state)
					fmt.Fprintf(w, "%s: check call %s (%s)%s: %d in %dms, %d bytes\n", d.Name, d.Check, call.Request.Line(), saidAs, res.Status, res.Took.Milliseconds(), res.Bytes)
					for _, line := range selected {
						fmt.Fprintf(w, "%s: %s\n", d.Name, line)
					}
				}
			}
			// Of each command the file names, whether its program is there
			// and can be run. None is run: the limits command is run below,
			// per kind, as it is today.
			var cmds []commandCheck
			cmdsOK := true
			var home string
			if len(d.Commands()) > 0 {
				cfg, err := a.config()
				if err != nil {
					return err
				}
				home = cfg.Home
				cmds, cmdsOK = checkCommands(d, home)
				out["commands"] = cmds
			}
			sayCmds := func(w io.Writer) {
				for _, cmd := range cmds {
					if cmd.Error != "" {
						fmt.Fprintf(w, "%s: %s\n", d.Name, cmd.Error)
					} else {
						fmt.Fprintf(w, "%s: command %s (%s): can be run\n", d.Name, cmd.Key, strings.Join(cmd.Argv, " "))
					}
				}
			}
			if d.Limits == nil {
				if err := a.out(c.OutOrStdout(), out, func(w io.Writer) { said(w); sayCmds(w) }); err != nil {
					return err
				}
				if !cmdsOK {
					return errCheckFailed // the lines said which command and why
				}
				return nil
			}
			asked, ok := checkLimits(c.Context(), limits.Answerer{Source: d.Name, Limits: d.Limits, Home: home})
			out["limits"] = asked
			if err := a.out(c.OutOrStdout(), out, func(w io.Writer) {
				said(w)
				sayCmds(w)
				for _, l := range checkLines(asked, time.Now()) {
					fmt.Fprintf(w, "%s: limits: %s\n", d.Name, l)
				}
			}); err != nil {
				return err
			}
			if !ok || !cmdsOK {
				return errCheckFailed // the lines said which kind or command and why
			}
			return nil
		},
	}

	check.Flags().StringVar(&checkUser, "user", "", "tell this user through the source: one real test message")
	check.Flags().StringVar(&checkAs, "as", "", "look at this user's credential, and make the check call as them")

	cmd.AddCommand(add, list, show, ignore, rm, set, key, check, grant, revoke, a.grantsCmd())
	return cmd
}

// checkTells is yardr source check <name> --user <user>: one message to
// the user through the source, which says that it is a test.
func (a *app) checkTells(w io.Writer, ctx context.Context, st store.API, name, user string) error {
	u, err := st.GetUser(ctx, user)
	if err != nil {
		return err
	}
	// The message buzzes a person's phone: theirs to ask for, or the
	// operator's. Like maySpeakAs it is a line, not a wall: the command
	// reads files its caller can read, and no store is asked.
	if operator, is, err := a.operator(ctx, st); err != nil {
		return err
	} else if !is && a.who() != u.Name {
		return fmt.Errorf("a test message to %s is theirs to ask for, or %s; this is %s", u.Name, store.OperatorsWhose(strings.Split(operator, ", ")), a.who())
	}
	title, text := store.SourceCheckMessage(name, u.Name)
	sent, err := notify.Through(ctx, st, a.home(), name, u, notify.Message{Title: title, Text: text})
	if err != nil {
		return err
	}
	if !sent.OK() {
		return fmt.Errorf("source %s: telling %s was answered %d: nothing reached them", name, u.Name, sent.Status)
	}
	return a.out(w, sent, func(w io.Writer) {
		fmt.Fprintf(w, "%s: told %s: %d in %dms\n", name, u.Name, sent.Status, sent.Took.Milliseconds())
		if u.Notify != name {
			fmt.Fprintf(w, "next: yardr user set %s %s=%s\n", u.Name, store.UserNotify, name)
		}
	})
}

// notifyLine says in a line how a source tells a person.
func notifyLine(d *source.Declaration) string {
	n := d.Notify
	return fmt.Sprintf("notify: the call %s tells a user, at the %s of their env_file (yardr user set <name> %s=%s)", n.Call, n.AddressEnv, store.UserNotify, d.Name)
}

// hearLine says in a line which deliveries of a source are kept: one whose
// envelope field holds an address a user's env file sets, read as telling
// reads it, or, by identity, one whose field holds a speaker's id some
// user maps; anything else is dropped after verification.
func hearLine(d *source.Declaration) string {
	h := d.Hear
	if h.ByIdentity() {
		return fmt.Sprintf("hear: keeps a delivery whose %s is some user's identity in %s (yardr user identity); anything else follows the hook's --unknown word", h.Field, d.Name)
	}
	who := "a user's env_file"
	if h.From == source.HearFromOperator {
		who = "an operator's env_file"
	}
	return fmt.Sprintf("hear: keeps a delivery whose %s is the %s of %s; anything else is dropped after verification", h.Field, h.Address.Env, who)
}

// ignoredLine says in a line which senders of an identity-hearing source
// the operator answered nobody for: their deliveries are dropped without
// a further card under any hook word, until the entry is removed (yardr
// source ignore <source> --rm <id>) or the sender is mapped.
func ignoredLine(name string, ignored []string) string {
	if len(ignored) == 0 {
		return "ignored: none"
	}
	return fmt.Sprintf("ignored: %s", strings.Join(sortedIgnored(ignored), ", "))
}

// sortedIgnored is the ignored sender ids in order, never nil, so --json
// carries an empty list and not null where none is ignored.
func sortedIgnored(ignored []string) []string {
	out := slices.Sorted(slices.Values(ignored))
	if out == nil {
		return []string{}
	}
	return out
}

// limitsLine says in a line that a source says limits: of which kinds, or
// of the source itself.
func limitsLine(d *source.Declaration) string {
	if d.Limits.Self() {
		return "limits: its command says what the source itself carries (yardr limits)"
	}
	return fmt.Sprintf("limits: its command says what the subscription of %s still carries (yardr limits <kind>)", strings.Join(d.Limits.KindNames(), ", "))
}

// verifyLine says in a line how a hook's deliveries are verified. A
// signature is named by what it covers, never assumed to be the body: the
// declaration may sign a template over the timestamp and the delivery id
// beside it. The line then says how the signature arrives (encoding,
// prefix, and the separator where one header carries several) and the key
// it is made with where the secret writes one. The ask card says the same
// in full (hookSays in the store); a knob added to hook.verify is named in
// both.
func verifyLine(d *source.Declaration) string {
	v := d.Hook.Verify
	if v.Scheme == source.HookSecret {
		// Nothing is signed: the header holds the secret itself.
		return fmt.Sprintf("hook.verify: secret in the header %s, the hook's secret itself, compared as it arrives", v.Signature.Header)
	}
	// An unset signed is the body alone, as the receiver reads it.
	over := v.Signed
	if over == "" || over == source.HookSigned {
		over = "the body"
	} else {
		// A declaration's literal template may contain control characters;
		// quote it so it stays one safe line in a terminal.
		over = fmt.Sprintf("%q", over)
	}
	arrives := "as " + v.Signature.Encoding
	if sep := v.Signature.List; sep != "" {
		// Several signatures may stand side by side, and one that matches
		// is enough.
		each := "with no prefix"
		if v.Signature.Prefix != "" {
			each = fmt.Sprintf("past the prefix %q", v.Signature.Prefix)
		}
		arrives += fmt.Sprintf(", one or more separated by %q, each %s, any one of them passing", sep, each)
	} else if v.Signature.Prefix != "" {
		arrives += fmt.Sprintf(" past the prefix %q", v.Signature.Prefix)
	}
	line := fmt.Sprintf("hook.verify: hmac-sha256 over %s, the signature in the header %s %s", over, v.Signature.Header, arrives)
	// A key the secret writes is not the secret's own bytes.
	if k := v.Key; k != nil {
		from := "the whole secret"
		if k.Prefix != "" {
			from = fmt.Sprintf("what follows the prefix %q in the secret", k.Prefix)
		}
		line += fmt.Sprintf("; the HMAC key is %s, read as %s", from, k.Encoding)
	}
	return line
}

// commandFileLine is one command's program in a line of yardr source show:
// the file the declaration brought, with its sha256, or that the program is
// the operator's own.
func commandFileLine(f source.CommandFile) string {
	switch {
	case !f.Declared:
		return "the program is the operator's own, not copied"
	case f.SHA256 == "":
		return "file " + f.Path + " is not there"
	default:
		return "file " + f.Path + " sha256 " + f.SHA256
	}
}

// usersHolding counts the users whose own file sets the credential a call
// as them reads, as source show says it per user: UserCredentialState
// without an error.
func usersHolding(d *source.Declaration, users []store.User) int {
	n := 0
	for _, u := range users {
		if _, err := d.UserCredentialState(u.Name, u.EnvFile); err == nil {
			n++
		}
	}
	return n
}

// mintLine says which command mints the source's credential and why: the
// operator's mint_command where one is set, which wins; else the one the
// declaration brings, with its copy and the copy's sha256; else none.
func mintLine(d *source.Declaration, src *store.Source, f source.CommandFile) string {
	brings := len(d.Credential.MintCommand) > 0
	switch {
	case src.MintCommand != "" && brings:
		return fmt.Sprintf("mint: %s, the source's mint_command, which wins over the declaration's own %s", src.MintCommand, strings.Join(d.Credential.MintCommand, " "))
	case src.MintCommand != "":
		return fmt.Sprintf("mint: %s, the source's mint_command", src.MintCommand)
	case brings:
		return fmt.Sprintf("mint: %s, the declaration's own (credential.mint_command): %s", strings.Join(d.Credential.MintCommand, " "), commandFileLine(f))
	default:
		return fmt.Sprintf("mint: none: yardr source set %s mint_command=<program> [args...]", d.Name)
	}
}

// commandCheck is one command of a declaration as source check finds it:
// its program as the yard runs it, and whether that is there and can be run.
type commandCheck struct {
	Key     string   `json:"key"`
	Argv    []string `json:"argv"`
	Program string   `json:"program"`
	Error   string   `json:"error,omitempty"` // why it cannot be run; "" when it can
}

// checkCommands looks at the program of each command the declaration names:
// there, and runnable. None is run.
func checkCommands(d *source.Declaration, home string) (cmds []commandCheck, ok bool) {
	ok = true
	for _, c := range d.Commands() {
		prog := c.Program(home, d.Name)
		cmd := commandCheck{Key: c.Key, Argv: c.Argv, Program: prog}
		what := "command " + c.Key + " (" + strings.Join(c.Argv, " ") + ")"
		if err := (config.Command{What: what, Argv: []string{prog}}).Missing(); err != nil {
			cmd.Error = err.Error()
			ok = false
		}
		cmds = append(cmds, cmd)
	}
	return cmds, ok
}

// checkField renders one selected field of a check call's answer for
// its line: a string as it is, a number or a boolean as its JSON, and an
// object or a list as compact JSON, as the answer already holds it.
func checkField(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// statusHint says what a status of a check call most likely means.
func statusHint(status int, d *source.Declaration) string {
	if (status == 401 || status == 403) && d.Credential != nil {
		if d.Credential.Minted() {
			return ": the service does not take its credential (minted from " + strings.Join(d.Credential.Mint, " and ") + ", or " + d.Credential.Env + ")"
		}
		return ": the service does not take what " + d.Credential.Env + " is set to"
	}
	return ""
}

// paramLine is a parameter in a line of yardr source show.
func paramLine(p source.Param) string {
	parts := []string{p.Name, p.Type}
	if p.Pattern != "" {
		parts = append(parts, "matching "+p.Pattern)
	}
	if p.Max > 0 {
		parts = append(parts, fmt.Sprintf("at most %d characters", p.Max))
	}
	if len(p.Values) > 0 {
		parts = append(parts, "one of "+strings.Join(p.Values, ", "))
	}
	if len(p.Default) > 0 {
		parts = append(parts, "default "+string(p.Default))
	} else if p.Optional {
		parts = append(parts, "optional")
	}
	if p.Log {
		parts = append(parts, "its value is kept in the record of a call")
	}
	return strings.Join(parts, "  ")
}

// callChanges says what the declaration now changes of the calls of the
// one it replaces, a line per call; nil for a source that is new.
func callChanges(was, now *source.Declaration) []string {
	if was == nil {
		return nil
	}
	var out []string
	for _, c := range now.Calls {
		old, had := was.Call(c.Name)
		switch {
		case !had:
			out = append(out, fmt.Sprintf("new call %s (%s): %s", c.Name, c.Kind, c.Request.Line()))
		case !sameCall(*old, c):
			out = append(out, fmt.Sprintf("changed call %s (%s): %s, was (%s) %s", c.Name, c.Kind, c.Request.Line(), old.Kind, old.Request.Line()))
		}
	}
	for _, c := range was.Calls {
		if _, has := now.Call(c.Name); !has {
			out = append(out, fmt.Sprintf("call %s is gone", c.Name))
		}
	}
	if was.Address() != now.Address() {
		out = append(out, fmt.Sprintf("base was %s", or(was.Address(), "none: it had no calls")))
	}
	if len(out) == 0 {
		out = append(out, "replaces the one of that name: its calls are as they were")
	}
	return out
}

// grantWords returns what says a grant's words in a line, an update call
// marked as one: "customer, note (update), read". It reads the sources
// once, for every grant it is asked about.
func grantWords(ctx context.Context, st store.API) func(store.SourceGrant) string {
	updates := map[string]bool{} // <source> <call>
	if srcs, err := st.Sources(ctx); err == nil {
		for _, src := range srcs {
			// One this build does not read has no call it could mark.
			d, err := src.Declaration()
			if err != nil {
				continue
			}
			for _, c := range d.Calls {
				updates[d.Name+" "+c.Name] = c.Kind == source.Update
			}
		}
	}
	return func(g store.SourceGrant) string {
		words := make([]string, len(g.Calls))
		for i, w := range g.Calls {
			words[i] = w
			if updates[g.Source+" "+w] {
				words[i] += " (" + source.Update + ")"
			}
		}
		return strings.Join(words, ", ")
	}
}

// asSuffix is " as <user>" for a grant made as a user, "" for a plain
// one: how a line about a grant says who a call is made as.
func asSuffix(g store.SourceGrant) string {
	if g.As == "" {
		return ""
	}
	return " as " + g.As
}

// stillGranted says who holds, by its name, a call the replaced declaration
// changes: the grant was given for the call as it was, and an update that
// now writes something else is still theirs to make. A line per call.
func stillGranted(was, now *source.Declaration, grants []store.SourceGrant) []string {
	if was == nil {
		return nil
	}
	var out []string
	for _, c := range now.Calls {
		old, had := was.Call(c.Name)
		if !had || sameCall(*old, c) {
			continue
		}
		var who []string
		for _, g := range grants {
			if slices.Contains(g.Calls, c.Name) {
				who = append(who, g.Holder())
			}
		}
		if len(who) > 0 {
			out = append(out, fmt.Sprintf("the changed call %s (%s) is still granted by its name to %s (yardr source revoke %s %s takes it back)",
				c.Name, c.Kind, strings.Join(who, ", "), now.Name, c.Name))
		}
	}
	return out
}

// grantedCall is who may make the call c under grants, each with the word
// of the grant it is by when that is read and not the call's name.
func grantedCall(grants []store.SourceGrant, c *source.Call) []string {
	var who []string
	for _, g := range grants {
		switch {
		case slices.Contains(g.Calls, c.Name):
			who = append(who, g.Holder())
		case g.Covers(c, g.As):
			who = append(who, g.Holder()+" (through "+store.GrantRead+")")
		}
	}
	return who
}

// widened says who a replaced declaration now lets reach more: the read
// calls that are new or changed, and the holders that get them through a
// grant of read, which was given before the call was as it is
// (store.Widened). "" when nobody does.
func widened(was, now *source.Declaration, grants []store.SourceGrant) string {
	calls, to := store.Widened(was, now, grants)
	if len(calls) == 0 {
		return ""
	}
	who := make([]string, len(to))
	for i, g := range to {
		who[i] = g.Holder()
	}
	return fmt.Sprintf("through their grant of %s, %s may now call: %s (yardr source revoke %s %s takes it back)",
		store.GrantRead, strings.Join(who, ", "), strings.Join(calls, ", "), now.Name, store.GrantRead)
}

type sourceRemovalReferences struct {
	subscriptions  int
	participations int
	unsubscribe    []string
	unfollow       []string
}

// sourceRmReferences counts the records source rm leaves behind and prints
// the command for removing each subscriber's subscriptions and each thread
// participation. A single unsubscribe takes every subscription of that
// source for one crew member, group or user. A name a crew member and a
// manual group share takes two commands, one with each of --crew and
// --group: the one without a flag would refuse, both having subscriptions.
func sourceRmReferences(ctx context.Context, st store.API, source string) (sourceRemovalReferences, error) {
	left := sourceRemovalReferences{
		unsubscribe: []string{},
		unfollow:    []string{},
	}
	subscribed := map[string]map[string]bool{}
	unfollow := map[string]bool{}
	addSubscriptions := func(name, kind string, subs []store.Subscription) {
		for _, sub := range subs {
			if sub.Source == source {
				left.subscriptions++
				if subscribed[name] == nil {
					subscribed[name] = map[string]bool{}
				}
				subscribed[name][kind] = true
			}
		}
	}

	crew, err := st.Crew(ctx)
	if err != nil {
		return left, err
	}
	for _, member := range crew {
		addSubscriptions(member.Name, "crew", member.Subscriptions)
		threads, err := st.Threads(ctx, member.Name)
		if err != nil {
			return left, err
		}
		for _, participation := range threads {
			if participation.Source == source {
				left.participations++
				unfollow["yardr thread unfollow "+shellQuote(member.Name)+" --source "+shellQuote(source)+" "+shellQuote(participation.Thread)] = true
			}
		}
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return left, err
	}
	for _, group := range groups {
		addSubscriptions(group.Name, "group", group.Subscriptions)
	}
	users, err := st.Users(ctx)
	if err != nil {
		return left, err
	}
	for _, user := range users {
		addSubscriptions(user.Name, "user", user.Subscriptions)
	}
	for name, kinds := range subscribed {
		base := "yardr unsubscribe " + shellQuote(name) + " " + shellQuote(source)
		switch {
		case kinds["crew"] && kinds["group"]:
			left.unsubscribe = append(left.unsubscribe, base+" --crew", base+" --group")
		case kinds["user"] && kinds["group"]:
			left.unsubscribe = append(left.unsubscribe, base, base+" --group")
		default:
			left.unsubscribe = append(left.unsubscribe, base)
		}
	}
	for command := range unfollow {
		left.unfollow = append(left.unfollow, command)
	}
	slices.Sort(left.unsubscribe)
	slices.Sort(left.unfollow)
	return left, nil
}

// printable is source text the CLI prints for the operator — a thread id,
// title or url, or a command holding one — with nothing a terminal would
// act on: a thread id is the source's own text, and one with a carriage
// return or an escape sequence could overwrite or hide what the table
// shows. --json carries the text as it is.
func printable(command string) string {
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar || !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, strings.ToValidUTF8(command, "?"))
}

// printableThreadFields prepares event data for the human-readable event and
// inbox journals, where thread fields are otherwise shown as a generic map.
func printableThreadFields(data map[string]any) map[string]any {
	printFields := func(fields map[string]any, names ...string) map[string]any {
		out := make(map[string]any, len(fields))
		for key, value := range fields {
			if s, ok := value.(string); ok && slices.Contains(names, key) {
				value = printable(s)
			}
			out[key] = value
		}
		return out
	}
	clean := printFields(data, "title", "url", "thread")
	if thread, ok := data["thread"].(map[string]any); ok {
		clean["thread"] = printFields(thread, "id", "title", "url")
	}
	return clean
}

func countPhrase(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("%d %s", count, singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func sameCall(a, b source.Call) bool { return store.SameCall(a, b) }
