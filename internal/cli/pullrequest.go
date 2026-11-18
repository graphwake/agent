package cli

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/graphwake/agent/internal/azuredevops"
	"github.com/graphwake/agent/internal/ci"
	"github.com/graphwake/agent/internal/github"
	"github.com/graphwake/agent/internal/ingest"
	"github.com/graphwake/agent/internal/report"
)

const (
	formatGitHub = "github"
	formatAzure  = "azure-devops"

	// acceptLabel on a pull request turns a failed check into an accepted one (matched case-insensitively).
	acceptLabel = "graphwake-accept-breaking"

	defaultPlatformDeadline = 10 * time.Second

	failOnUnsupported = "--fail-on is not supported on this CI yet; not failing"
	noAPIAccess       = ": cannot comment on the pull request or read its labels"
	noCheckResult     = "the server did not return a check result; not failing"
)

// prStep is the pull request part of an upload on an open pull request build: the hints sent with the spec, then
// the comment and the check result from the answer. Every platform problem is a warning; only a failed check
// fails the build.
type prStep struct {
	// platform is formatGitHub or formatAzure on a supported platform, "" otherwise (then no hints are sent).
	platform string
	// api: the platform API can be called (the CI token is there). Without it the check still applies, with no
	// comment and no accept label.
	api bool
	// comment: the comment is written (format sent); failOn: the value sent ("" when no check was asked for).
	comment bool
	failOn  string
	pr      string
	info    ci.Info
	gh      github.API
	ado     azuredevops.API
	budget  platformBudget

	warnings []string
}

// newPRStep prepares the pull request step. It returns nil when the build is not an open pull request build:
// then nothing changes and --fail-on is ignored silently (default-branch and closed pull request builds).
func newPRStep(cfg Config, info ci.Info, p plan, gh github.API, d Deps) *prStep {
	if p.ctx.Kind != "pullRequest" || p.ctx.State != "open" {
		return nil
	}
	s := &prStep{pr: p.ctx.PRID, info: info, gh: gh, budget: platformBudget{left: d.PlatformDeadline}}
	if s.budget.left <= 0 {
		s.budget.left = defaultPlatformDeadline
	}
	tokenName := ""
	switch info.Platform {
	case ci.GitHub:
		s.platform, s.api, tokenName = formatGitHub, gh.Usable(), "GITHUB_TOKEN"
	case ci.Azure:
		// Azure Pipelines can build GitHub or Bitbucket repositories; the thread and label APIs are Azure Repos only.
		if info.AzureRepoProvider == "TfsGit" {
			s.ado = azuredevops.API{CollectionURI: info.AzureCollectionURI, Project: info.AzureProject,
				RepoID: info.AzureRepoID, Token: info.Token, HTTP: d.HTTP}
			s.platform, s.api, tokenName = formatAzure, s.ado.Usable(), "SYSTEM_ACCESSTOKEN"
		}
	}
	if s.platform == "" {
		if cfg.FailOn != "" {
			s.warn(failOnUnsupported)
		}
		return s
	}
	s.failOn = cfg.FailOn
	s.comment = !cfg.NoComment && s.api
	if !s.api && (!cfg.NoComment || cfg.FailOn != "") {
		s.warn("no " + tokenName + noAPIAccess)
	}
	return s
}

func (s *prStep) warn(w string) { s.warnings = append(s.warnings, w) }

// hints reads what the server needs (visibility for the comment, labels for the check) and returns the
// "pullRequest" object of the ingest request; nil on an unsupported platform.
func (s *prStep) hints(ctx context.Context) *ingest.PullRequestHints {
	if s == nil || s.platform == "" {
		return nil
	}
	h := &ingest.PullRequestHints{FailOn: s.failOn}
	ctx, done := s.budget.start(ctx)
	defer done()
	if s.comment {
		h.CommentFormat = s.platform
	}
	h.RepoVisibility = knownVisibility(s.visibility(ctx))
	if s.failOn != "" && s.api {
		// Read at run time, not from the event payload: a re-run carries the old payload without a newer label.
		labels, err := s.labels(ctx)
		if err != nil {
			s.warn("could not read the pull request labels (" + platformReason(err) + ")")
		}
		for _, l := range labels {
			h.AcceptLabel = h.AcceptLabel || strings.EqualFold(strings.TrimSpace(l), acceptLabel)
		}
	}
	return h
}

