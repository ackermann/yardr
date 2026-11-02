// The crew add row of the run list (runList): a new crew member, proposed
// on a card and added only on the operator's approval. The member's kind,
// model, permission preset and directory ride argv; its role text rides
// beside argv (AskRun.Role), never as a path, so the card shows exactly
// what will run and a file changed after the ask changes nothing. What
// defines what the yard runs beyond that — env, free args, grants — stays
// out: env is the operator's alone, free args are replaced by the kind's
// named permission preset, and grants are proposed apart as crew set rows.
package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// crewAddUse is how a refusal names the crew add row.
const crewAddUse = "yardr crew add <name> kind=<kind> model=<model> preset=<preset> dir=<dir>"

// crewAddSpec is a crew add proposal as the yard holds it: the member to
// add, with the preset already resolved to its flags.
type crewAddSpec struct {
	name       string
	kind       string
	model      string
	preset     string // the preset's label as its file writes it; "" is none
	presetArgs []string
	presetNote string
	dir        string // absolute; "" in argv means the yard's home
	role       string
}

// parseCrewAdd reads the words of crew add <name> with its settings. It
// reads no record of the yard. The settings ride as key=value words, or
// as the terminal's --set key=value and --dir <dir>; prompt=<file> names
// where the role text was read from when the ask was written and is not
// kept: the text itself rides beside argv, and approval always writes it
// to roles/<name>.md. What this row never carries refuses here, in words
// that say where it goes instead, so no check downstream can miss it.
func parseCrewAdd(rest []string) ([]string, error) {
	if len(rest) == 0 {
		return nil, fmt.Errorf("--run takes %s: it names no crew member", crewAddUse)
	}
	name := rest[0]
	if strings.Contains(name, "=") || strings.HasPrefix(name, "-") {
		return nil, fmt.Errorf("--run takes %s: %q is no crew member name", crewAddUse, name)
	}
	sets := map[string]string{}
	set := func(kv string) error {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("%q: want key=value", kv)
		}
		if _, dup := sets[k]; dup {
			return fmt.Errorf("%s is set twice", k)
		}
		switch k {
		case "kind", "model", "preset", "dir", "prompt":
			sets[k] = v
		case "env_file", "env_command":
			return fmt.Errorf("--run takes %s: %s stays the operator's alone, typed in a terminal and never proposed as a run ask: env is the operator's alone", crewAddUse, k)
		case "args":
			return fmt.Errorf("--run takes %s: free args are not proposed on this card: name the kind's permission preset as preset=<preset> instead, as yardr harness show <kind> lists them", crewAddUse)
		case OpenThreads, SetState, MailPeers:
			return fmt.Errorf("--run takes %s: %s is a grant, proposed apart as yardr crew set <name> %s=<values> once the member stands, never with the add that defines it", crewAddUse, k, k)
		default:
			return fmt.Errorf("--run takes %s: %s is no setting of this card, and is set directly", crewAddUse, k)
		}
		return nil
	}
	for i := 1; i < len(rest); i++ {
		word := rest[i]
		switch {
		case word == "--set" && i+1 < len(rest):
			i++
			if err := set(rest[i]); err != nil {
				return nil, err
			}
		case strings.HasPrefix(word, "--set="):
			if err := set(strings.TrimPrefix(word, "--set=")); err != nil {
				return nil, err
			}
		case word == "--dir" && i+1 < len(rest):
			i++
			if _, dup := sets["dir"]; dup {
				return nil, fmt.Errorf("dir is set twice")
			}
			sets["dir"] = rest[i]
		case strings.HasPrefix(word, "--dir="):
			if _, dup := sets["dir"]; dup {
				return nil, fmt.Errorf("dir is set twice")
			}
			sets["dir"] = strings.TrimPrefix(word, "--dir=")
		case strings.HasPrefix(word, "-"):
			return nil, fmt.Errorf("--run takes %s, and crew add with %s is the operator's to type in a terminal", crewAddUse, strings.SplitN(word, "=", 2)[0])
		default:
			if err := set(word); err != nil {
				return nil, err
			}
		}
	}
	// The yard's own form, in the order the use line names: prompt goes,
	// since the text beside argv is what runs, never the path it was read
	// from.
	out := []string{name}
	for _, k := range []string{"kind", "model", "preset", "dir"} {
		if v, ok := sets[k]; ok {
			out = append(out, k+"="+v)
		}
	}
	return out, nil
}

