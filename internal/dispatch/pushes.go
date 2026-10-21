package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
)

// What lands in a user's inbox is told them outside the yard, through the
// source their record names (yardr user set <name> notify=<source>), and so
// is an ask for them, which stands in their queue. The store queues a push
// with the mail, the notification or the ask, in the same write
// (store.Push), so the inbox never waits for a source; sending is the
// daemon's, here, for the reasons issues.go gives for an issue's move: the
// call is outside the store's write, the daemon is what waits out a
// source's outage, and every pass reads the queue again, so a daemon that
// was down or replaced carries on with no state of its own.
//
// Per user and pass, one message: everything that is past the grace
// (store.PushGrace) and still unread, or for an ask still open. What the
// user acknowledged meanwhile is dropped without a word, which is what
// keeps a person who is reading along in yardr chat from being told of it a
// second time; so is an ask they answered, declined or put aside.
//
// The message was sent at most once when the source took it: the pushes
// are gone from the store with that answer. Should the daemon die between
// the answer and that write, the next pass sends it again: a person is
// then told twice, never not at all.

const (
	// pushTimeout bounds one send from a pass, which holds up routing
	// while it waits.
	pushTimeout = 10 * time.Second
	// PushTries is how often a push is sent before it is given up, and
	// PushMaxAge how old one may be and still be sent. Both are new for
	// pushes: an issue's move is tried IssueTries times (24) and has no age
	// at which it is dropped, because an issue left in the wrong status
	// stays wrong, while a push about this morning's mail is noise by the
	// afternoon. The waits between tries are an issue move's (Backoff,
	// doubling up to BackoffMax).
	PushTries  = 8
	PushMaxAge = 6 * time.Hour
	// pushSettle is how long a message waits for a burst to end: it goes
	// once nothing new came for the grace, and at the latest when its
	// oldest item has waited this long, so that mail that keeps coming
	// does not hold it back for good.
	pushSettle = 3 * store.PushGrace
	// pushMailStart is how many runes of a mail's text are told.
	pushMailStart = 200
)

// pushes sends what is due to be told outside the yard.
func (d *Dispatcher) pushes(ctx context.Context) error {
	all, err := d.Store.Pushes(ctx)
	if err != nil {
		return err
	}
	if d.pushWait == nil {
		d.pushWait = map[string]time.Time{}
	}
	// Pushes come by user.
	for len(all) > 0 {
		n := 1
		for n < len(all) && all[n].User == all[0].User {
			n++
		}
		d.tell(ctx, all[0].User, all[:n])
		all = all[n:]
	}
	return nil
}

// pushAt has the daemon wake at t for a push that is due between passes.
func (d *Dispatcher) pushAt(t time.Time) {
	if d.pushDue == nil || t.Before(*d.pushDue) {
		d.pushDue = &t
	}
}

// tell sends one user what of ps is due, as one message, and records how
// it ended.
func (d *Dispatcher) tell(ctx context.Context, user string, ps []store.Push) {
	now := d.now()
	var read, old, live []store.Push
	for _, p := range ps {
		switch {
		case !p.Unread():
			read = append(read, p)
		case now.Sub(p.At) > PushMaxAge:
			old = append(old, p)
		default:
			live = append(live, p)
		}
	}
	if len(read) > 0 {
		if err := d.Store.DropPushes(ctx, user, seqs(read)); err != nil {
			d.Log.Warn("push: dropping what was read", "user", user, "err", err)
		}
	}
	u, err := d.Store.GetUser(ctx, user)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) { // gone: its pushes went with it
			d.Log.Warn("push: reading the user", "user", user, "err", err)
		}
		return
	}
	if len(old) > 0 {
		d.pushEnded(ctx, u, old, store.PushResult{Reason: store.PushExpired, Tries: tries(old), Error: lastError(old)})
	}
	if len(live) == 0 {
		return
	}
	// live is oldest first.
	if at := pushReady(live[0].At, live[len(live)-1].At); now.Before(at) {
		d.pushAt(at)
		return
	}
	var due []store.Push
	for _, p := range live {
		if !now.Before(p.At.Add(store.PushGrace)) {
			due = append(due, p)
		}
	}
	// One that failed waits out its backoff, and what came since waits with
	// it: a source that does not answer is not asked with every new mail.
	for _, p := range due {
		if p.RetryAt != nil && now.Before(*p.RetryAt) {
			d.pushAt(*p.RetryAt)
			return
		}
	}
	if until, ok := d.pushWait[u.Notify]; ok && now.Before(until) {
		d.pushAt(until)
		return
	}
	sent := 0
	for _, at := range u.Pushed {
		if now.Sub(at) < time.Hour {
			sent++
		}
	}
	if sent >= store.PushesPerHour {
		// u.Pushed holds the newest PushesPerHour: its first is the one
		// whose hour ends next.
		d.pushAt(u.Pushed[len(u.Pushed)-store.PushesPerHour].Add(time.Hour))
		return
	}
	msg := pushMessage(due)
	msg.Image = pushImage(ctx, d.Store, d.Home, due)
	if sent == store.PushesPerHour-1 {
		msg.Text = fmt.Sprintf("The %dth message this hour, and the last: what comes next waits in yardr inbox.\n", store.PushesPerHour) + msg.Text
	}

	cctx, cancel := context.WithTimeout(ctx, pushTimeout)
	res, err := notify.Send(cctx, d.Store, d.Home, user, msg)
	cancel()
	var notSetUp *notify.NotSetUp
	var call *source.CallError
	switch {
	case errors.As(err, &notSetUp):
		d.pushEnded(ctx, u, due, store.PushResult{Reason: store.PushNotSetUp, Error: err.Error()})
	case errors.As(err, &call) && call.Status >= 200 && call.Status < 300:
		// The source took the message and answered with what yardr does
		// not read (no JSON, too much of it): told all the same, and not
		// to be told again.
		d.pushEnded(ctx, u, due, store.PushResult{Status: call.Status, Tries: tries(due) + 1})
	case errors.As(err, &call) && call.Reason != source.Unreachable && call.Reason != source.TimedOut:
		// The declaration or what it was given does not make a call: the
		// next try would say the same.
		d.pushEnded(ctx, u, due, store.PushResult{Reason: store.PushNotSetUp, Status: call.Status, Error: err.Error()})
	case err != nil:
		d.pushFailed(ctx, u, due, 0, err.Error())
	case res.OK():
		delete(d.pushWait, u.Notify)
		d.pushEnded(ctx, u, due, store.PushResult{Status: res.Status, Tries: tries(due) + 1})
	case finalStatus(res.Status):
		d.pushEnded(ctx, u, due, store.PushResult{Reason: store.PushRefused, Status: res.Status, Tries: tries(due) + 1,
			Error: fmt.Sprintf("%s answered %d", u.Notify, res.Status)})
	default:
		d.pushFailed(ctx, u, due, res.Status, fmt.Sprintf("%s answered %d", u.Notify, res.Status))
	}
}

