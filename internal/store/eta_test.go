package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// etaTest is a store with a clock the test sets: now is what now() answers.
type etaTest struct {
	*Store
	now time.Time
}

// newETATest sets the package's fake clock and YARDR_STORE_NOSYNC, so its
// callers stay sequential.
func newETATest(t testing.TB) *etaTest {
	t.Helper()
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	y := &etaTest{now: time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	var err error
	if y.Store, err = Open(filepath.Join(t.TempDir(), "y.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { y.Close(context.Background()) })
	ctx := context.Background()
	if err := y.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, Group{Name: "reviewers", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	return y
}

// at sets the clock and returns the test, so steps read as y.at(t0).create().
func (y *etaTest) at(t time.Time) *etaTest {
	y.now = t
	return y
}

func (y *etaTest) create(t testing.TB, title, stage string, labels ...string) *Bead {
	t.Helper()
	b, err := y.CreateBead(context.Background(), NewBead{Title: title, Stage: stage, Labels: labels}, "me")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (y *etaTest) claim(t testing.TB, id, group, stage string) *Session {
	t.Helper()
	s, err := y.Claim(context.Background(), id, group, stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (y *etaTest) advance(t testing.TB, id, stage string) {
	t.Helper()
	if _, err := y.Advance(context.Background(), id, stage, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
}

// row reads the bead's stored row, with its stay fields.
func (y *etaTest) row(t testing.TB, id string) *beadRow {
	t.Helper()
	var row *beadRow
	if err := y.read(func(tx *bolt.Tx) error {
		var err error
		row, err = loadBead(tx, id)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return row
}

func (y *etaTest) cell(t testing.TB, group, label, stage string) ETACell {
	t.Helper()
	c, err := y.ETACell(context.Background(), group, label, stage)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestETAClaimAdvanceRecordsBothDurations(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	t0 := y.now
	b := y.create(t, "x", "new", "difficulty:2")
	if r := y.row(t, b.ID); !r.StayStart.Equal(t0) || !slices.Equal(r.Visited, []string{"new"}) || r.StayClaimAt != nil {
		t.Fatalf("after create: stay=%v visited=%v claim=%v", r.StayStart, r.Visited, r.StayClaimAt)
	}
	t1 := t0.Add(time.Hour)
	y.at(t1).claim(t, b.ID, "builders", "new")
	if r := y.row(t, b.ID); r.StayClaimAt == nil || !r.StayClaimAt.Equal(t1) || r.StayClaimGroup != "builders" || !r.StayStart.Equal(t0) {
		t.Fatalf("after claim: stay=%v claim=%v group=%q", r.StayStart, r.StayClaimAt, r.StayClaimGroup)
	}
	t2 := t1.Add(30 * time.Minute)
	y.at(t2).advance(t, b.ID, "review")
	c := y.cell(t, "builders", "difficulty:2", "new")
	if c.Stays != 1 || c.EntryN != 1 || c.ClaimN != 1 || c.SendBacks != 0 {
		t.Fatalf("cell = %+v, want 1 stay with both durations", c)
	}
	if c.EntryMedian != 90*time.Minute || c.ClaimMedian != 30*time.Minute {
		t.Fatalf("entry=%v claim=%v, want 1h30m and 30m", c.EntryMedian, c.ClaimMedian)
	}
	if c.EntryP90 != 90*time.Minute || c.ClaimP90 != 30*time.Minute {
		t.Fatalf("p90 entry=%v claim=%v, want 1h30m and 30m", c.EntryP90, c.ClaimP90)
	}
	if r := y.row(t, b.ID); !r.StayStart.Equal(t2) || r.StayClaimAt != nil || !slices.Equal(r.Visited, []string{"new", "review"}) {
		t.Fatalf("after advance: stay=%v claim=%v visited=%v", r.StayStart, r.StayClaimAt, r.Visited)
	}
}

func TestETAReclaimAfterFailurePairsWithReclaim(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	t0 := y.now
	b := y.create(t, "x", "new", "difficulty:2")
	t1 := t0.Add(10 * time.Minute)
	s := y.at(t1).claim(t, b.ID, "builders", "new")
	t2 := t1.Add(5 * time.Minute)
	y.at(t2)
	if err := y.EndSession(context.Background(), s.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	if r := y.row(t, b.ID); r.StayClaimAt != nil || r.StayClaimGroup != "builders" {
		t.Fatalf("after failure: claim=%v group=%q, want nil time kept group", r.StayClaimAt, r.StayClaimGroup)
	}
	t3 := t2.Add(time.Hour)
	y.at(t3).claim(t, b.ID, "builders", "new")
	t4 := t3.Add(20 * time.Minute)
	y.at(t4).advance(t, b.ID, "review")
	c := y.cell(t, "builders", "difficulty:2", "new")
	if c.Stays != 1 || c.ClaimN != 1 {
		t.Fatalf("cell = %+v, want 1 stay with 1 claim duration", c)
	}
	if c.ClaimMedian != 20*time.Minute {
		t.Fatalf("claim=%v, want 20m (the reclaim's, not the failed claim's)", c.ClaimMedian)
	}
	if c.EntryMedian != t4.Sub(t0) {
		t.Fatalf("entry=%v, want %v", c.EntryMedian, t4.Sub(t0))
	}
}

func TestETAFailedClaimWithoutReclaimCountsOnly(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "x", "new", "difficulty:3")
	s := y.at(y.now.Add(time.Hour)).claim(t, b.ID, "builders", "new")
	if err := y.EndSession(context.Background(), s.ID, "died", "gone"); err != nil {
		t.Fatal(err)
	}
	y.at(y.now.Add(30*time.Minute)).advance(t, b.ID, "review")
	c := y.cell(t, "builders", "difficulty:3", "new")
	if c.Stays != 1 || c.EntryN != 0 || c.ClaimN != 0 {
		t.Fatalf("cell = %+v, want 1 stay with no durations", c)
	}
	// No other group holds it.
	if c := y.cell(t, "reviewers", "difficulty:3", "new"); c.Stays != 0 {
		t.Fatalf("reviewers cell = %+v, want zero", c)
	}
}

func TestETADispatchFailureClaimCountsOnlyWithoutChangingBeadRevision(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "x", "new", "difficulty:2")
	sess := y.at(y.now.Add(time.Minute)).claim(t, b.ID, "builders", "new")
	before, err := y.GetBead(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := y.LogDispatch(context.Background(), b.ID, "session_stalled", "yard", map[string]any{"session": sess.ID}); err != nil {
		t.Fatal(err)
	}
	after, err := y.GetBead(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("dispatch report changed bead revision from %s to %s", before.Revision, after.Revision)
	}
	y.at(y.now.Add(time.Minute)).advance(t, b.ID, "review")
	c := y.cell(t, "builders", "difficulty:2", "new")
	if c.Stays != 1 || c.EntryN != 0 || c.ClaimN != 0 {
		t.Fatalf("cell = %+v, want a counts-only stay for the stalled claim", c)
	}
	var marked bool
	if err := y.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bETAFailed, sess.ID, &marked)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if marked {
		t.Fatalf("stage exit left a failure marker for session %s", sess.ID)
	}
}

func TestETASendBackCountsAtSourceStage(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "x", "new", "difficulty:2")
	y.at(y.now.Add(5*time.Minute)).claim(t, b.ID, "builders", "new")
	y.at(y.now.Add(10*time.Minute)).advance(t, b.ID, "review")
	y.at(y.now.Add(5*time.Minute)).claim(t, b.ID, "builders", "review")
	y.at(y.now.Add(10*time.Minute)).advance(t, b.ID, "new")
	if got, want := y.cell(t, "builders", "difficulty:2", "new"), (ETACell{}); got.Stays != 1 || got.SendBacks != 0 {
		t.Fatalf("new = %+v, want 1 stay no send-back (got %+v)", got, want)
	}
	rev := y.cell(t, "builders", "difficulty:2", "review")
	if rev.Stays != 1 || rev.SendBacks != 1 || rev.ClaimN != 1 {
		t.Fatalf("review = %+v, want 1 stay, 1 send-back, durations", rev)
	}
	// Moving on after the send-back is not one.
	y.at(y.now.Add(5*time.Minute)).claim(t, b.ID, "builders", "new")
	y.at(y.now.Add(10*time.Minute)).advance(t, b.ID, "review")
	if c := y.cell(t, "builders", "difficulty:2", "new"); c.Stays != 2 || c.SendBacks != 0 {
		t.Fatalf("new after moving on = %+v, want 2 stays no send-back", c)
	}
}

func TestETAUnclaimedStayRecordsNothing(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "x", "new", "difficulty:2")
	y.at(y.now.Add(time.Hour)).advance(t, b.ID, "review")
	if c := y.cell(t, "builders", "difficulty:2", "new"); c != (ETACell{}) {
		t.Fatalf("cell = %+v, want zero (no group ran the stay)", c)
	}
	if r := y.row(t, b.ID); !slices.Equal(r.Visited, []string{"new", "review"}) {
		t.Fatalf("visited=%v, want [new review]", r.Visited)
	}
}

func TestETASameStageMoveRecordsNothing(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "x", "new", "difficulty:2")
	t1 := y.now.Add(10 * time.Minute)
	y.at(t1).claim(t, b.ID, "builders", "new")
	y.at(t1.Add(5*time.Minute)).advance(t, b.ID, "new")
	if c := y.cell(t, "builders", "difficulty:2", "new"); c != (ETACell{}) {
		t.Fatalf("cell after same-stage move = %+v, want zero", c)
	}
	r := y.row(t, b.ID)
	if r.StayClaimAt == nil || !r.StayClaimAt.Equal(t1) {
		t.Fatalf("same-stage move cleared the claim: %v", r.StayClaimAt)
	}
	// The stay ends unowned: the exit pairs with no claim of it, and counts
	// in the group that ran it.
	y.at(y.now.Add(10*time.Minute)).advance(t, b.ID, "review")
	if c := y.cell(t, "builders", "difficulty:2", "new"); c.Stays != 1 || c.ClaimN != 0 {
		t.Fatalf("cell = %+v, want 1 stay counts-only", c)
	}
}

func TestETARingCapsAt200(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	base := y.now
	for i := 1; i <= 201; i++ {
		y.at(base)
		b := y.create(t, "x", "new", "difficulty:2")
		y.at(base.Add(time.Duration(i)*time.Minute)).claim(t, b.ID, "builders", "new")
		// Each build takes as many minutes as its number, so the oldest
		// sample is the smallest.
		y.at(base.Add(2*time.Duration(i)*time.Minute)).advance(t, b.ID, "review")
	}
	c := y.cell(t, "builders", "difficulty:2", "new")
	if c.Stays != 201 || c.ClaimN != 200 || c.EntryN != 200 {
		t.Fatalf("cell = %+v, want 201 stays capped to 200 samples", c)
	}
	var stored etaCell
	if err := y.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bETACells, etaKey("builders", "difficulty:2", "new"), &stored)
		if err != nil || !ok {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(stored.Claim) != 200 || stored.Claim[0] != 2*time.Minute || stored.Claim[199] != 201*time.Minute {
		t.Fatalf("ring holds %d samples from %v to %v, want 200 from 2m to 201m (oldest dropped)",
			len(stored.Claim), stored.Claim[0], stored.Claim[len(stored.Claim)-1])
	}
	// Claim i runs i minutes, so durations are 1m..201m and the kept 200
	// are 2m..201m, whose median is (101+102)/2 = 101.5m.
	if want := 101*time.Minute + 30*time.Second; c.ClaimMedian != want {
		t.Fatalf("median=%v, want %v", c.ClaimMedian, want)
	}
}

func TestETAVisitedCapsAt64(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	stages := make([]string, 65)
	for i := range stages {
		stages[i] = fmt.Sprintf("stage%02d", i)
	}
	b := y.create(t, "x", stages[0])
	for _, s := range stages[1:] {
		y.at(y.now.Add(time.Minute)).advance(t, b.ID, s)
	}
	r := y.row(t, b.ID)
	if len(r.Visited) != ETAVisitedCap {
		t.Fatalf("visited holds %d stages, want %d", len(r.Visited), ETAVisitedCap)
	}
	if !slices.Equal(r.Visited, stages[:ETAVisitedCap]) {
		t.Fatalf("visited keeps the first %d, got %v", ETAVisitedCap, r.Visited)
	}
}

func TestETARestartKeepsCellsAndClocks(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, NewBead{Title: "x", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if _, err := s.Advance(ctx, b.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	before, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	open, err := s.CreateBead(ctx, NewBead{Title: "open", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.Claim(ctx, open.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	var stay time.Time
	var visited []string
	if err := s.read(func(tx *bolt.Tx) error {
		r, err := loadBead(tx, open.ID)
		if err != nil {
			return err
		}
		stay, visited = r.StayStart, slices.Clone(r.Visited)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_ = sess
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	after, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("cell after reopen = %+v, want %+v (no reseed, no loss)", after, before)
	}
	if err := s.read(func(tx *bolt.Tx) error {
		r, err := loadBead(tx, open.ID)
		if err != nil {
			return err
		}
		if !r.StayStart.Equal(stay) || !slices.Equal(r.Visited, visited) || r.StayClaimAt == nil {
			t.Fatalf("open stay after reopen: start=%v visited=%v claim=%v", r.StayStart, r.Visited, r.StayClaimAt)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestETAMigrateFailedMarkerForLiveClaim(t *testing.T) {
	// Sequential: process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 1}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, NewBead{Title: "x", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogDispatch(ctx, b.ID, "session_stalled", "yard", map[string]any{"session": sess.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Schema 21 had the stay clocks but not the marker bucket's contents.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if err := tx.Bucket(bETAFailed).Delete([]byte(sess.ID)); err != nil {
			return err
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 21))
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if _, err := s.Advance(ctx, b.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	c, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	if c.Stays != 1 || c.EntryN != 0 || c.ClaimN != 0 {
		t.Fatalf("cell after migration = %+v, want counts only for the stalled claim", c)
	}
}

func TestETAHelpers(t *testing.T) {
	t.Parallel()
	if got := MedianDuration(nil); got != 0 {
		t.Fatalf("median of none = %v, want 0", got)
	}
	if got := P90Duration(nil); got != 0 {
		t.Fatalf("p90 of none = %v, want 0", got)
	}
	ds := []time.Duration{3 * time.Minute, time.Minute, 2 * time.Minute}
	if got := MedianDuration(ds); got != 2*time.Minute {
		t.Fatalf("median = %v, want 2m", got)
	}
	ds = []time.Duration{4 * time.Minute, time.Minute, 3 * time.Minute, 2 * time.Minute}
	if got := MedianDuration(ds); got != 150*time.Second {
		t.Fatalf("even median = %v, want 2m30s", got)
	}
	// p90 is index ceiling(0.9n)-1: of 10, the 9th; of 1, the only one.
	ten := make([]time.Duration, 10)
	for i := range ten {
		ten[i] = time.Duration(10-i) * time.Minute
	}
	if got := P90Duration(ten); got != 9*time.Minute {
		t.Fatalf("p90 of 1..10m = %v, want 9m", got)
	}
	if got := P90Duration([]time.Duration{5 * time.Minute}); got != 5*time.Minute {
		t.Fatalf("p90 of one = %v, want 5m", got)
	}
	if !IsSendBack([]string{"new", "review"}, "review", "new") {
		t.Fatal("review -> new after [new review] is not a send-back")
	}
	if IsSendBack([]string{"new", "review"}, "new", "review") {
		t.Fatal("new -> review after [new review] is a send-back")
	}
	if IsSendBack([]string{"new"}, "new", "review") {
		t.Fatal("new -> review with review unvisited is a send-back")
	}
	if IsSendBack([]string{"new"}, "new", "new") {
		t.Fatal("same-stage move is a send-back")
	}
	if IsSendBack(nil, "new", "review") {
		t.Fatal("move with nothing visited is a send-back")
	}
	if got := AppendVisited([]string{"new"}, "new"); !slices.Equal(got, []string{"new"}) {
		t.Fatalf("revisit appends: %v", got)
	}
	full := make([]string, ETAVisitedCap)
	for i := range full {
		full[i] = "s"
	}
	if got := AppendVisited(full, "new"); len(got) != ETAVisitedCap {
		t.Fatalf("cap %d grows to %d", ETAVisitedCap, len(got))
	}
	if got := DifficultyLabel([]string{"size:large", "difficulty:3"}); got != "difficulty:3" {
		t.Fatalf("label=%q, want difficulty:3", got)
	}
	if got := DifficultyLabel([]string{"size:large"}); got != "-" {
		t.Fatalf("label=%q, want -", got)
	}
	t0 := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	pairs := PairETABuilds([]ETABuildClaim{
		{Bead: "b", Group: "builders", Stage: "new", Session: "good", At: t0, Seq: 1},
		{Bead: "b", Group: "builders", Stage: "new", Session: "failed", At: t0.Add(time.Minute), Seq: 2},
	}, []ETABuildExit{
		{Bead: "b", Group: "builders", Stage: "new", At: t0.Add(2 * time.Minute), Seq: 3},
		{Bead: "b", Group: "builders", Stage: "new", At: t0.Add(3 * time.Minute), Seq: 4},
	}, map[string]uint64{"failed": 2})
	if pairs[0] != (ETABuildPair{Claim: -1, Latest: 1}) || pairs[1] != (ETABuildPair{Claim: -1, Latest: -1}) {
		t.Fatalf("failed latest claim or later exit reused an earlier claim: %+v", pairs)
	}
	pairs = PairETABuilds([]ETABuildClaim{
		{Bead: "b", Group: "builders", Stage: "new", Session: "late", At: t0, Seq: 1},
	}, []ETABuildExit{
		{Bead: "b", Group: "builders", Stage: "new", At: t0.Add(time.Minute), Seq: 2},
	}, map[string]uint64{"late": 3})
	if pairs[0].Claim != 0 {
		t.Fatalf("a failure logged after the exit invalidated its build: %+v", pairs)
	}
}

func TestETANegativeDurationsDoNotBecomeSamples(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	d := -time.Minute
	if err := y.write(func(w *wtx) error {
		return recordETA(w.Tx, "builders", "difficulty:2", "new", &d, &d, false)
	}); err != nil {
		t.Fatal(err)
	}
	if c := y.cell(t, "builders", "difficulty:2", "new"); c.Stays != 1 || c.EntryN != 0 || c.ClaimN != 0 {
		t.Fatalf("negative durations = %+v, want a stay with no samples", c)
	}
}

// TestETASeedReplaysTheLog builds history with the live path, ages the file
// to schema 20 without ETA state, and reopens it: the seed rebuilds the
// cells from the events and starts the open stays' clocks.
func TestETASeedReplaysTheLog(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	mk := func(title, stage string, labels ...string) *Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, NewBead{Title: title, Stage: stage, Labels: labels}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	// A clean build.
	b1 := mk("one", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	if _, err := s.Claim(ctx, b1.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Minute)
	if _, err := s.Advance(ctx, b1.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Failed once, then built on the reclaim.
	b2 := mk("two", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	sess, err := s.Claim(ctx, b2.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if err := s.EndSession(ctx, sess.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err := s.Claim(ctx, b2.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Minute)
	if _, err := s.Advance(ctx, b2.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Failed with no reclaim: counts only.
	b3 := mk("three", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	sess, err = s.Claim(ctx, b3.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if err := s.EndSession(ctx, sess.ID, "failed", "boom"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := s.Advance(ctx, b3.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// A released claim ends without an owner. Replay must match the live
	// path's counts-only attribution instead of pairing its old claim time.
	bReleased := mk("released", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	if _, err := s.Claim(ctx, bReleased.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.Release(ctx, bReleased.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := s.Advance(ctx, bReleased.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// A stalled report leaves the live session in place, but the claim is
	// excluded from durations both now and when the retained log is seeded.
	bStalled := mk("stalled", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	stalled, err := s.Claim(ctx, bStalled.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogDispatch(ctx, bStalled.ID, "session_stalled", "yard", map[string]any{"session": stalled.ID}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.Advance(ctx, bStalled.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Unclaimed: in no cell.
	b4 := mk("four", "new", "difficulty:2")
	now = now.Add(time.Hour)
	if _, err := s.Advance(ctx, b4.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// A send-back at review.
	now = now.Add(10 * time.Minute)
	if _, err := s.Claim(ctx, b1.ID, "builders", "review", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(15 * time.Minute)
	if _, err := s.Advance(ctx, b1.ID, "new", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Two open stays: one claimed, one waiting.
	openClaimed := mk("held", "new", "difficulty:2")
	now = now.Add(10 * time.Minute)
	live, err := s.Claim(ctx, openClaimed.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	liveStart := live.StartedAt
	openWaiting := mk("waiting", "review", "difficulty:2")
	_ = openWaiting
	wantNew, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	wantRev, err := s.ETACell(ctx, "builders", "difficulty:2", "review")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// Age the file to schema 20: no cells, no stay fields.
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bETACells); err != nil {
			return err
		}
		if err := each(tx, bBeads, "", func(k []byte, r *beadRow) error {
			r.StayStart, r.StayClaimAt, r.StayClaimGroup, r.StayClaimSession, r.Visited =
				time.Time{}, nil, "", "", nil
			return put(tx, bBeads, string(k), r)
		}); err != nil {
			return err
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 20))
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	seeded := now
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	gotNew, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	gotRev, err := s.ETACell(ctx, "builders", "difficulty:2", "review")
	if err != nil {
		t.Fatal(err)
	}
	if gotNew != wantNew {
		t.Fatalf("seeded new = %+v, want live %+v", gotNew, wantNew)
	}
	if gotRev != wantRev {
		t.Fatalf("seeded review = %+v, want live %+v", gotRev, wantRev)
	}
	if wantNew.Stays != 5 || wantNew.ClaimN != 2 || wantRev.SendBacks != 1 {
		t.Fatalf("live cells new=%+v review=%+v, want 5 stays, 2 builds and a send-back", wantNew, wantRev)
	}
	if err := s.read(func(tx *bolt.Tx) error {
		r, err := loadBead(tx, openClaimed.ID)
		if err != nil {
			return err
		}
		if !r.StayStart.Equal(seeded) || r.StayClaimAt == nil || !r.StayClaimAt.Equal(liveStart) ||
			r.StayClaimGroup != "builders" || !slices.Equal(r.Visited, []string{"new"}) {
			t.Fatalf("seeded claimed stay: start=%v claim=%v group=%q visited=%v",
				r.StayStart, r.StayClaimAt, r.StayClaimGroup, r.Visited)
		}
		r, err = loadBead(tx, openWaiting.ID)
		if err != nil {
			return err
		}
		if !r.StayStart.Equal(seeded) || r.StayClaimAt != nil || !slices.Equal(r.Visited, []string{"review"}) {
			t.Fatalf("seeded waiting stay: start=%v claim=%v visited=%v", r.StayStart, r.StayClaimAt, r.Visited)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestETASeedUsesDifficultyAtEachExit(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, NewBead{Title: "change difficulty", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := s.Claim(ctx, b.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Minute)
	if _, err := s.Advance(ctx, b.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if _, err := s.UpdateBead(ctx, b.ID, BeadPatch{
		AddLabels: []string{"difficulty:3"}, RemoveLabel: []string{"difficulty:2"},
	}, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(5 * time.Minute)
	if _, err := s.Claim(ctx, b.ID, "builders", "review", "yard"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(10 * time.Minute)
	if _, err := s.Advance(ctx, b.ID, "done", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	beforeNew, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	beforeReview, err := s.ETACell(ctx, "builders", "difficulty:3", "review")
	if err != nil {
		t.Fatal(err)
	}
	if beforeNew.ClaimN != 1 || beforeReview.ClaimN != 1 {
		t.Fatalf("live cells new=%+v review=%+v, want one sample in each difficulty cell", beforeNew, beforeReview)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if err := tx.DeleteBucket(bETACells); err != nil {
			return err
		}
		if err := each(tx, bBeads, "", func(k []byte, r *beadRow) error {
			r.StayStart, r.StayClaimAt, r.StayClaimGroup, r.StayClaimSession, r.Visited = time.Time{}, nil, "", "", nil
			return put(tx, bBeads, string(k), r)
		}); err != nil {
			return err
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 20))
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	afterNew, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	afterReview, err := s.ETACell(ctx, "builders", "difficulty:3", "review")
	if err != nil {
		t.Fatal(err)
	}
	if afterNew != beforeNew || afterReview != beforeReview {
		t.Fatalf("seeded cells new=%+v review=%+v, want live new=%+v review=%+v", afterNew, afterReview, beforeNew, beforeReview)
	}
}

// A schema 20 seed consumes all claims belonging to an ended stay, resets
// entry clocks on reopen, and agrees with the live write path.
func TestETASeedPairsLatestClaimAndResetsReopenedClock(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	clockAt := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	base := clockAt
	y := &etaTest{now: clockAt}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	var err error
	y.Store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	t.Cleanup(func() { y.Close(ctx) })
	if err := y.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}

	// A successful reclaim ends the first stay. A later unclaimed exit from
	// the same stage must not reuse the failed claim from that stay.
	b := y.create(t, "reclaim and return", "new", "difficulty:2")
	first := y.at(clockAt.Add(time.Minute)).claim(t, b.ID, "builders", "new")
	if err := y.EndSession(ctx, first.ID, "failed", "interrupted"); err != nil {
		t.Fatal(err)
	}
	y.at(clockAt.Add(2*time.Hour)).claim(t, b.ID, "builders", "new")
	y.at(clockAt.Add(3*time.Hour)).advance(t, b.ID, "review")
	y.at(clockAt.Add(4*time.Hour)).claim(t, b.ID, "builders", "review")
	y.at(clockAt.Add(5*time.Hour)).advance(t, b.ID, "new")
	y.at(clockAt.Add(6*time.Hour)).advance(t, b.ID, "review")
	wantNew, err := y.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}

	// A direct close and reopen begin a new stay in the same stage. The
	// re-opened review sample should omit the time spent closed.
	b2 := y.at(clockAt.Add(7*time.Hour)).create(t, "reopen", "new", "difficulty:3")
	y.at(clockAt.Add(7*time.Hour+time.Minute)).claim(t, b2.ID, "builders", "new")
	y.at(clockAt.Add(8*time.Hour)).advance(t, b2.ID, "review")
	if _, err := y.CloseBead(ctx, b2.ID, "paused", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	clockAt = clockAt.Add(10 * time.Hour)
	y.at(clockAt)
	if _, err := y.Reopen(ctx, b2.ID, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	y.at(clockAt).claim(t, b2.ID, "builders", "review")
	y.at(clockAt.Add(30*time.Minute)).advance(t, b2.ID, "done")
	wantReview, err := y.ETACell(ctx, "builders", "difficulty:3", "review")
	if err != nil {
		t.Fatal(err)
	}
	if wantReview.EntryMedian != 30*time.Minute || wantReview.ClaimMedian != 30*time.Minute {
		t.Fatalf("live reopened review cell = %+v, want both durations 30m", wantReview)
	}

	// A failed reclaim supersedes an earlier completed attempt in that stay;
	// its exit is counts-only and cannot fall back to the earlier claim.
	b3 := y.at(base.Add(11*time.Hour)).create(t, "failed last claim", "new", "difficulty:4")
	y.at(base.Add(11*time.Hour+time.Minute)).claim(t, b3.ID, "builders", "new")
	if _, err := y.Release(ctx, b3.ID, "retry", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	failedLast := y.at(base.Add(11*time.Hour+10*time.Minute)).claim(t, b3.ID, "builders", "new")
	if err := y.EndSession(ctx, failedLast.ID, "failed", "interrupted"); err != nil {
		t.Fatal(err)
	}
	y.at(base.Add(11*time.Hour+20*time.Minute)).advance(t, b3.ID, "review")
	wantFailed, err := y.ETACell(ctx, "builders", "difficulty:4", "new")
	if err != nil {
		t.Fatal(err)
	}
	if wantFailed.Stays != 1 || wantFailed.EntryN != 0 || wantFailed.ClaimN != 0 {
		t.Fatalf("live failed-last cell = %+v, want counts only", wantFailed)
	}

	// Claims before a direct close must not be attributed to an unclaimed
	// stage exit after the bead is reopened.
	b4 := y.at(base.Add(12*time.Hour)).create(t, "reopen unclaimed", "new", "difficulty:5")
	y.at(base.Add(12*time.Hour+time.Minute)).claim(t, b4.ID, "builders", "new")
	y.at(base.Add(13*time.Hour)).advance(t, b4.ID, "review")
	y.at(base.Add(13*time.Hour+time.Minute)).claim(t, b4.ID, "builders", "review")
	y.at(base.Add(13*time.Hour + 10*time.Minute))
	if _, err := y.CloseBead(ctx, b4.ID, "paused", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	y.at(base.Add(14 * time.Hour))
	if _, err := y.Reopen(ctx, b4.ID, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	y.at(base.Add(14*time.Hour+10*time.Minute)).advance(t, b4.ID, "done")
	wantUnclaimed, err := y.ETACell(ctx, "builders", "difficulty:5", "review")
	if err != nil {
		t.Fatal(err)
	}
	if wantUnclaimed != (ETACell{}) {
		t.Fatalf("live reopened unclaimed cell = %+v, want zero", wantUnclaimed)
	}
	if err := y.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stripETAState(t, path, 20)

	clockAt = base.Add(12 * time.Hour)
	y.at(clockAt)
	y.Store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	gotNew, err := y.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	gotReview, err := y.ETACell(ctx, "builders", "difficulty:3", "review")
	if err != nil {
		t.Fatal(err)
	}
	if gotNew != wantNew {
		t.Fatalf("seeded new = %+v, want live %+v", gotNew, wantNew)
	}
	if gotReview != wantReview {
		t.Fatalf("seeded reopened review = %+v, want live %+v", gotReview, wantReview)
	}
	gotFailed, err := y.ETACell(ctx, "builders", "difficulty:4", "new")
	if err != nil {
		t.Fatal(err)
	}
	if gotFailed != wantFailed {
		t.Fatalf("seeded failed-last = %+v, want live %+v", gotFailed, wantFailed)
	}
	gotUnclaimed, err := y.ETACell(ctx, "builders", "difficulty:5", "review")
	if err != nil {
		t.Fatal(err)
	}
	if gotUnclaimed != wantUnclaimed {
		t.Fatalf("seeded reopened unclaimed = %+v, want live %+v", gotUnclaimed, wantUnclaimed)
	}
}

func TestETAMigrateIgnoresFailureForDifferentBead(t *testing.T) {
	// Sequential: the package's fake clock and process environment.
	dir := t.TempDir()
	path := filepath.Join(dir, "y.db")
	t.Setenv("YARDR_"+NoSyncEnv, "1")
	clockAt := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return clockAt }
	t.Cleanup(func() { clock = time.Now })
	ctx := context.Background()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	claimed, err := s.CreateBead(ctx, NewBead{Title: "claimed", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateBead(ctx, NewBead{Title: "other", Stage: "new", Labels: []string{"difficulty:2"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	clockAt = clockAt.Add(time.Minute)
	sess, err := s.Claim(ctx, claimed.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LogDispatch(ctx, other.ID, "session_stalled", "yard", map[string]any{"session": sess.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	stripETAState(t, path, 21)

	clockAt = clockAt.Add(time.Minute)
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if _, err := s.Advance(ctx, claimed.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	c, err := s.ETACell(ctx, "builders", "difficulty:2", "new")
	if err != nil {
		t.Fatal(err)
	}
	if c.Stays != 1 || c.EntryN != 1 || c.ClaimN != 1 {
		t.Fatalf("cell after migration = %+v, want durations for the unaffected live claim", c)
	}
}

func TestETAForeignFailureEventDoesNotResolveTheLocalSession(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	b := y.create(t, "local", "new", "difficulty:2")
	sess := y.claim(t, b.ID, "builders", "new")
	if err := y.LogDispatch(context.Background(), "remote-1@north", "session_stalled", "yard", map[string]any{"session": sess.ID}); err != nil {
		t.Fatalf("log a foreign-bead dispatch event: %v", err)
	}
	var marked bool
	if err := y.read(func(tx *bolt.Tx) error {
		_, err := get(tx, bETAFailed, sess.ID, &marked)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if marked {
		t.Fatalf("foreign-bead failure marked the local claim %s", sess.ID)
	}
}

// stripETAState ages a store to the schema just before the requested ETA
// migration, as if it had been written by that version.
func stripETAState(t *testing.T, path string, version uint64) {
	t.Helper()
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		if version == 20 {
			if tx.Bucket(bETACells) != nil {
				if err := tx.DeleteBucket(bETACells); err != nil {
					return err
				}
			}
			if tx.Bucket(bETAFailed) != nil {
				if err := tx.DeleteBucket(bETAFailed); err != nil {
					return err
				}
			}
			if err := each(tx, bBeads, "", func(k []byte, r *beadRow) error {
				r.StayStart, r.StayClaimAt, r.StayClaimGroup, r.StayClaimSession, r.Visited = time.Time{}, nil, "", "", nil
				return put(tx, bBeads, string(k), r)
			}); err != nil {
				return err
			}
		}
		if version == 21 {
			c := tx.Bucket(bETAFailed).Cursor()
			for k, _ := c.First(); k != nil; k, _ = c.Next() {
				if err := c.Delete(); err != nil {
					return err
				}
			}
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, version))
	})
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
}
