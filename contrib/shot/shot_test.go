package shot

// These tests run the reference screenshot script against pages of their
// own: an httptest server on loopback, never the network and never Dokku.
// The shot itself needs headless Chrome, which the machine running the
// tests may not have, may give no display to draw on (macOS Chrome then
// hangs), or may be too loaded for it to finish; then that test skips, and a
// stand-in Chrome proves the script passes the page through with the right
// flags. The refusals need no Chrome: the script checks the file and the
// page first. Nothing waits on Chrome without a deadline: the script stops
// it after SHOT_TIMEOUT, and the tests stop the script at their context's.

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// page is one local page at /page; every other path is missing.
func page(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/page", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<html><body><h1>shot</h1></body></html>"))
	})
	mux.HandleFunc("/redirect-to-missing", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/missing", http.StatusFound)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

// shotTimeout is the deadline the tests give runShot: a minute, the
// script's own Chrome bound.
const shotTimeout = time.Minute

// shotLoadTimeout is the deadline the window-sized-PNG test gives a
// loaded machine: ten seconds, twenty times a prompt Chrome's half a
// second. Under load that test skips a Chrome that misses its deadline,
// so the full minute would be spent only to skip (yardr-c2vt: 65-72 s
// under -race on a loaded machine, 6 s quiet).
const shotLoadTimeout = 10 * time.Second

// shotCtx gives runShot its deadline: the tests never wait on the script
// past a minute, whatever Chrome does.
func shotCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), shotTimeout)
	t.Cleanup(cancel)
	return ctx
}

// windowTimeout is the deadline TestShotWritesAWindowSizedPNG gives the
// shot: the full minute when quiet, short under load, where a stalled
// Chrome skips instead of failing.
func windowTimeout() time.Duration {
	if underLoad() {
		return shotLoadTimeout
	}
	return shotTimeout
}

// runShot runs the script with args and the environment added to its own.
// The script's Chrome can hang under load, so ctx carries the deadline: at
// it the script's process group is killed whole (a wrapper script's child
// would otherwise keep the output pipe open past the timeout) and runShot
// returns what was printed so far with the reason, in the script's own
// Chrome-timeout words.
func runShot(ctx context.Context, t *testing.T, args []string, env ...string) (int, string) {
	t.Helper()
	rc, out, err := runShotCmd(ctx, args, env...)
	if err != nil {
		t.Fatalf("shot %v: %v", args, err)
	}
	return rc, out
}

// runShotCmd is runShot without the test: a caller that arms the deadline
// after the stand-in is ready runs the script ahead of that, from another
// goroutine, where the test must not fail.
func runShotCmd(ctx context.Context, args []string, env ...string) (int, string, error) {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "./shot", args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return 1, string(out) + "shot: chrome did not finish within " + timeoutSecs(ctx, start) + "\n", nil
	}
	rc := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			rc = exit.ExitCode()
		} else {
			return 0, "", err
		}
	}
	return rc, string(out), nil
}

// timeoutSecs words ctx's deadline as whole seconds ("60s"), the script's
// SHOT_TIMEOUT shape, so a timeout from either reads the same. A context
// with no deadline words the time the run took instead.
func timeoutSecs(ctx context.Context, start time.Time) string {
	d := time.Since(start)
	if deadline, ok := ctx.Deadline(); ok {
		d = deadline.Sub(start)
	}
	if s := int(d.Seconds() + 0.5); s > 0 {
		return strconv.Itoa(s) + "s"
	}
	return "0s"
}

// underLoad reports whether a slow Chrome is the machine's fault, not the
// shot's: the landing suite, and any run with the race detector, load the
// machine enough that headless Chrome can miss its minute, and so does
// whatever else the machine is doing, which its load average shows
// (machineLoaded): a plain full run beside other work is as slow.
func underLoad() bool {
	return os.Getenv("YARDR_LANDING") != "" || race() || machineLoaded()
}

// machineLoaded reports whether the machine is too loaded for a timing
// verdict by the gate's own measure (loadedmachine in .yardr/test, rule R1
// in docs/testing.md): its one-minute load average is past its core count.
// Where the load cannot be read it is not loaded: an unknown load excuses no
// hang, as it passes no package there.
func machineLoaded() bool {
	avg, cores, ok := loadReading()
	return ok && avg > cores
}