// visibility is the repository's visibility: GitHub's from the event payload; Azure's from the API, asked only
// when the comment (the one place it is used) is written. "" when unknown.
func (s *prStep) visibility(ctx context.Context) string {
	if s.platform == formatGitHub {
		return s.info.RepoVisibility
	}
	if !s.comment {
		return ""
	}
	v, _ := s.ado.Visibility(ctx) // unknown on failure
	return v
}

func knownVisibility(v string) string {
	switch v = strings.ToLower(v); v {
	case "private", "internal", "public":
		return v
	}
	return "unknown"
}

func (s *prStep) labels(ctx context.Context) ([]string, error) {
	if s.platform == formatGitHub {
		return s.gh.Labels(ctx, s.pr)
	}
	return s.ado.Labels(ctx, s.pr)
}

// finish writes the comment from a successful answer, prints the one output line (the success text, the
// check's message, then the warnings) and returns the exit code: 1 only for a failed check.
func (s *prStep) finish(ctx context.Context, r *report.Reporter, success string, out *ingest.PROutcome) int {
	if s == nil {
		r.Info("%s", success)
		return 0
	}
	parts := []string{success}
	failed, invalid := false, false
	switch {
	case s.platform == "":
	case out == nil:
		if s.failOn != "" {
			s.warn(noCheckResult) // an older server
		}
	default:
		if s.comment && out.Comment != nil {
			s.writeComment(ctx, *out.Comment)
		}
		failed = s.failOn != "" && out.Gate.Result == "failed"
		invalid = out.Gate.Result == "invalid"
		if m := strings.TrimSpace(out.Gate.Message); m != "" {
			parts = append(parts, m)
		}
	}
	line := strings.Join(append(parts, s.warnings...), "; ")
	if failed || invalid || len(s.warnings) > 0 {
		r.Warn("%s", line)
	} else {
		r.Info("%s", line)
	}
	if failed {
		return 1
	}
	return 0
}

// failure prints a failed send with the step's warnings on the same line.
func (s *prStep) failure(r *report.Reporter, warning string) {
	if s != nil {
		warning = strings.Join(append([]string{warning}, s.warnings...), "; ")
	}
	r.Warn("%s", warning)
}

// writeComment applies the server's comment: "upsert" updates the service's comment or creates it, "resolve"
// only updates an existing one.
func (s *prStep) writeComment(ctx context.Context, c ingest.Comment) {
	if c.Key == "" || (c.Action != "upsert" && c.Action != "resolve") {
		return
	}
	ctx, done := s.budget.start(ctx)
	defer done()
	var err error
	if s.platform == formatGitHub {
		err = s.githubComment(ctx, c)
	} else {
		err = s.azureComment(ctx, c)
	}
	if err != nil {
		s.warn("could not comment on the pull request (" + platformReason(err) + ")")
	}
}

func (s *prStep) githubComment(ctx context.Context, c ingest.Comment) error {
	id, found, err := s.gh.FindComment(ctx, s.pr, github.Marker(c.Key))
	switch {
	case err != nil:
		return err
	case found:
		return s.gh.UpdateComment(ctx, id, c.Body)
	case c.Action == "upsert":
		return s.gh.CreateComment(ctx, s.pr, c.Body)
	}
	return nil
}

func (s *prStep) azureComment(ctx context.Context, c ingest.Comment) error {
	thread, comment, found, err := s.ado.FindThread(ctx, s.pr, c.Key)
	status := "active"
	if c.Action == "resolve" {
		status = "fixed"
	}
	switch {
	case err != nil:
		return err
	case found:
		return s.ado.UpdateThread(ctx, s.pr, thread, comment, c.Body, status)
	case c.Action == "upsert":
		return s.ado.CreateThread(ctx, s.pr, c.Key, c.Body)
	}
	return nil
}

// platformBudget is the time all platform API calls of one run share.
type platformBudget struct{ left time.Duration }

// start returns a context bounded by what is left of the budget; done charges the time spent.
func (b *platformBudget) start(ctx context.Context) (context.Context, func()) {
	began := time.Now()
	ctx, cancel := context.WithTimeout(ctx, b.left)
	return ctx, func() {
		cancel()
		b.left -= time.Since(began)
	}
}

var answeredStatus = regexp.MustCompile(`answered (\d{3})`)

// platformReason is a short, safe description of a platform API failure (never a URL or a token).
func platformReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	if m := answeredStatus.FindStringSubmatch(err.Error()); m != nil {
		return "HTTP " + m[1]
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		if uerr.Timeout() {
			return "timed out"
		}
		return "network error"
	}
	return "unreadable answer"
}
