package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func held(t *testing.T) (*Config, *Held, string) {
	t.Helper()
	c := &Config{Home: filepath.Join(t.TempDir(), "home")}
	if err := os.Mkdir(c.Home, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(c.Home, "made")
	if err := os.WriteFile(file, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := c.Hold(file)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Close)
	return c, h, file
}

func TestHeldHomeIsNotLostWhileItStands(t *testing.T) {
	c, h, file := held(t)
	// What a running yard does to its home and its files: none of it is a loss.
	if err := os.WriteFile(filepath.Join(c.Home, "other"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if why := h.Lost(); why != "" {
		t.Fatalf("lost: %s", why)
	}
}

func TestHeldHomeLost(t *testing.T) {
	for name, tc := range map[string]struct {
		do   func(t *testing.T, c *Config, file string)
		want string
	}{
		"home removed": {func(t *testing.T, c *Config, _ string) {
			if err := os.RemoveAll(c.Home); err != nil {
				t.Fatal(err)
			}
		}, "home is gone"},
		// A removed temp directory whose name is made again, files and all.
		"home made again": {func(t *testing.T, c *Config, file string) {
			if err := os.RemoveAll(c.Home); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(c.Home, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(file, []byte("1"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, "another directory"},
		"file removed": {func(t *testing.T, _ *Config, file string) {
			if err := os.Remove(file); err != nil {
				t.Fatal(err)
			}
		}, "made is gone"},
		// Renamed over, as a pidfile is written: the name holds a new file.
		"file replaced": {func(t *testing.T, _ *Config, file string) {
			if err := os.WriteFile(file+".new", []byte("1"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(file+".new", file); err != nil {
				t.Fatal(err)
			}
		}, "another file"},
	} {
		t.Run(name, func(t *testing.T) {
			c, h, file := held(t)
			tc.do(t, c, file)
			if why := h.Lost(); !strings.Contains(why, tc.want) {
				t.Fatalf("Lost() = %q, want it to say %q", why, tc.want)
			}
		})
	}
}

// TestOwner is the "<pid> <start>" a test names to the server and the
// daemon it starts; missing or malformed means no watch.
func TestTestOwner(t *testing.T) {
	for _, tc := range []struct {
		name      string
		value     string
		wantPid   int
		wantStart uint64
		wantOK    bool
	}{
		{"set", "4242 987654", 4242, 987654, true},
		{"missing", "", 0, 0, false},
		{"only a pid", "4242", 0, 0, false},
		{"not a pid", "x 987654", 0, 0, false},
		{"not a start", "4242 x", 0, 0, false},
		{"no pid", "0 987654", 0, 0, false},
		{"three fields", "4242 987654 1", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YARDR_TEST_OWNER", tc.value)
			pid, start, ok := TestOwner()
			if pid != tc.wantPid || start != tc.wantStart || ok != tc.wantOK {
				t.Fatalf("TestOwner() = (%d, %d, %v)", pid, start, ok)
			}
		})
	}
}

// TestHomeCheck is the tick a test names to the server and the daemon it
// starts through the real CLI; missing or malformed means the real minute.
func TestTestHomeCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
		ok    bool
	}{
		{"set", "1s", time.Second, true},
		{"millis", "250ms", 250 * time.Millisecond, true},
		{"missing", "", 0, false},
		{"not a duration", "soon", 0, false},
		{"no unit", "2", 0, false},
		{"zero", "0s", 0, false},
		{"negative", "-1s", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YARDR_TEST_HOME_CHECK", tc.value)
			if every, ok := TestHomeCheck(); every != tc.want || ok != tc.ok {
				t.Fatalf("TestHomeCheck() = (%v, %v)", every, ok)
			}
		})
	}
}

func TestTestOwnerUnset(t *testing.T) {
	v, had := os.LookupEnv("YARDR_TEST_OWNER")
	os.Unsetenv("YARDR_TEST_OWNER")
	t.Cleanup(func() {
		if had {
			os.Setenv("YARDR_TEST_OWNER", v)
		}
	})
	if pid, start, ok := TestOwner(); ok {
		t.Fatalf("TestOwner() = (%d, %d, true) without the variable", pid, start)
	}
}

func TestWatchSaysOnceWhatWasLost(t *testing.T) {
	c, h, _ := held(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lost := h.Watch(ctx, 10*time.Millisecond)
	select {
	case why := <-lost:
		t.Fatalf("lost while the home stands: %s", why)
	case <-time.After(100 * time.Millisecond):
	}
	if err := os.RemoveAll(c.Home); err != nil {
		t.Fatal(err)
	}
	select {
	case why := <-lost:
		if !strings.Contains(why, "is gone") {
			t.Fatalf("lost = %q", why)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a removed home was not noticed")
	}
}
