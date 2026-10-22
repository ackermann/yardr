package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// loginRig is stubServices for up --login and down --login: the manager
// starts what it loads, as the real one does. A herdr service it starts
// counts in herdrStarts, by which upYard's herdr answers; a daemon's holds
// the lock of the yard the test named (at) until it is unloaded or told
// to stop; a web view's makes its address answer. Nothing else may start
// a herdr server or a daemon: up --login leaves that to the services.
type loginRig struct {
	dir         string
	m           *manager
	herdrStarts *int
	unlocks     map[string]func()
	web         bool
	// home is the yard the rig stops the daemon of, named with at; ""
	// reads the process's.
	home string
}

func stubLogin(t *testing.T, system string, herdrStarts *int) *loginRig {
	t.Helper()
	dir, m := stubServices(t, system)
	r := &loginRig{dir: dir, m: m, herdrStarts: herdrStarts, unlocks: map[string]func(){}}
	t.Cleanup(func() {
		for _, u := range r.unlocks {
			u()
		}
	})
	r.use(t)
	return r
}

// at names the yard the rig stops the daemon of: the test's own, in
// place of the process's, which names TestMain's home with no yard.
func (r *loginRig) at(home string) { r.home = home }

// yard is the home the rig stops the daemon of.
func (r *loginRig) yard() string {
	if r.home != "" {
		return r.home
	}
	return os.Getenv("YARDR_HOME")
}

// unlock ends the daemon of the yard the test is in.
func (r *loginRig) unlock() {
	home := r.yard()
	if u := r.unlocks[home]; u != nil {
		u()
		delete(r.unlocks, home)
	}
}

// use puts the rig in place, again after a second upYard has replaced what
// starts things with its own.
func (r *loginRig) use(t *testing.T) {
	t.Helper()
	stub(t, &serviceDir, func() (string, error) { return r.dir, nil })
	stub(t, &serviceCtl, func(ctx context.Context, name string, args ...string) (string, error) {
		out, err := r.m.ctl(ctx, name, args...)
		line := strings.Join(args, " ")
		starts := strings.Contains(line, "bootstrap") || strings.Contains(line, "restart")
		switch {
		case err != nil:
		case starts && strings.Contains(line, "herdr"):
			*r.herdrStarts++
		case starts && strings.Contains(line, "daemon"):
			home := r.yard()
			u, lerr := lockDaemon((&config.Config{Home: home}).DaemonLock(), 0)
			if lerr != nil {
				t.Errorf("the service's daemon found the lock held: %v", lerr)
				break
			}
			r.unlocks[home] = u
		case starts && strings.Contains(line, "web"):
			r.web = true
		case strings.Contains(line, "daemon") && (strings.Contains(line, "bootout") || strings.Contains(line, "disable")):
			r.unlock()
		}
		return out, err
	})
	stub(t, &signalStop, func(int) error { r.unlock(); return nil })
	stub(t, &answers, func(string) bool { return r.web })
	stub(t, &spawnHerdr, func(string, string) (<-chan error, error) {
		t.Error("up --login started herdr's server itself")
		return nil, nil
	})
	stub(t, &spawn, func(_ []string, _ *config.Config, k *longRunning, _ []string) (int, <-chan error, func() error, error) {
		t.Errorf("up --login started yardr %s itself", k.Name)
		return 0, nil, func() error { return nil }, nil
	})
	stub(t, &yardRunners, func(string, store.API) map[string]dispatch.Runner { return map[string]dispatch.Runner{} })
	stub(t, &stopServer, func(*config.Config) error { return nil })
}

func upJSON(t *testing.T, a *app, flags ...string) upReport {
	t.Helper()
	out, err := run(t, a.rootCmd(), append([]string{"up", "--json"}, flags...)...)
	var rep upReport
	if jerr := json.Unmarshal([]byte(out), &rep); err != nil || jerr != nil {
		t.Fatalf("up --json %v: %v %v\n%s", flags, err, jerr, out)
	}
	return rep
}

