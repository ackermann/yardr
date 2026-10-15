package cli

import (
	"context"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// operators answers the pairing form on two yards at once, as former does
// for one. A page is told apart by what it shows: north's pages name
// laptop, which is what the other yard calls itself there, and laptop's
// name north. The first "show or enter" is north's, whose form starts
// first.
type operators struct {
	t  *testing.T
	mu sync.Mutex
	// ways are the answers to "show or enter", in the order asked.
	ways []string
	// by the name the other yard gives itself on the page: whether the
	// check is the same, what the link may carry, and the name to save the
	// other yard under ("" keeps the one it gives).
	same  map[string]bool
	carry map[string][]string
	name  map[string]string
	// expose is whom of the yard the other may write to, by the same name;
	// without an entry the page's ticks stay.
	expose map[string][]string
	// answers are for every other field, by key.
	answers map[string]any
	// abort cancels the form at a field (ctrl+c).
	abort string
	asked []string
	// pages is what the pages showed, as a terminal would.
	pages strings.Builder
}

// other is the name the other yard gives itself on a page of the form.
var other = regexp.MustCompile(`calls itself (\w+)`)

func (o *operators) ask(fields ...huh.Field) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	f := &former{t: o.t, answers: map[string]any{}}
	for k, v := range o.answers {
		f.answers[k] = v
	}
	var page strings.Builder
	for _, fld := range fields {
		fld.WithTheme(huh.ThemeBase())
		fld.WithWidth(200)
		page.WriteString(fld.View() + "\n")
	}
	o.pages.WriteString(page.String())
	who := ""
	if m := other.FindStringSubmatch(page.String()); m != nil {
		who = m[1]
	}
	for _, fld := range fields {
		switch key := fld.GetKey(); key {
		case "pair_way":
			if len(o.ways) > 0 {
				f.answers[key], o.ways = o.ways[0], o.ways[1:]
			}
		case "pair_same":
			f.answers[key] = o.same[who]
		case "pair_carry":
			if c, ok := o.carry[who]; ok {
				f.answers[key] = c
			}
		case "pair_expose":
			if e, ok := o.expose[who]; ok {
				f.answers[key] = e
			}
		case "pair_name":
			if n := o.name[who]; n != "" {
				f.answers[key] = n
			}
		}
	}
	if o.abort != "" {
		f.abort = func(fld huh.Field) bool { return fld.GetKey() == o.abort }
	}
	err := f.ask(fields...)
	o.asked = append(o.asked, f.asked...)
	return err
}

// set gives the answer to a field, while the forms run.
func (o *operators) set(key string, v any) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answers[key] = v
}

var shownCode = regexp.MustCompile(`The code\s+([0-9A-Z]{4}-[0-9A-Z]{4})\n\s+The address\s+(http://\S+)`)

// formYards are two yards whose operators sit at a terminal, with o
// answering both forms, a daemon carrying each yard's messages, and a short
// wait for the pong.
func formYards(t *testing.T, o *operators) (north, laptop *cliYard) {
	t.Helper()
	north, laptop = pairYards(t)
	o.t = t
	if o.answers == nil {
		o.answers = map[string]any{}
	}
	setInteractive(t, true)
	stubAsk(t, o.ask)
	stub(t, &pairFormPing, 20*time.Second)
	stub(t, &answers, realAnswers)
	bothStderr(t)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{}, 2)
	for _, y := range []*cliYard{north, laptop} {
		// The form looks whether a daemon runs before it waits for a pong.
		unlock, err := lockDaemon(y.a.cfg.DaemonLock(), 0)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(unlock)
		d := &dispatch.Dispatcher{Store: y.st, Runners: map[string]dispatch.Runner{}, PeerKey: y.a.cfg.PeerKey()}
		go func() {
			defer func() { done <- struct{}{} }()
			for ctx.Err() == nil {
				_ = d.Reconcile(ctx)
				time.Sleep(20 * time.Millisecond)
			}
		}()
	}
	t.Cleanup(func() {
		stop()
		<-done
		<-done
	})
	return north, laptop
}

