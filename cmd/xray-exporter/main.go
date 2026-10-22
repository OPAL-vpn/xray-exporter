package main

import (
	"flag"
	"fmt"
	"os"
	"runtime"
)

var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("xray-exporter %s (%s)\n", version, runtime.Version())
		return
	}

	flag.Usage()
	os.Exit(2)
}
