package store

import (
	"sync"
	"testing"
)

// The fetch of a released held delivery's files is registered by the
// server from serveHooks, after its carry and daemon watchers have
// started writing, so registering it must not race a write in flight:
// registering and clearing it while writes run is safe, and a release
// after registration hands the delivery's files to the fetch. Under
// -race the first half is the regression test for the race the
// assembly's gate found between SetHeldFilesFetch and Store.write.
func TestHeldFilesFetchRegisteredDuringWrites(t *testing.T) {
	y := mappingYard(t)
	type call struct {
		hook  string
		seq   uint64
		files []HeldFile
	}
	var mu sync.Mutex
	var calls []call
	fetch := func(hook string, seq uint64, files []HeldFile) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, call{hook, seq, append([]HeldFile(nil), files...)})
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				if err := y.LogEvent(y.ctx, y.bead, "probe", "me", nil); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	for j := 0; j < 25; j++ {
		y.SetHeldFilesFetch(fetch)
		y.SetHeldFilesFetch(nil)
	}
	wg.Wait()

	y.SetHeldFilesFetch(fetch)
	env := map[string]any{
		"fields": map[string]any{"sender": []string{"U777"}},
		"files":  []map[string]any{{"id": "f-1", "name": "note.txt"}},
	}
	if _, err := y.HoldIdentity(y.ctx, "talk", "talk", "U777", "C1", env, []byte("held body"), []string{"fetch-1"}); err != nil {
		t.Fatal(err)
	}
	card := heldCardOf(t, y, "U777")
	pick := pickFor(t, y, card.Bead, "ada")
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pick, "", "mckean", true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0].hook != "talk" || calls[0].seq == 0 ||
		len(calls[0].files) != 1 || calls[0].files[0].ID != "f-1" || calls[0].files[0].Name != "note.txt" {
		t.Fatalf("the fetch was called with %+v, want one call for talk with f-1/note.txt", calls)
	}
}
