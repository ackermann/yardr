// The source add row of the run-ask list (design yardr-bht9, B1): a
// source whose declaration the ask itself carries, so the card shows the
// whole declaration in the yard's words and the approval adds exactly
// what was carded. A commands-free add runs in the approval's own write;
// an add that names commands stores its declaration there and is carried
// out by the server's file step (B3). A name the yard has is replaced on a
// card of its own (runask_sourcereplace.go); an absolute program stays the
// operator's alone in a terminal.
package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"yardr.dev/yardr/internal/source"
)

// parseSourceAdd reads the words of source add <file>: one file, named as
// typed. What the file holds is carried beside argv, never in it.
func parseSourceAdd(rest []string) ([]string, error) {
	const use = "yardr source add <file>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one file, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// carriedSource validates what a source add ask carries and answers its
// declaration parsed by the store itself. file names the text in errors,
// as source add names it. absolute says the case that stays the
// operator's alone and exists that the name stands, which the card
// replaces; err refuses the ask for anything else it holds.
func (w *wtx) carriedSource(run AskRun) (d *source.Declaration, carried AskSource, absolute, exists bool, err error) {
	file := run.Argv[2]
	if len(run.Carried) != 1 {
		return nil, AskSource{}, false, false, fmt.Errorf("--run takes yardr source add <file> with the declaration of one source carried on the ask, and this carries %d", len(run.Carried))
	}
	carried = run.Carried[0]
	if len(carried.Text) > MaxRunSourceDeclaration {
		return nil, AskSource{}, false, false, fmt.Errorf("source %s: its declaration is %d bytes: a card holds at most %d, so this is the operator's to type in a terminal (yardr source add %s)", OneLine(carried.Name), len(carried.Text), MaxRunSourceDeclaration, file)
	}
	d, err = source.Parse(file, []byte(carried.Text))
	if err != nil {
		return nil, AskSource{}, false, false, fmt.Errorf("%s", OneLine(err.Error()))
	}
	if carried.Name != d.Name {
		return nil, AskSource{}, false, false, fmt.Errorf("the ask carries the source %s, and its declaration names %s: the two must be one", OneLine(carried.Name), d.Name)
	}
	total := len(carried.Text)
	byProgram := map[string]AskFile{}
	for _, f := range carried.Files {
		if _, twice := byProgram[f.Program]; twice {
			return nil, AskSource{}, false, false, fmt.Errorf("source %s: the ask carries the command file %s twice", d.Name, OneLine(f.Program))
		}
		byProgram[f.Program] = f
		if len(f.Text) > MaxRunSourceCommandFile {
			return nil, AskSource{}, false, false, fmt.Errorf("source %s: the command file %s is %d bytes: a card holds at most %d, so this is the operator's to type in a terminal (yardr source add %s)", d.Name, OneLine(f.Program), len(f.Text), MaxRunSourceCommandFile, file)
		}
		if !utf8.ValidString(f.Text) {
			return nil, AskSource{}, false, false, fmt.Errorf("source %s: the command file %s is not valid UTF-8 text: a card carries text only, so add it with yardr source add %s in a terminal", d.Name, OneLine(f.Program), file)
		}
		sum := sha256.Sum256([]byte(f.Text))
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			return nil, AskSource{}, false, false, fmt.Errorf("source %s: the ask carries %s with sha256 %s, and its bytes hash to %s: nothing was stored", d.Name, OneLine(f.Program), OneLine(f.SHA256), got)
		}
		total += len(f.Text)
	}
	if total > MaxRunSourceTexts {
		return nil, AskSource{}, false, false, fmt.Errorf("source %s: its declaration with its command files is %d bytes: a card holds at most %d together, so this is the operator's to type in a terminal (yardr source add %s)", d.Name, total, MaxRunSourceTexts, file)
	}
	// The files the declaration names, each once, in its own order.
	var programs []string
	seenProg := map[string]bool{}
	for _, c := range d.Commands() {
		if !c.Relative() {
			absolute = true
			continue
		}
		if !seenProg[c.Argv[0]] {
			seenProg[c.Argv[0]] = true
			programs = append(programs, c.Argv[0])
		}
	}
	if !absolute {
		for _, prog := range programs {
			if _, ok := byProgram[prog]; !ok {
				return nil, AskSource{}, false, false, fmt.Errorf("source %s: its declaration names the command file %s, and the ask carries no bytes for it", d.Name, OneLine(prog))
			}
		}
		for prog := range byProgram {
			if !seenProg[prog] {
				return nil, AskSource{}, false, false, fmt.Errorf("source %s: the ask carries %s, and its declaration names no such command file", d.Name, OneLine(prog))
			}
		}
		// Store the files in the declaration's order, as the card groups them.
		files := make([]AskFile, 0, len(programs))
		for _, prog := range programs {
			files = append(files, byProgram[prog])
		}
		carried.Files = files
	}
	if w.Bucket(bSources).Get([]byte(d.Name)) != nil {
		exists = true
	}
	return d, carried, absolute, exists, nil
}

