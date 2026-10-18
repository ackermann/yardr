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
	"encoding/json"
	"errors"
	"fmt"
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

// AskSetup is the setup a setup ask proposes: the pack, who the grants are
// for, and the yard's card for it. Its writer sends Pack and To alone; the
// rest is the yard's, written in the write that stores the ask (askedSetup)
// whatever the writer sent for it: the account names, the server's bytes of
// the pack with its presets, the sentences, and what the yard held of each
// thing they name.
type AskSetup struct {
	Pack  string `json:"pack"`
	Kind  string `json:"kind,omitempty"`
	Label string `json:"label,omitempty"`
	// To are the grantees, the groups before the crew members and each in
	// the order of their names.
	To []Grantee `json:"to"`
	// Account are the names to type unseen beside the preset pick: the
	// mint's inputs where the pack's credential is minted, else every
	// credential name. AccountFile is the file the value goes to.
	Account     []string `json:"account,omitempty"`
	AccountFile string   `json:"account_file,omitempty"`
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
// pack with the preset picked, the names given, and what the approval
// recorded (CarryPending until the server carries it out).
type AskSetupApproved struct {
	Bead   string   `json:"bead"`
	Ask    uint64   `json:"ask"`
	Asker  string   `json:"asker"`
	Pack   string   `json:"pack"`
	Preset string   `json:"preset"`
	Given  []string `json:"given"`
	Result string   `json:"result"`
}

// SetupAsked is the setup a setup ask proposes, as a client shows it: the
// pack, the account to type beside the preset pick, the yard's sentences
// for what approving it does, and the presets to pick from. All of it is
// the yard's, and none of it the asker's.
type SetupAsked struct {
	Pack string `json:"pack"`
	// Label is the build the pack is embedded in, as the card shows it.
	Label string `json:"label,omitempty"`
	// Account are the names to type unseen beside the preset pick, and
	// AccountFile the file the value goes to, on the yard's machine.
	Account     []string `json:"account"`
	AccountFile string   `json:"account_file"`
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
		AccountFile: a.Setup.AccountFile, Does: a.Setup.Does, Back: a.Setup.Back,
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
	return card, setupOptions(card.Presets, card.Carried, decls, ask.Recommend), nil
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
	account, file, err := w.setupAccount(pack, carried, decls)
	if err != nil {
		return nil, err
	}
	seenMap := map[string]string{
		"pack " + pack:              reading.Label,
		"pack " + pack + " bytes":   packBytesSeen(&PackReading{Sources: carried}),
		"pack " + pack + " presets": setupPresetsSeen(presets),
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
	does, err := w.setupDoes(pack, reading, carried, decls, account, file, to)
	if err != nil {
		return nil, err
	}
	card := &AskSetup{Pack: pack, Kind: reading.Kind, Label: reading.Label, To: to,
		Account: account, AccountFile: file, Carried: carried, Presets: presets,
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
// source that names a credential must name the same one, by its mint's
// inputs, its mint program's bytes and arguments, and its static names;
// the file is the first such source's, in the pack's declaration order. A
// pack whose sources name different credentials is set up step by step, an
// account card per source.
func (w *wtx) setupAccount(pack string, carried []AskSource, decls map[string]*source.Declaration) ([]string, string, error) {
	var have []string
	keys := map[string]string{}
	for i := range carried {
		d := decls[carried[i].Name]
		if d.Credential == nil {
			continue
		}
		have = append(have, carried[i].Name)
		mintCommand := ""
		if src, _, err := sourceIn(w.Tx, carried[i].Name); err == nil {
			mintCommand = src.MintCommand
		} else if !errors.Is(err, ErrNotFound) {
			return nil, "", err
		}
		keys[carried[i].Name] = setupCredentialKey(d, carried[i], mintCommand)
	}
	if len(have) == 0 {
		return nil, "", fmt.Errorf("pack %s names no credential: no account is owed, so set it up step by step instead: a pack apply card, and a grant card per source and kind", pack)
	}
	for _, name := range have[1:] {
		// A shared variable name does not make two static credentials the
		// same account, and an operator's per-source mint override is part
		// of the effective command the inputs belong to.
		if !decls[have[0]].Credential.Minted() || !decls[name].Credential.Minted() {
			return nil, "", differentSetupCredentials(pack, have[0], name)
		}
		if keys[name] != keys[have[0]] {
			return nil, "", differentSetupCredentials(pack, have[0], name)
		}
	}
	d := decls[have[0]]
	account := d.Credential.MintOrStatic()
	file := ""
	if src, _, err := sourceIn(w.Tx, have[0]); err == nil && src.EnvFile != "" {
		file = src.EnvFile
	} else if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, "", err
	} else if w.store != nil {
		file = source.EnvFile(w.store.home, have[0])
	}
	return account, file, nil
}

func differentSetupCredentials(pack, first, second string) error {
	return fmt.Errorf("pack %s carries sources under different credentials (the source %s and the source %s): one card carries one account choice, so set it up step by step instead: a pack apply card, an account card per source, and a grant card per source and kind", pack, first, second)
}

// setupCredentialKey is what groups a pack's sources under one account
// choice: the mint's input names, the mint program (by the sha256 of its
// carried bytes where relative, literally where absolute) with its
// arguments, and the credential names. An operator's mint_command for a
// source wins over the pack's command, as it does when a call is made.
func setupCredentialKey(d *source.Declaration, carried AskSource, setting string) string {
	c := d.Credential
	inputs := append([]string{}, c.Mint...)
	sort.Strings(inputs)
	var prog string
	argv := []string{}
	if command := strings.Fields(setting); len(command) > 0 {
		prog = "setting:" + command[0]
		argv = append([]string{}, command[1:]...)
	} else if len(c.MintCommand) > 0 {
		prog = c.MintCommand[0]
		argv = append([]string{}, c.MintCommand[1:]...)
		if !strings.HasPrefix(prog, "/") {
			prog = "sha256:" + carriedSHA(carried, prog)
		}
	}
	return seen(inputs, prog, argv, c.Env, c.UpdateEnv)
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

// setupDoes is the setup card's sentences: the pack line, each source as
// the apply card renders it, the account line, and the grants to whom.
// The pack line is the apply row's, without "It grants no call": the card
// grants, but the pack itself makes no hook and sets no credential's
// file, the account step doing that. Each source reads as the apply card
// renders it but for what stays owed after: the card settles the account
// itself, so the credential is no part of it.
func (w *wtx) setupDoes(pack string, reading *PackReading, carried []AskSource, decls map[string]*source.Declaration, account []string, file string, to []Grantee) ([]string, error) {
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
	does = append(does, fmt.Sprintf("the account %s: type it unseen. It is written to %s, named in the answer, never the value.", strings.Join(account, " and "), file))
	does = append(does, fmt.Sprintf("grants to %s — pick one:", granteesWord(to)))
	for i := range does {
		does[i] = safeSourceCardLine(does[i])
	}
	return does, nil
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
// who wants no grants declines the card and applies by terminal.
func setupOptions(presets []AskPreset, carried []AskSource, decls map[string]*source.Declaration, recommend int) []AskOption {
	out := make([]AskOption, 0, len(presets)+1)
	for _, p := range presets {
		out = append(out, AskOption{Label: p.Name, Consequence: setupPresetSays(p, carried, decls)})
	}
	decide := "no preset is recommended, so pick one of the presets above"
	if recommend >= 1 && recommend <= len(presets) {
		decide = fmt.Sprintf("the recommended preset, %s, is granted", presets[recommend-1].Name)
	}
	return append(out, AskOption{Label: YouDecide, Consequence: decide})
}

// setupPresetSays renders what picking the preset grants, from the
// declarations: each call with its method and path, read expanded to
// today's reads with the later-reads line, an update marked as changing
// something there. It reads as the grant card reads, for each source the
// preset grants of.
func setupPresetSays(p AskPreset, carried []AskSource, decls map[string]*source.Declaration) string {
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
		clauses = append(clauses, setupPresetSourceSays(d, calls))
	}
	return strings.Join(clauses, " ")
}

// setupPresetSourceSays renders what the preset grants of one source.
func setupPresetSourceSays(d *source.Declaration, calls []string) string {
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
	return strings.Join(says, " ")
}

// ApproveSetup approves the setup ask ask on the bead id as actor: the
// preset pick option with the account values typed unseen. In one write it
// checks the ask and who approves, holds the approval to the yard the card
// was written from, and records an answer that holds the preset, the names
// given and CarryPending, with the carrying record beside it for the server
// to take up. The values are handed to the server's memory after the write
// commits, never stored: between the approval and the account's write they
// live there alone.
//
// It refuses an ask that is not on the bead, sets up no pack, or was
// declined, taken back or approved already: a setup runs once for one ask.
// It refuses anyone but the operator, and every call from the pane of an
// agent whatever actor it names (mayApprove). It refuses when the yard
// would not write the same card now (setupStale), and when every grantee
// holds the picked preset already. Picking YouDecide grants the asker's
// recommended preset, and is refused where none is recommended. A refused
// approval records nothing and changes nothing, and leaves the ask open.
//
// It is served on the yard's socket, and on the listener to a device of
// the operator and to no other caller (rpc.OperatorCalls), as run
// approvals and secret answers travel. No answer that comes over a link
// approves.
func (s *Store) ApproveSetup(ctx context.Context, id string, ask uint64, option int, values map[string]string, actor string) (*AskSetupApproved, error) {
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
		out, err = w.approveSetup(ctx, id, ask, option, values, actor)
		return err
	})
	if err != nil {
		return nil, err
	}
	s.handSetupSecretLocked(carryKey(out.Bead, ask), values)
	return out, nil
}

// approveSetup is ApproveSetup inside the transaction. A batch may run it
// twice for one call (write): it reads the yard's records anew each time
// and ends the same. The values are handed to the memory after the write
// commits, so a rolled-back run hands nothing.
func (w *wtx) approveSetup(ctx context.Context, id string, seq uint64, option int, values map[string]string, actor string) (*AskSetupApproved, error) {
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
	// What was sent is not said back: a client that put a value where a
	// name goes would read it in the refusal.
	for n := range values {
		if !slices.Contains(was.Account, n) {
			return nil, fmt.Errorf("ask %d asks for the account %s, and was given a name it did not ask for", seq, strings.Join(was.Account, " and "))
		}
	}
	for _, n := range was.Account {
		value, ok := values[n]
		if !ok {
			return nil, fmt.Errorf("ask %d asks for the account %s: %s was not given", seq, strings.Join(was.Account, " and "), n)
		}
		if err := loginValueError(seq, n, value); err != nil {
			return nil, err
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
	// An approval the yard carries out after this write leaves its
	// carrying record in it, for the server to take up (carry.go).
	if err := w.recordSetupCarry(row.ID, seq, was, actor); err != nil {
		return nil, err
	}
	text, err := json.Marshal(Answer{Ask: seq, Option: picked, Given: was.Account, Ran: &AskRan{Argv: []string{"setup", was.Pack}, Result: CarryPending}})
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteAnswer, string(text), actor); err != nil {
		return nil, err
	}
	return &AskSetupApproved{Bead: row.ID, Ask: seq, Asker: r.Note.Author, Pack: was.Pack, Preset: was.Presets[picked-1].Name, Given: was.Account, Result: CarryPending}, nil
}

// setupStale says why a setup approval is refused: the build or the pack's
// bytes moved, its presets moved, a source it would add or replace moved,
// the account was given or ungiven meanwhile, a grantee moved, or the
// sentences read otherwise now.
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

// setupHeldStale refuses a setup approval whose picked preset every
// grantee already holds: it was granted since, and nothing is left to
// grant. What a grantee holds beside the preset is no part of it: a grant
// landed since is run past, as the grant row runs it.
func (w *wtx) setupHeldStale(seq uint64, was AskSetup, picked int) error {
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

// recordSetupCarry makes the carrying record of the setup ask bead/seq in
// the approval's own write: approved and not yet taken, with what each
// source it names stands with now. Argv names the setup for the server to
// take up; the typed account values are handed to the server's memory
// beside it (handSetupSecret), never stored.
func (w *wtx) recordSetupCarry(bead string, seq uint64, was AskSetup, actor string) error {
	rec := Carry{Bead: bead, Ask: seq, Argv: []string{"setup", was.Pack}, State: CarryApproved, Actor: actor}
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
