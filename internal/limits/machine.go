package limits

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/source"
)

// MachineSource is the yard's own machine as a limits source: every yard
// answers machine.load, machine.swap, machine.disk and machine.mem from
// install, with no declared source. The machine is the yard's own body,
// not an outside service, so it is answered in process and not by a
// command of the operator's. A declared source of its name wins over it.
const MachineSource = "machine"

// MachineAbout is what the built-in source says of itself, where a
// declaration has its about.
const MachineAbout = "Load, swap, disk and memory of this machine, as limits windows."

// MachineAnswerer is the yard's own machine as a source that answers for
// itself. home is the yard's home, whose volume the disk window reads.
func MachineAnswerer(home string) Answerer {
	return Answerer{Source: MachineSource, Limits: &source.Limits{}, Home: home, Builtin: true}
}

// MachineDeclaration is the built-in source as a declaration reads: its
// name, what it says of itself, and that it answers limits for itself. It
// has no command: the yard answers it in process.
func MachineDeclaration() *source.Declaration {
	return &source.Declaration{Name: MachineSource, About: MachineAbout, Limits: &source.Limits{Kinds: map[string]string{}}}
}

// MachineReport is what the yard's own machine carries: load, swap, disk
// and mem in order, each how far it is used as a percent, with no target
// and no reset: the thresholds live in the route conditions that name
// them. home is the yard's home, whose volume the disk window reads.
//
// The reads are sysctl, vm_stat and df on macOS, /proc/loadavg,
// /proc/meminfo and df on Linux. What it cannot read is no answer, as
// for a provider: the error says what and why in a line.
func MachineReport(ctx context.Context, home string) (*Report, error) {
	// The bound a limits command runs under: df can hang on a dead
	// mount, and a read that hangs must not hold a pass past it.
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	return machineFor(ctx, runtime.GOOS, machineIO{readFile: os.ReadFile, run: runMachine}, home)
}

// machineIO is how the machine is read: files whole, and programs for
// their stdout, run in dir.
type machineIO struct {
	readFile func(path string) ([]byte, error)
	run      func(ctx context.Context, dir, name string, args ...string) ([]byte, error)
}

// runMachine runs name for its stdout, in dir, with nothing to read. The
// error names the command and what it said on stderr.
func runMachine(ctx context.Context, dir, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		why := strings.TrimSpace(stderr.String())
		if why == "" {
			why = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", strings.Join(append([]string{name}, args...), " "), why)
	}
	return out, nil
}

// machineFor is MachineReport on the system goos names. A system that is
// neither refuses, as does a read that fails: no answer, with why.
func machineFor(ctx context.Context, goos string, io machineIO, home string) (*Report, error) {
	switch goos {
	case "darwin":
		return darwinMachine(ctx, io, home)
	case "linux":
		return linuxMachine(ctx, io, home)
	default:
		return nil, fmt.Errorf("%s is neither macOS nor Linux", goos)
	}
}

// darwinMachine reads the machine through sysctl, vm_stat and df, as the
// script did.
func darwinMachine(ctx context.Context, io machineIO, home string) (*Report, error) {
	sysctl := func(arg string) (string, error) {
		out, err := io.run(ctx, home, "sysctl", "-n", arg)
		if err != nil {
			return "", err
		}
		return string(out), nil
	}
	loadavg, err := sysctl("vm.loadavg")
	if err != nil {
		return nil, err
	}
	avg, err := darwinLoadavg(loadavg)
	if err != nil {
		return nil, err
	}
	coresOut, err := sysctl("hw.ncpu")
	if err != nil {
		return nil, err
	}
	cores, err := coreCount(coresOut)
	if err != nil {
		return nil, err
	}
	swapOut, err := sysctl("vm.swapusage")
	if err != nil {
		return nil, err
	}
	swapTotal, swapUsed, err := darwinSwap(swapOut)
	if err != nil {
		return nil, err
	}
	memOut, err := sysctl("hw.memsize")
	if err != nil {
		return nil, err
	}
	memTotal, err := memsize(memOut)
	if err != nil {
		return nil, err
	}
	vmstat, err := io.run(ctx, home, "vm_stat")
	if err != nil {
		return nil, err
	}
	memAvail, err := vmStatAvail(string(vmstat))
	if err != nil {
		return nil, err
	}
	disk, err := diskUsed(ctx, io, home)
	if err != nil {
		return nil, err
	}
	swap := 0.0
	if swapTotal > 0 {
		swap = swapUsed * 100 / swapTotal
	}
	return machineReport(
		avg*100/float64(cores),
		swap,
		disk,
		float64(memTotal-memAvail)*100/float64(memTotal),
	), nil
}

