package simdjson

import "time"

// timeoutAfter is the deadline the concurrency tests wait against. A test that
// hangs is worse than one that fails, because it takes the whole run with it.
func timeoutAfter() <-chan time.Time { return time.After(30 * time.Second) }
