package cli

import (
	"context"
	"slices"

	"yardr.dev/yardr/internal/store"
)

type sourceRemovalReferences struct {
	subscriptions  int
	participations int
	unsubscribe    []string
	unfollow       []string
}

// sourceRmReferences counts the records source rm leaves behind and prints
// the command for removing each subscriber's subscriptions and each thread
// participation. A single unsubscribe takes every subscription of that
// source for one crew member, group or user. A name a crew member and a
// manual group share takes two commands, one with each of --crew and
// --group: the one without a flag would refuse, both having subscriptions.
func sourceRmReferences(ctx context.Context, st store.API, source string) (sourceRemovalReferences, error) {
	left := sourceRemovalReferences{
		unsubscribe: []string{},
		unfollow:    []string{},
	}
	subscribed := map[string]map[string]bool{}
	unfollow := map[string]bool{}
	addSubscriptions := func(name, kind string, subs []store.Subscription) {
		for _, sub := range subs {
			if sub.Source == source {
				left.subscriptions++
				if subscribed[name] == nil {
					subscribed[name] = map[string]bool{}
				}
				subscribed[name][kind] = true
			}
		}
	}

	crew, err := st.Crew(ctx)
	if err != nil {
		return left, err
	}
	for _, member := range crew {
		addSubscriptions(member.Name, "crew", member.Subscriptions)
		threads, err := st.Threads(ctx, member.Name)
		if err != nil {
			return left, err
		}
		for _, participation := range threads {
			if participation.Source == source {
				left.participations++
				unfollow["yardr thread unfollow "+shellQuote(member.Name)+" --source "+shellQuote(source)+" "+shellQuote(participation.Thread)] = true
			}
		}
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return left, err
	}
	for _, group := range groups {
		addSubscriptions(group.Name, "group", group.Subscriptions)
	}
	users, err := st.Users(ctx)
	if err != nil {
		return left, err
	}
	for _, user := range users {
		addSubscriptions(user.Name, "user", user.Subscriptions)
	}
	for name, kinds := range subscribed {
		base := "yardr unsubscribe " + shellQuote(name) + " " + shellQuote(source)
		switch {
		case kinds["crew"] && kinds["group"]:
			left.unsubscribe = append(left.unsubscribe, base+" --crew", base+" --group")
		case kinds["user"] && kinds["group"]:
			left.unsubscribe = append(left.unsubscribe, base, base+" --group")
		default:
			left.unsubscribe = append(left.unsubscribe, base)
		}
	}
	for command := range unfollow {
		left.unfollow = append(left.unfollow, command)
	}
	slices.Sort(left.unsubscribe)
	slices.Sort(left.unfollow)
	return left, nil
}