// loginOf is the login service of part in a report.
func loginOf(t *testing.T, units []loginUnit, part string) loginUnit {
	t.Helper()
	for _, l := range units {
		if l.Part == part {
			return l
		}
	}
	t.Fatalf("no login service of %s in %+v", part, units)
	return loginUnit{}
}

// The files of herdr's service as written, for each manager: the docs' two
// files. herdr by its path, the PATH, started at login and started again
// only after a failure, so that herdr server stop and herdr update are not
// fought; and nothing that names a yard.
func TestHerdrServiceFilesGolden(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ file, manager, name, log string }{
		{"herdr.plist", launchd, "dev.yardr.herdr", "/Users/jo/Library/Logs/dev.yardr.herdr.log"},
		{"herdr.service", systemd, "yardr-herdr.service", ""},
	} {
		u := &unit{Kind: svcHerdr, Manager: c.manager, Name: c.name, Home: "/Users/jo", Program: "/Users/jo/.local/bin/herdr",
			Args: []string{"server"}, Env: []string{"PATH=/Users/jo/.local/bin:/opt/homebrew/bin:/usr/bin:/bin"}, Log: c.log}
		got := u.render()
		golden(t, c.file, got)
		if c.manager == launchd {
			plutilLint(t, got)
		}
		for _, not := range []string{"YARDR_", "KillMode", "AbandonProcessGroup", "RestartSec", "/.yardr"} {
			if strings.Contains(got, not) {
				t.Errorf("%s holds %q:\n%s", c.file, not, got)
			}
		}
		// A plist's comment takes no "--" but the two that hold it.
		if c.manager == launchd && strings.Count(got, "--") != 2 {
			t.Errorf("%s holds a -- in its comment:\n%s", c.file, got)
		}
		if !strings.Contains(got, herdrWritten) || !herdrServerLine.MatchString(got) {
			t.Errorf("%s is not known as yardr's service for herdr's server:\n%s", c.file, got)
		}
	}
}