// linuxMachine reads the machine through /proc, nproc and df, as the
// script did.
func linuxMachine(ctx context.Context, io machineIO, home string) (*Report, error) {
	loadOut, err := io.readFile("/proc/loadavg")
	if err != nil {
		return nil, fmt.Errorf("/proc/loadavg: %w", err)
	}
	avg, err := procLoadavg(string(loadOut))
	if err != nil {
		return nil, err
	}
	cores, err := linuxCores(ctx, io)
	if err != nil {
		return nil, err
	}
	memOut, err := io.readFile("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("/proc/meminfo: %w", err)
	}
	swapTotal, swapFree, err := meminfoSwap(string(memOut))
	if err != nil {
		return nil, err
	}
	memTotal, memAvail, err := meminfoMem(string(memOut))
	if err != nil {
		return nil, err
	}
	disk, err := diskUsed(ctx, io, home)
	if err != nil {
		return nil, err
	}
	swap := 0.0
	if swapTotal > 0 {
		swap = float64(swapTotal-swapFree) * 100 / float64(swapTotal)
	}
	return machineReport(
		avg*100/float64(cores),
		swap,
		disk,
		float64(memTotal-memAvail)*100/float64(memTotal),
	), nil
}

// linuxCores is the core count: nproc, or the processors /proc/cpuinfo
// lists where nproc itself cannot be run, as the script reads them.
func linuxCores(ctx context.Context, io machineIO) (uint64, error) {
	out, err := io.run(ctx, "", "nproc")
	if err == nil {
		return coreCount(string(out))
	}
	cpuinfo, ferr := io.readFile("/proc/cpuinfo")
	if ferr != nil {
		return 0, fmt.Errorf("no core count: %s, and /proc/cpuinfo: %s", err, ferr)
	}
	return cpuinfoCount(string(cpuinfo))
}

// diskUsed is the used share of the volume that holds home, through df
// run there, as the script reads it.
func diskUsed(ctx context.Context, io machineIO, home string) (float64, error) {
	out, err := io.run(ctx, home, "df", ".")
	if err != nil {
		return 0, err
	}
	return dfUsed(string(out))
}

// machineReport is the four windows as the yard keeps them: load, swap,
// disk and mem in order, each a used share to a point, with no target and
// no reset. load may read past 100, as the script's does. The answer holds
// for one pass: the machine is read in process, cheaply, and load moves
// faster than any window a provider reports, so a spiked reading must not
// hold a route's gate shut for the whole of the dispatcher's memory.
func machineReport(load, swap, disk, mem float64) *Report {
	used := func(p float64) *float64 { p = math.Round(p*10) / 10; return &p }
	return &Report{Provider: MachineSource, HoldsFor: 30 * time.Second, Windows: []Window{
		{Label: "load", UsedPercent: used(load)},
		{Label: "swap", UsedPercent: used(swap)},
		{Label: "disk", UsedPercent: used(disk)},
		{Label: "mem", UsedPercent: used(mem)},
	}}
}

// darwinLoadavg is the 1-minute load average of sysctl's vm.loadavg:
// "{ 1.65 1.82 2.10 }", the first of the three numbers.
func darwinLoadavg(out string) (float64, error) {
	for _, f := range strings.Fields(out) {
		if avg, err := strconv.ParseFloat(f, 64); err == nil && !math.IsNaN(avg) && !math.IsInf(avg, 0) && avg >= 0 {
			return avg, nil
		}
	}
	return 0, fmt.Errorf("no 1-minute load average")
}

// coreCount is a core count as sysctl's hw.ncpu and nproc print one: a
// whole number above 0.
func coreCount(out string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("no core count")
	}
	return n, nil
}

// darwinSwap is the total and used swap of sysctl's vm.swapusage, each in
// megabytes whatever unit it is given in: "total = 14336.00M used = ...".
func darwinSwap(out string) (total, used float64, err error) {
	fields := strings.Fields(out)
	at := func(key string) (float64, bool) {
		for i, f := range fields {
			if f == key && i+2 < len(fields) && fields[i+1] == "=" {
				v, ok := swapMB(fields[i+2])
				return v, ok
			}
		}
		return 0, false
	}
	total, ok := at("total")
	if !ok {
		return 0, 0, fmt.Errorf("no swap numbers")
	}
	used, ok = at("used")
	if !ok {
		return 0, 0, fmt.Errorf("no swap numbers")
	}
	return total, used, nil
}

