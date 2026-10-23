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
)

// serve starts the HTTP listener and the signal loop. SIGHUP reloads; SIGTERM
// and SIGINT stop the cycle, the listener and the instances.
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

	// A first cycle at startup means the first scrape does not wait a whole
	// interval for measurements.
	a.startCycle(runCtx, a.env.Interval/2)

	for {
		select {
		case <-runCtx.Done():
			a.logger.Info("shutting down")
			return a.stop(server)
		case sig := <-signals:
			if sig == syscall.SIGHUP {
				a.logger.Info("reloading configuration")
				a.reload(runCtx)
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

// handler builds the routes the listener serves: the Prometheus scrape endpoint
// over the snapshot and a liveness probe. It is separate from serve so the
// wiring can be exercised without binding a port.
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

// stop ends the in-flight cycle, closes every xray instance and shuts the
// listener down with a 5 s bound.
func (a *application) stop(server *http.Server) error {
	a.interruptCycle()

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
