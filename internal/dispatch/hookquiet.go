package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// HookQuietEvery is how often the daemon judges its hooks quiet: the usual
// gap floors at an hour, so looking more often finds nothing sooner.
const HookQuietEvery = time.Hour

// webhookProbeTimeout bounds one liveness probe of a chat hook's webhook,
// which holds up routing while it waits.
const webhookProbeTimeout = 30 * time.Second

// quietHooks logs hook_quiet for each hook that used to deliver and has
// delivered nothing for longer than its usual gap (store.QuietGaps): the
// webhook is off at the source, or cannot reach this yard. A hook that hears
// a person's chat back has no steady rhythm to time silence by, so
// QuietGaps leaves it out, and quietChats judges its webhook's health
// instead. It is its own memory, as a waited bead's stay is: a hook_quiet
// logged since the hook last accepted a delivery means the spell was told
// of, so a daemon that replaces itself does not tell again, and a hook that
// delivers and goes quiet again is told of anew. Tells are read per hook,
// so one hook's many tells never crowd another's out.
func (d *Dispatcher) quietHooks(ctx context.Context) {
	now := d.now()
	if !d.quietChecked.IsZero() && now.Sub(d.quietChecked) < HookQuietEvery {
		return
	}
	hooks, err := d.Store.Hooks(ctx)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	gaps, err := store.QuietGaps(ctx, d.Store, hooks, now)
	if err != nil {
		d.Log.Warn("quiet hooks", "err", err)
		return
	}
	byName := map[string]store.Hook{}
	for _, h := range hooks {
		byName[h.Name] = h
	}
	failed := false
	for name, gap := range gaps {
		h := byName[name]
		told, ok := d.quietTold(ctx, h)
		if !ok {
			failed = true
			continue
		}
		if told {
			continue
		}
		d.Log.Warn("hook quiet", "hook", h.Name, "source", h.Source,
			"last", h.Accepted.UTC().Format(time.RFC3339), "gap", round(gap).String())
		if err := d.Store.LogDispatch(ctx, "", store.HookQuiet, actor, map[string]any{
			"hook": h.Name, "source": h.Source,
			"last": h.Accepted.UTC().Format(time.RFC3339), "gap": round(gap).String()}); err != nil {
			d.Log.Warn(store.HookQuiet+" event", "hook", h.Name, "err", err)
			failed = true
		}
	}
	if !d.quietChats(ctx, hooks) {
		failed = true
	}
	// A transient read or write failure must be retried on the next pass,
	// rather than waiting another hour for the next quiet-hook check.
	if !failed {
		d.quietChecked = now
	}
}

// quietChats judges the hooks that hear a person's chat back, which
// QuietGaps leaves out: not by their silence, which has no rhythm, but by
// what the source holds of their webhook (webhookBroken). A broken one is
// told of as a quiet one is, once per spell, with why it is broken for a
// reason instead of a gap. It reports whether the pass judged them all: a
// probe that got no usable answer is warned of where it failed and tried
// again on the next hourly check, not on the next pass.
func (d *Dispatcher) quietChats(ctx context.Context, hooks []store.Hook) bool {
	ok := true
	for _, h := range hooks {
		if h.Accepted == nil || h.Blocked != "" || h.LastRefused() {
			continue
		}
		broken, known := d.webhookBroken(ctx, h)
		if !known || broken == "" {
			continue
		}
		told, toldOK := d.quietTold(ctx, h)
		if !toldOK {
			ok = false
			continue
		}
		if told {
			continue
		}
		d.Log.Warn("hook quiet", "hook", h.Name, "source", h.Source,
			"last", h.Accepted.UTC().Format(time.RFC3339), "reason", broken)
		if err := d.Store.LogDispatch(ctx, "", store.HookQuiet, actor, map[string]any{
			"hook": h.Name, "source": h.Source,
			"last": h.Accepted.UTC().Format(time.RFC3339), "reason": broken}); err != nil {
			d.Log.Warn(store.HookQuiet+" event", "hook", h.Name, "err", err)
			ok = false
		}
	}
	return ok
}

