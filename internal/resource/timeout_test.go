package resource

import "time"

// timeoutAfter bounds a test that must terminate.
func timeoutAfter(seconds int) <-chan time.Time {
	return time.After(time.Duration(seconds) * time.Second)
}