// crewAddOf reads a stored crew add argv: the member, then each setting.
func crewAddOf(argv []string) (name, kind, model, preset, dir string) {
	name = argv[2]
	for _, kv := range argv[3:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "kind":
			kind = v
		case "model":
			model = v
		case "preset":
			preset = v
		case "dir":
			dir = v
		}
	}
	return name, kind, model, preset, dir
}

// crewAddTerminal is the terminal command a refusal sends the operator
// to: the same member, added by hand, with the role written to a file
// the operator names.
func crewAddTerminal(name, kind, model, preset, dir string) string {
	parts := []string{"yardr crew add " + name}
	if kind != "" {
		parts = append(parts, "--set kind="+kind)
	}
	if model != "" {
		parts = append(parts, "--set model="+model)
	}
	if dir != "" {
		parts = append(parts, "--dir "+dir)
	}
	return strings.Join(parts, " ")
}

func crewAddPresetArgs(preset string) string {
	if preset == "" || PresetLabel(preset) == "none" {
		return "no preset flags"
	}
	return "the " + preset + " preset's flags"
}

// crewAddProposal holds a crew add ask to the yard as it stands: the
// member is new, the kind is one the yard knows, the preset is that
// kind's own, the directory is under a place the yard knows, and the
// carried role text fits a card. It answers the proposal resolved, with
// the preset's flags and the directory absolute.
func (w *wtx) crewAddProposal(run AskRun) (crewAddSpec, error) {
	name, kind, model, preset, dir := crewAddOf(run.Argv)
	if err := checkUserName(name); err != nil {
		return crewAddSpec{}, err
	}
	if filepath.Clean(name) != name {
		return crewAddSpec{}, fmt.Errorf("crew member %s: that name would not give its role a unique path in the yard's roles directory: choose a name with no empty or dot path parts", name)
	}
	if strings.Contains(name, "..") {
		return crewAddSpec{}, fmt.Errorf("crew member %s: that name would write its role outside the yard's roles directory: choose another", name)
	}
	switch {
	case w.Bucket(bCrew).Get([]byte(name)) != nil:
		return crewAddSpec{}, fmt.Errorf("%w: there is a crew member %s already: this adds, it never replaces; change it with yardr crew set %s, or pick another name", ErrConflict, name, name)
	case w.Bucket(bUsers).Get([]byte(name)) != nil:
		return crewAddSpec{}, fmt.Errorf("crew member %s: that is a user's name, and mail to a name reaches one party: choose another", name)
	case w.Bucket(bGroups).Get([]byte(name)) != nil:
		return crewAddSpec{}, fmt.Errorf("crew member %s: that is a group's name, and one name is one party: choose another", name)
	}
	crewNames := w.Bucket(bCrew).Cursor()
	for key, _ := crewNames.First(); key != nil; key, _ = crewNames.Next() {
		if strings.EqualFold(string(key), name) {
			return crewAddSpec{}, fmt.Errorf("crew member %s: that name would share its role path with crew member %s on a case-insensitive filesystem: choose another", name, key)
		}
	}
	if kind == "" {
		return crewAddSpec{}, fmt.Errorf("--run takes %s: it names no kind: name the harness kind as kind=<kind>, as yardr harness list names them", crewAddUse)
	}
	if _, ok := HarnessLabels[kind]; !ok {
		have := mapsKeys(HarnessLabels)
		slices.Sort(have)
		return crewAddSpec{}, fmt.Errorf("crew member %s: kind %q is no harness kind the yard knows (it knows: %s): name one as kind=<kind>", name, kind, strings.Join(have, ", "))
	}
	// The model goes to the agent as the value of its model flag: a word
	// that reads as a flag of its own is a free arg by another name.
	if strings.HasPrefix(model, "-") {
		return crewAddSpec{}, fmt.Errorf("crew member %s: %q is no model name: free args are not proposed on this card", name, model)
	}
	spec := crewAddSpec{name: name, kind: kind, model: model, role: run.Role}
	if preset != "" && PresetLabel(preset) != "none" {
		p, ok := HarnessPreset(kind, preset)
		if !ok {
			have := HarnessPresetLabels(kind)
			shown := "none"
			if len(have) > 0 {
				shown = strings.Join(have, ", ") + ", or none"
			}
			return crewAddSpec{}, fmt.Errorf("crew member %s: %s has no permission preset %q (it has: %s): name one as preset=<preset> in its one-word form (a dash for a space), as yardr harness show %s lists them, or preset=none for no preset", name, kind, preset, shown, kind)
		}
		spec.preset, spec.presetArgs, spec.presetNote = p.Label, p.Args, p.Note
	}
	// The role text rides beside argv, whole. Past the bound it is the
	// operator's to add in a terminal, where the file is named directly.
	role := run.Role
	switch {
	case strings.TrimSpace(role) == "":
		return crewAddSpec{}, fmt.Errorf("crew add %s carries no role text: the role goes beside --run, in --run-role \"<file>\", or as prompt=<file> whose text the ask carries; --run takes the member and its kind, model, preset and dir alone", name)
	case len(role) > MaxRunCrewRole:
		return crewAddSpec{}, fmt.Errorf("crew add %s: the role text it carries is %d bytes, past the %d a card holds and shows whole: write it to a file and type %s in a terminal, with --set prompt=<that file> and %s as its args", name, len(role), MaxRunCrewRole, crewAddTerminal(name, kind, model, preset, dir), crewAddPresetArgs(preset))
	case !utf8.ValidString(role):
		return crewAddSpec{}, fmt.Errorf("crew add %s: the role text is not valid UTF-8, so a card cannot carry it whole", name)
	case strings.IndexFunc(role, crewRoleHidden) >= 0 || strings.Contains(role, NoteFilesMark):
		return crewAddSpec{}, fmt.Errorf("crew add %s: the role text holds control characters or the yard's files mark, which a card would not show as written: write it to a file and type %s in a terminal, with --set prompt=<that file> and %s as its args", name, crewAddTerminal(name, kind, model, preset, dir), crewAddPresetArgs(preset))
	}
	// Another member's or group's prompt may be the file that stands here,
	// under a name no record holds, and it runs as written: a card that
	// adds must not rewrite what others read.
	disk, _, err := crewRolePath(w.home, name)
	if err != nil {
		return crewAddSpec{}, err
	}
	if _, err := os.Lstat(disk); !os.IsNotExist(err) {
		return crewAddSpec{}, fmt.Errorf("crew member %s: roles/%s.md stands already, and may be another's prompt: this adds, it never replaces: choose another name", name, name)
	}
	spec.role = role
	if dir == "" {
		spec.dir = w.home
		return spec, nil
	}
	if !filepath.IsAbs(dir) {
		return crewAddSpec{}, fmt.Errorf("crew member %s: dir %q is no absolute directory under the yard's known places (the yard's home, a depot, or under one of them): name one as dir=<dir>", name, dir)
	}
	clean := filepath.Clean(dir)
	info, err := os.Stat(clean)
	if err != nil || !info.IsDir() {
		return crewAddSpec{}, fmt.Errorf("crew member %s: dir %s is no directory under the yard's known places (the yard's home, a depot, or under one of them): name one as dir=<dir>", name, clean)
	}
	clean, err = filepath.EvalSymlinks(clean)
	if err != nil {
		return crewAddSpec{}, fmt.Errorf("crew member %s: resolve dir %s: %w", name, dir, err)
	}
	clean = filepath.Clean(clean)
	if !w.crewDirKnown(clean) {
		return crewAddSpec{}, fmt.Errorf("crew member %s: dir %s is outside the yard's known places (the yard's home, a depot, or under one of them): name one as dir=<dir>", name, clean)
	}
	spec.dir = clean
	return spec, nil
}

