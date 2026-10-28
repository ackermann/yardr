// The teammate rows of the run list: an invite and a removal that each
// do the yard user, the web_jwt_users mapping and the edge member report
// as one approved operation (design yardr-hosted-d3wc sections 5-7).
// Neither is a CLI verb: the card is the only way in, and its approval is
// carried out by the yard's server after the approval's write (carry.go),
// because the edge report cannot be one write of the store. The cards
// refuse what would leave the three writes disagreeing before any of
// them is made.
package store

import (
	"context"
	"fmt"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// userInviteUse is how a refusal names the invite a run ask takes.
const userInviteUse = "yardr user invite <name> --email <address> [--role operator]"

// parseUserInvite reads the words of user invite <name> --email
// <address> [--role operator]. The address is one word, and member is
// the role it leaves out: an explicit member stores as none given, so
// two ways of typing one invite are one command.
func parseUserInvite(rest []string) ([]string, error) {
	args, values, err := runWords(userInviteUse, "user invite", "the operator's", rest, map[string]bool{"--email": true, "--role": true})
	if err != nil {
		return nil, err
	}
	switch {
	case len(args) != 1:
		return nil, fmt.Errorf("--run takes %s: one user, not %d words", userInviteUse, len(args))
	case len(values["--email"]) != 1:
		return nil, fmt.Errorf("--run takes %s: one --email, not %d", userInviteUse, len(values["--email"]))
	case len(values["--role"]) > 1:
		return nil, fmt.Errorf("--run takes %s: --role once, not %d times", userInviteUse, len(values["--role"]))
	}
	email := strings.ToLower(values["--email"][0])
	if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t\n\r") {
		return nil, fmt.Errorf("--run takes %s: %q is no address (an address has an @)", userInviteUse, values["--email"][0])
	}
	out := []string{args[0], "--email", email}
	if len(values["--role"]) == 1 {
		switch values["--role"][0] {
		case RoleOperator:
			out = append(out, "--role", RoleOperator)
		case RoleMember:
		default:
			return nil, fmt.Errorf("--run takes %s: a role is %s or %s, not %q", userInviteUse, RoleMember, RoleOperator, values["--role"][0])
		}
	}
	return out, nil
}

// userInviteOf is the name, the address and the role of a parsed invite,
// the role a member's where the command names none.
func userInviteOf(argv []string) (name, email, role string) {
	name, role = argv[2], RoleMember
	for i := 3; i+1 < len(argv); i += 2 {
		switch argv[i] {
		case "--email":
			email = argv[i+1]
		case "--role":
			role = argv[i+1]
		}
	}
	return name, email, role
}

// checkUserInvite is inviting a teammate's own check as the asker: it
// adds a user, as user add's check is.
func checkUserInvite(ctx context.Context, w *wtx, argv []string, actor string) error {
	return w.may(ctx, actor, ActAddUser, Object{Kind: KindUser, Name: argv[2], Ask: Askable})
}

// settingValue is the value stored for key, "" where it is unset.
func (w *wtx) settingValue(key string) string {
	var v string
	_, _ = get(w.Tx, bSettings, key, &v)
	return v
}

// webJWTUsers is the standing web_jwt_users mapping and its stored
// value: an empty mapping and "" where the setting is unset.
func (w *wtx) webJWTUsers() (map[string]string, string, error) {
	raw := w.settingValue(WebJWTUsersSetting)
	if raw == "" {
		return map[string]string{}, raw, nil
	}
	m, err := ParseWebJWTUsers(raw)
	return m, raw, err
}

// formatWebJWTUsers is a web_jwt_users mapping as the setting holds it:
// email=user pairs in address order, separated by commas.
func formatWebJWTUsers(m map[string]string) string {
	emails := make([]string, 0, len(m))
	for email := range m {
		emails = append(emails, email)
	}
	slices.Sort(emails)
	pairs := make([]string, 0, len(emails))
	for _, email := range emails {
		pairs = append(pairs, email+"="+m[email])
	}
	return strings.Join(pairs, ",")
}

// mappedEmails are the addresses the mapping maps to user, in order.
func mappedEmails(m map[string]string, user string) []string {
	var out []string
	for email, u := range m {
		if u == user {
			out = append(out, email)
		}
	}
	slices.Sort(out)
	return out
}

