package probe

import "time"

// ResolverResult is the outcome of one DoH or DoT probe.
type ResolverResult struct {
	// Up is true when the resolver returned a DNS answer for our query.
	Up bool
	// RTT is the round trip including connection setup, 0 when the resolver is
	// down.
	RTT time.Duration
	// Err is why the resolver is down, nil when Up.
	Err error
}
