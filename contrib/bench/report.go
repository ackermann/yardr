package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
)

// growth says how a measure grew from the smallest size to the largest:
// the factor, and whether that is flat, linear in the number of beads, or
// worse. Flat is less than half as much again. Otherwise the growth per
// bead of the last step is set against that of the first: a cost that is a
// fixed part and a part per bead has the same on both, twice as much per
// bead is worse than linear, half is less. Where the first step grew by
// less than a tenth, which is what two runs of the same command differ by,
// it says nothing, and the last step alone is set against the growth of
// the yard.
func growth(sizes []int, vals []float64) string {
	n := len(vals)
	if n < 2 || len(sizes) != n || vals[0] <= 0 || vals[n-1] <= 0 {
		return ""
	}
	factor := vals[n-1] / vals[0]
	word := ""
	switch per := func(i int) float64 { return (vals[i] - vals[i-1]) / float64(sizes[i]-sizes[i-1]) }; {
	case factor < 1/1.5:
		word = "falls"
	case factor < 1.5:
		word = "flat"
	case n > 2 && vals[1]-vals[0] >= vals[0]/10:
		switch q := per(n-1) / per(1); {
		case q > 2:
			word = "worse than linear"
		case q < 0.5:
			word = "less than linear"
		default:
			word = "linear"
		}
	default:
		// How much of the yard's own growth the measure followed.
		if g := (vals[n-1]/vals[n-2] - 1) / (float64(sizes[n-1])/float64(sizes[n-2]) - 1); g > 2 {
			word = "worse than linear"
		} else if g < 0.5 {
			word = "linear or less"
		} else {
			word = "linear"
		}
	}
	return fmt.Sprintf("x%s %s", num(factor), word)
}

// num prints a value with the digits that mean something: three of them,
// and no fraction from 100 up.
func num(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v >= 100 || v <= -100:
		return group(int64(v + 0.5))
	case v >= 10:
		return strconv.FormatFloat(v, 'f', 1, 64)
	case v >= 1:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}