// loadReading is the one-minute load average and the core count as the gate
// takes them: /proc/loadavg where the system keeps it, sysctl's vm.loadavg
// elsewhere, and the cores Go counts (its nproc and hw.ncpu). Like there,
// YARDR_TEST_LOADAVG and YARDR_TEST_CORES stand in for both, so a test can
// pin the machine's load; with one set and not the other the reading is
// unknown, as there. ok is false when either cannot be read.
func loadReading() (avg, cores float64, ok bool) {
	text, n := "", strconv.Itoa(runtime.NumCPU())
	if a, c := os.Getenv("YARDR_TEST_LOADAVG"), os.Getenv("YARDR_TEST_CORES"); a != "" || c != "" {
		text, n = a, c
	} else if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		text = string(b)
	} else if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		text = string(out)
	}
	avg, ok = loadAverage(text)
	cores, err := strconv.ParseFloat(n, 64)
	return avg, cores, ok && err == nil && cores > 0
}

// loadAverage is the first number of what the system prints for the load:
// /proc/loadavg ("0.52 0.58 0.59 1/467 12345"), sysctl -n vm.loadavg on
// darwin ("{ 3.52 3.61 3.40 }"), or a bare number.
func loadAverage(text string) (float64, bool) {
	f := strings.Fields(strings.Trim(text, "{} \n"))
	if len(f) == 0 {
		return 0, false
	}
	avg, err := strconv.ParseFloat(f[0], 64)
	return avg, err == nil
}

// race reports whether the test binary was built with the race detector,
// read from its build info.
func race() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

// standin is a SHOT_CHROME that shoots nothing: it records its arguments
// and writes want to the --screenshot target, so the test sees what flags
// the script passed and what file it named.
func standin(t *testing.T, want string, exitCode int) (env []string, args string) {
	t.Helper()
	dir := t.TempDir()
	args = filepath.Join(dir, "args")
	bin := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\n" +
		"echo \"$@\" >" + args + "\n" +
		"for a in \"$@\"; do case \"$a\" in --screenshot=*) out=${a#--screenshot=};; esac; done\n"
	if want != "" {
		script += "printf '%s' '" + want + "' >\"$out\"\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"SHOT_CHROME=" + bin}, args
}

func TestShotIsExecutable(t *testing.T) {
	info, err := os.Stat("shot")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("contrib/shot/shot must be executable")
	}
}

func TestShotUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"http://127.0.0.1/"}, {"a", "b", "c"}} {
		if rc, out := runShot(shotCtx(t), t, args); rc != 2 || !strings.Contains(out, "shot <url> <file>") {
			t.Errorf("shot %v: exit %d, %q, want exit 2 with the usage", args, rc, out)
		}
	}
}

// The script passes the page to Chrome with its flags, and removes the
// throwaway profile afterwards.
func TestShotPassesFlagsToChrome(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, argsFile := standin(t, "shot", 0)
	out := filepath.Join(t.TempDir(), "shot.png")
	rc, outText := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc != 0 {
		t.Fatalf("shot: exit %d: %s", rc, outText)
	}
	got, err := os.ReadFile(out)
	if err != nil || string(got) != "shot" {
		t.Fatalf("shot wrote %q, %v, want the stand-in's bytes", got, err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	args := string(raw)
	for _, want := range []string{"--headless", "--disable-gpu",
		"--screenshot=" + out, "--window-size=1280,800", s.URL + "/page"} {
		if !strings.Contains(args, want) {
			t.Errorf("Chrome was passed %q, which lacks %s", args, want)
		}
	}
	prof := ""
	for _, field := range strings.Fields(args) {
		if rest, ok := strings.CutPrefix(field, "--user-data-dir="); ok {
			prof = rest
		}
	}
	if prof == "" {
		t.Fatalf("Chrome was passed %q, with no --user-data-dir", args)
	}
	if _, err := os.Stat(prof); !os.IsNotExist(err) {
		t.Errorf("the throwaway profile %s is still there: %v", prof, err)
	}
}

// A missing page is refused with its reason, and Chrome never runs.
func TestShotRefusesAMissingPage(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	// A port nothing listens on: listened on and closed.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	shut := l.Addr().String()
	l.Close()
	for name, url := range map[string]string{
		"a path the server does not have": s.URL + "/missing",
		"a redirect to a missing path":    s.URL + "/redirect-to-missing",
		"a port nothing listens on":       "http://" + shut + "/page",
	} {
		t.Run(name, func(t *testing.T) {
			env, argsFile := standin(t, "shot", 0)
			out := filepath.Join(t.TempDir(), "shot.png")
			rc, text := runShot(shotCtx(t), t, []string{url, out}, env...)
			if rc == 0 {
				t.Fatalf("shot %s: exit 0, want a refusal", url)
			}
			if !strings.Contains(text, url) {
				t.Errorf("shot %s: %q, which does not name the page", url, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a missing page")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a shot of a missing page is at %s", out)
			}
		})
	}
	if _, text := runShot(shotCtx(t), t, []string{s.URL + "/missing", filepath.Join(t.TempDir(), "shot.png")}); !strings.Contains(text, "404") {
		t.Errorf("a missing path is refused with %q, which carries no reason", text)
	}
}

// An unwritable file is refused with its reason, and Chrome never runs.
func TestShotRefusesAnUnwritableFile(t *testing.T) {
	s := page(t)
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	nowrite := filepath.Join(dir, "nowrite")
	if err := os.Mkdir(nowrite, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(nowrite, 0o755) })
	readonly := filepath.Join(dir, "readonly.png")
	if err := os.WriteFile(readonly, []byte("old shot"), 0o444); err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"a directory that does not exist": filepath.Join(dir, "gone", "shot.png"),
		"a directory":                     sub,
		"a directory not writable":        filepath.Join(nowrite, "shot.png"),
		"an existing file not writable":   readonly,
	} {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(name, "not writable") && os.Geteuid() == 0 {
				t.Skip("root writes anywhere")
			}
			env, argsFile := standin(t, "shot", 0)
			rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
			if rc == 0 {
				t.Fatalf("shot to %s: exit 0, want a refusal", out)
			}
			if !strings.Contains(text, out) {
				t.Errorf("shot to %s: %q, which does not name the file", out, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for an unwritable file")
			}
		})
	}
}

