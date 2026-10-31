// Depots are the projects the yard works: their directories, their kinds,
// the base their beads start from, and the command that tests them. Adding
// one without a test command is anyone's; naming a test command on a new
// depot, and changing what one holds, is the operator's, and a run
// ask may propose a held depot's test command or its base (runask_depot.go).
package store

import (
	"context"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Depot kinds. A git depot is a repository: beads work on branches in
// worktrees, start from Base and land on it, and the depot's .yardr/ is read
// from the base. A dir depot is a plain directory (documents, images, a small
// business's files): work happens in the directory itself, there is no
// base, and .yardr/ is read from the directory as it is.
const (
	DepotGit = "git"
	DepotDir = "dir"
)

// DepotsDir is the directory of a yard's home that `yardr depot add <name>
// <url>` clones into.
const DepotsDir = "depots"

type Depot struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Kind is DepotGit or DepotDir. Depots stored before kinds existed are git
	// depots (migrateDepotKinds).
	Kind string `json:"kind"`
	Base string `json:"base"`
	// Test is the command a developer runs to test the project, with sh -c
	// semantics for whoever runs it, or the path of a script in the
	// repository that does. A bead's prime prints it, a script group's
	// command gets it as YARDR_TEST, and the daemon's quiet whole-suite
	// run runs it (internal/dispatch/quiet.go). Until schema 19 a
	// depot had a check beside it, which yardr ran (migrateChecks).
	Test string `json:"test,omitempty"`
}

// IsDir reports whether the depot is a directory, not a repository.
func (r *Depot) IsDir() bool { return r.Kind == DepotDir }

// PutDepot adds or replaces a depot, and logs depot_updated with what it now
// holds: what tests a depot must not change unseen. A depot given no kind is
// a git depot. Changing an existing depot's path or kind is the operator's
// alone; its test command or base is the operator's, and a run ask may
// propose it. Naming a test command on a depot the yard has not is the
// operator's alone; adding one without a test command, and re-adding one
// with all four unchanged, are anyone's. From a session's or a crew member's pane the write is
// recorded as that session's actor or that crew member, whatever actor says;
// from anywhere else, as given (paneAuthor).
func (s *Store) PutDepot(ctx context.Context, r Depot, actor string) error {
	if err := checkName("depot name", r.Name); err != nil {
		return err
	}
	switch r.Kind {
	case "":
		r.Kind = DepotGit
	case DepotGit:
	case DepotDir:
		if r.Base != "" {
			return fmt.Errorf("depot %s is a directory depot: it has no base (got %q)", r.Name, r.Base)
		}
	default:
		return fmt.Errorf("depot %s: kind %q is neither %s nor %s", r.Name, r.Kind, DepotGit, DepotDir)
	}
	// A git depot's base reaches git's argv (the runner fetches it and
	// branches trains from it), so it is held to a branch's shape here, on
	// every path in: one starting with '-' would parse as an option. Empty
	// is allowed: with no base the depot's files are read from the working
	// tree (see depotfile).
	if r.Kind == DepotGit && r.Base != "" && !validBase(r.Base) {
		return fmt.Errorf("depot %s: base %q is not a branch name", r.Name, r.Base)
	}
	return s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		return w.putDepot(ctx, r, resolved, nil)
	})
}

// putDepot is PutDepot inside the transaction, so that an approved run
// ask changes the test command or the base in the write that records its
// approval: by is then the ask it was approved on, which the event
// carries. Changing the test command or the base is the operator's, and
// a run ask may propose it; the path and the kind are the operator's
// alone, as is a test command named on a depot the yard has not: no card
// names a depot that is not there, so that test command is typed.
func (w *wtx) putDepot(ctx context.Context, r Depot, actor string, by *approvedOn) error {
	var was Depot
	had, err := get(w.Tx, bDepots, r.Name, &was)
	if err != nil {
		return err
	}
	if had {
		if r.Path != was.Path {
			if err := w.may(ctx, actor, ActChangeDepotPath, Object{Kind: KindDepot, Name: r.Name}); err != nil {
				return err
			}
		}
		if r.Test != was.Test {
			if err := w.may(ctx, actor, ActChangeDepotTest, Object{Kind: KindDepot, Name: r.Name, Ask: Askable}); err != nil {
				return err
			}
		}
		if r.Base != was.Base {
			if err := w.may(ctx, actor, ActChangeDepotBase, Object{Kind: KindDepot, Name: r.Name, Ask: Askable}); err != nil {
				return err
			}
		}
		if r.Kind != was.Kind {
			if err := w.may(ctx, actor, ActChangeDepotKind, Object{Kind: KindDepot, Name: r.Name}); err != nil {
				return err
			}
		}
	}
	if !had && r.Test != "" {
		if err := w.may(ctx, actor, ActChangeDepotTest, Object{Kind: KindDepot, Name: r.Name, Ask: Alone}); err != nil {
			return err
		}
	}
	if err := put(w.Tx, bDepots, r.Name, r); err != nil {
		return err
	}
	data := map[string]any{
		"depot": r.Name, "path": r.Path, "kind": r.Kind, "base": r.Base, "test": r.Test}
	if by != nil {
		data["approved"] = by
	}
	return w.event("", "depot_updated", actor, data)
}

