package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

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
credential.env names, read at each call from the file the operator sets with
yardr source set <name> env_file=<file>. It goes in the credential's header,
or as a whole value of a body for a service that takes it there, and never
in a path or a query. credential.update_env names a second variable of that
file, which the update calls are sent with instead, so that the read calls
run on a credential that may only read.

  credential:
    env: CRM_READ_TOKEN
    update_env: CRM_WRITE_TOKEN
    header: "Authorization: Bearer {credential}"
  idempotency: Idempotency-Key

idempotency names the header a service takes a key in, by which it tells an
update it was sent before from a new one. yardr call then sends each update
with a key, and the same key when the same caller makes the same call with
the same values again after one that did not end in a success: a call run
again after a timeout writes once. After a success the same call is a new
one. A key nobody tried again is kept for ` + store.SourceKeyKept.String() + `.

Refused when the file is added: a base that is not https:// (plain http://
only to this machine, for a stand-in); a path that is an address, has '..'
or a query; a {param} the call does not declare; a read call with PUT, PATCH
or DELETE; a name yardr has built in (` + strings.Join(source.BuiltIn, ", ") + `). When a call is made,
no redirect is followed, the call ends after ` + source.Timeout.String() + `, and an answer over its
bound is refused, not cut.

A source can tell a person outside the yard when its file has a notify key:
the update call that does it, and which of the call's parameters take what
the yard has to say.

  notify:
    call: message
    address: { param: user, env: PUSHOVER_USER }  # where the person is reached
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

The command is run by yardr itself, as the user yardr runs as: without a
shell (each word is one element of the list), its stdin closed, in the
yard's home, with {provider} replaced by the name the kind maps to, for at
most ` + limits.Timeout.String() + `, after which it and everything it started are killed. Name the
program by its whole path (the daemon's PATH need not be your shell's), or
as a file beside the declaration, which add copies into the yard. It
answers with exit status 0 and one JSON object on stdout, at most 64 KB:

  {"provider": "Kimi", "plan": "…", "windows": [
    {"label": "5h", "used_percent": 100, "resets_at": "2026-10-07T00:02:32Z", "used": 100, "limit": 100}]}

provider and windows are required, and each window's label; used_percent is
a number, used and limit whole numbers, resets_at a time as RFC 3339 writes
one, plan a string, each left out when the provider does not know it. Other
keys are passed over. A window is used up at used_percent 100 or more, or
when used has reached limit. Anything else (another exit status, no answer
in time, other output) is no answer: the limits are then not known, and the
reason is the last lines the command printed on stderr.

Refused when the file is added: a command that is no list of strings, has no
{provider} among its arguments or has it in the program; a kind herdr starts
no agent of; a kind another source of the yard already answers for. No pack
brings a limits key: it is a command the yard runs, so it is the operator's.
What runs the command: yardr limits, yardr source check and yardr doctor
--try, each when a person asks, and the daemon when a harness stopped on a
usage limit, for the window and its reset in the session_harness_error, and
once more at that reset before it prompts the agent (docs/groups.md). yardr limits --help says more.

A file can also name the commands by which the yard speaks in the source's
threads, and how the source's issues are a bead's link:

  thread:
    reply:  [linear/thread, reply]      # in {thread, parent, body, key}  out {id, url}
    posted: [linear/thread, posted]     # in {thread, key}                out {id, url} or {}
    read:   [linear/thread, read]       # in {thread}                     out {messages: [...]}
    place:  team                        # what --in is called in this source
    open:   [linear/thread, open]       # in {in, title, body, key}       out {id, title, url}
    opened: [linear/thread, opened]     # in {in, key}                    out {id, title, url} or {}
    state:  [linear/thread, state]      # in {thread, state}              out {status, moved}
    states: [started, review, done]     # the words thread.state takes
  link:
    host: linear.app
    issue: '^/([^/]+)/issue/([A-Z][A-Z0-9]*-[0-9]+)'
    fixed: "https://linear.app/{1}/issue/{2}"   # templates over the
    thread: "{2}"                               # expression's groups

