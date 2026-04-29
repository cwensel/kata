package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "manage the kata daemon"}
	cmd.AddCommand(daemonStartCmd(), daemonStatusCmd(), daemonStopCmd())
	return cmd
}

func daemonStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "start the daemon in foreground",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return runDaemon(ctx)
		},
	}
}

func daemonStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "report whether a daemon is running",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := daemon.NewNamespace()
			if err != nil {
				return err
			}
			recs, err := daemon.ListRuntimeFiles(ns.DataDir)
			if err != nil {
				return err
			}
			alive := 0
			for _, r := range recs {
				if daemon.ProcessAlive(r.PID) {
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon pid=%d address=%s db=%s started_at=%s\n",
						r.PID, r.Address, r.DBPath, r.StartedAt.Format(time.RFC3339))
					alive++
				}
			}
			if alive == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no daemon running")
			}
			return nil
		},
	}
}

func daemonStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "send SIGTERM to a running daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ns, err := daemon.NewNamespace()
			if err != nil {
				return err
			}
			recs, err := daemon.ListRuntimeFiles(ns.DataDir)
			if err != nil {
				return err
			}
			for _, r := range recs {
				if daemon.ProcessAlive(r.PID) {
					p, _ := os.FindProcess(r.PID)
					_ = p.Signal(syscall.SIGTERM)
					_, _ = fmt.Fprintf(cmd.OutOrStdout(), "stopped pid=%d\n", r.PID)
				}
			}
			return nil
		},
	}
}

// runDaemon is the foreground daemon entry point. Used by `kata daemon start`
// and by the auto-start child process spawned by ensureDaemon.
func runDaemon(ctx context.Context) error {
	ns, err := daemon.NewNamespace()
	if err != nil {
		return err
	}
	if err := ns.EnsureDirs(); err != nil {
		return err
	}
	dbPath, err := config.KataDB()
	if err != nil {
		return err
	}
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	socketPath := filepath.Join(ns.SocketDir, "daemon.sock")
	endpoint := daemon.UnixEndpoint(socketPath)

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        store,
		StartedAt: time.Now().UTC(),
		Endpoint:  endpoint,
	})
	defer func() { _ = srv.Close() }()

	rec := daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   endpoint.Address(),
		DBPath:    dbPath,
		StartedAt: time.Now().UTC(),
	}
	if _, err := daemon.WriteRuntimeFile(ns.DataDir, rec); err != nil {
		return err
	}
	runtimeFile := filepath.Join(ns.DataDir, fmt.Sprintf("daemon.%d.json", os.Getpid()))
	defer func() { _ = os.Remove(runtimeFile) }()

	return srv.Run(ctx)
}
