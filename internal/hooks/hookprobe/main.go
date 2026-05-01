// Command hookprobe is a deterministic test helper used by the hooks
// package. It is intentionally placed under internal/hooks/hookprobe so
// it does not appear in user-facing builds. Tests build it once via
// TestMain in the parent package and exec the resulting binary.
package main

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hookprobe SUBCOMMAND [ARGS...]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "stdin":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "env":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe env KEY")
			os.Exit(2)
		}
		fmt.Println(os.Getenv(os.Args[2]))
	case "both":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe both OUT_LINE ERR_LINE")
			os.Exit(2)
		}
		// Test helper: writing the literal arg to each stream is the whole
		// point of this subcommand; gosec G705 (taint analysis) is moot
		// here because the binary is built only by tests in this repo and
		// is never exposed to untrusted callers.
		_, _ = fmt.Fprintln(os.Stdout, os.Args[2]) //nolint:gosec // G705: test helper, see comment
		_, _ = fmt.Fprintln(os.Stderr, os.Args[3]) //nolint:gosec // G705: test helper, see comment
	case "exit":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe exit N")
			os.Exit(2)
		}
		n, err := strconv.Atoi(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(n)
	case "sleep":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe sleep DURATION")
			os.Exit(2)
		}
		d, err := time.ParseDuration(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		time.Sleep(d)
	case "term-delay":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe term-delay DURATION")
			os.Exit(2)
		}
		d, err := time.ParseDuration(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		<-ch
		time.Sleep(d)
	case "term-ignore":
		signal.Ignore(syscall.SIGTERM)
		if len(os.Args) >= 3 {
			d, err := time.ParseDuration(os.Args[2])
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			time.Sleep(d)
			return
		}
		// No-arg fallback: stay alive until SIGKILL. A bare select{}
		// can trip Go's deadlock detector ("all goroutines are asleep -
		// deadlock"); a timer-backed loop keeps the runtime busy.
		for {
			time.Sleep(time.Hour)
		}
	case "spawn-orphan":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: hookprobe spawn-orphan DURATION")
			os.Exit(2)
		}
		d, err := time.ParseDuration(os.Args[2])
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		spawnOrphanAndExit(d)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n", os.Args[1]) //nolint:gosec // G705: diagnostic echo in test helper
		os.Exit(2)
	}
}
