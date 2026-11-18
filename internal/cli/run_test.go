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
	"strconv"
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
	adoToken = "ado_TOPSECRET_token"
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

// backend is a fake Graphwake ingest endpoint plus fake GitHub and Azure DevOps APIs.
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

	ghLabels         string        // labels API answer; "" → []
	ghLabelsDelay    time.Duration // the labels API answers this late
	ghCommentsStatus int           // non-zero: every comments call answers this status
	ghComments       []ghComment
	ghWrites         []string // "POST" or "PATCH <id>"

	adoCalls      int
	adoVisibility string
	adoLabels     []string
	adoThreads    []adoThread
	adoWrites     []string // "POST thread", "PATCH comment <thread>", "PATCH thread <thread> <status>"
}

type ghComment struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

type adoThread struct {
	ID, CommentID int
	Key, Content  string
	Status        string
}

const adoPR = "/ado/org/Shop/_apis/git/repositories/repo-1/pullRequests/42/"

func newBackend(t *testing.T) *backend {
	b := &backend{t: t, status: 200, body: `{"outcome":"Changed","changes":[]}`}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		delay := b.ghLabelsDelay
		b.mu.Unlock()
		if delay > 0 && strings.HasSuffix(r.URL.Path, "/labels") {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				return
			}
		}
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
			if b.serveGitHubPR(w, r) {
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
		case strings.HasPrefix(r.URL.Path, "/ado/"):
			b.adoCalls++
			if r.Header.Get("Authorization") != "Bearer "+adoToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			b.serveAzure(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(b.srv.Close)
	return b
}

// serveGitHubPR answers the pull request labels and comments routes of the fake GitHub (false: not one of them).
func (b *backend) serveGitHubPR(w http.ResponseWriter, r *http.Request) bool {
	const issue = "/gh/repos/acme/orders/issues/"
	path := r.URL.Path
	switch {
	case path == issue+"42/labels" && r.Method == http.MethodGet:
		io.WriteString(w, orDefault(b.ghLabels, "[]"))
	case strings.HasPrefix(path, issue+"comments/") || path == issue+"42/comments":
		if b.ghCommentsStatus != 0 {
			w.WriteHeader(b.ghCommentsStatus)
			return true
		}
		var in struct {
			Body string `json:"body"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		switch {
		case r.Method == http.MethodGet:
			page := []ghComment{}
			if r.URL.Query().Get("page") == "1" {
				page = append(page, b.ghComments...)
			}
			json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodPost:
			id := int64(len(b.ghComments) + 100)
			b.ghComments = append(b.ghComments, ghComment{ID: id, Body: in.Body})
			b.ghWrites = append(b.ghWrites, "POST")
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, "{}")
		case r.Method == http.MethodPatch:
			id, _ := strconv.ParseInt(strings.TrimPrefix(path, issue+"comments/"), 10, 64)
			for i := range b.ghComments {
				if b.ghComments[i].ID == id {
					b.ghComments[i].Body = in.Body
				}
			}
			b.ghWrites = append(b.ghWrites, "PATCH "+strconv.FormatInt(id, 10))
			io.WriteString(w, "{}")
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	default:
		return false
	}
	return true
}

// serveAzure is the fake Azure DevOps REST API for project Shop, repository repo-1, pull request 42.
func (b *backend) serveAzure(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path == "/ado/org/_apis/projects/Shop" {
		json.NewEncoder(w).Encode(map[string]string{"visibility": orDefault(b.adoVisibility, "Private")})
		return
	}
	rest, ok := strings.CutPrefix(path, adoPR)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	parts := strings.Split(rest, "/")
	var in struct {
		Content  string `json:"content"`
		Status   string `json:"status"`
		Comments []struct {
			Content string `json:"content"`
		} `json:"comments"`
		Properties map[string]struct {
			Value string `json:"$value"`
		} `json:"properties"`
	}
	json.NewDecoder(r.Body).Decode(&in)
	switch {
	case rest == "labels" && r.Method == http.MethodGet:
		value := []map[string]string{}
		for _, l := range b.adoLabels {
			value = append(value, map[string]string{"name": l})
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(value), "value": value})
	case rest == "threads" && r.Method == http.MethodGet:
		value := []map[string]any{}
		for _, th := range b.adoThreads {
			value = append(value, map[string]any{"id": th.ID, "status": th.Status,
				"properties": map[string]any{"Graphwake.Key": map[string]string{"$type": "System.String", "$value": th.Key}},
				"comments":   []map[string]any{{"id": th.CommentID, "content": th.Content}}})
		}
		json.NewEncoder(w).Encode(map[string]any{"count": len(value), "value": value})
	case rest == "threads" && r.Method == http.MethodPost:
		th := adoThread{ID: len(b.adoThreads) + 1, CommentID: 1, Key: in.Properties["Graphwake.Key"].Value, Status: in.Status}
		if len(in.Comments) > 0 {
			th.Content = in.Comments[0].Content
		}
		b.adoThreads = append(b.adoThreads, th)
		b.adoWrites = append(b.adoWrites, "POST thread")
		io.WriteString(w, "{}")
	case len(parts) >= 2 && parts[0] == "threads" && r.Method == http.MethodPatch:
		id, _ := strconv.Atoi(parts[1])
		for i := range b.adoThreads {
			if b.adoThreads[i].ID != id {
				continue
			}
			if len(parts) == 4 && parts[2] == "comments" {
				b.adoThreads[i].Content = in.Content
				b.adoWrites = append(b.adoWrites, "PATCH comment "+parts[1])
			} else {
				b.adoThreads[i].Status = in.Status
				b.adoWrites = append(b.adoWrites, "PATCH thread "+parts[1]+" "+in.Status)
			}
		}
		io.WriteString(w, "{}")
	default:
		w.WriteHeader(http.StatusNotFound)
	}
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
	// platformDeadline replaces the budget for platform API calls (0: 2 s).
	platformDeadline time.Duration
}

// run is exec for a run that must not fail the build.
func (b *backend) run(t *testing.T, r run) string {
	t.Helper()
	out, code := b.exec(t, r)
	if code != 0 {
		t.Errorf("exit code %d, want 0 (out %q)", code, out)
	}
	return out
}

// exec runs "graphwake upload" against the fakes and returns the one output line and the exit code.
func (b *backend) exec(t *testing.T, r run) (string, int) {
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

		PlatformDeadline: 2 * time.Second,
	}
	if r.platformDeadline != 0 {
		d.PlatformDeadline = r.platformDeadline
	}
	if d.Git == nil {
		d.Git = fakeGit{}
	}
	code := Main(append([]string{"upload"}, r.args...), d)
	s := out.String()
	if strings.Count(s, "\n") != 1 {
		t.Errorf("want exactly one output line, got %q", s)
	}
	if strings.Contains(s, testKey) || strings.Contains(s, ghToken) || strings.Contains(s, adoToken) {
		t.Errorf("secret printed: %q", s)
	}
	return strings.TrimSuffix(s, "\n"), code
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
	if d.PlatformDeadline != 10*time.Second {
		t.Fatalf("platform deadline %v", d.PlatformDeadline)
	}
}

// --- Pull request comment and --fail-on gate ---

const (
	serviceKey   = "graphwake:service:11111111-1111-1111-1111-111111111111"
	failedAffect = "check failed: breaking changes affect 2 subscribed teams"
)

// prOutcome is an ingest answer carrying a pull request outcome; action "" means no comment.
func prOutcome(action, commentBody, gate, message string) string {
	pr := map[string]any{
		"changeSetId": "cs-1",
		"impactUrl":   "https://app.graphwake.com/impact/cs-1",
		"comment":     nil,
		"gate":        map[string]any{"result": gate, "message": nil},
	}
	if action != "" {
		pr["comment"] = map[string]string{"action": action, "key": serviceKey,
			"body": "<!-- " + serviceKey + " -->\n" + commentBody}
	}
	if message != "" {
		pr["gate"] = map[string]any{"result": gate, "message": message}
	}
	data, _ := json.Marshal(map[string]any{"outcome": "Changed", "changes": []any{}, "pullRequest": pr})
	return string(data)
}

func privatePR(t *testing.T) string {
	return writePayload(t, `{"action":"synchronize","pull_request":{"number":42,"html_url":"https://github.com/acme/orders/pull/42",
	  "user":{"login":"octocat"},"head":{"sha":"`+otherSHA+`"}},"repository":{"default_branch":"main","private":true,"visibility":"private"}}`)
}

func hintsOf(t *testing.T, req map[string]any) map[string]any {
	t.Helper()
	h, ok := req["pullRequest"].(map[string]any)
	if !ok {
		t.Fatalf("no pullRequest hints in %v", req)
	}
	return h
}

func azureEnv(extra map[string]string) map[string]string {
	m := map[string]string{
		"TF_BUILD":                         "True",
		"BUILD_REASON":                     "PullRequest",
		"SYSTEM_PULLREQUEST_PULLREQUESTID": "42",
		"BUILD_REPOSITORY_PROVIDER":        "TfsGit",
		"BUILD_REPOSITORY_URI":             "https://dev.azure.com/org/Shop/_git/orders",
		"SYSTEM_COLLECTIONURI":             "{srv}/ado/org/",
		"SYSTEM_TEAMPROJECT":               "Shop",
		"BUILD_REPOSITORY_ID":              "repo-1",
		"SYSTEM_ACCESSTOKEN":               adoToken,
		"BUILD_SOURCEVERSION":              mainSHA,
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestGitHubCommentCreatedThenUpdated(t *testing.T) {
	b := newBackend(t)
	// Another service of the same repository already commented: its comment is never touched.
	other := ghComment{ID: 5, Body: "<!-- graphwake:service:other -->\nother service"}
	b.ghComments = []ghComment{other}
	b.body = prOutcome("upsert", "first", "off", "")
	env := githubEnv("pull_request", privatePR(t), nil)

	out := b.run(t, run{env: env})
	if out != "graphwake: spec sent (pull request #42)" {
		t.Fatalf("out = %q", out)
	}
	h := hintsOf(t, b.sent()[0])
	if h["commentFormat"] != "github" || h["repoVisibility"] != "private" || h["acceptLabel"] != false {
		t.Fatalf("hints %v", h)
	}
	if _, has := h["failOn"]; has {
		t.Fatalf("failOn sent without --fail-on: %v", h)
	}

	b.body = prOutcome("upsert", "second", "off", "")
	b.run(t, run{env: env})
	if len(b.ghWrites) != 2 || b.ghWrites[0] != "POST" || b.ghWrites[1] != "PATCH 101" {
		t.Fatalf("writes %v", b.ghWrites)
	}
	if b.ghComments[0] != other || !strings.HasSuffix(b.ghComments[1].Body, "\nsecond") || len(b.ghComments) != 2 {
		t.Fatalf("comments %v", b.ghComments)
	}
}

func TestGitHubResolveWithoutCommentDoesNothing(t *testing.T) {
	b := newBackend(t)
	b.body = prOutcome("resolve", "No breaking changes anymore.", "off", "")
	env := githubEnv("pull_request", privatePR(t), nil)
	if out := b.run(t, run{env: env}); out != "graphwake: spec sent (pull request #42)" || len(b.ghWrites) != 0 {
		t.Fatalf("out = %q, writes %v", out, b.ghWrites)
	}
	// With an earlier comment, resolve updates it.
	b.ghComments = []ghComment{{ID: 9, Body: "<!-- " + serviceKey + " -->\nold"}}
	b.run(t, run{env: env})
	if len(b.ghWrites) != 1 || b.ghWrites[0] != "PATCH 9" || !strings.Contains(b.ghComments[0].Body, "No breaking changes anymore.") {
		t.Fatalf("writes %v, comments %v", b.ghWrites, b.ghComments)
	}
}

func TestGitHubGateFailedExitsOne(t *testing.T) {
	b := newBackend(t)
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	out, code := b.exec(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "Affected"}})
	if code != 1 {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if out != "::warning::graphwake: spec sent (pull request #42); "+failedAffect {
		t.Fatalf("out = %q", out)
	}
	if h := hintsOf(t, b.sent()[0]); h["failOn"] != "affected" || h["acceptLabel"] != false || h["commentFormat"] != "github" {
		t.Fatalf("hints %v", h)
	}
	if len(b.ghWrites) != 1 || b.ghWrites[0] != "POST" {
		t.Fatalf("the comment must be written before failing: %v", b.ghWrites)
	}
}

// A re-run reuses the old event payload (no labels in it): the label is read from the API at run time.
func TestGitHubGateAcceptedByLabelAddedLater(t *testing.T) {
	for _, label := range []string{"graphwake-accept-breaking", "Graphwake-Accept-Breaking"} {
		b := newBackend(t)
		b.ghLabels = `[{"name":"bug"},{"name":"` + label + `"}]`
		b.body = prOutcome("upsert", "accepted", "accepted", "breaking changes accepted with the label graphwake-accept-breaking")
		out := b.run(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "breaking"}})
		if out != "graphwake: spec sent (pull request #42); breaking changes accepted with the label graphwake-accept-breaking" {
			t.Fatalf("out = %q", out)
		}
		if h := hintsOf(t, b.sent()[0]); h["acceptLabel"] != true || h["failOn"] != "breaking" {
			t.Fatalf("hints %v", h)
		}
	}
	// A label that only looks like it does not count.
	b := newBackend(t)
	b.ghLabels = `[{"name":"graphwake-accept-breaking-later"}]`
	b.body = prOutcome("", "", "passed", "check passed: no breaking changes")
	b.run(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "breaking"}})
	if h := hintsOf(t, b.sent()[0]); h["acceptLabel"] != false {
		t.Fatalf("hints %v", h)
	}
}

// A fork pull request or a missing permission: no comment, but the gate still decides.
func TestCommentForbiddenStillAppliesGate(t *testing.T) {
	b := newBackend(t)
	b.ghCommentsStatus = http.StatusForbidden
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	out, code := b.exec(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "affected"}})
	want := "::warning::graphwake: spec sent (pull request #42); " + failedAffect + "; could not comment on the pull request (HTTP 403)"
	if code != 1 || out != want {
		t.Fatalf("exit %d, out = %q", code, out)
	}
}

func TestPlatformDeadlineBoundsSlowApi(t *testing.T) {
	b := newBackend(t)
	b.ghLabelsDelay = 5 * time.Second
	start := time.Now()
	out, code := b.exec(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "affected"},
		platformDeadline: 50 * time.Millisecond})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("run took %v", elapsed)
	}
	want := "::warning::graphwake: spec sent (pull request #42); could not read the pull request labels (timed out); " +
		"the server did not return a check result; not failing"
	if code != 0 || out != want {
		t.Fatalf("exit %d, out = %q", code, out)
	}
	if h := hintsOf(t, b.sent()[0]); h["acceptLabel"] != false || h["failOn"] != "affected" {
		t.Fatalf("hints %v", h)
	}
}

func TestServerDownWithFailOnExitsZero(t *testing.T) {
	b := newBackend(t)
	b.status, b.body = http.StatusServiceUnavailable, ""
	out := b.run(t, run{env: githubEnv("pull_request", privatePR(t), nil), args: []string{"--fail-on", "breaking"}})
	if out != "::warning::graphwake: Graphwake unavailable, spec not sent" || len(b.sent()) != 3 {
		t.Fatalf("out = %q, sent %d", out, len(b.sent()))
	}
}

func TestAzureCommentThreadAndGate(t *testing.T) {
	b := newBackend(t)
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	out, code := b.exec(t, run{env: azureEnv(nil), args: []string{"--fail-on", "affected"}})
	if code != 1 || out != "##vso[task.logissue type=warning]graphwake: spec sent (pull request #42); "+failedAffect {
		t.Fatalf("exit %d, out = %q", code, out)
	}
	h := hintsOf(t, b.sent()[0])
	if h["commentFormat"] != "azure-devops" || h["repoVisibility"] != "private" || h["failOn"] != "affected" || h["acceptLabel"] != false {
		t.Fatalf("hints %v", h)
	}
	if len(b.adoThreads) != 1 || b.adoThreads[0].Key != serviceKey || b.adoThreads[0].Status != "active" ||
		!strings.HasSuffix(b.adoThreads[0].Content, "\nbreaking") {
		t.Fatalf("threads %v", b.adoThreads)
	}

	// The label is added and the breaking change removed: the thread is updated and resolved.
	b.adoLabels = []string{"graphwake-accept-breaking"}
	b.body = prOutcome("resolve", "No breaking changes anymore.", "passed", "check passed: no breaking changes")
	out = b.run(t, run{env: azureEnv(nil), args: []string{"--fail-on", "affected"}})
	if out != "graphwake: spec sent (pull request #42); check passed: no breaking changes" {
		t.Fatalf("out = %q", out)
	}
	if h := hintsOf(t, b.sent()[1]); h["acceptLabel"] != true {
		t.Fatalf("hints %v", h)
	}
	if len(b.adoThreads) != 1 || b.adoThreads[0].Status != "fixed" || !strings.Contains(b.adoThreads[0].Content, "No breaking changes anymore.") {
		t.Fatalf("threads %v, writes %v", b.adoThreads, b.adoWrites)
	}
}

// Azure Pipelines building a GitHub repository: the Azure Repos APIs do not apply, so neither does the gate.
func TestAzureWithGitHubRepoIsUnsupported(t *testing.T) {
	b := newBackend(t)
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	env := azureEnv(map[string]string{"BUILD_REPOSITORY_PROVIDER": "GitHub", "SYSTEM_PULLREQUEST_PULLREQUESTNUMBER": "42",
		"BUILD_REPOSITORY_URI": "https://github.com/acme/orders.git"})
	out := b.run(t, run{env: env, args: []string{"--fail-on", "breaking"}})
	if out != "##vso[task.logissue type=warning]graphwake: spec sent (pull request #42); --fail-on is not supported on this CI yet; not failing" {
		t.Fatalf("out = %q", out)
	}
	if _, has := b.sent()[0]["pullRequest"]; has || b.adoCalls != 0 {
		t.Fatalf("hints sent %v, azure calls %d", b.sent()[0], b.adoCalls)
	}
}

// The ingest answer here carries a failed gate the agent never asked for: it must not fail the build.
func TestGitLabFailOnNotSupported(t *testing.T) {
	b := newBackend(t)
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	out := b.run(t, run{env: map[string]string{"GITLAB_CI": "true", "CI_MERGE_REQUEST_IID": "3"}, args: []string{"--fail-on", "affected"}})
	if out != "WARNING graphwake: spec sent (pull request #3); --fail-on is not supported on this CI yet; not failing" {
		t.Fatalf("out = %q", out)
	}
	if _, has := b.sent()[0]["pullRequest"]; has {
		t.Fatalf("hints sent: %v", b.sent()[0])
	}
}

// Without GITHUB_TOKEN the labels cannot be read, so --fail-on is not applied (no failing without the escape hatch).
func TestGitHubWithoutTokenFailOnNotSupported(t *testing.T) {
	b := newBackend(t)
	out := b.run(t, run{env: githubEnv("pull_request", privatePR(t), map[string]string{"GITHUB_TOKEN": "<unset>"}),
		args: []string{"--fail-on", "affected"}})
	if out != "::warning::graphwake: spec sent (pull request #42); --fail-on is not supported on this CI yet; not failing" {
		t.Fatalf("out = %q", out)
	}
	if _, has := b.sent()[0]["pullRequest"]; has || b.ghCalls != 0 {
		t.Fatalf("hints %v, gh calls %d", b.sent()[0], b.ghCalls)
	}
}

func TestNoCommentFlagSendsNoCommentFormat(t *testing.T) {
	for _, r := range []run{
		{args: []string{"--no-comment", "--fail-on", "breaking"}},
		{env: map[string]string{"GRAPHWAKE_COMMENT": "false", "GRAPHWAKE_FAIL_ON": "breaking"}},
	} {
		b := newBackend(t)
		// Even if the server sends a comment, none is written.
		b.body = prOutcome("upsert", "breaking", "passed", "check passed: no subscribed team is affected")
		r.env = githubEnv("pull_request", privatePR(t), r.env)
		out := b.run(t, r)
		if out != "graphwake: spec sent (pull request #42); check passed: no subscribed team is affected" {
			t.Fatalf("out = %q", out)
		}
		h := hintsOf(t, b.sent()[0])
		if _, has := h["commentFormat"]; has || h["failOn"] != "breaking" {
			t.Fatalf("hints %v", h)
		}
		if len(b.ghWrites) != 0 {
			t.Fatalf("writes %v", b.ghWrites)
		}
	}
}

func TestDefaultBranchIgnoresFailOn(t *testing.T) {
	b := newBackend(t)
	b.ghTip, b.ghPulls = mainSHA, `[]`
	b.body = prOutcome("upsert", "breaking", "failed", failedAffect)
	push := writePayload(t, `{"head_commit":{"message":"Fix"},"repository":{"default_branch":"main","private":true}}`)
	out := b.run(t, run{env: githubEnv("push", push, map[string]string{"GITHUB_REF": "refs/heads/main"}), args: []string{"--fail-on", "breaking"}})
	if out != "graphwake: spec sent (live)" {
		t.Fatalf("out = %q", out)
	}
	// A closed pull request is not gated either.
	out = b.run(t, run{env: githubEnv("pull_request", prEvent(t, "closed", "false"), nil), args: []string{"--fail-on", "breaking"}})
	if out != "graphwake: pull request #42 closed, sent" {
		t.Fatalf("out = %q", out)
	}
	for _, req := range b.sent() {
		if _, has := req["pullRequest"]; has {
			t.Fatalf("hints sent: %v", req)
		}
	}
	if len(b.ghWrites) != 0 {
		t.Fatalf("writes %v", b.ghWrites)
	}
}

// The Azure build token never reaches the output, even when the server echoes it.
func TestAzureTokenNeverPrinted(t *testing.T) {
	b := newBackend(t)
	b.status, b.body = http.StatusUnprocessableEntity, `{"error":"bad spec `+adoToken+`"}`
	out := b.run(t, run{env: azureEnv(nil)})
	if out != "##vso[task.logissue type=warning]graphwake: spec rejected: bad spec ***" {
		t.Fatalf("out = %q", out)
	}
}
