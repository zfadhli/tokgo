package app

import (
	"testing"
	"time"
)

// steps is the multiplier backoffGap picks, before the jitter.
func wantSteps(misses int) int {
	if misses < 1 {
		return 0
	}
	return min(1<<min(misses-1, 6), backoffCap)
}

func TestBackoffGapDoublesUntilTheCap(t *testing.T) {
	const tick = 15 * time.Second

	cases := []struct {
		misses  int
		wantMul int
	}{
		{0, 0}, {1, 1}, {2, 2}, {3, 4}, {4, 8}, {5, 16}, {6, 32}, {7, 60}, {8, 60}, {200, 60},
	}
	for _, tc := range cases {
		base := tick * time.Duration(tc.wantMul)
		low := time.Duration(float64(base) * 0.9)
		for range 20 { // the jitter is random, so probe the band
			got := backoffGap(tick, tc.misses)
			if tc.wantMul == 0 {
				if got != 0 {
					t.Fatalf("misses %d gap = %v, want no wait after a live hit", tc.misses, got)
				}
				continue
			}
			if got < low || got > base {
				t.Fatalf("misses %d gap = %v, want within [%v, %v]", tc.misses, got, low, base)
			}
		}
	}
}

func TestBackoffGapKeepsTheCapAsACeiling(t *testing.T) {
	const tick = time.Minute
	cap := tick * backoffCap

	for range 200 {
		if got := backoffGap(tick, 50); got > cap {
			t.Fatalf("gap = %v, want at most the %v cap", got, cap)
		}
	}
}

func TestBackoffGapSpreadsTargetsThatStartedTogether(t *testing.T) {
	const tick = time.Second
	seen := make(map[time.Duration]struct{}, 20)
	for range 20 {
		seen[backoffGap(tick, 4)] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("twenty gaps collapsed to %v, want jitter to spread them", seen)
	}
}
