package hooks_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
)

func TestCheckAddr(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:8790", ":8790", "localhost:1", "[::1]:65535", "0.0.0.0:8790"} {
		if err := hooks.CheckAddr(ok); err != nil {
			t.Errorf("CheckAddr(%q) = %v", ok, err)
		}
	}
	for _, bad := range []string{"", "8790", "127.0.0.1", "127.0.0.1:", "127.0.0.1:http", "127.0.0.1:0", "127.0.0.1:65536",
		"http://127.0.0.1:8790", "::1:8790"} {
		if err := hooks.CheckAddr(bad); err == nil {
			t.Errorf("CheckAddr(%q) passed", bad)
		}
	}
}

// What a server said about its receiver is read only while that server
// runs: with no state, or a pidfile naming another process, nobody knows.
func TestReadListener(t *testing.T) {
	c := &config.Config{Home: t.TempDir()}
	if l, known := hooks.ReadListener(c); known {
		t.Fatalf("no state: %+v", l)
	}
	want := hooks.Listener{Addr: "127.0.0.1:8790", From: hooks.FromSetting, Error: "taken"}
	if err := hooks.WriteListener(c, want); err != nil {
		t.Fatal(err)
	}
	if l, known := hooks.ReadListener(c); known {
		t.Fatalf("no pidfile: %+v", l)
	}
	pid := func(n int) {
		if err := os.WriteFile(c.PIDFile(), []byte(strconv.Itoa(n)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pid(os.Getpid())
	want.PID = os.Getpid()
	if l, known := hooks.ReadListener(c); !known || l != want {
		t.Fatalf("ReadListener = %+v (known %v), want %+v", l, known, want)
	}
	pid(os.Getpid() + 1)
	if l, known := hooks.ReadListener(c); known {
		t.Fatalf("another server's pidfile: %+v", l)
	}
}

func TestListens(t *testing.T) {
	const addr = "127.0.0.1:8790"
	for _, c := range []struct {
		name    string
		l       hooks.Listener
		unknown bool
		setting string
		listens bool
		want    []string
	}{
		{name: "nobody said", unknown: true, setting: addr, want: []string{"has not said"}},
		{name: "the setting", l: hooks.Listener{Addr: addr, From: hooks.FromSetting}, setting: addr, listens: true,
			want: []string{"takes webhooks on " + addr + " (the hook_addr setting)"}},
		{name: "the setting, changed since", l: hooks.Listener{Addr: addr, From: hooks.FromSetting}, setting: ":9", listens: true,
			want: []string{"takes webhooks on " + addr, "hook_addr is :9 now", "at its next start", "yardr server stop"}},
		{name: "the setting, unset since", l: hooks.Listener{Addr: addr, From: hooks.FromSetting}, listens: true,
			want: []string{"hook_addr is unset now"}},
		{name: "the variable", l: hooks.Listener{Addr: addr, From: hooks.FromVariable}, listens: true,
			want: []string{"(YARDR_HOOK_ADDR, set where the server started)"}},
		{name: "the variable over the setting", l: hooks.Listener{Addr: addr, From: hooks.FromVariable}, setting: ":9", listens: true,
			want: []string{"the hook_addr setting (:9) is not used while the variable is set"}},
		{name: "taken, the setting", l: hooks.Listener{Addr: addr, From: hooks.FromSetting, Error: "in use"}, setting: addr,
			want: []string{"could not listen on " + addr + " (the hook_addr setting): in use", "yardr settings set hook_addr <host:port>", "yardr server stop"}},
		{name: "taken, the setting changed since", l: hooks.Listener{Addr: addr, From: hooks.FromSetting, Error: "in use"}, setting: ":9",
			want: []string{"could not listen on " + addr, "hook_addr is :9 now", "at its next start"}},
		{name: "taken, the variable", l: hooks.Listener{Addr: addr, From: hooks.FromVariable, Error: "in use"},
			want: []string{"could not listen on " + addr + " (YARDR_HOOK_ADDR", "change the variable where the server starts"}},
		{name: "no address, set since", setting: addr, want: []string{"takes no webhooks yet", "hook_addr is " + addr, "at its next start"}},
		{name: "no address", want: []string{"has no address", "yardr settings set hook_addr 127.0.0.1:8790"}},
	} {
		listens, text := hooks.Listens(c.l, !c.unknown, c.setting)
		if listens != c.listens {
			t.Errorf("%s: listens = %v", c.name, listens)
		}
		for _, w := range c.want {
			if !strings.Contains(text, w) {
				t.Errorf("%s: %q lacks %q", c.name, text, w)
			}
		}
	}
}
