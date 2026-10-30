package store

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// RepoFlowDir holds a depot's flows in its repository:
//
//	.yardr/flows/<name>.flow        a named flow; default.flow governs the depot
//	.yardr/flows/type/<type>.flow   governs the depot's beads of <type>
//
// They are read from the tip of the depot's base branch, never a bead's
// branch or worktree, so a bead cannot change its own lifecycle: an edit
// takes effect once it has been reviewed and landed on the base.
//
// A dir depot has no base: its flows are read from the directory as it is,
// <path>/.yardr/flows/, and reread when a file's mtime or size changes.
//
// Beside a flow file, a directory of its name holds the flow's stage files
// (StageFile), read from the same place as the flow:
//
//	.yardr/flows/<name>/<stage>.md        for <name>.flow
//	.yardr/flows/type/<type>/<stage>.md   for type/<type>.flow
//
// A flow in the store has its stage files in the yard's home instead
// (HomeStageFile); the repository is not asked for those.
const RepoFlowDir = ".yardr/flows"

// StageFile is the markdown definition of one stage of one flow: what the
// stage is for, what must be true to leave it, when each outcome applies.
// Whoever works a bead in the stage reads it, whichever group they are in.
// It is optional: a stage without one has only its name.
type StageFile struct {
	// Path is the file: in the depot's repository for a repository flow,
	// absolute in the yard's home for a flow in the store.
	Path string `json:"path"`
	Text string `json:"text"`
}

// StageFilePath is where the stage file of flow's stage lives in a depot's
// repository. flow is the flow's name: <name>, or type/<type> for a type's
// file. The file belongs to the flow, not to the stage name: review in
// default and review in yardr.train are two files.
func StageFilePath(flow, stage string) string {
	return RepoFlowDir + "/" + flow + "/" + stage + ".md"
}

// stageFile reports whether p may be a stage file: markdown in a directory
// under RepoFlowDir. Which flow and stage it belongs to is not read off the
// path, since stage names may hold a slash: a flow asks for its stages' files
// by path (StageFile), and StrayStageFiles names the files no flow asks for.
func stageFile(p string) bool {
	rel, ok := strings.CutPrefix(p, RepoFlowDir+"/")
	return ok && strings.HasSuffix(rel, ".md") && strings.Contains(rel, "/")
}

// RepoFlow is one flow file in a depot's repository.
type RepoFlow struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"` // .yardr/flows/<name>.flow
	Type string `json:"type,omitempty"` // .yardr/flows/type/<type>.flow
	Flow *Flow  `json:"flow,omitempty"`
	// Error says why the file is not a flow. Resolving to it fails rather
	// than leave the beads it would govern ungoverned.
	Error string `json:"error,omitempty"`
}

func (rf *RepoFlow) flow() (*Flow, error) {
	if rf.Error != "" {
		return nil, fmt.Errorf("%s: %s", rf.Path, rf.Error)
	}
	return rf.Flow, nil
}

// RepoFlows is what a depot's repository says about flows at the tip of its
// base branch. Commit is empty when the depot has no path or its base does
// not exist in the checkout: then the repository has no flows. Any other
// git failure (the path is not a checkout, git is missing) is an error, so
// a broken checkout does not silently leave its beads without their flow.
type RepoFlows struct {
	Depot  string `json:"depot"`
	Base   string `json:"base"`
	Commit string `json:"commit,omitempty"`
	// Dir is the directory a dir depot's flows were read from; then Base and
	// Commit are empty.
	Dir   string     `json:"dir,omitempty"`
	Flows []RepoFlow `json:"flows"`
	// StageFiles are the markdown files in the flows' directories, read
	// with the flows.
	StageFiles []StageFile `json:"stage_files,omitempty"`
	// stamp is what a dir depot's cached flows and stage files were read at:
	// each file's path, mtime and size.
	stamp string
}

// where says where the flows were read: "at <base>", or "in <dir>".
func (r *RepoFlows) where() string {
	if r.Dir != "" {
		return "in " + r.Dir
	}
	return "at " + r.Base
}

