// Timing under the race detector says little, and takes long.

//go:build !race

package secretdetect

import (
	"runtime"
	"syscall"
	"testing"
	"time"
)

// Detect takes about as long in text that was once pathological as in as
// much prose. It measures this thread's CPU time, best of 2, which a busy
// machine changes less than wall time.
func TestPathological(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	cpu := func(text string) time.Duration {
		var best time.Duration
		for i := range 2 {
			var before, after syscall.Rusage
			syscall.Getrusage(1, &before) // RUSAGE_THREAD
			Detect(text)
			syscall.Getrusage(1, &after)
			d := time.Duration(after.Utime.Nano() + after.Stime.Nano() - before.Utime.Nano() - before.Stime.Nano())
			if i == 0 || d < best {
				best = d
			}
		}
		return best
	}
	const n = 128 << 10
	compiled()
	prose := cpu(corpus(n))
	for _, c := range pathological(n) {
		if d := cpu(c.text); d > 12*prose+20*time.Millisecond {
			t.Errorf("%s: %v, %v for as much prose", c.name, d, prose)
		} else {
			t.Logf("%s: %.1fx", c.name, float64(d)/float64(prose))
		}
	}
}
