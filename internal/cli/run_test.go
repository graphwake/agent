package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/graphwake/agent/internal/ingest"
	"github.com/graphwake/agent/internal/spec"
)

const (
	testKey  = "dsk_TOPSECRET_key_value"
	ghToken  = "ghs_TOPSECRET_token"
	mainSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeGit answers git commands from a table; anything else fails like a missing git.
type fakeGit map[string]string

func (g fakeGit) Git(_ context.Context, args ...string) (string, error) {
	if out, ok := g[strings.Join(args, " ")]; ok {
		return out, nil
	}
	return "", errors.New("git: not available")
}

// backend is a fake Graphwake ingest endpoint plus a fake GitHub API.
type backend struct {
	t        *testing.T
	srv      *httptest.Server
	mu       sync.Mutex
	requests []map[string]any
	auth     []string
	status   int
	body     string
	ghTip    string // "" → 404
	ghPulls  string // "" → 500
	ghCalls  int
}

func newBackend(t *testing.T) *backend {
	b := &backend{t: t, status: 200, body: `{"outcome":"Changed","changes":[]}`}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		switch {
		case r.URL.Path == "/api/ingest/specs":
			data, _ := io.ReadAll(r.Body)
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				t.Errorf("ingest body is not JSON: %s", data)
			}
			b.requests = append(b.requests, m)
			b.auth = append(b.auth, r.Header.Get("Authorization"))
			w.WriteHeader(b.status)
			io.WriteString(w, b.body)
		case strings.HasPrefix(r.URL.Path, "/gh/"):
			b.ghCalls++
			if r.Header.Get("Authorization") != "Bearer "+ghToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/pulls") {
				if b.ghPulls == "" {
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				io.WriteString(w, b.ghPulls)
				return
			}
			if b.ghTip == "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			io.WriteString(w, b.ghTip)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) sent() []map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.requests
}

type run struct {
	env  map[string]string
	git  fakeGit
	args []string
}

func (b *backend) run(t *testing.T, r run) string {
	t.Helper()
	dir := t.TempDir()
	specPath := filepath.Join(dir, "openapi.json")
	os.WriteFile(specPath, []byte(`{"openapi":"3.0.0"}`), 0o600)

	env := map[string]string{"GRAPHWAKE_KEY": testKey, "GRAPHWAKE_URL": b.srv.URL, "GRAPHWAKE_SPEC": specPath}
	for k, v := range r.env {
		if v == "<unset>" {
			delete(env, k)
			continue
		}
		env[k] = strings.ReplaceAll(v, "{srv}", b.srv.URL)
	}
	var out bytes.Buffer
	reader := spec.NewReader(ingest.MaxBodyBytes)
	reader.Sleep = func(context.Context, time.Duration) {}
	reader.Deadline = 2 * time.Second
	d := Deps{
		Version:        "1.0.0-test",
		Out:            &out,
		Getenv:         func(k string) string { return env[k] },
		ReadFile:       os.ReadFile,
		Git:            r.git,
		HTTP:           &http.Client{Timeout: 2 * time.Second},
		Spec:           reader,
		Sleep:          func(context.Context, time.Duration) {},
		IngestAttempts: 3,
		IngestBackoff:  time.Millisecond,
	}
	if d.Git == nil {
		d.Git = fakeGit{}
	}
	Main(append([]string{"upload"}, r.args...), d)
	s := out.String()
	if strings.Count(s, "\n") != 1 {
		t.Errorf("want exactly one output line, got %q", s)
	}
	if strings.Contains(s, testKey) || strings.Contains(s, ghToken) {
		t.Errorf("secret printed: %q", s)
	}
	return strings.TrimSuffix(s, "\n")
}

func githubEnv(event, payload string, extra map[string]string) map[string]string {
	m := map[string]string{
		"GITHUB_ACTIONS":    "true",
		"GITHUB_EVENT_NAME": event,
		"GITHUB_REPOSITORY": "acme/orders",
		"GITHUB_API_URL":    "{srv}/gh",
		"GITHUB_TOKEN":      ghToken,
		"GITHUB_SHA":        mainSHA,
		"GITHUB_EVENT_PATH": payload,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func writePayload(t *testing.T, json string) string {
	p := filepath.Join(t.TempDir(), "event.json")
	os.WriteFile(p, []byte(json), 0o600)
	return p
}

const prPayload = `{"action":"%s","pull_request":{"number":42,"html_url":"https://github.com/acme/orders/pull/42","merged":%s,
  "user":{"login":"octocat"},"head":{"sha":"` + otherSHA + `"}},"repository":{"default_branch":"main"}}`

func prEvent(t *testing.T, action, merged string) string {
	return writePayload(t, strings.Replace(strings.Replace(prPayload, "%s", action, 1), "%s", merged, 1))
}

func ctxOf(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	c, ok := req["context"].(map[string]any)
	if !ok {
		t.Fatalf("no context in %v", req)
	}
	return c
}

func TestGitHubPullRequestOpen(t *testing.T) {
	b := newBackend(t)
	out := b.run(t, run{env: githubEnv("pull_request", prEvent(t, "opened", "false"),
		map[string]string{"GITHUB_HEAD_REF": "feature/orders"}),
		git: fakeGit{"log -1 --format=%cI " + otherSHA: "2026-10-30T09:00:00+03:00"}})
	if out != "graphwake: spec sent (pull request #42)" {
		t.Fatalf("out = %q", out)
	}
	reqs := b.sent()
	if len(reqs) != 1 || reqs[0]["spec"] != `{"openapi":"3.0.0"}` || b.auth[0] != "Bearer "+testKey {
		t.Fatalf("requests %v auth %v", reqs, b.auth)
	}
	want := map[string]any{"kind": "pullRequest", "prId": "42", "prUrl": "https://github.com/acme/orders/pull/42",
		"author": "octocat", "state": "open", "platform": "github", "commitSha": otherSHA, "commitTime": "2026-10-30T09:00:00+03:00",
		"branch": "feature/orders"}
	assertContext(t, ctxOf(t, reqs[0]), want)
}

func TestGitHubPullRequestMergedSendsNothing(t *testing.T) {
	b := newBackend(t)
	out := b.run(t, run{env: githubEnv("pull_request", prEvent(t, "closed", "true"), nil)})
	if !strings.Contains(out, "default-branch build reports the merge, nothing sent") || len(b.sent()) != 0 {
		t.Fatalf("out = %q, sent %v", out, b.sent())
	}
}

func TestGitHubPullRequestClosedSendsNoSpec(t *testing.T) {
	b := newBackend(t)
	out := b.run(t, run{env: githubEnv("pull_request", prEvent(t, "closed", "false"), map[string]string{"GRAPHWAKE_SPEC": "<unset>"})})
	if out != "graphwake: pull request #42 closed, sent" {
		t.Fatalf("out = %q", out)
	}
	req := b.sent()[0]
	if _, has := req["spec"]; has {
		t.Fatalf("closed PR carried a spec: %v", req)
	}
	assertContext(t, ctxOf(t, req), map[string]any{"kind": "pullRequest", "prId": "42", "state": "closed"})
}

func TestGitHubDefaultBranchPushFindsMergedPRThroughAPI(t *testing.T) {
	b := newBackend(t)
	b.ghTip = mainSHA
	b.ghPulls = `[{"number":7,"html_url":"https://github.com/acme/orders/pull/7","merged_at":null,"base":{"ref":"main"}},
	  {"number":42,"html_url":"https://github.com/acme/orders/pull/42","merged_at":"2026-10-30T10:00:00Z","user":{"login":"octocat"},"base":{"ref":"main"}}]`
	push := writePayload(t, `{"head_commit":{"message":"Squashed work","timestamp":"2026-10-30T10:00:00Z"},"repository":{"default_branch":"main"}}`)
	out := b.run(t, run{env: githubEnv("push", push, map[string]string{"GITHUB_REF": "refs/heads/main"})})
	if out != "graphwake: spec sent (pull request #42 merged, live)" {
		t.Fatalf("out = %q", out)
	}
	assertContext(t, ctxOf(t, b.sent()[0]), map[string]any{"kind": "pullRequest", "prId": "42", "state": "merged",
		"prUrl": "https://github.com/acme/orders/pull/42", "author": "octocat", "commitSha": mainSHA,
		"commitTime": "2026-10-30T10:00:00Z", "platform": "github"})
}

func TestGitHubDirectPushIsLive(t *testing.T) {
	b := newBackend(t)
	b.ghTip = mainSHA
	b.ghPulls = `[]`
	push := writePayload(t, `{"head_commit":{"message":"Fix typo (#99)"},"repository":{"default_branch":"main"}}`)
	out := b.run(t, run{env: githubEnv("push", push, map[string]string{"GITHUB_REF": "refs/heads/main"})})
	if out != "graphwake: spec sent (live)" {
		t.Fatalf("out = %q (the API's answer must win over the message)", out)
	}
	c := ctxOf(t, b.sent()[0])
	if c["kind"] != "live" || c["prId"] != nil || c["state"] != nil || c["branch"] != "main" {
		t.Fatalf("context %v", c)
	}
}

// The backend takes at most 255 characters of branch; a longer name is left out and the spec still goes.
func TestOverlongBranchIsNotSent(t *testing.T) {
	b := newBackend(t)
	long := strings.Repeat("b", 256)
	out := b.run(t, run{env: githubEnv("pull_request", prEvent(t, "opened", "false"),
		map[string]string{"GITHUB_HEAD_REF": long})})
	if out != "graphwake: spec sent (pull request #42)" {
		t.Fatalf("out = %q", out)
	}
	if c := ctxOf(t, b.sent()[0]); c["branch"] != nil {
		t.Fatalf("branch sent: %v", c["branch"])
	}
}

func TestGitHubWithoutAPIFallsBackToMessageAndGit(t *testing.T) {
	b := newBackend(t)
	push := writePayload(t, `{"head_commit":{"message":"Merge pull request #12 from a/b"},"repository":{"default_branch":"main"}}`)
	env := githubEnv("push", push, map[string]string{"GITHUB_REF": "refs/heads/main", "GITHUB_TOKEN": "<unset>"})
	out := b.run(t, run{env: env, git: fakeGit{"ls-remote origin refs/heads/main": mainSHA + "\trefs/heads/main"}})
	if out != "graphwake: spec sent (pull request #12 merged, live)" || b.ghCalls != 0 {
		t.Fatalf("out = %q, gh calls %d", out, b.ghCalls)
	}
	assertContext(t, ctxOf(t, b.sent()[0]), map[string]any{"prUrl": "https://github.com/acme/orders/pull/12"})
}

func TestTipOfBranchGuard(t *testing.T) {
	gitlab := func(extra map[string]string) map[string]string {
		m := map[string]string{"GITLAB_CI": "true", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main",
			"CI_COMMIT_SHA": mainSHA, "CI_PROJECT_URL": "https://gitlab.com/acme/orders"}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name     string
		env      map[string]string
		git      fakeGit
		ghTip    string
		wantSent bool
		wantOut  string
	}{
		{"head is the tip", gitlab(nil), fakeGit{"ls-remote origin refs/heads/main": mainSHA + "\trefs/heads/main"}, "", true, "graphwake: spec sent (live)"},
		{"a newer commit is on the branch", gitlab(nil), fakeGit{"ls-remote origin refs/heads/main": otherSHA + "\trefs/heads/main"}, "", false,
			"graphwake: this build is not the tip of main (a newer build sends the live spec), nothing sent"},
		{"git cannot tell: send anyway", gitlab(nil), fakeGit{}, "", true, "graphwake: spec sent (live)"},
		{"github API says stale", githubEnv("push", "", map[string]string{"GITHUB_REF": "refs/heads/main"}), fakeGit{}, otherSHA, false,
			"graphwake: this build is not the tip of main (a newer build sends the live spec), nothing sent"},
		{"github API down, git says tip", githubEnv("push", "", map[string]string{"GITHUB_REF": "refs/heads/main"}),
			fakeGit{"ls-remote origin refs/heads/main": mainSHA + "\trefs/heads/main"}, "", true, "graphwake: spec sent (live)"},
		{"merged PR found from the gitlab message", gitlab(nil), fakeGit{
			"ls-remote origin refs/heads/main": mainSHA + "\trefs/heads/main",
			"log -1 --format=%B " + mainSHA:    "Merge branch 'f' into 'main'\n\nSee merge request acme/orders!7"},
			"", true, "graphwake: spec sent (pull request #7 merged, live)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBackend(t)
			b.ghTip = tt.ghTip
			out := b.run(t, run{env: tt.env, git: tt.git})
			if out != tt.wantOut || (len(b.sent()) == 1) != tt.wantSent {
				t.Fatalf("out = %q, sent %d", out, len(b.sent()))
			}
		})
	}
}

func TestLiveBranchRule(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		args    []string
		wantOut string
	}{
		{"feature branch push", map[string]string{"GITLAB_CI": "true", "CI_COMMIT_BRANCH": "feature/x", "CI_DEFAULT_BRANCH": "main"}, nil,
			"graphwake: " + notApplicable},
		{"azure main without default-branch variable", map[string]string{"TF_BUILD": "True", "BUILD_REASON": "IndividualCI",
			"BUILD_SOURCEBRANCH": "refs/heads/main"}, nil, "graphwake: spec sent (live)"},
		{"azure master", map[string]string{"TF_BUILD": "True", "BUILD_SOURCEBRANCH": "refs/heads/master"}, nil, "graphwake: spec sent (live)"},
		{"azure hotfix", map[string]string{"TF_BUILD": "True", "BUILD_SOURCEBRANCH": "refs/heads/hotfix"}, nil, "graphwake: " + notApplicable},
		{"release pattern from env", map[string]string{"BITBUCKET_BUILD_NUMBER": "1", "BITBUCKET_BRANCH": "release/2.0",
			"GRAPHWAKE_LIVE_BRANCH": "main,release/*"}, nil, "graphwake: spec sent (live)"},
		{"deprecated DOCSYNC_LIVE_BRANCH", map[string]string{"JENKINS_URL": "x", "BRANCH_NAME": "production",
			"DOCSYNC_LIVE_BRANCH": "production"}, nil, "graphwake: spec sent (live)"},
		{"flag overrides env", map[string]string{"JENKINS_URL": "x", "BRANCH_NAME": "production",
			"GRAPHWAKE_LIVE_BRANCH": "main"}, []string{"--live-branch", "production"}, "graphwake: spec sent (live)"},
		{"unknown CI sends nothing", map[string]string{}, nil, "graphwake: " + notApplicable},
		{"unknown CI with --live", map[string]string{}, []string{"--live"}, "graphwake: spec sent (live)"},
		{"GRAPHWAKE_LIVE=true", map[string]string{"GRAPHWAKE_LIVE": "true"}, nil, "graphwake: spec sent (live)"},
		{"--live ignores the PR", map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "3"}, []string{"--live"},
			"graphwake: spec sent (live)"},
		{"manual PR on unknown CI", map[string]string{}, []string{"--pr", "#8", "--pr-url", "https://x/8", "--author", "me"},
			"graphwake: spec sent (pull request #8)"},
		{"jenkins change request", map[string]string{"JENKINS_URL": "x", "CHANGE_ID": "5", "BRANCH_NAME": "PR-5"}, nil,
			"graphwake: spec sent (pull request #5)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBackend(t)
			out := b.run(t, run{env: tt.env, args: tt.args})
			if out != tt.wantOut {
				t.Fatalf("out = %q, want %q", out, tt.wantOut)
			}
		})
	}
}