// A yard that names its herdr session gets the service of that session's
// server, under a name of its own and told which socket it is.
func TestHerdrServiceOfANamedSession(t *testing.T) {
	stubServices(t, "darwin")
	runner.UseSession("work")
	t.Cleanup(func() { runner.UseSession("") })
	u, err := herdrUnit(nil, "/bin/herdr")
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "dev.yardr.herdr.work" || !slices.Contains(u.Env, "HERDR_SESSION=work") ||
		!slices.Contains(u.Env, "HERDR_SOCKET_PATH="+runner.SessionSocket("work")) {
		t.Fatalf("unit %s with %v", u.Name, u.Env)
	}
	// A service of the person's own for the first session is not this one's.
	if err := os.MkdirAll(filepath.Dir(u.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(u.Path), "herdr-server.plist")
	if err := os.WriteFile(other, []byte("<string>/usr/local/bin/herdr</string>\n<string>server</string>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := foreignHerdr("work", u.Path); len(f) != 0 {
		t.Fatalf("the first session's service taken for work's: %v", f)
	}
	if f := foreignHerdr("", u.Path); !slices.Equal(f, []string{other}) {
		t.Fatalf("the first session's service: %v", f)
	}
}

// HERDR_SESSION is inherited from the caller, unlike the validated yard
// setting. It must not turn into a path outside the service directory.
func TestHerdrLoginRejectsUnsafeSessionName(t *testing.T) {
	stubServices(t, "darwin")
	t.Setenv("HERDR_SESSION", "../../../../tmp/yardr-review.plist")
	runner.UseSession("")
	t.Cleanup(func() { runner.UseSession("") })

	if _, err := herdrUnit(nil, "/bin/herdr"); err == nil || !strings.Contains(err.Error(), "cannot name a login service file") {
		t.Fatalf("herdrUnit accepted an unsafe inherited session name: %v", err)
	}
}

// From nothing up --login installs the service of herdr's server and the
// daemon's, and it is the services that start both. A second run finds
// both and writes and loads nothing. status and doctor say that herdr's
// server starts at login. The shell it runs in has the proxy and CA
// variables of a box behind a company's proxy: herdr's service holds them,
// which every agent's pane inherits, and nothing else of that shell.
func TestUpLoginInstallsAndStarts(t *testing.T) {
	home, st, a, herdrStarts, _ := upYard(t, false)
	rig := stubLogin(t, "darwin", herdrStarts)
	rig.at(home)
	dir, m := rig.dir, rig.m
	env := yardEnv(t, st)
	for _, kv := range slices.Concat(egressEnviron, []string{"GITHUB_TOKEN=ghp_secret", "ALL_PROXY=socks5://proxy.corp:1080"}) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	herdrFile := filepath.Join(dir, "dev.yardr.herdr.plist")
	daemonFile := filepath.Join(dir, "dev.yardr.daemon."+homeID(home)+".plist")

	out, err := run(t, a(false).rootCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "login service for its server: not installed ("+hereAt(home, "yardr up --login")+")")

	out, err = run(t, a(false).rootCmd(), "up", "--login")
	if err != nil || *herdrStarts != 1 {
		t.Fatalf("up --login: %v, herdr's service started %d times\n%s", err, *herdrStarts, out)
	}
	wantText(t, out,
		"herdr's server  started\nserver          running already (pid 4711)\ndaemon          started (pid ",
		"\nAt login:\nherdr's server  installed: "+herdrFile+"\ndaemon          installed: "+daemonFile+"\n",
		"It starts at every login from now on. To end that: "+hereAt(home, "yardr down --login")+"\n")
	b, err := os.ReadFile(herdrFile)
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, string(b), herdrWritten, "<string>/bin/herdr</string>\n\t\t<string>server</string>",
		"<key>PATH</key>\n\t\t<string>/opt/yardr/bin:/usr/bin:/bin</string>", "<key>SuccessfulExit</key>\n\t\t<false/>")
	for _, kv := range egressEnviron {
		k, v, _ := strings.Cut(kv, "=")
		wantText(t, string(b), "<key>"+k+"</key>\n\t\t<string>"+v+"</string>")
	}
	if strings.Contains(string(b), home) || strings.Contains(string(b), "GITHUB_TOKEN") || strings.Contains(string(b), "ALL_PROXY") {
		t.Errorf("herdr's service names the yard, or holds what is not the server's to have:\n%s", b)
	}
	if _, err := os.Stat(daemonFile); err != nil {
		t.Fatal(err)
	}
	calls := len(m.calls)

	rep := upJSON(t, a(true), "--login")
	h, d := loginOf(t, rep.Login, partHerdr), loginOf(t, rep.Login, svcDaemon)
	if !rep.Up || h.Result != loginFound || !h.Loaded || h.File != herdrFile || d.Result != loginFound || d.File != daemonFile || len(rep.Login) != 2 {
		t.Fatalf("a second up --login: %+v", rep)
	}
	for _, c := range m.calls[calls:] {
		if !strings.Contains(c, " print ") {
			t.Errorf("a second up --login told launchd: %s", c)
		}
	}

	out, err = run(t, a(false).rootCmd(), "status")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, out, "login service for its server: installed and loaded: launchd starts herdr's server at login ("+herdrFile+")")
	out, _ = run(t, a(false).rootCmd(), "doctor")
	wantText(t, out, "configured      login service for herdr's server: herdr's server starts at login ("+herdrFile+")")
}

