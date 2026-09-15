package catalog

import (
	"math"
	"testing"
)

func TestSafeOffsetNormalPages(t *testing.T) {
	cases := []struct {
		page, pageSize, want int
	}{
		{1, 20, 0},
		{2, 20, 20},
		{3, 20, 40},
		{1, 0, 0},   // pageSize<=0 falls back to DefaultPageSize, but page 1 is always offset 0
		{0, 20, 0},  // defensive: shouldn't happen (httpapi rejects page<1), but must not go negative
		{-5, 20, 0}, // same
	}
	for _, c := range cases {
		if got := safeOffset(c.page, c.pageSize); got != c.want {
			t.Errorf("safeOffset(%d, %d) = %d, want %d", c.page, c.pageSize, got, c.want)
		}
	}
}

func TestSafeOffsetClampsHugePageWithoutOverflow(t *testing.T) {
	// A page value near the top of the int range would make (page-1)*pageSize
	// wrap around (potentially negative), which Postgres rejects as an
	// invalid OFFSET. safeOffset must clamp instead of overflowing.
	got := safeOffset(math.MaxInt, 20)
	if got < 0 {
		t.Fatalf("safeOffset(MaxInt, 20) = %d, want a non-negative, clamped value", got)
	}
	if got > math.MaxInt32 {
		t.Fatalf("safeOffset(MaxInt, 20) = %d, want it clamped near math.MaxInt32/pageSize bounds", got)
	}
}

func TestSafeOffsetMonotonicBelowClamp(t *testing.T) {
	// Below the clamp ceiling, offset must still grow normally with page.
	if got, want := safeOffset(1000, 20), 999*20; got != want {
		t.Errorf("safeOffset(1000, 20) = %d, want %d", got, want)
	}
}
