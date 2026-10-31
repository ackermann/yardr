package store

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// RecallHits is how many journal entries a recall returns, the best by
// score: a word that many entries hold would otherwise fill the answer.
// The rest are counted (JournalRecall.Omitted), not returned.
const RecallHits = 20

// recallShortWord is the length, in characters, under which a word of a
// query is left out: "to" and "of" are in nearly every entry and say
// nothing of which one is meant.
const recallShortWord = 3

// recallQuery is a query as a recall reads it: the words to look for, and
// the query whole as one phrase.
type recallQuery struct {
	words  []string // each once, lowercase
	phrase string   // every word as typed, in order, lowercase, one space between
}

// newRecallQuery reads query as the words on its white space, lowercase.
// A word shorter than recallShortWord is dropped, unless that leaves none
// to look for.
func newRecallQuery(query string) recallQuery {
	fields := strings.Fields(strings.ToLower(query))
	q := recallQuery{phrase: strings.Join(fields, " ")}
	var all []string
	for _, w := range fields {
		if slices.Contains(all, w) {
			continue
		}
		all = append(all, w)
		if utf8.RuneCountInString(w) >= recallShortWord {
			q.words = append(q.words, w)
		}
	}
	if len(q.words) == 0 {
		q.words = all
	}
	return q
}

// none reports a query with no word in it: there is nothing to look for.
func (q recallQuery) none() bool { return len(q.words) == 0 }

// score is how well text answers the query: the number of its words that
// text holds, as part of a longer word too, and one more when text holds
// the query whole, so that of two entries with the same words the one with
// them side by side comes first. 0 is no answer.
func (q recallQuery) score(text string) int {
	if q.none() {
		return 0
	}
	text = strings.ToLower(text)
	n := 0
	for _, w := range q.words {
		if strings.Contains(text, w) {
			n++
		}
	}
	if strings.Contains(text, q.phrase) {
		n++
	}
	return n
}

// Recall lists live memories visible in scope (its own and "all") that
// answer query (see recallQuery), titles only: the ones holding more of
// its words first, and of equals in the order the memories are listed in.
// With no words in query it lists them all.
func (s *Store) Recall(ctx context.Context, scope, query string) ([]MemoryInfo, error) {
	q := newRecallQuery(query)
	scores := map[string]int{}
	beads, err := s.beadsWhere(true, time.Time{}, 0, func(_ *bolt.Tx, b *Bead) bool {
		if b.Type != TypeMemory || !b.open() {
			return false
		}
		sc, _ := b.Metadata["scope"].(string)
		if scope != "" && sc != "all" && sc != scope {
			return false
		}
		if q.none() {
			return true
		}
		scores[b.ID] = q.score(b.Title + "\n" + b.Body)
		return scores[b.ID] > 0
	})
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(beads, func(a, b Bead) int { return cmp.Compare(scores[b.ID], scores[a.ID]) })
	out := []MemoryInfo{}
	for _, b := range beads {
		mi := MemoryInfo{ID: b.ID, Title: b.Title, Expires: expiry(&b), UpdatedAt: b.UpdatedAt}
		mi.Scope, _ = b.Metadata["scope"].(string)
		out = append(out, mi)
	}
	return out, nil
}

// JournalHit is one entry of a crew member's journal that a recall found.
type JournalHit struct {
	Crew string `json:"crew"`
	Note Note   `json:"note"`
}

// JournalRecall is what RecallJournal found: the best RecallHits entries,
// and how many more answered the query and are not here.
type JournalRecall struct {
	Hits    []JournalHit `json:"hits"`
	Omitted int          `json:"omitted"`
}

// RecallJournal lists the entries of crew journals that answer query (see
// recallQuery), the ones holding more of its words first and of equals the
// newest first: the journal read back, which the prime's titles and `crew
// show`'s last lines are not. Only the first RecallHits are returned; the
// rest are counted. With no words in query there is nothing to find. scope
// keeps one crew member's journal ("crew:<name>"); "" and "all" search
// every crew member's.
func (s *Store) RecallJournal(ctx context.Context, scope, query string) (*JournalRecall, error) {
	out := &JournalRecall{Hits: []JournalHit{}}
	q := newRecallQuery(query)
	if q.none() {
		return out, nil
	}
	crew, err := s.Crew(ctx)
	if err != nil {
		return nil, err
	}
	type found struct {
		JournalHit
		score int
	}
	var all []found
	for _, r := range crew {
		if scope != "" && scope != "all" && scope != "crew:"+r.Name {
			continue
		}
		notes, err := s.Notes(ctx, r.Journal)
		if err != nil {
			return nil, err
		}
		for _, n := range notes {
			if score := q.score(n.Text); score > 0 {
				all = append(all, found{JournalHit{Crew: r.Name, Note: n}, score})
			}
		}
	}
	// Newest first among equals, by the notes' store-wide order.
	slices.SortFunc(all, func(a, b found) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(b.Note.Seq, a.Note.Seq))
	})
	for _, f := range all[:min(len(all), RecallHits)] {
		out.Hits = append(out.Hits, f.JournalHit)
	}
	out.Omitted = len(all) - len(out.Hits)
	return out, nil
}