// formBoth runs bare yardr peer pair on north and, once its form shows a
// code, on laptop, whose form is given the address and the code. It returns
// what the two printed and how each ended. Without a code on north's
// screen laptop's form is not run.
func formBoth(t *testing.T, o *operators, north, laptop *cliYard) (out string, hostErr, callErr error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	var printed output
	read := make(chan struct{})
	go func() { _, _ = io.Copy(&printed, r); close(read) }()
	pair := func(y *cliYard) error {
		c := y.a.peerCmd()
		c.SetArgs([]string{"pair"})
		c.SilenceUsage, c.SilenceErrors = true, true
		return c.Execute()
	}
	hosted := make(chan error, 1)
	go func() { hosted <- pair(north) }()
	var m []string
	ended := false
	for deadline := time.Now().Add(10 * time.Second); m == nil && !ended && time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		m = shownCode.FindStringSubmatch(printed.String())
		select {
		case hostErr = <-hosted:
			ended = true
		default:
		}
	}
	if m != nil {
		o.set("pair_code", m[1])
		o.set("pair_address", m[2])
		callErr = pair(laptop)
	}
	if !ended {
		select {
		case hostErr = <-hosted:
		case <-time.After(30 * time.Second):
			t.Error("the form on the host did not end")
		}
	}
	os.Stdout = old
	w.Close()
	<-read
	return printed.String(), hostErr, callErr
}

func carries(p store.Peer) string {
	return strings.Join(p.Send, ",") + " / " + strings.Join(p.Receive, ",")
}

// Bare yardr peer pair in a terminal is a form on both yards: one shows a
// code, the other enters it with the address, both are shown the check
// large and asked the one question, then what the link may carry and the
// other yard's name. Both end with a link that carries ping and mail, the
// pong that proves it, the line to try the mail with, and how to see and
// remove the link. No key and no invite line is on either screen.
func TestPairForm(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true},
		name: map[string]string{"laptop": "desk", "north": "up-north"}}
	north, laptop := formYards(t, o)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || onNorth[0].Name != "desk" || onNorth[0].Calls != "laptop" || onNorth[0].URL != "" || carries(onNorth[0]) != "mail,ping / mail,ping" {
		t.Fatalf("north's peers = %+v\n%s", onNorth, out)
	}
	if len(onLaptop) != 1 || onLaptop[0].Name != "up-north" || onLaptop[0].Calls != "north" || onLaptop[0].URL != north.srv.URL || carries(onLaptop[0]) != "mail,ping / mail,ping" {
		t.Fatalf("laptop's peers = %+v\n%s", onLaptop, out)
	}
	wantText(t, out,
		"Linking this yard with another", "Read both to the other yard's operator.", "Waiting for the other yard to call.",
		"Calling the other yard.",
		"The other one calls itself laptop.", "The other one calls itself north.",
		"This one knows the other as desk, and the link carries ping and mail, each way.",
		"This one knows the other as up-north, and the link carries ping and mail, each way.",
		"desk answered in ", "up-north answered in ", ": the link works.",
		`mail yardmaster@desk "hello from the other yard"`, `mail yardmaster@up-north "hello from the other yard"`,
		"peer show desk", "peer rm desk", "peer show up-north", "peer rm up-north",
		// Neither exposes a person, so neither is asked whom to trust as one.
		" peer allow desk --trust <name>@desk=<user>` does it later", " peer allow up-north --trust <name>@up-north=<user>` does it later")
	// The check is on both screens, the same, large and small.
	check := regexp.MustCompile(`\n    (\d{3} \d{3})\n`).FindAllStringSubmatch(out, -1)
	if len(check) != 2 || check[0][1] != check[1][1] || strings.Count(out, large(check[0][1])) != 2 {
		t.Fatalf("the checks shown: %v\n%s", check, out)
	}
	pages := o.pages.String()
	wantText(t, pages, "Show a code, or enter one?", "Does the other screen show the same?", "What may this link carry?",
		"ping: each yard may ask the other whether it is there", "mail between crew members: this yard's may write to laptop's, and laptop's to this yard's",
		"file: files in mail cross the link, kept within peer_file_max_bytes",
		"This yard's name for the other one", "The other yard's address", "The code")
	for _, y := range []*cliYard{north, laptop} {
		for _, p := range peersOf(t, y) {
			if strings.Contains(out+pages, p.Key) {
				t.Errorf("a key is on a screen of the form:\n%s", out)
			}
		}
	}
	if strings.Contains(out+pages, "yardr1:") || strings.Contains(out+pages, "invite") {
		t.Errorf("an invite line is on a screen of the form:\n%s\n%s", out, pages)
	}
	want := []string{"pair_way", "pair_way", "pair_address", "pair_code", "pair_same", "pair_same", "pair_carry", "pair_carry", "pair_expose", "pair_expose", "pair_name", "pair_name"}
	got := slices.Clone(o.asked)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("asked %q", o.asked)
	}
	// Every question came before either yard saved anything: the two
	// links were saved after the last name was asked for.
	for _, y := range []*cliYard{north, laptop} {
		if evs := eventsOf(t, y, "peer_allowed"); len(evs) != 2 {
			t.Errorf("peer_allowed events = %+v", evs)
		}
	}
}

