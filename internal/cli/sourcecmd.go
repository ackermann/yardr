package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
in a path or a query.

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
program by its whole path: the daemon's PATH need not be your shell's. It
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
usage limit, for the window and its reset in the session_harness_error
(docs/groups.md). yardr limits --help says more.

Adding, setting and removing a source is the operator's, and is refused from
the pane of an agent: a source is where a credential of the yard is sent,
and its limits command is run by the yard. What calls a declared source is
yardr source check, and the daemon for a source a user names as their
notify: it sends the user what landed in their inbox (yardr user set
--help).`,
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
			saved, err := st.PutSource(c.Context(), args[0], string(text), a.who())
			if err != nil {
				return err
			}
			changes := callChanges(before, d)
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
				if d.Credential != nil && saved.EnvFile == "" {
					fmt.Printf("next: yardr source set %s env_file=<a file of mode 0600 that sets %s>\n", d.Name, d.Credential.Env)
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
						cred = d.Credential.Env + " in " + or(src.EnvFile, "no env_file yet")
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
			_, state, credErr := d.ReadCredential(src.EnvFile)
			if credErr != nil {
				state = credErr.Error()
			}
			return a.out(map[string]any{"source": src, "declaration": d, "credential": state}, func() {
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
				}
				if d.Notify != nil {
					fmt.Println("  " + notifyLine(d))
				} else {
					fmt.Println("  notify: the file has no notify key, so no user is told through it")
				}
				if d.Limits != nil {
					fmt.Println("  " + limitsLine(d))
					fmt.Printf("    command: %s\n", strings.Join(d.Limits.Command, " "))
					for _, kind := range d.Limits.KindNames() {
						fmt.Printf("    kind %s is asked as %s\n", kind, d.Limits.Kinds[kind])
					}
				}
				fmt.Printf("  added %s by %s\n", since(src.AddedAt), src.AddedBy)
			})
		},
	}

	rm := &cobra.Command{
		Use: "rm <name>", Short: "Remove a source (the file its credential is in stays)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.DeleteSource(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"source": args[0], "rm": "ok"}, func() { fmt.Printf("%s: removed\n", args[0]) })
		},
	}

	set := &cobra.Command{
		Use:   "set <name> env_file=<file>",
		Short: "Say which file a source's credential is read from",
		Long: `env_file is a file of KEY=VALUE lines that sets the variable the source's
declaration names (credential.env). It is held to the rule of every env_file:
a regular file of mode 0600 or stricter, read at each call, so rotating the
credential is rewriting the file. The store keeps its path, never what is in
it. A file that is open to others or does not set the name is refused here.
env_file= with nothing after it takes the setting away.`,
		Example: `  printf 'PUSHOVER_TOKEN=%s\n' '<token>' > ~/.yardr/sources/pushover.env
  chmod 600 ~/.yardr/sources/pushover.env
  yardr source set pushover env_file=~/.yardr/sources/pushover.env`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			key, value, ok := strings.Cut(args[1], "=")
			if !ok || key != store.SourceEnvFile {
				return fmt.Errorf("%q: want %s=<file>, the one setting a source has", args[1], store.SourceEnvFile)
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
			if value != "" {
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
				if _, state, err = d.ReadCredential(value); err != nil {
					return err
				}
			}
			saved, err := st.SetSource(c.Context(), args[0], key, value, a.who())
			if err != nil {
				return err
			}
			return a.out(saved, func() { fmt.Printf("source %s: %s\n", saved.Name, state) })
		},
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
			cred, state, err := d.ReadCredential(src.EnvFile)
			if err != nil {
				return fmt.Errorf("source %s: %w", d.Name, err)
			}
			out := map[string]any{"source": d.Name, "credential": state, "sent": false}
			// said is the part on the calls, for a person; limits follows it.
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
			if d.Limits == nil {
				return a.out(out, said)
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			asked, ok := checkLimits(c.Context(), limits.Answerer{Source: d.Name, Limits: d.Limits, Home: cfg.Home})
			out["limits"] = asked
			if err := a.out(out, func() {
				said()
				now := time.Now()
				for _, k := range asked {
					for _, l := range k.lines(now) {
						fmt.Printf("%s: limits: %s\n", d.Name, l)
					}
				}
			}); err != nil {
				return err
			}
			if !ok {
				return errCheckFailed // the lines said which kind and why
			}
			return nil
		},
	}

	check.Flags().StringVar(&checkUser, "user", "", "tell this user through the source: one real test message")

	cmd.AddCommand(add, list, show, rm, set, check)
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
	return strings.Join(parts, "  ")
}

// callChanges says what the declaration now changes of the calls of the
// one it replaces, a line per call; nil for a source that is new.
func callChanges(was, now *source.Declaration) []string {
	if was == nil {
		return nil
	}
	var out []string
	same := func(a, b source.Call) bool {
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		return string(x) == string(y)
	}
	for _, c := range now.Calls {
		old, had := was.Call(c.Name)
		switch {
		case !had:
			out = append(out, fmt.Sprintf("new call %s (%s): %s", c.Name, c.Kind, c.Request.Line()))
		case !same(*old, c):
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
