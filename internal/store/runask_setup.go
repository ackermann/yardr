// The setup ask (design yardr-7d4e, B2): `yardr bead ask <id> "<q>" --setup
// <pack> --crew|--group <grantee>` carries the whole setup of a pack's
// sources as one decision: the apply, the account and the grants. Its
// options are the pack's presets, picked once with --recommend as on any
// option ask; its account is typed unseen beside the pick. This is
// everything short of carrying it out: asking, the card, and the approval
// with its stale holding. The approval records approved-and-not-carried-out
// with the carrying record beside it; the server carries it out (B3).
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// AskPresetGrant is one source's calls of a preset: the source the pack
// declares, and what of it the preset grants, in the words yardr source
// grant takes (GrantRead for every read call, calls by name).
type AskPresetGrant struct {
	Source string   `json:"source"`
	Calls  []string `json:"calls"`
}

// AskPreset is one preset of a setup ask's pack: the pack author's named
// grant suggestion, with what of each source it grants. The author's about
// line is no part of it: pack show prints it for the asker, and the card
// renders each preset's calls from the declarations.
type AskPreset struct {
	Name   string           `json:"name"`
	Grants []AskPresetGrant `json:"grants"`
}

// AskCandidate is one source already in the yard whose account file the
// setup card offers to reuse: the source by name, the file, and the shared
// input values as the file holds them. Only yard-level source files are
// ever candidates, never a user's own, and only the shared mint inputs are
// ever shown, never a static variable.
type AskCandidate struct {
	Source string            `json:"source"`
	File   string            `json:"file"`
	Values map[string]string `json:"values"`
}

// AskSetup is the setup a setup ask proposes: the pack, who the grants are
// for, and the yard's card for it. Its writer sends Pack and To alone; the
// rest is the yard's, written in the write that stores the ask (askedSetup)
// whatever the writer sent for it: the account names, the candidates to
// take instead of typing, the server's bytes of the pack with its presets,
// the sentences, and what the yard held of each thing they name.
type AskSetup struct {
	Pack  string `json:"pack"`
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
	// To are the grantees, the groups before the crew members and each in
	// the order of their names.
	To []Grantee `json:"to"`
	// Account are the names to type unseen beside the preset pick: the
	// mint's inputs where the pack's credential is minted, else every
	// credential name. AccountFile is the file the value goes to. Where
	// Candidates stand, the answer names one of them or types another.
	Account     []string `json:"account,omitempty"`
	AccountFile string   `json:"account_file,omitempty"`
	// Given is a drift card whose standing file holds every account name:
	// the answer may keep it with a bare preset pick, type another, or
	// take a candidate. Missing are the account names the standing file
	// does not set where the updated declaration newly needs them: the
	// card names them and the answer types them, or takes a candidate.
	// Both are empty on a first setup, which has no standing file.
	Given   bool     `json:"given,omitempty"`
	Missing []string `json:"missing,omitempty"`
	// Candidates are the yard sources whose account the card offers, each
	// by source name with its file and the shared values it holds, in the
	// order of their names. Empty where none matches.
	Candidates []AskCandidate `json:"candidates,omitempty"`
	// Carried are the pack's sources as the server read them from its own
	// binary, with what the yard's copies of a source they would replace
	// hold; Presets are the pack's presets with their grants.
	Carried []AskSource `json:"carried,omitempty"`
	Presets []AskPreset `json:"presets,omitempty"`
	// Does are the yard's sentences, and Back the commands that take the
	// setup back. Seen holds what the yard held of each thing the sentences
	// name when it wrote them; an approval is held to it.
	Does   []string          `json:"does,omitempty"`
	Seen   map[string]string `json:"seen,omitempty"`
	Back   string            `json:"back,omitempty"`
	BackBy string            `json:"back_by,omitempty"`
}

// TakenBack is the card's line for what takes the setup back, with who may
// run it.
func (s AskSetup) TakenBack() string { return or(s.BackBy, "anyone") + " can take it back: " + s.Back }

// Grantees are who the setup's grants are for, in words.
func (s AskSetup) Grantees() string { return granteesWord(s.To) }

// granteesWord is who the grants are for, in words: each crew member and
// each group by name.
func granteesWord(to []Grantee) string {
	words := make([]string, len(to))
	for i, t := range to {
		if t.Crew != "" {
			words[i] = "crew " + t.Crew
			continue
		}
		words[i] = "group " + t.Group
	}
	return andList(words)
}

// Summary is what approving the setup does, in one line: the apply, the
// account and the grants.
func (s AskSetup) Summary() string {
	return fmt.Sprintf("yardr pack apply %s, the account %s, and the preset's grants to %s",
		s.Pack, strings.Join(s.Account, " and "), s.Grantees())
}

// AskSetupApproved is what ApproveSetup did: the ask and who asked it, the
// pack with the preset picked, the names given, the candidate taken or the
// standing account kept, and what the approval recorded (CarryPending until
// the server carries it out). Exactly one of Given, Account and Keep is
// set: the typed names, the picked candidate's source, or a kept standing
// account on a drift card.
type AskSetupApproved struct {
	Bead    string   `json:"bead"`
	Ask     uint64   `json:"ask"`
	Asker   string   `json:"asker"`
	Pack    string   `json:"pack"`
	Preset  string   `json:"preset"`
	Given   []string `json:"given,omitempty"`
	Account string   `json:"account,omitempty"`
	Keep    bool     `json:"keep,omitempty"`
	Result  string   `json:"result"`
}

// SetupAsked is the setup a setup ask proposes, as a client shows it: the
// pack, the account to type beside the preset pick or the candidates to
// take instead, the yard's sentences for what approving it does, and the
// presets to pick from. All of it is the yard's, and none of it the
// asker's.
type SetupAsked struct {
	Pack string `json:"pack"`
	// Label is the build the pack is embedded in, as the card shows it.
	Label string `json:"label,omitempty"`
	// Account are the names to type unseen beside the preset pick, and
	// AccountFile the file the value goes to, on the yard's machine.
	// Where Candidates stand, the answer names one of them or types
	// another. Given is a drift card whose standing file holds every
	// name, kept with a bare pick; Missing are the names the standing
	// file does not set, which the answer types.
	Account     []string `json:"account"`
	AccountFile string   `json:"account_file"`
	Given       bool     `json:"given,omitempty"`
	Missing     []string `json:"missing,omitempty"`
	// Candidates are the yard sources whose account the card offers, each
	// by source name with its file and the shared values it holds.
	Candidates []AskCandidate `json:"candidates,omitempty"`
	// Does are the yard's sentences for what approving changes and what
	// is there now, and Back the commands that take the setup back.
	Does []string `json:"does"`
	Back string   `json:"back"`
	// BackBy is who may run Back, in words that stand before "can take it
	// back"; absent where that is anyone.
	BackBy string `json:"back_by,omitempty"`
	// Carried is what the pack brings, for a client to show the texts
	// behind the card; Presets are the pack's presets with their grants,
	// in the order the ask's options count them.
	Carried []AskSource `json:"carried,omitempty"`
	Presets []AskPreset `json:"presets"`
	// To are who the grants are for: each crew member and each group by
	// name, the groups before the crew members.
	To []Grantee `json:"to"`
}

// setup is the setup of the ask a as a client shows it, nil for an ask
// that proposes none.
func (a Ask) setup() *SetupAsked {
	if a.Setup == nil {
		return nil
	}
	return &SetupAsked{Pack: a.Setup.Pack, Label: a.Setup.Label, Account: a.Setup.Account,
		AccountFile: a.Setup.AccountFile, Given: a.Setup.Given, Missing: a.Setup.Missing,
		Candidates: a.Setup.Candidates, Does: a.Setup.Does, Back: a.Setup.Back,
		BackBy: a.Setup.BackBy, Carried: a.Setup.Carried, Presets: a.Setup.Presets, To: a.Setup.To}
}