// An operator who unticks mail gets a link that carries ping alone, and
// is shown no line to try mail with. The other's choice is their own yard's.
func TestPairFormWithoutMail(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true},
		carry: map[string][]string{"north": {store.PeerPing}}}
	north, laptop := formYards(t, o)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || carries(onNorth[0]) != "mail,ping / mail,ping" || len(onLaptop) != 1 || carries(onLaptop[0]) != "ping / ping" {
		t.Fatalf("peers: north %+v, laptop %+v\n%s", onNorth, onLaptop, out)
	}
	wantText(t, out, "This one knows the other as north, and the link carries ping, each way.", "mail yardmaster@laptop ")
	if strings.Contains(out, "mail yardmaster@north") {
		t.Errorf("mail is offered on a link without it:\n%s", out)
	}
}

// An operator who ticks file gets a link that carries it beside ping and
// mail, each way, and is told how an image or a page goes. It is not ticked
// for them: the other's link carries what it did.
func TestPairFormWithFile(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true},
		carry: map[string][]string{"laptop": {store.PeerPing, store.PeerMail, store.PeerFile}}}
	north, laptop := formYards(t, o)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || carries(onNorth[0]) != "file,mail,ping / file,mail,ping" || len(onLaptop) != 1 || carries(onLaptop[0]) != "mail,ping / mail,ping" {
		t.Fatalf("peers: north %+v, laptop %+v\n%s", onNorth, onLaptop, out)
	}
	wantText(t, out, "This one knows the other as laptop, and the link carries ping, mail and file, each way.",
		"This one knows the other as north, and the link carries ping and mail, each way.")
	if n := strings.Count(out, "An image or a page goes with the mail (`--file <image or page>`)"); n != 1 {
		t.Errorf("how an image or a page goes is said %d times, and one yard ticked file:\n%s", n, out)
	}
}

// file cannot be ticked without mail, which carries the files:
// the form does not go on until mail is ticked too.
func TestPairFormFileGoesWithMail(t *testing.T) {
	stubAsk(t, func(fields ...huh.Field) error {
		carry := fields[0].(*huh.MultiSelect[string])
		carry.WithKeyMap(huh.NewDefaultKeyMap())
		carry.Focus()
		x, down, up := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyUp}
		if got := carry.GetValue().([]string); !slices.Equal(got, []string{store.PeerPing, store.PeerMail}) {
			t.Fatalf("ticked before anything is: %v", got)
		}
		for _, k := range []tea.KeyMsg{down, x, down, x} { // mail off, file on
			carry.Update(k)
		}
		if got := carry.GetValue().([]string); !slices.Equal(got, []string{store.PeerPing, store.PeerFile}) {
			t.Fatalf("after unticking mail and ticking file: %v", got)
		}
		carry.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if err := carry.Error(); err == nil || !strings.Contains(err.Error(), "file goes with mail") {
			t.Fatalf("a page with file and no mail goes on: %v", err)
		}
		carry.Update(up)
		carry.Update(x)
		carry.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if err := carry.Error(); err != nil {
			t.Fatalf("with mail ticked again: %v", err)
		}
		return nil
	})
	var f pairChoice
	if _, err := f.ask("north", nil, nil, func(string) error { return nil }); err != nil || !f.mail || !f.file {
		t.Fatalf("answers: mail %v, file %v, %v", f.mail, f.file, err)
	}
}

// ping cannot be unticked: the page does not go on without it.
func TestPairFormKeepsPing(t *testing.T) {
	stubAsk(t, func(fields ...huh.Field) error {
		carry := fields[0].(*huh.MultiSelect[string])
		carry.WithKeyMap(huh.NewDefaultKeyMap())
		carry.Focus()
		carry.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}) // the first option, ping
		if got := carry.GetValue().([]string); !slices.Equal(got, []string{store.PeerMail}) {
			t.Fatalf("after unticking the first option: %v", got)
		}
		carry.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if err := carry.Error(); err == nil || !strings.Contains(err.Error(), "ping stays ticked") {
			t.Fatalf("a page without ping goes on: %v", err)
		}
		carry.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
		carry.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if err := carry.Error(); err != nil {
			t.Fatalf("with ping ticked again: %v", err)
		}
		return nil
	})
	var f pairChoice
	name, err := f.ask("north", nil, nil, func(string) error { return nil })
	if err != nil || name != "north" || !f.mail || f.file || f.expose != nil {
		t.Fatalf("answers: %q, mail %v, file %v, expose %v, %v", name, f.mail, f.file, f.expose, err)
	}
}