// A nonzero Chrome exit is a failed shot even if it left a non-empty file.
func TestShotReportsChromeFailure(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, _ := standin(t, "partial shot", 7)
	out := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(out, []byte("stale shot"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc != 7 || !strings.Contains(text, "Chrome failed with exit 7") {
		t.Errorf("failed Chrome shot: exit %d, %q, want exit 7 and its reason", rc, text)
	}
}

// A successful Chrome exit that writes nothing is still a failed shot, even
// when the destination held an older screenshot.
func TestShotRefusesAnEmptyShot(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	env, _ := standin(t, "", 0)
	out := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(out, []byte("stale shot"), 0o644); err != nil {
		t.Fatal(err)
	}
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
	if rc == 0 || !strings.Contains(text, "Chrome shot nothing") {
		t.Errorf("empty Chrome shot: exit %d, %q, want an empty-shot refusal", rc, text)
	}
	if got, err := os.ReadFile(out); err != nil || len(got) != 0 {
		t.Errorf("old screenshot remains after an empty shot: %q, %v", got, err)
	}
}

// SHOT_CHROME names the binary; one that is not there is refused with its
// reason.
func TestShotRefusesAnUnusableChrome(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, "SHOT_CHROME="+filepath.Join(t.TempDir(), "gone"))
	if rc == 0 || !strings.Contains(text, "SHOT_CHROME=") || !strings.Contains(text, "is not executable") {
		t.Errorf("an unusable SHOT_CHROME: exit %d, %q", rc, text)
	}
}

// pngSize reads the dimensions of a PNG from its header.
func pngSize(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) < 24 || !bytes.Equal(raw[:8], []byte("\x89PNG\r\n\x1a\n")) || string(raw[12:16]) != "IHDR" {
		t.Fatalf("%s is %d bytes, not a PNG", path, len(raw))
	}
	return binary.BigEndian.Uint32(raw[16:20]), binary.BigEndian.Uint32(raw[20:24])
}

// displayLinkFailure is the line headless Chrome on macOS prints, over and
// over, when it cannot make a display link for the host's display
// ("CVDisplayLinkCreateWithCGDisplay failed. CVReturn: -6670"), and then
// does not finish, so the script's deadline ends the shot (yardr-zkfj). That
// is the host's to give, not the script's: a shot that times out beside it
// is skipped however quiet the machine.
const displayLinkFailure = "CVDisplayLinkCreateWithCGDisplay failed"

// verdict is what TestShotWritesAWindowSizedPNG makes of one run of the script.
type verdict int

const (
	shotMade    verdict = iota // the script exited 0: the PNG is there to check
	shotSkipped                // the host or the load, not the shot, is why it is not
	shotFailed                 // the script is at fault
)

