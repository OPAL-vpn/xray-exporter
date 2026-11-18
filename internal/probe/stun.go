package probe

import (
	"context"
	"errors"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/pion/stun/v3"
)

// stunTransactionID keys the requests of one burst; the wire format carries it
// as the raw 96-bit array.
type stunTransactionID = [stun.TransactionIDSize]byte

// UDPResult is the outcome of one STUN burst probe.
type UDPResult struct {
	// Up is true when at least one request was answered and no relay read
	// failed mid-burst.
	Up        bool
	Sent      int           // requests written through the relay
	Replies   int           // unique transaction ids answered
	Median    time.Duration // median RTT of the replies; 0 when Up is false
	Max       time.Duration // longest RTT of the replies
	Jitter    time.Duration // mean |ΔRTT| across consecutive replies in arrival order
	Reordered int           // replies whose send index is lower than the previous reply's
	Err       error
}

// ProbeSTUN sends count STUN binding requests to target ("host:port") through
// the socks inbound at socksAddr, spaced by spacing, and collects replies until
// timeout bounds the whole probe. It creates and closes its own relay: one
// association, one destination.
func ProbeSTUN(ctx context.Context, socksAddr, target string, count int, spacing, timeout time.Duration) UDPResult {
	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	relay, err := NewSocksUDPRelay(socksAddr, time.Until(deadline))
	if err != nil {
		return UDPResult{Err: err}
	}
	defer relay.Close()

	burst := newStunBurst(count)

	// The relay keeps a read deadline until it is changed, so one explicit
	// deadline bounds every read of the burst.
	if err := relay.SetReadDeadline(deadline); err != nil {
		return UDPResult{Err: err}
	}

	// Replies are read while the paced sends run; a read after the send loop
	// would timestamp them late by the remaining spacing and report that wait
	// as RTT.
	received := make(chan struct{})
	go func() {
		defer close(received)
		burst.receive(relay)
	}()

	sent, sendErr := burst.send(ctx, relay, target, count, spacing)
	if sendErr != nil {
		// Closing the relay unblocks the reader so it is joined before the
		// result is built and the deferred Close runs.
		relay.Close()
		<-received
		return UDPResult{Sent: sent, Err: sendErr}
	}

	// timeout bounds the probe; with no loss left to observe, waiting it out
	// only delays the caller for another pacing window.
	if burst.allAnswered() {
		if err := relay.SetReadDeadline(time.Now()); err != nil {
			relay.Close()
			<-received
			return UDPResult{Sent: sent, Err: err}
		}
	}
	<-received

	return burst.result(sent)
}

// stunBurst is the shared state of one probe: the sender records each request
// before writing it, and the receive goroutine timestamps its reply on arrival.
// The mutex covers every field because both goroutines run concurrently.
type stunBurst struct {
	mu        sync.Mutex
	sentAt    map[stunTransactionID]time.Time
	sentIndex map[stunTransactionID]int
	answered  map[stunTransactionID]bool
	rtts      []time.Duration // arrival order
	reordered int
	prevIndex int
	readErr   error
	sent      int
	sendsDone bool
}

func newStunBurst(count int) *stunBurst {
	return &stunBurst{
		sentAt:    make(map[stunTransactionID]time.Time, count),
		sentIndex: make(map[stunTransactionID]int, count),
		answered:  make(map[stunTransactionID]bool, count),
		prevIndex: -1,
	}
}

// send writes count requests paced by spacing and records their send times, so
// the receive goroutine can match replies as they arrive. It returns the number
// of requests written through the relay.
func (b *stunBurst) send(ctx context.Context, relay *SocksUDPRelay, target string, count int, spacing time.Duration) (int, error) {
	sent := 0

	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			b.finishSend(sent)
			return sent, err
		}

		request := stun.MustBuild(stun.BindingRequest, stun.NewTransactionIDSetter(stun.NewTransactionID()))
		id := request.TransactionID

		b.mu.Lock()
		b.sentAt[id] = time.Now()
		b.sentIndex[id] = i
		b.mu.Unlock()

		if err := relay.WriteTo(target, request.Raw); err != nil {
			b.finishSend(sent)
			return sent, err
		}
		sent++

		if i == count-1 || spacing <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			b.finishSend(sent)
			return sent, ctx.Err()
		case <-time.After(spacing):
		}
	}

	b.finishSend(sent)
	return sent, nil
}

func (b *stunBurst) finishSend(sent int) {
	b.mu.Lock()
	b.sent = sent
	b.sendsDone = true
	b.mu.Unlock()
}

// receive reads replies until the relay fails or the read deadline expires,
// timestamping each matching reply at its arrival.
func (b *stunBurst) receive(relay *SocksUDPRelay) {
	for {
		_, payload, err := relay.ReadFrom()
		if err != nil {
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				b.mu.Lock()
				b.readErr = err
				b.mu.Unlock()
			}
			return
		}

		var reply stun.Message
		if err := stun.Decode(payload, &reply); err != nil {
			continue
		}
		// A TURN server answers binding requests with an error response, so
		// both classes of binding reply count.
		if reply.Type != stun.BindingSuccess && reply.Type != stun.BindingError {
			continue
		}

		id := reply.TransactionID

		b.mu.Lock()
		index, wasSent := b.sentIndex[id]
		if wasSent && !b.answered[id] {
			b.answered[id] = true
			b.rtts = append(b.rtts, time.Since(b.sentAt[id]))
			if index < b.prevIndex {
				b.reordered++
			}
			b.prevIndex = index
		}
		answeredAll := b.allAnsweredLocked()
		b.mu.Unlock()

		if answeredAll {
			return
		}
	}
}

// allAnswered reports whether every request was written and answered. A probe
// that sent nothing is never complete, so it keeps waiting for its timeout.
func (b *stunBurst) allAnswered() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.allAnsweredLocked()
}

func (b *stunBurst) allAnsweredLocked() bool {
	return b.sendsDone && b.sent > 0 && len(b.answered) == b.sent
}

// result aggregates the replied burst. It is called after the receive goroutine
// was joined, so the counters and RTTs are final.
func (b *stunBurst) result(sent int) UDPResult {
	b.mu.Lock()
	rtts := append([]time.Duration(nil), b.rtts...)
	reordered := b.reordered
	readErr := b.readErr
	b.mu.Unlock()

	result := UDPResult{Sent: sent, Replies: len(rtts), Reordered: reordered}
	if readErr != nil {
		// A relay read that fails mid-burst leaves a truncated reply set: the
		// loss ratio and the latencies would describe a partial denominator, so
		// the burst is a failure, not a success with inflated loss.
		result.Err = readErr
		return result
	}
	if len(rtts) == 0 {
		return result
	}

	sorted := append([]time.Duration(nil), rtts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + sorted[len(sorted)/2]) / 2
	}

	var jitterSum time.Duration
	for i := 1; i < len(rtts); i++ {
		delta := rtts[i] - rtts[i-1]
		if delta < 0 {
			delta = -delta
		}
		jitterSum += delta
	}

	result.Up = true
	result.Median = median
	result.Max = sorted[len(sorted)-1]
	if len(rtts) > 1 {
		result.Jitter = jitterSum / time.Duration(len(rtts)-1)
	}

	return result
}