// people gives a yard a yardmaster, a second crew member and, beside its
// operator, a second user: whom its form offers to expose.
func people(t *testing.T, y *cliYard) {
	t.Helper()
	ctx := context.Background()
	for _, name := range []string{"yardmaster", "clerk"} {
		if _, err := y.st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", Dir: y.home, Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if err := y.st.SetSetting(ctx, store.YardmasterSetting, "yardmaster", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.st.AddUser(ctx, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
}

// After what the link may carry, the form asks whom of this yard the other
// one may write to: its crew members and its users, the yardmaster ticked.
// What is ticked is exposed on the link, in the write that allows the
// mail, and the other yard is told the names. Each operator answers for
// their own yard.
func TestPairFormAsksWhoMayBeWrittenTo(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true},
		// laptop's operator, whose pages name north, ticks a user as well.
		expose: map[string][]string{"north": {"yardmaster", "sam"}}}
	north, laptop := formYards(t, o)
	people(t, north)
	people(t, laptop)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || !slices.Equal(onNorth[0].Expose, []string{"yardmaster"}) || carries(onNorth[0]) != "mail,ping / mail,ping" {
		t.Fatalf("north's peers = %+v\n%s", onNorth, out)
	}
	if len(onLaptop) != 1 || !slices.Equal(onLaptop[0].Expose, []string{"sam", "yardmaster"}) {
		t.Fatalf("laptop's peers = %+v\n%s", onLaptop, out)
	}
	pages := o.pages.String()
	// The yardmaster is first, though clerk sorts before it: the list opens
	// at its first ticked option, and what is above that is out of sight.
	wantText(t, pages, "Who may laptop write to?", "Who may north write to?",
		"[•] yardmaster  (crew member, the yardmaster)", "[ ] clerk  (crew member)", "[ ] jo  (user, the operator)", "[ ] sam  (user)")
	wantText(t, out, "laptop may write to: yardmaster.", "north may write to: sam, yardmaster.")
	if strings.Contains(out, "as taking no mail") {
		t.Errorf("mail is ticked, and the names are said to take none:\n%s", out)
	}
	// The question comes after what the link may carry and before the name.
	for _, want := range [][]string{{"pair_carry", "pair_expose", "pair_name"}} {
		n := 0
		for i := 0; i+len(want) <= len(o.asked); i++ {
			if slices.Equal(o.asked[i:i+len(want)], want) {
				n++
			}
		}
		if n != 2 {
			t.Errorf("asked %q", o.asked)
		}
	}
	// One write allows the mail and exposes the names: the pairing's own
	// and this one.
	for _, y := range []*cliYard{north, laptop} {
		evs := eventsOf(t, y, "peer_allowed")
		if len(evs) != 2 || evs[1].Data["expose"] == nil {
			t.Errorf("peer_allowed events = %+v", evs)
		}
	}
	// What each yard exposes is what the other is told, and shows.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if p := peersOf(t, north); p[0].Said != nil && len(p[0].Said.Roster) == 2 {
			break
		}
	}
	if said := peersOf(t, north)[0].Said; said == nil || len(said.Roster) != 2 || said.Roster[0].Name != "sam" || !said.Roster[0].Mail {
		t.Fatalf("what laptop said to north: %+v", said)
	}
}

// Names ticked on a link whose mail is not: the form says, before anything
// is saved, that they will be listed as taking no mail, and exposes them
// all the same. Nobody ticked exposes nobody, and the form says how to.
func TestPairFormExposesWithoutMail(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true},
		carry:  map[string][]string{"north": {store.PeerPing}},
		expose: map[string][]string{"north": {"yardmaster", "clerk"}, "laptop": {}}}
	north, laptop := formYards(t, o)
	people(t, north)
	people(t, laptop)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if hostErr != nil || callErr != nil {
		t.Fatalf("host: %v\ncaller: %v\n%s", hostErr, callErr, out)
	}
	onNorth, onLaptop := peersOf(t, north), peersOf(t, laptop)
	if len(onNorth) != 1 || onNorth[0].Expose != nil || carries(onNorth[0]) != "mail,ping / mail,ping" {
		t.Fatalf("north's peers = %+v\n%s", onNorth, out)
	}
	if len(onLaptop) != 1 || !slices.Equal(onLaptop[0].Expose, []string{"clerk", "yardmaster"}) || carries(onLaptop[0]) != "ping / ping" {
		t.Fatalf("laptop's peers = %+v\n%s", onLaptop, out)
	}
	wantText(t, out,
		"Mail is not ticked, so clerk, yardmaster will be listed to north as taking no mail: it sees them here, and cannot\nwrite to them.",
		"north sees clerk, yardmaster, and cannot write to them: the link carries no mail (`",
		"laptop sees nobody of this yard and can write to no one here: `", " peer allow laptop --expose <crew member or user>`.")
	if said := strings.Index(out, "as taking no mail"); said < 0 || said > strings.Index(out, "The two yards are linked") {
		t.Errorf("the names are said to take no mail after the link was saved:\n%s", out)
	}
}

