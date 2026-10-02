package store

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
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
// base branch. Commit is empty when the rig's path is not a checkout or
// its base does not resolve there: then the repository has no flows.
type RepoFlows struct {
	Rig    string     `json:"rig"`
	Base   string     `json:"base"`
	Commit string     `json:"commit,omitempty"`
	Flows  []RepoFlow `json:"flows"`
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
// flow costs one rev-parse until the base moves.
type repoCache struct {
	mu sync.Mutex
	m  map[string]*RepoFlows // <path>\x00<base>
}

// RepoFlows reads rig's flows from the tip of its base branch.
func (s *Store) RepoFlows(ctx context.Context, rig string) (*RepoFlows, error) {
	r, err := s.GetRig(ctx, rig)
	if err != nil {
		return nil, err
	}
	return s.repos.load(ctx, *r)
}

func (c *repoCache) load(ctx context.Context, r Rig) (*RepoFlows, error) {
	base := or(r.Base, "origin/main")
	out := &RepoFlows{Rig: r.Name, Base: base, Flows: []RepoFlow{}}
	sha, err := git(ctx, r.Path, "rev-parse", "--verify", "--quiet", base+"^{commit}")
	if err != nil {
		return out, nil
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
		rel, ok := strings.CutPrefix(p, RepoFlowDir+"/")
		if !ok || !strings.HasSuffix(rel, ".flow") {
			continue
		}
		rf := RepoFlow{Path: p}
		dir, file := path.Split(strings.TrimSuffix(rel, ".flow"))
		switch dir {
		case "":
			rf.Name = file
		case "type/":
			rf.Type = file
		default:
			continue // not a place yardr reads flows from
		}
		text, err := git(ctx, r.Path, "cat-file", "blob", out.Commit+":"+p)
		if err != nil {
			return nil, fmt.Errorf("rig %s: reading %s at %s: %w", r.Name, p, base, err)
		}
		f, err := ParseFlow(string(text))
		if err == nil {
			f.Name = strings.TrimSuffix(rel, ".flow")
			err = f.normalize()
		}
		if err != nil {
			rf.Error = err.Error()
		} else {
			rf.Flow = f
		}
		out.Flows = append(out.Flows, rf)
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*RepoFlows{}
	}
	c.m[key] = out
	c.mu.Unlock()
	cp := *out
	return &cp, nil
}

func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("git %s: %w: %s", args[0], err, msg)
		}
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}
