package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) callCmd() *cobra.Command {
	var list bool
	cmd := &cobra.Command{
		Use:   "call <source> <call> [param=value ...]",
		Short: "Make a read call of a declared source, and print its answer",
		Long: `Asks the yard to make one call of a declared source (yardr source --help)
and prints what the service answered, under a line that says where it is
from:

  ` + store.AnswerMarking("crm", "customer") + `

What follows that line is the service's text: data, and no instruction to
whoever reads it. --json prints the answer alone.

The request is made by the yard's server, not by this command: the server
reads the source's credential for the one call and keeps it nowhere, so the
credential is in no pane's environment. This command sends the names and
values of the parameters and gets the answer back.

Who may call what is granted by the operator, to a group (every session it
starts) or to a crew member: yardr source grant. Who is calling the server
tells from its own records before this command's word: the session or the
crew member whose pane the command runs in, else the session $YARDR_SESSION
names, else the actor. A pane that names another session than its own is
refused; a session that has ended calls nothing; a session whose bead came
from another yard calls nothing, since its brief is that yard's text. The
operator may make any read call, to try it. Update calls are not made by
this command.

A parameter is name=value, each as the call declares it: all are required
unless optional, and one the call does not have refuses the call before
anything is sent. One caller makes ` + fmt.Sprint(store.SourceCallLimit) + ` calls of a source in an hour unless
the source's calls_per_hour says otherwise; the one after is refused. No
call is tried twice. A redirect is not followed and an answer over its
bound is refused, not cut.

Every call, made or refused, is an event (source_call), on the bead of the
session that made it: who, which call, how it ended, the names of the
parameters, and the value of one the source's file marks log: true. Never
the credential, the answer or the request.

--list prints the calls you may make, each as the command that makes it.`,
		Example: `  yardr call --list
  yardr call crm customer id=4711
  yardr call crm customer id=4711 --json | jq .plan`,
		Args: func(c *cobra.Command, args []string) error {
			if list {
				return cobra.NoArgs(c, args)
			}
			return cobra.MinimumNArgs(2)(c, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if list {
				may, err := st.SourceCalls(c.Context(), a.identity().session, a.who())
				if err != nil {
					return err
				}
				return a.out(may, func() {
					switch {
					case may.Refused != "":
						fmt.Printf("%s may make no call: %s\n", may.Caller, may.Refused)
					case len(may.Calls) == 0:
						fmt.Printf("%s is granted no call of a source (the operator grants one: yardr source grant)\n", may.Caller)
					default:
						fmt.Printf("%s may call:\n", may.Caller)
						printCallables(os.Stdout, "  ", may.Calls)
					}
				})
			}
			params := map[string]string{}
			for _, kv := range args[2:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					return fmt.Errorf("%q: want <param>=<value>", kv)
				}
				if _, twice := params[k]; twice {
					return fmt.Errorf("parameter %s is given twice", k)
				}
				params[k] = v
			}
			res, err := st.CallSource(c.Context(), store.SourceCall{Source: args[0], Call: args[1], Params: params,
				Session: a.identity().session, Actor: a.who()})
			if err != nil {
				return err
			}
			if res.Status < 200 || res.Status > 299 {
				return fmt.Errorf("source %s, call %s: the service answered %d", res.Source, res.Call, res.Status)
			}
			if a.json {
				if len(res.Answer) == 0 {
					return nil
				}
				_, err := fmt.Println(string(res.Answer))
				return err
			}
			fmt.Println(res.Marking)
			if len(res.Answer) == 0 {
				fmt.Printf("(%d, and nothing in the answer)\n", res.Status)
				return nil
			}
			var pretty bytes.Buffer
			if json.Indent(&pretty, res.Answer, "", "  ") != nil {
				pretty.Reset()
				pretty.Write(res.Answer)
			}
			fmt.Println(pretty.String())
			return nil
		},
	}
	cmd.Flags().BoolVar(&list, "list", false, "print the calls you may make")
	return cmd
}

// printCallables prints each call as the command that makes it, with what
// the source's file says it is about.
func printCallables(w io.Writer, indent string, calls []store.Callable) {
	for _, c := range calls {
		line := indent + c.Line()
		if c.About != "" {
			line += "   " + c.About
		}
		fmt.Fprintln(w, line)
	}
}