// A server that runs is not restarted for its service, and no second copy
// is started beside it: launchd is told nothing and loads the file at the
// next login; systemd is told to start it at login, and not now.
func TestUpLoginLeavesARunningServer(t *testing.T) {
	for _, system := range []string{"darwin", "linux"} {
		t.Run(system, func(t *testing.T) {
			home, _, a, herdrStarts, _ := upYard(t, true)
			rig := stubLogin(t, system, herdrStarts)
			rig.at(home)
			dir, m := rig.dir, rig.m
			stubLookPath(t, "claude", "git", "herdr", "systemctl")
			rep := upJSON(t, a(true), "--login")
			h := loginOf(t, rep.Login, partHerdr)
			if !rep.Up || h.Result != loginInstalled || h.Error != "" || !strings.Contains(h.Detail, "left as it is") || *herdrStarts != 0 {
				t.Fatalf("up --login: %+v, herdr's service started %d times", rep, *herdrStarts)
			}
			var told []string
			for _, c := range m.calls {
				if strings.Contains(c, "herdr") {
					told = append(told, c)
				}
			}
			var want []string
			file := filepath.Join(dir, "dev.yardr.herdr.plist")
			if system == "linux" {
				want, file = []string{"systemctl --user enable yardr-herdr.service"}, filepath.Join(dir, "yardr-herdr.service")
			}
			if !slices.Equal(told, want) || h.File != file || h.Loaded != (system == "linux") {
				t.Fatalf("the manager was told %q about herdr, want %q; %+v", told, want, h)
			}
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if system == "linux" {
				wantText(t, string(b), `ExecStart="/bin/herdr" "server"`, "Restart=on-failure\n", "WantedBy=default.target")
			}
		})
	}
}

