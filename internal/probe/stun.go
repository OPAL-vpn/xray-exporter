package probe

import (
	"context"
	"errors"
	"net"
	"sort"
	"time"

	"github.com/pion/stun/v3"
)

// stunTransactionID keys the requests of one burst; the wire format carries it
// as the raw 96-bit array.
type stunTransactionID = [stun.TransactionIDSize]byte

// UDPResult is the outcome of one STUN burst probe.
type UDPResult struct {
	// Up is true when at least one request was answered.
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

	sentAt := make(map[stunTransactionID]time.Time, count)
	sentIndex := make(map[stunTransactionID]int, count)
	sent := 0

	for i := 0; i < count; i++ {
		if err := ctx.Err(); err != nil {
			return UDPResult{Sent: sent, Err: err}
		}

		request := stun.MustBuild(stun.BindingRequest, stun.NewTransactionIDSetter(stun.NewTransactionID()))
		id := request.TransactionID
		sentAt[id] = time.Now()
		sentIndex[id] = i

		if err := relay.WriteTo(target, request.Raw); err != nil {
			return UDPResult{Sent: sent, Err: err}
		}
		sent++

		if i == count-1 || spacing <= 0 {
			continue
		}
		select {
		case <-ctx.Done():
			return UDPResult{Sent: sent, Err: ctx.Err()}
		case <-time.After(spacing):
		}
	}

	// The relay keeps a read deadline until it is changed, so one explicit
	// deadline bounds every read of the collection loop.
	if err := relay.SetReadDeadline(deadline); err != nil {
		return UDPResult{Sent: sent, Err: err}
	}

	var (
		rtts      []time.Duration // arrival order
		answered  = make(map[stunTransactionID]bool, sent)
		prevIndex = -1
		reordered int
		readErr   error
	)

	for {
		_, payload, err := relay.ReadFrom()
		if err != nil {
			var netErr net.Error
			if !errors.As(err, &netErr) || !netErr.Timeout() {
				readErr = err
			}
			break
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
		index, wasSent := sentIndex[id]
		if !wasSent || answered[id] {
			continue
		}
		answered[id] = true
		rtts = append(rtts, time.Since(sentAt[id]))
		if index < prevIndex {
			reordered++
		}
		prevIndex = index

		// timeout bounds the probe; with no loss left to observe, waiting
		// out the rest of it only delays the caller.
		if len(answered) == sent {
			break
		}
	}

	result := UDPResult{Sent: sent, Replies: len(rtts), Reordered: reordered}
	if len(rtts) == 0 {
		// A broken relay read is only worth reporting when it left no sample
		// at all; a burst with replies is usable whatever ended the reads.
		result.Err = readErr
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