Each command is run as the limits command is: a list without a shell, one
JSON object on stdin, one on stdout, exit 0. posted and opened answer {}
for none: a second attempt asks first, so nothing is posted twice.
thread.state picks the source's status for the word and never moves a
thread backwards: a thread at or past it is reported as not moved.
A program with no / at its start is a file beside the declaration's file:
source add copies it under ~/.yardr/sources/<name>/ (the relative path
kept, mode 0700) and the command runs from there, source show lists it with
its sha256, and source rm removes the directory; an absolute program is the
operator's own and is not copied. A command's environment is PATH, HOME,
the variable credential.env names and YARDR_SOURCE_BASE, the declaration's
base: nothing else of the yard's environment. The core hands the command
the credential's value: the caller's own. Refused: a command that is no
list of strings or has no program, a thread.open without thread.place, a
thread.state without thread.states, a state that is none of started,
review, done, a link.issue that does not compile, a template naming a group
the expression has not. No pack brings a thread or link key.
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

verify is data, checked by the core: the hex HMAC-SHA256 of the raw body
with the hook's secret, past the prefix (prefix: "sha256=", or none), and
after it the timestamp, in the body at the dot path or in a header, in ms
or s, within window of the yard's clock. The signing secret reaches no
command, and an unverified body reaches none. bodies is unique (no two
deliveries share a body) or repeat (a body may come again). normalize
turns a verified body into the envelope: stdin {"headers": {...}, "body":
...}, stdout the envelope, which the core refuses without a type and sets
the source and the delivery of itself. It runs as the limits command does
and is handed no credential. keys are what the events' fields may carry,
which a subscription's --where may then name beside type, action, team,
project, repo and label. Such a file needs no base and no calls, and with
neither has no credential, check or notify either. Refused: a scheme that
is not hmac-sha256, a signature that covers anything but the body, an
encoding that is not hex, a timestamp in the body and in a header, or in
neither, a unit that is not ms or s, a window that is no positive
duration, bodies that are neither unique nor repeat, a command that is no
list of strings or has no program, a key that is not lowercase letters,
digits and _. No pack brings a hook key: it is a command the yard runs,
so it is the operator's.

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
  yardr source revoke crm read --group reviewers

A grant is kept on its own, by source and grantee, and not in a group's
definition, which yardr group add replaces whole. read is every read call of
the source, one a later file adds included: yardr source add says who gets a
call that is new or changed. An update call is granted by its name and no
other way: there is no word for every update. Granting is the operator's,
and is refused from the pane of an agent whatever --actor says; anyone may
revoke. Removing a source removes its grants.`,
	}

	add := &cobra.Command{
		Use:   "add <file>",
		Short: "Add the source a file declares, or replace the one of its name",
		Long: `Reads the declaration and keeps it in the store under the name the file