// userInviteCard is the card of user invite: who would be made, as what,
// and where it is reported. It refuses, before any write, a name a user
// or a crew member holds (userAddCard's words), an address the mapping
// already maps to anyone (never re-pointed), and a yard with no edge to
// report to.
func (w *wtx) userInviteCard(argv []string, asking bool) (AskRun, error) {
	name, email, role := userInviteOf(argv)
	if err := checkUserName(name); err != nil {
		return AskRun{}, err
	}
	if name == OriginatorAny {
		return AskRun{}, fmt.Errorf("user %s: that is a route keyword, not a user, and mail to a name reaches one party: choose another", name)
	}
	switch {
	case w.Bucket(bUsers).Get([]byte(name)) != nil:
		return AskRun{}, fmt.Errorf("%w: there is a user %s already", ErrConflict, name)
	case w.Bucket(bCrew).Get([]byte(name)) != nil:
		return AskRun{}, fmt.Errorf("user %s: that is a crew member's name, and mail to a name reaches one party: choose another", name)
	}
	mapping, raw, err := w.webJWTUsers()
	if err != nil {
		return AskRun{}, fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
	}
	if other, taken := mapping[strings.ToLower(email)]; taken {
		return AskRun{}, fmt.Errorf("email %s is mapped to %s in %s already, and an invite never re-points it: remove that mapping's user first if %s is to act as %s", email, other, WebJWTUsersSetting, email, name)
	}
	edge, connected, err := EdgeOf(map[string]string{EdgeSetting: w.settingValue(EdgeSetting)})
	if err != nil {
		return AskRun{}, err
	}
	if !connected {
		return AskRun{}, fmt.Errorf("this yard is connected to no edge (the %s setting names none), so an invite has nowhere to report the membership: connect the yard to its edge first (yardr edge connect)", EdgeSetting)
	}
	does := []string{
		fmt.Sprintf("adds the user %s with the role %s, and maps %s to %s in %s: signed in at the edge as %s, they act as %s.", name, role, email, name, WebJWTUsersSetting, email, name),
		fmt.Sprintf("reports the invite to the edge for the yard %s/%s (member add): until the report lands the card reads as not yet at the edge, and nobody is admitted meanwhile.", edge.Handle, edge.Name),
		"they set a password from the mail the edge sends to this email, then sign in; nothing the operator holds admits them.",
	}
	if role == RoleOperator {
		does = append(does, fmt.Sprintf("the user %s can approve and administer this yard: they hold the operator role, not only a member's.", name))
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine([]string{"user", "uninvite", name}),
		BackBy: "only the operator",
		Seen: map[string]string{
			"user " + name:  seen("no user", name),
			"web_jwt_users": seen(raw),
			"edge":          seen(edge.Handle, edge.Name, edge.URL),
		},
	}, nil
}