// quietTold reports whether the hook's spell was told of: a hook_quiet
// logged since the hook last accepted a delivery. ok is false when that is
// unknown: the read failed, or the walk did not reach the hook's tells. The
// pass then tells nothing and tries again on the next, rather than tell
// twice.
func (d *Dispatcher) quietTold(ctx context.Context, h store.Hook) (told, ok bool) {
	read, err := d.Store.EventsOfHook(ctx, store.HookQuiet, h.Name, 1)
	if err != nil {
		d.Log.Warn("quiet hooks", "hook", h.Name, "err", err)
		return false, false
	}
	if !read.Complete {
		// The walk did not reach the hook's tells: unknown whether
		// the spell was told of. Skip this pass and retry the next,
		// rather than tell twice.
		return false, false
	}
	return len(read.Events) == 1 && !read.Events[0].At.Before(*h.Accepted), true
}

// webhookBroken asks the source what it holds of the hook's webhook: the
// source's check call, as yardr source check makes it, with the source's
// credential. It returns why the webhook is broken, "" when it is healthy.
// known is false for a hook of no chat source, one whose source names no
// check call, and a probe that got no usable answer: none of those is a
// break, and the hook is not told of.
func (d *Dispatcher) webhookBroken(ctx context.Context, h store.Hook) (broken string, known bool) {
	decl, src, err := declaredSource(ctx, d.Store, h.Source)
	if err != nil {
		d.Log.Warn("quiet hooks", "hook", h.Name, "err", err)
		return "", false
	}
	if decl == nil || !decl.HearsChat() || decl.Check == "" {
		return "", false
	}
	// The probe goes to the source's chosen base, as a call does.
	decl = decl.WithBase(src.Base)
	cred, _, err := decl.ReadCredential(ctx, src.EnvFile, src.Mint, source.Read)
	if err != nil {
		d.Log.Warn("quiet hooks", "hook", h.Name, "err", err)
		return "", false
	}
	pctx, cancel := context.WithTimeout(ctx, webhookProbeTimeout)
	defer cancel()
	res, err := source.Do(pctx, decl, decl.Check, nil, cred)
	if err != nil {
		d.Log.Warn("quiet hooks", "hook", h.Name, "err", err)
		return "", false
	}
	if !res.OK() {
		d.Log.Warn("quiet hooks", "hook", h.Name, "check", decl.Check, "status", res.Status)
		return "", false
	}
	return webhookBreak(res.Answer, *h.Accepted), true
}

// webhookBreak judges what the source holds of a chat hook's webhook: the
// declaration's check call answered as a webhook's status, with the url it
// sends to, the updates that wait, and when its last delivery failed and
// why. It returns why the webhook is broken, "" when it is healthy or the
// answer says nothing usable. No webhook set at the source is broken,
// whatever the silence; a delivery that failed since the hook last
// accepted one is the break the silence hides, with how many updates wait
// behind it.
func webhookBreak(answer json.RawMessage, accepted time.Time) string {
	var info struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(answer, &info); err != nil || len(info.Result) == 0 || string(info.Result) == "null" {
		return ""
	}
	var held struct {
		URL     string `json:"url"`
		Pending int    `json:"pending_update_count"`
		ErrorAt int64  `json:"last_error_date"`
	}
	if err := json.Unmarshal(info.Result, &held); err != nil {
		return ""
	}
	if held.URL == "" {
		return "no webhook is set at the source"
	}
	// The source reports error dates in whole seconds. Include the accepted
	// delivery's second so an error later in that same second is not missed.
	if held.ErrorAt > 0 && held.ErrorAt >= accepted.Unix() {
		failed := time.Unix(held.ErrorAt, 0).UTC().Format(time.RFC3339)
		// last_error_message is service text. It is unnecessary for this
		// diagnosis and may repeat a credential or contain log control text.
		return fmt.Sprintf("the source's deliveries fail (last failed %s, %d pending)", failed, held.Pending)
	}
	return ""
}
