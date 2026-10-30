package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// socket returns a short unix socket path (t.TempDir is too long).
func socket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "yr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func serve(t *testing.T, sock string, h http.HandlerFunc) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
}

func TestSkewMessage(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(version.Header, "0123456789ab")
		w.Header().Set(PIDHeader, "4242")
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method " + strings.TrimPrefix(r.URL.Path, "/call/")}})
	})
	c := NewClient(sock)
	c.Revision = "ba9876543210"
	_, err := c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	want := "the yardr server (pid 4242, built 0123456789ab) differs from this yardr (ba9876543210); " +
		"run `yardr server stop` (no such method AdvanceNote)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}

	// A server from before the handshake sends no revision or pid.
	sock = socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method AdvanceNote"}})
	})
	c = NewClient(sock)
	c.Revision = "ba9876543210"
	_, err = c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	want = "the yardr server (pid unknown, built unknown) differs from this yardr (ba9876543210); " +
		"run `yardr server stop` (no such method AdvanceNote)"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant  %s", err, want)
	}
}

func TestSameBuildKeepsPlainError(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(version.Header, r.Header.Get(version.Header))
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "no such method AdvanceNote"}})
	})
	c := NewClient(sock)
	c.Revision = "0123456789ab"
	_, err := c.AdvanceNote(context.Background(), "b", "review", "n", store.Expect{}, "me")
	if err == nil || err.Error() != "no such method AdvanceNote" {
		t.Fatalf("err = %v", err)
	}
}

func TestCallSendsRevisionAndRetriesWhileReplacing(t *testing.T) {
	sock := socket(t)
	var calls atomic.Int32
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(version.Header) != "0123456789ab" {
			t.Errorf("revision header = %q", r.Header.Get(version.Header))
		}
		if calls.Add(1) < 3 {
			WriteReplacing(w)
			return
		}
		writeJSON(w, reply{Result: uint64(7)})
	})
	c := NewClient(sock)
	c.Revision = "0123456789ab"
	head, err := c.Head(context.Background())
	if err != nil || head != 7 {
		t.Fatalf("Head = %d, %v", head, err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d; want 2 refusals then the answer", calls.Load())
	}
}

func TestCallWaitsForAServerToListen(t *testing.T) {
	sock := socket(t)
	// A stale socket file refuses connections, as while a server execs.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	go func() {
		time.Sleep(300 * time.Millisecond)
		os.Remove(sock)
		serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, reply{Result: uint64(1)})
		})
	}()
	c := NewClient(sock)
	if head, err := c.Head(context.Background()); err != nil || head != 1 {
		t.Fatalf("Head = %d, %v", head, err)
	}

	c = NewClient(socket(t))
	c.Retry = 200 * time.Millisecond
	start := time.Now()
	if _, err := c.Head(context.Background()); err == nil {
		t.Fatal("no server, yet the call succeeded")
	} else if time.Since(start) > 2*time.Second {
		t.Fatalf("gave up after %v; retry is meant to be brief", time.Since(start))
	}
}

// A call of a test's yard stops retrying once the test binary is gone: no
// server replaces itself then, so waiting out the deadline only outlives
// the test (an init --start orphaned by a killed binary). While the owner
// runs, the call retries to its deadline as ever.
func TestCallStopsWhenItsTestOwnerIsGone(t *testing.T) {
	self, err := peer.Describe(os.Getpid())
	if err != nil {
		t.Skipf("this system tells nothing of a process's start: %v", err)
	}
	// An owner that has ended: a sleep, described while it ran, then
	// killed and reaped.
	sleep := exec.Command("sleep", "60")
	if err := sleep.Start(); err != nil {
		t.Fatal(err)
	}
	// Do not leave a child behind if describing its start fails.
	t.Cleanup(func() {
		if sleep.ProcessState == nil {
			_ = sleep.Process.Kill()
			_ = sleep.Wait()
		}
	})
	ended, err := peer.Describe(sleep.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := sleep.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	sleep.Wait()

	for _, c := range []struct {
		name       string
		owner      string
		retry      time.Duration
		wantPrompt bool // the call returns well before its deadline
	}{
		{"an owner that has ended", fmt.Sprintf("%d %d", sleep.Process.Pid, ended.Start), 5 * time.Second, true},
		{"an owner that runs", fmt.Sprintf("%d %d", os.Getpid(), self.Start), 300 * time.Millisecond, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(config.Key(config.TestOwnerEnv), c.owner)
			client := NewClient(socket(t)) // nothing listens
			client.Retry = c.retry
			start := time.Now()
			_, err := client.Head(context.Background())
			took := time.Since(start)
			if err == nil {
				t.Fatal("no server, yet the call succeeded")
			}
			if c.wantPrompt && took >= 2*time.Second {
				t.Errorf("took %v with the owner gone; want it to stop at once, not at the %v deadline", took, c.retry)
			}
			if !c.wantPrompt && took < c.retry {
				t.Errorf("took %v with the owner running; want it to retry to the %v deadline", took, c.retry)
			}
		})
	}
}