// No at the check, by either operator, saves nothing on either yard. The
// form of the one who said no says so and is no failure; the other's says
// that the other operator declined. Neither is asked anything after it.
func TestPairFormNoAtTheCheck(t *testing.T) {
	for name, no := range map[string]string{"on the yard that shows": "laptop", "on the yard that enters": "north"} {
		t.Run(name, func(t *testing.T) {
			o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": no != "laptop", "north": no != "north"}}
			north, laptop := formYards(t, o)
			out, hostErr, callErr := formBoth(t, o, north, laptop)
			mine, theirs := hostErr, callErr
			if no == "north" {
				mine, theirs = theirs, mine
			}
			if mine != nil || theirs == nil || !strings.Contains(theirs.Error(), "operator declined") || !strings.HasSuffix(theirs.Error(), nothingSaved) {
				t.Fatalf("who said no: %v\nthe other: %v\n%s", mine, theirs, out)
			}
			wantText(t, out, "You said no, so nothing was saved, on this yard or on the other.")
			if len(peersOf(t, north)) != 0 || len(peersOf(t, laptop)) != 0 {
				t.Fatalf("peers: north %+v, laptop %+v", peersOf(t, north), peersOf(t, laptop))
			}
			// The one who said no was not asked what the link may carry.
			if n := strings.Count(strings.Join(o.asked, " "), "pair_carry"); n > 1 {
				t.Errorf("asked %q", o.asked)
			}
			if strings.Contains(out, "The two yards are linked") {
				t.Errorf("a link is said to be there:\n%s", out)
			}
		})
	}
}

// Leaving the form at the questions after the check is a no too.
func TestPairFormLeftAfterTheCheck(t *testing.T) {
	o := &operators{ways: []string{pairShow, pairEnter}, same: map[string]bool{"laptop": true, "north": true}, abort: "pair_carry"}
	north, laptop := formYards(t, o)
	out, hostErr, callErr := formBoth(t, o, north, laptop)
	if len(peersOf(t, north)) != 0 || len(peersOf(t, laptop)) != 0 {
		t.Fatalf("peers: north %+v, laptop %+v\nhost: %v\ncaller: %v\n%s", peersOf(t, north), peersOf(t, laptop), hostErr, callErr, out)
	}
	wantText(t, out, "You said no, so nothing was saved, on this yard or on the other.")
}

// A yard that cannot be called is told so in plain words, with what the
// server says of its listener, and offered the other way round, which goes
// on to the address and the code. Nothing of the yard is changed.
func TestPairFormYardThatCannotBeCalled(t *testing.T) {
	o := &operators{ways: []string{pairShow}, answers: map[string]any{"pair_listen": listenOther}, abort: "pair_address"}
	o.t = t
	laptop := newCLIYard(t, "laptop") // its server has said nothing of a listener
	setInteractive(t, true)
	stubAsk(t, o.ask)
	restarts := 0
	stub(t, &restartServer, func(context.Context, *config.Config) error { restarts++; return nil })
	out := laptop.must(t, "pair")
	wantText(t, out, "This yard cannot be called yet. To show a code it has to take the other yard's call, on an\naddress of this machine that the other yard can reach.",
		"the server has not said what it takes webhooks on",
		"On the other yard, its operator runs `yardr peer pair` and picks \"Show a code\".",
		"Left before the yards were linked: nothing was saved.")
	wantText(t, o.pages.String(), "Set the address now: yardr saves it and restarts its server",
		"Go the other way round: the other yard shows the code, and you enter it here", "Stop here")
	if !slices.Equal(o.asked, []string{"pair_way", "pair_listen", "pair_address"}) || restarts != 0 {
		t.Fatalf("asked %q, %d restarts\n%s", o.asked, restarts, out)
	}
	if all, _ := laptop.st.Settings(context.Background()); all[hooks.FromSetting] != "" {
		t.Fatalf("hook_addr = %q", all[hooks.FromSetting])
	}
	if strings.Contains(strings.ToLower(out+o.pages.String()), "tailscale") {
		t.Errorf("a vendor's product is named:\n%s", out)
	}

	// Stop here changes nothing either; no to the restart saves no address.
	for _, answers := range []map[string]any{
		{"pair_listen": listenStop},
		{"pair_listen": listenSet, "pair_listen_addr": "127.0.0.1:1", "pair_restart": false},
	} {
		o.ways, o.answers, o.abort = []string{pairShow}, answers, ""
		out = laptop.must(t, "pair")
		wantText(t, out, "othing was changed. `", " peer pair` starts again.")
		if all, _ := laptop.st.Settings(context.Background()); all[hooks.FromSetting] != "" || restarts != 0 {
			t.Fatalf("hook_addr = %q, %d restarts\n%s", all[hooks.FromSetting], restarts, out)
		}
	}
}

