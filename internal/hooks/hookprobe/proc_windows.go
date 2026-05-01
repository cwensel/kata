//go:build windows

package main

import (
	"fmt"
	"os"
	"time"
)

func spawnOrphanAndExit(d time.Duration) {
	fmt.Fprintln(os.Stderr, "spawn-orphan: not supported on windows")
	_ = d
	os.Exit(0)
}