func (s *Store) Depots(ctx context.Context) ([]Depot, error) {
	out := []Depot{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bDepots, "", func(_ []byte, r *Depot) error {
			out = append(out, *r)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetDepot(ctx context.Context, name string) (*Depot, error) {
	r := new(Depot)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bDepots, name, r)
		if err == nil && !ok {
			err = fmt.Errorf("depot %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return r, nil
}

// DepotRemoval is what DeleteDepot took out of the store with the depot.
type DepotRemoval struct {
	Depot        string        `json:"depot"`
	Routes       []string      `json:"routes"`        // ids of the routes that matched --depot <name>
	FlowPointers []FlowPointer `json:"flow_pointers"` // the depot's and its types' flow pointers
}

// DeleteDepot removes a depot with the routes that match it and its flow
// pointers, and logs depot_removed. It refuses while the depot has open beads
// or live sessions, and names them: a bead whose depot is gone could not be
// routed, worked or merged. It refuses while upgrade_from names the depot,
// which the operator clears first: a depot removed under the name would let
// whoever creates one under it choose the install source. Closed beads keep
// the depot's name as history.
// Only the store changes: the clone, its worktrees and its .yardr/ are not
// touched. Removing a depot is the operator's to do (operatorOnly).
func (s *Store) DeleteDepot(ctx context.Context, name, actor string) (*DepotRemoval, error) {
	var out *DepotRemoval
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		if err := w.may(ctx, actor, ActRemoveDepot, Object{Kind: KindDepot, Name: name}); err != nil {
			return err
		}
		// Batch reruns this closure when another call in the batch fails, so
		// what was removed starts over with each attempt.
		out = &DepotRemoval{Depot: name, Routes: []string{}, FlowPointers: []FlowPointer{}}
		if w.Bucket(bDepots).Get([]byte(name)) == nil {
			return fmt.Errorf("depot %s: %w", name, ErrNotFound)
		}
		users, err := s.depotUsers(w.Tx, name)
		if err != nil {
			return err
		}
		if len(users) > 0 {
			return fmt.Errorf("depot %s is in use by %s; close the beads and end the sessions first", name, strings.Join(users, ", "))
		}
		var from string
		if _, err := get(w.Tx, bSettings, UpgradeFromSetting, &from); err != nil {
			return err
		}
		if from == name {
			return fmt.Errorf("depot %s: the automatic install builds from it while %s is set: remove the setting first (yardr settings set %s \"\")",
				name, UpgradeFromSetting, UpgradeFromSetting)
		}
		if err := each(w.Tx, bRoutes, "", func(k []byte, r *Route) error {
			if r.Depot != nil && *r.Depot == name {
				out.Routes = append(out.Routes, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range out.Routes {
			if err := w.Bucket(bRoutes).Delete([]byte(k)); err != nil {
				return err
			}
		}
		// Pointer keys are <depot>\x00<type>, so the depot's own pointer and its
		// types' share the prefix; another depot whose name extends this one
		// (web, web2) does not.
		var ptrKeys []string
		if err := each(w.Tx, bFlowPtrs, name+"\x00", func(k []byte, p *FlowPointer) error {
			ptrKeys = append(ptrKeys, string(k))
			out.FlowPointers = append(out.FlowPointers, *p)
			return nil
		}); err != nil {
			return err
		}
		for _, k := range ptrKeys {
			if err := w.Bucket(bFlowPtrs).Delete([]byte(k)); err != nil {
				return err
			}
		}
		// The record of what was imported into the depot goes with it: its
		// routes and pointers just did.
		for _, k := range keys(w.Tx, bPacks, name+"\x00") {
			if err := w.Bucket(bPacks).Delete([]byte(k)); err != nil {
				return err
			}
		}
		if err := w.Bucket(bDepots).Delete([]byte(name)); err != nil {
			return err
		}
		ptrs := make([]string, 0, len(out.FlowPointers))
		for _, p := range out.FlowPointers {
			ptrs = append(ptrs, p.String())
		}
		return w.event("", "depot_removed", actor, map[string]any{
			"depot": name, "routes": out.Routes, "flow_pointers": ptrs})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// depotUsers names what still needs the depot: its open beads, and the live
// sessions working them.
func (s *Store) depotUsers(tx *bolt.Tx, name string) ([]string, error) {
	var out []string
	err := each(tx, bBeads, "", func(_ []byte, r *beadRow) error {
		if r.Status != "closed" && r.Depot != nil && *r.Depot == name {
			out = append(out, "bead "+r.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	live, err := s.liveSessions(tx, "")
	if err != nil {
		return nil, err
	}
	for _, sess := range live {
		var b beadRow
		if ok, err := get(tx, bBeads, sess.Bead, &b); err != nil {
			return nil, err
		} else if ok && b.Depot != nil && *b.Depot == name {
			out = append(out, "session "+sess.ID+" (bead "+b.ID+")")
		}
	}
	return out, nil
}