// askedSetup checks the setup an ask of the user to proposes, asked by
// actor, and answers it as the ask stores it with its options: one per
// preset as the yard renders them, with YouDecide last. Who may ask, who
// it is put to and who may approve are the run list's, unchanged: a crew
// member or a user and not a bead's agent, to the operator alone. The
// store throws away whatever carried bytes, sentences or fingerprints the
// writer sent, as the pack row does: the server's reading supplies the
// pack, and the store parses every carried text itself.
func (w *wtx) askedSetup(ctx context.Context, ask Ask, to, actor string) (*AskSetup, []AskOption, error) {
	pack := strings.TrimSpace(ask.Setup.Pack)
	if pack == "" {
		return nil, nil, fmt.Errorf("--setup names the pack to set up: --setup <pack>")
	}
	const whose = "a setup is proposed by a crew member or a user of the yard"
	sess, err := paneSession(w.Tx, callerOf(ctx))
	if err != nil {
		return nil, nil, err
	}
	if sess != nil && !sess.Manual() {
		return nil, nil, fmt.Errorf("%s; this is the session %s of a bead's agent: say what is needed in a note on the bead (yardr bead note)", whose, sess.ID)
	}
	if err := startingRefused(w.Tx, sessionOf(ctx)); err != nil {
		return nil, nil, err
	}
	if w.Bucket(bCrew).Get([]byte(actor)) == nil && w.Bucket(bUsers).Get([]byte(actor)) == nil {
		return nil, nil, fmt.Errorf("%s; this is %s: say what is needed in a note on the bead (yardr bead note)", whose, or(actor, "nobody"))
	}
	// The setup's own check as the asker, as a run ask runs its command's:
	// nothing says it needs no approval better than the yard allowing it.
	if err := w.operatorOnly(ctx, actor, "set up a pack's sources", Askable); err != nil {
		if class, ok := IsOperatorOnly(err); !ok {
			return nil, nil, err
		} else if class == Alone {
			return nil, nil, aloneRefusal(err)
		}
	} else {
		return nil, nil, fmt.Errorf("setting up pack %s needs no approval: the operator applies, keys and grants it in a terminal", pack)
	}
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case operator == "":
		return nil, nil, fmt.Errorf("a setup is the operator's to approve, and this yard has none: yardr settings set operator <name>")
	case to != operator:
		return nil, nil, fmt.Errorf("a setup is the operator's to approve (%s): the yard carries it out as them, so %s cannot be asked to", operator, to)
	}
	card, err := w.setupCard(pack, ask.Setup.To)
	if err != nil {
		return nil, nil, err
	}
	// --recommend counts the card's preset options as pack show prints
	// them, with --why saying why, as on any option ask.
	switch {
	case ask.Recommend != 0 && strings.TrimSpace(ask.Why) == "":
		return nil, nil, fmt.Errorf("--recommend %d needs --why: why this option, in a line", ask.Recommend)
	case ask.Recommend == 0 && strings.TrimSpace(ask.Why) != "":
		return nil, nil, fmt.Errorf("--why needs --recommend: which option it is for")
	case ask.Recommend < 0 || ask.Recommend > len(card.Presets):
		return nil, nil, fmt.Errorf("pack %s has presets 1..%d (yardr pack show %s prints them numbered): --recommend %d names none", pack, len(card.Presets), pack, ask.Recommend)
	}
	decls := map[string]*source.Declaration{}
	for i := range card.Carried {
		d, err := source.Parse("pack source "+card.Carried[i].Name, []byte(card.Carried[i].Text))
		if err != nil {
			return nil, nil, fmt.Errorf("%s", OneLine(err.Error()))
		}
		decls[d.Name] = d
	}
	options, err := w.setupOptions(card.Presets, card.Carried, decls, card.To, ask.Recommend)
	if err != nil {
		return nil, nil, err
	}
	return card, options, nil
}

// normSetupTo is the grantees in the yard's own order, once each: the
// groups before the crew members and each in the order of their names, as
// a grant command stores them.
func normSetupTo(to []Grantee) []Grantee {
	var groups, crews []string
	for _, t := range to {
		switch {
		case t.Crew != "":
			crews = append(crews, t.Crew)
		case t.Group != "":
			groups = append(groups, t.Group)
		}
	}
	sort.Strings(groups)
	sort.Strings(crews)
	var out []Grantee
	for _, g := range slices.Compact(groups) {
		out = append(out, Grantee{Group: g})
	}
	for _, c := range slices.Compact(crews) {
		out = append(out, Grantee{Crew: c})
	}
	return out
}

// setupCard is the card of setting up pack for the grantees to: the
// server's own pack in the yard's sentences, the account line, and the
// grants the presets give to whom. Asking refuses what the row refuses;
// an approval reads it again and setupStale says what moved.
func (w *wtx) setupCard(pack string, to []Grantee) (*AskSetup, error) {
	read := w.packReaderOf()
	if read == nil {
		return nil, fmt.Errorf("pack %s: no server has registered its reading of the packs yardr embeds on this store, so no card can be written from the binary that runs the yard: a setup is proposed from a yard its server runs", pack)
	}
	reading, err := read(context.Background(), pack, w.store)
	if err != nil {
		return nil, err
	}
	if reading == nil || reading.Name != pack {
		return nil, fmt.Errorf("pack %s: the server's reading named another pack: nothing was stored", pack)
	}
	if reading.Kind != "source" {
		kind := reading.Kind
		if kind == "" {
			kind = "of no kind"
		} else {
			kind = "kind " + kind
		}
		return nil, fmt.Errorf("--setup sets up a pack of kind source, and pack %s is %s: it is the operator's to apply in a terminal (yardr pack check %s shows its plan there)", pack, kind, pack)
	}
	to = normSetupTo(to)
	if len(to) == 0 {
		return nil, fmt.Errorf("--setup takes --crew <name> or --group <name>, at least one: a grant names who it is for")
	}
	declTotal := 0
	carried := make([]AskSource, 0, len(reading.Sources))
	decls := map[string]*source.Declaration{}
	for _, s := range reading.Sources {
		d, norm, err := validatePackSource(s, pack)
		if err != nil {
			return nil, err
		}
		declTotal += len(s.Text)
		decls[d.Name] = d
		carried = append(carried, norm)
	}
	if declTotal > MaxRunPackDeclarations {
		return nil, fmt.Errorf("pack %s: its declarations run to %d bytes together: a card holds at most %d, so this is the operator's to set up in a terminal (yardr pack check %s shows it there)", pack, declTotal, MaxRunPackDeclarations, pack)
	}
	// What the yard's copies of a source the pack would replace hold, read
	// by the store itself through its home, with each file's diff.
	payload := 0
	for i := range carried {
		pay, err := w.packReplacement(&carried[i], decls[carried[i].Name], pack)
		if err != nil {
			return nil, err
		}
		payload += pay
		for _, f := range carried[i].Files {
			payload += len(f.Text)
		}
	}
	if payload > MaxRunPackFiles {
		return nil, fmt.Errorf("pack %s: its carried file texts and diffs run to %d bytes: a card holds at most %d together, so this is the operator's to set up in a terminal (yardr pack check %s shows it there)", pack, payload, MaxRunPackFiles, pack)
	}
	presets, err := setupPresets(pack, reading.Presets, decls)
	if err != nil {
		return nil, err
	}
	account, file, given, missing, err := w.setupAccount(pack, carried, decls)
	if err != nil {
		return nil, err
	}
	candidates, err := w.setupCandidates(carried, decls, account)
	if err != nil {
		return nil, err
	}
	seenMap := map[string]string{
		"pack " + pack:              reading.Label,
		"pack " + pack + " bytes":   packBytesSeen(&PackReading{Sources: carried}),
		"pack " + pack + " presets": setupPresetsSeen(presets),
	}
	for _, c := range candidates {
		seenMap["candidate "+c.Source] = candidateSeen(c)
	}
	for i := range carried {
		key := "source " + carried[i].Name
		if src, _, err := sourceIn(w.Tx, carried[i].Name); err == nil {
			fingerprint, err := w.sourceSeen(src)
			if err != nil {
				return nil, err
			}
			seenMap[key] = fingerprint
		} else if errors.Is(err, ErrNotFound) {
			if len(carried[i].Inventory) == 0 {
				seenMap[key] = ""
			} else {
				seenMap[key] = seen("no source", carried[i].Inventory)
			}
		} else {
			return nil, err
		}
		// Owed-ness: whether the account is still owed, held apart from
		// the source so an account given meanwhile refuses the approval.
		// With no home there is no file to have given it in.
		if decls[carried[i].Name].Credential != nil && w.store != nil && w.store.home != "" {
			owed, err := w.setupOwed(decls[carried[i].Name])
			if err != nil {
				return nil, err
			}
			seenMap["account "+carried[i].Name] = owed
		}
	}
	for _, t := range to {
		if _, fingerprint, err := w.granteeSeen(t); err != nil {
			return nil, err
		} else {
			seenMap[t.seenKey()] = fingerprint
		}
	}
	does, err := w.setupDoes(pack, reading, carried, decls, account, file, given, missing, candidates, to)
	if err != nil {
		return nil, err
	}
	card := &AskSetup{Pack: pack, Kind: reading.Kind, Label: reading.Label, To: to,
		Account: account, AccountFile: file, Given: given, Missing: missing,
		Candidates: candidates, Carried: carried, Presets: presets,
		Does: does, Seen: seenMap, Back: setupBack(carried, to), BackBy: "the operator"}
	if len(does)+len(presets) > MaxRunSourceCardLines {
		return nil, fmt.Errorf("pack %s: its setup card would run to %d lines: a card is read whole, so set it up step by step instead (yardr pack check %s shows its plan there): a pack apply card, an account card per source, and a grant card per source and kind", pack, len(does)+len(presets), pack)
	}
	return card, nil
}

