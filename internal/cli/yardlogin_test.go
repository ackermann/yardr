package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// standInPanes is a runner whose crew panes run the login stand-in: a pane
// started while the stand-in has no login shows its login screen until it
// is started again, as Claude Code's does when it was logged in elsewhere.
type standInPanes struct {
	dir     string // the stand-in's, where it writes logged-in
	mu      sync.Mutex
	n       int
	atLogin map[string]bool
	prompts []string
}

func (p *standInPanes) StartCrew(_ context.Context, r store.CrewMember, _ string) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.n++
	agent := fmt.Sprintf("%s-%d", r.Name, p.n)
	_, err := os.Stat(filepath.Join(p.dir, "logged-in"))
	p.atLogin[agent] = err != nil
	return map[string]any{"agent": agent, "kind": "claude", store.PendingPrompt: "prime yourself"}, nil
}

func (p *standInPanes) Start(context.Context, dispatch.Job) (map[string]any, error) { return nil, nil }
func (p *standInPanes) Alive(context.Context, map[string]any) (bool, error)         { return true, nil }
func (p *standInPanes) Stop(_ context.Context, h map[string]any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.atLogin, h["agent"].(string))
	return nil
}
func (p *standInPanes) Status(context.Context, map[string]any) (string, error) { return "idle", nil }

func (p *standInPanes) InputLogin(_ context.Context, h map[string]any) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.atLogin[h["agent"].(string)] {
		return "Select login method:", nil
	}
	return "", nil
}

func (p *standInPanes) Nudge(_ context.Context, h map[string]any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.atLogin[h["agent"].(string)] {
		p.prompts = append(p.prompts, h["agent"].(string)+": "+text)
	}
	return nil
}

func (p *standInPanes) prompted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.prompts...)
}

// A yard whose yardmaster sits at the login screen of a logged-out claude
// leads that login itself, with no agent running (yardr-302g): the
// operator is asked how, the real leader runs against the stand-in, the
// code goes through a secret ask to the leader alone, and the yardmaster
// starts again logged in and is given its first prompt. The code is in no
// note, event or mail afterwards.
func TestYardLeadsStandInLogin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	// The leader's machinery: the stand-in as claude, and no real call.
	noHome(t)
	requireNonInteractive(t)
	stub(t, &tryHarnessCall, realTry)
	dir := fakeTools(t, map[string]string{"claude": loginStandIn})
	var leaders sync.WaitGroup
	t.Cleanup(leaders.Wait)
	lead := func(ctx context.Context, kind string) (*dispatch.LoginLeader, error) {
		ansR, ansW := io.Pipe()
		needsR, needsW := io.Pipe()
		ended := make(chan error, 1)
		leaders.Add(1)
		go func() {
			defer leaders.Done()
			code := execute(&app{st: heldStore{st}, stdin: ansR, stdout: needsW}, []string{"harness", "login", kind, "--json"})
			needsW.Close()
			if code != 0 {
				ended <- fmt.Errorf("exit status %d", code)
				return
			}
			ended <- nil
		}()
		go func() {
			<-ctx.Done()
			ansW.Close()
		}()
		return &dispatch.LoginLeader{Needs: needsR, Answers: ansW, Wait: func() error { return <-ended }}, nil
	}
	pane := &standInPanes{dir: dir, atLogin: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: st, Runners: map[string]dispatch.Runner{"panes": pane}, PromptSettle: -1, PromptWait: -1,
		LeadLogin: lead, HarnessInstalled: func(kind string) bool { return kind == "claude" }}
	passCtx, stop := context.WithCancel(ctx)
	t.Cleanup(stop)
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "panes", Enabled: true,
		Config: map[string]any{"kind": "claude"}}, "op"); err != nil {
		t.Fatal(err)
	}
	r, err := st.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	open := func() []store.ResolvedAsk {
		notes, err := st.Notes(ctx, r.Journal)
		if err != nil {
			t.Fatal(err)
		}
		var out []store.ResolvedAsk
		for _, a := range store.ResolveAsks(notes, time.Now()) {
			if a.State == store.AskOpen {
				out = append(out, a)
			}
		}
		return out
	}
	until := func(what string, cond func() bool) {
		t.Helper()
		for deadline := time.Now().Add(time.Minute); !cond(); {
			if time.Now().After(deadline) {
				t.Fatalf("never: %s", what)
			}
			if err := d.Reconcile(passCtx); err != nil {
				t.Fatal(err)
			}
		}
	}

	until("the operator asked how to log in", func() bool { return len(open()) == 1 })
	if err := st.Mail(ctx, "yardmaster", "hi", "op"); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerAsk(ctx, r.Journal, open()[0].Note.Seq, 1, "", "op", false); err != nil {
		t.Fatal(err)
	}
	until("a secret ask for the code", func() bool { a := open(); return len(a) == 1 && a[0].Ask.Secret != nil })
	input := open()[0]
	if !strings.Contains(strings.Join(input.Ask.Context, " "), loginPage) {
		t.Errorf("the sign-in page is not in the ask: %q", input.Ask.Context)
	}
	if _, err := st.GiveSecret(ctx, r.Journal, input.Note.Seq, map[string]string{"INPUT": "good#s1"}, "op", true); err != nil {
		t.Fatal(err)
	}
	until("an ask whether to check", func() bool { a := open(); return len(a) == 1 && a[0].Ask.Secret == nil })
	if err := st.AnswerAsk(ctx, r.Journal, open()[0].Note.Seq, 1, "", "op", false); err != nil {
		t.Fatal(err)
	}
	until("the yardmaster given its first prompt", func() bool { return len(pane.prompted()) > 0 })
	if got := pane.prompted()[0]; got != "yardmaster-2: prime yourself" {
		t.Errorf("first prompt: %q", got)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "logins"))
	if strings.TrimSpace(string(b)) != "auth login --claudeai" {
		t.Errorf("the stand-in was logged in with %q", b)
	}
	if calls, _ := os.ReadFile(filepath.Join(dir, "calls")); len(calls) == 0 {
		t.Errorf("the check was picked, and no call was made")
	}

	evs, err := st.EventsSince(ctx, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "good#s1") {
			t.Errorf("event %s holds the code", e.Kind)
		}
	}
	notes, err := st.Notes(ctx, r.Journal)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range notes {
		if strings.Contains(n.Text, "good#s1") {
			t.Errorf("note %d holds the code", n.Seq)
		}
	}
	page, err := st.Inbox(ctx, "yardmaster", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page.Events {
		if b, _ := json.Marshal(e); strings.Contains(string(b), "good#s1") {
			t.Errorf("mail holds the code")
		}
	}
	if n := len(open()); n != 0 {
		t.Errorf("%d asks still open", n)
	}
}
