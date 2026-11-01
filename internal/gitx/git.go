// Package gitx runs the few read-only git commands the agent needs.
package gitx

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runner runs "git <args>" and returns its trimmed standard output.
type Runner interface {
	Git(ctx context.Context, args ...string) (string, error)
}

// Exec runs the real git binary. It never prompts for credentials and gives up after Timeout.
type Exec struct{ Timeout time.Duration }

func (e Exec) Git(ctx context.Context, args ...string) (string, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_ASKPASS=echo")
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// HeadCommit returns HEAD's SHA.
func HeadCommit(ctx context.Context, r Runner) (string, error) {
	return r.Git(ctx, "rev-parse", "HEAD")
}

// CommitTime returns the committer date of rev in strict ISO 8601 (RFC3339).
func CommitTime(ctx context.Context, r Runner, rev string) (string, error) {
	return r.Git(ctx, "log", "-1", "--format=%cI", rev)
}

// CommitMessage returns the full message of rev.
func CommitMessage(ctx context.Context, r Runner, rev string) (string, error) {
	return r.Git(ctx, "log", "-1", "--format=%B", rev)
}

// RemoteBranchTip returns the SHA origin's branch points at.
func RemoteBranchTip(ctx context.Context, r Runner, branch string) (string, error) {
	out, err := r.Git(ctx, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	sha, _, _ := strings.Cut(out, "\t")
	return strings.TrimSpace(sha), nil
}
