package kvm

import (
	"fmt"
	"slices"
	"time"
)

// linkStats records how evenly mouse motion arrives from the local input
// devices while the peer has control. A mouse sends every few milliseconds
// while it moves; long gaps mean the input side (for example a Bluetooth
// mouse sharing the radio with the link to the peer) is delaying events.
type linkStats struct {
	gaps []time.Duration
}

// pauseGap is a gap so long it is the user stopping, not lag.
const pauseGap = 250 * time.Millisecond

func (s *linkStats) input(gap time.Duration) {
	if gap >= pauseGap || len(s.gaps) >= 20000 {
		return
	}
	s.gaps = append(s.gaps, gap)
}

// summary is e.g. "1520 motion frames, gap p50 2ms, p95 9ms, max 61ms".
func (s *linkStats) summary() string {
	if len(s.gaps) < 20 {
		return ""
	}
	g := slices.Clone(s.gaps)
	slices.Sort(g)
	pct := func(p float64) time.Duration { return g[int(p*float64(len(g)-1))] }
	ms := func(d time.Duration) string { return fmt.Sprintf("%.0fms", float64(d)/float64(time.Millisecond)) }
	slow := 0
	for _, d := range g {
		if d > 30*time.Millisecond {
			slow++
		}
	}
	return fmt.Sprintf("%d motion frames, gap p50 %s, p95 %s, max %s, %d gaps over 30ms",
		len(g), ms(pct(0.5)), ms(pct(0.95)), ms(g[len(g)-1]), slow)
}