// checkSourceAdd is adding a source's own check as the asker: a source
// whose bytes the ask can carry may be proposed, one the yard has
// included (its card is a replacement's), and an absolute program is the
// operator's alone, in maySource's words with its mark. PutSource keeps
// its own check, so a replacement by a crew member's own command is
// refused as before.
func checkSourceAdd(ctx context.Context, w *wtx, run AskRun, actor string) error {
	d, _, absolute, _, err := w.carriedSource(run)
	if err != nil {
		return err
	}
	if absolute {
		return w.may(ctx, actor, ActAddSource, Object{Kind: KindSource, Name: d.Name})
	}
	return w.may(ctx, actor, ActAddSource, Object{Kind: KindSource, Name: d.Name, Ask: Askable})
}

// sourceAddCard is the card of source add <file>: the whole declaration
// the ask carries, in the yard's sentences (sourceDoes), held to the name
// being free (Seen). A name the yard has gets a replacement's card
// (sourceReplaceCard) when the ask is written, and when an approval reads
// the card again for an ask that was written as one; an ask written for
// a free name is stale if the name was taken since, and is never turned
// into a replacement. Where a free name's copy directory holds orphaned
// files from a removed source, the card inventories and names what the
// add would replace or remove, as a pack card does, and holds the ask to
// that inventory.
func (w *wtx) sourceAddCard(run AskRun, asking bool) (AskRun, error) {
	d, carried, absolute, exists, err := w.carriedSource(run)
	if err != nil {
		return AskRun{}, err
	}
	// What the yard's copies hold is the yard's own record, made from the
	// yard as it stands. The ask's, which an approval reads back from the
	// stored card and a writer may have sent, is thrown away as its Does
	// and Seen are.
	carried.Old, carried.Removed, carried.Inventory = nil, nil, nil
	switch {
	case absolute:
		return AskRun{}, fmt.Errorf("source %s: a command of its declaration names an absolute program, which is the operator's own to have installed: add it with yardr source add %s in a terminal", d.Name, run.Argv[2])
	case exists && (asking || storedReplacement(run, d.Name)):
		return w.sourceReplaceCard(run, d, carried)
	case exists:
		// The name was taken since the card was written: say so in one
		// sentence, so the approval is held to it as stale.
		src, _, err := sourceIn(w.Tx, d.Name)
		if err != nil {
			return AskRun{}, err
		}
		fingerprint, err := w.sourceSeen(src)
		if err != nil {
			return AskRun{}, err
		}
		return AskRun{
			Argv:    run.Argv,
			Does:    []string{fmt.Sprintf("source %s is in the yard already.", d.Name)},
			Back:    runCommandLine([]string{"source", "rm", d.Name}),
			BackBy:  "the operator",
			Seen:    map[string]string{"source " + d.Name: fingerprint},
			Carried: []AskSource{carried},
		}, nil
	}
	file := run.Argv[2]
	terminal := fmt.Sprintf("this is the operator's to type in a terminal (yardr source add %s)", file)
	pay, err := w.packReplacement(&carried, d, terminal)
	if err != nil {
		return AskRun{}, err
	}
	for _, f := range carried.Files {
		pay += len(f.Text)
	}
	if pay > MaxRunPackFiles {
		return AskRun{}, fmt.Errorf("source %s: its carried file texts and diffs run to %d bytes: a card holds at most %d together, so %s", d.Name, pay, MaxRunPackFiles, terminal)
	}
	diffs := commandDiffs(d, carried)
	does := sourceDoesChanged(d, carried, diffs, false, "")
	seenMap := map[string]string{"source " + d.Name: ""}
	if len(carried.Inventory) > 0 {
		header := fmt.Sprintf("the source %s is not in the yard, but its command copy directory has entries it would replace or remove.", d.Name)
		extra := append([]string{header}, changedCopyLines(carried, diffs)...)
		// After the credential line, which is the second of does: the
		// adding line stands for the pack line.
		out := make([]string, 0, len(does)+len(extra))
		out = append(out, does[:2]...)
		out = append(out, extra...)
		out = append(out, does[2:]...)
		does = out
		// A previous removal can leave command copies behind if the
		// process stopped between deleting the source and its files.
		// Adding this source removes that directory too, so hold the
		// ask to what it contains even though no source record stands.
		seenMap["source "+d.Name] = seen("no source", carried.Inventory)
	}
	if len(does) > MaxRunSourceCardLines {
		return AskRun{}, cardTooLong(d.Name, len(does), file)
	}
	return AskRun{
		Argv:    run.Argv,
		Does:    does,
		Back:    runCommandLine([]string{"source", "rm", d.Name}),
		BackBy:  "the operator",
		Seen:    seenMap,
		Carried: []AskSource{carried},
	}, nil
}

