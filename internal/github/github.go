// Package github asks the GitHub REST API the two questions the agent needs on GitHub Actions: where a branch
// points, and which pull request a commit was merged from. Both are best effort.
package github

import (
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(a.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("Authorization", "Bearer "+a.Token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API answered %d", resp.StatusCode)
	}
	return body, nil
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
