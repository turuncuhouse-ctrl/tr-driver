package backup

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"necipdrive/internal/store"
)

// RunCLI implements `necipdrive backup <command>`. It returns the process exit
// code. Snapshot listing and restore deliberately avoid the database so they
// keep working on a fresh disaster-recovery machine.
func RunCLI(args []string) int {
	if len(args) == 0 {
		cliUsage()
		return 2
	}
	switch args[0] {
	case "run":
		return cliRun(args[1:])
	case "verify":
		return cliVerify(args[1:])
	case "status":
		return cliStatus(args[1:])
	case "snapshots":
		return cliSnapshots(args[1:])
	case "restore":
		return cliRestore(args[1:])
	case "help", "-h", "--help":
		cliUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown backup command %q\n\n", args[0])
		cliUsage()
		return 2
	}
}

func cliUsage() {
	fmt.Print(`Usage: necipdrive backup <command> [flags]

Commands:
  run                  Take a full backup now (database + files), synchronously
  verify               Re-check the latest snapshots without a new backup
  status               Show scheduler state, last runs and repo health
  snapshots            List snapshots in a repository
  restore              Restore a snapshot to a directory (disaster recovery)

Flags:
  snapshots: --repo local|offsite   (default local)
  restore:   --repo local|offsite --snapshot ID|latest --target DIR (required)

Environment: BACKUP_DIR, BACKUP_PASSWORD, BACKUP_S3_* (see docs/BACKUP.md).
`)
}

func cliService(ctx context.Context) (*Service, error) {
	cfg := ConfigFromEnv()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if cfg.Password == "" {
		return nil, fmt.Errorf("BACKUP_PASSWORD is not set")
	}
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required for this command")
	}
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	// Idempotent: guarantees backup_runs exists for run history.
	if err := store.Migrate(ctx, db, 0, 0); err != nil {
		db.Close()
		return nil, err
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "./data"
	}
	return NewService(db, cfg, dataDir, databaseURL), nil
}

func cliRun(args []string) int {
	fs := flag.NewFlagSet("backup run", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	svc, err := cliService(ctx)
	if err != nil {
		return cliError(err)
	}
	run, err := svc.RunSync(ctx, KindCLI)
	if err != nil {
		return cliError(err)
	}
	fmt.Printf("run %s finished: %s (verify=%v offsite=%v)\n", run.ID, run.Status, run.VerifyOK, run.OffsiteOK)
	if run.Error != "" {
		fmt.Printf("notes: %s\n", run.Error)
	}
	if run.Status == StatusFailed {
		return 1
	}
	return 0
}

func cliVerify(args []string) int {
	fs := flag.NewFlagSet("backup verify", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	svc, err := cliService(ctx)
	if err != nil {
		return cliError(err)
	}
	run, err := svc.VerifySync(ctx)
	if err != nil {
		return cliError(err)
	}
	fmt.Printf("verify %s finished: %s (verify=%v offsite=%v)\n", run.ID, run.Status, run.VerifyOK, run.OffsiteOK)
	if run.Error != "" {
		fmt.Printf("notes: %s\n", run.Error)
	}
	if run.Status == StatusFailed {
		return 1
	}
	return 0
}

func cliStatus(args []string) int {
	fs := flag.NewFlagSet("backup status", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	svc, err := cliService(ctx)
	if err != nil {
		return cliError(err)
	}
	st, err := svc.Status(ctx)
	if err != nil {
		return cliError(err)
	}
	fmt.Printf("enabled=%v configured=%v schedule=%s offsite=%v stale=%v\n",
		st.Enabled, st.Configured, st.Schedule, st.OffsiteEnabled, st.Stale)
	if st.ConfigError != "" {
		fmt.Printf("config: %s\n", st.ConfigError)
	}
	if st.LastSuccessAt != nil {
		fmt.Printf("last success: %s\n", st.LastSuccessAt.Format(time.RFC3339))
	} else {
		fmt.Println("last success: never")
	}
	runs, err := ListRuns(ctx, svc.db, 5)
	if err != nil {
		return cliError(err)
	}
	fmt.Println("recent runs:")
	for _, r := range runs {
		fmt.Printf("  %s  %-9s %-8s verify=%v offsite=%v %s\n",
			r.StartedAt.Format("2006-01-02 15:04"), r.Kind, r.Status, r.VerifyOK, r.OffsiteOK, r.Error)
	}
	return 0
}

func cliRepo(args []string, cmd string) (Repo, *Client, []string, error) {
	fs := flag.NewFlagSet("backup "+cmd, flag.ContinueOnError)
	repoName := fs.String("repo", "local", "local|offsite")
	if err := fs.Parse(args); err != nil {
		return Repo{}, nil, nil, err
	}
	cfg := ConfigFromEnv()
	if cfg.Password == "" {
		return Repo{}, nil, nil, fmt.Errorf("BACKUP_PASSWORD is not set")
	}
	client := &Client{Bin: cfg.ResticBin, Password: cfg.Password}
	switch *repoName {
	case "local":
		return Repo{Location: cfg.Dir}, client, fs.Args(), nil
	case "offsite":
		if !cfg.S3Enabled() {
			return Repo{}, nil, nil, fmt.Errorf("offsite replication is not configured (BACKUP_S3_*)")
		}
		return Repo{Location: cfg.S3Repo(), Env: cfg.S3Env(), Options: cfg.S3Options()}, client, fs.Args(), nil
	default:
		return Repo{}, nil, nil, fmt.Errorf("repo must be local or offsite")
	}
}

func cliSnapshots(args []string) int {
	repo, client, _, err := cliRepo(args, "snapshots")
	if err != nil {
		return cliError(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	snapshots, err := client.Snapshots(ctx, repo)
	if err != nil {
		return cliError(err)
	}
	if len(snapshots) == 0 {
		fmt.Println("no snapshots")
		return 0
	}
	for _, s := range snapshots {
		fmt.Printf("%s  %s  %s  tags=%s\n", s.ShortID, s.Time.Format("2006-01-02 15:04"), s.Hostname, strings.Join(s.Tags, ","))
	}
	return 0
}

func cliRestore(args []string) int {
	fs := flag.NewFlagSet("backup restore", flag.ContinueOnError)
	repoName := fs.String("repo", "local", "local|offsite")
	snapshot := fs.String("snapshot", "latest", "snapshot id or 'latest'")
	target := fs.String("target", "", "restore target directory (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "backup restore: --target is required")
		return 2
	}
	repoArgs := []string{"--repo", *repoName}
	repo, client, _, err := cliRepo(repoArgs, "restore")
	if err != nil {
		return cliError(err)
	}
	if err := os.MkdirAll(*target, 0o755); err != nil {
		return cliError(err)
	}
	fmt.Printf("restoring snapshot %s from %s to %s ...\n", *snapshot, *repoName, *target)
	if err := client.RestoreLatest(context.Background(), repo, *snapshot, *target); err != nil {
		return cliError(err)
	}
	fmt.Print(`restore complete. Next steps (see docs/BACKUP.md):
  1. Find db.dump under the target dir and restore it:
       pg_restore --dbname "$DATABASE_URL" --no-owner --clean db.dump
  2. Move the restored files tree back to DATA_DIR (or point DATA_DIR at it).
  3. Restart the stack and verify /healthz + login.
`)
	return 0
}

func cliError(err error) int {
	fmt.Fprintf(os.Stderr, "backup: %v\n", err)
	return 1
}