// storedReplacement says the stored card run was written as a
// replacement of the source name: its Seen holds a source fingerprint.
// A card written for a free name holds none: "" where no copies stood,
// the orphan inventory's where they did.
func storedReplacement(run AskRun, name string) bool {
	fingerprint := run.Seen["source "+name]
	if fingerprint == "" {
		return false
	}
	var inv []AskInventory
	if len(run.Carried) > 0 {
		inv = run.Carried[0].Inventory
	}
	return fingerprint != seen("no source", inv)
}

// sourceAddStale says why the ask seq for source add is approved no more:
// the name was free when the card was written and is taken now, the
// orphaned copies a free name's card was written about moved, or the
// source a replacement's card was written about is another
// (sourceReplaceStale).
func (w *wtx) sourceAddStale(seq uint64, was, now AskRun) error {
	if len(was.Carried) == 0 {
		return staleCard(seq, was, now)
	}
	name := was.Carried[0].Name
	key := "source " + name
	if was.Seen[key] == now.Seen[key] {
		return staleCard(seq, was, now)
	}
	if storedReplacement(was, name) {
		return w.sourceReplaceStale(seq, name, was, now)
	}
	if w.Bucket(bSources).Get([]byte(name)) != nil {
		e := lastEvent(w.Tx, "source_added", naming("source", name))
		return fmt.Errorf("ask %d was written when the source %s was not in the yard: it was added since%s. %s", seq, name, since(e), nothingRan)
	}
	var nowInv []AskInventory
	if len(now.Carried) > 0 {
		nowInv = now.Carried[0].Inventory
	}
	if moved := inventoryMoved(was.Carried[0].Inventory, nowInv); moved != "" {
		return fmt.Errorf("ask %d was written when the source %s's copies stood as carded: %s since. %s", seq, name, moved, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runSourceAdd adds or replaces the carried source as actor: a
// commands-free declaration in this write, through the same put PutSource
// makes, and one that names commands, or replaces a source whose command
// copies stand, with its declaration stored and its files left to the
// server's carrying out (B3), which makes the copies anew as source add
// does. The ask's Seen says which it was written as.
func runSourceAdd(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error) {
	d, carried, _, _, err := w.carriedSource(run)
	if err != nil {
		return "", err
	}
	src, err := w.putCarriedSource(ctx, d, carried.Text, actor, storedReplacement(run, d.Name), by)
	if err != nil {
		return "", err
	}
	if len(d.Commands()) > 0 || len(carried.Inventory) > 0 {
		return CarryPending, nil
	}
	if len(d.Calls) == 0 {
		return fmt.Sprintf("source %s saved: no call, its limits command alone", d.Name), nil
	}
	return fmt.Sprintf("source %s saved: %s under %s", d.Name, pluralCalls(len(d.Calls)), src.CalledAt(d)), nil
}

// pluralCalls is n with call or calls, as yardr source add prints it.
func pluralCalls(n int) string {
	if n == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", n)
}

// sourceDoes renders the declaration d the ask carries in the yard's
// sentences, complete as section 3.3 of yardr-bht9 holds it: approving
// without opening the text approves nothing these sentences have not
// named. The text itself stands behind them in a terminal.
func sourceDoes(d *source.Declaration, carried AskSource) []string {
	return sourceDoesChanged(d, carried, nil, false, "")
}

// sourceDoesChanged renders as sourceDoes does, and where diffs names a
// command file's change the command line carries the change itself: its
// old and new hashes with its insertions and deletions counted, beside
// the uses the file already names. A replacement's card reads that way,
// so the change is said once and not again on its own line. Where
// settleAccount the credential is no part of what stays owed after: the
// card settles the account itself, as a setup card does. base is the
// region base a setup ask chose, "" for the declaration's own: the card
// shows the base the setup will call.
func sourceDoesChanged(d *source.Declaration, carried AskSource, diffs map[string]AskFileDiff, settleAccount bool, base string) []string {
	files, commands := len(carried.Files), len(d.Commands())
	copies := "copies no command files"
	if files > 0 {
		copies = fmt.Sprintf("copies %s (%s)", countNoun(files, "command file"), countNoun(commands, "command"))
	}
	does := []string{fmt.Sprintf("it adds one source and %s, and changes nothing else. It grants no call, makes no hook, and sets no credential's file.", copies)}
	does = append(does, credentialSays(d, carried, diffs, base))
	for i := range d.Calls {
		does = append(does, callSaysWhole(d, &d.Calls[i])...)
	}
	does = append(does, checkSays(d))
	if d.Notify != nil {
		n := d.Notify
		var fills []string
		if n.Title != "" {
			fills = append(fills, "the title fills "+n.Title)
		} else {
			fills = append(fills, "it takes no title")
		}
		if n.Text != "" {
			fills = append(fills, "the text fills "+n.Text)
		} else {
			fills = append(fills, "it takes no text")
		}
		if n.Link != "" {
			fills = append(fills, "the link fills "+n.Link)
		} else {
			fills = append(fills, "it takes no link")
		}
		line := fmt.Sprintf("it can tell a user: the call %s, whose %s takes the person's address, read as %s from the person's own env file; %s", n.Call, n.Address, n.AddressEnv, strings.Join(fills, " and "))
		does = append(does, line+".")
	} else {
		does = append(does, "it can tell no user.")
	}
	if d.Hear != nil {
		who := "every told user's"
		if d.Hear.From == source.HearFromOperator {
			who = "only an operator's"
		}
		does = append(does, fmt.Sprintf("it hears deliveries: only one whose %s is the address of a user told through it, read as %s, and %s: another's, and a stranger's, is dropped after verification.", d.Hear.Field, d.Hear.Address.Env, who))
	} else {
		does = append(does, "it hears no deliveries.")
	}
	if d.Limits != nil {
		does = append(does, limitsSays(d))
	} else {
		does = append(does, "it names no limits command.")
	}
	if d.Thread != nil {
		does = append(does, threadSays(d))
	} else {
		does = append(does, "it speaks in no threads.")
	}
	if d.Link != nil {
		does = append(does, linkSays(d))
	} else {
		does = append(does, "its issues are no bead's link.")
	}
	if d.Identity != nil {
		does = append(does, fmt.Sprintf("its identity keys fold to lowercase (identity fold: %s), so one spelling is stored however it was typed.", d.Identity.Fold))
	} else {
		does = append(does, "its identity keys keep their case.")
	}
	if d.Hook != nil {
		does = append(does, hookSays(d))
	} else {
		does = append(does, "it sends no hook deliveries into the yard.")
	}
	if d.Events != nil {
		does = append(does, eventsSays(d))
	} else {
		does = append(does, "it sends no pull-request or CI events.")
	}
	does = append(does, commandSaysChanged(d, carried, diffs)...)
	var owed []string
	if d.Credential != nil && !settleAccount {
		owed = append(owed, fmt.Sprintf("the credential, by a secret ask for %s", d.Name))
	}
	if d.Notify != nil {
		owed = append(owed, fmt.Sprintf("which user it tells, by yardr user set <user> notify=%s", d.Name))
	}
	if len(owed) > 0 {
		does = append(does, "still owed after it: "+strings.Join(owed, "; and ")+".")
	}
	// Declaration text is outside input. Keep each yard sentence to one
	// terminal-safe line so a value cannot forge another part of the card.
	for i := range does {
		does[i] = safeSourceCardLine(does[i])
	}
	return does
}

func safeSourceCardLine(s string) string {
	s = NoteLetters(s)
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "\t", " ")
}

// credentialSays is the source and its credential in one sentence: where
// its calls go, the variables its credential is read under by name and
// never a value, where the value goes, and the mint command the
// declaration brings with its file's sha256 whole, or its change where
// diffs names one: that program runs with the operator's logins, so what
// approving it approves is said where the credential is.
func credentialSays(d *source.Declaration, carried AskSource, diffs map[string]AskFileDiff, base string) string {
	head := fmt.Sprintf("source %s: it has no base and calls nothing.", d.Name)
	if addr := d.Address(); addr != "" {
		head = fmt.Sprintf("source %s: its calls go to %s.", d.Name, addr)
		if u, ok := d.BaseURL(base); base != "" && ok {
			head = fmt.Sprintf("source %s: its calls go to %s (base %s).", d.Name, u, base)
		}
	}
	if d.Credential == nil {
		return head + " It names no credential."
	}
	c := d.Credential
	s := head + fmt.Sprintf(" Its credential is the variable %s (the name, never the value)", c.Env)
	if c.UpdateEnv != "" {
		s += fmt.Sprintf(", with %s for updates", c.UpdateEnv)
	}
	switch {
	case len(c.Mint) > 0 && len(c.MintCommand) > 0:
		// The program that runs with a person's login is named here, and
		// its file's sha256 on its command line below: approving the card
		// approves those bytes, and a later pack that changes them shows
		// the change on its own card. The operator's own mint_command,
		// where one is set, still wins over it.
		prog := c.MintCommand[0]
		at := "<home>/sources/" + d.Name + "/" + prog
		file := fmt.Sprintf("command file %s, copied to %s: sha256 %s, mode 0700", prog, at, carriedSHA(carried, prog))
		if df, ok := diffs[prog]; ok {
			ins, del := Counts(df.Diff)
			file = fmt.Sprintf("command file %s, copied to %s: sha256 %s -> sha256 %s, mode 0700 (%d insertions, %d deletions)", prog, at, or(df.OldSHA256, "(none)"), or(df.NewSHA256, "(none)"), ins, del)
		}
		s += fmt.Sprintf(", minted from %s by the declaration's own mint command %s (%s), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it", strings.Join(c.Mint, " and "), showCommand(c.MintCommand), file)
	case len(c.Mint) > 0:
		s += fmt.Sprintf(", minted from %s: a mint runs only where the yard's own mint_command is the operator's already, and this adds none", strings.Join(c.Mint, " and "))
	}
	var placements []string
	if c.URL {
		placements = append(placements, "as the address every call goes to (credential.url: true): whoever sets the variable chooses where the calls go, over https or plain http to this machine alone")
	}
	if c.Header != "" {
		placements = append(placements, fmt.Sprintf("in the header %q on every call", c.Header))
	}
	if c.InPath {
		placements = append(placements, "in the request path wherever the declaration puts {credential} (credential.in_path: true): it stands in the logs of every server on the way")
	}
	if calls := bodyCredentialCalls(d); len(calls) > 0 {
		placements = append(placements, "as a leaf of the body of call "+andList(calls))
	}
	if len(placements) == 0 {
		s += ", and no call's header, path or body holds it."
	} else {
		s += ", sent " + andList(placements) + "."
	}
	var lacks []string
	if c.UpdateEnv == "" {
		lacks = append(lacks, "no second credential for updates")
	}
	if len(c.Mint) == 0 {
		lacks = append(lacks, "no mint")
	}
	if c.Header == "" {
		lacks = append(lacks, "no header of its own")
	}
	if len(lacks) > 0 {
		s = strings.TrimSuffix(s, ".") + fmt.Sprintf(". It has %s.", strings.Join(lacks, ", "))
	}
	return s
}

func bodyCredentialCalls(d *source.Declaration) []string {
	var holds func(v any) bool
	holds = func(v any) bool {
		switch v := v.(type) {
		case map[string]any:
			for _, child := range v {
				if holds(child) {
					return true
				}
			}
		case []any:
			for _, child := range v {
				if holds(child) {
					return true
				}
			}
		case string:
			return v == source.CredentialMark
		}
		return false
	}
	var calls []string
	for i := range d.Calls {
		if holds(d.Calls[i].Request.Body) {
			calls = append(calls, d.Calls[i].Name)
		}
	}
	return calls
}

// callSaysWhole is one call in two sentences: its method and path whole
// with its parameters and query, then its body whole as the yard would
// send it and what of its answer is kept.
func callSaysWhole(d *source.Declaration, c *source.Call) []string {
	takes := "It takes no parameter"
	if len(c.Params) > 0 {
		parts := make([]string, len(c.Params))
		for i, p := range c.Params {
			parts[i] = paramSays(p)
		}
		takes = "It takes " + andList(parts)
	}
	sends := "and sends no query."
	if len(c.Request.Query) > 0 {
		var qs []string
		for _, q := range c.Request.Query {
			qs = append(qs, displaySourceValue(q.Key)+"="+displaySourceValue(q.Value))
		}
		sends = "and sends the query " + strings.Join(qs, ", ") + "."
	}
	first := fmt.Sprintf("call %s (%s): %s. %s, %s", c.Name, c.Kind, c.Request.Line(), takes, sends)
	var second string
	if len(c.Build) > 0 {
		second = fmt.Sprintf("call %s builds its body with the command %s, run with the credential at each call and free to make its own requests with it while it builds", c.Name, showCommand(c.Build))
	} else if c.Request.Body != nil {
		raw, _ := json.Marshal(c.Request.Body)
		second = fmt.Sprintf("call %s sends the body %s", c.Name, raw)
	} else {
		second = fmt.Sprintf("call %s sends no body", c.Name)
	}
	if len(c.Answer.Select) > 0 {
		selects := make([]string, len(c.Answer.Select))
		for i, name := range c.Answer.Select {
			selects[i] = displaySourceValue(name)
		}
		second += fmt.Sprintf(". Its answer keeps %s", strings.Join(selects, ", "))
	} else {
		second += ". Its answer is returned as it comes"
	}
	second += fmt.Sprintf(", at most %d bytes", c.Answer.Max)
	return []string{first, second + "."}
}

// paramSays is one parameter with its type, whether it must be given, and
// every bound the file puts on it.
func paramSays(p source.Param) string {
	given := "required"
	if p.Optional {
		given = "optional"
	}
	s := fmt.Sprintf("%s (%s, %s", p.Name, p.Type, given)
	if p.Pattern != "" {
		s += ", pattern " + displaySourceValue(p.Pattern)
	}
	if p.Max > 0 {
		s += fmt.Sprintf(", at most %d", p.Max)
	}
	if len(p.Values) > 0 {
		values := make([]string, len(p.Values))
		for i, value := range p.Values {
			values[i] = displaySourceValue(value)
		}
		s += ", one of " + strings.Join(values, ", ")
	}
	if p.Log {
		s += ", its value is kept in the record of each call"
	}
	return s + ")"
}

func displaySourceValue(s string) string {
	if s == "" || strings.ContainsAny(s, " ,=\\\"\r\n\t") || strings.IndexFunc(s, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r) || unicode.Is(unicode.Bidi_Control, r)
	}) >= 0 {
		return strconv.Quote(s)
	}
	return s
}