// setupPresets are the pack's presets as the setup card keeps them: the
// server's, checked against the declarations they grant. A pack with no
// presets is set up step by step: its grants have no names to pick.
func setupPresets(pack string, presets []AskPreset, decls map[string]*source.Declaration) ([]AskPreset, error) {
	if len(presets) == 0 {
		return nil, fmt.Errorf("pack %s carries no presets (yardr pack show %s prints a pack's): set it up step by step instead: a pack apply card, an account card per source, and a grant card per source and kind", pack, pack)
	}
	for _, p := range presets {
		if len(p.Grants) == 0 {
			return nil, fmt.Errorf("pack %s: its preset %s grants nothing: the pack is the operator's to check in a terminal (yardr pack check %s)", pack, p.Name, pack)
		}
		for _, g := range p.Grants {
			d, ok := decls[g.Source]
			if !ok {
				return nil, fmt.Errorf("pack %s: its preset %s names the source %s, which the pack does not declare: the pack is the operator's to check in a terminal (yardr pack check %s)", pack, p.Name, g.Source, pack)
			}
			if len(g.Calls) == 0 {
				return nil, fmt.Errorf("pack %s: its preset %s grants nothing of the source %s: the pack is the operator's to check in a terminal (yardr pack check %s)", pack, p.Name, g.Source, pack)
			}
			for _, what := range g.Calls {
				if err := grantable(d, what); err != nil {
					return nil, fmt.Errorf("pack %s: its preset %s: %v: the pack is the operator's to check in a terminal (yardr pack check %s)", pack, p.Name, err, pack)
				}
			}
		}
	}
	return presets, nil
}

// setupPresetsSeen fingerprints a setup card's presets: each name with the
// calls it grants per source. The author's about lines are no part of the
// card, as a declaration's about is no part of the apply card, so they are
// no part of it either.
func setupPresetsSeen(presets []AskPreset) string {
	var parts []any
	for _, p := range presets {
		parts = append(parts, p.Name)
		for _, g := range p.Grants {
			parts = append(parts, g.Source, g.Calls)
		}
	}
	return seen(parts...)
}