// A call the server took and did not answer is store.ErrNoAnswer: whether
// it ran is not known. A refusal is an answer, and a call nothing listened
// for never ran: neither is one.
func TestCallWithoutAnAnswer(t *testing.T) {
	sock := socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		// The server dies with the call in hand.
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	})
	c := NewClient(sock)
	_, err := c.GiveSecret(context.Background(), "b", 1, map[string]string{"NAME": "value"}, "me", true)
	if !errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a broken call: %v, want store.ErrNoAnswer", err)
	}
	if strings.Contains(err.Error(), "value") {
		t.Fatalf("the error holds what was sent: %v", err)
	}

	sock = socket(t)
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, reply{Error: &wireError{Code: "error", Message: "a change to a source is the operator's"}})
	})
	_, err = NewClient(sock).GiveSecret(context.Background(), "b", 1, nil, "me", true)
	if err == nil || errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a refusal: %v", err)
	}

	c = NewClient(socket(t)) // nothing listens
	c.Retry = 0
	_, err = c.GiveSecret(context.Background(), "b", 1, nil, "me", true)
	if err == nil || errors.Is(err, store.ErrNoAnswer) {
		t.Fatalf("a call nothing listened for: %v", err)
	}
}

// The client reads a conversation in pages, and is told whether older mail
// is left. A call as a yardr from before the pages makes it, three
// arguments in an array, is answered as it was: all of it, and nothing
// beside the result.
func TestClientConversationPages(t *testing.T) {
	c := NewClient(apiYard(t))
	ctx := t.Context()
	for _, text := range []string{"and the fence", "and the gate"} {
		if err := c.Mail(ctx, "yardmaster", text, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	// With what jo wrote the yardmaster as josephine@north.
	all, more, err := c.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil || len(all) != 5 || more {
		t.Fatalf("the conversation = %d letters, more %v, %v", len(all), more, err)
	}
	page, more, err := c.Conversation(ctx, "jo", "yardmaster", 0, 3, 0)
	if err != nil || !more || !reflect.DeepEqual(page, all[2:]) {
		t.Fatalf("the newest three = %+v, more %v, %v", page, more, err)
	}
	page, more, err = c.Conversation(ctx, "jo", "yardmaster", 0, 3, page[0].Seq)
	if err != nil || more || !reflect.DeepEqual(page, all[:2]) {
		t.Fatalf("the page before = %+v, more %v, %v", page, more, err)
	}
	want, _ := json.Marshal(map[string]any{"result": all})
	for _, body := range []string{`["jo","yardmaster",0]`, `{"a":"jo","b":"yardmaster","after":0}`, `{"a":"jo","b":"yardmaster"}`} {
		resp, err := c.http.Post("http://yard/call/Conversation", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if strings.TrimSpace(string(got)) != string(want) {
			t.Errorf("Conversation %s answers\n%s\nwant, as before,\n%s", body, got, want)
		}
	}
}

// Mail through the API sends a text over one mail's size as numbered mails,
// in order, by array as by name; past the most mails one text is sent as it
// is refused, naming the file it goes as instead.
func TestClientMailLong(t *testing.T) {
	c := NewClient(apiYard(t))
	ctx := t.Context()
	text := strings.Repeat("x", 10<<10)
	if err := c.Mail(ctx, "yardmaster", text, "jo"); err != nil {
		t.Fatal(err)
	}
	byName, _ := json.Marshal(map[string]string{"to": "yardmaster", "text": text, "actor": "jo"})
	resp, err := c.http.Post("http://yard/call/Mail", "application/json", strings.NewReader(string(byName)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(got), "error") {
		t.Fatalf("Mail by name answers %s", got)
	}
	all, _, err := c.Conversation(ctx, "jo", "yardmaster", 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	var marks []string
	bodies := ""
	for _, l := range all[len(all)-6:] {
		head, body, _ := strings.Cut(l.Text, "\n")
		marks = append(marks, head)
		bodies += body
	}
	if strings.Join(marks, " ") != "(1/3) (2/3) (3/3) (1/3) (2/3) (3/3)" || bodies != text+text {
		t.Fatalf("the mails are %q", marks)
	}
	big := strings.Repeat("x", store.MaxMailParts*store.MaxMailBytes+1)
	if err := c.Mail(ctx, "yardmaster", big, "jo"); err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("a text past %d mails = %v", store.MaxMailParts, err)
	}
}

// A call made with a fresh sign-in on its context carries the witness
// header: the user the gateway authenticated and their authentication
// time. One made without carries none.
func TestCallSendsFreshWitness(t *testing.T) {
	sock := socket(t)
	var got []string
	serve(t, sock, func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(FreshWitnessHeader))
		writeJSON(w, reply{Result: uint64(1)})
	})
	c := NewClient(sock)
	at := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ctx := store.WithFreshWitness(context.Background(), "jo", at)
	if _, err := c.Head(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{"jo " + fmt.Sprint(at.Unix()), ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("witness headers = %q, want %q", got, want)
	}
}