// checkSays is the source's check call and idempotency header, or that it
// names neither.
func checkSays(d *source.Declaration) string {
	switch {
	case d.Check == "" && d.Idempotency == "":
		return "The source names no check call and no idempotency header."
	case d.Check == "":
		return fmt.Sprintf("The source names no check call; updates carry their key in the header %s.", d.Idempotency)
	case d.Idempotency == "":
		return fmt.Sprintf("Its check call is %s; it names no idempotency header.", d.Check)
	default:
		return fmt.Sprintf("Its check call is %s, and updates carry their key in the header %s.", d.Check, d.Idempotency)
	}
}

// limitsSays is the limits command whole with the kinds it answers for.
func limitsSays(d *source.Declaration) string {
	if d.Limits.Self() {
		return fmt.Sprintf("its limits command %s says what the source itself carries, asked by the source's name.", showCommand(d.Limits.Command))
	}
	kinds := d.Limits.KindNames()
	asked := make([]string, len(kinds))
	for i, k := range kinds {
		asked[i] = fmt.Sprintf("%s as %s", k, d.Limits.Kinds[k])
	}
	return fmt.Sprintf("its limits command %s says what the subscription of %s still carries.", showCommand(d.Limits.Command), strings.Join(asked, ", "))
}

// threadSays is how the source speaks in threads beside its commands: the
// place, the words state takes, labels and the assignee.
func threadSays(d *source.Declaration) string {
	t := d.Thread
	var parts []string
	if t.Place != "" {
		parts = append(parts, fmt.Sprintf("a place to open one is called %s", t.Place))
	} else {
		parts = append(parts, "it names no place to open one")
	}
	if len(t.States) > 0 {
		parts = append(parts, fmt.Sprintf("the words state takes are %s", strings.Join(t.States, ", ")))
	} else {
		parts = append(parts, "it names no state words")
	}
	if t.Labels {
		parts = append(parts, "it takes labels (label:<name> adds one, never removes)")
	} else {
		parts = append(parts, "it takes no labels")
	}
	if t.Assignee != "" {
		parts = append(parts, fmt.Sprintf("an assignee is a person's %s in the source", t.Assignee))
	} else {
		parts = append(parts, "it assigns nobody")
	}
	return "it speaks in threads: " + strings.Join(parts, "; ") + "."
}