func TestManualFlagsOverrideDetection(t *testing.T) {
	b := newBackend(t)
	b.run(t, run{env: map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "3", "CI_PROJECT_URL": "https://gitlab.com/a/b",
		"GITLAB_USER_LOGIN": "dev"}, args: []string{"--author", "someone", "--state", "merged"}})
	assertContext(t, ctxOf(t, b.sent()[0]), map[string]any{"kind": "pullRequest", "prId": "3", "author": "someone",
		"state": "merged", "platform": "gitlab", "prUrl": "https://gitlab.com/a/b/-/merge_requests/3"})
}

// Every answer of the backend gives exactly one line, never the key, and (Main returns) no failing exit.
func TestEveryResponseIsOneSafeLine(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   string
	}{
		{200, `{"outcome":"Changed","changes":[{"method":"DELETE","path":"/orders"}]}`, "graphwake: spec sent (live)"},
		{401, ``, "WARNING graphwake: invalid or missing ingest key, spec not sent"},
		{402, `{"code":"account_closed","error":"closed"}`, "WARNING graphwake: the Graphwake account is closed, spec not sent"},
		{409, `{"code":"url_source","error":"x"}`, "WARNING graphwake: this service takes its spec from a URL, not from CI; spec not sent"},
		{413, ``, "WARNING graphwake: spec too large (limit 10 MB), not sent"},
		{422, `{"error":"bad spec ` + testKey + `\nsecond line"}`, "WARNING graphwake: spec rejected: bad spec *** second line"},
		{400, ``, "WARNING graphwake: request rejected: bad request"},
		{415, ``, "WARNING graphwake: request rejected: bad request"},
		{503, ``, "WARNING graphwake: Graphwake unavailable, spec not sent"},
		{418, ``, "WARNING graphwake: unexpected response 418, spec not sent"},
	}
	for _, tt := range tests {
		b := newBackend(t)
		b.status, b.body = tt.status, tt.body
		out := b.run(t, run{args: []string{"--live"}})
		if out != tt.want {
			t.Errorf("%d: out = %q, want %q", tt.status, out, tt.want)
		}
	}
}

