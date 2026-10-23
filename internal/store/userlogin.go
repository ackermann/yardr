package store

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// A person with a login of their own is made of records that name them
// (docs/groups.md, "A person with a login of their own"): a group whose
// login_for is the user, the routes that take their beads or send work to
// that group, and the crew members that serve them a conversation. yardr
// user add --login makes them, and removing the user takes them in the same
// write: a route left behind would send beads for the name to a group whose
// login belongs to no one, and a name is reusable, so the first's login must
// not be the second's.

// UserLogin is what carries a user's own login in the yard, by name and id.
type UserLogin struct {
	// Groups are the groups whose login_for is the user.
	Groups []string `json:"groups,omitempty"`
	// EnvFiles are the env_file of those groups, each once: where the
	// login's folder is named. They stay on disk when the user goes.
	EnvFiles []string `json:"env_files,omitempty"`
	// Routes are the ids of the routes that take the user's beads (their
	// originator) or name one of those groups.
	Routes []string `json:"routes,omitempty"`
	// Crew are the crew members that serve the user (for=) or keep a
	// conversation session for them (env_for_<user>).
	Crew []string `json:"crew,omitempty"`
}

// Empty reports whether the yard holds nothing of the user's login.
func (l UserLogin) Empty() bool {
	return len(l.Groups) == 0 && len(l.Routes) == 0 && len(l.Crew) == 0
}

// LoginOf reads what carries user's own login off the yard's groups, routes
// and crew: the same sets removing the user takes (deleteUser). Each list is
// sorted.
func LoginOf(user string, groups []Group, routes []Route, crew []CrewMember) UserLogin {
	var l UserLogin
	for _, g := range groups {
		if g.LoginFor() != user {
			continue
		}
		l.Groups = append(l.Groups, g.Name)
		if file, _ := g.Config["env_file"].(string); file != "" && !slices.Contains(l.EnvFiles, file) {
			l.EnvFiles = append(l.EnvFiles, file)
		}
	}
	for _, r := range routes {
		if (r.Originator != nil && *r.Originator == user) || slices.ContainsFunc(l.Groups, r.Names) {
			l.Routes = append(l.Routes, r.ID)
		}
	}
	for _, c := range crew {
		if slices.Contains(c.Served(), user) || hasEnvFor(c.Config, user) {
			l.Crew = append(l.Crew, c.Name)
		}
	}
	sort.Strings(l.Groups)
	sort.Strings(l.EnvFiles)
	sort.Strings(l.Routes)
	sort.Strings(l.Crew)
	return l
}

// Said is what removing user takes of their own login, in sentences, and
// what stays: the card of user rm shows them before and yardr user rm after,
// so that both say it in the same words. Nothing for a user with no login of
// their own.
func (l UserLogin) Said(user string) []string {
	var out []string
	if len(l.Groups) > 0 {
		out = append(out, "Groups that carry their login go with them: "+strings.Join(l.Groups, ", ")+".")
	}
	if len(l.Routes) > 0 {
		out = append(out, fmt.Sprintf("Their routes go with them (%s).", countNoun(len(l.Routes), "route")))
	}
	if len(l.Crew) > 0 {
		out = append(out, fmt.Sprintf("They are taken out of for= and env_for_%s of %s.", user, strings.Join(l.Crew, ", ")))
	}
	if len(l.EnvFiles) > 0 {
		out = append(out, "Their login stays on disk, in the folder the env file "+strings.Join(l.EnvFiles, ", ")+" names; no command erases it yet.")
	}
	return out
}

func hasEnvFor(cfg map[string]any, user string) bool {
	_, ok := cfg[EnvForPrefix+user]
	return ok
}

// SignInMeta is the metadata key of the bead a person's own sign-in stands on
// (yardr user add --login, yardr user login); its value is the harness kind
// being signed in. The command makes the bead before it serves the person's
// conversation and closes it when the sign-in ends, whatever the end, so the
// bead is open for exactly as long as the sign-in is. That is the one trace
// of the sign-in the daemon can read: the command is a process of its own,
// and the code it asks for comes and goes with it.
const SignInMeta = "sign_in"