// A service for herdr's server that the person saved, under any name, is
// found by what it runs and left as it is: yardr writes none beside it and
// says which file it found. The daemon's is installed all the same.
func TestUpLoginLeavesAServiceThatIsNotYardrs(t *testing.T) {
	home, _, a, herdrStarts, _ := upYard(t, true)
	rig := stubLogin(t, "darwin", herdrStarts)
	rig.at(home)
	dir, m := rig.dir, rig.m
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mine := filepath.Join(dir, "herdr-server.plist")
	const saved = "<plist><dict>\n<key>Label</key>\n<string>herdr-server</string>\n<key>ProgramArguments</key>\n<array>\n" +
		"\t<string>/usr/local/bin/herdr</string>\n\t<string>server</string>\n</array>\n</dict></plist>\n"
	if err := os.WriteFile(mine, []byte(saved), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a(false).rootCmd(), "up", "--login")
	if err != nil {
		t.Fatalf("up --login: %v\n%s", err, out)
	}
	wantText(t, out, "herdr's server  left as it is: a service for herdr's server that is not yardr's is there ("+mine+"), and yardr writes none beside it\n",
		"daemon          installed: ")
	if _, err := os.Stat(filepath.Join(dir, "dev.yardr.herdr.plist")); err == nil {
		t.Error("yardr wrote its own service beside the person's")
	}
	if b, _ := os.ReadFile(mine); string(b) != saved {
		t.Errorf("the person's file was changed:\n%s", b)
	}
	rep := upJSON(t, a(true), "--login")
	if h := loginOf(t, rep.Login, partHerdr); h.Result != loginLeft || !slices.Equal(h.Foreign, []string{mine}) || h.File != "" {
		t.Fatalf("herdr's service in the report: %+v", h)
	}
	out, _ = run(t, a(false).rootCmd(), "status")
	wantText(t, out, "login service for its server: one of your own, not yardr's ("+mine+")")

	// down --login leaves it too.
	out, err = run(t, a(false).rootCmd(), "down", "--login")
	if err != nil {
		t.Fatalf("down --login: %v\n%s", err, out)
	}
	wantText(t, out, "herdr's server  left as it is: the service for herdr's server is not yardr's ("+mine+")\n", "daemon          removed: ")
	if b, _ := os.ReadFile(mine); string(b) != saved {
		t.Errorf("the person's file was changed by down:\n%s", b)
	}
	for _, c := range m.calls {
		if strings.Contains(c, "herdr") {
			t.Errorf("the manager was told about a herdr service: %s", c)
		}
	}

	// A file where yardr's own would be that yardr did not write is the
	// person's as well.
	os.Remove(mine)
	own := filepath.Join(dir, "dev.yardr.herdr.plist")
	if err := os.WriteFile(own, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep = upJSON(t, a(true), "--login")
	if h := loginOf(t, rep.Login, partHerdr); h.Result != loginLeft || !slices.Equal(h.Foreign, []string{own}) {
		t.Fatalf("a file of the person's under yardr's name: %+v", h)
	}
	if b, _ := os.ReadFile(own); string(b) != "<plist/>" {
		t.Errorf("it was overwritten:\n%s", b)
	}
}

// Two yards of one user have one service for herdr's server: the second
// yard's up --login finds it and installs its daemon's alone. down --login
// in one yard leaves it while the other still starts at login, naming that
// yard, and the last yard's removes it, without stopping the server.
func TestTwoYardsShareHerdrsLoginService(t *testing.T) {
	for _, system := range []string{"darwin", "linux"} {
		t.Run(system, func(t *testing.T) {
			homeA, _, a, herdrStarts, _ := upYard(t, false)
			rig := stubLogin(t, system, herdrStarts)
			rig.at(homeA)
			dir, m := rig.dir, rig.m
			stubLookPath(t, "claude", "git", "herdr", "systemctl")
			herdrFile := filepath.Join(dir, "dev.yardr.herdr.plist")
			if system == "linux" {
				herdrFile = filepath.Join(dir, "yardr-herdr.service")
			}
			if rep := upJSON(t, a(true), "--login"); !rep.Up || loginOf(t, rep.Login, partHerdr).Result != loginInstalled {
				t.Fatalf("the first yard: %+v", rep)
			}
			written, err := os.ReadFile(herdrFile)
			if err != nil {
				t.Fatal(err)
			}

			// The second yard, of the same user: the same service directory.
			homeB, _, b, startsB, _ := upYard(t, true)
			rig.herdrStarts = startsB
			rig.use(t)
			rig.at(homeB)
			stubLookPath(t, "claude", "git", "herdr", "systemctl")
			calls := len(m.calls)
			rep := upJSON(t, b(true), "--login")
			h, d := loginOf(t, rep.Login, partHerdr), loginOf(t, rep.Login, svcDaemon)
			if !rep.Up || h.Result != loginFound || h.File != herdrFile || d.Result != loginInstalled || !strings.Contains(d.File, homeID(homeB)) {
				t.Fatalf("the second yard: %+v", rep)
			}
			for _, c := range m.calls[calls:] {
				if strings.Contains(c, "herdr") && !strings.Contains(c, " print ") && !strings.Contains(c, "is-enabled") {
					t.Errorf("the second yard told the manager about herdr's service: %s", c)
				}
			}
			if now, _ := os.ReadFile(herdrFile); string(now) != string(written) {
				t.Errorf("the second yard rewrote herdr's service:\n%s", now)
			}

			// The second yard stops starting at login: the first still does.
			out, err := run(t, b(false).rootCmd(), "down", "--login")
			if err != nil {
				t.Fatalf("down --login in the second yard: %v\n%s", err, out)
			}
			wantText(t, out, "\nAt login:\ndaemon          removed: ",
				"herdr's server  left as it is: "+herdrFile+": still needed: another yard starts at login too ("+homeA+")\n")
			if _, err := os.Stat(herdrFile); err != nil {
				t.Fatalf("herdr's service is gone while the first yard needs it: %v", err)
			}

			// The first yard is the last. Its downs name its home in an
			// environment of their own, and the rig stops the daemon
			// of that yard.
			rig.at(homeA)
			downA := func() *app {
				x := a(true)
				x.env = testEnv(t, homeA)
				return x
			}
			calls = len(m.calls)
			out, err = run(t, downA().rootCmd(), "down", "--login", "--json")
			var down downReport
			if jerr := json.Unmarshal([]byte(out), &down); err != nil || jerr != nil {
				t.Fatalf("down --login --json in the first yard: %v %v\n%s", err, jerr, out)
			}
			h, d = loginOf(t, down.Login, partHerdr), loginOf(t, down.Login, svcDaemon)
			if !down.Down || h.Result != loginRemoved || h.File != herdrFile || d.Result != loginRemoved || !strings.Contains(h.Detail, "left running") {
				t.Fatalf("down --login in the first yard: %+v", down)
			}
			for _, f := range []string{herdrFile, d.File} {
				if _, err := os.Stat(f); err == nil {
					t.Errorf("%s is still there", f)
				}
			}
			// herdr's server is never stopped: its service is not unloaded.
			for _, c := range m.calls[calls:] {
				if strings.Contains(c, "herdr") && (strings.Contains(c, "bootout") || strings.Contains(c, "--now") || strings.Contains(c, " stop ")) {
					t.Errorf("down --login stopped herdr's server: %s", c)
				}
			}
			if system == "linux" && !slices.Contains(m.calls[calls:], "systemctl --user disable yardr-herdr.service") {
				t.Errorf("systemd was not told to stop starting herdr's server at login: %q", m.calls[calls:])
			}

			// Once more: nothing is there, and that is no failure.
			down = downReport{}
			out, err = run(t, downA().rootCmd(), "down", "--login", "--json")
			if jerr := json.Unmarshal([]byte(out), &down); err != nil || jerr != nil {
				t.Fatalf("a second down --login: %v %v\n%s", err, jerr, out)
			}
			if loginOf(t, down.Login, partHerdr).Result != loginAbsent || loginOf(t, down.Login, svcDaemon).Result != loginAbsent {
				t.Fatalf("a second down --login: %+v", down.Login)
			}
		})
	}
}

// With web_addr set the web view is one of the yard's parts, and up --login
// installs its service too, with that address; without, the web view's
// service stays yardr web install's, and down --login leaves one it finds.
func TestUpLoginInstallsTheWebViewTheYardNames(t *testing.T) {
	home, st, a, herdrStarts, _ := upYard(t, true)
	rig := stubLogin(t, "darwin", herdrStarts)
	rig.at(home)
	dir := rig.dir
	webFile := filepath.Join(dir, "dev.yardr.web."+homeID(home)+".plist")
	rep := upJSON(t, a(true), "--login")
	if len(rep.Login) != 2 {
		t.Fatalf("without web_addr: %+v", rep.Login)
	}
	if _, err := os.Stat(webFile); err == nil {
		t.Fatal("the web view's service was installed on a yard that names no address for it")
	}
	if err := st.SetSetting(context.Background(), store.WebAddrSetting, "127.0.0.1:1", "Jo"); err != nil {
		t.Fatal(err)
	}
	rep = upJSON(t, a(true), "--login")
	w := loginOf(t, rep.Login, svcWeb)
	if w.Result != loginInstalled || w.File != webFile || !w.Loaded {
		t.Fatalf("with web_addr: %+v", rep.Login)
	}
	b, _ := os.ReadFile(webFile)
	wantText(t, string(b), "<string>--addr</string>\n\t\t<string>127.0.0.1:1</string>")
	for _, p := range rep.Parts {
		if p.Part == svcWeb && (!p.Running || p.URL != "http://127.0.0.1:1/") {
			t.Errorf("the web view's part: %+v", p)
		}
	}

	// Without the setting the service is yardr web install's, and stays.
	if err := st.SetSetting(context.Background(), store.WebAddrSetting, "", "Jo"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a(false).rootCmd(), "down", "--login")
	if err != nil {
		t.Fatalf("down --login: %v\n%s", err, out)
	}
	wantText(t, out, "web view        left as it is: "+webFile+": the yard's web_addr setting is unset", hereAt(home, "yardr web uninstall")+" removes it\n")
	if _, err := os.Stat(webFile); err != nil {
		t.Fatal(err)
	}
}

// Nothing is removed while nothing is stopped: --json without --yes on a
// yard with an agent asks first, and leaves the services.
func TestDownLoginRemovesNothingBeforeItStops(t *testing.T) {
	_, _, _, _, a, _ := downYard(t, map[string]string{"yardmaster": agentIdle})
	dir, _ := stubServices(t, "darwin")
	u, err := herdrUnit(nil, "/bin/herdr")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(u.Path, []byte(u.render()), 0o644); err != nil {
		t.Fatal(err)
	}
	rep := downJSON(t, a(true), "--login")
	if !rep.Confirm || len(rep.Login) != 0 {
		t.Fatalf("down --login --json without --yes: %+v", rep)
	}
	if _, err := os.Stat(u.Path); err != nil {
		t.Fatalf("herdr's service was removed by a down that stopped nothing: %v", err)
	}
}

// On a system yardr writes no service for, --login says so before it
// starts or stops anything.
func TestLoginNotSupported(t *testing.T) {
	_, _, a, herdrStarts, daemonStarts := upYard(t, false)
	stubServices(t, "freebsd")
	for _, cmd := range []string{"up", "down"} {
		out, err := run(t, a(false).rootCmd(), cmd, "--login")
		if err == nil || !strings.Contains(err.Error(), "--login is not supported on freebsd") || *herdrStarts+*daemonStarts != 0 {
			t.Fatalf("%s --login: %v\n%s", cmd, err, out)
		}
	}
}

// What up --login writes into a service is the login shell's PATH, with
// what this process has beyond it after: a window starts yardr without what
// a profile adds.
// up --login writes the daemon's service with the login shell's PATH
// (useLoginPATH), taken from the run's environment as it is when the
// service is written, not as it was when the run began.
func TestUpLoginWritesTheLoginShellsPATH(t *testing.T) {
	home, _, a, herdrStarts, _ := upYard(t, false)
	rig := stubLogin(t, "darwin", herdrStarts)
	rig.at(home)
	// The test is about what up writes, not about asking the shell, so it
	// stubs the answer instead of starting one: no spawn to stretch under
	// load.
	stub(t, &loginShellPATH, func(context.Context) string { return "/home/jo/.local/bin:/usr/bin:/bin" })
	// A run with an environment of its own, as Execute gives one, taken
	// before up --login merges PATH.
	x := a(false)
	x.env = environMap(os.Environ())
	// On the yard's home: the process's names TestMain's, with no yard.
	x.env["YARDR_HOME"] = home
	out, err := run(t, x.rootCmd(), "up", "--login")
	if err != nil {
		t.Fatalf("up --login: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(rig.dir, "dev.yardr.daemon."+homeID(home)+".plist"))
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, string(b), "<key>PATH</key>\n\t\t<string>/home/jo/.local/bin:/usr/bin:/bin:/opt/yardr/bin</string>")
}

func TestUseLoginPATH(t *testing.T) {
	// The test is about the merge, not about asking the shell, so it
	// stubs the answer instead of starting one: no spawn to stretch under
	// load.
	stub(t, &loginShellPATH, func(context.Context) string { return "/home/jo/.local/bin:/usr/bin:/bin" })
	t.Setenv("PATH", "/usr/bin:/bin:/Applications/Yard.app/bin")
	useLoginPATH(context.Background())
	if got, want := os.Getenv("PATH"), "/home/jo/.local/bin:/usr/bin:/bin:/Applications/Yard.app/bin"; got != want {
		t.Fatalf("PATH = %s, want %s", got, want)
	}
	stub(t, &loginShellPATH, func(context.Context) string { return "" })
	useLoginPATH(context.Background())
	if got := os.Getenv("PATH"); !strings.HasPrefix(got, "/home/jo/.local/bin:") {
		t.Fatalf("PATH without a login shell = %s", got)
	}
}
