package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// machineOf is doctor --json's machine items on a home with no yard, by
// program.
func machineOf(t *testing.T) map[string]need {
	t.Helper()
	rep, _ := doctorJSON(t, nil)
	out := map[string]need{}
	for _, n := range rep.Machine {
		out[n.Program] = n
	}
	return out
}

// herdr's fetch is not in a harness file, so nothing checks it when yardr
// starts: this does. It names a file for each system the onboarding installs
// on, and each is a different one.
func TestHerdrFetch(t *testing.T) {
	t.Parallel()
	if err := herdrFetch.Check("herdr"); err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, goos := range []string{"darwin", "linux"} {
		for _, arch := range []string{"arm64", "amd64"} {
			d := herdrFetch.For(goos, arch, "/home/x")
			if d == nil {
				t.Errorf("no herdr for %s/%s", goos, arch)
				continue
			}
			if urls[d.URL] {
				t.Errorf("%s/%s has a file that another system has: %s", goos, arch, d.URL)
			}
			urls[d.URL] = true
		}
	}
}

// On a Mac, doctor --json says for each missing program how a program that
// fetches gets it: herdr's file for this machine, with the path under this
// user's home, and under the line that no harness is installed each harness
// that can be fetched. git has none: it is Apple's own dialog.
func TestDoctorFetchOnMac(t *testing.T) {
	fakeTools(t, nil)
	stub(t, &loginShell, func() string { return "" })
	stub(t, &goos, "darwin")
	stub(t, &goarch, "arm64")
	stub(t, &userHomeDir, func() (string, error) { return "/Users/pat", nil })
	m := machineOf(t)

	herdr := m["herdr"]
	if f := herdr.Fetch; herdr.Found || herdr.Get != "curl -fsSL https://herdr.dev/install.sh | sh (more: https://herdr.dev)" || f == nil ||
		f.Path != "/Users/pat/.local/bin/herdr" || f.Version != "0.9.4" || !strings.HasSuffix(f.URL, "/v0.9.4/herdr-macos-aarch64") ||
		f.SHA256 != herdrFetch.Targets["darwin/arm64"].SHA256 || f.Size == 0 || f.Member != "" || f.Run != nil ||
		f.Verified != (store.Verified{From: "https://herdr.dev/docs/install/", On: "2026-11-05"}) {
		t.Errorf("herdr: %+v, fetch %+v", herdr, f)
	}
	if git := m["git"]; git.Fetch != nil || !strings.Contains(git.Get, "xcode-select --install") {
		t.Errorf("git: %+v", git)
	}
	none := m["a harness"]
	if none.Fetch != nil || none.Get == "" || len(none.AnyOf) != 2 {
		t.Fatalf("a harness: %+v", none)
	}
	for i, kind := range []string{"claude", "codex"} {
		n := none.AnyOf[i]
		if f := n.Fetch; n.Harness != kind || n.Program != kind || n.Found || n.Get != agents.InstallHint(kind) || f == nil ||
			f.Path != "/Users/pat/.local/bin/"+kind || f.SHA256 != agents.Harnesses[i].Fetch.Targets["darwin/arm64"].SHA256 {
			t.Errorf("any_of %d: %+v, fetch %+v", i, n, f)
		}
	}
	if f := none.AnyOf[0].Fetch; strings.Join(f.Run, " ") != "install "+f.Version {
		t.Errorf("claude is run with %v", f.Run)
	}

	// The other kind of Mac gets its own file.
	stub(t, &goarch, "amd64")
	if f := machineOf(t)["herdr"].Fetch; f == nil || !strings.HasSuffix(f.URL, "/herdr-macos-x86_64") {
		t.Errorf("herdr on an Intel Mac: %+v", f)
	}

	// A claude on PATH that is another program is as missing as none, and a
	// program that fetches is told how to get the real one all the same;
	// the line that says no harness is installed still lists it among those
	// that can be fetched.
	fakeTools(t, map[string]string{"claude": toolAnswers})
	m = machineOf(t)
	if c := m["claude"]; c.Found || c.Path == "" || c.Version == nil || c.Version.State != versionNotHarness ||
		c.Get != agents.InstallHint("claude") || c.Fetch == nil || c.Fetch.Path != "/Users/pat/.local/bin/claude" {
		t.Errorf("a claude that is another program: %+v", c)
	}
	if none := m["a harness"]; len(none.AnyOf) != 2 || none.AnyOf[0].Harness != "claude" || none.AnyOf[0].Fetch == nil || none.AnyOf[0].Found {
		t.Errorf("a harness: %+v", none)
	}
}

// Off a Mac nothing is fetched: every missing program keeps its hint to
// install it by hand, and no item carries a fetch. The onboarding has herdr's
// file for a Linux box to install from all the same, which no report carries.
func TestDoctorNoFetchOnLinux(t *testing.T) {
	fakeTools(t, nil)
	stub(t, &loginShell, func() string { return "" })
	stub(t, &goos, "linux")
	stub(t, &userHomeDir, func() (string, error) { return "/home/pat", nil })
	for _, arch := range []string{"amd64", "arm64"} {
		stub(t, &goarch, arch)
		for program, n := range machineOf(t) {
			if n.Fetch != nil || n.AnyOf != nil || n.Get == "" {
				t.Errorf("%s on linux/%s: get %q, fetch %+v, any_of %+v", program, arch, n.Get, n.Fetch, n.AnyOf)
			}
		}
		if h := basicNeeds()[0]; h.Program != "herdr" || h.download == nil || h.download.Path != "/home/pat/.local/bin/herdr" ||
			!strings.Contains(h.download.URL, "-linux-") || h.Fetch != nil {
			t.Errorf("herdr on linux/%s: %+v, download %+v", arch, h, h.download)
		}
	}
}

// What is installed needs no fetch, on a Mac as anywhere.
func TestDoctorNoFetchOfWhatIsFound(t *testing.T) {
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": claudeAnswers})
	stub(t, &herdrServer, func(context.Context, string) serverCheck { return serverCheck{State: herdrUp} })
	stub(t, &goos, "darwin")
	stub(t, &goarch, "arm64")
	for program, n := range machineOf(t) {
		if !n.Found || n.Fetch != nil || n.AnyOf != nil {
			t.Errorf("%s: %+v", program, n)
		}
	}
}
