// Command bench measures yardr at several yard sizes: how long its commands
// take, how big the store file gets, what the server and the daemon cost
// while nothing happens, how long a bead waits for its session, and how
// writes behave when several clients write at once. README.md says how to
// run it and what each number is.
//
// It runs inside an isolated yard (contrib/isolated-yard), started by the
// script beside it (./bench), and refuses any other home: it stops the
// yard's server and writes the store file itself.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	sizes := flag.String("sizes", "250,2000,20000", "yard sizes to measure, in beads, ascending")
	runs := flag.Int("runs", 20, "runs of each timed command")
	idle := flag.Duration("idle", 5*time.Minute, "how long the yard is left alone for the idle CPU measure")
	workers := flag.Int("workers", 8, "writers of the fill")
	out := flag.String("out", "", "also write the results as JSON to this file")
	flag.Parse()

	d, home, err := isolatedHome()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(2)
	}
	var ns []int
	for _, f := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 || (len(ns) > 0 && n <= ns[len(ns)-1]) {
			fmt.Fprintf(os.Stderr, "bench: -sizes %q: positive numbers, ascending\n", *sizes)
			os.Exit(2)
		}
		ns = append(ns, n)
	}
	// Every yardr this starts acts as the operator, who is a member of the
	// manual groups.
	os.Setenv("YARDR_ACTOR", operator)

	b := &bench{d: d, home: home, runs: *runs, idle: *idle, workers: *workers, ctx: context.Background()}
	res := &Results{Machine: machine(), Started: time.Now()}
	if err := b.setup(); err != nil {
		b.fail(err)
	}
	for _, n := range ns {
		r, err := b.round(n)
		if err != nil {
			b.fail(fmt.Errorf("at %d beads: %w", n, err))
		}
		res.Rounds = append(res.Rounds, *r)
	}
	b.stop()
	res.Took = time.Since(res.Started).Round(time.Second).String()
	if *out != "" {
		js, _ := json.MarshalIndent(res, "", "  ")
		if err := os.WriteFile(*out, append(js, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "bench:", err)
		}
	}
	report(os.Stdout, res)
}

// isolatedHome is the isolated yard this process runs in: its directory D
// and the yard's home D/y. Anything else is refused, before any write: the
// benchmark stops the home's server and daemon and writes its store, which
// must never be a real yard's. An isolated yard is known the way
// isolated-yard down knows one, by its name and the mark `up` left in it,
// and by the caller's herdr not being reachable.
func isolatedHome() (d, home string, err error) {
	home = os.Getenv("YARDR_HOME")
	d = filepath.Dir(home)
	mark, _ := os.ReadFile(filepath.Join(d, ".isolated-yard"))
	switch {
	case home == "":
		return "", "", fmt.Errorf("YARDR_HOME is not set: run contrib/bench/bench, which starts an isolated yard")
	case !strings.HasPrefix(d, "/tmp/iy.") || filepath.Base(home) != "y" || strings.TrimSpace(string(mark)) != d:
		return "", "", fmt.Errorf("YARDR_HOME=%s is not an isolated yard's home: run contrib/bench/bench", home)
	case os.Getenv("HERDR_SOCKET_PATH") != "":
		return "", "", fmt.Errorf("HERDR_SOCKET_PATH is set, so this is not running under %s/e", d)
	}
	return d, home, nil
}

func (b *bench) fail(err error) {
	fmt.Fprintln(os.Stderr, "bench:", err)
	b.stop()
	os.Exit(1)
}

func (b *bench) logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "%s  %s\n", time.Now().Format(time.TimeOnly), fmt.Sprintf(format, args...))
}
