package probe

import "time"

// ResolverResult is the outcome of one DoH or DoT probe.
type ResolverResult struct {
	// Up is true when the resolver returned a DNS answer for our query.
	Up  bool
	RTT time.Duration
	Err error
}
