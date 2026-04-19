package fsm

import (
	"runtime"
	"strconv"
	"strings"
)

// goroutineID returns the current goroutine's ID. This uses runtime.Stack
// which is not cheap, but it's only called on the restore path (not on
// every event), so the cost is acceptable.
func goroutineID() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	// Output starts with "goroutine 123 ["
	s := string(buf[:n])
	s = strings.TrimPrefix(s, "goroutine ")
	s = s[:strings.IndexByte(s, ' ')]
	id, _ := strconv.ParseInt(s, 10, 64)
	return id
}
