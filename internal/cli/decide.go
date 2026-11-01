package cli

import (
	"fmt"
	"unicode/utf16"

	"github.com/graphwake/agent/internal/ci"
	"github.com/graphwake/agent/internal/ingest"
)

// plan is what the agent will do, decided from the flags and the detected CI context alone (no I/O).
type plan struct {
	// skip, when set, is the reason nothing is sent; skipWarn prints it as a warning instead of an info line.
	skip     string
	skipWarn bool

	ctx      ingest.Context
	needSpec bool

	// guardBranch: a live send from a build of this branch; check first that HEAD is still the branch tip.
	guardBranch string
	// findMerged: a default-branch build; look for the pull request the commit was merged from.
	findMerged bool
}

const notApplicable = "not a pull request or live-branch build, nothing sent"

func decide(cfg Config, info ci.Info) plan {
	p := plan{ctx: ingest.Context{
		Platform:   string(info.Platform),
		CommitSHA:  info.CommitSHA,
		CommitTime: info.CommitTime,
		Branch:     branchContext(info.CommitBranch),
	}}
	branchBuild := info.Event == ci.EventBranch

	switch {
	case cfg.Live:
		// --live ignores any pull request context and publishes the live spec.
		p.ctx.Kind = "live"
		p.needSpec = true
		if branchBuild {
			p.guardBranch = info.Branch
		}
		return p

	case cfg.PR != "":
		p.ctx.Kind = "pullRequest"
		p.ctx.PRID = cfg.PR
		if info.PRID == cfg.PR {
			p.ctx.PRURL, p.ctx.Author = info.PRURL, info.Author
		} else {
			p.ctx.PRURL = info.PRURLFor(cfg.PR)
		}
		p.ctx.State = orDefault(cfg.State, "open")

	case info.Problem != "":
		return plan{skip: info.Problem + ", nothing sent", skipWarn: true}

	case info.Event == ci.EventPRMerged:
		return plan{skip: fmt.Sprintf("pull request #%s merged; the default-branch build reports the merge, nothing sent", info.PRID)}

	case info.Event == ci.EventPRClosed || info.Event == ci.EventPullRequest:
		p.ctx.Kind = "pullRequest"
		p.ctx.PRID, p.ctx.PRURL, p.ctx.Author = info.PRID, info.PRURL, info.Author
		state := "open"
		if info.Event == ci.EventPRClosed {
			state = "closed"
		}
		p.ctx.State = orDefault(cfg.State, state)

	case branchBuild && ci.IsLiveBranch(info.Branch, ci.LiveBranches(cfg.LiveBranch, info)):
		p.ctx.Kind = "live"
		p.needSpec = true
		p.guardBranch = info.Branch
		p.findMerged = true
		return p

	default:
		return plan{skip: notApplicable}
	}

	// A pull request context: flags override what was detected.
	if cfg.PRURL != "" {
		p.ctx.PRURL = cfg.PRURL
	}
	if cfg.Author != "" {
		p.ctx.Author = cfg.Author
	}
	p.needSpec = p.ctx.State != "closed"
	return p
}

// successLine is the one line printed after a successful send.
func successLine(c ingest.Context) string {
	switch {
	case c.Kind == "live":
		return "spec sent (live)"
	case c.State == "closed":
		return fmt.Sprintf("pull request #%s closed, sent", c.PRID)
	case c.State == "merged":
		return fmt.Sprintf("spec sent (pull request #%s merged, live)", c.PRID)
	}
	return fmt.Sprintf("spec sent (pull request #%s)", c.PRID)
}

func orDefault(s, def string) string {
	if s != "" {
		return s
	}
	return def
}

// maxBranchLen is the backend's limit on context.branch, in UTF-16 code units (.NET string length).
const maxBranchLen = 255

// branchContext is the branch as sent: dropped when longer than the backend accepts, so the spec still goes.
func branchContext(branch string) string {
	if len(utf16.Encode([]rune(branch))) > maxBranchLen {
		return ""
	}
	return branch
}