// find returns the type's file when typ is set, else the named one. A name
// that ends in wagon is looked for under car too, after the one asked for
// (flowNames).
func (r *RepoFlows) find(name, typ string) *RepoFlow {
	if typ != "" {
		for i := range r.Flows {
			if rf := &r.Flows[i]; rf.Type == typ {
				return rf
			}
		}
		return nil
	}
	for _, w := range flowNames(name) {
		for i := range r.Flows {
			if rf := &r.Flows[i]; rf.Type == "" && rf.Name == w {
				return rf
			}
		}
	}
	return nil
}

// StageFile is the stage file of the stage of the flow named flow, or nil
// when the repository has none. A file with nothing in it says nothing
// about the stage, and counts as none.
func (r *RepoFlows) StageFile(flow, stage string) *StageFile {
	want := StageFilePath(flow, stage)
	for i := range r.StageFiles {
		if sf := &r.StageFiles[i]; sf.Path == want && strings.TrimSpace(sf.Text) != "" {
			return sf
		}
	}
	return nil
}

// StrayStageFiles names the stage files in r that belong to no stage, each
// as "<path>: <why>": a file in a flow's directory named after no stage of
// that flow, or in a directory with no flow file beside it. Nobody would be
// shown such a file, so it is a typo to report. A flow file that does not
// parse cannot say what its stages are, so the files beside it are left
// alone.
func (r *RepoFlows) StrayStageFiles() []string {
	flows := map[string]*Flow{} // by name; nil for a file that is no flow
	for _, rf := range r.Flows {
		flows[strings.TrimSuffix(strings.TrimPrefix(rf.Path, RepoFlowDir+"/"), ".flow")] = rf.Flow
	}
	var out []string
	for _, sf := range r.StageFiles {
		rel := strings.TrimSuffix(strings.TrimPrefix(sf.Path, RepoFlowDir+"/"), ".md")
		// The flow is the longest directory prefix that names one, so
		// type/train/review is review of type/train.flow before it is
		// train/review of a flow called type.
		name, stage, found := "", "", false
		for i := len(rel) - 1; i > 0; i-- {
			if rel[i] != '/' {
				continue
			}
			if _, ok := flows[rel[:i]]; ok {
				name, stage, found = rel[:i], rel[i+1:], true
				break
			}
		}
		switch f := flows[name]; {
		case !found && path.Dir(rel) == "type":
			// Beside the type flows, in no flow's directory: a README.
		case !found:
			out = append(out, fmt.Sprintf("%s: there is no %s/%s.flow for it to belong to (a flow in the store has its stage files in the yard's home, <home>/flows/<name>/<stage>.md)", sf.Path, RepoFlowDir, path.Dir(rel)))
		case f == nil:
		case !slices.Contains(f.Stages, stage):
			out = append(out, fmt.Sprintf("%s: flow %s has no stage %s; stages: %s", sf.Path, name, stage, strings.Join(f.Stages, ", ")))
		}
	}
	return out
}

// HomeStageDir is where the stage files of the store flow named flow live:
// plain files in the yard's home, <home>/flows/<name>/<stage>.md, the way
// role prompts outside a depot are files under the home. The store holds no
// prose. Every depot following the flow reads the same files, and they are in
// no depot, so no bead's branch can change them.
func HomeStageDir(home, flow string) string { return filepath.Join(FlowsDir(home), flow) }

// HomeStageFile reads the stage file of a store flow's stage from the
// yard's home, as it is on disk; nil when there is none, or nothing in it.
func HomeStageFile(home, flow, stage string) (*StageFile, error) {
	rel := filepath.FromSlash(stage) + ".md"
	// A stage name may hold dots and slashes: it must not lead out of the
	// flow's directory.
	if home == "" || !validFlowName.MatchString(flow) || !filepath.IsLocal(rel) {
		return nil, nil
	}
	p := filepath.Join(HomeStageDir(home, flow), rel)
	text, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) || (err == nil && strings.TrimSpace(string(text)) == "") {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stage file of %s in flow %s: %w", stage, flow, err)
	}
	return &StageFile{Path: p, Text: string(text)}, nil
}

