package store_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// What the receiver fetched of a delivery's files is kept beside its event,
// once: each a blob the yard holds, or the reason it was not taken, and the
// write that keeps it logs a hook_files event naming the delivery, the word
// to read the files again on. A blob a kept delivery names is served by its
// hash and outlives the prune of blobs; once the delivery goes with the
// log, its record goes with it, and the blob is swept as one nothing names.
func TestDeliveryFiles(t *testing.T) {
	t.Parallel()
	api, s := storetest.NewOwned(t)
	ctx := context.Background()
	seq, _, err := s.RecordHook(ctx, "telegram", map[string]any{"source": "telegram", "type": "message"}, []byte(`{"update_id":1}`), []string{"d1"})
	if err != nil {
		t.Fatal(err)
	}
	young, _, err := s.RecordHook(ctx, "telegram", map[string]any{"source": "telegram", "type": "message"}, []byte(`{"update_id":2}`), []string{"d2"})
	if err != nil {
		t.Fatal(err)
	}
	named, err := api.PutBlob(ctx, pngBytes)
	if err != nil {
		t.Fatal(err)
	}
	kept, err := api.PutBlob(ctx, jpegBytes)
	if err != nil {
		t.Fatal(err)
	}
	named.Name = "shed.png"
	over := store.BlobRef{Hash: "ignored", Name: "plans.pdf", Bytes: 1048576, NotTaken: "document of 1048576 bytes, over the yard's limit of 8192"}
	// The deliveries the hook_files events name, oldest first. Each is
	// about no bead and by the hook the delivery came by.
	filed := func() []uint64 {
		t.Helper()
		evs, err := api.EventsOfKind(ctx, store.HookFiles, 10)
		if err != nil {
			t.Fatal(err)
		}
		var seqs []uint64
		for _, e := range evs {
			n, _ := e.Data["seq"].(float64)
			seqs = append(seqs, uint64(n))
			if e.Actor != "hook:telegram" || e.Bead != nil {
				t.Errorf("hook_files event %d is by %q about %v, want by hook:telegram about no bead", e.Seq, e.Actor, e.Bead)
			}
		}
		return seqs
	}
	if got, err := api.DeliveryFiles(ctx, seq); err != nil || got != nil || filed() != nil {
		t.Fatalf("before any record: %+v, %v, hook_files for %v", got, err, filed())
	}
	if err := s.RecordDeliveryFiles(ctx, seq, []store.BlobRef{*named, over}); err != nil {
		t.Fatal(err)
	}
	// The event is in the log once the record is read: it is the same write.
	if want := []uint64{seq}; !slices.Equal(filed(), want) {
		t.Fatalf("hook_files after the first record names %v, want %v", filed(), want)
	}
	if err := s.RecordDeliveryFiles(ctx, young, []store.BlobRef{*kept}); err != nil {
		t.Fatal(err)
	}
	if want := []uint64{seq, young}; !slices.Equal(filed(), want) {
		t.Fatalf("hook_files after both records names %v, want %v", filed(), want)
	}
	got, err := api.DeliveryFiles(ctx, seq)
	if err != nil || len(got) != 2 || got[0] != *named || got[1].Hash != "" || got[1].NotTaken != over.NotTaken || got[1].Bytes != 1048576 {
		t.Fatalf("DeliveryFiles = %+v, %v", got, err)
	}
	// What a delivery carried is recorded once, of a delivery, and of
	// blobs the yard holds.
	if err := s.RecordDeliveryFiles(ctx, seq, []store.BlobRef{*named}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Errorf("a second record: %v", err)
	}
	if err := s.RecordDeliveryFiles(ctx, seq+100, []store.BlobRef{*named}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a record for no event: %v", err)
	}
	if err := s.BulkEvents(1, "noise", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordDeliveryFiles(ctx, head(t, api), []store.BlobRef{*named}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a record for an event that is no delivery: %v", err)
	}
	if err := s.RecordDeliveryFiles(ctx, young, []store.BlobRef{{Hash: strings.Repeat("0", 64)}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a record of a blob the yard does not hold: %v", err)
	}
	if err := s.RecordDeliveryFiles(ctx, young, []store.BlobRef{{Name: "plans\x1b[2K.pdf", NotTaken: "too big"}}); err == nil {
		t.Error("a record of a name no letter may carry was kept")
	}
	// A record that is refused logs nothing: the two above stand alone.
	if want := []uint64{seq, young}; !slices.Equal(filed(), want) {
		t.Fatalf("hook_files after the refused records names %v, want %v", filed(), want)
	}
	// A blob a kept delivery names is served by its hash, under the name
	// the delivery gives it; anything else is not found.
	if ref, err := api.DeliveryBlob(ctx, named.Hash); err != nil || ref.Name != "shed.png" || ref.Bytes != int64(len(pngBytes)) {
		t.Fatalf("DeliveryBlob = %+v, %v", ref, err)
	}
	for _, hash := range []string{strings.Repeat("0", 64), "nohash", ""} {
		if _, err := api.DeliveryBlob(ctx, hash); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeliveryBlob(%q): %v", hash, err)
		}
	}
	// Past their grace, the blobs kept deliveries name stay.
	old := time.Now().Add(-2 * store.BlobGrace)
	for _, ref := range []*store.BlobRef{named, kept} {
		if err := os.Chtimes(s.BlobPath(ref.Hash), old, old); err != nil {
			t.Fatal(err)
		}
	}
	if res, err := api.Prune(ctx, store.Retention{}, "me"); err != nil || res.Blobs != 0 || len(blobFiles(t, s)) != 2 {
		t.Fatalf("prune while the deliveries are kept = %+v, %v; files %v", res, err, blobFiles(t, s))
	}
	// The first delivery goes with the log, and its record with it: its
	// blob is named by nothing and swept, the other delivery's stays.
	if err := s.Backdate(seq, seq, time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, err := api.Prune(ctx, keep90, "me"); err != nil || res.Events != 1 || res.Blobs != 1 {
		t.Fatalf("prune of the delivery = %+v, %v", res, err)
	}
	if got, err := api.DeliveryFiles(ctx, seq); err != nil || got != nil {
		t.Fatalf("the pruned delivery's files = %+v, %v", got, err)
	}
	if _, err := api.DeliveryBlob(ctx, named.Hash); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the pruned delivery's blob is still served: %v", err)
	}
	if files := blobFiles(t, s); len(files) != 1 || files[0] != kept.Hash {
		t.Fatalf("after the prune the yard holds %v, want the kept delivery's %s", files, kept.Hash)
	}
	if got, err := api.DeliveryFiles(ctx, young); err != nil || len(got) != 1 || got[0].Hash != kept.Hash {
		t.Fatalf("the kept delivery's files = %+v, %v", got, err)
	}
}
