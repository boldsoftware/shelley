//go:build !linux

package claudetool

// resourceNotes notes nothing outside Linux, which alone has PSI and
// /dev/kmsg.
type resourceNotes struct{}

func newResourceNotes() *resourceNotes { return nil }

type resourceWatch struct{}

func (n *resourceNotes) watch(pid int) *resourceWatch { return nil }

func (w *resourceWatch) finish() string { return "" }