// linkSays is the source's issues as a bead's link, whole.
func linkSays(d *source.Declaration) string {
	l := d.Link
	fold := "the groups keep their case"
	if l.Fold != "" {
		fold = fmt.Sprintf("the groups fold %s", l.Fold)
	}
	s := fmt.Sprintf("its issues are a bead's link: host %s, the expression %s reads an issue, the one fixed form %s and the thread id %s; %s.", l.Host, displaySourceValue(l.Issue), displaySourceValue(l.Fixed), displaySourceValue(l.Thread), fold)
	if l.Pull != nil {
		s += fmt.Sprintf(" Its pull requests read %s, with the fixed form %s and the thread id %s.", displaySourceValue(l.Pull.Issue), displaySourceValue(l.Pull.Fixed), displaySourceValue(l.Pull.Thread))
	} else {
		s += " It names no pull-request shape."
	}
	if l.Repos != "" {
		s += fmt.Sprintf(" A repository's path matches %s.", displaySourceValue(l.Repos))
	} else {
		s += " It keeps no repositories."
	}
	return s
}

// hookSays is what the source sends in: how a delivery is verified, where
// it is named, its bodies, its normalize command and its fields.
func hookSays(d *source.Declaration) string {
	h := d.Hook
	v := h.Verify
	var verified string
	if v.Scheme == source.HookSecret {
		verified = fmt.Sprintf("deliveries are verified by the header %s holding the hook's secret itself (scheme secret)", v.Signature.Header)
	} else {
		// The prefix and the template are the declaration's own text, and the
		// reader takes any literal text in them, a newline and an escape
		// too. They are quoted as the file's other values are, so the
		// sentence holds no control character (the hook-add card is stored
		// as built, with no filter over it) and shows what the author wrote,
		// where a filter would only drop it. The yard's own "none" and "the
		// body" stay bare.
		prefix := v.Signature.Prefix
		if prefix == "" {
			prefix = "none"
		} else {
			prefix = displaySourceValue(prefix)
		}
		// What the signature covers is the declaration's own word: the
		// body alone, or a template over timestamp and body together.
		signed := v.Signed
		var over string
		if signed == "" || signed == source.HookSigned {
			signed, over = source.HookSigned, "the body"
		} else {
			signed = displaySourceValue(signed)
			over = signed
		}
		arrives := fmt.Sprintf("as %s past the prefix %s", v.Signature.Encoding, prefix)
		if sep := v.Signature.List; sep != "" {
			// Several signatures may stand side by side, and one that
			// matches is enough: the operator approves that too.
			each := "with no prefix"
			if v.Signature.Prefix != "" {
				each = fmt.Sprintf("past the prefix %q", v.Signature.Prefix)
			}
			arrives = fmt.Sprintf("as %s, one or more separated by %q, each %s, any one of them passing", v.Signature.Encoding, sep, each)
		}
		// The hash is named as the sentence's HMAC and, where the declaration
		// picks another than the default SHA-256, among its own words, which
		// the operator approves with it. Left out and written as sha256 read
		// alike: they are one hash.
		hashed, words := strings.ToUpper(v.Hash()), "scheme "+source.HookScheme
		if v.Hash() != source.HookSHA256 {
			words += ", algorithm: " + v.Hash()
		}
		verified = fmt.Sprintf("deliveries are verified by HMAC-%s over %s (%s, signed: %s), the signature in the header %s %s", hashed, over, words, signed, v.Signature.Header, arrives)
		// A key the secret writes is not the secret's own bytes, which
		// the operator approves as well.
		if k := v.Key; k != nil {
			from := "the whole secret"
			if k.Prefix != "" {
				from = fmt.Sprintf("what follows the prefix %q in the secret", k.Prefix)
			}
			verified += fmt.Sprintf("; the HMAC key is %s, read as %s (not the secret's own bytes)", from, k.Encoding)
		}
	}
	// A dot path into the body is any text with something between its dots,
	// so it is quoted like the prefix; a header name is held to letters,
	// digits and hyphens by the reader and prints as it is.
	named := "it sends no delivery id"
	switch {
	case v.Delivery.Header != "":
		named = fmt.Sprintf("a delivery is named by the header %s", v.Delivery.Header)
	case v.Delivery.Body != "":
		named = fmt.Sprintf("a delivery is named by %s in its body", displaySourceValue(v.Delivery.Body))
	}
	timed := "it sends no timestamp"
	if v.Timestamp != nil {
		where := v.Timestamp.Body
		if where == "" {
			where = "the header " + v.Timestamp.Header
		} else {
			where = displaySourceValue(where)
		}
		timed = fmt.Sprintf("its time arrives at %s, in %s within %s of the yard's clock", where, v.Timestamp.Unit, v.Timestamp.Window)
	}
	keys := "its events carry no field of their own"
	if len(h.Keys) > 0 {
		keys = fmt.Sprintf("its events may carry the fields %s", strings.Join(h.Keys, ", "))
	}
	reg := ""
	if h.Register != nil {
		if c, ok := d.Call(h.Register.Call); ok {
			reg = fmt.Sprintf("; the yard registers its webhook with %s (%s), filling %s with the public URL", h.Register.Call, c.Request.Line(), h.Register.URL)
			if h.Register.Secret != "" {
				reg += fmt.Sprintf(" and %s with the hook's secret", h.Register.Secret)
			}
		}
	}
	return fmt.Sprintf("what it sends in: %s; %s; %s; bodies are %s; the command %s turns a verified body into the envelope, handed no credential; %s%s.", verified, named, timed, h.Bodies, showCommand(h.Normalize), keys, reg)
}

