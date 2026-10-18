package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// An import finishes what a pack declares but cannot carry. A pack brings
// the declaration of a source and names no file and no user; --source-env
// <source>=<file> sets the file the source's credential is read from, and
// --notify <user>=<source>[,<envfile>] tells the user through it, as yardr
// source set and yardr user set do. Both are for a source the plan
// declares, and both are repeatable, the last winning for one source or
// user. What the actor may not set is skipped, with why, and the rest of
// the import is done.

// sourceEnv is one --source-env <source>=<file>.
type sourceEnv struct{ source, file string }

// notifyTo is one --notify <user>=<source>[,<envfile>].
type notifyTo struct{ user, source, file string }

// notifySets is what the flags set for the sources the plan declares: per
// source, what the import set and what it did not, each as the line the
// result prints.
type notifySets struct {
	Set  map[string][]string `json:"set,omitempty"`
	Skip map[string][]string `json:"skip,omitempty"`
}

func (s *notifySets) add(set bool, src, line string) {
	if set {
		if s.Set == nil {
			s.Set = map[string][]string{}
		}
		s.Set[src] = append(s.Set[src], line)
		return
	}
	if s.Skip == nil {
		s.Skip = map[string][]string{}
	}
	s.Skip[src] = append(s.Skip[src], line)
}

// planNotify checks the flags against the plan and the yard: the form,
// the sources the plan declares, the users, and the files with what each
// sets, as the set commands check them. Everything a set needs is checked
// before the import writes; who may set it is decided when it runs.
func planNotify(ctx context.Context, st store.API, p *pack.Pack, envFlags, notifyFlags []string) ([]sourceEnv, []notifyTo, error) {
	decls := map[string]*source.Declaration{}
	for _, d := range p.Declared {
		decls[d.Name] = d.Declaration
	}
	decl := func(flag, kv, name string) (*source.Declaration, error) {
		d, ok := decls[name]
		if !ok {
			return nil, fmt.Errorf("%s %q: the pack declares no source %s", flag, kv, name)
		}
		if d == nil {
			return nil, fmt.Errorf("%s %q: the pack has no declaration for the source %s: read it again", flag, kv, name)
		}
		return d, nil
	}
	var envs []sourceEnv
	for _, kv := range envFlags {
		name, file, ok := strings.Cut(kv, "=")
		if !ok || name == "" || file == "" {
			return nil, nil, fmt.Errorf("--source-env %q: want <source>=<file>", kv)
		}
		d, err := decl("--source-env", kv, name)
		if err != nil {
			return nil, nil, err
		}
		if d.Credential == nil {
			return nil, nil, fmt.Errorf("--source-env %s=%s: the declaration names no credential, so there is nothing a file would set", name, file)
		}
		if file, err = config.ExpandPath(file); err != nil {
			return nil, nil, err
		}
		// As yardr source set checks it: the file is refused here, and a
		// mint without its command is the next step, not a refusal.
		if _, err := d.CredentialState(file, nil); err != nil {
			var noCommand *source.NoMintCommand
			if !errors.As(err, &noCommand) {
				return nil, nil, fmt.Errorf("--source-env %s=%s: %w", name, file, err)
			}
		}
		envs = append(envs, sourceEnv{name, file})
	}
	var tos []notifyTo
	for _, kv := range notifyFlags {
		user, rest, ok := strings.Cut(kv, "=")
		if !ok || user == "" || rest == "" {
			return nil, nil, fmt.Errorf("--notify %q: want <user>=<source>[,<envfile>]", kv)
		}
		name, file, _ := strings.Cut(rest, ",")
		if name == "" {
			return nil, nil, fmt.Errorf("--notify %q: want <user>=<source>[,<envfile>]", kv)
		}
		d, err := decl("--notify", kv, name)
		if err != nil {
			return nil, nil, err
		}
		if d.Notify == nil {
			return nil, nil, fmt.Errorf("--notify %s=%s: the source cannot tell a user: its file has no notify key", user, name)
		}
		if _, err := st.GetUser(ctx, user); err != nil {
			return nil, nil, fmt.Errorf("--notify %s=%s: no user %s (yardr user add %s)", user, name, user, user)
		}
		if file != "" {
			if file, err = config.ExpandPath(file); err != nil {
				return nil, nil, err
			}
			// As yardr user set checks it: a file that is open to
			// others, or does not set the address, is refused here.
			f, err := config.ReadEnvFile(file)
			if err != nil {
				return nil, nil, fmt.Errorf("--notify %s=%s: %w", user, name, err)
			}
			if source.Lookup(f.Env, d.Notify.AddressEnv) == "" {
				return nil, nil, fmt.Errorf("--notify %s=%s: user %s: env_file %s does not set %s", user, name, user, f.Path, d.Notify.AddressEnv)
			}
		}
		tos = append(tos, notifyTo{user, name, file})
	}
	return envs, tos, nil
}

