package runner

import (
	"regexp"
	"strings"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

var nameUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Branch is the git branch a bead is worked on (see store.Branch).
func Branch(b store.Bead) string { return store.Branch(b.ID) }

// BaseOf is what the job's bead branches from and lands on: its train's
// branch for a child of a train, the commit a linked yard named for a bead
// that came from it (store.Family), else the depot's base.
func BaseOf(job dispatch.Job) string {
	if job.Base != "" || job.Depot == nil {
		return job.Base
	}
	return job.Depot.Base
}

// AgentName derives a herdr agent name ([a-z][a-z0-9_-]{0,31}) from the bead
// and stage, so `herdr agent list` reads like the yard.
func AgentName(bead, stage string) string { return agentName(bead + "-" + stage) }

// CrewAgentName is the herdr agent name of a yard's crew member,
// "<yard>-<crew member>": herdr lists every yard on the machine's agents
// together and refuses a second of one name, so two yards' yardmasters must not
// both be "yardmaster". Without a yard name it is the crew member's.
func CrewAgentName(yard, member string) string { return agentName(yard + "-" + member) }

// CrewLabel is the herdr label of a crew member's workspace, "<yard>
// <crew member>", for the same reason.
func CrewLabel(yard, member string) string {
	return strings.TrimSpace(yard + " " + member)
}

// CrewConversationAgentName is the herdr agent name of a crew member's
// conversation session for user: the line session's with the user, so the
// two sessions are two agents.
func CrewConversationAgentName(yard, member, user string) string {
	return agentName(yard + "-" + member + "-" + user)
}

// CrewConversationLabel is the herdr label of its workspace, "<yard>
// <crew member> for <user>".
func CrewConversationLabel(yard, member, user string) string {
	return strings.TrimSpace(yard + " " + member + " for " + user)
}

// agentName is raw as a herdr agent name.
func agentName(raw string) string {
	n := nameUnsafe.ReplaceAllString(strings.ToLower(raw), "-")
	n = strings.Trim(n, "-")
	if n == "" || n[0] < 'a' || n[0] > 'z' {
		n = "y" + n
	}
	if len(n) > 32 {
		n = strings.TrimRight(n[:32], "-")
	}
	return n
}

// WorkspaceLabel is the herdr label for a bead's workspace: the group
// working on it first, then "[<train>] " for a child of a train, so the tab
// bar shows which train it belongs to, then the bead ("builders [sv6b] 2cet",
// or "builders jtyz" for a bead with no train). The workspace is reused
// across stages and relabelled on each reopen, so the group in it is always
// the current one.
func WorkspaceLabel(depot string, group store.Group, train, bead string) string {
	return joinLabel(GroupLabel(depot, group), depot, train, bead)
}

// TabLabel is the herdr label for a stage's tab: the workspace's label with
// the stage in place of the group ("review [sv6b] 2cet"). The group is not
// repeated, since the workspace above the tab names it.
func TabLabel(depot, stage, train, bead string) string {
	return joinLabel(stage, depot, train, bead)
}

// GroupLabel is the name a group goes by in herdr labels: its `label`
// config when set, else its name. herdr already nests workspaces under the
// depot's repo, so a leading "<depot>-" on the name is noise that only gets the
// label truncated in the sidebar.
func GroupLabel(depot string, group store.Group) string {
	if l := str(group.Config, "label", ""); l != "" {
		return l
	}
	return ShortID(depot, group.Name)
}

// ShortID drops the leading "<depot>-" from an id for a herdr label, where
// the depot is already the node above. An id without it stays whole; ids
// anywhere else in yardr stay full.
func ShortID(depot, id string) string {
	if depot == "" {
		return id
	}
	return strings.TrimPrefix(id, depot+"-")
}

// joinLabel is "<head> [<train>] <bead>", the parts that are empty left out
// and the ids shortened.
func joinLabel(head, depot, train, bead string) string {
	var parts []string
	if head != "" {
		parts = append(parts, head)
	}
	if train != "" {
		parts = append(parts, "["+ShortID(depot, train)+"]")
	}
	return strings.Join(append(parts, ShortID(depot, bead)), " ")
}
