package web

import (
	"bytes"
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// talkKind is how a letter's article reads, from its classes: head or grouped
// under the head above it, opening a day, and saying a line its neighbour
// does not.
func talkKind(class string) string {
	has := func(token string) bool { return slices.Contains(strings.Fields(class), token) }
	out := "grouped"
	if has("head") {
		out = "head"
	}
	if has("day") {
		out += " day"
	}
	if has("differs") {
		out += " differs"
	}
	return out
}

// How the talk page groups mail (talk.go's grouped): a letter reads under the
// head above it only if it has the same sender, the letter's From and not the
// line the page names for it, and is within five minutes of the letter above
// it, on the same day in the reader's zone; a letter that opens a day, as
// the first of the page does, carries the day's separator. Each row is a
// conversation between jo and the yardmaster, oldest first, and what the page
// makes of each letter: the classes of its article, which the stylesheet and
// yard.js go by, and the words of the day it opens.
func TestTalkGrouping(t *testing.T) {
	t.Parallel()
	s, err := NewServer(nil, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	utc := time.UTC
	west := time.FixedZone("UTC-5", -5*60*60)
	at := func(day, h, m, sec int) time.Time { return time.Date(2026, 10, day, h, m, sec, 0, time.UTC) }
	// Friday 30 October, midday: the 29th is yesterday.
	now := at(30, 12, 0, 0)
	jo := func(when time.Time) store.Letter { return store.Letter{From: "jo", To: "yardmaster", At: when} }
	master := func(when time.Time) store.Letter { return store.Letter{From: "yardmaster", To: "jo", At: when} }
	via := func(l store.Letter, device string) store.Letter { l.Via = device; return l }
	as := func(l store.Letter, user string) store.Letter { l.As = user; return l }
	to := func(l store.Letter, name string) store.Letter { l.To = name; return l }

	for _, c := range []struct {
		name    string
		zone    *time.Location
		letters []store.Letter
		// want is how each letter reads; days, where set, the words of the
		// separator each opens ("" for none).
		want        []string
		days        []string
		wantSenders []string
	}{
		{name: "the same sender four minutes apart", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), jo(at(30, 9, 4, 0))},
			want:    []string{"head day", "grouped"}, days: []string{"Today", ""}},
		{name: "five minutes apart is still within", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), jo(at(30, 9, 5, 0))},
			want:    []string{"head day", "grouped"}},
		{name: "a second past five minutes", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), jo(at(30, 9, 5, 1))},
			want:    []string{"head day", "head"}},
		{name: "the same sender six minutes apart", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), jo(at(30, 9, 6, 0))},
			want:    []string{"head day", "head"}, days: []string{"Today", ""}},
		{name: "another sender heads its own group however close", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), master(at(30, 9, 0, 5)), jo(at(30, 9, 0, 10))},
			want:    []string{"head day", "head", "head"}},
		{name: "the five minutes run from the letter above, not from the head", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 0, 0)), jo(at(30, 9, 4, 0)), jo(at(30, 9, 8, 0)), jo(at(30, 9, 14, 0))},
			want:    []string{"head day", "grouped", "grouped", "head"}},
		{name: "a letter stamped before the one above groups by the gap", zone: utc,
			letters: []store.Letter{jo(at(30, 9, 4, 0)), jo(at(30, 9, 0, 0))},
			want:    []string{"head day", "grouped"}},
		{name: "across midnight, a day separator and a new head", zone: utc,
			letters: []store.Letter{jo(at(29, 23, 58, 0)), jo(at(30, 0, 2, 0))},
			want:    []string{"head day", "head day"}, days: []string{"Yesterday", "Today"}},
		{name: "the day is the reader's: the same instants in a zone five hours behind", zone: west,
			letters: []store.Letter{jo(at(29, 23, 58, 0)), jo(at(30, 0, 2, 0))},
			want:    []string{"head day", "grouped"}, days: []string{"Yesterday", ""}},
		{name: "a day older than yesterday is named by its weekday", zone: utc,
			letters: []store.Letter{jo(at(17, 9, 0, 0)), jo(at(30, 9, 0, 0))},
			want:    []string{"head day", "head day"}, days: []string{"Saturday 17 Oct", "Today"}},
		{name: "the device differing on one sender", zone: utc,
			letters:     []store.Letter{jo(at(30, 9, 0, 0)), via(jo(at(30, 9, 2, 0)), "phone"), via(jo(at(30, 9, 3, 0)), "phone")},
			want:        []string{"head day", "grouped differs", "grouped"},
			wantSenders: []string{"jo", "jo (via phone)", "jo (via phone)"}},
		{name: "the name it speaks as differing on one sender", zone: utc,
			letters:     []store.Letter{{From: "jo@north", To: "yardmaster", At: at(30, 9, 0, 0)}, as(store.Letter{From: "jo@north", To: "yardmaster", At: at(30, 9, 2, 0)}, "jo")},
			want:        []string{"head day", "grouped differs"},
			wantSenders: []string{"jo@north", "jo@north (as jo)"}},
		{name: "the addressee differing on one sender", zone: utc,
			letters:     []store.Letter{master(at(30, 9, 0, 0)), to(master(at(30, 9, 2, 0)), "jo@north")},
			want:        []string{"head day", "grouped differs"},
			wantSenders: []string{"yardmaster", "yardmaster to jo@north"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for i := range c.letters {
				c.letters[i].Seq = uint64(i + 1)
			}
			shown, err := s.talkLetters(context.Background(), "jo", "yardmaster", c.letters)
			if err != nil {
				t.Fatal(err)
			}
			groupTalk(shown, c.zone, now)
			var got, days []string
			for i, l := range shown {
				if len(c.wantSenders) > 0 && l.Sender != c.wantSenders[i] {
					t.Errorf("letter %d is named %q, want %q", i+1, l.Sender, c.wantSenders[i])
				}
				var body bytes.Buffer
				if err := s.pages["talk"].ExecuteTemplate(&body, "letter", l); err != nil {
					t.Fatal(err)
				}
				class := regexp.MustCompile(`<article class="([^"]*)"`).FindStringSubmatch(body.String())
				if class == nil {
					t.Fatalf("the letter draws no article: %s", body.String())
				}
				got = append(got, talkKind(class[1]))
				days = append(days, l.Day)
				// The separator's words stand in the letter that opens the day.
				if want := `<p class="day-break">` + l.Day + `</p>`; !strings.Contains(body.String(), want) {
					t.Errorf("letter %d lacks %s: %s", l.Seq, want, body.String())
				}
			}
			if !slices.Equal(got, c.want) {
				t.Errorf("the letters read %q, want %q", got, c.want)
			}
			if c.days != nil && !slices.Equal(days, c.days) {
				t.Errorf("the letters open the days %q, want %q", days, c.days)
			}
		})
	}
}