// group prints n with a comma between its thousands.
func group(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// report prints the one table: a row per measure, a column per size, and
// how the measure grew.
func report(w io.Writer, res *Results) {
	var sizes []int
	lo, hi := -1.0, -1.0
	for _, r := range res.Rounds {
		sizes = append(sizes, r.Size)
		for _, l := range []float64{r.Load0, r.Load1} {
			if l >= 0 && (lo < 0 || l < lo) {
				lo = l
			}
			hi = max(hi, l)
		}
	}
	fmt.Fprintf(w, "yardr benchmark: %s, %d cores, %.0f GB; load average %.1f to %.1f; took %s\n\n",
		res.Machine.OS, res.Machine.Cores, res.Machine.MemGB, lo, hi, res.Took)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	head := []string{"beads"}
	for _, n := range sizes {
		head = append(head, group(int64(n)))
	}
	fmt.Fprintln(tw, strings.Join(append(head, "growth"), "\t"))
	// Every line has every column, or tabwriter starts a new table.
	tabs := strings.Repeat("\t", len(sizes)+1)
	section := func(name string) { fmt.Fprintf(tw, "%s\n%s%s\n", tabs, name, tabs) }
	// row prints one measure; pair, when set, is its second number.
	row := func(name string, grows bool, val func(*Round) float64, pair func(*Round) float64) {
		cells, vals := []string{"  " + name}, []float64{}
		for i := range res.Rounds {
			v := val(&res.Rounds[i])
			vals = append(vals, v)
			if pair != nil {
				cells = append(cells, num(v)+" / "+num(pair(&res.Rounds[i])))
			} else {
				cells = append(cells, num(v))
			}
		}
		g := ""
		if grows {
			g = growth(sizes, vals)
		}
		fmt.Fprintln(tw, strings.Join(append(cells, g), "\t"))
	}
	one := func(name string, val func(*Round) float64) { row(name, true, val, nil) }

	section("the yard")
	count := func(name string, val func(*Round) int) {
		cells := []string{"  " + name}
		for i := range res.Rounds {
			cells = append(cells, group(int64(val(&res.Rounds[i]))))
		}
		fmt.Fprintln(tw, strings.Join(append(cells, ""), "\t"))
	}
	count("beads when timed", func(r *Round) int { return r.Beads })
	count("events", func(r *Round) int { return int(r.Events) })
	count("live sessions", func(r *Round) int { return r.Sessions })
	row("fill, seconds", false, func(r *Round) float64 { return r.Fill.Secs }, nil)

	section("commands, ms: median / p95")
	for _, name := range timedOrder {
		row(name, true, func(r *Round) float64 { return r.Timed[name].Med }, func(r *Round) float64 { return r.Timed[name].P95 })
	}

	section("dispatch, ms after the advance: median / p95")
	row("claimed", true, func(r *Round) float64 { return r.Dispatch.Claimed.Med }, func(r *Round) float64 { return r.Dispatch.Claimed.P95 })
	row("session started", true, func(r *Round) float64 { return r.Dispatch.Started.Med }, func(r *Round) float64 { return r.Dispatch.Started.P95 })

	section("store file, MB")
	one("yard.db", func(r *Round) float64 { return r.StoreMB })
	one("  of it in use", func(r *Round) float64 { return r.Engine.UsedMB })
	one("  compacted", func(r *Round) float64 { return r.Engine.CompactedMB })
	one("all closed and pruned", func(r *Round) float64 { return r.Engine.PrunedMB })
	one("  of it in use", func(r *Round) float64 { return r.Engine.PrunedUsedMB })
	one("  compacted", func(r *Round) float64 { return r.Engine.PrunedCompactedMB })

	section("memory, MB RSS")
	for _, p := range []string{"server", "daemon", "web"} {
		one(p, func(r *Round) float64 { return r.Procs[p].RSSMB })
	}
	idle := ""
	if len(res.Rounds) > 0 {
		idle = fmt.Sprintf(" in %.0f idle seconds", res.Rounds[0].IdleSecs)
	}
	section("CPU seconds" + idle)
	for _, p := range []string{"server", "daemon", "web"} {
		one(p, func(r *Round) float64 { return r.IdleCPU[p].Med })
	}

	section("writers at once: per second / p95 ms")
	loads := func(what string, of func(*Round) []Load) {
		if len(res.Rounds) == 0 {
			return
		}
		for i, l := range of(&res.Rounds[0]) {
			row(fmt.Sprintf("%s, %d", what, l.Clients), true,
				func(r *Round) float64 { return of(r)[i].PerSec }, func(r *Round) float64 { return of(r)[i].P95 })
		}
	}
	loads("bead note", func(r *Round) []Load { return r.Notes })
	loads("bead create", func(r *Round) []Load { return r.Creates })
	row("ready while 16 write notes, ms: median / p95", true, func(r *Round) float64 { return r.ReadyUnder.Med }, func(r *Round) float64 { return r.ReadyUnder.P95 })

	section("in this process, on a copy, ms")
	one("bbolt walks the beads bucket", func(r *Round) float64 { return r.Engine.ScanBeadsMs })
	one("store.ListBeads, all", func(r *Round) float64 { return r.Engine.ListAllMs })
	one("store.ListBeads, open", func(r *Round) float64 { return r.Engine.ListOpenMs })
	one("store.Ready", func(r *Round) float64 { return r.Engine.ReadyMs })
	one("store.GetBead", func(r *Round) float64 { return r.Engine.GetBeadMs })
	row("store.AddNote, synced: median / p95", true, func(r *Round) float64 { return r.Engine.NoteSyncMs.Med }, func(r *Round) float64 { return r.Engine.NoteSyncMs.P95 })
	row("store.AddNote, not synced: median / p95", true, func(r *Round) float64 { return r.Engine.NoteNoSyncMs.Med }, func(r *Round) float64 { return r.Engine.NoteNoSyncMs.P95 })
	tw.Flush()

	if len(res.Rounds) == 0 {
		return
	}
	last := res.Rounds[len(res.Rounds)-1]
	fmt.Fprintf(w, "\nthe store file at %s beads, by bucket (%s MB in all; pruning removed %s events and %s sessions)\n",
		group(int64(last.Size)), num(last.Engine.BackupMB), group(int64(last.Engine.PrunedEvents)), group(int64(last.Engine.PrunedSessions)))
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, bk := range last.Engine.Buckets {
		fmt.Fprintf(tw, "  %s\t%s keys\t%s MB\n", bk.Name, group(int64(bk.Keys)), num(bk.MB))
	}
	tw.Flush()
	if last.Engine.HeldBy != "" {
		fmt.Fprintf(w, "  the prune kept events back: %s\n", last.Engine.HeldBy)
	}
}