func TestGitHubWarningFormat(t *testing.T) {
	b := newBackend(t)
	b.status = 401
	out := b.run(t, run{env: githubEnv("pull_request", prEvent(t, "opened", "false"), nil)})
	if out != "::warning::graphwake: invalid or missing ingest key, spec not sent" {
		t.Fatalf("out = %q", out)
	}
}

func TestConfigurationProblems(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"no key", map[string]string{"GRAPHWAKE_KEY": "<unset>"}, []string{"--live"}, "WARNING graphwake: GRAPHWAKE_KEY is not set; nothing sent"},
		{"no spec", map[string]string{"GRAPHWAKE_SPEC": "<unset>"}, []string{"--live"}, "WARNING graphwake: no spec given (--spec or GRAPHWAKE_SPEC); nothing sent"},
		{"missing file", nil, []string{"--live", "--spec", "/nope/openapi.yaml"}, "WARNING graphwake: spec file not found: /nope/openapi.yaml; nothing sent"},
		{"bad state", nil, []string{"--state", "weird"}, "WARNING graphwake: --state must be open, closed or merged; nothing sent"},
		{"unknown flag", nil, []string{"--nope"}, "WARNING graphwake: invalid arguments: unknown flag; nothing sent"},
		{"bad URL", map[string]string{"GRAPHWAKE_URL": "ftp://x"}, []string{"--live"}, "WARNING graphwake: GRAPHWAKE_URL must be an http(s) URL; nothing sent"},
		{"github PR payload unreadable", githubEnv("pull_request", "/nope.json", nil), nil,
			"::warning::graphwake: could not read the pull request event payload, nothing sent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBackend(t)
			if out := b.run(t, run{env: tt.env, args: tt.args}); out != tt.want {
				t.Fatalf("out = %q, want %q", out, tt.want)
			}
			if len(b.sent()) != 0 {
				t.Fatalf("sent %v", b.sent())
			}
		})
	}
}

