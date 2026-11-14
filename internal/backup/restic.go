package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Client is a thin wrapper around the restic CLI.
type Client struct {
	Bin      string
	Password string
}

// Repo describes one restic repository (local dir or s3:... location).
type Repo struct {
	Location string
	// Env holds extra KEY=value pairs (e.g. AWS credentials). Never log these.
	Env []string
	// Options holds extra global "-o key=value" pairs (e.g. s3.endpoint).
	Options []string
}

// BackupSummary is the parsed result of `restic backup --json`.
type BackupSummary struct {
	SnapshotID      string
	FilesNew        int64
	FilesChanged    int64
	FilesUnmodified int64
	DataAdded       int64
	TotalBytes      int64
	TotalFiles      int64
}

// Snapshot is a subset of `restic snapshots --json` entries.
type Snapshot struct {
	ID       string    `json:"-"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
	Tags     []string  `json:"tags"`
}

func (c *Client) baseEnv(repo Repo) []string {
	env := make([]string, 0, len(repo.Env)+1)
	env = append(env, "RESTIC_PASSWORD="+c.Password)
	env = append(env, repo.Env...)
	return env
}

func (c *Client) baseArgs(repo Repo) []string {
	args := []string{"-r", repo.Location, "--no-color"}
	args = append(args, repo.Options...)
	return args
}

// Available reports whether the restic binary executes.
func (c *Client) Available(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, _, err := runCmd(ctx, c.Bin, nil, "version")
	return err == nil
}

// EnsureInit creates the repository if `cat config` shows it does not exist yet.
func (c *Client) EnsureInit(ctx context.Context, repo Repo) error {
	args := append(c.baseArgs(repo), "cat", "config")
	if _, _, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...); err == nil {
		return nil
	}
	args = append(c.baseArgs(repo), "init")
	if _, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...); err != nil {
		// Racy double-init (two processes) is harmless: re-probe config.
		probe := append(c.baseArgs(repo), "cat", "config")
		if _, _, probeErr := runCmd(ctx, c.Bin, c.baseEnv(repo), probe...); probeErr == nil {
			return nil
		}
		return cmdError("restic init "+repo.Location, err, stderr)
	}
	return nil
}

// Backup runs `restic backup` and returns the parsed JSON summary.
// A snapshot is authoritative: if restic printed a summary with a snapshot id,
// the backup counts as taken even when stderr carries warnings (e.g. files
// that changed mid-backup on a live DATA_DIR).
func (c *Client) Backup(ctx context.Context, repo Repo, tags, excludes, sources []string) (BackupSummary, error) {
	args := c.baseArgs(repo)
	args = append(args, "backup", "--json", "--one-file-system")
	for _, tag := range tags {
		if tag != "" {
			args = append(args, "--tag", tag)
		}
	}
	for _, ex := range excludes {
		if ex != "" {
			args = append(args, "--exclude", ex)
		}
	}
	args = append(args, sources...)
	stdout, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	summary, parseErr := parseBackupSummary(stdout)
	if parseErr == nil && summary.SnapshotID != "" {
		return summary, nil
	}
	if err != nil {
		return BackupSummary{}, cmdError("restic backup", err, stderr)
	}
	return BackupSummary{}, fmt.Errorf("restic backup: no snapshot summary in output: %s", truncate(stderr))
}

// Forget applies the retention policy and prunes unreferenced data.
func (c *Client) Forget(ctx context.Context, repo Repo, keepDaily, keepWeekly, keepMonthly int) error {
	args := append(c.baseArgs(repo),
		"forget", "--prune",
		"--keep-daily", strconv.Itoa(keepDaily),
		"--keep-weekly", strconv.Itoa(keepWeekly),
		"--keep-monthly", strconv.Itoa(keepMonthly),
	)
	_, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return cmdError("restic forget", err, stderr)
	}
	return nil
}

// Check verifies repository integrity (metadata; fast, no data download).
func (c *Client) Check(ctx context.Context, repo Repo) error {
	args := append(c.baseArgs(repo), "check")
	_, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return cmdError("restic check "+repo.Location, err, stderr)
	}
	return nil
}

// Snapshots lists snapshots, newest first.
func (c *Client) Snapshots(ctx context.Context, repo Repo) ([]Snapshot, error) {
	args := append(c.baseArgs(repo), "snapshots", "--json")
	stdout, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		// An initialised-but-empty repo exits non-zero with "no snapshots".
		if strings.Contains(stderr, "no snapshots") {
			return nil, nil
		}
		return nil, cmdError("restic snapshots", err, stderr)
	}
	var raw []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Paths    []string  `json:"paths"`
		Tags     []string  `json:"tags"`
	}
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		return nil, fmt.Errorf("restic snapshots: parse: %w", err)
	}
	out := make([]Snapshot, 0, len(raw))
	for _, s := range raw {
		out = append(out, Snapshot{
			ID: s.ID, ShortID: s.ShortID, Time: s.Time,
			Hostname: s.Hostname, Paths: s.Paths, Tags: s.Tags,
		})
	}
	return out, nil
}

// FindPaths returns every stored path matching basename inside a snapshot.
// It shells out to `restic find --json` and is tolerant to output shape
// differences (JSON array vs JSON-lines) across restic versions.
func (c *Client) FindPaths(ctx context.Context, repo Repo, snapshotID, basename string) ([]string, error) {
	args := append(c.baseArgs(repo), "find", "--json", "--snapshot", snapshotID, basename)
	stdout, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return nil, cmdError("restic find "+basename, err, stderr)
	}
	var out []string
	for _, path := range parseFindPaths(stdout) {
		if path == "" {
			continue
		}
		if path == basename || strings.HasSuffix(path, "/"+basename) {
			out = append(out, path)
		}
	}
	return out, nil
}

// FindPath locates the stored path of a file inside a snapshot by basename.
func (c *Client) FindPath(ctx context.Context, repo Repo, snapshotID, basename string) (string, error) {
	paths, err := c.FindPaths(ctx, repo, snapshotID, basename)
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("restic find: %q not found in snapshot %s", basename, snapshotID)
	}
	return paths[0], nil
}

// RestorePath restores a single stored path from a snapshot into target dir.
func (c *Client) RestorePath(ctx context.Context, repo Repo, snapshotID, includePath, target string) error {
	args := append(c.baseArgs(repo),
		"restore", snapshotID, "--target", target, "--include", includePath,
	)
	_, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return cmdError("restic restore", err, stderr)
	}
	return nil
}

// RestoreLatest restores a whole snapshot (used by disaster-recovery CLI).
func (c *Client) RestoreLatest(ctx context.Context, repo Repo, snapshotID, target string) error {
	args := append(c.baseArgs(repo), "restore", snapshotID, "--target", target)
	_, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return cmdError("restic restore", err, stderr)
	}
	return nil
}

func parseBackupSummary(stdout string) (BackupSummary, error) {
	var summary BackupSummary
	found := false
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for decoder.More() {
		var msg struct {
			MessageType     string `json:"message_type"`
			SnapshotID      string `json:"snapshot_id"`
			FilesNew        int64  `json:"files_new"`
			FilesChanged    int64  `json:"files_changed"`
			FilesUnmodified int64  `json:"files_unmodified"`
			DataAdded       int64  `json:"data_added"`
			TotalBytes      int64  `json:"total_bytes_processed"`
			TotalFiles      int64  `json:"total_files_processed"`
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			break
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			continue
		}
		if msg.MessageType != "summary" {
			continue
		}
		summary = BackupSummary{
			SnapshotID: msg.SnapshotID, FilesNew: msg.FilesNew,
			FilesChanged: msg.FilesChanged, FilesUnmodified: msg.FilesUnmodified,
			DataAdded: msg.DataAdded, TotalBytes: msg.TotalBytes, TotalFiles: msg.TotalFiles,
		}
		found = true
	}
	if !found {
		return BackupSummary{}, fmt.Errorf("no summary message")
	}
	return summary, nil
}

// parseFindPaths extracts "path" values from `restic find --json` output,
// accepting both a top-level array and JSON-lines shapes.
func parseFindPaths(stdout string) []string {
	var paths []string
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var arr []struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(trimmed), &arr); err == nil {
			for _, item := range arr {
				paths = append(paths, item.Path)
			}
			return paths
		}
	}
	// Maybe {"files":[...]} envelope (newer restic).
	var envelope struct {
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(trimmed), &envelope); err == nil && len(envelope.Files) > 0 {
		for _, f := range envelope.Files {
			paths = append(paths, f.Path)
		}
		return paths
	}
	// Fall back to JSON-lines: one match object per line.
	decoder := json.NewDecoder(strings.NewReader(stdout))
	for {
		var item struct {
			Path string `json:"path"`
		}
		if err := decoder.Decode(&item); err != nil {
			break
		}
		paths = append(paths, item.Path)
	}
	return paths
}