// With the operator's yes the form saves the address as hook_addr and
// restarts the server; once that listens there, it goes on to the code. A
// server that says it listens where nothing answers is not taken at its word.
func TestPairFormSetsTheAddress(t *testing.T) {
	north := newCLIYard(t, "north")
	addr := strings.TrimPrefix(north.srv.URL, "http://")
	o := &operators{ways: []string{pairShow}, answers: map[string]any{"pair_listen": listenSet, "pair_listen_addr": addr, "pair_restart": true}}
	o.t = t
	setInteractive(t, true)
	stubAsk(t, o.ask)
	stub(t, &listenWait, time.Second)
	stub(t, &answers, realAnswers)
	// The address of a listener that is gone: the server's word for it.
	gone := newCLIYard(t, "gone")
	gone.srv.Close()
	said(t, north.a.cfg, hooks.Listener{Addr: strings.TrimPrefix(gone.srv.URL, "http://"), From: hooks.FromSetting})
	var restarts atomic.Int32
	stub(t, &restartServer, func(_ context.Context, cfg *config.Config) error {
		restarts.Add(1)
		said(t, cfg, hooks.Listener{Addr: addr, From: hooks.FromSetting})
		return nil
	})
	// Ctrl+C once the code is shown: the pairing is ended.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for i := 0; i < 2000 && restarts.Load() == 0; i++ {
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	c := north.a.peerCmd()
	c.SetArgs([]string{"pair"})
	c.SilenceUsage, c.SilenceErrors = true, true
	var err error
	out := capture(t, func() { err = c.ExecuteContext(ctx) })
	if err == nil || !strings.Contains(err.Error(), "the pairing was ended"+nothingSaved) || restarts.Load() != 1 {
		t.Fatalf("%v, %d restarts\n%s", err, restarts.Load(), out)
	}
	wantText(t, out, "and nothing answers there", "The address is saved (hook_addr = "+addr+"). Restarting the server.",
		"The server is back, and takes calls there.", "The address   "+north.srv.URL, "which only a yard on this same machine can reach")
	if !shownCode.MatchString(out) {
		t.Fatalf("no code is shown:\n%s", out)
	}
	if all, _ := north.st.Settings(context.Background()); all[hooks.FromSetting] != addr {
		t.Fatalf("hook_addr = %q", all[hooks.FromSetting])
	}
	if !slices.Equal(o.asked, []string{"pair_way", "pair_listen", "pair_listen_addr", "pair_restart"}) {
		t.Fatalf("asked %q", o.asked)
	}
}

// A call that gets no pairing (a wrong code) is said in the form's words,
// and the form asks for the address and the code again.
func TestPairFormAsksAgainAfterAWrongCode(t *testing.T) {
	o := &operators{ways: []string{pairEnter}, answers: map[string]any{"pair_code": "AAAA-AAAA", "pair_again": false}}
	o.t = t
	north, laptop := pairYards(t)
	o.answers["pair_address"] = north.srv.URL
	setInteractive(t, true)
	stubAsk(t, o.ask)
	out := laptop.must(t, "pair")
	wantText(t, out, "The call did not go through: the yard at "+north.srv.URL+" has no pairing open")
	if !slices.Equal(o.asked, []string{"pair_way", "pair_address", "pair_code", "pair_again"}) || len(peersOf(t, laptop)) != 0 {
		t.Fatalf("asked %q\n%s", o.asked, out)
	}
}

// Where no form runs, yardr peer pair is the command it was: without a
// terminal, with --json, in an agent's session, and with any flag or
// argument. Nothing is asked.
func TestNoPairFormWithoutATerminal(t *testing.T) {
	o := &operators{}
	o.t = t
	laptop := newCLIYard(t, "laptop") // no listener: the command says so, at once
	stubAsk(t, o.ask)
	const refuses, noListener = "--yes confirms the check without asking", "this yard has no listener, so the other yard cannot call it"
	setInteractive(t, false)
	if _, err := laptop.peer(t, "pair"); err == nil || !strings.Contains(err.Error(), refuses) {
		t.Errorf("without a terminal: %v", err)
	}
	setInteractive(t, true)
	laptop.a.json = true
	if _, err := laptop.peer(t, "pair"); err == nil || !strings.Contains(err.Error(), refuses) {
		t.Errorf("with --json: %v", err)
	}
	laptop.a.json = false
	for _, v := range []string{"YARDR_SESSION", "YARDR_CREW"} {
		t.Setenv(v, "s1")
		if _, err := laptop.peer(t, "pair"); err == nil || !strings.Contains(err.Error(), noListener) {
			t.Errorf("with %s: %v", v, err)
		}
		t.Setenv(v, "")
	}
	for _, args := range [][]string{{"--name", "north"}, {"--url", "http://127.0.0.1:1"}, {"--yes"}, {"--timeout", "1s"}} {
		if _, err := laptop.peer(t, append([]string{"pair"}, args...)...); err == nil || !strings.Contains(err.Error(), noListener) {
			t.Errorf("with %v: %v", args, err)
		}
	}
	if _, err := laptop.peer(t, "pair", "--end"); err == nil || !strings.Contains(err.Error(), "this yard has no pairing open") {
		t.Errorf("with --end: %v", err)
	}
	if len(o.asked) != 0 {
		t.Fatalf("asked %q where no form runs", o.asked)
	}
}

