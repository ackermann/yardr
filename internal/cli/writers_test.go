package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// writerRun runs a direct subcommand with buffers of its own as its output
// and error streams, and returns what each stream holds.
func writerRun(t *testing.T, c *cobra.Command, args ...string) (stdout, stderr string) {
	t.Helper()
	var errs bytes.Buffer
	c.SetErr(&errs)
	out, err := run(t, c, args...)
	if err != nil {
		t.Fatalf("yardr %s: %v\n%s%s", strings.Join(args, " "), err, out, errs.String())
	}
	return out, errs.String()
}

// The long tail's commands print to their command's writers: each case
// runs one with buffers of its own and reads each stream from its buffer.
// An empty err list pins that the command warns of nothing.
func TestLongTailCommandsWriteToTheirCommandWriters(t *testing.T) {
	t.Parallel()
	bare := func(t *testing.T) *app {
		t.Helper()
		return &app{st: storetest.New(t), actor: "me"}
	}
	for _, tc := range []struct {
		name string
		yard func(t *testing.T) *app
		cmd  func(a *app) *cobra.Command
		args []string
		out  []string
		err  []string
	}{
		{
			name: "depot list",
			yard: bare,
			cmd:  (*app).depotCmd,
			args: []string{"list"},
			out:  []string{"NAME"},
		},
		{
			name: "depot show",
			yard: func(t *testing.T) *app {
				t.Helper()
				a := bare(t)
				if err := a.st.PutDepot(context.Background(), store.Depot{Name: "web", Path: t.TempDir(), Kind: store.DepotDir}, "me"); err != nil {
					t.Fatal(err)
				}
				return a
			},
			cmd:  (*app).depotCmd,
			args: []string{"show", "web"},
			out:  []string{"depot web\n", "path:", "kind:", "base:  none (a dir depot)"},
		},
		{
			name: "group list",
			yard: func(t *testing.T) *app {
				t.Helper()
				a := bare(t)
				if err := a.st.PutGroup(context.Background(), store.Group{Name: "people", Runner: "manual"}, "me"); err != nil {
					t.Fatal(err)
				}
				return a
			},
			cmd:  (*app).groupCmd,
			args: []string{"list"},
			out:  []string{"people"},
		},
		{
			name: "route list",
			yard: bare,
			cmd:  (*app).routeCmd,
			args: []string{"list"},
			out:  []string{"ID"},
		},
		{
			name: "hook list",
			yard: bare,
			cmd:  (*app).hookCmd,
			args: []string{"list"},
			out:  []string{"NAME"},
		},
		{
			name: "flow list",
			yard: bare,
			cmd:  (*app).flowCmd,
			args: []string{"list"},
			out:  []string{"NAME"},
		},
		{
			name: "timer list",
			yard: bare,
			cmd:  (*app).timerCmd,
			args: []string{"list"},
			out:  []string{"ID"},
		},
		{
			name: "thread list",
			yard: func(t *testing.T) *app {
				t.Helper()
				a := bare(t)
				if _, err := a.st.PutCrew(context.Background(), store.CrewMember{Name: "clerk", Runner: "herdr"}, "me"); err != nil {
					t.Fatal(err)
				}
				return a
			},
			cmd:  (*app).threadCmd,
			args: []string{"list", "clerk"},
			out:  []string{"SOURCE"},
		},
		{
			name: "review",
			yard: bare,
			cmd:  (*app).reviewCmd,
			args: nil,
			out:  []string{"ID"},
			err:  []string{"not a terminal: listed instead of the form"},
		},
		{
			name: "review list",
			yard: bare,
			cmd:  (*app).reviewCmd,
			args: []string{"list"},
			out:  []string{"ID"},
		},
		{
			name: "user list",
			yard: func(t *testing.T) *app {
				t.Helper()
				return &app{st: userYard(t), actor: "jo"}
			},
			cmd:  (*app).userCmd,
			args: []string{"list"},
			out:  []string{"jo"},
		},
		{
			name: "session list",
			yard: bare,
			cmd:  (*app).sessionCmd,
			args: []string{"list"},
			out:  []string{"ID"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := tc.yard(t)
			stdout, stderr := writerRun(t, tc.cmd(a), tc.args...)
			for _, want := range tc.out {
				if !strings.Contains(stdout, want) {
					t.Errorf("stdout lacks %q:\n%s", want, stdout)
				}
			}
			if len(tc.err) == 0 {
				if stderr != "" {
					t.Errorf("stderr is not empty:\n%s", stderr)
				}
				return
			}
			for _, want := range tc.err {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr lacks %q:\n%s", want, stderr)
				}
			}
		})
	}
}

