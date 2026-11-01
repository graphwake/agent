// Package cli is the graphwake command: it reads the configuration, detects the CI context, decides what to send
// and sends it. Whatever happens it prints one line and never asks for a failing exit code.
package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/graphwake/agent/internal/ci"
	"github.com/graphwake/agent/internal/github"
	"github.com/graphwake/agent/internal/gitx"
	"github.com/graphwake/agent/internal/ingest"
	"github.com/graphwake/agent/internal/report"
	"github.com/graphwake/agent/internal/spec"
)

// Deps are the agent's connections to the outside world, replaced in tests.
type Deps struct {
	Version  string
	Out      io.Writer
	Getenv   ci.Getenv
	ReadFile ci.ReadFile
	Git      gitx.Runner
	// HTTP is used for the ingest call and the GitHub API.
	HTTP           *http.Client
	Spec           *spec.Reader
	Sleep          func(context.Context, time.Duration)
	IngestAttempts int
	IngestBackoff  time.Duration
	// IngestDeadline caps the whole send (retries included); IngestAttempt caps one request.
	IngestDeadline time.Duration
	IngestAttempt  time.Duration
}

// DefaultDeps returns the production wiring.
func DefaultDeps(version string, out io.Writer) Deps {
	return Deps{
		Version:        version,
		Out:            out,
		Getenv:         os.Getenv,
		ReadFile:       os.ReadFile,
		Git:            gitx.Exec{Timeout: 15 * time.Second},
		HTTP:           &http.Client{Timeout: 30 * time.Second},
		IngestDeadline: 30 * time.Second,
		IngestAttempt:  8 * time.Second, // 3 attempts x 8 s + waits 2 s + 4 s = 30 s at most
		Spec:           spec.NewReader(ingest.MaxBodyBytes),
		Sleep:          spec.SleepContext,
		IngestAttempts: 3,
		IngestBackoff:  2 * time.Second,
	}
}

const usage = `graphwake sends your service's OpenAPI spec from CI to Graphwake.

commands:
  graphwake upload --spec <file|http(s)-url> [flags]   send the spec (see "graphwake upload -h")
  graphwake version                                    print the version`

// Main runs the command. It never fails: every problem is one printed line.
func Main(args []string, d Deps) {
	r := &report.Reporter{W: d.Out, Platform: ci.Other}
	if d.Getenv != nil {
		r.Hide(d.Getenv("GRAPHWAKE_KEY"))
		r.Hide(d.Getenv("DOCSYNC_KEY"))
	}
	if len(args) == 0 {
		r.Info("%s", "no command given; use \"graphwake upload --spec <file|url>\" or \"graphwake version\"")
		return
	}
	switch args[0] {
	case "version", "--version", "-version":
		io.WriteString(d.Out, "graphwake "+d.Version+"\n")
	case "upload":
		upload(args[1:], d)
	case "help", "-h", "--help":
		io.WriteString(d.Out, usage+"\n")
	default:
		// Never the argument itself: it may be the key passed by mistake.
		r.Warn("%s", "unknown command; use \"graphwake upload\" or \"graphwake version\"")
	}
}

