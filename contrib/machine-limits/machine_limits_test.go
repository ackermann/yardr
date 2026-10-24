// Package machinelimits tests contrib/machine-limits/machine/limits, the
// limits command of the machine source: load, swap, disk and memory of
// this machine as limits windows.
package machinelimits

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// run runs the script with args, in the environment the core runs a limits
// command with: the caller's PATH and HOME, its stdin closed, in dir.
func run(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("./machine/limits", args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "LC_ALL=de_CH.UTF-8"}
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

type window struct {
	Label         string   `json:"label"`
	UsedPercent   *float64 `json:"used_percent"`
	TargetPercent *float64 `json:"target_percent"`
	ResetsAt      *string  `json:"resets_at"`
}

type report struct {
	Provider string   `json:"provider"`
	Windows  []window `json:"windows"`
}

// The script answers the four windows with their default targets, on this
// machine, as the one JSON object the yard reads.
func TestMachineLimitsAnswersFourWindows(t *testing.T) {
	stdout, stderr, err := run(t, ".")
	if err != nil {
		t.Fatalf("./machine/limits: %v\n%s", err, stderr)
	}
	var r report
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("its answer is no JSON object: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("it printed more than one JSON object:\n%s", stdout)
	}
	if r.Provider != "machine" || len(r.Windows) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for i, tc := range []struct {
		label  string
		target float64
		max    float64 // 0 means no upper bound: load may read past 100
	}{
		{"load", 80, 0},
		{"swap", 85, 0},
		{"disk", 90, 0},
		{"mem", 90, 100},
	} {
		w := r.Windows[i]
		if w.Label != tc.label || w.UsedPercent == nil || *w.UsedPercent < 0 ||
			(tc.max > 0 && *w.UsedPercent > tc.max) ||
			w.TargetPercent == nil || *w.TargetPercent != tc.target || w.ResetsAt != nil {
			t.Fatalf("window %d = %+v, want %s with a used share from 0 and target %v, never resetting", i, w, tc.label, tc.target)
		}
	}
}

// --target sets a window's target; the declaration's are the defaults.
func TestMachineLimitsTargets(t *testing.T) {
	stdout, stderr, err := run(t, ".", "--target", "load=50", "--target", "swap=87.5", "--target", "mem=70")
	if err != nil {
		t.Fatalf("./machine/limits: %v\n%s", err, stderr)
	}
	var r report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("its answer is no JSON object: %v\n%s", err, stdout)
	}
	if got := []float64{*r.Windows[0].TargetPercent, *r.Windows[1].TargetPercent, *r.Windows[2].TargetPercent, *r.Windows[3].TargetPercent}; got[0] != 50 || got[1] != 87.5 || got[2] != 90 || got[3] != 70 {
		t.Fatalf("targets = %v, want [50 87.5 90 70]", got)
	}
	stdout, stderr, err = run(t, ".", "--target", "load=080", "--target", "swap=085.0", "--target", "disk=090", "--target", "mem=090")
	if err != nil {
		t.Fatalf("./machine/limits with padded targets: %v\n%s", err, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("padded targets made invalid JSON: %v\n%s", err, stdout)
	}
	if got := []float64{*r.Windows[0].TargetPercent, *r.Windows[1].TargetPercent, *r.Windows[2].TargetPercent, *r.Windows[3].TargetPercent}; got[0] != 80 || got[1] != 85 || got[2] != 90 || got[3] != 90 {
		t.Fatalf("padded targets = %v, want [80 85 90 90]", got)
	}
	for _, bad := range [][]string{
		{"--target", "load=101"},
		{"--target", "load=-1"},
		{"--target", "load=lots"},
		{"--target", "load="},
		{"--target", "mem=101"},
		{"--target", "mem=-1"},
		{"--target", "mem=lots"},
		{"--target"},
		{"--bogus"},
		{"80"},
	} {
		if stdout, stderr, err := run(t, ".", bad...); err == nil {
			t.Fatalf("%v answered:\n%s", bad, stdout)
		} else if stderr == "" {
			t.Fatalf("%v failed silently", bad)
		}
	}
}

// The shipped declaration is a source that answers for itself, whose
// command is the script with the default targets.
func TestMachineDeclaration(t *testing.T) {
	text, err := os.ReadFile("machine.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse("machine.yaml", text)
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "machine" || d.Limits == nil || !d.Limits.Self() {
		t.Fatalf("declaration = %+v", d)
	}
	want := []string{"machine/limits", "--target", "load=80", "--target", "swap=85", "--target", "disk=90", "--target", "mem=90"}
	if strings.Join(d.Limits.Command, " ") != strings.Join(want, " ") {
		t.Fatalf("command = %q, want %q", d.Limits.Command, want)
	}
	if _, err := os.Stat("machine/limits"); err != nil {
		t.Fatalf("the command's file beside the declaration: %v", err)
	}
}