// windowVerdict judges a run of the script that exited rc and printed text,
// on a machine that is loaded or not, and says why unless the shot was made.
// A Chrome that missed its deadline is skipped when it printed the display
// link failure, or when the machine is loaded; one that misses it silently
// on a quiet machine is a real hang, and fails. A Chrome that failed any
// other way fails the shot, loaded or not.
func windowVerdict(rc int, text string, loaded bool) (verdict, string) {
	switch {
	case rc != 0 && strings.Contains(text, "no headless Chrome"):
		return shotSkipped, "no headless Chrome: " + strings.TrimSpace(text)
	case strings.Contains(text, "chrome did not finish within"):
		if line := lineWith(text, displayLinkFailure); line != "" {
			return shotSkipped, "chrome cannot make a display link on this host, and did not finish: " +
				line + "; " + lineWith(text, "chrome did not finish within")
		}
		if loaded {
			return shotSkipped, "chrome missed its deadline under load: " + strings.TrimSpace(text)
		}
	case rc == 0:
		return shotMade, ""
	}
	return shotFailed, fmt.Sprintf("shot: exit %d: %s", rc, text)
}

// lineWith is the first line of text that holds sub, trimmed, or "".
func lineWith(text, sub string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, sub) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// The shot itself, with real Chrome against the local page: a window-sized
// PNG. A machine with no Chrome skips it; the stand-in test above proves
// the flags there. So does a Chrome that cannot make a display link, and
// under load the deadline is short and a Chrome that misses it skips too,
// with the reason (windowVerdict): a slow machine or a host with no display
// is not a failure of the shot code, and the skip must not cost the full
// minute.
func TestShotWritesAWindowSizedPNG(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	out := filepath.Join(t.TempDir(), "shot.png")
	timeout := windowTimeout()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	rc, text := runShot(ctx, t, []string{s.URL + "/page", out},
		"SHOT_TIMEOUT="+strconv.Itoa(int(timeout/time.Second)))
	// The load is read after the shot: a minute's wait is long enough for
	// the machine to have been quiet at its start and busy by its end.
	switch v, why := windowVerdict(rc, text, underLoad()); v {
	case shotSkipped:
		t.Skip(why)
	case shotFailed:
		t.Fatal(why)
	}
	if w, h := pngSize(t, out); w != 1280 || h != 800 {
		t.Errorf("the shot is %dx%d, want 1280x800", w, h)
	}
}

// The verdict on a run that timed out (yardr-zkfj): a Chrome that printed
// the display link failure is skipped, quiet or loaded; one that printed
// nothing of the kind is skipped only on a loaded machine, and fails on a
// quiet one, since a real hang must; and what did not time out is judged as
// before, a Chrome that failed or a shot that was made whatever the load.
func TestWindowVerdict(t *testing.T) {
	const timedOut = "shot: chrome did not finish within 60s\n"
	display := "[1234:5678:1021/090000.000000:ERROR:display_link_mac.cc(1)] " + displayLinkFailure + ". CVReturn: -6670\n"
	for _, c := range []struct {
		name   string
		rc     int
		text   string
		loaded bool
		want   verdict
		why    string // a word the reason must carry
	}{
		{"shot made", 0, "", false, shotMade, ""},
		{"shot made under load", 0, "", true, shotMade, ""},
		{"no Chrome", 1, "shot: no headless Chrome found (looked for chromium)\n", false, shotSkipped, "no headless Chrome"},
		{"display link failure, quiet", 1, display + display + timedOut, false, shotSkipped, "display link"},
		{"display link failure, loaded", 1, display + timedOut, true, shotSkipped, "display link"},
		{"silent hang, quiet", 1, timedOut, false, shotFailed, "did not finish"},
		{"hang with other output, quiet", 1, "chrome is warming up\n" + timedOut, false, shotFailed, "warming up"},
		{"silent hang, loaded", 1, timedOut, true, shotSkipped, "under load"},
		{"Chrome failed, loaded", 7, "shot: http://127.0.0.1/page: Chrome failed with exit 7\n", true, shotFailed, "exit 7"},
		{"display link failure without a timeout", 7, display + "shot: http://127.0.0.1/page: Chrome failed with exit 7\n", false, shotFailed, "exit 7"},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, why := windowVerdict(c.rc, c.text, c.loaded)
			if v != c.want || !strings.Contains(why, c.why) {
				t.Errorf("windowVerdict(%d, %q, loaded=%v) = %d, %q, want %d with %q in the reason", c.rc, c.text, c.loaded, v, why, c.want, c.why)
			}
		})
	}
}