// swapMB is a swap size in megabytes: a number with a K, M, G or T unit,
// as sysctl prints one.
func swapMB(s string) (float64, bool) {
	if len(s) < 2 {
		return 0, false
	}
	v, err := strconv.ParseFloat(s[:len(s)-1], 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, false
	}
	switch s[len(s)-1] {
	case 'K':
		return v / 1024, true
	case 'M':
		return v, true
	case 'G':
		return v * 1024, true
	case 'T':
		return v * 1024 * 1024, true
	}
	return 0, false
}

// memsize is total RAM in bytes, as sysctl's hw.memsize prints it.
func memsize(out string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("no memory total")
	}
	return n, nil
}

// vmStatAvail is the memory still available in bytes: vm_stat's free and
// inactive pages, times the page size its first line names ("page size of
// 16384 bytes"); each count ends in a dot.
func vmStatAvail(out string) (uint64, error) {
	var pagesize, free, inactive uint64
	var sized, freed, inactived bool
	num := func(f string) (uint64, bool) {
		n, err := strconv.ParseUint(strings.TrimSuffix(f, "."), 10, 64)
		return n, err == nil
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		switch {
		case len(fields) > 0 && strings.HasPrefix(line, "Pages free:") && len(fields) >= 3:
			free, freed = num(fields[2])
		case len(fields) > 0 && strings.HasPrefix(line, "Pages inactive:") && len(fields) >= 3:
			inactive, inactived = num(fields[2])
		case strings.Contains(line, "page size of"):
			for i, f := range fields {
				if f == "of" && i+1 < len(fields) {
					pagesize, sized = num(fields[i+1])
				}
			}
		}
	}
	if !sized || !freed || !inactived || pagesize == 0 {
		return 0, fmt.Errorf("no available memory")
	}
	return (free + inactive) * pagesize, nil
}

// dfUsed is the used share of the volume df was run on: the first field
// that ends in % on its last line, the device having maybe wrapped onto a
// line of its own, the header naming the column Capacity on one system
// and Use% on the other.
func dfUsed(out string) (float64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return 0, fmt.Errorf("no used share for this directory's volume")
	}
	for _, f := range strings.Fields(lines[len(lines)-1]) {
		if p, ok := strings.CutSuffix(f, "%"); ok {
			used, err := strconv.ParseFloat(p, 64)
			if err != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 {
				break
			}
			return used, nil
		}
	}
	return 0, fmt.Errorf("no used share for this directory's volume")
}

// procLoadavg is the 1-minute load average of /proc/loadavg: its first
// field.
func procLoadavg(out string) (float64, error) {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return 0, fmt.Errorf("no 1-minute load average")
	}
	avg, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(avg) || math.IsInf(avg, 0) || avg < 0 {
		return 0, fmt.Errorf("no 1-minute load average")
	}
	return avg, nil
}

// cpuinfoCount is the processors /proc/cpuinfo lists: its lines that start
// with "processor".
func cpuinfoCount(out string) (uint64, error) {
	var n uint64
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "processor") {
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no core count")
	}
	return n, nil
}

// meminfo reads /proc/meminfo for key, in kilobytes.
func meminfo(out, key string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == key+":" {
			v, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil || v < 0 {
				return 0, false
			}
			return v, true
		}
	}
	return 0, false
}

// meminfoSwap is total and free swap of /proc/meminfo.
func meminfoSwap(out string) (total, free int64, err error) {
	total, ok := meminfo(out, "SwapTotal")
	if !ok {
		return 0, 0, fmt.Errorf("no swap numbers")
	}
	free, ok = meminfo(out, "SwapFree")
	if !ok {
		return 0, 0, fmt.Errorf("no swap numbers")
	}
	return total, free, nil
}

// meminfoMem is total and available RAM of /proc/meminfo.
func meminfoMem(out string) (total, avail int64, err error) {
	total, ok := meminfo(out, "MemTotal")
	if !ok {
		return 0, 0, fmt.Errorf("no memory total")
	}
	avail, ok = meminfo(out, "MemAvailable")
	if !ok {
		return 0, 0, fmt.Errorf("no available memory")
	}
	return total, avail, nil
}
