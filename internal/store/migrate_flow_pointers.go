package store

import (
	"strings"

	bolt "go.etcd.io/bbolt"
)

// migrateFlowPointers points a depot, or a depot's type, at the flow in
// today's words where its pointer names one in those of before schema 10
// (trainFlowName): train -> yardr.epic, from when the repository's file was
// yardr.epic.flow, becomes train -> yardr.train. The pin of a bead that is
// not closed is a pointer of the same kind (resolveFlow looks both up the
// same way) and is rewritten by the same rule. Until v0.7.0 such a name
// was looked up under both words, and that release, which stopped, rewrote
// none: the depot's flows, and the pinned bead's, were not found. A pointer
// or a pin at a flow the store has under the old name is left, since it
// names that flow and resolves (migrateFlows made <depot>.epic, which
// migrateTrains kept). The repository cannot be read here (git does not run
// inside a transaction), so a file still called <name>.epic.flow is not
// looked for: a pointer at it stops resolving until the file has the new
// name. One event says what each pointer and each pin was. It looks on
// every open, not by the store's stamp: a store v0.7.0 opened is stamped as
// this build stamps it, and a second run finds no such pointer or pin.
func migrateFlowPointers(tx *bolt.Tx) error {
	// renamed is the name in today's words for a pointer or pin to rewrite.
	renamed := func(flow string) (string, bool) {
		name, old := trainFlowName(flow)
		return name, old && tx.Bucket(bFlows).Get([]byte(flow)) == nil
	}
	var ptrs []FlowPointer
	was := map[string]any{}
	err := each(tx, bFlowPtrs, "", func(_ []byte, p *FlowPointer) error {
		name, ok := renamed(p.Flow)
		if !ok {
			return nil
		}
		was[p.String()] = map[string]any{"was": p.Flow, "flow": name}
		p.Flow = name
		ptrs = append(ptrs, *p)
		return nil
	})
	if err != nil {
		return err
	}
	pins := map[string]beadRow{}
	pinned := map[string]any{}
	err = each(tx, bBeads, "", func(k []byte, r *beadRow) error {
		pin := PinnedFlow(&r.Bead)
		name, ok := renamed(pin)
		if !ok || r.Status == "closed" {
			return nil
		}
		pinned[r.ID] = map[string]any{"was": pin, "flow": name}
		r.Metadata["flow"] = name
		pins[string(k)] = *r
		return nil
	})
	if err != nil || len(ptrs)+len(pins) == 0 {
		return err
	}
	for _, p := range ptrs {
		if err := put(tx, bFlowPtrs, join(p.Depot, p.Type), p); err != nil {
			return err
		}
	}
	for k, r := range pins {
		if err := put(tx, bBeads, k, r); err != nil {
			return err
		}
	}
	data := map[string]any{"from": "types " + oldTypeTrain + " and " + oldTypeWagon}
	if len(was) > 0 {
		data["pointers"] = was
	}
	if len(pinned) > 0 {
		data["beads"] = pinned
	}
	return (&wtx{Tx: tx}).event("", "flows_migrated", "yardr", data)
}

// trainFlowName is the name a flow of trains or wagons has now, for one
// named in the words of before schema 10, and whether name was: <name>.epic
// is <name>.train and <name>.part is <name>.wagon, and the type's own file
// of a repository, type/epic or type/part, is type/train or type/wagon.
func trainFlowName(name string) (string, bool) {
	for old, typ := range map[string]string{oldTypeTrain: TypeTrain, oldTypeWagon: TypeWagon} {
		if head, ok := strings.CutSuffix(name, "."+old); ok && head != "" {
			return head + "." + typ, true
		}
		if name == "type/"+old {
			return "type/" + typ, true
		}
	}
	return name, false
}
