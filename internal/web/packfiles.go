package web

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"yardr.dev/yardr/internal/store"
)

// The full text behind a summary card (design yardr-y6gp 3.3), one file to a
// page. A summary card names the pack by its digest and each file by its
// hash, and keeps the texts in the ask. The bead's page links each file's
// sentence to its page here, so the operator can read everything the
// approval installs without a terminal.

// PackFilePage is /beads/<id>/asks/<ask>/files/<n>: one file of a summary
// card's full text, whole, headed by its hashes as the card gives them, with
// links to the file before it, the file after it and the card. It holds no
// copy. It renders the stored ask, as the bead's page renders the bead, and
// ends when the ask does.
type PackFilePage struct {
	Bead string `json:"bead"`
	Ask  uint64 `json:"ask"`
	// Pack is the pack's name and Digest its digest, as `yardr pack check`
	// prints it. The approval is bound to it.
	Pack   string `json:"pack"`
	Digest string `json:"digest"`
	// N is this file's place among the Of files of the full text, counted
	// from 1: the files the import writes in the order the card lists
	// them, then each source it adds with its declaration and command files.
	N  int `json:"n"`
	Of int `json:"of"`
	// Kind is file, declaration or command file. Action is what the import
	// does with a file (create or change) and is absent on the others.
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Action string `json:"action,omitempty"`
	// OldSHA256 is the hash of the file that stands, absent for a create;
	// NewSHA256 the hash of the text below, which the card gives for a file
	// and a command file and not for a declaration.
	OldSHA256 string `json:"old_sha256,omitempty"`
	NewSHA256 string `json:"new_sha256,omitempty"`
	// Bytes, Insertions and Deletions are the figures the card's line gives.
	Bytes      int `json:"bytes"`
	Insertions int `json:"insertions,omitempty"`
	Deletions  int `json:"deletions,omitempty"`
	// Diff is a changed file's unified diff, old against new, and Text the
	// whole text as the pack would install it.
	Diff string `json:"diff,omitempty"`
	Text string `json:"text"`
	// Card is the bead's page, where the card stands; Prev and Next the
	// pages on either side, absent at the ends.
	Card string `json:"card"`
	Prev string `json:"prev,omitempty"`
	Next string `json:"next,omitempty"`
}

// What names the file in a few words for its label: a file by what the
// import does with it, the rest by what they are.
func (p PackFilePage) What() string {
	switch p.Kind {
	case "declaration":
		return "declaration of a source it adds"
	case "command file":
		return "command file of a source it adds"
	}
	return "file to " + p.Action
}

// packText is one file of a summary card's full text as the ask carries it.
// diff is the diff the card counts: a created file has one too, and its page
// shows none, since a created file is read as its text.
type packText struct {
	kind, name, action string
	oldSHA, newSHA     string
	diff, text         string
}

// packSummary says whether a is a summary card: the shape that keeps its
// texts behind the card, to be read file by file.
func packSummary(a store.BeadAsk) bool {
	return a.Run != nil && a.Run.Pack != nil && a.Run.Pack.Summary
}

// packFilePath is the page of the n'th file of the full text behind an ask,
// counted from 1.
func packFilePath(bead string, ask uint64, n int) string {
	return "/beads/" + url.PathEscape(bead) + "/asks/" + strconv.FormatUint(ask, 10) + "/files/" + strconv.Itoa(n)
}

// packTexts are the files of a summary card's full text, in the order of
// their pages. They are what the card's closing line counts. The files the
// import writes come first, in the order run.pack.files lists them, so file
// i of that list is page i+1. Each source the pack adds follows, with its
// declaration and then its command files. A summary card replaces no source,
// so none has an old side to show.
func packTexts(run *store.RunAsked) ([]packText, error) {
	files := map[string]store.AskPlanObject{}
	if run.Pack.Plan != nil {
		for _, o := range run.Pack.Plan.Objects {
			if o.Kind == "file" {
				files[o.Name] = o
			}
		}
	}
	out := make([]packText, 0, len(run.Pack.Files))
	for _, f := range run.Pack.Files {
		o, ok := files[f.Name]
		if !ok {
			return nil, fmt.Errorf("the card lists the file %s and its plan holds none of that name", f.Name)
		}
		out = append(out, packText{kind: "file", name: o.Name, action: o.Action, oldSHA: o.FileOldSHA, newSHA: o.FileNewSHA, diff: o.Diff, text: o.Text})
	}
	for _, c := range run.Carried {
		out = append(out, packText{kind: "declaration", name: c.Name, text: c.Text})
		for _, f := range c.Files {
			out = append(out, packText{kind: "command file", name: f.Program, newSHA: f.SHA256, text: f.Text})
		}
	}
	return out, nil
}