// userInviteStale says why an invite approval is refused: the mapping or
// the edge it was carded against stands otherwise now. A name taken
// since is refused by the card itself, before this is read.
func (w *wtx) userInviteStale(seq uint64, was, now AskRun) error {
	name, email, _ := userInviteOf(was.Argv)
	if was.Seen["web_jwt_users"] != now.Seen["web_jwt_users"] {
		return fmt.Errorf("ask %d was written when %s did not map %s to %s: the mapping was changed since. %s", seq, WebJWTUsersSetting, email, name, nothingRan)
	}
	if was.Seen["edge"] != now.Seen["edge"] {
		return fmt.Errorf("ask %d was written for another edge than the one this yard is connected to now: the edge it reports to changed since. %s", seq, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runUserInvite records an invite approval as to be carried out: the
// user, the mapping and the edge report are the server's, after this
// write, and nothing of them is written here.
func runUserInvite(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	if err := w.may(ctx, actor, ActAddUser, Object{Kind: KindUser, Name: argv[2], Ask: Askable}); err != nil {
		return "", err
	}
	_, _, role := userInviteOf(argv)
	if role == RoleOperator {
		if err := w.may(ctx, actor, ActChangeRole, Object{Kind: KindUser, Name: argv[2]}); err != nil {
			return "", err
		}
	}
	return CarryPending, nil
}

// parseUserUninvite reads the words of user uninvite <name>.
func parseUserUninvite(rest []string) ([]string, error) {
	const use = "yardr user uninvite <name>"
	if len(rest) != 1 {
		return nil, fmt.Errorf("--run takes %s: one user, not %d words", use, len(rest))
	}
	return []string{rest[0]}, nil
}

// userUninviteCard is the card of user uninvite: whose membership goes,
// at the edge first and then here. It refuses a user who is not found,
// whom keepsOperator protects, and whose name no web_jwt_users entry
// maps (a plain user, whose removal is user rm's).
func (w *wtx) userUninviteCard(argv []string, asking bool) (AskRun, error) {
	name := argv[2]
	u, err := getUser(w.Tx, name)
	if err != nil {
		return AskRun{}, err
	}
	if err := keepsOperator(w.Tx, u, "removed"); err != nil {
		return AskRun{}, err
	}
	mapping, _, err := w.webJWTUsers()
	if err != nil {
		return AskRun{}, fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
	}
	emails := mappedEmails(mapping, name)
	if len(emails) == 0 {
		return AskRun{}, fmt.Errorf("user %s is no teammate: no entry of %s maps an address to them, so there is no membership to report the removal of (a plain user is removed with yardr user rm %s)", name, WebJWTUsersSetting, name)
	}
	devices := keys(w.Tx, bDevices, name+"\x00")
	does := []string{
		fmt.Sprintf("reports the removal to the edge first (member remove for %s): from then on that sign-in is admitted to this yard no more.", strings.Join(emails, ", ")),
		fmt.Sprintf("removes the user %s: their %s and %s go, and what waited to be told them; the mail stays in the log.", name, countNoun(len(u.Subscriptions), "subscription"), countNoun(len(devices), "device")),
		fmt.Sprintf("removes %s from %s, so the address maps to nobody from then on.", strings.Join(emails, ", "), WebJWTUsersSetting),
		"if the edge cannot be reached the user and the mapping still go, and the report is retried until it lands: meanwhile they act as nobody here.",
	}
	back := []string{"user", "invite", name, "--email", emails[0]}
	if u.IsOperator() {
		back = append(back, "--role", RoleOperator)
	}
	return AskRun{
		Argv:   argv,
		Does:   does,
		Back:   runCommandLine(back),
		BackBy: "only the operator",
		Seen: map[string]string{
			"user " + name:          seen(u.CreatedAt, u.CreatedBy),
			"web_jwt_users " + name: seen(emails),
		},
	}, nil
}

// userUninviteStale says why a removal approval is refused: the user was
// removed and added again since, or the mapping entry it reports stands
// otherwise now.
func (w *wtx) userUninviteStale(seq uint64, was, now AskRun) error {
	name := was.Argv[2]
	if key := "user " + name; was.Seen[key] != now.Seen[key] {
		e := lastEvent(w.Tx, "user_added", naming("user", name))
		return fmt.Errorf("ask %d was written when the user %s was another: they were removed and added again since%s. %s", seq, name, since(e), nothingRan)
	}
	if key := "web_jwt_users " + name; was.Seen[key] != now.Seen[key] {
		return fmt.Errorf("ask %d was written when another address of %s mapped to %s: the mapping was changed since. %s", seq, WebJWTUsersSetting, name, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runUserUninvite records a removal approval as to be carried out, as
// runUserInvite does: the edge report and the removal are the server's.
func runUserUninvite(ctx context.Context, w *wtx, argv []string, actor string, by *approvedOn) (string, error) {
	if err := w.may(ctx, actor, ActRemoveUser, Object{Kind: KindUser, Name: argv[2], Ask: Askable}); err != nil {
		return "", err
	}
	return CarryPending, nil
}

// teammateEmails are the addresses a teammate carry reports, as the
// yard stands in w: an invite's own address, and a removal's the
// addresses the mapping maps to its user.
func teammateEmails(w *wtx, argv []string) []string {
	if len(argv) < 3 || argv[0] != "user" {
		return nil
	}
	switch argv[1] {
	case "invite":
		_, email, _ := userInviteOf(argv)
		if email == "" {
			return nil
		}
		return []string{email}
	case "uninvite":
		mapping, _, err := w.webJWTUsers()
		if err != nil {
			return nil
		}
		return mappedEmails(mapping, argv[2])
	}
	return nil
}

// inviteTeammate makes an approved invite's yard writes in one write:
// the user with its role, then the invited address mapped to them. It
// is the carrier's first steps, and a carrying out taken up again finds
// each already standing and writes neither again: a user standing with
// another role, or an address mapped to another user, refuses whole.
func (w *wtx) inviteTeammate(ctx context.Context, name, email, role, actor string) error {
	if w.Bucket(bUsers).Get([]byte(name)) == nil {
		u, err := w.insertUser(ctx, name, actor, nil)
		if err != nil {
			return err
		}
		if role == RoleOperator && !u.IsOperator() {
			if _, err := w.setUser(ctx, name, []string{UserRole}, map[string]string{UserRole: RoleOperator}, actor, nil); err != nil {
				return err
			}
		}
	} else {
		u, err := getUser(w.Tx, name)
		if err != nil {
			return err
		}
		if u.RoleOf() != role {
			return fmt.Errorf("user %s stands as a %s, not the %s the invite approved", name, u.RoleOf(), role)
		}
	}
	mapping, _, err := w.webJWTUsers()
	if err != nil {
		return fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
	}
	low := strings.ToLower(email)
	switch mapping[low] {
	case name:
		return nil
	case "":
		mapping[low] = name
		return w.setSetting(ctx, WebJWTUsersSetting, formatWebJWTUsers(mapping), true, actor, nil)
	default:
		return fmt.Errorf("email %s is mapped to %s in %s already, and an invite never re-points it", email, mapping[low], WebJWTUsersSetting)
	}
}

// InviteTeammate is inviteTeammate as its own write, for the server that
// carries an approved invite out.
func (s *Store) InviteTeammate(ctx context.Context, name, email, role, actor string) error {
	return s.write(func(w *wtx) error { return w.inviteTeammate(ctx, name, email, role, actor) })
}

// removeTeammate makes an approved removal's yard writes in one write:
// the user (their devices, grants and login with them, as deleteUser
// drops them) and every mapping entry that maps to them. A carrying out
// taken up again, after the report it owed lands, finds both gone and
// writes nothing.
func (w *wtx) removeTeammate(ctx context.Context, name string, actor string) error {
	if w.Bucket(bUsers).Get([]byte(name)) != nil {
		if err := w.deleteUser(ctx, name, actor, nil); err != nil {
			return err
		}
	}
	mapping, _, err := w.webJWTUsers()
	if err != nil {
		return fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
	}
	changed := false
	for email, u := range mapping {
		if u == name {
			delete(mapping, email)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return w.setSetting(ctx, WebJWTUsersSetting, formatWebJWTUsers(mapping), true, actor, nil)
}

// RemoveTeammate is removeTeammate as its own write, for the server
// that carries an approved removal out.
func (s *Store) RemoveTeammate(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error { return w.removeTeammate(ctx, name, actor) })
}

// TeammateEmails are the addresses the standing mapping maps to user,
// for a carrier that holds no record of them.
func (s *Store) TeammateEmails(ctx context.Context, user string) ([]string, error) {
	var out []string
	err := s.read(func(tx *bolt.Tx) error {
		w := &wtx{Tx: tx, store: s}
		mapping, _, err := w.webJWTUsers()
		if err != nil {
			return err
		}
		out = mappedEmails(mapping, user)
		return nil
	})
	return out, err
}

// claimedTeammateCarry reads a teammate carrying record and holds it to
// the boot that owns its current claim, as claimedSetupCarry does.
func (w *wtx) claimedTeammateCarry(bead string, ask uint64, boot string) (*Carry, error) {
	var rec Carry
	ok, err := get(w.Tx, bCarry, carryKey(bead, ask), &rec)
	if err != nil {
		return nil, err
	}
	if !ok || rec.State != CarryClaimed || rec.Boot != boot || len(rec.Argv) < 3 || rec.Argv[0] != "user" ||
		rec.Argv[1] != "invite" && rec.Argv[1] != "uninvite" {
		return nil, fmt.Errorf("teammate ask %d on %s is not claimed by this server", ask, bead)
	}
	return &rec, nil
}

// CarryInvite makes the yard writes of the claimed invite bead/ask once,
// for the server that carries it out, and records that it made them in
// the same write. The first time, a user standing under the name already
// was added since the approval and is never taken over. Taken up again
// while its report is owed, it writes nothing and holds the user and the
// mapping to what it made: where the operator removed either since, the
// invite is undone at the yard and its report must not admit anyone.
func (s *Store) CarryInvite(ctx context.Context, bead string, ask uint64, boot, name, email, role, actor string) error {
	return s.write(func(w *wtx) error {
		rec, err := w.claimedTeammateCarry(bead, ask, boot)
		if err != nil {
			return err
		}
		if rec.TeammateWritten {
			u, err := getUser(w.Tx, name)
			if err != nil || u.RoleOf() != role {
				return fmt.Errorf("user %s no longer stands as the %s the invite made: it was changed or removed since, so the invite is not reported", name, role)
			}
			mapping, _, err := w.webJWTUsers()
			if err != nil {
				return fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
			}
			if mapping[strings.ToLower(email)] != name {
				return fmt.Errorf("%s no longer maps %s to %s: it was changed since, so the invite is not reported", WebJWTUsersSetting, email, name)
			}
			return nil
		}
		if w.Bucket(bUsers).Get([]byte(name)) != nil {
			return fmt.Errorf("%w: there is a user %s already, added since the invite was approved", ErrConflict, name)
		}
		if err := w.inviteTeammate(ctx, name, email, role, actor); err != nil {
			return err
		}
		rec.TeammateWritten = true
		return put(w.Tx, bCarry, carryKey(bead, ask), rec)
	})
}

// CarryUninvite makes the yard writes of the claimed removal bead/ask
// once, and records that it made them in the same write. Taken up again
// while its report is owed it writes nothing: a user added since under
// the same name is not the one the removal was approved for.
func (s *Store) CarryUninvite(ctx context.Context, bead string, ask uint64, boot, name, actor string) error {
	return s.write(func(w *wtx) error {
		rec, err := w.claimedTeammateCarry(bead, ask, boot)
		if err != nil {
			return err
		}
		if rec.TeammateWritten {
			return nil
		}
		if err := w.removeTeammate(ctx, name, actor); err != nil {
			return err
		}
		rec.TeammateWritten = true
		return put(w.Tx, bCarry, carryKey(bead, ask), rec)
	})
}
