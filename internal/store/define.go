// The settings of a group or a crew member that are held to the operator
// when a definition is saved: what sessions run, where their environment
// comes from, and which user's login they burn. A setting the stored
// definition already holds, with the same value, is kept unasked, as a
// depot re-put is; everything else stays anyone's. Each check here is
// Alone: a run ask never proposes these (runask_alone.go), so the store
// and the run-ask refusal name the same settings in the same way.
package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
)

// checkGroupDefine holds a group's cmd, dir, env_file, kind, args, prompt
// and worktrees settings, and a change of its runner to or from exec, to
// the operator.
func checkGroupDefine(ctx context.Context, w *wtx, old, want Group, actor string) error {
	before, after := asStored(old.Config), asStored(want.Config)
	for _, key := range []string{"cmd", "dir", "env_file", "kind", "args", "prompt", "worktrees"} {
		was, had := before[key]
		is, has := after[key]
		if had == has && reflect.DeepEqual(was, is) {
			continue
		}
		if err := w.may(ctx, actor, ActChangeGroup, Object{Kind: KindGroup, Name: want.Name, Does: fmt.Sprintf("change %s on group %s", key, want.Name), Ask: Alone}); err != nil {
			return err
		}
	}
	if want.Runner != old.Runner && (want.Runner == "exec" || old.Runner == "exec") {
		if err := w.may(ctx, actor, ActChangeGroup, Object{Kind: KindGroup, Name: want.Name, Does: fmt.Sprintf("change runner on group %s", want.Name), Ask: Alone}); err != nil {
			return err
		}
	}
	return nil
}

// checkCrewDefine holds a crew member's env_file, env_command, kind, args
// and prompt settings to the operator, and the directory of a member the
// yard has, which is where its env_command runs and what a relative one
// is read against. A new member's directory stays anyone's: it can hold
// no env_command yet that the operator did not set.
func (w *wtx) checkCrewDefine(ctx context.Context, before, want CrewMember, existed bool, actor string) error {
	if existed && want.Dir != before.Dir {
		if err := w.may(ctx, actor, ActChangeCrew, Object{Kind: KindCrew, Name: want.Name, Does: fmt.Sprintf("change dir on crew member %s", want.Name), Ask: Alone}); err != nil {
			return err
		}
	}
	b, a := asStored(before.Config), asStored(want.Config)
	for _, key := range []string{"env_file", "env_command", "kind", "args", "prompt"} {
		was, had := b[key]
		is, has := a[key]
		if had == has && reflect.DeepEqual(was, is) {
			continue
		}
		if err := w.may(ctx, actor, ActChangeCrew, Object{Kind: KindCrew, Name: want.Name, Does: fmt.Sprintf("change %s on crew member %s", key, want.Name), Ask: Alone}); err != nil {
			return err
		}
	}
	return nil
}

// checkCrewSessionSettings keeps the conversation logins and the serving list in the operator's hands.
func (w *wtx) checkCrewSessionSettings(ctx context.Context, name string, before, after map[string]any, actor string) error {
	keys := map[string]bool{}
	for key := range before {
		if key == CrewForKey || IsEnvForKey(key) {
			keys[key] = true
		}
	}
	for key := range after {
		if key == CrewForKey || IsEnvForKey(key) {
			keys[key] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		ordered = append(ordered, key)
	}
	slices.Sort(ordered)
	for _, key := range ordered {
		was, had := before[key]
		is, set := after[key]
		if had == set && reflect.DeepEqual(was, is) {
			continue
		}
		if err := w.may(ctx, actor, ActChangeCrew, Object{Kind: KindCrew, Name: name, Does: fmt.Sprintf("change %s on crew member %s", key, name)}); err != nil {
			return err
		}
	}
	return nil
}
