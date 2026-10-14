package store_test

import (
	"context"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The hatch matches what deliverBeadless would: kind alone, scope all,
// with no bead fields.
func TestMatchBeadless(t *testing.T) {
	mk := func(scope string, where map[string]string) store.Subscription {
		return store.Subscription{Source: store.SourceYardr, Scope: scope, Where: where}
	}
	cases := []struct {
		sub  store.Subscription
		kind string
		want bool
	}{
		{mk(store.ScopeAll, map[string]string{"kind": "install_failed"}), "install_failed", true},
		{mk(store.ScopeAll, map[string]string{"kind": "attention"}), "rolled_back", true},
		{mk(store.ScopeAll, nil), "install_failed", true},
		{mk(store.ScopeAll, map[string]string{"kind": "install_failed", "actor": "yard"}), "install_failed", true},
		{mk(store.ScopeAll, map[string]string{"kind": "install_failed", "actor": "me"}), "install_failed", false},
		{mk(store.ScopeAll, map[string]string{"kind": "install_failed", "bead": "x"}), "install_failed", false},
		{mk(store.ScopeMentions, map[string]string{"kind": "install_failed"}), "install_failed", false},
		{mk(store.ScopeAll, map[string]string{"kind": "noted"}), "install_failed", false},
		{store.Subscription{Source: "github", Scope: store.ScopeAll}, "install_failed", false},
	}
	for i, c := range cases {
		if got := store.MatchBeadless(c.sub, c.kind, "yard") != ""; got != c.want {
			t.Errorf("case %d: match = %v, want %v (%+v)", i, got, c.want, c.sub)
		}
	}
}

// upgrade_restore is true unless set or overridden false, and refuses any
// other value.
func TestUpgradeRestoreSetting(t *testing.T) {
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.SetSetting(ctx, store.UpgradeRestoreSetting, "maybe", "me"); err == nil {
		t.Fatalf("maybe is taken")
	}
	// A yard that names no operator yet takes one from whoever sets one.
	if err := st.SetSetting(ctx, "operator", "me", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.UpgradeRestoreSetting, "false", "me"); err != nil {
		t.Fatal(err)
	}
	got, err := st.Settings(ctx)
	if err != nil || got[store.UpgradeRestoreSetting] != "false" {
		t.Fatalf("settings = %v, %v", got, err)
	}
	set, _ := store.SettingOf(store.UpgradeRestoreSetting)
	if v, _ := set.Effective(map[string]string{}); v != "true" {
		t.Fatalf("unset reads as %q, want true", v)
	}
	t.Setenv("YARDR_UPGRADE_RESTORE", "true")
	if v, from := set.Effective(got); v != "true" || from != store.FromEnvironment {
		t.Fatalf("env reads as %q from %q", v, from)
	}
}

// The hatch opens the pre-install copy read-only, without migrations, and
// serves its users and sources to the direct send.
func TestOpenHatchCopy(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	if _, err := st.AddUser(ctx, "jo", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSubscription(ctx, "jo", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"kind": store.JoinWhere([]string{store.InstallFailed, store.RolledBack})}}, "jo"); err != nil {
		t.Fatal(err)
	}
	// A backup is the copy the hatch reads.
	backup := path + ".copy.db"
	res, err := st.Backup(ctx, backup)
	if err != nil || res.Path != backup {
		t.Fatalf("backup = %+v, %v", res, err)
	}
	h, err := store.OpenHatchCopy(backup)
	if err != nil {
		t.Fatal(err)
	}
	u, err := h.GetUser(ctx, "jo")
	if err != nil || len(u.Subscriptions) != 1 {
		t.Fatalf("hatch user = %+v, %v", u, err)
	}
	if got := store.MatchBeadless(u.Subscriptions[0], store.InstallFailed, "yard"); got == "" {
		t.Fatalf("the copy's subscription does not match install_failed")
	}
	if _, err := h.GetSource(ctx, "missing"); err == nil {
		t.Fatalf("a missing source reads without an error")
	}
}