// The onboarding offers the link among what the yard runs without, and a
// yes opens the form in the same run. yardr doctor's line for a yard with
// no peer names the command.
func TestSetupOffersTheLink(t *testing.T) {
	home := newHome(t)
	actAsOperator(t)
	stubServices(t, "darwin")
	stubWeb(t)
	st := storetest.New(t)
	out, keys := onboardTo(t, st, map[string]any{"link": true, "pair_listen": listenStop})
	if !slices.Equal(keys[len(keys)-5:], []string{"web", "link", "pair_way", "pair_listen", "go"}) {
		t.Fatalf("asked %q\n%s", keys, out)
	}
	wantText(t, out, "Linking this yard with another", "This yard cannot be called yet.", "Stopped: nothing was changed.")
	part, err := peerPart(context.Background(), &config.Config{Home: home}, st)
	if err != nil || part.Configured || !strings.Contains(part.Detail, here("yardr peer pair")+" in a terminal links it with one, as a form") {
		t.Fatalf("doctor's peers line: %+v, %v", part, err)
	}

	// The guided setup asks at its end, once a run, and not on a yard
	// that has a peer.
	f := &former{t: t, answers: map[string]any{}}
	stubAsk(t, f.ask)
	a := &app{st: st}
	cfg := &config.Config{Home: home}
	for i := 0; i < 2; i++ {
		if err := a.offerLink(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(keysOf(f), []string{"link"}) {
		t.Fatalf("asked %q", keysOf(f))
	}
	actor, aerr := config.Actor()
	if aerr != nil {
		t.Fatal(aerr)
	}
	if _, err := st.AddPeer(context.Background(), store.Peer{Name: "north", Key: "a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY"}, actor); err != nil {
		t.Fatal(err)
	}
	f.asked = nil
	if err := (&app{st: st}).offerLink(context.Background(), cfg); err != nil || len(f.asked) != 0 {
		t.Fatalf("a yard with a peer is asked %q, %v", f.asked, err)
	}
}

// Once the link is proved the form asks whom of the other yard to trust as
// a person of this one: the users of this yard whose name the other yard
// exposes as a person, nobody ticked. What is ticked is granted on the
// link. It does not ask while whom the other yard exposes is not known, nor
// when nobody it exposes has a user's name here, and says then what does it
// later. Leaving the question trusts nobody and is no failure.
func TestPairFormAsksWhomToTrust(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	ctx := context.Background()
	t.Setenv("YARDR_HOME", north.home)
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	t.Setenv("YARDR_HOME", south.home)
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", sInvite)
	south.must(t, "add", "up-north", nInvite)
	for _, name := range []string{"ada", "sam"} {
		if _, err := north.st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := north.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	// South has a user sam too, and one called as north's yardmaster is.
	for _, name := range []string{"sam", "yardmaster"} {
		if _, err := south.st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	step := func(proved bool, f *former) (string, []string) {
		t.Helper()
		f.t = t
		stubAsk(t, f.ask)
		var err error
		out := capture(t, func() { err = south.a.askTrust(ctx, south.a.cfg, south.st, "up-north", proved) })
		if err != nil {
			t.Fatalf("askTrust: %v\n%s", err, out)
		}
		return out, f.asked
	}
	trusts := func() string {
		t.Helper()
		p, err := south.st.GetPeer(ctx, "up-north")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range p.Trusted() {
			out = append(out, g.String())
		}
		return strings.Join(out, ", ")
	}
	const later = " peer allow up-north --trust <name>@up-north=<user>` does it later"

	// North has said nothing yet, proved or not: not asked.
	for _, proved := range []bool{false, true} {
		out, asked := step(proved, &former{})
		if len(asked) != 0 {
			t.Fatalf("proved %v, nothing said: asked %q", proved, asked)
		}
		wantText(t, out, "Whom up-north exposes is not known yet, so the form does not ask: `", later)
	}

	// It exposes people of whom none has a user's name here, and a crew
	// member who has: still nobody to ask about.
	north.must(t, "allow", "south", "--send", "mail", "--expose", "ada,yardmaster")
	south.must(t, "allow", "up-north", "--receive", "mail")
	carry(t, north, south)
	out, asked := step(true, &former{})
	if len(asked) != 0 {
		t.Fatalf("nobody of a user's name exposed: asked %q", asked)
	}
	wantText(t, out, "up-north exposes nobody who has the name of a person of this yard, so the form does not ask: `", later)

	// It exposes jo and sam, who are users here: asked, nothing ticked.
	north.must(t, "allow", "south", "--expose", "jo,sam")
	carry(t, north, south)
	if out, asked = step(false, &former{}); len(asked) != 0 || !strings.Contains(out, "is not known yet") {
		t.Fatalf("a link that is not proved: asked %q\n%s", asked, out)
	}
	pages := &operators{t: t, answers: map[string]any{}}
	stubAsk(t, pages.ask)
	var err error
	out = capture(t, func() { err = south.a.askTrust(ctx, south.a.cfg, south.st, "up-north", true) })
	if err != nil || !slices.Equal(pages.asked, []string{"pair_trust"}) || trusts() != "" {
		t.Fatalf("asked %q, trusted %q, %v\n%s", pages.asked, trusts(), err, out)
	}
	wantText(t, pages.pages.String(), "Trust a name on up-north as a person of this yard?",
		"[ ] jo  (jo@up-north speaks as jo)", "[ ] sam  (sam@up-north speaks as sam)", "--no-trust <name>@up-north")
	for _, not := range []string{"ada", "yardmaster  (", "[•]"} {
		if strings.Contains(pages.pages.String(), not) {
			t.Errorf("the question shows %q:\n%s", not, pages.pages.String())
		}
	}
	wantText(t, out, "No name on up-north is trusted as a person of this yard: `", later)

	// Leaving it trusts nobody; ticking one grants it.
	out, asked = step(true, &former{abort: func(f huh.Field) bool { return f.GetKey() == "pair_trust" }})
	if !slices.Equal(asked, []string{"pair_trust"}) || trusts() != "" {
		t.Fatalf("left at the question: asked %q, trusted %q\n%s", asked, trusts(), out)
	}
	// Whoever is ticked has a second question, of its own: whether their
	// asks are sent there as well. Nothing is ticked, and nothing sent.
	reaches := func() string {
		t.Helper()
		p, err := south.st.GetPeer(ctx, "up-north")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range p.Reaching() {
			out = append(out, g.String())
		}
		return strings.Join(out, ", ")
	}
	pages = &operators{t: t, answers: map[string]any{"pair_trust": []string{"sam"}}}
	stubAsk(t, pages.ask)
	out = capture(t, func() { err = south.a.askTrust(ctx, south.a.cfg, south.st, "up-north", true) })
	if err != nil || !slices.Equal(pages.asked, []string{"pair_trust", "pair_reach"}) || trusts() != "sam@up-north -> sam" || reaches() != "" {
		t.Fatalf("asked %q, trusted %q, reach %q, %v\n%s", pages.asked, trusts(), reaches(), err, out)
	}
	wantText(t, pages.pages.String(), "Send their questions to up-north as well?",
		"[ ] sam  (asks for sam are also sent to sam@up-north)", "with its bead's handle, title, topic, stage, labels and cut body",
		"can answer as them", "--no-reach <name>@up-north")
	wantText(t, out, "Mail from sam@up-north counts as sam of this yard speaking.",
		"No ask for a person of this yard is sent to up-north: `", " peer allow up-north --reach <name>@up-north=<user>` does it later.")
	out, asked = step(true, &former{answers: map[string]any{"pair_trust": []string{"sam"}},
		abort: func(f huh.Field) bool { return f.GetKey() == "pair_reach" }})
	if !slices.Equal(asked, []string{"pair_trust", "pair_reach"}) || trusts() != "sam@up-north -> sam" || reaches() != "" {
		t.Fatalf("left at the second question: asked %q, trusted %q, reach %q\n%s", asked, trusts(), reaches(), out)
	}
	out, _ = step(true, &former{answers: map[string]any{"pair_trust": []string{"sam"}, "pair_reach": []string{"sam"}}})
	if reaches() != "sam@up-north -> sam" {
		t.Fatalf("reach %q\n%s", reaches(), out)
	}
	wantText(t, out, "Reach: sam@up-north -> sam: every ask for sam is also sent there, the ask whole, with its bead's handle, title, topic, stage, labels and cut body, and the handle, title and stage of each bead it is about,",
		"(nothing is sent while the link carries no ask out: yardr peer allow up-north --send ask)")
}