// A key pasted in the wrong place on the command line never comes back in the output.
func TestArgumentErrorsNeverEchoTheKey(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		args []string
		want string
	}{
		{"positional key", nil, []string{"--live", testKey}, "WARNING graphwake: unexpected argument (flags take the form --name value); nothing sent"},
		{"key as a bool value", nil, []string{"--live=" + testKey}, "WARNING graphwake: invalid arguments: invalid value for --live; nothing sent"},
		{"deprecated env key as a bool value", map[string]string{"GRAPHWAKE_KEY": "<unset>", "DOCSYNC_KEY": testKey},
			[]string{"--live=" + testKey}, "WARNING graphwake: invalid arguments: invalid value for --live; nothing sent"},
		{"--key then a bad flag", map[string]string{"GRAPHWAKE_KEY": "<unset>"}, []string{"--key", testKey, "--live=" + testKey},
			"WARNING graphwake: invalid arguments: invalid value for --live; nothing sent"},
		{"key as an unknown flag", map[string]string{"GRAPHWAKE_KEY": "<unset>"}, []string{"--key=" + testKey, "-" + testKey},
			"WARNING graphwake: invalid arguments: unknown flag; nothing sent"},
		{"key as an unknown flag before --key", map[string]string{"GRAPHWAKE_KEY": "<unset>"}, []string{"-" + testKey, "--key", testKey},
			"WARNING graphwake: invalid arguments: unknown flag; nothing sent"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := newBackend(t)
			if out := b.run(t, run{env: tt.env, args: tt.args}); out != tt.want {
				t.Fatalf("out = %q, want %q", out, tt.want)
			}
		})
	}
}

