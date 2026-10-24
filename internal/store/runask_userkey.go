// The user key row of the run-ask list: a person's own address in a
// source, carried from a delivery the yard dropped for no known address
// (ClaimAddress in claim.go). The command as typed takes no value --
// yardr user key types it unseen -- so the ask carries it in
// AskRun.Value and shows it in the yard's sentences; approving writes
// it exactly where yardr user key writes it, through keyUser.
package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/source"
)

// parseUserKey reads the words of user key <user> <source>: one user
// and one source, and no value, which is carried beside the command.
func parseUserKey(rest []string) ([]string, error) {
	const use = "yardr user key <user> <source>"
	if len(rest) != 2 {
		return nil, fmt.Errorf("--run takes %s: one user and one source, not %d words", use, len(rest))
	}
	return []string{rest[0], rest[1]}, nil
}

// userKeyOf is the user and the source of a parsed user key.
func userKeyOf(argv []string) (user, src string) { return argv[2], argv[3] }

// isUserKeyRun reports whether argv is a user key command in its stored
// form. Only a claim card carries one: AddAsk refuses a user key it did
// not offer, since only a dropped delivery may carry the address.
func isUserKeyRun(argv []string) bool {
	return len(argv) == 4 && argv[0] == "user" && argv[1] == "key"
}

// checkUserKey is giving a user's own address as the asker: it is that
// user's to give, or the operator's for them (mayTell), and a crew
// member's proposal of it is the operator's to approve. It returns nil
// where the asker may give it themselves, which askedRun refuses as
// needing no approval.
func checkUserKey(ctx context.Context, w *wtx, run AskRun, actor string) error {
	user, _ := userKeyOf(run.Argv)
	if err := w.may(ctx, actor, ActTell, Object{Kind: KindUser, Name: user}); err != nil {
		// A crew member (or anyone else) may not give it, so the ask
		// stands: say whose approval it takes, in mayTell's words.
		return &OperatorOnlyError{msg: err.Error(), ask: Askable}
	}
	return nil
}

// userKeyAddress is what the user's env file sets under env now. A
// missing file means none; a file that cannot be read must not be
// mistaken for an empty one, since approval could overwrite its value.
func (w *wtx) userKeyAddress(u *User, env string) (string, error) {
	file := u.EnvFile
	if file == "" {
		if w.home == "" {
			return "", nil
		}
		var err error
		file, err = userEnvFile(w.home, u.Name)
		if err != nil {
			return "", err
		}
	}
	f, err := config.ReadEnvFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("user %s: %w", u.Name, err)
	}
	return source.Lookup(f.Env, env), nil
}

