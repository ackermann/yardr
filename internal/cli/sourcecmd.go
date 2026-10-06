package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
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

Adding, setting and removing a source is the operator's, and is refused from
the pane of an agent: a source is where a credential of the yard is sent.
Nothing in the yard calls a declared source yet but yardr source check.`,
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
				fmt.Printf("source %s saved: %s under %s\n", d.Name, plural(len(d.Calls), "call"), d.Base)
				for _, l := range changes {
					fmt.Println("  " + l)
				}
				if len(d.Unread) > 0 {
					fmt.Printf("note: the file's %s is kept with it and not read by this yardr\n", strings.Join(d.Unread, ", "))
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
						rows = append(rows, []string{src.Name, "-", "not read by this yardr: yardr source show " + src.Name, "-", "-"})
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
					rows = append(rows, []string{d.Name, d.Base, strings.Join(names, ", "), cred, or(d.Check, "-")})
				}
				table("NAME\tBASE\tCALLS\tCREDENTIAL\tCHECK", rows)
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
			_, state, credErr := declaredCredential(src, d)
			if credErr != nil {
				state = credErr.Error()
			}
			return a.out(map[string]any{"source": src, "declaration": d, "credential": state}, func() {
				fmt.Printf("%s  %s\n", d.Name, d.Base)
				if d.About != "" {
					fmt.Printf("  %s\n", d.About)
				}
				fmt.Printf("  credential: %s\n", state)
				if d.Check != "" {
					fmt.Printf("  check: the call %s (yardr source check %s)\n", d.Check, d.Name)
				} else {
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
				if len(d.Unread) > 0 {
					fmt.Printf("  kept with the file and not read by this yardr: %s\n", strings.Join(d.Unread, ", "))
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
				src.EnvFile = value
				if _, state, err = declaredCredential(src, d); err != nil {
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

	check := &cobra.Command{
		Use:   "check <name>",
		Short: "See that a source's credential is set up, and make its check call",
		Long: `Reads the source's env_file as a call would, and makes the one call its
declaration names as check: a read call without parameters. Prints the
status the service answered with, and fails on anything but a success. A
source whose file names no check call is sent nothing: its credential is
looked at and that is all.

The call is made by this command, with the credential read into this
process for it. Nothing of the request is written anywhere.`,
		Args: cobra.ExactArgs(1),
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
			cred, state, err := declaredCredential(src, d)
			if err != nil {
				return fmt.Errorf("source %s: %w", d.Name, err)
			}
			out := map[string]any{"source": d.Name, "credential": state}
			if d.Check == "" {
				out["sent"] = false
				return a.out(out, func() {
					fmt.Printf("%s: credential: %s\n", d.Name, state)
					fmt.Printf("%s: its file names no check call (check: <a read call without parameters>), so nothing was sent\n", d.Name)
				})
			}
			res, err := source.Do(c.Context(), d, d.Check, nil, cred)
			if err != nil {
				return err
			}
			call, _ := d.Call(d.Check)
			out["sent"], out["call"], out["status"], out["ms"], out["bytes"] = true, d.Check, res.Status, res.Took.Milliseconds(), res.Bytes
			if !res.OK() {
				return fmt.Errorf("source %s: the check call %s (%s) was answered %d%s", d.Name, d.Check, call.Request.Line(), res.Status, statusHint(res.Status, d))
			}
			return a.out(out, func() {
				fmt.Printf("%s: credential: %s\n", d.Name, state)
				fmt.Printf("%s: check call %s (%s): %d in %dms, %d bytes\n", d.Name, d.Check, call.Request.Line(), res.Status, res.Took.Milliseconds(), res.Bytes)
			})
		},
	}

	cmd.AddCommand(add, list, show, rm, set, check)
	return cmd
}

// statusHint says what a status of a check call most likely means.
func statusHint(status int, d *source.Declaration) string {
	if (status == 401 || status == 403) && d.Credential != nil {
		return ": the service does not take what " + d.Credential.Env + " is set to"
	}
	return ""
}

// declaredCredential reads the credential of src as a call needs it: the
// value of the variable d names, from the source's env_file, read now.
// state says in words where it is, for showing, and never holds the value;
// err says why there is none, with the command that sets it up.
func declaredCredential(src *store.Source, d *source.Declaration) (value, state string, err error) {
	if d.Credential == nil {
		return "", "none: the file names no credential", nil
	}
	name := d.Credential.Env
	if src.EnvFile == "" {
		return "", "", fmt.Errorf("%s is not set up: yardr source set %s %s=<a file of mode 0600 that sets %s>", name, src.Name, store.SourceEnvFile, name)
	}
	f, err := config.ReadEnvFile(src.EnvFile)
	if err != nil {
		return "", "", err
	}
	for _, e := range f.Env {
		if k, v, _ := strings.Cut(e, "="); k == name {
			value = v // of a name set twice the last, as in a pane's environment
		}
	}
	if value == "" {
		return "", "", fmt.Errorf("env_file %s does not set %s", f.Path, name)
	}
	return value, name + " is set in " + f.Path, nil
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
		out = append(out, fmt.Sprintf("base was %s", was.Base))
	}
	if len(out) == 0 {
		out = append(out, "replaces the one of that name: its calls are as they were")
	}
	return out
}