// A real-length key (dsk_ + 43 characters) pasted as a flag: no part of it is printed.
func TestRealLengthKeyAsUnknownFlag(t *testing.T) {
	key := "dsk_" + strings.Repeat("Ab3_", 10) + "xyz"
	if len(key) != 47 {
		t.Fatalf("key length %d", len(key))
	}
	b := newBackend(t)
	out := b.run(t, run{env: map[string]string{"GRAPHWAKE_KEY": key}, args: []string{"-" + key}})
	if out != "WARNING graphwake: invalid arguments: unknown flag; nothing sent" || strings.Contains(out, key[:12]) {
		t.Fatalf("out = %q", out)
	}
}

func TestUnknownCommandNeverPrinted(t *testing.T) {
	key := "dsk_" + strings.Repeat("Zq9-", 10) + "abc"
	var out bytes.Buffer
	Main([]string{key, "--spec", "x"}, Deps{Out: &out, Getenv: func(k string) string {
		if k == "GRAPHWAKE_KEY" {
			return key
		}
		return ""
	}})
	if len(key) != 47 || out.String() != "WARNING graphwake: unknown command; use \"graphwake upload\" or \"graphwake version\"\n" ||
		strings.Contains(out.String(), key[:8]) {
		t.Fatalf("out = %q", out.String())
	}
}

