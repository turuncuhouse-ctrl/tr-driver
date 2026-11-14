package backup

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// maxCapturedOutput caps how much child output we keep in memory / DB errors.
const maxCapturedOutput = 16 * 1024

// runCmd executes a child process, capturing stdout and stderr separately.
// env entries are appended to the current environment (KEY=value).
// The returned error never contains env values, but stdout/stderr may contain
// tool diagnostics — callers must not log S3 credentials (restic only echoes
// them with --verbose, which we never pass).
func runCmd(ctx context.Context, name string, env []string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if len(env) > 0 {
		cmd.Env = append(cmd.Environ(), env...)
	}
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return truncate(outBuf.String()), truncate(errBuf.String()), err
}

func truncate(s string) string {
	if len(s) <= maxCapturedOutput {
		return s
	}
	return s[len(s)-maxCapturedOutput:]
}

// cmdError formats a Go error + tool stderr into a short, storable message.
func cmdError(what string, err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("%s: %v", what, err)
	}
	if len(stderr) > 2000 {
		stderr = stderr[len(stderr)-2000:]
	}
	return fmt.Errorf("%s: %v: %s", what, err, stderr)
}
