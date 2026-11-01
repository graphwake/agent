package cli

import (
	"errors"
	"flag"
	"io"
	"regexp"
	"strings"
)

// DefaultAPIURL is Graphwake's production API. It is the only place the address lives; GRAPHWAKE_URL overrides it.
// (Placeholder domain until the production domain is final.)
const DefaultAPIURL = "https://api.graphwake.com"

// Config is the resolved upload configuration: flags first, then GRAPHWAKE_* variables, then the deprecated
// DOCSYNC_* names.
type Config struct {
	Spec, Key, URL    string
	PR, PRURL, Author string
	State             string
	Live              bool
	LiveBranch        string
}

// lookup reads GRAPHWAKE_<suffix>, falling back to the deprecated DOCSYNC_<suffix>.
func lookup(getenv func(string) string, suffix string) string {
	if v := getenv("GRAPHWAKE_" + suffix); v != "" {
		return v
	}
	return getenv("DOCSYNC_" + suffix)
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

const uploadUsage = `usage: graphwake upload --spec <file|http(s)-url> [--pr <id>] [--pr-url <url>] [--author <name>]
                        [--state open|closed|merged] [--live] [--live-branch <branches>] [--url <api-url>]
The ingest key is read from GRAPHWAKE_KEY (--key is accepted but not recommended).
Every flag has a GRAPHWAKE_* environment variable (GRAPHWAKE_SPEC, GRAPHWAKE_PR, GRAPHWAKE_LIVE_BRANCH, ...).`

var errHelp = errors.New("help requested")

// parseUpload reads the upload flags and fills the gaps from the environment.
func parseUpload(args []string, getenv func(string) string) (Config, error) {
	fs := flag.NewFlagSet("upload", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // never echo flag values (the key) back; errors are reported as one warning line
	var c Config
	fs.StringVar(&c.Spec, "spec", "", "")
	fs.StringVar(&c.Key, "key", "", "")
	fs.StringVar(&c.URL, "url", "", "")
	fs.StringVar(&c.PR, "pr", "", "")
	fs.StringVar(&c.PRURL, "pr-url", "", "")
	fs.StringVar(&c.Author, "author", "", "")
	fs.StringVar(&c.State, "state", "", "")
	fs.BoolVar(&c.Live, "live", false, "")
	fs.StringVar(&c.LiveBranch, "live-branch", "", "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, errHelp
		}
		return c, errors.New("invalid arguments: " + flagProblem(err))
	}
	if fs.NArg() > 0 {
		return c, errors.New("unexpected argument (flags take the form --name value)")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	fill := func(v *string, suffix string) {
		if *v == "" {
			*v = lookup(getenv, suffix)
		}
	}
	fill(&c.Spec, "SPEC")
	fill(&c.Key, "KEY")
	fill(&c.URL, "URL")
	fill(&c.PR, "PR")
	fill(&c.PRURL, "PR_URL")
	fill(&c.Author, "AUTHOR")
	fill(&c.State, "STATE")
	fill(&c.LiveBranch, "LIVE_BRANCH")
	if !set["live"] {
		c.Live = truthy(lookup(getenv, "LIVE"))
	}
	if c.URL == "" {
		c.URL = DefaultAPIURL
	}
	c.Key = strings.TrimSpace(c.Key)
	c.PR = strings.TrimPrefix(strings.TrimSpace(c.PR), "#")
	c.State = strings.ToLower(strings.TrimSpace(c.State))
	switch c.State {
	case "", "open", "closed", "merged":
	default:
		return c, errors.New("--state must be open, closed or merged")
	}
	return c, nil
}

var flagNameInError = regexp.MustCompile(` for (?:flag )?-+([A-Za-z-]+):`)

// flagProblem describes a flag error without the argument's value (it may be the key passed by mistake).
func flagProblem(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "flag provided but not defined: "):
		// Never the name: a key pasted as "-dsk_..." would otherwise be printed.
		return "unknown flag"
	case strings.HasPrefix(msg, "flag needs an argument: "):
		return "missing value for " + strings.TrimPrefix(strings.TrimPrefix(msg, "flag needs an argument: "), "-")
	case strings.HasPrefix(msg, "invalid boolean value") || strings.HasPrefix(msg, "invalid value"):
		if m := flagNameInError.FindStringSubmatch(msg); m != nil {
			return "invalid value for --" + m[1]
		}
	}
	return "invalid flag"
}