// userKeyCard is the card of user key <user> <source> with the address
// the ask carries: it writes the one variable the source's notify key
// names, with that value, into the user's env file, and subscribes the
// yard's hearer to that address beside it, so no terminal step is left
// after the claim. Asking refuses a source that tells nobody, a user
// the yard has not, no carried address (a command typed in a terminal
// gives its value unseen, so it is typed there, not proposed), an
// address with white space in it, and a user who has an address in the
// source already: a claim is offered only while they have none. The
// approval is held to the source as declared, to the user, and to the
// address their file sets now (Seen), so a giving in between is stale,
// not overwritten; the subscription's sentence names the member, so a
// hearer set in between reads otherwise and is stale the same way.
func (w *wtx) userKeyCard(run AskRun, asking bool) (AskRun, error) {
	user, srcName := userKeyOf(run.Argv)
	u, err := getUser(w.Tx, user)
	if err != nil {
		return AskRun{}, err
	}
	src, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return AskRun{}, err
	}
	if d.Notify == nil {
		return AskRun{}, fmt.Errorf("source %s: its declaration has no notify key, so it reaches no person and reads no address of %s's", srcName, user)
	}
	env := d.Notify.AddressEnv
	value := strings.TrimSpace(run.Value)
	switch {
	case value == "":
		return AskRun{}, fmt.Errorf("user key %s %s carries no address: the value is typed unseen at a terminal (yardr user key %s %s), and a card carries only an address a delivery brought", user, srcName, user, srcName)
	case strings.TrimSpace(value) != run.Value || strings.ContainsAny(value, " \t\r\n"):
		return AskRun{}, fmt.Errorf("user key %s %s: the address it carries has white space in it, and no address the yard writes has", user, srcName)
	case claimPlain(value) != value:
		return AskRun{}, fmt.Errorf("user key %s %s: the address it carries holds a control character, and no address the yard writes does", user, srcName)
	}
	fingerprint, err := w.sourceSeen(src)
	if err != nil {
		return AskRun{}, err
	}
	current, err := w.userKeyAddress(u, env)
	if err != nil {
		return AskRun{}, err
	}
	if asking && current != "" {
		return AskRun{}, fmt.Errorf("user %s has %s set in %s already: a claim is offered only while they have no address in %s", user, env, u.EnvFile, srcName)
	}
	member, sub, err := w.claimSubscriber(srcName, d, value)
	if err != nil {
		return AskRun{}, err
	}
	file := u.EnvFile
	if file == "" && w.home != "" && !strings.ContainsAny(user, `/\`) {
		file = filepath.Join(w.home, "users", user+".env")
	}
	now := "none is set now"
	if current != "" {
		now = "one is set now"
	}
	does := []string{fmt.Sprintf("writes %s=%s into %s's env file (%s), exactly where yardr user key %s %s writes it: the file is mode 0600, its other lines are kept, and %s.", env, value, user, or(file, "the file the yard chooses for them"), user, srcName, now)}
	if member != "" {
		does = append(does, claimSubscribeLine(member, sub))
	}
	return AskRun{
		Argv:   run.Argv,
		Does:   does,
		Back:   runCommandLine([]string{"user", "key", user, srcName}),
		BackBy: "only the operator, by giving another address with",
		Seen: map[string]string{
			"source " + srcName:        fingerprint,
			"user " + user:             seen(u.EnvFile, u.Notify, u.CreatedAt),
			"user " + user + " " + env: seen(current),
		},
		Value: value,
	}, nil
}

// claimSubscriber is the crew member an approval of the claim for srcName
// subscribes beside writing the address, with the subscription: the
// yard's hearer, where one is set and names a crew member of the yard.
// The subscription hears every delivery (scope all) whose hear field
// holds the claimed address, with the filter the declaration's hear key
// names beside it. member is "" where the card offers no subscription:
// no hearer set, no such crew member, or a source that hears nothing
// back.
func (w *wtx) claimSubscriber(srcName string, d *source.Declaration, address string) (member string, sub Subscription, err error) {
	if d.Hear == nil || d.Hear.Field == "" {
		return "", Subscription{}, nil
	}
	var hearer string
	if _, err := get(w.Tx, bSettings, HearerSetting, &hearer); err != nil || hearer == "" {
		return "", Subscription{}, err
	}
	if w.Bucket(bCrew).Get([]byte(hearer)) == nil {
		return "", Subscription{}, nil
	}
	where := map[string]string{d.Hear.Field: address}
	for k, v := range d.Hear.Subscribe {
		where[k] = v
	}
	sub = Subscription{Source: srcName, Scope: ScopeAll, Where: where}
	if err := w.bindSubscription(hearer, "", &sub); err != nil {
		return "", Subscription{}, err
	}
	return hearer, sub, nil
}

// claimSubscribeLine is the card's sentence for the subscription an
// approval of the claim makes beside writing the address: who hears the
// chat, and the command that takes it back. It reads the same whether
// the member has the subscription already, so a giving in between is
// kept once, not stale.
func claimSubscribeLine(member string, sub Subscription) string {
	back := []string{"unsubscribe", member, sub.Source, "--scope", sub.Scope}
	for _, k := range slices.Sorted(maps.Keys(sub.Where)) {
		for _, v := range WhereValues(sub.Where[k]) {
			back = append(back, "--where", k+"="+v)
		}
	}
	return fmt.Sprintf("subscribes %s to %s (kept once if %s has it already), so %s hears this chat; taken back with %s.", member, sub, member, member, runCommandLine(back))
}

// userKeyStale says why the ask seq for user key is approved no more:
// the source was replaced, the user was set under their name, or the
// address their file sets is not the one the card was written from. A
// hearer set in between reads otherwise on the card, and is stale by
// that sentence.
func (w *wtx) userKeyStale(seq uint64, was, now AskRun) error {
	user, srcName := userKeyOf(was.Argv)
	if err := w.staleSource(seq, srcName, was, now); err != nil {
		return err
	}
	_, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return staleCard(seq, was, now)
	}
	env := d.Notify.AddressEnv
	if key := "user " + user + " " + env; was.Seen[key] != now.Seen[key] {
		return fmt.Errorf("ask %d was written while %s had no %s in %s: one is set now. %s", seq, user, env, srcName, nothingRan)
	}
	if key := "user " + user; was.Seen[key] != now.Seen[key] {
		return fmt.Errorf("ask %d was written when the user %s was another: they were set since. %s", seq, user, nothingRan)
	}
	return staleCard(seq, was, now)
}

// runUserKey writes the carried address into the user's env file as
// actor, through keyUser, subscribes the yard's hearer to it beside
// the write, as the card says, and closes the card's bead. It answers
// what yardr user key prints, without the file's path: the approval's
// line is read beside the command's, and each yard's home is its own.
func runUserKey(ctx context.Context, w *wtx, run AskRun, actor string, by *approvedOn) (string, error) {
	user, srcName := userKeyOf(run.Argv)
	_, d, err := sourceIn(w.Tx, srcName)
	if err != nil {
		return "", err
	}
	env := d.Notify.AddressEnv
	var k sourceKey
	if err := w.keyUser(ctx, w.home, user, srcName, map[string]string{env: run.Value}, actor, false, &k); err != nil {
		return "", err
	}
	out := fmt.Sprintf("user %s: %s is set", user, env)
	member, sub, err := w.claimSubscriber(srcName, d, run.Value)
	if err != nil {
		return "", err
	}
	if member != "" {
		var r CrewMember
		if ok, err := get(w.Tx, bCrew, member, &r); err != nil {
			return "", err
		} else if ok {
			has := slices.ContainsFunc(r.Subscriptions, sub.same)
			if err := w.addSubscription(ctx, member, sub, actor, by); err != nil {
				return "", err
			}
			if has {
				out += fmt.Sprintf("; %s already subscribed: %s", member, sub)
			} else {
				out += fmt.Sprintf("; %s subscribed: %s", member, sub)
			}
		}
	}
	// The approval settles the claim: its card's bead closes, as a mapping
	// card's does when its sender is settled. The answer is recorded after
	// this runs, so the ask is answered, not taken back; a bead closed by
	// hand meanwhile is left as it is.
	if by != nil {
		if row, err := w.loadBead(by.Bead); err != nil {
			return "", err
		} else if row.open() {
			if err := w.closeCard(row, fmt.Sprintf("%s is %s in %s", run.Value, user, srcName), actor); err != nil {
				return "", err
			}
		}
	}
	return out, nil
}
