package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// blk builds a block on consecutive lines of one file, n statements each,
// so a made-up suite reads as line numbers.
func blk(line, n int) block {
	return block{file: "p.go", startLine: line, startCol: 1, endLine: line, endCol: 2, numStmt: n}
}

func set(lines ...int) map[block]bool {
	out := map[block]bool{}
	for _, l := range lines {
		out[blk(l, 1)] = true
	}
	return out
}

func TestParseCoverProfile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "p.cover")
	body := "mode: set\n" +
		"yardr.dev/yardr/x/p.go:3.1,5.2 2 1\n" +
		"yardr.dev/yardr/x/p.go:7.1,7.9 1 0\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := parseCoverProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if p.mode != "set" {
		t.Fatalf("mode %q", p.mode)
	}
	if got := totalStatements(p); got != 3 {
		t.Fatalf("total %d, want 3", got)
	}
	covered := coveredSet(p)
	if got := statements(covered); got != 2 {
		t.Fatalf("covered %d statements, want 2", got)
	}
	for _, bad := range []string{"", "no mode line\n", "mode: set\np.go 1 2\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := parseCoverProfile(path); err == nil {
			t.Fatalf("profile %q parses, want an error", bad)
		}
	}
}

func TestGreedyRemoval(t *testing.T) {
	// Slow covers {2,3}, Mid covers {1,2,3}, Quick covers {3}: Slow and
	// Quick are both zero-unique, so the most expensive goes first; once
	// Slow is gone Quick is still zero-unique against Mid and goes
	// second. Mid owns line 1 throughout and stays.
	sets := map[string]map[block]bool{
		"TestSlow":  set(2, 3),
		"TestMid":   set(1, 2, 3),
		"TestQuick": set(3),
	}
	seconds := map[string]float64{"TestSlow": 9, "TestMid": 5, "TestQuick": 1}
	got := greedyRemoval(sets, seconds)
	if want := []string{"TestSlow", "TestQuick"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removal %v, want %v", got, want)
	}
	// The kept tests still run every line the suite ran.
	kept := unionOf(sets, []string{"TestMid"})
	if got := statements(kept); got != 3 {
		t.Fatalf("kept cover %d statements, want 3", got)
	}

	// Twins cover exactly the same lines: both are zero-unique, but only
	// the slower goes; the faster owns the lines once it is alone with them.
	sets = map[string]map[block]bool{
		"TestTwinSlow": set(1, 2),
		"TestTwinFast": set(1, 2),
	}
	seconds = map[string]float64{"TestTwinSlow": 4, "TestTwinFast": 2}
	got = greedyRemoval(sets, seconds)
	if want := []string{"TestTwinSlow"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("twin removal %v, want %v", got, want)
	}

	// Every test owns a line: nothing goes.
	sets = map[string]map[block]bool{
		"TestA": set(1, 2),
		"TestB": set(2, 3),
	}
	seconds = map[string]float64{"TestA": 4, "TestB": 2}
	if got := greedyRemoval(sets, seconds); len(got) != 0 {
		t.Fatalf("removal %v, want none", got)
	}
}

func TestNearDuplicates(t *testing.T) {
	// A and B share 39 of 41 blocks (39/41); C shares 38 of 42 with
	// either (38/42), below the cutoff, and the empty test pairs with
	// nothing.
	var a, b, c []int
	for l := 1; l <= 40; l++ {
		a = append(a, l)
	}
	for l := 1; l <= 39; l++ {
		b = append(b, l)
	}
	b = append(b, 41)
	for l := 1; l <= 38; l++ {
		c = append(c, l)
	}
	c = append(c, 42, 43)
	sets := map[string]map[block]bool{
		"TestA": set(a...), "TestB": set(b...), "TestC": set(c...), "TestEmpty": {},
	}
	pairs := nearDuplicates(sets, similarityCutoff)
	if len(pairs) != 1 || pairs[0].a != "TestA" || pairs[0].b != "TestB" {
		t.Fatalf("pairs %v, want only TestA/TestB", pairs)
	}
	if pairs[0].similarity != 39.0/41.0 {
		t.Fatalf("similarity %v, want %v", pairs[0].similarity, 39.0/41.0)
	}
}

func TestUniqueCounts(t *testing.T) {
	shared := blk(1, 4)
	aOnly := blk(2, 3)
	bOnly := blk(3, 2)
	sets := map[string]map[block]bool{
		"TestA": {shared: true, aOnly: true},
		"TestB": {shared: true, bOnly: true},
	}
	got := uniqueCounts(sets)
	if got["TestA"] != 3 || got["TestB"] != 2 {
		t.Fatalf("unique statements %v, want TestA=3, TestB=2", got)
	}
}