// HomeStageFiles looks at the stage files of store flow f in the yard's
// home: have maps each stage that has one to its path, and stray names, as
// "<path>: <why>", the markdown files in the flow's directory named after
// no stage of it, which nobody would be shown.
func HomeStageFiles(home string, f *Flow) (have map[string]string, stray []string, err error) {
	have = map[string]string{}
	if home == "" || !validFlowName.MatchString(f.Name) {
		return have, nil, nil
	}
	for _, st := range f.Stages {
		sf, err := HomeStageFile(home, f.Name, st)
		if err != nil {
			return nil, nil, err
		}
		if sf != nil {
			have[st] = sf.Path
		}
	}
	dir := HomeStageDir(home, f.Name)
	err = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil || e.IsDir() || filepath.Ext(p) != ".md" {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if stage := strings.TrimSuffix(filepath.ToSlash(rel), ".md"); !slices.Contains(f.Stages, stage) {
			stray = append(stray, fmt.Sprintf("%s: flow %s has no stage %s; stages: %s", p, f.Name, stage, strings.Join(f.Stages, ", ")))
		}
		return nil
	})
	return have, stray, err
}

// repoCache keeps each depot checkout's flows by base commit, so resolving a
// flow costs one rev-parse until the base moves, and each dir depot's by its
// files' mtimes and sizes, so it costs a listing until a file changes. The
// flows' stage files are kept with them.
type repoCache struct {
	mu sync.Mutex
	m  map[string]*RepoFlows // <path>\x00<base>, or <path>\x00\x00dir
}

// RepoFlows reads depot's flows from the tip of its base branch, or a dir
// depot's from its directory.
func (s *Store) RepoFlows(ctx context.Context, depot string) (*RepoFlows, error) {
	r, err := s.GetDepot(ctx, depot)
	if err != nil {
		return nil, err
	}
	return s.repos.load(ctx, *r)
}

func (c *repoCache) load(ctx context.Context, r Depot) (*RepoFlows, error) {
	if r.IsDir() {
		return c.loadDir(r)
	}
	base := or(r.Base, "origin/main")
	out := &RepoFlows{Depot: r.Name, Base: base, Flows: []RepoFlow{}}
	if r.Path == "" {
		return out, nil // git -C "" would read whatever the server runs in
	}
	sha, err := ResolveRef(ctx, r.Path, base+"^{commit}")
	if errors.Is(err, errGitNotFound) {
		return out, nil
	} else if err != nil {
		return nil, fmt.Errorf("depot %s: resolving base %s in %s: %w", r.Name, base, r.Path, err)
	}
	out.Commit = strings.TrimSpace(string(sha))
	key := join(r.Path, base)
	c.mu.Lock()
	hit := c.m[key]
	c.mu.Unlock()
	if hit != nil && hit.Commit == out.Commit {
		cp := *hit // shared and read only; the depot's name may differ
		cp.Depot = r.Name
		return &cp, nil
	}
	list, err := git(ctx, r.Path, "ls-tree", "-r", "-z", "--name-only", out.Commit, "--", RepoFlowDir+"/")
	if err != nil {
		return nil, fmt.Errorf("depot %s: listing %s at %s: %w", r.Name, RepoFlowDir, base, err)
	}
	var paths []string
	for _, p := range strings.Split(string(list), "\x00") {
		if _, isFlow := flowFile(p); isFlow || stageFile(p) {
			paths = append(paths, p)
		}
	}
	texts, err := catBlobs(ctx, r.Path, out.Commit, paths)
	if err != nil {
		return nil, fmt.Errorf("depot %s: reading %s at %s: %w", r.Name, errBlob(err), base, errUnwrapBlob(err))
	}
	for i, p := range paths {
		if rf, isFlow := flowFile(p); isFlow {
			out.Flows = append(out.Flows, rf.parse(texts[i]))
		} else {
			out.StageFiles = append(out.StageFiles, StageFile{Path: p, Text: string(texts[i])})
		}
	}
	c.put(key, out)
	cp := *out
	return &cp, nil
}

// A blobErr is which path a batched blob read failed on, and why.
type blobErr struct {
	path string
	err  error
}

func (e *blobErr) Error() string { return e.path + ": " + e.err.Error() }