// packFileLinks are the links a summary card carries on its bead's page.
// pages maps the index of a sentence of the card to the page of the file it
// names, as run.pack.files says; the sentences themselves are never read for
// it. first is the first page, for the files folded into a count and for
// the sources, which have no sentence of their own. A card that is no
// summary has none.
func packFileLinks(bead string, a store.BeadAsk) (pages map[int]string, first string) {
	if !packSummary(a) {
		return nil, ""
	}
	for i, f := range a.Run.Pack.Files {
		if f.Line < 0 {
			continue
		}
		if pages == nil {
			pages = map[int]string{}
		}
		pages[f.Line] = packFilePath(bead, a.Ask, i+1)
	}
	if len(a.Run.Pack.Files) > 0 || len(a.Run.Carried) > 0 {
		first = packFilePath(bead, a.Ask, 1)
	}
	return pages, first
}

func (s *Server) handlePackFile(w http.ResponseWriter, r *http.Request) {
	d, err := s.packFilePage(r.Context(), r.PathValue("id"), r.PathValue("ask"), r.PathValue("n"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.respond(w, r, Page{Template: "packfile", Title: fmt.Sprintf("%s · file %d of %d", d.Bead, d.N, d.Of), Menu: "beads", Data: d})
}

// packFilePage reads one file of the full text behind a summary card. The
// texts are the operator's configuration, so the page opens only for the
// card's own reader: a view that acts as an operator, on an ask that is open
// and put to that user, as the bead's page lists it. Any other request gets
// the words of an ask that is not there: another viewer, a user who is no
// operator now, a closed ask, a whole card, a file past the last. So the
// page never tells a viewer who may not read it that a card exists.
func (s *Server) packFilePage(ctx context.Context, id, askWord, nWord string) (*PackFilePage, error) {
	gone := fmt.Errorf("ask %s of bead %s has no file for you to read: %w", askWord, id, store.ErrNotFound)
	seq, err := strconv.ParseUint(askWord, 10, 64)
	n, nerr := strconv.Atoi(nWord)
	// One address for a page: 03 and +3 are not 3.
	if err != nil || nerr != nil || strconv.Itoa(n) != nWord || !s.isOperator(ctx) {
		return nil, gone
	}
	// A bead is addressed by its handle or a prefix of it; the page's
	// links name it by its handle, as the bead's page does.
	b, err := s.st.GetBead(ctx, id)
	if err != nil {
		return nil, err
	}
	asks, err := s.openBeadAsks(ctx, b.ID)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(asks, func(a store.BeadAsk) bool { return a.Ask == seq })
	if i < 0 || !packSummary(asks[i]) {
		return nil, gone
	}
	run := asks[i].Run
	texts, err := packTexts(run)
	if err != nil {
		return nil, fmt.Errorf("ask %d of bead %s: %w", seq, b.ID, err)
	}
	if n < 1 || n > len(texts) {
		return nil, gone
	}
	t := texts[n-1]
	insertions, deletions := store.Counts(t.diff)
	if t.action != "change" {
		t.diff = ""
	}
	d := &PackFilePage{
		Bead: b.ID, Ask: seq, Pack: run.Pack.Name, Digest: run.Pack.Digest,
		N: n, Of: len(texts), Kind: t.kind, Name: t.name, Action: t.action,
		OldSHA256: t.oldSHA, NewSHA256: t.newSHA, Bytes: len(t.text),
		Insertions: insertions, Deletions: deletions,
		// The terminal reads these texts as letters (store.NoteLetters),
		// and so does the page, so the two readings show the same text.
		Diff: store.NoteLetters(t.diff), Text: store.NoteLetters(t.text),
		Card: "/beads/" + url.PathEscape(b.ID),
	}
	if n > 1 {
		d.Prev = packFilePath(b.ID, seq, n-1)
	}
	if n < len(texts) {
		d.Next = packFilePath(b.ID, seq, n+1)
	}
	return d, nil
}
