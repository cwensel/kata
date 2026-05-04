package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/wesm/kata/internal/config"
	"github.com/wesm/kata/internal/daemon"
	"github.com/wesm/kata/internal/db"
	"github.com/wesm/kata/internal/hooks"
	"github.com/wesm/kata/internal/jsonl"
	"github.com/wesm/kata/internal/version"
)

func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "manage the kata daemon"}
	cmd.AddCommand(daemonStartCmd(), daemonStatusCmd(), daemonStopCmd(), daemonReloadCmd(), daemonLogsCmd())
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
			out := daemonStatusOutput{Daemons: make([]daemonStatusEntry, 0, len(recs))}
			for _, r := range recs {
				if daemon.ProcessAlive(r.PID) {
					out.Daemons = append(out.Daemons, daemonStatusEntry{
						PID:       r.PID,
						Version:   daemonRuntimeVersion(r),
						Address:   r.Address,
						DBPath:    r.DBPath,
						StartedAt: r.StartedAt,
					})
				}
			}
			if flags.JSON {
				return emitJSON(cmd.OutOrStdout(), out)
			}
			if len(out.Daemons) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "no daemon running")
				return nil
			}
			for _, d := range out.Daemons {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "daemon pid=%d version=%s address=%s db=%s started_at=%s\n",
					d.PID, d.Version, d.Address, d.DBPath, d.StartedAt.Format(time.RFC3339))
			}
			return nil
		},
	}
}

type daemonStatusOutput struct {
	Daemons []daemonStatusEntry `json:"daemons"`
}

type daemonStatusEntry struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	Address   string    `json:"address"`
	DBPath    string    `json:"db_path"`
	StartedAt time.Time `json:"started_at"`
}

func daemonRuntimeVersion(r daemon.RuntimeRecord) string {
	if r.Version == "" {
		return "unknown"
	}
	return r.Version
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

func daemonReloadCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reload",
		Short: "send SIGHUP to a running daemon to reload hook config",
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
				if !daemon.ProcessAlive(r.PID) {
					continue
				}
				p, err := os.FindProcess(r.PID)
				if err != nil {
					return &cliError{
						Kind: kindInternal, ExitCode: ExitInternal,
						Message: fmt.Sprintf("find pid %d: %v", r.PID, err),
					}
				}
				if err := p.Signal(syscall.SIGHUP); err != nil {
					return &cliError{
						Kind: kindInternal, ExitCode: ExitInternal,
						Message: fmt.Sprintf("signal pid %d: %v", r.PID, err),
					}
				}
				_, _ = fmt.Fprintf(cmd.OutOrStdout(),
					"reload signal sent to pid=%d (check daemon log for result)\n", r.PID)
				return nil
			}
			return &cliError{Kind: kindUsage, ExitCode: ExitUsage, Message: "no daemon running"}
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
	if ver, err := db.PeekSchemaVersion(ctx, dbPath); err == nil && ver < db.CurrentSchemaVersion() {
		if err := jsonl.AutoCutover(ctx, dbPath); err != nil {
			return err
		}
	}
	store, err := db.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	disp, daemonLog, hookCfgPath, err := setupHooks(store, dbPath)
	if err != nil {
		return err
	}
	defer shutdownHooks(disp)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go runReloadLoop(ctx, sigs, hookCfgPath, disp, daemonLog)

	socketPath := filepath.Join(ns.SocketDir, "daemon.sock")
	endpoint := daemon.UnixEndpoint(socketPath)

	srv := daemon.NewServer(daemon.ServerConfig{
		DB:        store,
		StartedAt: time.Now().UTC(),
		Endpoint:  endpoint,
		Hooks:     disp,
	})
	defer func() { _ = srv.Close() }()

	rec := daemon.RuntimeRecord{
		PID:       os.Getpid(),
		Address:   endpoint.Address(),
		DBPath:    dbPath,
		Version:   version.Version,
		StartedAt: time.Now().UTC(),
	}
	if _, err := daemon.WriteRuntimeFile(ns.DataDir, rec); err != nil {
		return err
	}
	runtimeFile := filepath.Join(ns.DataDir, fmt.Sprintf("daemon.%d.json", os.Getpid()))
	defer func() { _ = os.Remove(runtimeFile) }()

	return srv.Run(ctx)
}

// setupHooks loads hooks.toml, materializes $KATA_HOME, and constructs
// the dispatcher with DB-backed resolvers. Returned values are wired
// into runDaemon: the dispatcher feeds ServerConfig.Hooks, the logger
// is shared with runReloadLoop, and the config path is passed to
// runReloadLoop so SIGHUP re-reads the same file.
func setupHooks(store *db.DB, dbPath string) (*hooks.Dispatcher, *log.Logger, string, error) {
	home, err := config.KataHome()
	if err != nil {
		return nil, nil, "", err
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return nil, nil, "", err
	}
	hookCfgPath, err := config.HookConfigPath()
	if err != nil {
		return nil, nil, "", err
	}
	loaded, err := hooks.LoadStartup(hookCfgPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("hooks: %w", err)
	}
	daemonLog := log.New(os.Stderr, "kata-daemon: ", log.LstdFlags)
	deps := hooks.DispatcherDeps{
		DBHash:          config.DBHash(dbPath),
		KataHome:        home,
		DaemonLog:       daemonLog,
		AliasResolver:   makeAliasResolver(store),
		IssueResolver:   makeIssueResolver(store),
		CommentResolver: makeCommentResolver(store),
		ProjectResolver: makeProjectResolver(store),
		Now:             time.Now,
		GraceWindow:     5 * time.Second,
	}
	disp, err := hooks.New(loaded, deps)
	if err != nil {
		return nil, nil, "", fmt.Errorf("hooks: %w", err)
	}
	return disp, daemonLog, hookCfgPath, nil
}

// shutdownHooks drives the dispatcher's Shutdown with a 10s ceiling.
// Errors (timeout, in-flight jobs) are not returned: the daemon exit
// path proceeds either way, with the dispatcher's own log capturing
// the timeout reason.
func shutdownHooks(disp *hooks.Dispatcher) {
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = disp.Shutdown(sctx)
}
