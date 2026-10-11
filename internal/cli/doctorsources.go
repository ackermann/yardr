package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// sourceCallsRead is how many of the yard's last source_call events doctor
// reads to say how each source's last call ended.
const sourceCallsRead = 500

// sourcesPart is what doctor says of the yard's declared sources: which it
// has, and what keeps a call of one from being made or answered: the file
// its credential is read from is missing, open to others or does not set
// the name; the service refused the credential at the last call; nobody is
// granted a call. It makes no call unless try: then the check call of each
// source that names one, as yardr source check makes it, with the
// credential read into this process and nothing of it written anywhere.
func sourcesPart(ctx context.Context, cfg *config.Config, st store.API, try bool) (optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	part := optionalPart{Key: "sources", Title: "sources",
		Detail: fmt.Sprintf("no service outside the yard is declared, so no agent calls one through the yard; %s source add <file> (%s source --help, docs/integrations.md)", yardr, yardr)}
	srcs, err := st.Sources(ctx)
	if err != nil || len(srcs) == 0 {
		return part, err
	}
	grants, err := st.SourceGrants(ctx, "")
	if err != nil {
		return part, err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return part, err
	}
	last, err := lastAnswers(ctx, st)
	if err != nil {
		return part, err
	}
	var names []string
	for _, src := range srcs {
		d, err := src.Declaration()
		if err != nil {
			names = append(names, src.Name)
			part.Notes = append(part.Notes, fmt.Sprintf("source %s: its file is not read by this yardr (%s source show %s)", src.Name, yardr, src.Name))
			continue
		}
		names = append(names, fmt.Sprintf("%s (%s)", d.Name, plural(len(d.Calls), "call")))
		// A source that only says limits has no call anybody makes.
		if len(d.Calls) == 0 {
			continue
		}
		granted := slices.ContainsFunc(grants, func(g store.SourceGrant) bool { return g.Source == d.Name })
		told := slices.ContainsFunc(users, func(u store.User) bool { return u.Notify == d.Name })
		cred, _, credErr := d.ReadCredential(src.EnvFile, source.Read)
		// A file nobody needs is not a note: with no grant or
		// notification the server makes no call of the source, and a
		// crew member holds the key its commands run with. Only its
		// check call wants the source's own file then.
		quiet := credErr != nil && src.EnvFile == "" && !granted && !told && pack.KeyHolder(ctx, st, d) != ""
		if credErr != nil {
			if !quiet {
				// The error names the command, as it is typed for the default home.
				note := fmt.Sprintf("source %s: no call of it can be made: %v", d.Name, credErr)
				part.Notes = append(part.Notes, strings.ReplaceAll(note, ": yardr source set", ": "+yardr+" source set"))
			}
		} else if _, err := d.CredentialState(src.EnvFile); err != nil {
			// A read call can still be made; only its updates lack one.
			part.Notes = append(part.Notes, fmt.Sprintf("source %s: an update call of it cannot be made: %v", d.Name, err))
		}
		tried := false
		switch {
		case !try:
		case credErr != nil:
			part.Notes = append(part.Notes, fmt.Sprintf("not tried: source %s has no credential to make its check call with", d.Name))
		case d.Check == "":
			part.Notes = append(part.Notes, fmt.Sprintf("not tried: the file of source %s names no check call (check: <a read call without parameters>)", d.Name))
		default:
			call, _ := d.Call(d.Check)
			res, err := source.Do(ctx, d, d.Check, nil, cred)
			switch {
			case err != nil:
				part.Notes = append(part.Notes, fmt.Sprintf("tried: source %s: its check call %s (%s) got no answer: %v", d.Name, d.Check, call.Request.Line(), err))
			case !res.OK():
				part.Notes = append(part.Notes, fmt.Sprintf("tried: source %s: its check call %s (%s) was answered %d%s", d.Name, d.Check, call.Request.Line(), res.Status, statusHint(res.Status, d)))
			default:
				tried = true
				part.Notes = append(part.Notes, fmt.Sprintf("tried: source %s: its check call %s (%s) was answered %d in %dms", d.Name, d.Check, call.Request.Line(), res.Status, res.Took.Milliseconds()))
			}
		}
		// A check call answered just now says more than a call made before it.
		if e, ok := last[d.Name]; ok && !tried && credErr == nil {
			if status, _ := e.Data["status"].(float64); status == 401 || status == 403 {
				env := ""
				if d.Credential != nil {
					kind, _ := e.Data["kind"].(string)
					env = fmt.Sprintf(": the service did not take what %s is set to", d.Credential.EnvOf(kind))
				}
				part.Notes = append(part.Notes, fmt.Sprintf("source %s: its last call that was answered (%v, %s) was answered %d%s. %s source check %s asks it again",
					d.Name, e.Data["call"], since(e.At), int(status), env, yardr, d.Name))
			}
		}
		if !granted && !told {
			part.Notes = append(part.Notes, fmt.Sprintf("source %s: nobody is granted a call of it, so only the operator makes one (%s source grant %s %s --group <group>)",
				d.Name, yardr, d.Name, store.GrantRead))
		}
	}
	part.Configured, part.Detail = true, strings.Join(names, ", ")
	return part, nil
}

// lastAnswers is, by source, the last source_call event whose call a
// service answered: one that was refused before it was sent, or got no
// answer, says nothing of the credential.
func lastAnswers(ctx context.Context, st store.API) (map[string]store.Event, error) {
	evs, err := st.EventsOfKind(ctx, "source_call", sourceCallsRead)
	if err != nil {
		return nil, err
	}
	out := map[string]store.Event{}
	for _, e := range evs {
		src, _ := e.Data["source"].(string)
		if _, answered := e.Data["status"].(float64); answered && src != "" {
			out[src] = e // oldest first: the last one stays
		}
	}
	return out, nil
}