// SignInWithin is how long a sign-in can be open: the login ends within its
// own timeouts (a quarter of an hour, docs/doctor.md) and the one call that
// proves it takes seconds. A bead open for longer is what a command killed
// outright left, and holds nothing back.
const SignInWithin = time.Hour

// SigningIn reports whether beads hold an open sign-in of user to the harness
// kind at now: their own login is being made, by the person, and no one else
// can log in a session that sits at its login screen on it.
func SigningIn(beads []Bead, user, kind string, now time.Time) bool {
	return slices.ContainsFunc(beads, func(b Bead) bool {
		of, _ := b.Metadata[SignInMeta].(string)
		return b.open() && b.Originator == user && of == kind && now.Sub(b.CreatedAt) < SignInWithin
	})
}

// userLogin is LoginOf over what the transaction holds.
func (w *wtx) userLogin(user string) (UserLogin, error) {
	var groups []Group
	if err := each(w.Tx, bGroups, "", func(_ []byte, g *Group) error {
		groups = append(groups, *g)
		return nil
	}); err != nil {
		return UserLogin{}, err
	}
	var routes []Route
	if err := each(w.Tx, bRoutes, "", func(_ []byte, r *Route) error {
		routes = append(routes, *r)
		return nil
	}); err != nil {
		return UserLogin{}, err
	}
	var crew []CrewMember
	if err := each(w.Tx, bCrew, "", func(_ []byte, c *CrewMember) error {
		crew = append(crew, *c)
		return nil
	}); err != nil {
		return UserLogin{}, err
	}
	return LoginOf(user, groups, routes, crew), nil
}

// dropLogin takes what carries user's own login out of the yard, in the
// caller's write: the groups (through deleteGroup, so their live sessions are
// released and the routes naming them go), the routes that take the user's
// beads wherever they lead (a subsidy route is theirs too), and the user from
// each crew member's for= and its env_for_<user>. The login folder and its
// env file are files, and stay: they hold the person's transcripts, and no
// command erases them yet. It answers what it took.
func (w *wtx) dropLogin(user, actor string) (UserLogin, error) {
	l, err := w.userLogin(user)
	if err != nil || l.Empty() {
		return l, err
	}
	for _, g := range l.Groups {
		if _, _, err := w.deleteGroup(g, actor); err != nil {
			return l, err
		}
	}
	// Deleting a key that deleteGroup took with its group is no error.
	for _, id := range l.Routes {
		if err := w.Bucket(bRoutes).Delete([]byte(id)); err != nil {
			return l, err
		}
	}
	for _, name := range l.Crew {
		var c CrewMember
		if ok, err := get(w.Tx, bCrew, name, &c); err != nil {
			return l, err
		} else if !ok {
			continue
		}
		unserve(c.Config, user)
		c.Revision = newRevision()
		if err := put(w.Tx, bCrew, name, c); err != nil {
			return l, err
		}
	}
	return l, nil
}

// unserve takes user out of a crew config: from its for= list, which keeps
// its form (a string of commas or a list), and its env_for_<user> key. A list
// the removal empties is dropped whole: an empty for= serves everyone, which
// is what a crew member with none named does.
func unserve(cfg map[string]any, user string) {
	delete(cfg, EnvForPrefix+user)
	rest := slices.DeleteFunc(servedList(cfg), func(u string) bool { return u == user })
	switch cfg[CrewForKey].(type) {
	case nil:
		return
	case string:
		if len(rest) == 0 {
			delete(cfg, CrewForKey)
			return
		}
		cfg[CrewForKey] = strings.Join(rest, ",")
	default:
		if len(rest) == 0 {
			delete(cfg, CrewForKey)
			return
		}
		list := make([]any, len(rest))
		for i, u := range rest {
			list[i] = u
		}
		cfg[CrewForKey] = list
	}
}
