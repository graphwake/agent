package cli

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/graphwake/agent/internal/ci"
	"github.com/graphwake/agent/internal/report"
	"github.com/graphwake/agent/internal/spec"
	"github.com/graphwake/agent/internal/usage"
)

const usageHelp = `usage: graphwake usage --source <preset> [--input <file>] [--url <api-url>] [--service <name>]
                       [--caller-field <field>] [--no-counts] [--dry-run]
       graphwake usage query --source <preset>
Reads a gateway log or trace export from --input or stdin and sends Graphwake a summary: which caller used which
endpoint, how often and when last. Raw paths and personal-looking caller names stay on this machine.
Presets: apim, generic, otel. "graphwake usage query" prints the query or configuration that produces the input.
--dry-run prints the summary that would be sent and what stays on this machine, and sends nothing.
The usage key is read from GRAPHWAKE_USAGE_KEY (--key is accepted but not recommended).
Every flag has a GRAPHWAKE_USAGE_* environment variable (GRAPHWAKE_USAGE_SOURCE, GRAPHWAKE_USAGE_INPUT, ...).`

// usageConfig is the resolved usage configuration: flags first, then GRAPHWAKE_USAGE_* variables (the API URL:
// GRAPHWAKE_URL).
type usageConfig struct {
	Source, Input, Key, URL, Service, CallerField string
	NoCounts, DryRun                              bool
}

// parseUsage reads the usage flags and fills the gaps from the environment.
func parseUsage(args []string, getenv func(string) string) (usageConfig, error) {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // never echo flag values (the key) back
	var c usageConfig
	fs.StringVar(&c.Source, "source", "", "")
	fs.StringVar(&c.Input, "input", "", "")
	fs.StringVar(&c.Key, "key", "", "")
	fs.StringVar(&c.URL, "url", "", "")
	fs.StringVar(&c.Service, "service", "", "")
	fs.StringVar(&c.CallerField, "caller-field", "", "")
	fs.BoolVar(&c.NoCounts, "no-counts", false, "")
	fs.BoolVar(&c.DryRun, "dry-run", false, "")
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
			*v = getenv("GRAPHWAKE_USAGE_" + suffix)
		}
		*v = strings.TrimSpace(*v)
	}
	fill(&c.Source, "SOURCE")
	fill(&c.Input, "INPUT")
	fill(&c.Key, "KEY")
	fill(&c.Service, "SERVICE")
	fill(&c.CallerField, "CALLER_FIELD")
	if !set["no-counts"] {
		c.NoCounts = truthy(getenv("GRAPHWAKE_USAGE_NO_COUNTS"))
	}
	if !set["dry-run"] {
		c.DryRun = truthy(getenv("GRAPHWAKE_USAGE_DRY_RUN"))
	}
	if c.URL == "" {
		c.URL = lookup(getenv, "URL")
	}
	if c.URL == "" {
		c.URL = DefaultAPIURL
	}
	return c, nil
}

// usageStart prepares a usage run: the reporter with every key hidden, then the parsed configuration. ok is false
// when the run is over (help printed, or a configuration problem reported).
func usageStart(args []string, d Deps) (r *report.Reporter, cfg usageConfig, platform ci.Platform, ok bool) {
	getenv := d.Getenv
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	info := ci.Detect(getenv, d.ReadFile)
	r = &report.Reporter{W: d.Out, Platform: info.Platform}
	r.Hide(info.Token)
	// Register every possible key before parsing, so a parse error can never show it.
	for _, name := range []string{"GRAPHWAKE_USAGE_KEY", "GRAPHWAKE_KEY", "DOCSYNC_KEY"} {
		r.Hide(getenv(name))
	}
	for _, v := range keyArgValues(args) {
		r.Hide(v)
	}
	cfg, err := parseUsage(args, getenv)
	r.Hide(cfg.Key)
	if errors.Is(err, errHelp) {
		io.WriteString(d.Out, usageHelp+"\n")
		return r, cfg, info.Platform, false
	}
	if err != nil {
		r.Warn("usage not sent: %s", err.Error())
		return r, cfg, info.Platform, false
	}
	return r, cfg, info.Platform, true
}

// presetName tells a preset name from something else pasted into --source (such as a key): only such a name is
// repeated in a warning, exactly as given (so a registered secret is still masked).
var presetName = regexp.MustCompile(`^[a-z0-9-]{1,20}$`)

// findPreset returns the preset cfg.Source names; ok is false when there is none (problem says why).
func findPreset(source string) (p usage.Preset, problem string, ok bool) {
	names := strings.Join(usage.PresetNames(), ", ")
	if source == "" {
		return p, "no source given (one of: " + names + ")", false
	}
	if p, ok = usage.PresetByName(strings.ToLower(source)); ok {
		return p, "", true
	}
	if presetName.MatchString(source) {
		return p, `unknown source "` + source + `" (one of: ` + names + ")", false
	}
	return p, "unknown source (one of: " + names + ")", false
}

// usageQuery prints the preset's query or configuration text (several lines, no network call).
func usageQuery(args []string, d Deps) int {
	r, cfg, _, ok := usageStart(args, d)
	if !ok {
		return 0
	}
	p, problem, ok := findPreset(cfg.Source)
	if !ok {
		r.Warn("%s", problem)
		return 0
	}
	io.WriteString(d.Out, p.Query)
	return 0
}