// finalStatus reports whether a source's answer says that the same message
// will not be taken on another try: any 4xx but a timeout (408) and a
// limit (429). The rule is yardr's, the same for every source.
func finalStatus(status int) bool {
	return status >= 400 && status < 500 && status != 408 && status != 429
}

// pushEnded records that the user's pushes ps ended as res says, with a
// line in the log. The line, as the event the store logs for a failure,
// has the user, the source and how it went, and nothing of the message.
func (d *Dispatcher) pushEnded(ctx context.Context, u *store.User, ps []store.Push, res store.PushResult) {
	res.At, res.Source = d.now(), u.Notify
	switch {
	case res.OK():
		d.Log.Info("user told", "user", u.Name, "source", res.Source, "items", len(ps), "status", res.Status)
	case res.Reason == store.PushNotSetUp && u.LastPush != nil && u.LastPush.Reason == res.Reason && u.LastPush.Error == res.Error:
		// Said when it was first met.
	default:
		d.Log.Warn("user not told", "user", u.Name, "source", res.Source, "items", len(ps), "reason", res.Reason, "status", res.Status, "err", res.Error)
	}
	if err := d.Store.PushTried(ctx, u.Name, seqs(ps), res); err != nil {
		d.Log.Warn("push: recording how it ended", "user", u.Name, "err", err)
		d.pushWait[u.Notify] = d.now().Add(d.Backoff) // the pushes stay: not sent again at once
	}
}

// pushFailed has a send that failed tried again, after a wait that doubles
// from Backoff up to BackoffMax, and gives up those of ps that have now
// failed PushTries times.
func (d *Dispatcher) pushFailed(ctx context.Context, u *store.User, ps []store.Push, status int, cause string) {
	d.pushWait[u.Notify] = d.now().Add(d.Backoff)
	var again, over []store.Push
	for _, p := range ps {
		if p.Attempts+1 >= PushTries {
			over = append(over, p)
		} else {
			again = append(again, p)
		}
	}
	if len(again) > 0 {
		n := tries(again) + 1
		retry := d.now().Add(min(d.Backoff<<min(n-1, 30), d.BackoffMax))
		if n == 1 {
			d.Log.Warn("telling a user failed; trying again", "user", u.Name, "source", u.Notify, "items", len(again), "err", cause)
		}
		res := store.PushResult{At: d.now(), Source: u.Notify, Reason: store.PushRetry, Status: status, Error: cause, Tries: n, RetryAt: &retry}
		if err := d.Store.PushTried(ctx, u.Name, seqs(again), res); err != nil {
			d.Log.Warn("push: recording the failure", "user", u.Name, "err", err)
		}
	}
	if len(over) > 0 {
		d.pushEnded(ctx, u, over, store.PushResult{Reason: store.PushGaveUp, Status: status, Tries: tries(over) + 1,
			Error: fmt.Sprintf("gave up after %d tries: %s", tries(over)+1, cause)})
	}
}

