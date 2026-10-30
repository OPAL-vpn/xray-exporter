package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/OPAL-vpn/xray-exporter/internal/egress"
)

// serve starts the listener, the timer and the signal loop.
func (a *application) serve(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.env.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.env.ListenAddr, err)
	}

	server := &http.Server{Handler: a.handler(), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	a.logger.Info("metrics server listening", "addr", listener.Addr().String())

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(signals)

	ticker := time.NewTicker(a.env.Interval)
	defer ticker.Stop()

	// Refresh on the source's own interval; SIGHUP reloads the inputs instead.
	go a.refreshLoop(runCtx)
	go a.egressLoop(runCtx, egress.New())

	// Measure at startup so the first scrape does not wait an interval.
	a.startCycle(runCtx, a.env.Interval/2)

	for {
		select {
		case <-runCtx.Done():
			a.logger.Info("shutting down")
			return a.stop(server)
		case sig := <-signals:
			if sig == syscall.SIGHUP {
				a.logger.Info("reloading configuration")
				// The reload fetches, so it must not block the signal loop; a
				// shutdown waits for it below.
				go a.reload(runCtx)
				continue
			}
			a.logger.Info("shutting down", "signal", sig.String())
			cancel()
			return a.stop(server)
		case <-ticker.C:
			a.startCycle(runCtx, a.env.Interval/2)
		case err := <-serveErr:
			if errors.Is(err, http.ErrServerClosed) {
				return nil
			}
			a.logger.Error("metrics server stopped", "error", err)
			cancel()
			a.stop(server)
			return fmt.Errorf("serve %s: %w", a.env.ListenAddr, err)
		}
	}
}

func (a *application) handler() http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(a.snap.Collector())

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ok")
	})
	return mux
}

// stop ends the cycle, closes the instances and shuts the listener down.
func (a *application) stop(server *http.Server) error {
	a.interruptCycle()

	// A refresh or reload in flight would apply to a closed manager; the run
	// context is already cancelled, so this waits for it to finish.
	a.refreshMu.Lock()
	a.refreshMu.Unlock()

	if err := a.manager.Close(); err != nil {
		a.logger.Error("closing xray instances", "error", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		a.logger.Warn("http server shutdown", "error", err)
	}
	return nil
}