// usageSend reads the input, matches it to the endpoint templates and sends the summary (or, with --dry-run, prints
// it). Whatever happens it prints one line and returns 0; --dry-run prints the summary instead.
func usageSend(args []string, d Deps) int {
	r, cfg, platform, ok := usageStart(args, d)
	if !ok {
		return 0
	}
	preset, problem, ok := findPreset(cfg.Source)
	if !ok {
		r.Warn("usage not sent: %s", problem)
		return 0
	}
	if cfg.Key == "" {
		r.Warn("usage not sent: no usage key (set GRAPHWAKE_USAGE_KEY)")
		return 0
	}
	if !spec.IsURL(cfg.URL) {
		r.Warn("usage not sent: GRAPHWAKE_URL must be an http(s) URL")
		return 0
	}
	in, closeInput, problem := openUsageInput(cfg.Input, d.Stdin)
	if problem != "" {
		r.Warn("usage not sent: %s", problem)
		return 0
	}
	defer closeInput()

	ctx := context.Background()
	sleep := d.Sleep
	if sleep == nil {
		sleep = spec.SleepContext
	}
	client := usage.Client{
		BaseURL:   cfg.URL,
		Key:       cfg.Key,
		UserAgent: "graphwake-agent/" + d.Version + " (" + string(platform) + ")",
		HTTP:      d.HTTP,
		Sleep: func(ctx context.Context, wait time.Duration) error {
			sleep(ctx, wait)
			return ctx.Err()
		},
	}
	templates, res := client.Templates(ctx)
	if !res.OK {
		r.Warn("%s", res.Warning)
		return 0
	}
	matcher, err := usage.NewMatcher(templates, cfg.Service)
	if err != nil {
		// The error names the service; it is not repeated.
		r.Warn("usage not sent: --service names no service in the endpoint list Graphwake sent")
		return 0
	}

	agg := usage.NewAggregator(matcher, cfg.NoCounts)
	var records, requests int64
	skipped, err := preset.Parse(in, usage.ParseOptions{CallerField: cfg.CallerField}, func(rec usage.Record) {
		records++
		requests += max(rec.Count, 1)
		agg.Add(rec)
	})
	if err != nil {
		r.Warn("usage not sent: the input could not be read")
		return 0
	}
	if records == 0 && skipped == 0 {
		if cfg.Input != "" {
			r.Warn("usage not sent: the input file is empty")
		} else {
			r.Warn("usage not sent: %s", noInput)
		}
		return 0
	}

	snap, err := agg.Snapshot(preset.Name, d.Version)
	var tooMany *usage.TooManyCallersError
	if errors.As(err, &tooMany) {
		r.Warn("usage not sent: too many distinct callers (%d, limit %d). The caller field may identify end users; "+
			"choose another with --caller-field", tooMany.Callers, tooMany.Limit)
		return 0
	}
	if err != nil {
		r.Warn("usage not sent: the summary could not be built")
		return 0
	}
	if !cfg.DryRun && len(snap.Rows) == 0 {
		r.Warn("usage not sent: %s", emptySummary(snap, requests))
		return 0
	}
	body, err := usage.Encode(snap)
	if err != nil {
		r.Warn("usage not sent: the summary is too large")
		return 0
	}
	if cfg.DryRun {
		usage.RenderDryRun(d.Out, body, agg.UnmatchedByPrefix(), snap.WithheldRequests, snap.AnonymousRequests)
		return 0
	}

	res = client.Send(ctx, body)
	if !res.OK {
		r.Warn("%s", res.Warning)
		return 0
	}
	r.Info("%s", res.Info+usageNotes(snap, skipped))
	return 0
}

const noInput = "no input (give --input or pipe a file)"

// openUsageInput opens the --input file, else standard input when something is piped into it. A terminal is no
// input: reading it would wait for typing. problem is the warning when there is nothing to read; the path is never
// part of it.
func openUsageInput(path string, stdin io.Reader) (in io.Reader, closeInput func(), problem string) {
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return nil, nil, "the input file could not be opened"
		}
		return f, func() { f.Close() }, ""
	}
	if stdin == nil {
		return nil, nil, noInput
	}
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err != nil || fi.Mode()&os.ModeCharDevice != 0 {
			return nil, nil, noInput
		}
	}
	return stdin, func() {}, ""
}

// emptySummary says why a summary without rows is not sent.
func emptySummary(snap usage.Snapshot, requests int64) string {
	switch {
	case snap.AnonymousRequests == requests:
		return "no caller names were found in the input. Check --caller-field"
	case snap.UnmatchedRequests == 0:
		return "no caller name could be sent: every name looked personal (an e-mail address, an IP address or a token) " +
			"or was missing. Check --caller-field"
	}
	return "nothing in the input matched an endpoint"
}

// usageNotes are the sentences after the success line: what was counted but not sent, and the lines the parser
// skipped.
func usageNotes(snap usage.Snapshot, skipped int64) string {
	var b strings.Builder
	if n := snap.UnmatchedRequests; n > 0 {
		b.WriteString(" " + usage.Plural(n, "request", "requests") + " matched no endpoint.")
	}
	if n := snap.AnonymousRequests; n > 0 {
		b.WriteString(" " + usage.Plural(n, "request", "requests") + " had no caller name.")
	}
	if n := snap.WithheldRequests; n == 1 {
		b.WriteString(" 1 request was withheld because the caller name looked personal.")
	} else if n > 1 {
		b.WriteString(" " + usage.Plural(n, "request", "requests") + " were withheld because the caller name looked personal.")
	}
	if skipped > 0 {
		b.WriteString(" " + usage.Plural(skipped, "line", "lines") + " could not be read.")
	}
	return b.String()
}