// mapsKeys is the keys of m, for a refusal that lists them.
func mapsKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// crewDirKnown says whether dir is the yard's home, a depot, or under
// one of them, or the home's parent itself, where a yardmaster works:
// the places a crew member may work that the yard already knows. Under
// that parent is every directory the operator has, so it is no place of
// the yard's: any other absolute directory is the operator's to name in
// a terminal, never on a card an agent proposed.
func (w *wtx) crewDirKnown(dir string) bool {
	places := []string{w.home}
	var paths []string
	_ = each(w.Tx, bDepots, "", func(_ []byte, d *Depot) error {
		if d.Path != "" {
			paths = append(paths, filepath.Clean(d.Path))
		}
		return nil
	})
	places = append(places, paths...)
	if parent, err := filepath.EvalSymlinks(filepath.Dir(w.home)); err == nil && dir == filepath.Clean(parent) {
		return true
	}
	for _, p := range places {
		if p == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(p)
		if err != nil {
			continue
		}
		p = filepath.Clean(resolved)
		if dir == p || strings.HasPrefix(dir, p+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// crewRoleHidden says whether r would be drawn other than as written:
// the role's lines are the card's, and what a card leaves out or reorders
// is no longer what the operator saw.
func crewRoleHidden(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) || r == '\u2028' || r == '\u2029'
}

// presetSkipsPermissions says whether a permission preset runs without
// asking: its flags or its own description say so. The card says it in
// plain words where it does, since approval then starts an agent that
// never stops to ask.
func presetSkipsPermissions(p Permission) bool {
	note := strings.ToLower(p.Note)
	for _, s := range []string{"never asks", "without asking", "asks nothing", "no approval", "auto-approve"} {
		if strings.Contains(note, s) {
			return true
		}
	}
	for _, a := range p.Args {
		switch a {
		case "--approve-for-me", "--dangerously-skip-permissions", "--dangerously-bypass-approvals-and-sandbox", "--yolo", "--always-approve", "--trust-all-tools", "--disable-approval", "--auto":
			return true
		}
		if strings.HasPrefix(a, "--trust-tools") {
			return true
		}
	}
	return false
}

// checkCrewAdd is adding a crew member as the asker: defining what the
// yard runs is the operator's, which a run ask may propose. The proposal
// itself is held first, so a kind, preset, directory or role the yard
// would refuse is refused in its own words whoever asks.
func checkCrewAdd(ctx context.Context, w *wtx, run AskRun, actor string) error {
	spec, err := w.crewAddProposal(run)
	if err != nil {
		return err
	}
	return w.may(ctx, actor, ActChangeCrew, Object{Kind: KindCrew, Name: spec.name, Does: "add crew member " + spec.name, Ask: Askable})
}

// crewAddCard is the card of crew add <name>: the member, its kind,
// model, preset and directory, and its whole role text, each as the
// yard holds them. Asking refuses a member, kind, preset, directory or
// role the yard would refuse, and a name in use already, since this
// adds and never replaces; an approval reads it again and stale says
// what moved. The card holds the resolved preset in Seen; a member added
// since is refused by the proposal itself.
func (w *wtx) crewAddCard(run AskRun, asking bool) (AskRun, error) {
	spec, err := w.crewAddProposal(run)
	if err != nil {
		return AskRun{}, err
	}
	_ = asking
	label := HarnessLabels[spec.kind]
	model := spec.model
	if model == "" {
		model = "the agent's own default"
	}
	does := []string{
		fmt.Sprintf("adds the crew member %s on %s (%s) with model %s, working in %s.", spec.name, spec.kind, label, model, spec.dir),
	}
	seenMap := map[string]string{}
	if spec.preset == "" {
		does = append(does, "Permissions: none — the agent asks as usual.")
	} else {
		args := make([]string, len(spec.presetArgs))
		for i, arg := range spec.presetArgs {
			args[i] = strconv.Quote(arg)
		}
		shownArgs := "no arguments"
		if len(args) > 0 {
			shownArgs = strings.Join(args, " ")
		}
		does = append(does, fmt.Sprintf("Permissions: %s — passes %s; %s.", spec.preset, shownArgs, spec.presetNote))
		if presetSkipsPermissions(Permission{Label: spec.preset, Args: spec.presetArgs, Note: spec.presetNote}) {
			does = append(does, "It skips permissions: it runs without asking for permission.")
		}
		seenMap["permission preset "+spec.kind+" "+PresetLabel(spec.preset)] = seen(spec.preset, spec.presetArgs, spec.presetNote)
	}
	does = append(does, fmt.Sprintf("Its role, written to roles/%s.md on approval, is:", spec.name))
	lines := strings.Split(strings.TrimSuffix(spec.role, "\n"), "\n")
	does = append(does, lines...)
	return AskRun{
		Argv:   run.Argv,
		Does:   does,
		Back:   runCommandLine([]string{"crew", "rm", spec.name}),
		BackBy: "only the operator",
		Seen:   seenMap,
		Role:   spec.role,
	}, nil
}

// crewAddStale says why the ask seq for crew add is approved no more:
// the yard would not write its card as it stands now.
func (w *wtx) crewAddStale(seq uint64, was, now AskRun) error {
	return staleCard(seq, was, now)
}

// crewRolePath is where approval writes name's role text, and the
// prompt it sets for it: the yard's roles directory, by the member's
// name. The path must stay inside it, which checkCrewAdd's name check
// and this together hold.
func crewRolePath(home, name string) (disk, prompt string, err error) {
	home, err = filepath.Abs(home)
	if err != nil {
		return "", "", fmt.Errorf("crew member %s: resolve its role path: %w", name, err)
	}
	roles := filepath.Join(home, "roles")
	disk = filepath.Join(roles, name+".md")
	prompt = disk
	clean := filepath.Clean(disk)
	if clean != roles && !strings.HasPrefix(clean, roles+string(filepath.Separator)) {
		return "", "", fmt.Errorf("crew member %s: that name would write its role outside the yard's roles directory: choose another", name)
	}
	return clean, prompt, nil
}

// writeCrewRole puts the carried text at a path that holds nothing,
// whole or not at all: the link refuses whatever arrived there since the
// proposal was read, a symlink included, so nothing is replaced or
// followed. A member can read only the role the operator approved, and
// the role stays private.
func writeCrewRole(home, disk, role string) error {
	dir := filepath.Dir(disk)
	roles, err := filepath.Abs(filepath.Join(home, "roles"))
	if err != nil {
		return err
	}
	relPath, err := filepath.Rel(roles, disk)
	if err != nil || relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) || filepath.IsAbs(relPath) {
		return fmt.Errorf("%s is outside the yard's roles directory", disk)
	}
	if err := os.MkdirAll(roles, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(roles)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is not a directory", roles)
	}
	rel, err := filepath.Rel(roles, dir)
	if err != nil {
		return err
	}
	current := roles
	if rel != "." {
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			current = filepath.Join(current, part)
			if err := os.Mkdir(current, 0o700); err != nil && !os.IsExist(err) {
				return err
			}
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is not a directory", current)
			}
		}
	}
	f, err := os.CreateTemp(dir, ".yardr-role-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if _, err := f.Write([]byte(role)); err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Link(tmp, disk)
}