// errBlob is the path a batched blob read failed on; errUnwrapBlob why.
func errBlob(err error) string {
	var b *blobErr
	if errors.As(err, &b) {
		return b.path
	}
	return ""
}

func errUnwrapBlob(err error) error {
	var b *blobErr
	if errors.As(err, &b) {
		return b.err
	}
	return err
}

// catBlobs reads the blobs paths at commit in one `git cat-file --batch`,
// in position, instead of starting git per file. A path the commit does
// not have cannot happen (the paths were just listed at it), and fails its
// read as git's missing would the one git started for it.
func catBlobs(ctx context.Context, dir, commit string, paths []string) ([][]byte, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "cat-file", "--batch")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, &blobErr{paths[0], err}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, &blobErr{paths[0], err}
	}
	if err := cmd.Start(); err != nil {
		return nil, &blobErr{paths[0], err}
	}
	out := bufio.NewReader(stdout)
	texts := make([][]byte, len(paths))
	for i, p := range paths {
		name := commit + ":" + p
		io.WriteString(stdin, name+"\n")
		line, err := out.ReadString('\n')
		if err != nil {
			stdin.Close()
			cmd.Wait()
			return nil, &blobErr{p, fmt.Errorf("git cat-file --batch: %s: %w: %s", name, err, strings.TrimSpace(errb.String()))}
		}
		var oid, kind string
		var size int64
		if head, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), name+" "); ok && (head == "missing" || head == "ambiguous") {
			stdin.Close()
			cmd.Wait()
			return nil, &blobErr{p, fmt.Errorf("git cat-file --batch: %s missing", name)}
		}
		if n, _ := fmt.Sscanf(line, "%s %s %d\n", &oid, &kind, &size); n != 3 || size < 0 || kind != "blob" {
			stdin.Close()
			cmd.Wait()
			return nil, &blobErr{p, fmt.Errorf("git cat-file --batch: unexpected answer %q", strings.TrimSpace(line))}
		}
		body := make([]byte, size+1)
		if _, err := io.ReadFull(out, body); err != nil {
			stdin.Close()
			cmd.Wait()
			return nil, &blobErr{p, fmt.Errorf("git cat-file --batch: %s: %w: %s", name, err, strings.TrimSpace(errb.String()))}
		}
		texts[i] = body[:size]
	}
	stdin.Close()
	if err := cmd.Wait(); err != nil {
		return nil, &blobErr{paths[len(paths)-1], fmt.Errorf("git cat-file --batch: %w: %s", err, strings.TrimSpace(errb.String()))}
	}
	return texts, nil
}

// loadDir reads a dir depot's flows and stage files from <path>/.yardr/flows/
// as they are on disk. The cache holds them until one is added, removed, or
// its mtime or size changes; a missing directory is no flows.
func (c *repoCache) loadDir(r Depot) (*RepoFlows, error) {
	out := &RepoFlows{Depot: r.Name, Dir: r.Path, Flows: []RepoFlow{}}
	if r.Path == "" {
		return out, nil
	}
	var files []string // flow and stage files, by their path in the depot
	var stamp strings.Builder
	// A directory's files before its directories: the named flows, then
	// type/, then each flow's stage files.
	var list func(sub string) error
	list = func(sub string) error {
		ents, err := os.ReadDir(filepath.Join(r.Path, filepath.FromSlash(RepoFlowDir+"/"+sub)))
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("depot %s: listing %s in %s: %w", r.Name, RepoFlowDir, r.Path, err)
		}
		for _, e := range ents {
			p := RepoFlowDir + "/" + sub + e.Name()
			if _, ok := flowFile(p); (!ok && !stageFile(p)) || e.IsDir() {
				continue
			}
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed since the listing
			} else if err != nil {
				return fmt.Errorf("depot %s: %s: %w", r.Name, p, err)
			}
			fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00", p, info.ModTime().UnixNano(), info.Size())
			files = append(files, p)
		}
		for _, e := range ents {
			if e.IsDir() {
				if err := list(sub + e.Name() + "/"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := list(""); err != nil {
		return nil, err
	}
	out.stamp = stamp.String()
	key := join(r.Path, "\x00dir")
	c.mu.Lock()
	hit := c.m[key]
	c.mu.Unlock()
	if hit != nil && hit.stamp == out.stamp {
		cp := *hit
		cp.Depot = r.Name
		return &cp, nil
	}
	for _, p := range files {
		text, err := os.ReadFile(filepath.Join(r.Path, filepath.FromSlash(p)))
		if err != nil {
			return nil, fmt.Errorf("depot %s: reading %s in %s: %w", r.Name, p, r.Path, err)
		}
		if rf, ok := flowFile(p); ok {
			out.Flows = append(out.Flows, rf.parse(text))
		} else {
			out.StageFiles = append(out.StageFiles, StageFile{Path: p, Text: string(text)})
		}
	}
	c.put(key, out)
	cp := *out
	return &cp, nil
}

func (c *repoCache) put(key string, rf *RepoFlows) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*RepoFlows{}
	}
	c.m[key] = rf
	c.mu.Unlock()
}