func seqs(ps []store.Push) []uint64 {
	out := make([]uint64, len(ps))
	for i, p := range ps {
		out[i] = p.Seq
	}
	return out
}

// tries is how often the most tried of ps has failed.
func tries(ps []store.Push) int {
	n := 0
	for _, p := range ps {
		n = max(n, p.Attempts)
	}
	return n
}

// lastError is what the last failed send of any of ps came to.
func lastError(ps []store.Push) string {
	for i := len(ps) - 1; i >= 0; i-- {
		if ps[i].Error != "" {
			return ps[i].Error
		}
	}
	return ""
}

// pushReady is when a message for what landed from oldest to newest may
// go: once the newest is past the grace, so that a burst is one message,
// and no later than pushSettle after the oldest.
func pushReady(oldest, newest time.Time) time.Time {
	at := newest.Add(store.PushGrace)
	if latest := oldest.Add(pushSettle); latest.Before(at) {
		return latest
	}
	return at
}

// pushMessage is what a user is told of ps outside the yard: the one place
// that decides what of the yard leaves the machine in a push. It says who,
// what and the start of the text. For mail that is the sender (or that a
// timer fired) and the first pushMailStart runes; for a notification the
// bead and what happened to it, or the source of a hook delivery, and the
// notification's line as the inbox has it, within the bound the store
// makes it to; for an ask who asks, and the first pushMailStart runes of the
// question, with nothing of its context, its options or its bead. No link
// is sent, and so not the URL a hook delivery's line ends with. Several are
// counted in the title and named in the text, each by its who and what
// alone; the title says the inbox unless an ask is among them, which is in
// none. The source cuts both to what its call takes.
func pushMessage(ps []store.Push) notify.Message {
	if len(ps) == 1 {
		title, text := pushLine(ps[0])
		return notify.Message{Title: title, Text: text}
	}
	// In the order they came, each kind said once with how many.
	var heads []string
	count := map[string]int{}
	where := "your yardr inbox"
	for _, p := range ps {
		if p.Ask != nil {
			where = "yardr"
		}
		head, _ := pushLine(p)
		if count[head] == 0 {
			heads = append(heads, head)
		}
		count[head]++
	}
	for i, head := range heads {
		if n := count[head]; n > 1 {
			heads[i] = fmt.Sprintf("%s (%d)", head, n)
		}
	}
	return notify.Message{Title: fmt.Sprintf("%d new in %s", len(ps), where), Text: strings.Join(heads, ", ")}
}

// pushImage is the local path of the picture the message about ps carries:
// the first image across the batch in order. A mail carries the first
// image of its files; a notification of a done bead carries the newest
// note on that bead with an image. The rest stay in the yard: one tell
// carries one picture. "" when no image is told, when the bead or its
// notes are gone, and when the blob the file names is not there: the
// tell then goes unadorned.
func pushImage(ctx context.Context, st store.API, home string, ps []store.Push) string {
	for _, p := range ps {
		if p.Mail != nil {
			if path := firstImage(home, p.Mail.Files); path != "" {
				return path
			}
		}
		if p.Note != nil && p.Note.Kind == "closed" && p.Note.Bead != "" {
			if path := doneImage(ctx, st, home, p.Note.Bead); path != "" {
				return path
			}
		}
	}
	return ""
}

// doneImage is the local path of the newest picture on the notes of the
// done bead: its notes newest first, each note's images in order. "" when
// no note carries an image the yard still holds.
func doneImage(ctx context.Context, st store.API, home, bead string) string {
	notes, err := st.Notes(ctx, bead)
	if err != nil {
		return ""
	}
	for i := len(notes) - 1; i >= 0; i-- {
		if path := firstImage(home, notes[i].Files); path != "" {
			return path
		}
	}
	return ""
}

// firstImage is the local path of the first image of files: the first
// the yard took whose bytes say image, and whose blob stands. "" when
// there is none.
func firstImage(home string, files []store.BlobRef) string {
	for _, f := range files {
		if f.NotTaken != "" || !strings.HasPrefix(f.Type, "image/") {
			continue
		}
		path := store.BlobPath(home, f.Hash)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

// pushLine is one push as who and what (title) and the start of its text.
func pushLine(p store.Push) (title, text string) {
	switch {
	case p.Mail != nil:
		title = "mail from " + p.Mail.From
		if p.Mail.Timer != "" {
			title = "timer from " + p.Mail.From
		}
		return title, clipRunes(p.Mail.Text, pushMailStart)
	case p.Ask != nil:
		return p.Ask.Asker + " asks", clipRunes(p.Ask.Question, pushMailStart)
	case p.Note != nil && p.Note.Bead != "":
		title = p.Note.Bead + " " + p.Note.Kind
	case p.Note != nil:
		title = strings.TrimSpace(p.Note.Source + " " + p.Note.Kind)
	default:
		return "", ""
	}
	return title, clipRunes(p.Note.Said(), store.MaxNotificationText)
}

// clipRunes cuts s to at most n runes, marking the cut.
func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