// The verdict on the script's own output, from a stand-in Chrome that hangs
// (yardr-zkfj): one that prints the display link failure first is skipped on
// a quiet machine, and one that prints nothing of the kind still fails
// there. The script's deadline is a second, started once the stand-in is
// ready as in TestShotKillsAChromeThatDoesNotFinish, so the failure line is
// printed before it is.
func TestWindowVerdictOfAHungChrome(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	for _, c := range []struct {
		name, line string
		want       verdict
	}{
		{"prints the display link failure", displayLinkFailure + ". CVReturn: -6670", shotSkipped},
		{"prints nothing", "", shotFailed},
	} {
		t.Run(c.name, func(t *testing.T) {
			env, pids := sleeper(t, c.line, false)
			env = append(env, readySleep(t, pids)...)
			env = append(env, "SHOT_TIMEOUT=1")
			rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", filepath.Join(t.TempDir(), "shot.png")}, env...)
			// Quiet is given, not read: the machine this runs on may be busy.
			if v, why := windowVerdict(rc, text, false); v != c.want {
				t.Errorf("a Chrome that hangs and %s: verdict %d (%s), want %d; exit %d, output %q", c.name, v, why, c.want, rc, text)
			}
			dead(t, pids)
		})
	}
}

// Under load a Chrome that never answers must fail fast: the window-sized
// test skips a stalled Chrome there, so its deadline is short, and the
// package stays inside the 60 s budget (docs/testing.md R1). A sleeper
// stands in for the stalled Chrome; the bound is three times the loaded
// deadline, waited on, not slept for.
func TestShotWindowTimeoutIsShortUnderLoad(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	t.Setenv("YARDR_LANDING", "1")
	if !underLoad() {
		t.Fatal("YARDR_LANDING=1 must read as under load")
	}
	s := page(t)
	env, pids := sleeper(t, "chrome is warming up", false)
	timeout := windowTimeout()
	env = append(env, "SHOT_TIMEOUT="+strconv.Itoa(int(timeout/time.Second)))
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	t.Cleanup(cancel)
	start := time.Now()
	rc, text := runShot(ctx, t, []string{s.URL + "/page", filepath.Join(t.TempDir(), "shot.png")}, env...)
	if rc == 0 || !strings.Contains(text, "chrome did not finish within") {
		t.Fatalf("a Chrome past the loaded deadline: exit %d, %q, want a refusal naming the deadline", rc, text)
	}
	if dt := time.Since(start); dt > 3*shotLoadTimeout {
		t.Errorf("the refusal took %s, want the loaded deadline of %s", dt.Round(time.Millisecond), timeout)
	}
	dead(t, pids)
}

// sleeper is a SHOT_CHROME that prints line and never finishes: it sleeps
// past every deadline the test gives it. It records the PIDs the test must
// find dead afterwards in pids: its own, and with child its sleeper's, so a
// group kill is told from one that took Chrome's own process alone.
func sleeper(t *testing.T, line string, child bool) (env []string, pids string) {
	t.Helper()
	dir := t.TempDir()
	pids = filepath.Join(dir, "pids")
	bin := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\necho '" + line + "'\necho $$ >" + pids + "\n"
	if child {
		script += "sleep 30 & echo $! >>" + pids + "\nwait\n"
	} else {
		script += "exec sleep 30\n"
	}
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"SHOT_CHROME=" + bin}, pids
}

// dead fails the test unless every PID in pids is gone: the kill the
// timeout promises, waited on, not slept for.
func dead(t *testing.T, pids string) {
	deadWithin(t, pids, 10*time.Second)
}

