package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// RepoFlowDir holds a rig's flows in its repository:
//
//	.yardr/flows/<name>.flow        a named flow; default.flow governs the rig
//	.yardr/flows/type/<type>.flow   governs the rig's beads of <type>
//
// They are read from the tip of the rig's base branch, never a bead's
// branch or worktree, so a bead cannot change its own lifecycle: an edit
// takes effect once it has been reviewed and landed on the base.
//
// A dir rig has no base: its flows are read from the directory as it is,
// <path>/.yardr/flows/, and reread when a file's mtime or size changes.
const RepoFlowDir = ".yardr/flows"

// RepoFlow is one flow file in a rig's repository.
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

// RepoFlows is what a rig's repository says about flows at the tip of its
// base branch. Commit is empty when the rig has no path or its base does
// not exist in the checkout: then the repository has no flows. Any other
// git failure (the path is not a checkout, git is missing) is an error, so
// a broken checkout does not silently leave its beads without their flow.
type RepoFlows struct {
	Rig    string `json:"rig"`
	Base   string `json:"base"`
	Commit string `json:"commit,omitempty"`
	// Dir is the directory a dir rig's flows were read from; then Base and
	// Commit are empty.
	Dir   string     `json:"dir,omitempty"`
	Flows []RepoFlow `json:"flows"`
	// stamp is what a dir rig's cached flows were read at: each file's
	// path, mtime and size.
	stamp string
}

// where says where the flows were read: "at <base>", or "in <dir>".
func (r *RepoFlows) where() string {
	if r.Dir != "" {
		return "in " + r.Dir
	}
	return "at " + r.Base
}

// find returns the type's file when typ is set, else the named one.
func (r *RepoFlows) find(name, typ string) *RepoFlow {
	for i := range r.Flows {
		rf := &r.Flows[i]
		if (typ != "" && rf.Type == typ) || (typ == "" && rf.Type == "" && rf.Name == name) {
			return rf
		}
	}
	return nil
}

// repoCache keeps each rig checkout's flows by base commit, so resolving a
// flow costs one rev-parse until the base moves, and each dir rig's by its
// files' mtimes and sizes, so it costs a listing until a file changes.
type repoCache struct {
	mu sync.Mutex
	m  map[string]*RepoFlows // <path>\x00<base>, or <path>\x00\x00dir
}

// RepoFlows reads rig's flows from the tip of its base branch, or a dir
// rig's from its directory.
func (s *Store) RepoFlows(ctx context.Context, rig string) (*RepoFlows, error) {
	r, err := s.GetRig(ctx, rig)
	if err != nil {
		return nil, err
	}
	return s.repos.load(ctx, *r)
}

func (c *repoCache) load(ctx context.Context, r Rig) (*RepoFlows, error) {
	if r.IsDir() {
		return c.loadDir(r)
	}
	base := or(r.Base, "origin/main")
	out := &RepoFlows{Rig: r.Name, Base: base, Flows: []RepoFlow{}}
	if r.Path == "" {
		return out, nil // git -C "" would read whatever the server runs in
	}
	sha, err := git(ctx, r.Path, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if errors.Is(err, errGitNotFound) {
		return out, nil
	} else if err != nil {
		return nil, fmt.Errorf("rig %s: resolving base %s in %s: %w", r.Name, base, r.Path, err)
	}
	out.Commit = strings.TrimSpace(string(sha))
	key := join(r.Path, base)
	c.mu.Lock()
	hit := c.m[key]
	c.mu.Unlock()
	if hit != nil && hit.Commit == out.Commit {
		cp := *hit // shared and read only; the rig's name may differ
		cp.Rig = r.Name
		return &cp, nil
	}
	list, err := git(ctx, r.Path, "ls-tree", "-r", "-z", "--name-only", out.Commit, "--", RepoFlowDir+"/")
	if err != nil {
		return nil, fmt.Errorf("rig %s: listing %s at %s: %w", r.Name, RepoFlowDir, base, err)
	}
	for _, p := range strings.Split(string(list), "\x00") {
		rf, ok := flowFile(p)
		if !ok {
			continue
		}
		text, err := git(ctx, r.Path, "cat-file", "blob", out.Commit+":"+p)
		if err != nil {
			return nil, fmt.Errorf("rig %s: reading %s at %s: %w", r.Name, p, base, err)
		}
		out.Flows = append(out.Flows, rf.parse(text))
	}
	c.put(key, out)
	cp := *out
	return &cp, nil
}

// loadDir reads a dir rig's flows from <path>/.yardr/flows/ as they are on
// disk. The cache holds them until a flow file is added, removed, or its
// mtime or size changes; a missing directory is no flows.
func (c *repoCache) loadDir(r Rig) (*RepoFlows, error) {
	out := &RepoFlows{Rig: r.Name, Dir: r.Path, Flows: []RepoFlow{}}
	if r.Path == "" {
		return out, nil
	}
	var files []RepoFlow
	var stamp strings.Builder
	for _, sub := range []string{"", "type/"} {
		ents, err := os.ReadDir(filepath.Join(r.Path, filepath.FromSlash(RepoFlowDir+"/"+sub)))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, fmt.Errorf("rig %s: listing %s in %s: %w", r.Name, RepoFlowDir, r.Path, err)
		}
		for _, e := range ents {
			rf, ok := flowFile(RepoFlowDir + "/" + sub + e.Name())
			if !ok || e.IsDir() {
				continue
			}
			info, err := e.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed since the listing
			} else if err != nil {
				return nil, fmt.Errorf("rig %s: %s: %w", r.Name, rf.Path, err)
			}
			fmt.Fprintf(&stamp, "%s\x00%d\x00%d\x00", rf.Path, info.ModTime().UnixNano(), info.Size())
			files = append(files, rf)
		}
	}
	out.stamp = stamp.String()
	key := join(r.Path, "\x00dir")
	c.mu.Lock()
	hit := c.m[key]
	c.mu.Unlock()
	if hit != nil && hit.stamp == out.stamp {
		cp := *hit
		cp.Rig = r.Name
		return &cp, nil
	}
	for _, rf := range files {
		text, err := os.ReadFile(filepath.Join(r.Path, filepath.FromSlash(rf.Path)))
		if err != nil {
			return nil, fmt.Errorf("rig %s: reading %s in %s: %w", r.Name, rf.Path, r.Path, err)
		}
		out.Flows = append(out.Flows, rf.parse(text))
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

// parse fills rf from the file's text, or says why it is not a flow.
func (rf RepoFlow) parse(text []byte) RepoFlow {
	f, err := ParseFlow(string(text))
	if err == nil {
		f.Name = strings.TrimSuffix(strings.TrimPrefix(rf.Path, RepoFlowDir+"/"), ".flow")
		err = f.normalize()
	}
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