// runCrewAdd adds the crew member as actor: it writes the carried role
// text, byte for byte, to the yard's roles directory, and saves the
// member through putCrew with its prompt there, its preset's flags as
// its args, and no grant. The daemon starts an enabled member, as it
// starts one added in a terminal.
func runCrewAdd(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error) {
	spec, err := w.crewAddProposal(run)
	if err != nil {
		return "", err
	}
	disk, prompt, err := crewRolePath(w.home, spec.name)
	if err != nil {
		return "", err
	}
	config := map[string]any{"kind": spec.kind, "prompt": prompt}
	if spec.model != "" {
		config["model"] = spec.model
	}
	if len(spec.presetArgs) > 0 {
		args := make([]any, len(spec.presetArgs))
		for i, a := range spec.presetArgs {
			args[i] = a
		}
		config["args"] = args
	}
	def := CrewMember{
		Name: spec.name, Runner: "herdr", Dir: spec.dir,
		On: []string{KindAttention}, HandoffGrace: "15m", Config: config,
	}
	r, err := w.putCrew(ctx, def, actor, by)
	if err != nil {
		return "", err
	}
	if err := writeCrewRole(w.home, disk, spec.role); err != nil {
		return "", fmt.Errorf("crew member %s: write its role to %s: %w", spec.name, disk, err)
	}
	return fmt.Sprintf("crew member %s added; the daemon starts it (journal %s)", r.Name, r.Journal), nil
}