// eventsSays is what the source sends of pull requests and CI, with the
// commands that read them back whole.
func eventsSays(d *source.Declaration) string {
	e := d.Events
	var parts []string
	if len(e.Runs) > 0 {
		parts = append(parts, fmt.Sprintf("the envelope types %s are runs", strings.Join(e.Runs, ", ")))
	} else {
		parts = append(parts, "no envelope type is a run")
	}
	if len(e.HeadFrom) > 0 {
		parts = append(parts, fmt.Sprintf("%s tell a head", strings.Join(e.HeadFrom, ", ")))
	} else {
		parts = append(parts, "no envelope type tells a head")
	}
	if len(e.PR) > 0 {
		parts = append(parts, fmt.Sprintf("the command %s reads the pull request, handed the credential", showCommand(e.PR)))
	} else {
		parts = append(parts, "it names no pull-request read-back command")
	}
	if len(e.CI) > 0 {
		parts = append(parts, fmt.Sprintf("the command %s reads CI, handed the credential", showCommand(e.CI)))
	} else {
		parts = append(parts, "it names no CI read-back command")
	}
	return "what it sends of pull requests and CI: " + strings.Join(parts, "; ") + "."
}

// carriedSHA is the SHA-256 of the carried file prog, "" for none.
func carriedSHA(carried AskSource, prog string) string {
	for _, f := range carried.Files {
		if f.Program == prog {
			return f.SHA256
		}
	}
	return ""
}