// flowFile places p, a path under RepoFlowDir: a named flow or a type's.
// Anything else is not a place yardr reads flows from.
func flowFile(p string) (RepoFlow, bool) {
	rel, ok := strings.CutPrefix(p, RepoFlowDir+"/")
	if !ok || !strings.HasSuffix(rel, ".flow") {
		return RepoFlow{}, false
	}
	rf := RepoFlow{Path: p}
	dir, file := path.Split(strings.TrimSuffix(rel, ".flow"))
	switch dir {
	case "":
		rf.Name = file
	case "type/":
		rf.Type = file
	default:
		return RepoFlow{}, false
	}
	return rf, true
}

// ReadFlow reads a flow file's text as the flow called name, checked and
// with its stages in order as a saved flow has them: what a flow file in a
// repository is read as, and what a command reads one from disk with.
func ReadFlow(name, text string) (*Flow, error) {
	f, err := ParseFlow(text)
	if err != nil {
		return nil, err
	}
	f.Name = name
	if err := f.normalize(); err != nil {
		return nil, err
	}
	return f, nil
}

// parse fills rf from the file's text, or says why it is not a flow.
func (rf RepoFlow) parse(text []byte) RepoFlow {
	f, err := ReadFlow(strings.TrimSuffix(strings.TrimPrefix(rf.Path, RepoFlowDir+"/"), ".flow"), string(text))
	if err != nil {
		rf.Error = err.Error()
	} else {
		rf.Flow = f
	}
	return rf
}

// errGitNotFound is a git command that exited 1 without a word on stderr:
// how `rev-parse --verify --quiet` says the revision does not exist.
var errGitNotFound = errors.New("git: not found")

// ResolveRef is what `git rev-parse --verify --quiet` prints for ref in the
// repository at dir: its commit, with the newline git prints. A plain branch
// tip kept loose is read from its file, which is microseconds where starting
// git is tens of milliseconds; a resolve runs on nearly every store call on
// a git depot, so the process is the cost. Anything but a loose branch tip
// runs git as ever: a tag (which ^{commit} peels), a packed ref, HEAD, a
// worktree or bare repository, no checkout at all, and a name git would not
// take. What comes back is git's in every case: the same bytes, the same
// errors, errGitNotFound for a revision that does not exist.
func ResolveRef(ctx context.Context, dir, ref string) ([]byte, error) {
	if sha, ok := looseRef(dir, ref); ok {
		return []byte(sha + "\n"), nil
	}
	return git(ctx, dir, "rev-parse", "--verify", "--quiet", ref)
}