// A peer show prints the peer to its command's writer: the yards are
// linked first, then the show runs with buffers of its own.
func TestPeerShowWritesToItsCommandWriter(t *testing.T) {
	t.Parallel()
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	south.a.env = testEnv(t, south.home)
	invite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", invite)
	stdout, stderr := writerRun(t, north.a.peerCmd(), "show", "south")
	if stderr != "" {
		t.Errorf("stderr is not empty:\n%s", stderr)
	}
	wantText(t, stdout, "south  (it calls itself south)", "has not said what it understands")
}

func TestOTelNoCollectorWarnsOnItsCommandErrorWriter(t *testing.T) {
	t.Parallel()
	stdout, stderr := writerRun(t, testApp(t, nil, newHomeDir(t)).otelCmd(), "export")
	if stdout != "" {
		t.Errorf("stdout = %q, want no output", stdout)
	}
	if stderr != "yardr otel export: no collector: set OTEL_EXPORTER_OTLP_ENDPOINT or give --endpoint; nothing is exported\n" {
		t.Errorf("stderr = %q", stderr)
	}
}

// The long tail's print helpers write to the writer they are given, pinned
// at the function.
func TestLongTailHelpersWriteToTheirWriter(t *testing.T) {
	t.Parallel()
	t.Run("printLogin", func(t *testing.T) {
		var buf bytes.Buffer
		printLogin(&buf, []loginUnit{{Part: "herdr", Manager: "launchd", Name: "dev.yardr.herdr", Result: "installed", Loaded: true,
			Warnings: []string{"another server answers there"}}})
		wantText(t, buf.String(), "At login", "herdr's server", "mind: another server answers there")
	})
	t.Run("serviceResult", func(t *testing.T) {
		var buf bytes.Buffer
		(&serviceResult{Home: "/tmp/y1", Kind: "daemon", Manager: "launchd", Name: "dev.yardr.daemon.1",
			File: "/tmp/y1/dev.yardr.daemon.1.plist", Program: "/opt/yardr/bin/yardr", Args: []string{"daemon"},
			Log: "/tmp/y1/daemon.log", Loaded: true, Warnings: []string{"claude is not on the PATH"}}).print(&buf)
		wantText(t, buf.String(), "launchd agent dev.yardr.daemon.1 written:", "Mind: claude is not on the PATH",
			"launchd has started the daemon")
	})
	t.Run("saySetAside", func(t *testing.T) {
		var buf bytes.Buffer
		web := "web"
		aside := []store.SetAside{{Route: store.Route{ID: "aaa111", Stage: "new", Group: "a"}, Own: store.Route{ID: "bbb222", Stage: "new", Depot: &web, Group: "a"}}}
		saySetAside(&buf, "web", aside, "me")
		wantText(t, buf.String(), "1 default set aside in web:")
	})
	t.Run("printReviewList", func(t *testing.T) {
		var buf bytes.Buffer
		printReviewList(&buf, []reviewItem{{Bead: store.Bead{ID: "web-1", Stage: "review", Title: "Say hello"}, Group: "reviewers",
			Actions: []reviewAction{{Kind: actAdvance, Stage: "approved"}}}})
		wantText(t, buf.String(), "web-1", "approved", "Say hello")
	})
	t.Run("showCode", func(t *testing.T) {
		var buf bytes.Buffer
		showCode(&buf, "https://north:8443", "48151623", time.Date(2026, 10, 19, 20, 0, 0, 0, time.UTC))
		wantText(t, buf.String(), "48151623", "https://north:8443")
	})
	t.Run("stoppedBefore", func(t *testing.T) {
		var buf bytes.Buffer
		stoppedBefore(&buf, &config.Config{Home: "/tmp/y1"}, "yardmaster", "claude")
		wantText(t, buf.String(), "Stopped before yardmaster was started", "init --start")
	})
	t.Run("upgradeCheckReport", func(t *testing.T) {
		var buf bytes.Buffer
		(&upgradeCheckReport{Server: upgradeCheckServer{Verdict: "ok", Detail: "answers at r1"},
			Daemon: upgradeCheckDaemon{Verdict: "skip", Detail: "no daemon"},
			Views:  upgradeCheckViews{Verdict: "ok", Detail: "none recorded"}}).print(&buf)
		wantText(t, buf.String(), "server     ok: answers at r1", "daemon     skip: no daemon", "views      ok: none recorded")
	})
}