// commandSaysChanged groups every command by the file the yard would run,
// with its whole command and its file's SHA-256 whole, or says the source
// names no command. A command is never named by its key alone. Where diffs
// names a file's change the file's line holds the change whole: its old and
// new hashes with its insertions and deletions counted.
func commandSaysChanged(d *source.Declaration, carried AskSource, diffs map[string]AskFileDiff) []string {
	commands := d.Commands()
	if len(commands) == 0 {
		return []string{"it names no command."}
	}
	hashes := map[string]string{}
	for _, f := range carried.Files {
		hashes[f.Program] = f.SHA256
	}
	// Group by program in the declaration's order of first use.
	var ordered []string
	byProgram := map[string][]source.Command{}
	for _, c := range commands {
		// The mint command is said whole on the credential's line, its
		// file and sha256 with it (credentialSays).
		if c.Key == source.MintCommandKey {
			continue
		}
		prog := c.Argv[0]
		if _, ok := byProgram[prog]; !ok {
			ordered = append(ordered, prog)
		}
		byProgram[prog] = append(byProgram[prog], c)
	}
	var out []string
	for _, prog := range ordered {
		group := byProgram[prog]
		if !group[0].Relative() {
			for _, c := range group {
				handed := "handed no credential"
				if commandGetsCredential(c.Key) {
					handed = "handed the credential"
				}
				out = append(out, fmt.Sprintf("command %s (%s): the program is the operator's own, %s.", showCommand(c.Argv), c.Key, handed))
			}
			continue
		}
		var named []string
		for _, c := range group {
			handed := "handed no credential"
			if commandGetsCredential(c.Key) {
				handed = "handed the credential"
			}
			named = append(named, fmt.Sprintf("%s (%s, %s)", showCommand(c.Argv), c.Key, handed))
		}
		if df, ok := diffs[prog]; ok {
			ins, del := Counts(df.Diff)
			out = append(out, fmt.Sprintf("command file %s: %s: sha256 %s -> sha256 %s, mode 0700 (%d insertions, %d deletions).",
				prog, andList(named), or(df.OldSHA256, "(none)"), or(df.NewSHA256, "(none)"), ins, del))
			continue
		}
		out = append(out, fmt.Sprintf("command file %s: %s: sha256 %s, mode 0700", prog, andList(named), hashes[prog]))
	}
	if len(out) == 0 {
		// The mint command alone, said on the credential's line.
		return []string{"it names no command but its mint command, said with its credential above."}
	}
	return out
}