// looseRef is the commit ref names in the repository at dir, read from its
// loose ref file. ok is false where the answer is not a branch tip read
// loose, and the caller runs git. The read is current as git's own: loose
// refs are replaced atomically, so what is read is the tip as it is, or was
// a moment ago, exactly as a resolve that ran git beside it would say.
func looseRef(dir, ref string) (sha string, ok bool) {
	// Heads and remote tips are commits, so ^{commit} changes nothing for
	// them; it is cut for the name's check alone.
	name, _ := strings.CutSuffix(ref, "^{commit}")
	if !safeRefName(name) {
		return "", false
	}
	// These variables make git read refs from another directory or
	// namespace. In that case the checkout's loose files are not its answer.
	for _, key := range []string{"GIT_DIR", "GIT_COMMON_DIR", "GIT_NAMESPACE"} {
		if os.Getenv(key) != "" {
			return "", false
		}
	}
	// A qualified branch or remote names its file; a bare name is looked
	// for in git's own order. Anything else that resolves first (HEAD, a
	// tag) is git's to answer, since only git peels it.
	if name == "HEAD" {
		return "", false
	}
	var cands []string
	switch {
	case strings.HasPrefix(name, "refs/heads/"), strings.HasPrefix(name, "refs/remotes/"):
		cands = []string{name}
	case strings.HasPrefix(name, "refs/"):
		return "", false
	default:
		cands = []string{"refs/heads/" + name, "refs/remotes/" + name}
	}
	gitdir := filepath.Join(dir, ".git")
	if st, err := os.Stat(gitdir); err != nil || !st.IsDir() {
		return "", false
	}
	// A bare name resolves through what stands in front of the branches
	// first: HEAD, an exact ref, a tag. Where one does, git answers.
	if len(cands) == 2 {
		for _, p := range []string{filepath.Join(gitdir, name), filepath.Join(gitdir, "refs", name), filepath.Join(gitdir, "refs", "tags", name)} {
			if _, err := os.Stat(p); err == nil {
				return "", false
			} else if !os.IsNotExist(err) {
				return "", false
			}
		}
		if packedRefCouldAffect(gitdir, name) {
			return "", false
		}
	}
	for _, cand := range cands {
		raw, err := os.ReadFile(filepath.Join(gitdir, cand))
		if err != nil {
			if !os.IsNotExist(err) {
				return "", false
			}
			continue
		}
		sha := strings.TrimSpace(string(raw))
		if !isHexSHA(sha) {
			return "", false
		}
		return sha, true
	}
	return "", false
}

// packedRefCouldAffect reports whether a packed ref could change how git
// resolves a bare name. Git checks packed refs in the same DWIM order as
// loose refs, so even a packed tag can shadow the loose branch we would
// otherwise read. An unreadable packed-refs file also leaves the answer to
// git.
func packedRefCouldAffect(gitdir, name string) bool {
	data, err := os.ReadFile(filepath.Join(gitdir, "packed-refs"))
	if err != nil {
		return !os.IsNotExist(err)
	}
	want := map[string]bool{
		"refs/" + name:                   true,
		"refs/tags/" + name:              true,
		"refs/heads/" + name:             true,
		"refs/remotes/" + name:           true,
		"refs/remotes/" + name + "/HEAD": true,
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if len(line) == 0 || line[0] == '#' || line[0] == '^' {
			continue
		}
		_, ref, ok := strings.Cut(string(line), " ")
		if ok && want[ref] {
			return true
		}
	}
	return false
}

// safeRefName reports whether name may stand in a ref path read by hand: no
// escape to another file, nothing git reads as a revision operator, no
// option where the ref is meant, and no component git would refuse (one
// starting or ending with a dot, or a lock file left by a write). Anything
// else runs git, which refuses it as ever.
func safeRefName(name string) bool {
	if name == "" || strings.HasPrefix(name, "-") || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.Contains(name, "..") || strings.Contains(name, "//") ||
		strings.ContainsAny(name, "~^:?*[\\@") {
		return false
	}
	for _, c := range strings.Split(name, "/") {
		if c == "" || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".") || strings.HasSuffix(c, ".lock") {
			return false
		}
	}
	return true
}

// isHexSHA reports whether s is a commit id as a loose ref file holds one:
// forty lowercase hex digits, or sixty-four where the repository hashes so.
func isHexSHA(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !('0' <= s[i] && s[i] <= '9' || 'a' <= s[i] && s[i] <= 'f') {
			return false
		}
	}
	return true
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		var exit *exec.ExitError
		if msg == "" && errors.As(err, &exit) && exit.ExitCode() == 1 && ctx.Err() == nil {
			return nil, fmt.Errorf("git %s: %w", args[0], errGitNotFound)
		}
		if msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}