func TestLiveSendSkippedWhenHeadIsNotTheBuildCommit(t *testing.T) {
	b := newBackend(t)
	env := map[string]string{"GITLAB_CI": "true", "CI_COMMIT_BRANCH": "main", "CI_DEFAULT_BRANCH": "main", "CI_COMMIT_SHA": mainSHA}
	out := b.run(t, run{env: env, git: fakeGit{"rev-parse HEAD": otherSHA}})
	if out != "graphwake: the checked-out commit is not the commit this build is for, live spec not sent" || len(b.sent()) != 0 {
		t.Fatalf("out = %q, sent %d", out, len(b.sent()))
	}
	// Same commit: sent.
	out = b.run(t, run{env: env, git: fakeGit{"rev-parse HEAD": mainSHA}})
	if out != "graphwake: spec sent (live)" {
		t.Fatalf("out = %q", out)
	}
	// A pull request build checks out a merge commit: not a live send, no check.
	out = b.run(t, run{env: map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "3", "CI_COMMIT_SHA": mainSHA},
		git: fakeGit{"rev-parse HEAD": otherSHA}})
	if out != "graphwake: spec sent (pull request #3)" {
		t.Fatalf("out = %q", out)
	}
	// An explicit --pr N --state merged is not a branch build: no HEAD check either.
	out = b.run(t, run{env: env, args: []string{"--pr", "4", "--state", "merged"}, git: fakeGit{"rev-parse HEAD": otherSHA}})
	if out != "graphwake: spec sent (pull request #4 merged, live)" {
		t.Fatalf("out = %q", out)
	}
}

