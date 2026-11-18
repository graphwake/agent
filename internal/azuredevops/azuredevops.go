// Package azuredevops asks the Azure DevOps REST API (7.1) what the agent needs on Azure Pipelines with Azure
// Repos: the project's visibility, a pull request's labels, and the Graphwake comment thread on a pull request
// (find, create, update). All calls use the job's SYSTEM_ACCESSTOKEN.
package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// keyProperty is the thread property that carries the Graphwake comment key.
const keyProperty = "Graphwake.Key"

// API talks to one Azure Repos repository in one project.
type API struct {
	CollectionURI string // SYSTEM_COLLECTIONURI, e.g. https://dev.azure.com/org/ (trailing slash optional)
	Project       string // SYSTEM_TEAMPROJECT, a name (may contain spaces) or id
	RepoID        string // BUILD_REPOSITORY_ID
	Token         string // SYSTEM_ACCESSTOKEN
	HTTP          *http.Client
}

// Usable reports whether the API can be asked at all.
func (a API) Usable() bool {
	return a.CollectionURI != "" && a.Project != "" && a.RepoID != "" && a.Token != "" && a.HTTP != nil
}

func (a API) collection() string { return strings.TrimRight(a.CollectionURI, "/") + "/" }

func (a API) pullRequest(pr string) string {
	return a.collection() + url.PathEscape(a.Project) + "/_apis/git/repositories/" + url.PathEscape(a.RepoID) +
		"/pullRequests/" + url.PathEscape(pr)
}

func (a API) do(ctx context.Context, method, u string, payload any) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u+"?api-version=7.1", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("Azure DevOps API answered %d", resp.StatusCode)
	}
	return out, nil
}

// Visibility returns the project's visibility, lowercased: "private" or "public".
func (a API) Visibility(ctx context.Context) (string, error) {
	body, err := a.do(ctx, http.MethodGet, a.collection()+"_apis/projects/"+url.PathEscape(a.Project), nil)
	if err != nil {
		return "", err
	}
	var p struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return "", err
	}
	return strings.ToLower(p.Visibility), nil
}

// Labels returns the names of the labels on the pull request.
func (a API) Labels(ctx context.Context, pr string) ([]string, error) {
	body, err := a.do(ctx, http.MethodGet, a.pullRequest(pr)+"/labels", nil)
	if err != nil {
		return nil, err
	}
	type label struct {
		Name string `json:"name"`
	}
	var labels []label
	// The API wraps lists as {"count":n,"value":[...]}; the reference shows the bare array, so accept both.
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && trimmed[0] == '[' {
		err = json.Unmarshal(trimmed, &labels)
	} else {
		var wrapped struct {
			Value []label `json:"value"`
		}
		err = json.Unmarshal(body, &wrapped)
		labels = wrapped.Value
	}
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(labels))
	for _, l := range labels {
		names = append(names, l.Name)
	}
	return names, nil
}

// FindThread returns the pull request thread that carries key (and the id of its first comment). A thread
// matches on its Graphwake.Key property; failing that, on its first comment containing the "<!-- key -->"
// marker the comment body starts with. Deleted threads and comments are ignored.
func (a API) FindThread(ctx context.Context, pr, key string) (threadID, commentID int, found bool, err error) {
	body, err := a.do(ctx, http.MethodGet, a.pullRequest(pr)+"/threads", nil)
	if err != nil {
		return 0, 0, false, err
	}
	var threads struct {
		Value []struct {
			ID         int  `json:"id"`
			IsDeleted  bool `json:"isDeleted"`
			Properties map[string]struct {
				Value any `json:"$value"`
			} `json:"properties"`
			Comments []struct {
				ID        int    `json:"id"`
				Content   string `json:"content"`
				IsDeleted bool   `json:"isDeleted"`
			} `json:"comments"`
		} `json:"value"`
	}
	if err := json.Unmarshal(body, &threads); err != nil {
		return 0, 0, false, err
	}
	marker := "<!-- " + key + " -->"
	fallbackThread, fallbackComment := 0, 0
	for _, t := range threads.Value {
		if t.IsDeleted || len(t.Comments) == 0 || t.Comments[0].IsDeleted {
			continue
		}
		first := t.Comments[0]
		if v, ok := t.Properties[keyProperty].Value.(string); ok && v == key {
			return t.ID, first.ID, true, nil
		}
		if fallbackThread == 0 && strings.Contains(first.Content, marker) {
			fallbackThread, fallbackComment = t.ID, first.ID
		}
	}
	if fallbackThread != 0 {
		return fallbackThread, fallbackComment, true, nil
	}
	return 0, 0, false, nil
}

// CreateThread opens an active pull request thread with body as its only comment, tagged with key.
func (a API) CreateThread(ctx context.Context, pr, key, body string) error {
	type comment struct {
		ParentCommentID int    `json:"parentCommentId"`
		Content         string `json:"content"`
		CommentType     int    `json:"commentType"` // 1 = text
	}
	type property struct {
		Type  string `json:"$type"`
		Value string `json:"$value"`
	}
	payload := struct {
		Comments   []comment           `json:"comments"`
		Status     string              `json:"status"`
		Properties map[string]property `json:"properties"`
	}{
		Comments:   []comment{{ParentCommentID: 0, Content: body, CommentType: 1}},
		Status:     "active",
		Properties: map[string]property{keyProperty: {Type: "System.String", Value: key}},
	}
	_, err := a.do(ctx, http.MethodPost, a.pullRequest(pr)+"/threads", payload)
	return err
}

// UpdateThread replaces the comment's content, then sets the thread's status ("active" to keep it open,
// "fixed" to resolve it).
func (a API) UpdateThread(ctx context.Context, pr string, threadID, commentID int, body, status string) error {
	thread := a.pullRequest(pr) + "/threads/" + strconv.Itoa(threadID)
	if _, err := a.do(ctx, http.MethodPatch, thread+"/comments/"+strconv.Itoa(commentID),
		struct {
			Content string `json:"content"`
		}{body}); err != nil {
		return err
	}
	_, err := a.do(ctx, http.MethodPatch, thread, struct {
		Status string `json:"status"`
	}{status})
	return err
}