func deadWithin(t *testing.T, pids string, within time.Duration) {
	t.Helper()
	raw, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	giveUp := time.Now().Add(within)
	for _, field := range strings.Fields(string(raw)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 {
			t.Fatalf("the stand-in recorded %q, not a PID", field)
		}
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(giveUp) {
				t.Fatalf("process %d is still alive after %s", pid, within)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// ready waits for the stand-in to be ready: the pids file it writes on
// start, up to within. Under load the stand-in can take seconds to be
// scheduled; the deadline the test then takes must not start before this
// returns. It returns when the file appeared, so the kill after it is told
// from one that took the whole bound.
func ready(t *testing.T, pids string, within time.Duration) time.Time {
	t.Helper()
	giveUp := time.Now().Add(within)
	for {
		if _, err := os.Stat(pids); err == nil {
			return time.Now()
		} else if !os.IsNotExist(err) {
			t.Fatalf("stat %s: %v", pids, err)
		}
		if time.Now().After(giveUp) {
			t.Fatalf("%s did not appear within %s", pids, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readySleep is a sleep for the timeout test: the script's watchdog must
// not start its SHOT_TIMEOUT clock before the stand-in Chrome is ready, so
// this sleep waits for readyFile (the pids file the stand-in writes on
// start), with a generous bound, and then sleeps as asked. Past the bound
// it sleeps anyway, and the test fails on its assertions instead of
// hanging.
func readySleep(t *testing.T, readyFile string) []string {
	t.Helper()
	realSleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep: the shot watchdog uses it")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"i=0\n" +
		"while [ ! -e \"$SHOT_SLEEP_READY\" ] && [ \"$i\" -lt 300 ]; do \"$SHOT_REAL_SLEEP\" 0.1; i=$((i+1)); done\n" +
		"exec \"$SHOT_REAL_SLEEP\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{
		"SHOT_REAL_SLEEP=" + realSleep,
		"SHOT_SLEEP_READY=" + readyFile,
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
	}
}

// A Chrome that finishes promptly must not leave the watchdog's sleep
// running for the whole timeout.
func TestShotStopsItsWatchdogAfterChromeExits(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	realSleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep: the shot watchdog uses it")
	}
	s := page(t)
	env, argsFile := standin(t, "png", 0)
	realChrome := strings.TrimPrefix(env[0], "SHOT_CHROME=")
	dir := t.TempDir()
	chrome := filepath.Join(dir, "chrome")
	script := "#!/bin/sh\n\"$SHOT_REAL_SLEEP\" 0.2\nexec \"$SHOT_REAL_CHROME\" \"$@\"\n"
	if err := os.WriteFile(chrome, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	traceDir := t.TempDir()
	sleepPIDs := filepath.Join(t.TempDir(), "sleep-pids")
	sleepScript := "#!/bin/sh\necho $$ >>\"$SHOT_SLEEP_PIDS\"\nexec \"$SHOT_REAL_SLEEP\" \"$@\"\n"
	if err := os.WriteFile(filepath.Join(traceDir, "sleep"), []byte(sleepScript), 0o755); err != nil {
		t.Fatal(err)
	}
	env = append(env,
		"SHOT_CHROME="+chrome,
		"SHOT_REAL_CHROME="+realChrome,
		"SHOT_REAL_SLEEP="+realSleep,
		"SHOT_SLEEP_PIDS="+sleepPIDs,
		"SHOT_TIMEOUT=60",
		"PATH="+traceDir+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", filepath.Join(t.TempDir(), "shot.png")}, env...)
	if rc != 0 {
		t.Fatalf("shot: exit %d: %s", rc, text)
	}
	if _, err := os.Stat(argsFile); err != nil {
		t.Fatal(err)
	}
	deadWithin(t, sleepPIDs, time.Second)
}

// A Chrome that sleeps past its deadline is killed, and the shot fails with
// what it printed so far and the reason, within about a second: the script's
// own bound and runShot's context each do it, so a hung Chrome hangs neither
// a builder nor the suite. Neither one-second clock starts before the
// stand-in is ready: under load it can take seconds to be scheduled, and a
// deadline that fires first kills a Chrome that never ran, leaving the test
// a missing pids file instead of the refusal.
func TestShotKillsAChromeThatDoesNotFinish(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	t.Run("the script's SHOT_TIMEOUT", func(t *testing.T) {
		env, pids := sleeper(t, "chrome is warming up", false)
		// The watchdog's clock waits for the stand-in: sleep holds it
		// until the pids file appears.
		env = append(env, readySleep(t, pids)...)
		env = append(env, "SHOT_TIMEOUT=1")
		out := filepath.Join(t.TempDir(), "shot.png")
		rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
		if rc == 0 || !strings.Contains(text, "chrome did not finish within 1s") {
			t.Errorf("a Chrome past SHOT_TIMEOUT: exit %d, %q, want a refusal naming the deadline", rc, text)
		}
		if !strings.Contains(text, "chrome is warming up") {
			t.Errorf("the refusal is %q, without what Chrome printed", text)
		}
		dead(t, pids)
	})
	t.Run("runShot's context deadline", func(t *testing.T) {
		env, pids := sleeper(t, "chrome is warming up", true)
		out := filepath.Join(t.TempDir(), "shot.png")
		// The deadline starts when the test says so: the run starts with
		// no deadline but a backstop, the test waits for the stand-in's
		// pids file, and only then takes the second.
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		backstop := time.AfterFunc(shotTimeout, cancel)
		t.Cleanup(func() { backstop.Stop() })
		type outcome struct {
			rc   int
			text string
			err  error
		}
		done := make(chan outcome, 1)
		go func() {
			rc, text, err := runShotCmd(ctx, []string{s.URL + "/page", out}, env...)
			done <- outcome{rc, text, err}
		}()
		readyAt := ready(t, pids, 30*time.Second)
		kill := time.AfterFunc(time.Second, cancel)
		defer kill.Stop()
		res := <-done
		if res.err != nil {
			t.Fatalf("shot: %v", res.err)
		}
		if res.rc == 0 || !strings.Contains(res.text, "chrome did not finish within") {
			t.Errorf("a Chrome past the deadline: exit %d, %q, want a refusal naming the deadline", res.rc, res.text)
		}
		if !strings.Contains(res.text, "chrome is warming up") {
			t.Errorf("the refusal is %q, without what Chrome printed", res.text)
		}
		if dt := time.Since(readyAt); dt > 10*time.Second {
			t.Errorf("the kill took %s after the stand-in was ready, want the test's one-second deadline", dt.Round(time.Millisecond))
		}
		dead(t, pids)
	})
}

// A Chrome that never finishes must fail at its deadline on every run, not
// just usually: the watchdog once lost its TERM to a timer that had forked
// but not exec'd, waited out its 5 s grace sleep, and returned SHOT_TIMEOUT
// + 5 s about one run in ten (yardr-pt36). Each run is timed from the
// stand-in's ready marker, so process startup under load does not consume
// the watchdog's allowance; a slow run fails however fast the rest were.
// The bound is the timeout plus four seconds: normal runs take about two,
// slow ones over six.
func TestShotTimesOutAtItsDeadlineEveryRun(t *testing.T) {
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("no curl: it checks the page before the shot")
	}
	s := page(t)
	const runs = 10
	for i := 0; i < runs; i++ {
		env, pids := sleeper(t, "chrome is warming up", false)
		// The watchdog's clock waits for the stand-in: sleep holds it
		// until the pids file appears.
		env = append(env, readySleep(t, pids)...)
		env = append(env, "SHOT_TIMEOUT=1")
		out := filepath.Join(t.TempDir(), "shot.png")
		type outcome struct {
			rc   int
			text string
			err  error
		}
		done := make(chan outcome, 1)
		ctx := shotCtx(t)
		go func() {
			rc, text, err := runShotCmd(ctx, []string{s.URL + "/page", out}, env...)
			done <- outcome{rc, text, err}
		}()
		readyAt := ready(t, pids, 30*time.Second)
		res := <-done
		if res.err != nil {
			t.Fatalf("run %d of %d: shot: %v", i+1, runs, res.err)
		}
		if res.rc == 0 || !strings.Contains(res.text, "chrome did not finish within 1s") {
			t.Errorf("run %d of %d: exit %d, %q, want a refusal naming the deadline", i+1, runs, res.rc, res.text)
		}
		if dt := time.Since(readyAt); dt > 5*time.Second {
			t.Errorf("run %d of %d: the refusal took %s after Chrome was ready, want within SHOT_TIMEOUT + 4 s", i+1, runs, dt.Round(time.Millisecond))
		}
		dead(t, pids)
	}
}

// A slow Chrome is the machine's fault on the landing suite, under the race
// detector and on a machine whose load is past its cores, and the shot's own
// anywhere else. The machine's own load is pinned, as the gate's tests pin
// theirs, so what the test finds does not depend on what else is running.
func TestUnderLoad(t *testing.T) {
	t.Setenv("YARDR_TEST_LOADAVG", "1.5")
	t.Setenv("YARDR_TEST_CORES", "10")
	t.Setenv("YARDR_LANDING", "1")
	if !underLoad() {
		t.Error("underLoad with YARDR_LANDING=1 is false")
	}
	t.Setenv("YARDR_LANDING", "")
	if underLoad() != race() {
		t.Errorf("underLoad without YARDR_LANDING is %v with race %v", underLoad(), race())
	}
	t.Setenv("YARDR_TEST_LOADAVG", "72.4")
	if !underLoad() {
		t.Error("underLoad on a machine at load 72.4 on 10 cores is false")
	}
}

// The machine is loaded when its one-minute load average is past its core
// count, as the gate's loadedmachine says; at the count is not past it, and
// a load or a count that cannot be read is no load (yardr-zkfj).
func TestMachineLoaded(t *testing.T) {
	for _, c := range []struct {
		avg, cores string
		want       bool
	}{
		{"72.4", "10", true},
		{"10.5", "10", true},
		{"10", "10", false},
		{"1.5", "10", false},
		{"", "10", false},
		{"unknown", "10", false},
		{"72.4", "", false},
		{"72.4", "0", false},
	} {
		t.Run(c.avg+" on "+c.cores, func(t *testing.T) {
			t.Setenv("YARDR_TEST_LOADAVG", c.avg)
			t.Setenv("YARDR_TEST_CORES", c.cores)
			if got := machineLoaded(); got != c.want {
				t.Errorf("machineLoaded at load %q on %q cores is %v, want %v", c.avg, c.cores, got, c.want)
			}
		})
	}
}

// loadAverage reads the first number out of what each system prints for the
// load: Linux's /proc/loadavg and darwin's sysctl vm.loadavg.
func TestLoadAverage(t *testing.T) {
	for text, want := range map[string]float64{
		"0.52 0.58 0.59 1/467 12345\n": 0.52,
		"{ 3.52 3.61 3.40 }\n":         3.52,
		"72.4":                         72.4,
	} {
		if got, ok := loadAverage(text); !ok || got != want {
			t.Errorf("loadAverage(%q) = %v, %v, want %v", text, got, ok, want)
		}
	}
	for _, text := range []string{"", "\n", "{ }", "unknown"} {
		if got, ok := loadAverage(text); ok {
			t.Errorf("loadAverage(%q) = %v, want no reading", text, got)
		}
	}
}

// Where the yard runs, the system's own reading comes through: a reader that
// never found one would leave every machine quiet, and the skip on load
// would never fire.
func TestSystemLoadIsRead(t *testing.T) {
	switch runtime.GOOS {
	case "darwin":
	case "linux":
		if _, err := os.Stat("/proc/loadavg"); err != nil {
			t.Skip("this linux keeps no /proc/loadavg")
		}
	default:
		t.Skip("the yard runs on linux and darwin")
	}
	t.Setenv("YARDR_TEST_LOADAVG", "")
	t.Setenv("YARDR_TEST_CORES", "")
	avg, cores, ok := loadReading()
	if !ok || avg < 0 || cores < 1 {
		t.Errorf("the system's load reading is %v on %v cores, ok=%v", avg, cores, ok)
	}
}

// The URL is a web page's and nothing else: one that starts with a dash
// would be read as an option by curl and by Chrome, and a file: URL would
// shoot a file of this machine into a note. Neither reaches curl or Chrome.
func TestShotRefusesAURLThatIsNotAWebPage(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(dir, "planted")
	for name, url := range map[string]string{
		"an option to Chrome":    "--remote-debugging-port=0",
		"an option to curl":      "-o" + planted,
		"a file of this machine": "file://" + secret,
		"no scheme":              "127.0.0.1/page",
		"nothing":                "",
	} {
		t.Run(name, func(t *testing.T) {
			env, argsFile := standin(t, "shot", 0)
			out := filepath.Join(t.TempDir(), "shot.png")
			rc, text := runShot(shotCtx(t), t, []string{url, out}, env...)
			if rc != 2 || !strings.Contains(text, "http:// or https://") {
				t.Errorf("shot %q: exit %d, %q, want exit 2 naming the schemes", url, rc, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a URL that is not a web page")
			}
			if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Errorf("a shot of it is at %s", out)
			}
			if _, err := os.Stat(planted); !os.IsNotExist(err) {
				t.Error("curl took the URL as an option and wrote a file")
			}
		})
	}
}

// The file is not followed through a symbolic link: a link planted at a
// path a builder is known to shoot to (the role's /tmp/shot-<bead>.png)
// must not have its target emptied and overwritten.
func TestShotRefusesASymlinkedFile(t *testing.T) {
	s := page(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("not a shot"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, to := range map[string]string{
		"a link to a file":  target,
		"a link to nothing": filepath.Join(dir, "gone"),
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "shot.png")
			if err := os.Symlink(to, out); err != nil {
				t.Fatal(err)
			}
			env, argsFile := standin(t, "shot", 0)
			rc, text := runShot(shotCtx(t), t, []string{s.URL + "/page", out}, env...)
			if rc == 0 || !strings.Contains(text, out) || !strings.Contains(text, "symbolic link") {
				t.Errorf("shot to %s: exit %d, %q, want a refusal naming the link", out, rc, text)
			}
			if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
				t.Error("Chrome ran for a symlinked file")
			}
			if got, err := os.ReadFile(target); err != nil || string(got) != "not a shot" {
				t.Errorf("the link's target is now %q, %v", got, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "gone")); !os.IsNotExist(err) {
				t.Error("the shot was written through a dangling link")
			}
		})
	}
}
