package store_test

import (
	"context"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A rotated GOCACHE directory is recorded with its rotation's time, listed
// for the landing that removes it, and dropped from the record once it is
// gone. Dropping what was never recorded is no error.
func TestGOCACHEOldRoundTrip(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

	if err := st.RecordGOCACHEOld(ctx, "/cache/go-build-8", at); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveGOCACHEOld(ctx, "/cache/go-build-7"); err != nil {
		t.Fatal(err)
	}
	olds, err := st.GOCACHEOldDirs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(olds) != 1 || olds[0].Dir != "/cache/go-build-8" || !olds[0].At.Equal(at) {
		t.Fatalf("olds = %+v", olds)
	}
	if err := st.RemoveGOCACHEOld(ctx, "/cache/go-build-8"); err != nil {
		t.Fatal(err)
	}
	if olds, err := st.GOCACHEOldDirs(ctx); err != nil || len(olds) != 0 {
		t.Fatalf("olds = %+v, err = %v", olds, err)
	}
}

// disk_keep_gb reads as its gigabytes, as 30 while unset, unreadable or
// negative, and as 0 while off.
func TestDiskKeepGB(t *testing.T) {
	for _, tc := range []struct {
		in   map[string]string
		want int
	}{
		{nil, 30},
		{map[string]string{"disk_keep_gb": ""}, 30},
		{map[string]string{"disk_keep_gb": "50"}, 50},
		{map[string]string{"disk_keep_gb": "0"}, 0},
		{map[string]string{"disk_keep_gb": "-1"}, 30},
		{map[string]string{"disk_keep_gb": "soon"}, 30},
	} {
		if got := store.DiskKeepGB(tc.in); got != tc.want {
			t.Errorf("DiskKeepGB(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
