package limits

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"
)

// capturedDarwin is what this Mac answered on 25 Oct 2026: load 6.41 on 10
// cores, no swap, 89% of the volume used.
const capturedDarwinLoadavg = "{ 6.41 5.69 9.29 }\n"

const capturedDarwinSwap = "total = 0.00M  used = 0.00M  free = 0.00M  (encrypted)\n"

const capturedDarwinVmstat = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                    50082.
Pages active:                                 466470.
Pages inactive:                               727220.
Pages speculative:                              3527.
Pages throttled:                                   0.
Pages wired down:                             179653.
Pages purgeable:                                3241.
"Translation faults":                     6630775218.
Pages copy-on-write:                      1363065943.
Pages zero filled:                        1127619762.
Pages reactivated:                          13295547.
Pages purged:                                 553496.
File-backed pages:                            748729.
Anonymous pages:                              448488.
Pages stored in compressor:                   296587.
Pages occupied by compressor:                 111577.
Decompressions:                              3624715.
Compressions:                                6046223.
Pageins:                                    50411680.
Pageouts:                                     310767.
Swapins:                                           0.
Swapouts:                                          0.
`

const capturedDarwinDf = `Filesystem   512-blocks      Used Available Capacity iused     ifree %iused  Mounted on
/dev/disk3s5  965595304 808738448 105947472    89% 6614892 529737360    1%   /System/Volumes/Data
`

// capturedLinux is a /proc and df as a Linux machine answers them: load
// 0.52 on 8 cores, half the swap used, 40% of the volume.
const capturedLinuxLoadavg = "0.52 0.58 0.59 1/456 12345\n"

const capturedLinuxMeminfo = `MemTotal:       16384000 kB
MemFree:          1234567 kB
MemAvailable:     8765432 kB
Buffers:           234567 kB
Cached:           3456789 kB
SwapCached:             0 kB
SwapTotal:        2097148 kB
SwapFree:         1048574 kB
`

const capturedLinuxCpuinfo = `processor	: 0
model name	: QEMU Virtual CPU version 2.5+
processor	: 1
model name	: QEMU Virtual CPU version 2.5+
`

const capturedLinuxDf = `Filesystem     1K-blocks    Used Available Use% Mounted on
/dev/sda1      100000000 40000000  60000000  40% /
`

// machineScript is a machineIO that answers reads from runs and files: the
// stdout a program prints, by its name and arguments, and the text a file
// holds, by its path. A read it names nothing for fails.
type machineScript struct {
	runs  map[string]string
	files map[string]string
}

func (s machineScript) run(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	_ = ctx
	_ = dir
	out, ok := s.runs[strings.Join(append([]string{name}, args...), " ")]
	if !ok {
		return nil, errors.New(name + ": no such program")
	}
	return []byte(out), nil
}

func (s machineScript) readFile(path string) ([]byte, error) {
	out, ok := s.files[path]
	if !ok {
		return nil, errors.New(path + ": no such file")
	}
	return []byte(out), nil
}

func (s machineScript) io() machineIO {
	return machineIO{readFile: s.readFile, run: s.run}
}

// darwinScript answers every macOS read with the captured text above: 10
// cores, 25769803776 bytes of RAM.
func darwinScript() machineScript {
	return machineScript{runs: map[string]string{
		"sysctl -n vm.loadavg":   capturedDarwinLoadavg,
		"sysctl -n hw.ncpu":      "10\n",
		"sysctl -n vm.swapusage": capturedDarwinSwap,
		"sysctl -n hw.memsize":   "25769803776\n",
		"vm_stat":                capturedDarwinVmstat,
		"df .":                   capturedDarwinDf,
	}}
}

// linuxScript answers every Linux read with the captured text above.
func linuxScript() machineScript {
	return machineScript{
		runs: map[string]string{"nproc": "8\n", "df .": capturedLinuxDf},
		files: map[string]string{
			"/proc/loadavg": capturedLinuxLoadavg,
			"/proc/meminfo": capturedLinuxMeminfo,
			"/proc/cpuinfo": capturedLinuxCpuinfo,
		},
	}
}

// The built-in answers the four windows as the script measured them: load
// is the 1-minute average over the core count and may read past 100, swap
// is used over total, disk the volume's used share, mem used RAM over
// total. None carries a target or a reset.
func TestMachineReadsDarwin(t *testing.T) {
	r, err := machineFor(context.Background(), "darwin", darwinScript().io(), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider != "machine" || len(r.Windows) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for i, tc := range []struct {
		label string
		used  float64
	}{
		{"load", 64.1},
		{"swap", 0}, // no swap on the machine the text was captured on
		{"disk", 89},
		{"mem", 50.6},
	} {
		w := r.Windows[i]
		if w.Label != tc.label || w.UsedPercent == nil || *w.UsedPercent != tc.used ||
			w.TargetPercent != nil || w.ResetsAt != nil {
			t.Fatalf("window %d = %+v, want %s at %v with no target and no reset", i, w, tc.label, tc.used)
		}
	}
}

func TestMachineReadsLinux(t *testing.T) {
	r, err := machineFor(context.Background(), "linux", linuxScript().io(), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if r.Provider != "machine" || len(r.Windows) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for i, tc := range []struct {
		label string
		used  float64
	}{
		{"load", 6.5},
		{"swap", 50},
		{"disk", 40},
		{"mem", 46.5},
	} {
		w := r.Windows[i]
		if w.Label != tc.label || w.UsedPercent == nil || *w.UsedPercent != tc.used ||
			w.TargetPercent != nil || w.ResetsAt != nil {
			t.Fatalf("window %d = %+v, want %s at %v with no target and no reset", i, w, tc.label, tc.used)
		}
	}
	// Without nproc the processors /proc/cpuinfo lists are the core
	// count: load 0.52 on 2 is 26.
	s := linuxScript()
	delete(s.runs, "nproc")
	r, err = machineFor(context.Background(), "linux", s.io(), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if *r.Windows[0].UsedPercent != 26 {
		t.Fatalf("load without nproc = %v, want 26", *r.Windows[0].UsedPercent)
	}
}

// A swap given in gigabytes, and a load past the core count, read as the
// script reads them.
func TestMachineReadsUnitsAndOverload(t *testing.T) {
	s := darwinScript()
	s.runs["sysctl -n vm.swapusage"] = "total = 2.00G  used = 512.00M  free = 1536.00M  (encrypted)\n"
	s.runs["sysctl -n vm.loadavg"] = "{ 25.00 20.00 15.00 }\n"
	r, err := machineFor(context.Background(), "darwin", s.io(), "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if *r.Windows[0].UsedPercent != 250 {
		t.Fatalf("load = %v, want 250", *r.Windows[0].UsedPercent)
	}
	if *r.Windows[1].UsedPercent != 25 {
		t.Fatalf("swap = %v, want 25", *r.Windows[1].UsedPercent)
	}
}

// What the machine cannot read is no answer, as for a provider: neither
// Darwin nor Linux, or one read failed.
func TestMachineRefusals(t *testing.T) {
	broken := func(system string, change func(*machineScript)) machineScript {
		var s machineScript
		if system == "darwin" {
			s = darwinScript()
		} else {
			s = linuxScript()
		}
		change(&s)
		return s
	}
	for _, tc := range []struct {
		name   string
		system string
		change func(*machineScript)
		want   string
	}{
		{"no load average on macOS", "darwin", func(s *machineScript) {
			s.runs["sysctl -n vm.loadavg"] = "{ }\n"
		}, "no 1-minute load average"},
		{"no core count on macOS", "darwin", func(s *machineScript) {
			s.runs["sysctl -n hw.ncpu"] = "0\n"
		}, "no core count"},
		{"no swap numbers on macOS", "darwin", func(s *machineScript) {
			s.runs["sysctl -n vm.swapusage"] = "total = 1.00X  used = 0.00M  free = 1.00X\n"
		}, "no swap numbers"},
		{"no memory total on macOS", "darwin", func(s *machineScript) {
			s.runs["sysctl -n hw.memsize"] = "plenty\n"
		}, "no memory total"},
		{"no available memory on macOS", "darwin", func(s *machineScript) {
			s.runs["vm_stat"] = "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 1.\n"
		}, "no available memory"},
		{"no volume share on macOS", "darwin", func(s *machineScript) {
			s.runs["df ."] = "Filesystem 512-blocks Used Available Capacity Mounted on\n"
		}, "no used share for this directory's volume"},
		{"a wrapped device row still reads", "darwin", func(s *machineScript) {
			s.runs["df ."] = "Filesystem   512-blocks Used Available Capacity Mounted on\n/dev/disk3s5\n 965595304 808738448 105947472 89% /System/Volumes/Data\n"
		}, ""},
		{"a program that is not there", "darwin", func(s *machineScript) {
			delete(s.runs, "vm_stat")
		}, "vm_stat: no such program"},
		{"no load average on Linux", "linux", func(s *machineScript) {
			s.files["/proc/loadavg"] = "\n"
		}, "no 1-minute load average"},
		{"no core count on Linux", "linux", func(s *machineScript) {
			delete(s.runs, "nproc")
			s.files["/proc/cpuinfo"] = "model name: QEMU\n"
		}, "no core count"},
		{"no swap numbers on Linux", "linux", func(s *machineScript) {
			s.files["/proc/meminfo"] = "MemTotal: 16384000 kB\nMemAvailable: 8765432 kB\n"
		}, "no swap numbers"},
		{"no available memory on Linux", "linux", func(s *machineScript) {
			s.files["/proc/meminfo"] = "MemTotal: 16384000 kB\nSwapTotal: 2097148 kB\nSwapFree: 1048574 kB\n"
		}, "no available memory"},
		{"no volume share on Linux", "linux", func(s *machineScript) {
			s.runs["df ."] = "\n"
		}, "no used share for this directory's volume"},
	} {
		r, err := machineFor(context.Background(), tc.system, broken(tc.system, tc.change).io(), "/tmp")
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: %v", tc.name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s answered: %+v", tc.name, r)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s = %v, want %q", tc.name, err, tc.want)
		}
	}
	if _, err := machineFor(context.Background(), "windows", darwinScript().io(), "/tmp"); err == nil ||
		err.Error() != "windows is neither macOS nor Linux" {
		t.Errorf("another system = %v", err)
	}
}

// The built-in answers on this machine, as the script's test ran the
// script here: four windows with used shares from 0 and no targets.
func TestMachineReportOnThisMachine(t *testing.T) {
	r, err := MachineReport(context.Background(), t.TempDir())
	switch runtime.GOOS {
	case "darwin", "linux":
		if err != nil {
			t.Fatal(err)
		}
	default:
		if err == nil {
			t.Fatalf("another system answered: %+v", r)
		}
		return
	}
	if r.Provider != "machine" || len(r.Windows) != 4 {
		t.Fatalf("report = %+v", r)
	}
	for i, label := range []string{"load", "swap", "disk", "mem"} {
		w := r.Windows[i]
		if w.Label != label || w.UsedPercent == nil || *w.UsedPercent < 0 ||
			w.TargetPercent != nil || w.ResetsAt != nil {
			t.Fatalf("window %d = %+v, want %s used from 0 with no target and no reset", i, w, label)
		}
	}
	if *r.Windows[1].UsedPercent > 100 || *r.Windows[2].UsedPercent > 100 || *r.Windows[3].UsedPercent > 100 {
		t.Fatalf("a share past 100: %+v", r.Windows)
	}
}

// The built-in resolves when no declared source of its name stands, and
// yields when one does: the name taken wins, whatever the file says.
func TestBuiltinMachineResolvesAndYields(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	as, err := Answerers(ctx, sources{}, home)
	if err != nil || len(as) != 1 || as[0].Source != "machine" || !as[0].Builtin || !as[0].Limits.Self() || as[0].Missing() != nil {
		t.Fatalf("a yard with no source answers %+v, %v", as, err)
	}
	self, ok := SelfAnswerer(as, "machine")
	if !ok {
		t.Fatal("the built-in answers for nothing of its own")
	}
	// Asked for itself it answers in process, as the report of a run is
	// kept: from the source, and when.
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		before := time.Now()
		r, err := self.AskSelf(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if r.Source != "machine" || r.Provider != "machine" || len(r.Windows) != 4 || r.HoldsFor != 30*time.Second ||
			r.AskedAt.Before(before) || r.AskedAt.After(time.Now()) {
			t.Fatalf("report = %+v", r)
		}
	}
	// Asked for itself it answers in process: the command is never run,
	// since there is none.
	p := standIn(t, `echo '{"provider":"machine","windows":[]}'`)
	st := sources{
		{Name: "machine", Text: "name: machine\nlimits:\n  command: [" + p + "]\n"},
	}
	as, err = Answerers(ctx, st, home)
	if err != nil || len(as) != 1 || as[0].Source != "machine" || as[0].Builtin {
		t.Fatalf("a declared machine answers %+v, %v", as, err)
	}
	if _, ok := SelfAnswerer(as, "machine"); !ok {
		t.Fatal("the declared machine answers for nothing of its own")
	}
	// A declared machine without a limits key still takes the name: the
	// built-in yields and nothing answers for the source.
	st = sources{
		{Name: "machine", Text: "name: machine\nbase: https://machine.example.com\n"},
	}
	as, err = Answerers(ctx, st, home)
	if err != nil || len(as) != 0 {
		t.Fatalf("a machine that says no limits answers %+v, %v", as, err)
	}
	if _, ok := SelfAnswerer(as, "machine"); ok {
		t.Fatal("a machine that says no limits answers for itself")
	}
	// Beside a declared source the built-in keeps its place by name.
	st = sources{
		{Name: "quokka", Text: "name: quokka\nlimits:\n  command: [" + p + ", \"{provider}\", --json]\n  kinds: { kimi: moonshot }\n"},
	}
	as, err = Answerers(ctx, st, home)
	if err != nil || len(as) != 2 || as[0].Source != "machine" || !as[0].Builtin || as[1].Source != "quokka" || as[1].Builtin {
		t.Fatalf("Answerers = %+v, %v", as, err)
	}
}