func upload(args []string, d Deps) {
	ctx := context.Background()
	info := ci.Detect(d.Getenv, d.ReadFile)
	r := &report.Reporter{W: d.Out, Platform: info.Platform}
	r.Hide(info.Token)
	// Register every possible key before parsing, so a parse error can never show it.
	r.Hide(d.Getenv("GRAPHWAKE_KEY"))
	r.Hide(d.Getenv("DOCSYNC_KEY"))
	for _, v := range keyArgValues(args) {
		r.Hide(v)
	}

	cfg, err := parseUpload(args, d.Getenv)
	r.Hide(cfg.Key)
	if errors.Is(err, errHelp) {
		io.WriteString(d.Out, uploadUsage+"\n")
		return
	}
	if err != nil {
		r.Warn("%s; nothing sent", err.Error())
		return
	}
	if cfg.Key == "" {
		r.Warn("GRAPHWAKE_KEY is not set; nothing sent")
		return
	}
	if !spec.IsURL(cfg.URL) {
		r.Warn("GRAPHWAKE_URL must be an http(s) URL; nothing sent")
		return
	}

	head := completeCommit(ctx, d.Git, &info)
	p := decide(cfg, info)
	if p.skip != "" {
		if p.skipWarn {
			r.Warn("%s", p.skip)
		} else {
			r.Info("%s", p.skip)
		}
		return
	}
	if p.needSpec && cfg.Spec == "" {
		r.Warn("no spec given (--spec or GRAPHWAKE_SPEC); nothing sent")
		return
	}

	gh := github.API{BaseURL: info.APIURL, Repo: info.Repo, Token: info.Token, HTTP: d.HTTP}
	if info.Platform != ci.GitHub {
		gh = github.API{}
	}
	// Live sends from branch builds only: the platform's commit must be what is checked out.
	if p.guardBranch != "" && head != "" && info.CommitSHA != "" &&
		!strings.EqualFold(head, info.CommitSHA) {
		r.Info("the checked-out commit is not the commit this build is for, live spec not sent")
		return
	}
	if p.guardBranch != "" && info.CommitSHA != "" {
		if tip := branchTip(ctx, gh, d.Git, p.guardBranch); tip != "" && !strings.EqualFold(tip, info.CommitSHA) {
			r.Info("this build is not the tip of %s (a newer build sends the live spec), nothing sent", p.guardBranch)
			return
		}
	}
	if p.findMerged {
		applyMergedPR(ctx, gh, d.Git, info, &p)
	}

	body := ingest.Body{Context: p.ctx}
	if p.needSpec {
		content, err := d.Spec.Read(ctx, cfg.Spec)
		if errors.Is(err, spec.ErrTooLarge) {
			r.Warn("%s", ingest.ErrBodyTooLarge.Error())
			return
		}
		if err != nil {
			r.Warn("%s; nothing sent", err.Error())
			return
		}
		body.Spec = &content
	}
	encoded, err := ingest.Encode(body)
	if err != nil {
		r.Warn("%s", err.Error())
		return
	}

	client := ingest.Client{
		BaseURL:        cfg.URL,
		Key:            cfg.Key,
		UserAgent:      "graphwake-agent/" + d.Version + " (" + string(info.Platform) + ")",
		HTTP:           d.HTTP,
		Attempts:       d.IngestAttempts,
		Backoff:        d.IngestBackoff,
		Sleep:          d.Sleep,
		Deadline:       d.IngestDeadline,
		AttemptTimeout: d.IngestAttempt,
	}
	res := client.Send(ctx, encoded)
	if !res.OK {
		r.Warn("%s", res.Warning)
		return
	}
	r.Info("%s", successLine(p.ctx))
}

// completeCommit fills the commit SHA and time from git when the platform did not give them and returns
// the checked-out HEAD ("" when git cannot tell). The time is normalised to RFC3339 or dropped.
func completeCommit(ctx context.Context, git gitx.Runner, info *ci.Info) (head string) {
	if sha, err := gitx.HeadCommit(ctx, git); err == nil {
		head = sha
	}
	if info.CommitSHA == "" {
		info.CommitSHA = head
	}
	if info.CommitSHA != "" {
		// The committer date from git orders commits best; the platform's value is the fallback.
		if t, err := gitx.CommitTime(ctx, git, info.CommitSHA); err == nil && t != "" {
			info.CommitTime = t
		}
	}
	info.CommitTime = normaliseTime(info.CommitTime)
	return head
}

// keyArgValues returns the values given to --key/-key on the command line, before any parsing.
func keyArgValues(args []string) []string {
	var keys []string
	for i, a := range args {
		name, value, hasValue := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "key" {
			continue
		}
		if hasValue {
			keys = append(keys, value)
		} else if i+1 < len(args) {
			keys = append(keys, args[i+1])
		}
	}
	return keys
}

func normaliseTime(s string) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return ""
	}
	return t.Format(time.RFC3339)
}

// branchTip returns where the branch points on the remote, or "" when that cannot be found out (then the
// live send goes ahead: the check is best effort).
func branchTip(ctx context.Context, gh github.API, git gitx.Runner, branch string) string {
	if gh.Usable() {
		if sha, err := gh.BranchHead(ctx, branch); err == nil && sha != "" {
			return sha
		}
	}
	if sha, err := gitx.RemoteBranchTip(ctx, git, branch); err == nil {
		return sha
	}
	return ""
}

// applyMergedPR turns a live send into "pull request #n merged" when the commit came from a merged pull request:
// the GitHub API when available (also right for squash and rebase merges), else the merge commit message.
func applyMergedPR(ctx context.Context, gh github.API, git gitx.Runner, info ci.Info, p *plan) {
	var id, url, author string
	apiAnswered := false
	if gh.Usable() && info.CommitSHA != "" {
		if pr, err := gh.MergedPullRequest(ctx, info.CommitSHA, info.Branch); err == nil {
			apiAnswered = true
			if pr != nil {
				id, url, author = strconv.Itoa(pr.Number), pr.URL, pr.Author
			}
		}
	}
	if !apiAnswered {
		message := info.CommitMessage
		rev := info.CommitSHA
		if rev == "" {
			rev = "HEAD"
		}
		if m, err := gitx.CommitMessage(ctx, git, rev); err == nil && m != "" {
			message = m
		}
		if id = ci.MergedPRFromMessage(info.Platform, message); id != "" {
			url = info.PRURLFor(id)
		}
	}
	if id == "" {
		return // a direct push or an unknown merge message: plain live
	}
	p.ctx.Kind = "pullRequest"
	p.ctx.State = "merged"
	p.ctx.PRID, p.ctx.PRURL, p.ctx.Author = id, url, author
}