// applyNotify sets what planNotify planned, after the import wrote its
// objects, so the sources are there to be set. What the actor may not set
// is skipped, with why, and the rest is done. A set that fails stops the
// import where it is: what was set before stays, and running the command
// again finishes it. A set that is already as it would be writes nothing.
func applyNotify(ctx context.Context, st store.API, plan *pack.Plan, actor string, refused map[string]string, envs []sourceEnv, tos []notifyTo) (*notifySets, error) {
	sets := &notifySets{}
	settings, err := st.Settings(ctx)
	if err != nil {
		return sets, err
	}
	operator := settings["operator"]
	for _, e := range envs {
		if why := refused[e.source]; why != "" {
			sets.add(false, e.source, fmt.Sprintf("the env_file of source %s was not set: the source was not added (%s)", e.source, why))
			continue
		}
		// As adding a source: setting where a credential is read from
		// is the operator's, and the rest of the import is done.
		if operator != "" && actor != operator {
			sets.add(false, e.source, fmt.Sprintf("the env_file of source %s was not set: setting a source's credential is the operator's (%s), and this is %s. The rest of the import does not depend on it", e.source, operator, actor))
			continue
		}
		src, err := st.GetSource(ctx, e.source)
		if err != nil {
			return sets, fmt.Errorf("source %s: %w", e.source, err)
		}
		if src.EnvFile == e.file {
			state, err := sourceSetState(src)
			if err != nil {
				return sets, err
			}
			sets.add(true, e.source, fmt.Sprintf("source %s: %s", src.Name, state))
			continue
		}
		saved, err := st.SetSource(ctx, e.source, store.SourceEnvFile, e.file, actor)
		if err != nil {
			return sets, fmt.Errorf("source %s: %w", e.source, err)
		}
		state, err := sourceSetState(saved)
		if err != nil {
			return sets, err
		}
		sets.add(true, e.source, fmt.Sprintf("source %s: %s", saved.Name, state))
	}
	for _, n := range tos {
		if why := refused[n.source]; why != "" {
			sets.add(false, n.source, fmt.Sprintf("user %s was not told through %s: the source was not added (%s)", n.user, n.source, why))
			continue
		}
		// As yardr user set holds it: the user or the operator, never
		// from an agent's pane, which the store refuses at the set.
		if actor != n.user && (operator == "" || actor != operator) {
			why := "where " + n.user + " is told outside the yard is set by " + n.user
			if operator != "" && operator != n.user {
				why += ", or by the operator (" + operator + ")"
			}
			why += ": what lands in their inbox goes there"
			sets.add(false, n.source, fmt.Sprintf("user %s is not told through %s: %s; this is %s. The rest of the import does not depend on it", n.user, n.source, why, or(actor, "nobody")))
			continue
		}
		u, err := st.GetUser(ctx, n.user)
		if err != nil {
			return sets, err
		}
		if u.Notify == n.source && (n.file == "" || u.EnvFile == n.file) {
			sets.add(true, n.source, fmt.Sprintf("user %s: told outside the yard: %s", u.Name, or(told(ctx, st, u), "nowhere")))
			continue
		}
		set := map[string]string{store.UserNotify: n.source}
		if n.file != "" {
			set[store.UserEnvFile] = n.file
		}
		saved, err := st.SetUser(ctx, n.user, set, actor)
		if err != nil {
			return sets, fmt.Errorf("user %s: %w", n.user, err)
		}
		sets.add(true, n.source, fmt.Sprintf("user %s: told outside the yard: %s", saved.Name, or(told(ctx, st, saved), "nowhere")))
	}
	return sets, nil
}

// sourceSetState is the source's credential state as yardr source set
// prints it: where the names are set, and the mint command where one is
// still owed.
func sourceSetState(src *store.Source) (string, error) {
	d, err := src.Declaration()
	if err != nil {
		return "", err
	}
	state, err := d.CredentialState(src.EnvFile, src.Mint)
	if err != nil {
		var noCommand *source.NoMintCommand
		if !errors.As(err, &noCommand) {
			return "", fmt.Errorf("source %s: %w", src.Name, err)
		}
		state += "; " + err.Error()
	}
	return state, nil
}
