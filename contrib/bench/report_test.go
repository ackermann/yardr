package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func TestGrowth(t *testing.T) {
	sizes := []int{250, 2000, 20000}
	for _, tc := range []struct {
		name string
		vals []float64
		want string
	}{
		{"a fixed cost", []float64{12, 13, 12.5}, "flat"},
		// 12 ms to start, and 10 microseconds a bead.
		{"a fixed cost and one per bead", []float64{14.5, 32, 212}, "linear"},
		{"quadratic", []float64{1, 64, 6400}, "worse than linear"},
		{"a square root", []float64{16, 45, 141}, "less than linear"},
		// Nothing to tell in the first step: the last alone, which
		// grew as the yard did.
		{"only the last step grows, as the yard", []float64{12, 12.2, 120}, "linear"},
		{"only the last step grows, a little", []float64{12, 12.2, 19}, "linear or less"},
		{"throughput that drops", []float64{40, 30, 10}, "falls"},
	} {
		got := growth(sizes, tc.vals)
		if !strings.HasSuffix(got, " "+tc.want) || strings.HasSuffix(got, "worse than "+tc.want) || strings.HasSuffix(got, "less than "+tc.want) {
			t.Errorf("%s: growth(%v) = %q, want %q", tc.name, tc.vals, got, tc.want)
		}
	}
	if got := growth(sizes, []float64{0, 1, 2}); got != "" {
		t.Errorf("a measure that starts at zero has no factor: %q", got)
	}
}

func TestQuantiles(t *testing.T) {
	var ds []time.Duration
	for i := 20; i >= 1; i-- {
		ds = append(ds, time.Duration(i)*time.Millisecond)
	}
	// Of 20 runs the median is between the 10th and 11th, and the 95th
	// percentile the 19th.
	if st := quantiles(ds); st.Med != 10.5 || st.P95 != 19 {
		t.Errorf("quantiles(1..20ms) = %+v, want 10.5 and 19", st)
	}
	if st := quantiles(ds[:1]); st.Med != 20 || st.P95 != 20 {
		t.Errorf("quantiles of one run = %+v", st)
	}
}

func TestCPUSecs(t *testing.T) {
	for in, want := range map[string]float64{"0:01.25": 1.25, "12:03.50": 723.5, "00:00:07": 7, "1-02:00:00": 93600} {
		if got, err := cpuSecs(in); err != nil || got != want {
			t.Errorf("cpuSecs(%q) = %v, %v, want %v", in, got, err, want)
		}
	}
	if _, err := cpuSecs("soon"); err == nil {
		t.Error("cpuSecs took a word for a time")
	}
}

// The benchmark stops a yard's server and writes its store, so it must
// refuse every home that is not an isolated yard's.
func TestIsolatedHome(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "")
	for _, home := range []string{"", "/Users/someone/.yardr", t.TempDir(), "/tmp/iy.none/y"} {
		t.Setenv("YARDR_HOME", home)
		if _, _, err := isolatedHome(); err == nil {
			t.Errorf("YARDR_HOME=%q was taken for an isolated yard", home)
		}
	}
}

func TestOpenStoreNoSyncDoesNotLeak(t *testing.T) {
	t.Setenv(store.NoSyncEnv, "")
	s, err := openStore(filepath.Join(t.TempDir(), "yard.db"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	if got := os.Getenv(store.NoSyncEnv); got != "" {
		t.Errorf("%s remains %q after opening the store", store.NoSyncEnv, got)
	}
}
