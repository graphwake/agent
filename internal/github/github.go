// Package github talks to the GitHub REST API on GitHub Actions: where a branch points, which pull request a
// commit was merged from, a pull request's labels, and the Graphwake comment on a pull request. All best effort.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// API talks to one repository ("owner/name") with the job's GITHUB_TOKEN.
type API struct {
	BaseURL string // e.g. https://api.github.com
	Repo    string
	Token   string
	HTTP    *http.Client
}

// Usable reports whether the API can be asked at all.
func (a API) Usable() bool { return a.BaseURL != "" && a.Repo != "" && a.Token != "" && a.HTTP != nil }

func (a API) get(ctx context.Context, path, accept string) ([]byte, error) {
	body, status, err := a.do(ctx, http.MethodGet, path, accept, nil)
	if err == nil && status != http.StatusOK {
		err = fmt.Errorf("GitHub API answered %d", status)
	}
	if err != nil {
		return nil, err
	}
	return body, nil
}

// send makes a JSON API call; body (when not nil) is sent as JSON. Any non-2xx answer is an error.
func (a API) send(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	return a.do(ctx, method, path, "application/vnd.github+json", body)
}

// maxResponseBytes caps how much of an answer is read: a page of 100 long bot comments can pass 1 MiB.
const maxResponseBytes = 8 << 20

// do makes one authenticated call and reads up to maxResponseBytes of the answer. Errors never carry the token.
func (a API) do(ctx context.Context, method, path, accept string, payload any) ([]byte, int, error) {
	var reqBody io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, 0, err
		}
		reqBody = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(a.BaseURL, "/")+path, reqBody)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("GitHub API answered %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

// BranchHead returns the SHA the branch currently points at.
func (a API) BranchHead(ctx context.Context, branch string) (string, error) {
	body, err := a.get(ctx, "/repos/"+a.Repo+"/commits/"+url.PathEscape(branch), "application/vnd.github.sha")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(body)), nil
}

// PullRequest is a merged pull request found for a commit.
type PullRequest struct {
	Number int
	URL    string
	Author string
}

// MergedPullRequest returns the pull request sha was merged from into base (nil when the commit came from no
// merged pull request into base). It also covers squash and rebase merges.
func (a API) MergedPullRequest(ctx context.Context, sha, base string) (*PullRequest, error) {
	body, err := a.get(ctx, "/repos/"+a.Repo+"/commits/"+url.PathEscape(sha)+"/pulls", "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	var pulls []struct {
		Number   int     `json:"number"`
		HTMLURL  string  `json:"html_url"`
		MergedAt *string `json:"merged_at"`
		User     struct {
			Login string `json:"login"`
		} `json:"user"`
		Base struct {
			Ref string `json:"ref"`
		} `json:"base"`
	}
	if err := json.Unmarshal(body, &pulls); err != nil {
		return nil, err
	}
	for _, p := range pulls {
		if p.MergedAt != nil && *p.MergedAt != "" && p.Base.Ref == base {
			return &PullRequest{Number: p.Number, URL: p.HTMLURL, Author: p.User.Login}, nil
		}
	}
	return nil, nil
}

// Marker is the hidden first line that identifies one service's Graphwake comment on a pull request. key is the
// backend's comment key ("graphwake:service:<serviceId>").
func Marker(key string) string { return "<!-- " + key + " -->" }

// Labels returns the names of the pull request's labels (the first 100).
func (a API) Labels(ctx context.Context, pr string) ([]string, error) {
	body, _, err := a.send(ctx, http.MethodGet, "/repos/"+a.Repo+"/issues/"+url.PathEscape(pr)+"/labels?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &labels); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names, nil
}

const (
	commentsPerPage = 100
	maxCommentPages = 10
)

// FindComment returns the id of the first pull request comment whose body starts with marker (leading whitespace
// aside), so a comment that quotes the marker is never taken for ours. It reads at most maxCommentPages pages.
func (a API) FindComment(ctx context.Context, pr, marker string) (int64, bool, error) {
	for page := 1; page <= maxCommentPages; page++ {
		path := fmt.Sprintf("/repos/%s/issues/%s/comments?per_page=%d&page=%d", a.Repo, url.PathEscape(pr), commentsPerPage, page)
		body, _, err := a.send(ctx, http.MethodGet, path, nil)
		if err != nil {
			return 0, false, err
		}
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if err := json.Unmarshal(body, &comments); err != nil {
			return 0, false, err
		}
		for _, c := range comments {
			if strings.HasPrefix(strings.TrimLeft(c.Body, " \t\r\n"), marker) {
				return c.ID, true, nil
			}
		}
		if len(comments) < commentsPerPage {
			break
		}
	}
	return 0, false, nil
}

// CreateComment adds a comment to the pull request.
func (a API) CreateComment(ctx context.Context, pr, body string) error {
	_, _, err := a.send(ctx, http.MethodPost, "/repos/"+a.Repo+"/issues/"+url.PathEscape(pr)+"/comments", map[string]string{"body": body})
	return err
}

// UpdateComment replaces the body of an existing comment.
func (a API) UpdateComment(ctx context.Context, id int64, body string) error {
	_, _, err := a.send(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/comments/%d", a.Repo, id), map[string]string{"body": body})
	return err
}