gives it. Everything wrong with the file is named at once, by line and key,
and nothing is kept. A source of that name is replaced and keeps its
env_file; what the new file changes of its calls is printed.`,
		Example: `  yardr source add pushover.yaml
  yardr source set pushover env_file=~/.yardr/sources/pushover.env
  yardr source check pushover`,
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
			var before *source.Declaration
			if was, err := st.GetSource(c.Context(), d.Name); err == nil {
				before, _ = was.Declaration()
			}
			// The files the declaration's commands name are the operator's
			// to add, as the declaration is: they are looked at before the
			// store is asked, and copied after it has answered.
			home := ""
			if d.HasCommandFiles() || (before != nil && before.HasCommandFiles()) {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				home = cfg.Home
			}
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
			saved, err := st.PutSource(c.Context(), args[0], string(text), a.who())
			if err != nil {
				return err
			}
			if home != "" {
				// CopyCommandFiles makes the directory anew, so the files of
				// the declaration this one replaces are gone with it.
				if err := d.CopyCommandFiles(declDir, home); err != nil {
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
			return a.out(map[string]any{"source": saved, "declaration": d, "changes": changes}, func() {
				if len(d.Calls) == 0 {
					fmt.Printf("source %s saved: no call, its limits command alone\n", d.Name)
				} else {
					fmt.Printf("source %s saved: %s under %s\n", d.Name, plural(len(d.Calls), "call"), d.Base)
				}
				for _, l := range changes {
					fmt.Println("  " + l)
				}
				if d.Notify != nil {
					fmt.Println("  " + notifyLine(d))
				}
				if d.Limits != nil {
					fmt.Println("  " + limitsLine(d))
					fmt.Printf("next: yardr source check %s (it asks for each of those kinds)\n", d.Name)
				}
				var copied []string
				for _, f := range d.CommandFiles(home) {
					if f.Declared && !slices.Contains(copied, f.Program) {
						copied = append(copied, f.Program)
					}
				}
				if len(copied) > 0 {
					fmt.Printf("  the commands' files are under %s, each mode 0700: %s\n", filepath.Join(home, "sources", d.Name), strings.Join(copied, ", "))
				}
				if d.Credential != nil && saved.EnvFile == "" {
					fmt.Printf("next: yardr source set %s env_file=<a file of mode 0600 that sets %s>\n", d.Name, strings.Join(d.Credential.Envs(), " and "))
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
			return a.out(srcs, func() {
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
						cred = strings.Join(d.Credential.Envs(), ", ") + " in " + or(src.EnvFile, "no env_file yet")
					}
					notify := "-"
					if d.Notify != nil {
						notify = d.Notify.Call
					}
					kinds := "-"
					if d.Limits != nil {
						kinds = strings.Join(d.Limits.KindNames(), ", ")
					}
					rows = append(rows, []string{d.Name, or(d.Base, "-"), or(strings.Join(names, ", "), "-"), cred, or(d.Check, "-"), notify, kinds})
				}
				table("NAME\tBASE\tCALLS\tCREDENTIAL\tCHECK\tNOTIFY\tLIMITS", rows)
			})
		},
	}

	show := &cobra.Command{
		Use: "show <name>", Short: "Print a source: its calls, and whether its credential is set up", Args: cobra.ExactArgs(1),
		Long: `Prints what the source's file declares, and for its credential the name of
