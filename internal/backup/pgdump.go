package backup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SplitDatabaseURL separates the password out of a libpq connection string so
// pg_dump/pg_restore can receive it via PGPASSWORD instead of argv (which is
// visible in process listings). It returns the sanitized URL and the password.
func SplitDatabaseURL(raw string) (sanitized, password string, err error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if parsed.User != nil {
		password, _ = parsed.User.Password()
		if name := parsed.User.Username(); name != "" {
			parsed.User = url.User(name)
		} else {
			parsed.User = nil
		}
	}
	if query := parsed.Query(); query.Has("password") {
		if password == "" {
			password = query.Get("password")
		}
		query.Del("password")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String(), password, nil
}

// Dump runs pg_dump in custom (-Fc) format. Custom format is compressed,
// checksummed per section, and verifiable offline with `pg_restore --list`.
func Dump(ctx context.Context, pgDumpBin, databaseURL, dest string) (int64, error) {
	sanitized, password, err := SplitDatabaseURL(databaseURL)
	if err != nil {
		return 0, err
	}
	var env []string
	if password != "" {
		env = []string{"PGPASSWORD=" + password}
	}
	_, stderr, err := runCmd(ctx, pgDumpBin, env,
		"--format=custom", "--compress=6", "--no-owner",
		"--file", dest, sanitized,
	)
	if err != nil {
		return 0, cmdError("pg_dump", err, stderr)
	}
	info, err := os.Stat(dest)
	if err != nil {
		return 0, fmt.Errorf("pg_dump produced no file: %w", err)
	}
	if info.Size() == 0 {
		return 0, fmt.Errorf("pg_dump produced an empty file")
	}
	return info.Size(), nil
}

// RestoreRehearsal proves a dump restores end-to-end: it creates a throwaway
// database, pg_restores the dump into it, runs a sanity query and drops it.
// The database role needs the CREATEDB privilege (or superuser).
func RestoreRehearsal(ctx context.Context, databaseURL, pgRestoreBin, dumpPath string) error {
	sanitized, password, err := SplitDatabaseURL(databaseURL)
	if err != nil {
		return err
	}
	dbName := fmt.Sprintf("trdriver_verify_%d", time.Now().Unix())
	maintURL, err := replaceDBName(sanitized, "postgres")
	if err != nil {
		return err
	}
	maintPool, err := pgxpool.New(ctx, withPassword(maintURL, sanitized, password))
	if err != nil {
		return fmt.Errorf("rehearsal connect: %w", err)
	}
	defer maintPool.Close()
	quoted := pgx.Identifier{dbName}.Sanitize()
	if _, err := maintPool.Exec(ctx, `CREATE DATABASE `+quoted); err != nil {
		return fmt.Errorf("rehearsal create database (role needs CREATEDB): %w", err)
	}
	defer maintPool.Exec(context.WithoutCancel(ctx), `DROP DATABASE IF EXISTS `+quoted)

	targetURL, err := replaceDBName(sanitized, dbName)
	if err != nil {
		return err
	}
	var env []string
	if password != "" {
		env = []string{"PGPASSWORD=" + password}
	}
	_, stderr, err := runCmd(ctx, pgRestoreBin, env,
		"--dbname", targetURL, "--no-owner", "--exit-on-error", dumpPath)
	if err != nil {
		return cmdError("rehearsal pg_restore", err, stderr)
	}
	checkPool, err := pgxpool.New(ctx, withPassword(targetURL, sanitized, password))
	if err != nil {
		return fmt.Errorf("rehearsal connect restored db: %w", err)
	}
	defer checkPool.Close()
	var users int
	if err := checkPool.QueryRow(ctx, `select count(*) from users`).Scan(&users); err != nil {
		return fmt.Errorf("rehearsal sanity query: %w", err)
	}
	return nil
}

func replaceDBName(rawURL, dbName string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	parsed.Path = "/" + dbName
	return parsed.String(), nil
}

// withPassword re-attaches credentials (already parsed out once) for pgx dials.
func withPassword(rawURL, sanitized, password string) string {
	if password == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	username := ""
	if base, err := url.Parse(sanitized); err == nil && base.User != nil {
		username = base.User.Username()
	}
	parsed.User = url.UserPassword(username, password)
	return parsed.String()
}

// RestoreList runs `pg_restore --list` as an offline integrity check of a
// custom-format dump. It returns the table-of-contents entry count.
func RestoreList(ctx context.Context, pgRestoreBin, dumpPath string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stdout, stderr, err := runCmd(ctx, pgRestoreBin, nil, "--list", dumpPath)
	if err != nil {
		return 0, cmdError("pg_restore --list", err, stderr)
	}
	count := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), ";") {
			count++
		}
	}
	if count == 0 {
		return 0, fmt.Errorf("pg_restore --list: dump has no restorable entries")
	}
	return count, nil
}
