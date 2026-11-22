package app

import (
	"math/rand/v2"
	"time"
)

// backoffCap bounds the gap between two polls of one target, in pool ticks:
// fifteen minutes at the default. A target that has been dark for days costs
// four checks an hour instead of one per tick.
const backoffCap = 60

// backoffGap is how long a target waits before its next poll after misses
// consecutive polls found it offline: one tick, then double, up to the cap.
// Jitter keeps targets that started together from staying in lockstep. The
// jitter only shortens the wait, so the cap stays a hard ceiling. A target that
// has just been live passes misses 0 and is polled at once, because a stream
// that ended can come back within a minute.
func backoffGap(tick time.Duration, misses int) time.Duration {
	if misses < 1 {
		return 0
	}
	steps := min(1<<min(misses-1, 6), backoffCap)
	base := tick * time.Duration(steps)
	return time.Duration(float64(base) * (0.9 + rand.Float64()*0.1))
}