the variable and whether the env_file sets it. The value is never printed.
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
			state, credErr := d.CredentialState(src.EnvFile)
			if credErr != nil {
				state = credErr.Error()
			}
			grants, err := st.SourceGrants(c.Context(), d.Name)
			if err != nil {
				return err
			}
			home := ""
			if d.HasCommandFiles() {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				home = cfg.Home
			}
			files := map[string]source.CommandFile{}
			for _, f := range d.CommandFiles(home) {
				files[f.Key] = f
			}
			return a.out(map[string]any{"source": src, "declaration": d, "credential": state, "grants": grants}, func() {
				fmt.Printf("%s  %s\n", d.Name, or(d.Base, "no calls"))
				if d.About != "" {
					fmt.Printf("  %s\n", d.About)
				}
				fmt.Printf("  credential: %s\n", state)
				switch {
				case d.Check != "":
					fmt.Printf("  check: the call %s (yardr source check %s)\n", d.Check, d.Name)
				case len(d.Calls) > 0:
					fmt.Println("  check: the file names no call for it")
				}
				for _, call := range d.Calls {
					line := fmt.Sprintf("  call %s  %s  %s", call.Name, call.Kind, call.Request.Line())
					if call.About != "" {
						line += "  " + call.About
					}
					fmt.Println(line)
					for _, p := range call.Params {
						fmt.Println("    " + paramLine(p))
					}
					if len(call.Answer.Select) > 0 {
						fmt.Printf("    answers %s\n", strings.Join(call.Answer.Select, ", "))
					}
					if who := grantedCall(grants, &call); len(who) > 0 {
						fmt.Printf("    may be called by %s\n", strings.Join(who, ", "))
					}
				}
				if len(d.Calls) > 0 {
					fmt.Printf("  calls: %d an hour for each caller, %d of them updates at most\n", src.CallLimit(), src.UpdateLimit())
				}
				if d.Idempotency != "" {
					fmt.Printf("  updates: each is sent with a key in the header %s, the same when it is tried again\n", d.Idempotency)
				}
				if d.Notify != nil {
					fmt.Println("  " + notifyLine(d))
				} else {
					fmt.Println("  notify: the file has no notify key, so no user is told through it")
				}
				if d.Limits != nil {
					fmt.Println("  " + limitsLine(d))
					fmt.Printf("    command: %s\n", strings.Join(d.Limits.Command, " "))
					if f, ok := files["limits.command"]; ok {
						fmt.Println("    " + commandFileLine(f))
					}
					for _, kind := range d.Limits.KindNames() {
						fmt.Printf("    kind %s is asked as %s\n", kind, d.Limits.Kinds[kind])
					}
				}
				if d.Thread != nil {
					for _, cmd := range d.Commands() {
						if !strings.HasPrefix(cmd.Key, "thread.") {
							continue
						}
						fmt.Printf("  command %s: %s\n", cmd.Key, strings.Join(cmd.Argv, " "))
						fmt.Println("    " + commandFileLine(files[cmd.Key]))
					}
					if d.Thread.Place != "" {
						fmt.Printf("  thread.place: %s\n", d.Thread.Place)
					}
					if len(d.Thread.States) > 0 {
						fmt.Printf("  thread.states: %s\n", strings.Join(d.Thread.States, ", "))
					}
				}
				if d.Link != nil {
					fmt.Printf("  link: issues of %s (%s)\n", d.Link.Host, d.Link.Issue)
					fmt.Printf("    fixed: %s\n", d.Link.Fixed)
					fmt.Printf("    thread: %s\n", d.Link.Thread)
				}
				if d.Hook != nil {
					fmt.Printf("  command hook.normalize: %s\n", strings.Join(d.Hook.Normalize, " "))
					fmt.Println("    " + commandFileLine(files["hook.normalize"]))
					fmt.Printf("  hook.verify: %s of the body in %s\n", d.Hook.Verify.Scheme, d.Hook.Verify.Signature.Header)
					fmt.Printf("  hook.bodies: %s\n", d.Hook.Bodies)
					if len(d.Hook.Keys) > 0 {
						fmt.Printf("  hook.keys: %s\n", strings.Join(d.Hook.Keys, ", "))
					}
				}
				fmt.Printf("  added %s by %s\n", since(src.AddedAt), src.AddedBy)
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
			src, err := st.GetSource(c.Context(), args[0])
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
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				if err := source.RemoveCommandFiles(cfg.Home, args[0]); err != nil {
					return err
				}
			}
			return a.out(map[string]string{"source": args[0], "rm": "ok"}, func() { fmt.Printf("%s: removed\n", args[0]) })
		},
	}

	set := &cobra.Command{
		Use:   "set <name> env_file=<file> | calls_per_hour=<n> | updates_per_hour=<n>",
		Short: "Say which file a source's credential is read from, or how many calls a caller makes in an hour",
		Long: `env_file is a file of KEY=VALUE lines that sets the variable the source's
declaration names (credential.env, and credential.update_env where it names
one). It is held to the rule of every env_file:
a regular file of mode 0600 or stricter, read at each call, so rotating the
credential is rewriting the file. The store keeps its path, never what is in
it. A file that is open to others or does not set the name is refused here.
env_file= with nothing after it takes the setting away.

calls_per_hour is how many calls of the source one session or crew member
makes with yardr call within an hour (` + fmt.Sprint(store.SourceCallLimit) + ` unless set); the call after is
refused until the hour that began with the first has passed.
calls_per_hour= with nothing after it is ` + fmt.Sprint(store.SourceCallLimit) + ` again.

updates_per_hour is how many of those calls are updates (` + fmt.Sprint(store.SourceUpdateLimit) + ` unless set):
an update is counted as a call and as an update, and is refused when either
is used up. updates_per_hour= with nothing after it is ` + fmt.Sprint(store.SourceUpdateLimit) + ` again.`,
		Example: `  printf 'PUSHOVER_TOKEN=%s\n' '<token>' > ~/.yardr/sources/pushover.env
  chmod 600 ~/.yardr/sources/pushover.env
  yardr source set pushover env_file=~/.yardr/sources/pushover.env
  yardr source set crm calls_per_hour=20
  yardr source set crm updates_per_hour=3`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			key, value, ok := strings.Cut(args[1], "=")
			if !ok || !slices.Contains(store.SourceSettings, key) {
				return fmt.Errorf("%q: want %s=<file>, %s=<n> or %s=<n>, the settings a source has", args[1], store.SourceEnvFile, store.SourceCallsPerHour, store.SourceUpdatesPerHour)
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			state := "no " + store.SourceEnvFile
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
				if state, err = d.CredentialState(value); err != nil {
					return err
				}
			}
			saved, err := st.SetSource(c.Context(), args[0], key, value, a.who())
			if err != nil {
				return err
			}
			if key != store.SourceEnvFile {
				state = fmt.Sprintf("%d calls an hour for each caller, %d of them updates at most", saved.CallLimit(), saved.UpdateLimit())
			}
			return a.out(saved, func() { fmt.Printf("source %s: %s\n", saved.Name, state) })
		},
	}

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
	grant := &cobra.Command{
		Use:   "grant <name> read|<call> --group <group> | --crew <crew member>",
		Short: "Let a group's sessions or a crew member make calls of a source",
		Long: `read is every read call of the source, one a later file adds included; a
call's name is that one call. An update call is granted by its name alone:
read does not cover it, and there is no word for every update. Both flags
repeat. A grant is the operator's to give, and is refused from the pane of
an agent whatever --actor says.

A session whose bead came from another yard makes no call, whatever its
group is granted.`,
		Example: `  yardr source grant crm read --group builders --group reviewers
  yardr source grant crm customer --crew yardmaster
  yardr source grant crm note --group support`,
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
			var saved []*store.SourceGrant
			for _, g := range to {
				s, err := st.GrantSource(c.Context(), args[0], args[1], g, a.who())
				if err != nil {
					return err
				}
				saved = append(saved, s)
			}
			words := grantWords(c.Context(), st)
			return a.out(saved, func() {
				for _, s := range saved {
					fmt.Printf("%s may call %s: %s\n", s.Grantee, s.Source, words(*s))
				}
			})
		},
	}
	revoke := &cobra.Command{
		Use:   "revoke <name> read|<call> --group <group> | --crew <crew member>",
		Short: "Take a grant of a source's calls back",
		Long: `Takes back what yardr source grant gave, by the same word: read, or a
call's name. A grantee that has read and a call by name keeps the one that
is not named here. Anyone may revoke.`,
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
				s, err := st.RevokeSource(c.Context(), args[0], args[1], g, a.who())
				if err != nil {
					return err
				}
				left = append(left, s)
			}
			words := grantWords(c.Context(), st)
			return a.out(left, func() {
				for i, s := range left {
					fmt.Printf("%s may call %s: %s\n", to[i], args[0], or(words(*s), "nothing"))
				}
			})
		},
	}
	for _, cmd := range []*cobra.Command{grant, revoke} {
		cmd.Flags().StringArrayVar(&groups, "group", nil, "a group: each session it starts (repeatable)")
		cmd.Flags().StringArrayVar(&crew, "crew", nil, "a crew member (repeatable)")
	}

	var checkUser string
	check := &cobra.Command{
		Use:   "check <name>",
		Short: "See that a source's credential is set up, make its check call, and ask its limits command",
		Long: `Reads the source's env_file as a call would, and makes the one call its
declaration names as check: a read call without parameters. Prints the
status the service answered with, and fails on anything but a success. A
source whose file names no check call is sent nothing: its credential is
looked at and that is all.

A source that says limits (a limits key) has its command run once for each
harness kind its file lists, one after the other, and what each answered is
printed as yardr limits prints it. It fails when a kind got no answer, so a
kind whose harness is not logged in on this machine is better left out of
the file. Each run may take up to ` + limits.Timeout.String() + `.

Of every other command the file names (thread's), check says whether its
program is there and can be run, and fails when one cannot; it runs none.

With --user it tells that user through the source instead: one real
message, a line that says it is a test, sent as everything the yard tells
them will be, with the address their env_file sets. It is the user's own to
ask for, or the operator's, and works before the user names the source as
their notify, to try it first. A source yardr has built in is checked this
way too, once one can tell a person.

The call is made by this command, with the credential read into this
process for it. Nothing of the request is written anywhere.`,
		Example: `  yardr source check pushover
  yardr source check pushover --user mckean`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if checkUser != "" {
				return a.checkTells(c.Context(), st, args[0], checkUser)
			}
			src, err := st.GetSource(c.Context(), args[0])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			// The check is a read call, and what is looked at is every
			// variable the file names, the one of its updates too.
			cred, _, err := d.ReadCredential(src.EnvFile, source.Read)
			if err != nil {
				return fmt.Errorf("source %s: %w", d.Name, err)
			}
			state, err := d.CredentialState(src.EnvFile)
			if err != nil {
				return fmt.Errorf("source %s: %w", d.Name, err)
			}
			out := map[string]any{"source": d.Name, "credential": state, "sent": false}
			// said is the part on the calls, for a person; the commands and
			// limits follow it.
			said := func() {}
			switch {
			case len(d.Calls) == 0:
			case d.Check == "":
				said = func() {
					fmt.Printf("%s: credential: %s\n", d.Name, state)
					fmt.Printf("%s: its file names no check call (check: <a read call without parameters>), so nothing was sent\n", d.Name)
				}
			default:
				res, err := source.Do(c.Context(), d, d.Check, nil, cred)
				if err != nil {
					return err
				}
				call, _ := d.Call(d.Check)
				out["sent"], out["call"], out["status"], out["ms"], out["bytes"] = true, d.Check, res.Status, res.Took.Milliseconds(), res.Bytes
				if !res.OK() {
					return fmt.Errorf("source %s: the check call %s (%s) was answered %d%s", d.Name, d.Check, call.Request.Line(), res.Status, statusHint(res.Status, d))
				}
				said = func() {
					fmt.Printf("%s: credential: %s\n", d.Name, state)
					fmt.Printf("%s: check call %s (%s): %d in %dms, %d bytes\n", d.Name, d.Check, call.Request.Line(), res.Status, res.Took.Milliseconds(), res.Bytes)
				}
			}
			// Of each command the file names, whether its program is there
			// and can be run. None is run: the limits command is run below,
			// per kind, as it is today.
			var cmds []commandCheck
			cmdsOK := true
			var home string
			if len(d.Commands()) > 0 {
				cfg, err := config.Load()
				if err != nil {
					return err
				}
				home = cfg.Home
				cmds, cmdsOK = checkCommands(d, home)
				out["commands"] = cmds
			}
			sayCmds := func() {
				for _, cmd := range cmds {
					if cmd.Error != "" {
						fmt.Printf("%s: %s\n", d.Name, cmd.Error)
					} else {
						fmt.Printf("%s: command %s (%s): can be run\n", d.Name, cmd.Key, strings.Join(cmd.Argv, " "))
					}
				}
			}
			if d.Limits == nil {
				if err := a.out(out, func() { said(); sayCmds() }); err != nil {
					return err
				}
				if !cmdsOK {
					return errCheckFailed // the lines said which command and why
				}
				return nil
			}
			asked, ok := checkLimits(c.Context(), limits.Answerer{Source: d.Name, Limits: d.Limits, Home: home})
			out["limits"] = asked
			if err := a.out(out, func() {
				said()
				sayCmds()
				now := time.Now()
				for _, k := range asked {
					for _, l := range k.lines(now) {
						fmt.Printf("%s: limits: %s\n", d.Name, l)
					}
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

	cmd.AddCommand(add, list, show, rm, set, check, grant, revoke)
	return cmd
}

// checkTells is yardr source check <name> --user <user>: one message to
// the user through the source, which says that it is a test.
func (a *app) checkTells(ctx context.Context, st store.API, name, user string) error {
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
		return fmt.Errorf("a test message to %s is theirs to ask for, or the operator's (%s); this is %s", u.Name, operator, a.who())
	}
	sent, err := notify.Through(ctx, st, name, u, notify.Message{
		Title: "yardr: a test",
		Text:  fmt.Sprintf("%s is told here by the yard, through the source %s. Sent by yardr source check %s --user %s.", u.Name, name, name, u.Name),
	})
	if err != nil {
		return err
	}
	if !sent.OK() {
		return fmt.Errorf("source %s: telling %s was answered %d: nothing reached them", name, u.Name, sent.Status)
	}
	return a.out(sent, func() {
		fmt.Printf("%s: told %s: %d in %dms\n", name, u.Name, sent.Status, sent.Took.Milliseconds())
		if u.Notify != name {
			fmt.Printf("next: yardr user set %s %s=%s\n", u.Name, store.UserNotify, name)
		}
	})
}

// notifyLine says in a line how a source tells a person.
func notifyLine(d *source.Declaration) string {
	n := d.Notify
	return fmt.Sprintf("notify: the call %s tells a user, at the %s of their env_file (yardr user set <name> %s=%s)", n.Call, n.AddressEnv, store.UserNotify, d.Name)
}

// limitsLine says in a line that a source says limits, and of which kinds.
func limitsLine(d *source.Declaration) string {
	return fmt.Sprintf("limits: its command says what the subscription of %s still carries (yardr limits <kind>)", strings.Join(d.Limits.KindNames(), ", "))
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

// statusHint says what a status of a check call most likely means.
func statusHint(status int, d *source.Declaration) string {
	if (status == 401 || status == 403) && d.Credential != nil {
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
	if p.Optional {
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
	if was.Base != now.Base {
		out = append(out, fmt.Sprintf("base was %s", or(was.Base, "none: it had no calls")))
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
				who = append(who, g.Grantee.String())
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
			who = append(who, g.Grantee.String())
		case g.Covers(c):
			who = append(who, g.Grantee.String()+" (through "+store.GrantRead+")")
		}
	}
	return who
}

// widened says who a replaced declaration now lets reach more: the read
// calls that are new or changed, and the grantees that get them through a
// grant of read, which was given before the call was as it is
// (store.Widened). "" when nobody does.
func widened(was, now *source.Declaration, grants []store.SourceGrant) string {
	calls, to := store.Widened(was, now, grants)
	if len(calls) == 0 {
		return ""
	}
	who := make([]string, len(to))
	for i, g := range to {
		who[i] = g.String()
	}
	return fmt.Sprintf("through their grant of %s, %s may now call: %s (yardr source revoke %s %s takes it back)",
		store.GrantRead, strings.Join(who, ", "), strings.Join(calls, ", "), now.Name, store.GrantRead)
}

func sameCall(a, b source.Call) bool { return store.SameCall(a, b) }
