package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// snapshotHost is stamped on every snapshot via `backup --host`. It must stay
// stable: restic groups snapshots by host+paths for `forget`, and a container
// hostname would change on every recreate, fragmenting groups so old
// snapshots are never pruned.
const snapshotHost = "trdriver"

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
	// NOTE: restic has no --no-color flag; it disables color automatically
	// when stdout is not a TTY.
	args := []string{"-r", repo.Location}
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
		if strings.Contains(strings.ToLower(stderr), "already exists") {
			return fmt.Errorf("restic repo at %s exists but the password is wrong or the repo is damaged: %s",
				repo.Location, truncate(stderr))
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
	args = append(args, "backup", "--json", "--one-file-system", "--host", snapshotHost)
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
	var raw []struct {
		ID       string    `json:"id"`
		ShortID  string    `json:"short_id"`
		Time     time.Time `json:"time"`
		Hostname string    `json:"hostname"`
		Paths    []string  `json:"paths"`
		Tags     []string  `json:"tags"`
	}
	// Parse first: some versions print usable JSON alongside a warning exit.
	if uerr := json.Unmarshal([]byte(strings.TrimSpace(stdout)), &raw); uerr == nil {
		out := make([]Snapshot, 0, len(raw))
		for _, s := range raw {
			out = append(out, Snapshot{
				ID: s.ID, ShortID: s.ShortID, Time: s.Time,
				Hostname: s.Hostname, Paths: s.Paths, Tags: s.Tags,
			})
		}
		// Do not trust daemon-side ordering: newest first, deterministically.
		sort.Slice(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
		return out, nil
	}
	if err != nil {
		// An initialised-but-empty repo reports "no snapshots".
		if strings.Contains(strings.ToLower(stderr), "no snapshots") ||
			strings.Contains(strings.ToLower(stdout), "no snapshots") {
			return nil, nil
		}
		return nil, cmdError("restic snapshots", err, stderr)
	}
	return nil, fmt.Errorf("restic snapshots: parse: %s", truncate(stdout))
}

// FindPaths returns every stored path matching basename inside a snapshot.
// It shells out to `restic find --json` and is tolerant to output shape
// differences (JSON array vs JSON-lines) across restic versions.
func (c *Client) FindPaths(ctx context.Context, repo Repo, snapshotID, basename string) ([]string, error) {
	args := append(c.baseArgs(repo), "find", "--json", "--snapshot", snapshotID, basename)
	stdout, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	var out []string
	for _, path := range parseFindPaths(stdout) {
		if path == "" {
			continue
		}
		if path == basename || strings.HasSuffix(path, "/"+basename) {
			out = append(out, path)
		}
	}
	if len(out) > 0 || err == nil {
		return out, nil
	}
	// No matches: some restic versions exit 1 with an empty result. Only
	// swallow that case — anything alarming (Fatal, wrong password…) is an
	// error. Note stderr is never matched for secrets: it holds no password.
	if strings.Contains(strings.ToLower(stderr), "fatal") {
		return nil, cmdError("restic find "+basename, err, stderr)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return nil, nil
	}
	return nil, cmdError("restic find "+basename, err, stderr)
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
		"restore", snapshotID, "--target", target, "--include", escapePattern(includePath),
	)
	_, stderr, err := runCmd(ctx, c.Bin, c.baseEnv(repo), args...)
	if err != nil {
		return cmdError("restic restore", err, stderr)
	}
	return nil
}

// escapePattern quotes glob metacharacters so a literal stored path (which may
// contain user-chosen names like "[final].jpg") matches exactly one file in
// --include/--exclude patterns.
func escapePattern(path string) string {
	var b strings.Builder
	b.Grow(len(path) + 8)
	for _, r := range path {
		switch r {
		case '*', '?', '[':
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteByte(']')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
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
	// Line-by-line: a stray warning line must not hide the summary, and old
	// restic printed the summary without a message_type field.
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
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
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.MessageType != "" && msg.MessageType != "summary" {
			continue
		}
		if msg.SnapshotID == "" {
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

// parseFindPaths extracts "path" values from `restic find --json` output.
// The documented shape is an array of per-snapshot objects holding a
// "matches" array; older versions printed bare {"path":...} objects, either
// as an array or JSON-lines. All shapes are accepted.
func parseFindPaths(stdout string) []string {
	var paths []string
	trimmed := strings.TrimSpace(stdout)
	if trimmed == "" {
		return nil
	}
	collect := func(items []findMatch) {
		for _, item := range items {
			if item.Path != "" {
				paths = append(paths, item.Path)
			}
		}
	}
	// Shape 1 (documented): [{"snapshot":..,"hits":..,"matches":[{...,"path":..}]}]
	var grouped []struct {
		Matches []findMatch `json:"matches"`
	}
	if err := json.Unmarshal([]byte(trimmed), &grouped); err == nil && len(grouped) > 0 {
		for _, g := range grouped {
			collect(g.Matches)
		}
		if len(paths) > 0 {
			return paths
		}
	}
	// Shape 2: [{"path":..}, ...]
	var flat []findMatch
	if err := json.Unmarshal([]byte(trimmed), &flat); err == nil && len(flat) > 0 {
		collect(flat)
		if len(paths) > 0 {
			return paths
		}
	}
	// Shape 3: single object, possibly {"matches":[...]} / {"files":[...]}.
	var single struct {
		findMatch
		Matches []findMatch `json:"matches"`
		Files   []findMatch `json:"files"`
	}
	if err := json.Unmarshal([]byte(trimmed), &single); err == nil {
		if single.Path != "" {
			paths = append(paths, single.Path)
		}
		collect(single.Matches)
		collect(single.Files)
		if len(paths) > 0 {
			return paths
		}
	}
	// Shape 4: JSON-lines, one match or one grouped object per line.
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var item struct {
			findMatch
			Matches []findMatch `json:"matches"`
		}
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if item.Path != "" {
			paths = append(paths, item.Path)
		}
		collect(item.Matches)
	}
	return paths
}

type findMatch struct {
	Path string `json:"path"`
}