// setupAccount is the setup card's account: the names to type unseen and
// the file the value goes to. One card carries one account choice: every
// source that names a credential must name the same one, by the shared
// account rule (source.SharesAccount); the file is the first such source's,
// in the pack's declaration order. A pack whose sources name different
// credentials is set up step by step, an account card per source. On a drift
// card, where the first source already stands in the yard, it also says
// whether that file holds every name (given) or which it does not (missing).
func (w *wtx) setupAccount(pack string, carried []AskSource, decls map[string]*source.Declaration) ([]string, string, bool, []string, error) {
	var have []string
	settings := map[string]string{}
	byName := map[string]AskSource{}
	for i := range carried {
		d := decls[carried[i].Name]
		byName[carried[i].Name] = carried[i]
		if d.Credential == nil {
			continue
		}
		have = append(have, carried[i].Name)
		if src, _, err := sourceIn(w.Tx, carried[i].Name); err == nil {
			settings[carried[i].Name] = src.MintCommand
		} else if !errors.Is(err, ErrNotFound) {
			return nil, "", false, nil, err
		}
	}
	if len(have) == 0 {
		return nil, "", false, nil, fmt.Errorf("pack %s names no credential: no account is owed, so set it up step by step instead: a pack apply card, and a grant card per source and kind", pack)
	}
	for _, name := range have[1:] {
		first, second := byName[have[0]], byName[name]
		if !source.SharesAccount(decls[have[0]], decls[name], settings[have[0]], settings[name],
			carriedProgSHA(first), carriedProgSHA(second)) {
			return nil, "", false, nil, differentSetupCredentials(pack, have[0], name)
		}
	}
	d := decls[have[0]]
	account := d.Credential.MintOrStatic()
	file := ""
	standing := false
	if src, _, err := sourceIn(w.Tx, have[0]); err == nil {
		standing = true
		if src.EnvFile != "" {
			file = src.EnvFile
		} else if w.store != nil {
			file = source.EnvFile(w.store.home, have[0])
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, "", false, nil, err
	} else if w.store != nil {
		file = source.EnvFile(w.store.home, have[0])
	}
	if !standing || w.store == nil || w.store.home == "" || file == "" {
		return account, file, false, nil, nil
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return account, file, false, append([]string{}, account...), nil
	}
	var missing []string
	for _, n := range account {
		if source.Lookup(f.Env, n) == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) == 0 {
		return account, file, true, nil, nil
	}
	return account, file, false, missing, nil
}

func differentSetupCredentials(pack, first, second string) error {
	return fmt.Errorf("pack %s carries sources under different credentials (the source %s and the source %s): one card carries one account choice, so set it up step by step instead: a pack apply card, an account card per source, and a grant card per source and kind", pack, first, second)
}

// carriedProgSHA resolves a pack source's relative mint program to the
// sha256 of its carried bytes, false where the pack carries no such file.
func carriedProgSHA(carried AskSource) func(string) (string, bool) {
	return func(prog string) (string, bool) {
		for _, f := range carried.Files {
			if f.Program == prog {
				return f.SHA256, true
			}
		}
		return "", false
	}
}

// setupOwed is what a setup card keeps of a source's account: whether it
// is still owed. The file is the yard's env_file for the source, or its
// per-source default where the yard holds none; a file that cannot be
// read, or that does not set every needed name, owes.
func (w *wtx) setupOwed(d *source.Declaration) (string, error) {
	file := source.EnvFile(w.store.home, d.Name)
	if src, _, err := sourceIn(w.Tx, d.Name); err == nil {
		if src.EnvFile != "" {
			file = src.EnvFile
		}
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	f, err := config.ReadEnvFile(file)
	if err != nil {
		return "owed", nil
	}
	for _, n := range d.Credential.MintOrStatic() {
		if source.Lookup(f.Env, n) == "" {
			return "owed", nil
		}
	}
	return "given", nil
}

// setupCandidates are the yard sources whose account the setup card offers
// to reuse: every source already in the yard, beside the pack's own, that
// names the same credential and mint as the pack (source.SharesAccount)
// and whose yard-level env file holds every shared input name. A
// credential a user gave for themselves lives in the user's file, never a
// source's, so it is never listed; a source file that resolves to a user's
// or crew member's file, or under the users or crew directories, is not
// listed either, even where a hand-set env_file points the two at one
// file. In the order of their names. With no home there is no file to
// hold one.
func (w *wtx) setupCandidates(carried []AskSource, decls map[string]*source.Declaration, account []string) ([]AskCandidate, error) {
	if w.store == nil || w.store.home == "" || len(account) == 0 {
		return nil, nil
	}
	var first string
	byName := map[string]AskSource{}
	for i := range carried {
		byName[carried[i].Name] = carried[i]
		if first == "" && decls[carried[i].Name].Credential != nil {
			first = carried[i].Name
		}
	}
	if first == "" {
		return nil, nil
	}
	// A static credential never shares: one variable name across two
	// services is coincidence, not sameness.
	if !decls[first].Credential.Minted() {
		return nil, nil
	}
	firstSetting := ""
	if src, _, err := sourceIn(w.Tx, first); err == nil {
		firstSetting = src.MintCommand
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	firstSHA := carriedProgSHA(byName[first])
	owned, err := w.userOwnedFiles()
	if err != nil {
		return nil, err
	}
	var out []AskCandidate
	err = each(w.Tx, bSources, "", func(_ []byte, src *Source) error {
		if _, ok := byName[src.Name]; ok {
			return nil
		}
		d, err := src.Declaration()
		if err != nil {
			return nil
		}
		if !source.SharesAccount(decls[first], d, firstSetting, src.MintCommand,
			firstSHA, diskProgSHA(w.store.home, src.Name)) {
			return nil
		}
		file := src.EnvFile
		if file == "" {
			file = source.EnvFile(w.store.home, src.Name)
		}
		f, err := config.ReadEnvFile(file)
		if err != nil {
			return nil
		}
		values := map[string]string{}
		for _, n := range account {
			if v := source.Lookup(f.Env, n); v != "" {
				values[n] = v
			} else {
				return nil
			}
		}
		if userOwnedFile(f.Path, owned, w.store.home) {
			return nil
		}
		out = append(out, AskCandidate{Source: src.Name, File: f.Path, Values: values})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// diskProgSHA resolves a yard source's relative mint program to the sha256
// of its copy's bytes under <home>/sources/<name>/, false where the copy
// cannot be read.
func diskProgSHA(home, name string) func(string) (string, bool) {
	return func(prog string) (string, bool) {
		data, err := os.ReadFile(filepath.Join(home, "sources", name, prog))
		if err != nil {
			return "", false
		}
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:]), true
	}
}

// userOwnedFiles are the resolved files a user's or crew member's
// credential is read from: each record's env file, or the yard's default
// for the name where none is set and the yard chooses one. A path that
// resolves to one of these is never a setup candidate, even where a
// hand-set source env_file points the two at one file.
func (w *wtx) userOwnedFiles() (map[string]bool, error) {
	owned := map[string]bool{}
	home := ""
	if w.store != nil {
		home = w.store.home
	}
	if err := each(w.Tx, bUsers, "", func(_ []byte, u *User) error {
		file, ok := effectiveEnvFile(home, u)
		if !ok {
			return nil
		}
		if resolved, err := resolvedEnvFile(file); err == nil {
			owned[resolved] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	if err := each(w.Tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		file, _ := r.Config["env_file"].(string)
		if file == "" {
			var err error
			if file, err = crewEnvFile(home, r.Name); err != nil {
				return nil
			}
		}
		if resolved, err := resolvedEnvFile(file); err == nil {
			owned[resolved] = true
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return owned, nil
}

// userOwnedFile reports whether the candidate file path is a user's own:
// one of their effective files, or under the users or crew directories by
// default. Paths are compared resolved, so spellings and a link to the
// file count as the one file.
func userOwnedFile(path string, owned map[string]bool, home string) bool {
	resolved, err := resolvedEnvFile(path)
	if err != nil {
		return false
	}
	if owned[resolved] {
		return true
	}
	for _, dir := range []string{filepath.Join(home, "users"), filepath.Join(home, "crew")} {
		if d, err := resolvedEnvFile(dir); err == nil && (resolved == d || strings.HasPrefix(resolved, d+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// candidateSeen fingerprints a carded candidate: the file with the shared
// input values it holds. The approval is held to it, not to the source
// settings alone, so a candidate env file changed in place refuses it.
func candidateSeen(c AskCandidate) string {
	var names []string
	for n := range c.Values {
		names = append(names, n)
	}
	sort.Strings(names)
	var pairs []string
	for _, n := range names {
		pairs = append(pairs, n+"="+c.Values[n])
	}
	return seen(c.File, pairs)
}

// setupDoes is the setup card's sentences: the pack line, each source as
// the apply card renders it, the account line, and the grants to whom.
// The pack line is the apply row's, without "It grants no call": the card
// grants, but the pack itself makes no hook and sets no credential's
// file, the account step doing that. Each source reads as the apply card
// renders it but for what stays owed after: the card settles the account
// itself, so the credential is no part of it.
func (w *wtx) setupDoes(pack string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration, account []string, file string, given bool, missing []string, candidates []AskCandidate, to []Grantee) ([]string, error) {
	adds, replaces, files, commands, err := w.packSourceCounts(carried, decls)
	if err != nil {
		return nil, err
	}
	var clauses []string
	if adds > 0 {
		clauses = append(clauses, "adds "+countNoun(adds, "source"))
	}
	if replaces > 0 {
		clauses = append(clauses, "replaces "+countNoun(replaces, "source"))
	}
	if files > 0 {
		clauses = append(clauses, fmt.Sprintf("copies %s (%s)", countNoun(files, "command file"), countNoun(commands, "command")))
	} else {
		clauses = append(clauses, "copies no command files")
	}
	does := []string{fmt.Sprintf("pack %s (%s), of kind source, into the yard: it %s, and changes nothing else. It makes no hook and sets no credential's file.",
		pack, reading.Label, strings.Join(clauses, " and "))}
	for i := range carried {
		// The card settles the account itself, so the credential is no
		// part of what stays owed after it; which user a notifying
		// source tells still is.
		lines, err := w.packOneSourceDoes(carried[i], decls[carried[i].Name], true)
		if err != nil {
			return nil, err
		}
		does = append(does, lines...)
	}
	does = append(does, setupAccountLine(account, file, given, missing, candidates))
	does = append(does, fmt.Sprintf("grants to %s — pick one:", granteesWord(to)))
	for i := range does {
		does[i] = safeSourceCardLine(does[i])
	}
	return does, nil
}

// setupAccountLine is the setup card's account sentence: the names to type
// unseen and the file a typed value goes to, with each candidate the card
// offers by source name and the shared values its file holds. There is no
// silent reuse, not even with a single match: the answer names a candidate
// or types another. On a drift card the standing file already holds the
// account: the line reads given with its file, kept with a bare preset
// pick, typed another to change it, or a candidate's file to take. Where
// the updated declaration newly needs names the file does not set, the
// line names the missing names and the answer types them.
func setupAccountLine(account []string, file string, given bool, missing []string, candidates []AskCandidate) string {
	names := strings.Join(account, " and ")
	candidateWords := func() (string, string) {
		var each []string
		for _, c := range candidates {
			var pairs []string
			ordered := append([]string{}, account...)
			sort.Strings(ordered)
			for _, n := range ordered {
				pairs = append(pairs, n+"="+c.Values[n])
			}
			each = append(each, fmt.Sprintf("for %s (%s, in %s)", c.Source, strings.Join(pairs, ", "), c.File))
		}
		already := "already given " + andList(each)
		take := fmt.Sprintf("Take it with --account %s", candidates[0].Source)
		if len(candidates) > 1 {
			take = "Take one with --account <source>"
		}
		return already, take
	}
	if len(missing) > 0 {
		need := strings.Join(missing, " and ")
		if len(missing) == len(account) {
			if len(candidates) == 0 {
				return fmt.Sprintf("the account %s: the file %s holds none of it: type %s unseen (written to %s, named in the answer, never the value).", names, file, need, file)
			}
			already, take := candidateWords()
			return fmt.Sprintf("the account %s: the file %s holds none of it. %s. %s, or type %s unseen (written to %s, named in the answer, never the value).", names, file, already, take, need, file)
		}
		if len(candidates) == 0 {
			return fmt.Sprintf("the account %s: given, in %s, but missing %s: type %s unseen (written to %s, named in the answer, never the value).", names, file, need, need, file)
		}
		already, take := candidateWords()
		return fmt.Sprintf("the account %s: given, in %s, but missing %s. %s. %s, or type %s unseen (written to %s, named in the answer, never the value).", names, file, need, already, take, need, file)
	}
	if given {
		if len(candidates) == 0 {
			return fmt.Sprintf("the account %s: given, in %s. Type another unseen to change it, or approve keeping it with a bare preset pick (written to %s, named in the answer, never the value).", names, file, file)
		}
		already, take := candidateWords()
		return fmt.Sprintf("the account %s: given, in %s. %s. %s, type another unseen to change it, or approve keeping it with a bare preset pick (written to %s, named in the answer, never the value).", names, file, already, take, file)
	}
	if len(candidates) == 0 {
		return fmt.Sprintf("the account %s: type it unseen. It is written to %s, named in the answer, never the value.", names, file)
	}
	already, take := candidateWords()
	return fmt.Sprintf("the account %s: %s. %s, or type another unseen (written to %s, named in the answer, never the value).", names, already, take, file)
}

// setupBack is the commands that take the setup back: each source removed,
// each source's grants revoked. The preset picked is decided at approval,
// so the revoked calls stay a <what> for the operator to name.
func setupBack(carried []AskSource, to []Grantee) string {
	var flags []string
	for _, t := range to {
		if t.Crew != "" {
			flags = append(flags, "--crew", t.Crew)
		} else {
			flags = append(flags, "--group", t.Group)
		}
	}
	var parts []string
	for _, c := range carried {
		parts = append(parts, "yardr source rm "+c.Name,
			"yardr source revoke "+c.Name+" <what> "+strings.Join(flags, " "))
	}
	return strings.Join(parts, "; ")
}

// setupOptions are the setup card's options: one per preset, each
// consequence rendered by the yard from the declarations, with YouDecide
// last. Picking YouDecide grants the asker's recommended preset; with
// none recommended the card says to pick a preset instead. An operator
// who wants no grants declines the card and applies by terminal. On a
// drift card each preset is rendered against what the grantees hold:
// calls held marked held, calls the preset would add marked new.
func (w *wtx) setupOptions(presets []AskPreset, carried []AskSource, decls map[string]*source.Declaration, to []Grantee, recommend int) ([]AskOption, error) {
	out := make([]AskOption, 0, len(presets)+1)
	for _, p := range presets {
		says, err := w.setupPresetSays(p, carried, decls, to)
		if err != nil {
			return nil, err
		}
		out = append(out, AskOption{Label: p.Name, Consequence: says})
	}
	decide := "no preset is recommended, so pick one of the presets above"
	if recommend >= 1 && recommend <= len(presets) {
		decide = fmt.Sprintf("the recommended preset, %s, is granted", presets[recommend-1].Name)
	}
	return append(out, AskOption{Label: YouDecide, Consequence: decide}), nil
}

// setupPresetSays renders what picking the preset grants, from the
// declarations: each call with its method and path, read expanded to
// today's reads with the later-reads line, an update marked as changing
// something there. It reads as the grant card reads, for each source the
// preset grants of.
func (w *wtx) setupPresetSays(p AskPreset, carried []AskSource, decls map[string]*source.Declaration, to []Grantee) (string, error) {
	var clauses []string
	for i := range carried {
		d := decls[carried[i].Name]
		var calls []string
		for _, g := range p.Grants {
			if g.Source == carried[i].Name {
				calls = g.Calls
			}
		}
		if len(calls) == 0 {
			continue
		}
		says, err := w.setupPresetSourceSays(p.Name, d, calls, to)
		if err != nil {
			return "", err
		}
		clauses = append(clauses, says)
	}
	return strings.Join(clauses, " "), nil
}

// setupPresetSourceSays renders what the preset grants of one source.
func (w *wtx) setupPresetSourceSays(preset string, d *source.Declaration, calls []string, to []Grantee) (string, error) {
	of := fmt.Sprintf("of the source %s (%s)", d.Name, sourceHost(d))
	var rest []string
	hasRead := false
	for _, what := range calls {
		if what == GrantRead {
			hasRead = true
		} else {
			rest = append(rest, what)
		}
	}
	// A source not yet in the yard reads as the first setup does: what
	// the preset grants, with no holdings to mark it against. A source
	// already in the yard is drift: each call marked held where the
	// grantees hold it and new where the preset would add it.
	old, err := declared(w.Tx, d.Name)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return "", err
	}
	if old == nil {
		var says []string
		if hasRead {
			var reads []string
			for _, c := range d.Calls {
				if c.Kind == source.Read {
					reads = append(reads, callSays(c))
				}
			}
			today := "today that is " + strings.Join(reads, ", ")
			if len(reads) == 0 {
				today = "today the source has none"
			}
			says = append(says, fmt.Sprintf("may make every read call %s: %s, and it covers any read call the operator later gives the source.", of, today))
		}
		for _, what := range rest {
			c, ok := d.Call(what)
			if !ok {
				continue // checked at asking; the approval renders what stands
			}
			if c.Kind == source.Update {
				says = append(says, fmt.Sprintf("may make the call %s %s, which changes something there.", callSays(*c), of))
			} else {
				says = append(says, fmt.Sprintf("may make the call %s %s, which the declaration says reads there and changes nothing.", callSays(*c), of))
			}
		}
		return strings.Join(says, " "), nil
	}
	held := map[Grantee]SourceGrant{}
	for _, t := range to {
		var g SourceGrant
		if _, err := get(w.Tx, bSourceGrants, grantKey(d.Name, t, ""), &g); err != nil {
			return "", err
		}
		held[t] = g
	}
	oldNames := map[string]bool{}
	for _, c := range old.Calls {
		oldNames[c.Name] = true
	}
	var says []string
	if hasRead {
		var reads, added []string
		for _, c := range d.Calls {
			if c.Kind != source.Read {
				continue
			}
			word := callSays(c)
			if !oldNames[c.Name] {
				word += " (new)"
				added = append(added, c.Name)
			}
			reads = append(reads, word)
		}
		today := "today that is " + strings.Join(reads, ", ")
		if len(reads) == 0 {
			today = "today the source has none"
		}
		line := fmt.Sprintf("may make every read call %s: %s, and it covers any read call the operator later gives the source.", of, today)
		line = strings.TrimSuffix(line, ".") + setupHeldMark(to, held, func(g SourceGrant) bool {
			return slices.Contains(g.Calls, GrantRead)
		}) + "."
		says = append(says, line)
		// A standing read grant covers a read call the update adds, and
		// the card says so beside that call; only an update call the
		// update adds waits on a preset that names it.
		if len(added) > 0 {
			var covered, fresh []string
			for _, t := range to {
				if slices.Contains(held[t].Calls, GrantRead) {
					covered = append(covered, granteeShort(t))
				} else {
					fresh = append(fresh, granteeShort(t))
				}
			}
			noun := "call " + strings.Join(added, ", ")
			if len(added) > 1 {
				noun = "calls " + strings.Join(added, ", ")
			}
			switch {
			case len(covered) == len(to):
				says = append(says, fmt.Sprintf("New read %s is covered by the standing read grant, no grant needed.", noun))
			case len(covered) > 0:
				says = append(says, fmt.Sprintf("New read %s is covered for %s by the standing read grant, no grant needed there; new for %s.", noun, andList(covered), andList(fresh)))
			}
		}
	}
	for _, what := range rest {
		c, ok := d.Call(what)
		if !ok {
			continue // checked at asking; the approval renders what stands
		}
		var line string
		if c.Kind == source.Update {
			line = fmt.Sprintf("may make the call %s %s, which changes something there.", callSays(*c), of)
		} else {
			line = fmt.Sprintf("may make the call %s %s, which the declaration says reads there and changes nothing.", callSays(*c), of)
		}
		line = strings.TrimSuffix(line, ".") + setupHeldMark(to, held, func(g SourceGrant) bool {
			return g.Covers(c, "")
		})
		if !oldNames[c.Name] {
			var waiting []Grantee
			for _, t := range to {
				if !held[t].Covers(c, "") {
					waiting = append(waiting, t)
				}
			}
			if len(waiting) > 0 && c.Kind == source.Update {
				if len(waiting) == len(to) {
					line += fmt.Sprintf(", waiting on the preset %s", preset)
				} else {
					var who []string
					for _, t := range waiting {
						who = append(who, granteeShort(t))
					}
					line += fmt.Sprintf(", waiting on the preset %s for %s", preset, andList(who))
				}
			}
		}
		says = append(says, line+".")
	}
	return strings.Join(says, " "), nil
}

// setupHeldMark is what a drift preset's call carries for who holds it:
// held where every grantee does, new where none does, and the split where
// some do. It stands where the sentence's full stop was, which the caller
// puts back.
func setupHeldMark(to []Grantee, held map[Grantee]SourceGrant, has func(SourceGrant) bool) string {
	var have, lack []string
	for _, t := range to {
		if has(held[t]) {
			have = append(have, granteeShort(t))
		} else {
			lack = append(lack, granteeShort(t))
		}
	}
	switch {
	case len(have) == len(to):
		return " (held)"
	case len(have) == 0:
		return " (new)"
	default:
		return fmt.Sprintf(" (held by %s, new for %s)", andList(have), andList(lack))
	}
}

// granteeShort is who the grants are for, in the card's short words.
func granteeShort(t Grantee) string {
	if t.Crew != "" {
		return "crew " + t.Crew
	}
	return "group " + t.Group
}

// ApproveSetup approves the setup ask ask on the bead id as actor: the
// preset pick option with the account typed unseen (values), the candidate
// taken (account), or neither where a drift card's standing account is
// kept. In one write it checks the ask and who approves, holds the approval
// to the yard the card was written from, and records an answer that holds
// the preset, the names given, the candidate taken or the kept account,
// and CarryPending, with the carrying record beside it for the server to
// take up. Typed values are handed to the server's memory after the write
// commits, never stored: between the approval and the account's write they
// live there alone. A picked candidate and a kept account hand nothing.
//
// It refuses an ask that is not on the bead, sets up no pack, or was
// declined, taken back or approved already: a setup runs once for one ask.
// It refuses anyone but the operator, and every call from the pane of an
// agent whatever actor it names (mayApprove). It refuses when the yard
// would not write the same card now (setupStale), and — on a first setup —
// when every grantee holds the picked preset already. Picking YouDecide
// grants the asker's recommended preset, and is refused where none is
// recommended. Where candidates stand, exactly one of the typed values and
// the picked candidate settles the account, and where none stand the typed
// values do; a drift card whose file holds every name is also kept with a
// bare pick, and one with missing names wants those typed. A refused
// approval records nothing and changes nothing, and leaves the ask open.
//
// It is served on the yard's socket, and on the listener to a device of
// the operator and to no other caller (rpc.OperatorCalls), as run
// approvals and secret answers travel. No answer that comes over a link
// approves.
func (s *Store) ApproveSetup(ctx context.Context, id string, ask uint64, option int, values map[string]string, account, actor string) (*AskSetupApproved, error) {
	// A bead's handle never holds an "@" (validID).
	if strings.Contains(id, "@") {
		return nil, fmt.Errorf("ask %d on %s is a setup of another yard's operator: it is approved in the yard that asked, never over a link", ask, id)
	}
	// The carry watcher can wake as soon as the write commits. Hold its
	// one-time take until the values are in memory beside the committed
	// carrying record.
	s.setupSecrets.mu.Lock()
	defer s.setupSecrets.mu.Unlock()
	var out *AskSetupApproved
	err := s.writeVia(ctx, func(w *wtx) (err error) {
		out, err = w.approveSetup(ctx, id, ask, option, values, account, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	if account == "" && len(values) > 0 {
		s.handSetupSecretLocked(carryKey(out.Bead, ask), values)
	}
	return out, nil
}

// approveSetup is ApproveSetup inside the transaction. A batch may run it
// twice for one call (write): it reads the yard's records anew each time
// and ends the same. The values are handed to the memory after the write
// commits, so a rolled-back run hands nothing.
func (w *wtx) approveSetup(ctx context.Context, id string, seq uint64, option int, values map[string]string, account, actor string) (*AskSetupApproved, error) {
	who, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = who
	row, err := w.loadBead(id)
	if err != nil {
		return nil, err
	}
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return nil, err
	}
	var r *ResolvedAsk
	resolved := ResolveAsks(notes, now())
	for i := range resolved {
		if resolved[i].Note.Seq == seq {
			r = &resolved[i]
		}
	}
	switch {
	case r == nil && slices.ContainsFunc(notes, func(n Note) bool { return n.Seq == seq }):
		return nil, fmt.Errorf("note %d on bead %s is not an ask", seq, row.ID)
	case r == nil:
		return nil, fmt.Errorf("bead %s has no ask %d", row.ID, seq)
	case r.AskErr != nil:
		return nil, r.AskErr
	case r.Ask.Setup == nil:
		return nil, fmt.Errorf("ask %d sets up no pack: a preset pick with the account settles a setup ask alone", seq)
	case r.State == AskDeclined:
		return nil, fmt.Errorf("ask %d was declined: %s", seq, declinedLine(*r.Current))
	case r.State == AskTakenBack:
		return nil, fmt.Errorf("ask %d is asked no more: %s", seq, takenBackLine(*r.Current))
	case r.State == AskAnswered:
		return nil, fmt.Errorf("ask %d was approved already, by %s: a setup runs once for one ask", seq, r.Current.Note.Author)
	}
	was := *r.Ask.Setup
	// Before anything is written: an agent's approval changes nothing, not
	// even what the transaction would take back.
	if err := w.mayApprove(ctx, actor); err != nil {
		return nil, err
	}
	if option < 1 || option > len(r.Ask.Options) {
		return nil, fmt.Errorf("ask %d has options 1..%d", seq, len(r.Ask.Options))
	}
	picked := option
	if r.Ask.Options[option-1].Label == YouDecide {
		if r.Ask.Recommend == 0 {
			return nil, fmt.Errorf("ask %d: You decide grants the recommended preset, and none is recommended: pick a preset, or decline it", seq)
		}
		picked = r.Ask.Recommend
	}
	if picked < 1 || picked > len(was.Presets) {
		return nil, fmt.Errorf("ask %d holds options the yard did not write: nothing ran", seq)
	}
	account = strings.TrimSpace(account)
	if account != "" && len(values) > 0 {
		return nil, fmt.Errorf("ask %d takes the account typed or a candidate taken, not both: --secret or --account <source>", seq)
	}
	keep := false
	if account != "" {
		found := false
		for _, c := range was.Candidates {
			if c.Source == account {
				found = true
			}
		}
		if !found {
			if len(was.Candidates) == 0 {
				return nil, fmt.Errorf("ask %d lists no candidate to take: type the account %s", seq, strings.Join(was.Account, " and "))
			}
			var names []string
			for _, c := range was.Candidates {
				names = append(names, c.Source)
			}
			return nil, fmt.Errorf("ask %d lists no candidate %s: it lists %s, or type another %s", seq, account, strings.Join(names, ", "), strings.Join(was.Account, " and "))
		}
	} else if len(was.Missing) > 0 {
		// A drift card with missing names wants those typed: the
		// standing names stay where they are.
		if err := checkSetupValues(seq, was, values); err != nil {
			return nil, err
		}
		for _, n := range was.Missing {
			if _, ok := values[n]; !ok {
				if len(was.Candidates) > 0 {
					return nil, fmt.Errorf("ask %d asks for the missing account %s: %s was not given, and no candidate was taken (--account <source>)", seq, strings.Join(was.Missing, " and "), n)
				}
				return nil, fmt.Errorf("ask %d asks for the missing account %s: %s was not given", seq, strings.Join(was.Missing, " and "), n)
			}
		}
		if !sameNames(values, was.Missing) {
			return nil, fmt.Errorf("ask %d asks for the missing account %s: type %s", seq, strings.Join(was.Missing, " and "), strings.Join(was.Missing, " and "))
		}
	} else if was.Given {
		// A drift card whose file holds every name is kept with a bare
		// pick, typed another to change it, or a candidate taken.
		if len(values) == 0 {
			keep = true
		} else {
			if err := checkSetupValues(seq, was, values); err != nil {
				return nil, err
			}
			if !sameNames(values, was.Account) {
				return nil, fmt.Errorf("ask %d holds the account %s: type %s to change it, or approve keeping it with a bare preset pick", seq, strings.Join(was.Account, " and "), strings.Join(was.Account, " and "))
			}
		}
	} else {
		// What was sent is not said back: a client that put a value
		// where a name goes would read it in the refusal.
		if err := checkSetupValues(seq, was, values); err != nil {
			return nil, err
		}
		for _, n := range was.Account {
			if _, ok := values[n]; !ok {
				if len(was.Candidates) > 0 {
					return nil, fmt.Errorf("ask %d asks for the account %s: %s was not given, and no candidate was taken (--account <source>)", seq, strings.Join(was.Account, " and "), n)
				}
				return nil, fmt.Errorf("ask %d asks for the account %s: %s was not given", seq, strings.Join(was.Account, " and "), n)
			}
		}
	}
	card, err := w.setupCard(was.Pack, was.To)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	now := *card
	if !sameMap(now.Seen, was.Seen) || !slices.Equal(now.Does, was.Does) || now.Back != was.Back || now.BackBy != was.BackBy {
		if err := w.setupStale(seq, was, now); err != nil {
			return nil, err
		}
	}
	if err := w.setupHeldStale(seq, was, picked); err != nil {
		return nil, err
	}
	// Weighed again from the card as it stands: a card whose weight is
	// not the one it showed, or that showed none, is approved no more.
	weight, err := w.setupWeight(now)
	if err != nil {
		return nil, fmt.Errorf("ask %d: %w. %s", seq, err, nothingRan)
	}
	if err := weightStale(seq, r.Ask.Weight, weight); err != nil {
		return nil, err
	}
	// An approval the yard carries out after this write leaves its
	// carrying record in it, for the server to take up (carry.go).
	if err := w.recordSetupCarry(row.ID, seq, was, account, keep, actor); err != nil {
		return nil, err
	}
	ans := Answer{Ask: seq, Option: picked, Ran: &AskRan{Argv: []string{"setup", was.Pack}, Result: CarryPending}}
	out := &AskSetupApproved{Bead: row.ID, Ask: seq, Asker: r.Note.Author, Pack: was.Pack, Preset: was.Presets[picked-1].Name, Result: CarryPending}
	switch {
	case account != "":
		ans.Account, out.Account = account, account
	case keep:
		out.Keep = true
	default:
		var given []string
		for _, n := range was.Account {
			if _, ok := values[n]; ok {
				given = append(given, n)
			}
		}
		ans.Given, out.Given = given, given
	}
	text, err := json.Marshal(ans)
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return nil, err
	}
	return out, nil
}

// checkSetupValues checks typed account values against the ask: every name
// sent is one the card asked for, and every value is a login value. What
// was sent is not said back: a client that put a value where a name goes
// would read it in the refusal.
func checkSetupValues(seq uint64, was AskSetup, values map[string]string) error {
	for n, value := range values {
		if !slices.Contains(was.Account, n) {
			return fmt.Errorf("ask %d asks for the account %s, and was given a name it did not ask for", seq, strings.Join(was.Account, " and "))
		}
		if err := loginValueError(seq, n, value); err != nil {
			return err
		}
	}
	return nil
}

// sameNames reports whether the values hold exactly the names: no more,
// no fewer.
func sameNames(values map[string]string, names []string) bool {
	if len(values) != len(names) {
		return false
	}
	for _, n := range names {
		if _, ok := values[n]; !ok {
			return false
		}
	}
	return true
}

// setupStale says why a setup approval is refused: the build or the pack's
// bytes moved, its presets moved, a source it would add or replace moved,
// the account was given or ungiven meanwhile, a candidate was added,
// removed or changed, a grantee moved, or the sentences read otherwise
// now.
func (w *wtx) setupStale(seq uint64, was, now AskSetup) error {
	name := was.Pack
	labelKey, bytesKey, presetsKey := "pack "+name, "pack "+name+" bytes", "pack "+name+" presets"
	if was.Seen[labelKey] != now.Seen[labelKey] || was.Seen[bytesKey] != now.Seen[bytesKey] {
		if was.Seen[bytesKey] == now.Seen[bytesKey] {
			return fmt.Errorf("ask %d was written from pack %s as %s embeds it: the pack is unchanged file for file, but the card named the build it was carded from, and this yard runs %s. %s", seq, name, was.Label, now.Label, nothingRan)
		}
		return fmt.Errorf("ask %d was written from pack %s as %s embeds it: this yard runs %s, whose pack differs. %s", seq, name, was.Label, now.Label, nothingRan)
	}
	if was.Seen[presetsKey] != now.Seen[presetsKey] {
		return fmt.Errorf("ask %d was written from the presets of pack %s as %s embeds them: this yard runs %s, whose presets differ. %s", seq, name, was.Label, now.Label, nothingRan)
	}
	for i := range was.Carried {
		srcName := was.Carried[i].Name
		key := "source " + srcName
		if was.Seen[key] == now.Seen[key] {
			continue
		}
		var nowInv []AskInventory
		if i < len(now.Carried) {
			nowInv = now.Carried[i].Inventory
		}
		if moved := inventoryMoved(was.Carried[i].Inventory, nowInv); moved != "" {
			return fmt.Errorf("ask %d was written when the source %s's copies stood as carded: %s since. %s", seq, srcName, moved, nothingRan)
		}
		if was.Seen[key] == "" {
			return fmt.Errorf("ask %d was written when the source %s was not in the yard: it was added since. %s", seq, srcName, nothingRan)
		}
		// staleSource and staleCard read Seen and Does alone.
		if err := w.staleSource(seq, srcName, AskRun{Seen: was.Seen}, AskRun{Seen: now.Seen}); err != nil {
			return err
		}
		return fmt.Errorf("ask %d was written when the source %s was another: its declaration, its %s or its %s was changed since. %s", seq, srcName, SourceEnvFile, SourceMintCommand, nothingRan)
	}
	for _, c := range was.Carried {
		key := "account " + c.Name
		wasOwed, ok := was.Seen[key]
		if !ok {
			continue
		}
		if wasOwed == now.Seen[key] {
			continue
		}
		if now.Seen[key] == "given" {
			return fmt.Errorf("ask %d was written when the account for the source %s was still owed: it was given since. %s", seq, c.Name, nothingRan)
		}
		return fmt.Errorf("ask %d was written when the account for the source %s was given: its file no longer holds %s. %s", seq, c.Name, strings.Join(was.Account, " and "), nothingRan)
	}
	if err := setupCandidatesStale(seq, was, now); err != nil {
		return err
	}
	for _, t := range was.To {
		if key := t.seenKey(); was.Seen[key] == now.Seen[key] {
			continue
		}
		if t.Crew != "" {
			e := lastEvent(w.Tx, "crew_defined", naming("crew", t.Crew))
			return fmt.Errorf("ask %d was written when the crew member %s was another: its config was changed since%s. %s", seq, t.Crew, since(e), nothingRan)
		}
		e := lastEvent(w.Tx, "group_set", naming("name", t.Group))
		return fmt.Errorf("ask %d was written when the group %s was another: its config was changed since%s. %s", seq, t.Group, since(e), nothingRan)
	}
	return staleCard(seq, AskRun{Does: was.Does}, AskRun{Does: now.Does})
}

// setupCandidatesStale says why a setup approval is refused for its
// candidates: one added, removed or changed since the card was written,
// including a candidate env file changed in place with the same path and
// source settings, which the source fingerprints alone would not catch.
func setupCandidatesStale(seq uint64, was, now AskSetup) error {
	wasByName := map[string]AskCandidate{}
	for _, c := range was.Candidates {
		wasByName[c.Source] = c
	}
	nowByName := map[string]AskCandidate{}
	for _, c := range now.Candidates {
		nowByName[c.Source] = c
	}
	for _, c := range was.Candidates {
		n, ok := nowByName[c.Source]
		if !ok {
			return fmt.Errorf("ask %d was written when the source %s held a matching account: it holds none since. %s", seq, c.Source, nothingRan)
		}
		if was.Seen["candidate "+c.Source] != now.Seen["candidate "+n.Source] {
			return fmt.Errorf("ask %d was written when the candidate %s held %s as carded: its file no longer holds it. %s", seq, c.Source, strings.Join(was.Account, " and "), nothingRan)
		}
	}
	for _, c := range now.Candidates {
		if _, ok := wasByName[c.Source]; !ok {
			return fmt.Errorf("ask %d was written when the source %s held no matching account: it holds one since. %s", seq, c.Source, nothingRan)
		}
	}
	return nil
}

// setupHeldStale refuses a setup approval whose picked preset every
// grantee already holds: it was granted since, and nothing is left to
// grant. What a grantee holds beside the preset is no part of it: a grant
// landed since is run past, as the grant row runs it. A drift card whose
// pack would change anything is never refused for it: the update itself
// still wants applying, and the grants step runs past what is held.
func (w *wtx) setupHeldStale(seq uint64, was AskSetup, picked int) error {
	if (was.Given || len(was.Missing) > 0) && setupDriftChanges(w, was) {
		return nil
	}
	p := was.Presets[picked-1]
	unheld := false
outer:
	for _, t := range was.To {
		for _, pg := range p.Grants {
			var g SourceGrant
			if _, err := get(w.Tx, bSourceGrants, grantKey(pg.Source, t, ""), &g); err != nil {
				return err
			}
			d, derr := declared(w.Tx, pg.Source)
			for _, what := range pg.Calls {
				if slices.Contains(g.Calls, what) {
					continue
				}
				if what != GrantRead && derr == nil {
					if c, ok := d.Call(what); ok && g.Covers(c, "") {
						continue
					}
				}
				unheld = true
				break outer
			}
		}
	}
	if unheld {
		return nil
	}
	t, src := was.To[0], p.Grants[0].Source
	e := lastEvent(w.Tx, "source_granted", func(e Event) bool {
		if e.Data["source"] != src || e.Data["as"] != "" {
			return false
		}
		if t.Crew != "" {
			return e.Data["crew"] == t.Crew
		}
		return e.Data["group"] == t.Group
	})
	return fmt.Errorf("ask %d would grant the preset %s of pack %s to %s, and it is held already: it was granted since%s. %s", seq, p.Name, was.Pack, was.Grantees(), since(e), nothingRan)
}

// setupDriftChanges reports whether the drift card's pack would change
// anything were it applied: a source added, a declaration replaced, or a
// command file changed or removed. A drift card that would change nothing,
// with its preset held and its account standing, is refused held already,
// as a first setup is.
func setupDriftChanges(w *wtx, was AskSetup) bool {
	for i := range was.Carried {
		c := was.Carried[i]
		if len(c.Old) > 0 || len(c.Removed) > 0 {
			return true
		}
		src, _, err := sourceIn(w.Tx, c.Name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return true
			}
			continue
		}
		if src.Text != c.Text {
			return true
		}
	}
	return false
}

// recordSetupCarry makes the carrying record of the setup ask bead/seq in
// the approval's own write: approved and not yet taken, with what each
// source it names stands with now. Argv names the setup for the server to
// take up; the typed account values are handed to the server's memory
// beside it (handSetupSecret), never stored, a picked candidate is named
// on the record for the carrying out to point at, and a kept standing
// account is marked so a stop loses nothing.
func (w *wtx) recordSetupCarry(bead string, seq uint64, was AskSetup, account string, keep bool, actor string) error {
	rec := Carry{Bead: bead, Ask: seq, Argv: []string{"setup", was.Pack}, State: CarryApproved, Actor: actor, Account: account, Keep: keep}
	for _, carried := range was.Carried {
		snap := CarrySource{Name: carried.Name}
		if src, _, err := sourceIn(w.Tx, carried.Name); err == nil {
			snap.EnvFile, snap.MintCommand = src.EnvFile, src.MintCommand
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		rec.Sources = append(rec.Sources, snap)
	}
	return put(w.Tx, bCarry, carryKey(bead, seq), rec)
}

// setupSecretStash is the setup account values approvals handed to the
// server's memory, by carrying key: in memory only, so a value is never in
// the file. The approval hands them after its write commits (ApproveSetup);
// the carrying out takes them once. A stop between loses them, and the ask
// is recorded failed naming the account asked again.
type setupSecretStash struct {
	mu    sync.Mutex
	byAsk map[string]map[string]string
}

// handSetupSecretLocked hands the account values of one approved setup to
// the memory, copied: the caller keeps its map. The approval holds the
// mutex from before its write through this handoff, so a watcher cannot
// take a committed record before its values are ready.
func (s *Store) handSetupSecretLocked(key string, values map[string]string) {
	if s.setupSecrets.byAsk == nil {
		s.setupSecrets.byAsk = map[string]map[string]string{}
	}
	handed := make(map[string]string, len(values))
	for n, v := range values {
		handed[n] = v
	}
	s.setupSecrets.byAsk[key] = handed
}

// takeSetupSecret takes the account values of one approved setup from the
// memory, once: a second take finds none. The carrying out takes them for
// the account's write.
func (s *Store) takeSetupSecret(key string) (map[string]string, bool) {
	s.setupSecrets.mu.Lock()
	defer s.setupSecrets.mu.Unlock()
	values, ok := s.setupSecrets.byAsk[key]
	if ok {
		delete(s.setupSecrets.byAsk, key)
	}
	return values, ok
}

// TakeSetupSecret takes the account values the approval of the setup ask
// ask on the bead bead handed to the server's memory, once: a second take
// finds none, as a server that stopped before the account's write does.
// The server's carrying out takes them for the account's write.
func (s *Store) TakeSetupSecret(bead string, ask uint64) (map[string]string, bool) {
	return s.takeSetupSecret(carryKey(bead, ask))
}
