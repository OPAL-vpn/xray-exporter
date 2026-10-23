package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/Anton-beep/xray-exporter/internal/app"
)

var version = "dev"

func main() {
	os.Exit(run())
}

// run keeps os.Exit out of the wiring so every deferred close runs.
func run() int {
	once := flag.Bool("once", false, "run one measurement cycle, print a report and exit")
	out := flag.String("out", "", "write the --once report to this file (mode 0600) instead of stdout")
	deadline := flag.Duration("deadline", 5*time.Minute, "per-config deadline for --once")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("xray-exporter %s (%s)\n", version, runtime.Version())
		return 0
	}

	env, err := app.LoadEnv(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "xray-exporter:", err)
		return 1
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: env.LogLevel}))
	opts := app.Options{Once: *once, Out: *out, Deadline: *deadline, Version: version}
	if err := app.Run(context.Background(), env, opts, logger); err != nil {
		logger.Error("xray-exporter failed", "error", err)
		return 1
	}
	return 0
}
