package yards

import (
	"fmt"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func TestParseRefusal(t *testing.T) {
	const until int64 = 1790000000123456789
	got := parseRefusal(fmt.Sprintf("%s:30:%d", store.RefusedRate, until))
	if got.Reason != store.RefusedRate || got.Limit != 30 || got.Until == nil || !got.Until.Equal(time.Unix(0, until)) {
		t.Fatalf("rate refusal = %+v", got)
	}

	other := "too_large:30:1790000000123456789"
	got = parseRefusal(other)
	if got.Reason != other || got.Limit != 0 || got.Until != nil {
		t.Fatalf("other refusal = %+v, want its full reason", got)
	}
}
