package store

import (
	"slices"
	"testing"
)

// A query is read as its words, lowercase and each once; a word under three
// characters is left out unless no word is longer, and the phrase keeps
// every word as typed.
func TestRecallQueryWords(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query  string
		words  []string
		phrase string
	}{
		{"Worktree remove cleanup timeout", []string{"worktree", "remove", "cleanup", "timeout"}, "worktree remove cleanup timeout"},
		{"  worktree \t cleanup\n", []string{"worktree", "cleanup"}, "worktree cleanup"},
		{"Cleanup cleanup CLEANUP", []string{"cleanup"}, "cleanup cleanup cleanup"},
		{"ci pipeline", []string{"pipeline"}, "ci pipeline"},
		{"go", []string{"go"}, "go"},
		{"to be", []string{"to", "be"}, "to be"},
		{"部署 ab", []string{"部署", "ab"}, "部署 ab"},
		{"部署計画 ab", []string{"部署計画"}, "部署計画 ab"},
		{"", nil, ""},
		{" \t\n", nil, ""},
	} {
		q := newRecallQuery(tc.query)
		if !slices.Equal(q.words, tc.words) || q.phrase != tc.phrase || q.none() != (len(tc.words) == 0) {
			t.Errorf("newRecallQuery(%q) = %q, phrase %q, none %v; want %q, %q", tc.query, q.words, q.phrase, q.none(), tc.words, tc.phrase)
		}
	}
}

// An entry scores one for each word of the query it holds, as part of a
// longer word too and whatever the case, and one more for holding the
// query whole; no word is no answer, however much the text holds.
func TestRecallQueryScore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		query, text string
		want        int
	}{
		{"worktree cleanup", "Worktree cleanup kept a half-removed tree", 3},
		{"worktree cleanup", "Cleanup of the worktree", 2},
		{"worktree cleanup", "the WORKTREES", 1},
		{"worktree cleanup", "nothing here", 0},
		{"worktree remove cleanup timeout", "worktree cleanup kept a half-removed tree", 3},
		{"frozen", "deploys are frozen until Friday", 2},
		{"ci pipeline", "the pipeline", 1},
		{"ci pipeline", "ci pipeline", 2}, // "ci" is no word of the query, but is in its phrase
		{"", "anything", 0},
		{"  ", "anything", 0},
	} {
		if got := newRecallQuery(tc.query).score(tc.text); got != tc.want {
			t.Errorf("score of %q for %q = %d, want %d", tc.text, tc.query, got, tc.want)
		}
	}
}