// commandGetsCredential follows the runners: limits and hook.normalize see
// no credential; thread and events read-back commands, notify's image,
// and each call's build do.
func commandGetsCredential(key string) bool {
	switch key {
	case source.MintCommandKey:
		// The mint is handed the mint's inputs, not the token, but runs
		// with the operator's local logins it mints from: the card says
		// it is handed a credential, never that it is handed none.
		return true
	case "thread.reply", "thread.posted", "thread.read", "thread.file", "thread.open", "thread.opened", "thread.state", "notify.image", "events.pr", "events.ci":
		return true
	default:
		// A call's build runs on the call path with the call's
		// credential in its environment; call names hold no dots,
		// so the key is call.<name>.build and nothing else.
		return strings.HasPrefix(key, "call.") && strings.HasSuffix(key, ".build")
	}
}

func showCommand(argv []string) string {
	words := make([]string, len(argv))
	for i, word := range argv {
		words[i] = word
		if strings.ContainsAny(word, " ,[]\\\"\t\r\n") || strings.IndexFunc(word, func(r rune) bool {
			return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
		}) >= 0 {
			words[i] = strconv.Quote(word)
		}
	}
	return "[" + strings.Join(words, ", ") + "]"
}

// putCarriedSource stores the declaration text declares under d's name,
// as PutSource does inside its write (putSource), with the approval that
// asked for it on the event. It adds a name the yard does not have and
// replaces one it does, keeping the source's settings, as the ask was
// written: an ask for a free name refuses a name taken since, and one for
// a replacement refuses a source gone since, so neither is turned into
// the other.
func (w *wtx) putCarriedSource(ctx context.Context, d *source.Declaration, text, actor string, replace bool, by *approvedOn) (*Source, error) {
	if err := w.may(ctx, actor, ActAddSource, Object{Kind: KindSource, Name: d.Name, Ask: Askable}); err != nil {
		return nil, err
	}
	if standing := w.Bucket(bSources).Get([]byte(d.Name)) != nil; standing && !replace {
		return nil, fmt.Errorf("source %s is in the yard already", d.Name)
	} else if !standing && replace {
		return nil, fmt.Errorf("source %s is not in the yard any more", d.Name)
	}
	if err := hearSubscribeKeys(d); err != nil {
		return nil, err
	}
	return w.putSource(d, text, actor, by)
}
