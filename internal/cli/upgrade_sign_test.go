package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// On darwin a yard that names a code-signing identity signs the installed
// binary with it, after the install and before anything gates on the new
// build, and says so in its own step line.
func testUpgradeRunSignsInstalledBinaryOnDarwin(t *testing.T) {
	sc := newUpgradeScene(t)
	ctx := context.Background()
	const identity = "Developer ID Application: Ada (TEAM000001)"
	if err := sc.st.SetSetting(ctx, store.CodesignIdentitySetting, identity, "jo"); err != nil {
		t.Fatal(err)
	}
	stub(t, &goos, "darwin")
	var calls []string
	stub(t, &upgradeGoBuild, func(ctx context.Context, dir, file string) (string, error) {
		calls = append(calls, "install")
		return sc.build(ctx, dir, file)
	})
	var gotIdentity, gotTarget string
	stub(t, &upgradeCodesign, func(_ context.Context, identity, target string) (string, error) {
		calls = append(calls, "sign")
		gotIdentity, gotTarget = identity, target
		return "", nil
	})

	out, err := sc.upgrade(t)
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}

	if strings.Join(calls, " ") != "install sign" {
		t.Fatalf("install and sign ran as %v, want install then sign once", calls)
	}
	if gotIdentity != identity {
		t.Fatalf("signed with %q, want the setting's %q", gotIdentity, identity)
	}
	if gotTarget != sc.target {
		t.Fatalf("signed %q, want the install target %q", gotTarget, sc.target)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var seen []string
	for _, l := range lines {
		seen = append(seen, strings.Fields(l)[0])
	}
	if strings.Join(seen, " ") != "backup keep sign install scripts health" {
		t.Fatalf("the output's lines are %v:\n%s", seen, out)
	}
	if want := fmt.Sprintf("%-9s  signed as %s", "sign", upgradeCodesignIdentifier); lines[2] != want {
		t.Fatalf("sign line = %q, want %q", lines[2], want)
	}
}

// With no identity set, or off darwin, no signing call is made: the
// scene's upgradeCodesign fails the test when called, and no sign line
// is printed.
func testUpgradeRunLeavesBinaryUnsigned(t *testing.T) {
	ctx := context.Background()
	t.Run("no identity on darwin", func(t *testing.T) {
		sc := newUpgradeScene(t)
		stub(t, &goos, "darwin")

		out, err := sc.upgrade(t)
		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, out)
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.Fields(l)[0] == "sign" {
				t.Fatalf("a sign line with no identity set:\n%s", out)
			}
		}
	})
	t.Run("identity set off darwin", func(t *testing.T) {
		sc := newUpgradeScene(t)
		if err := sc.st.SetSetting(ctx, store.CodesignIdentitySetting, "Developer ID Application: Ada (TEAM000001)", "jo"); err != nil {
			t.Fatal(err)
		}

		out, err := sc.upgrade(t)
		if err != nil {
			t.Fatalf("upgrade: %v\n%s", err, out)
		}
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.Fields(l)[0] == "sign" {
				t.Fatalf("a sign line off darwin:\n%s", out)
			}
		}
	})
}

// A failed signing fails the upgrade in the build-failure shape, with
// codesign's words in the reason: the unsigned new bytes already stand
// at the target, so the previous binary is put back first, and the gate
// files come down.
func testUpgradeRunSignFailureRestoresPrevious(t *testing.T) {
	t.Run("restores previous binary", func(t *testing.T) { testUpgradeRunSignFailureRestoresPreviousBinary(t) })
	t.Run("reports restore failure", func(t *testing.T) { testUpgradeRunSignFailureReportsRestoreError(t) })
}

func testUpgradeRunSignFailureRestoresPreviousBinary(t *testing.T) {
	sc := newUpgradeScene(t)
	ctx := context.Background()
	if err := sc.st.SetSetting(ctx, store.CodesignIdentitySetting, "Developer ID Application: Ada (TEAM000001)", "jo"); err != nil {
		t.Fatal(err)
	}
	stub(t, &goos, "darwin")
	stub(t, &upgradeCodesign, func(_ context.Context, _, target string) (string, error) {
		// Stand in for the unsigned new bytes: what the restore must
		// overwrite.
		if err := os.WriteFile(target, []byte("unsigned new bytes"), 0o755); err != nil {
			t.Fatal(err)
		}
		return "  errSecInternalComponent: no identity found\n", errors.New("exit status 1")
	})

	out, err := sc.upgrade(t)
	if err == nil || !strings.Contains(err.Error(), "errSecInternalComponent: no identity found") {
		t.Fatalf("upgrade = %v, want codesign's trimmed output in the reason\n%s", err, out)
	}

	prev, err := os.ReadFile(filepath.Join(filepath.Dir(sc.target), "yardr.prev"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(sc.target); err != nil || string(got) != string(prev) {
		t.Fatalf("the target holds %q, want the previous binary back", got)
	}
	if _, err := os.Stat(config.At(sc.home).UpgradeGate()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gate file stands after a failed signing: %v", err)
	}
	evs, err := sc.st.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var told *store.Event
	for i, e := range evs {
		if e.Kind == store.InstallFailed {
			told = &evs[i]
		}
	}
	if told == nil || told.Data["stage"] != "sign" {
		t.Fatalf("no install_failed at stage sign in %+v", evs)
	}
	if reason, _ := told.Data["reason"].(string); !strings.Contains(reason, "errSecInternalComponent: no identity found") {
		t.Fatalf("the told reason carries %q", reason)
	}
}

func testUpgradeRunSignFailureReportsRestoreError(t *testing.T) {
	sc := newUpgradeScene(t)
	ctx := context.Background()
	if err := sc.st.SetSetting(ctx, store.CodesignIdentitySetting, "Developer ID Application: Ada (TEAM000001)", "jo"); err != nil {
		t.Fatal(err)
	}
	stub(t, &goos, "darwin")
	stub(t, &upgradeCodesign, func(_ context.Context, _, target string) (string, error) {
		if err := os.Remove(filepath.Join(filepath.Dir(target), "yardr.prev")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("unsigned new bytes"), 0o755); err != nil {
			t.Fatal(err)
		}
		return "codesign failed", errors.New("exit status 1")
	})

	out, err := sc.upgrade(t)
	if err == nil || !strings.Contains(err.Error(), "the previous binary could not be restored") {
		t.Fatalf("upgrade = %v, want the failed restore in the reason\n%s", err, out)
	}
	if _, err := os.Stat(config.At(sc.home).UpgradeGate()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the gate file stands after a failed signing: %v", err)
	}
}

// The real signing step marks the binary with the fixed identifier, so a
// Developer Tools entry naming it holds across reinstalls. It signs with
// the ad-hoc identity, which needs no certificate, and runs only where
// codesign exists.
func TestUpgradeCodesignSetsIdentifier(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "darwin" {
		t.Skip("codesign is darwin's")
	}
	if _, err := exec.LookPath("codesign"); err != nil {
		t.Skip("codesign is not on PATH")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "hello")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "main.go")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	if out, err := upgradeCodesign(ctx, "-", bin); err != nil {
		t.Fatalf("upgradeCodesign: %v\n%s", err, out)
	}
	show := exec.CommandContext(ctx, "codesign", "-dv", bin)
	out, err := show.CombinedOutput()
	if err != nil {
		t.Fatalf("codesign -dv: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Identifier="+upgradeCodesignIdentifier) {
		t.Fatalf("codesign -dv reports:\n%s", out)
	}
}