func TestKeyFlagAndDeprecatedEnv(t *testing.T) {
	b := newBackend(t)
	b.run(t, run{env: map[string]string{"GRAPHWAKE_KEY": "<unset>", "DOCSYNC_KEY": "dsk_old"}, args: []string{"--live"}})
	b.run(t, run{env: map[string]string{"GRAPHWAKE_KEY": "<unset>"}, args: []string{"--live", "--key", testKey}})
	if len(b.auth) != 2 || b.auth[0] != "Bearer dsk_old" || b.auth[1] != "Bearer "+testKey {
		t.Fatalf("auth %v", b.auth)
	}
}

func TestSpecFromURL(t *testing.T) {
	b := newBackend(t)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "openapi: 3.1.0") }))
	defer app.Close()
	b.run(t, run{args: []string{"--live", "--spec", app.URL + "/swagger/v1/swagger.json"}})
	if b.sent()[0]["spec"] != "openapi: 3.1.0" {
		t.Fatalf("sent %v", b.sent())
	}
}

func TestTooLargeSpecIsNotSent(t *testing.T) {
	b := newBackend(t)
	path := filepath.Join(t.TempDir(), "big.json")
	os.WriteFile(path, bytes.Repeat([]byte("\""), ingest.MaxBodyBytes/2+10), 0o600) // each quote doubles when escaped
	out := b.run(t, run{args: []string{"--live", "--spec", path}})
	if out != "WARNING graphwake: spec too large (limit 10 MB), not sent" || len(b.sent()) != 0 {
		t.Fatalf("out = %q, sent %d", out, len(b.sent()))
	}
}

func TestVersionAndUsage(t *testing.T) {
	var out bytes.Buffer
	Main([]string{"version"}, Deps{Version: "1.2.3", Out: &out})
	if out.String() != "graphwake 1.2.3\n" {
		t.Fatalf("version = %q", out.String())
	}
	out.Reset()
	Main(nil, Deps{Out: &out})
	Main([]string{"bogus"}, Deps{Out: &out})
	if strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("out = %q", out.String())
	}
}

func assertContext(t *testing.T, got, want map[string]any) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("context[%q] = %v, want %v (context %v)", k, got[k], v, got)
		}
	}
}

// The production wiring keeps the whole ingest send within the design's 30 s.
func TestDefaultIngestBudget(t *testing.T) {
	d := DefaultDeps("x", io.Discard)
	waits := d.IngestBackoff + 2*d.IngestBackoff
	worst := time.Duration(d.IngestAttempts)*d.IngestAttempt + waits
	if d.IngestDeadline != 30*time.Second || worst > 30*time.Second || d.IngestAttempts != 3 {
		t.Fatalf("deadline %v, worst case %v, attempts %d", d.IngestDeadline, worst, d.IngestAttempts)
	}
}
