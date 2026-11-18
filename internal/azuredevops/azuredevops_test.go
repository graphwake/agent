package azuredevops

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recorded struct {
	Method, Path, Query, Auth, Body string
}

// server answers each "METHOD escaped-path" with the given status and body, and records every request.
func server(t *testing.T, routes map[string]struct {
	status int
	body   string
}) (*httptest.Server, *[]recorded) {
	t.Helper()
	var mu sync.Mutex
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recorded{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get("Authorization"), string(b)})
		mu.Unlock()
		route, ok := routes[r.Method+" "+r.URL.EscapedPath()]
		if !ok {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.EscapedPath())
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(route.status)
		_, _ = io.WriteString(w, route.body)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func api(srv *httptest.Server, collectionSuffix string) API {
	return API{
		CollectionURI: srv.URL + "/org" + collectionSuffix,
		Project:       "My Project",
		RepoID:        "11111111-2222-3333-4444-555555555555",
		Token:         "secret-token",
		HTTP:          srv.Client(),
	}
}

const repoPath = "/org/My%20Project/_apis/git/repositories/11111111-2222-3333-4444-555555555555/pullRequests/7"

type route = struct {
	status int
	body   string
}

func TestUsable(t *testing.T) {
	full := API{CollectionURI: "https://dev.azure.com/org/", Project: "p", RepoID: "r", Token: "t", HTTP: http.DefaultClient}
	if !full.Usable() {
		t.Fatal("full API should be usable")
	}
	for name, a := range map[string]API{
		"no collection": {Project: "p", RepoID: "r", Token: "t", HTTP: http.DefaultClient},
		"no project":    {CollectionURI: "c", RepoID: "r", Token: "t", HTTP: http.DefaultClient},
		"no repo":       {CollectionURI: "c", Project: "p", Token: "t", HTTP: http.DefaultClient},
		"no token":      {CollectionURI: "c", Project: "p", RepoID: "r", HTTP: http.DefaultClient},
		"no client":     {CollectionURI: "c", Project: "p", RepoID: "r", Token: "t"},
	} {
		if a.Usable() {
			t.Errorf("%s: should not be usable", name)
		}
	}
}

func TestVisibility(t *testing.T) {
	for _, suffix := range []string{"/", ""} {
		srv, reqs := server(t, map[string]route{
			"GET /org/_apis/projects/My%20Project": {200, `{"id":"x","name":"My Project","visibility":"Public"}`},
		})
		got, err := api(srv, suffix).Visibility(context.Background())
		if err != nil || got != "public" {
			t.Fatalf("suffix %q: Visibility = %q, %v; want public", suffix, got, err)
		}
		r := (*reqs)[0]
		if r.Query != "api-version=7.1" || r.Auth != "Bearer secret-token" {
			t.Errorf("suffix %q: query %q auth %q", suffix, r.Query, r.Auth)
		}
	}
}

func TestLabels(t *testing.T) {
	srv, reqs := server(t, map[string]route{
		"GET " + repoPath + "/labels": {200, `{"count":2,"value":[{"id":"a","name":"graphwake-ack","active":true},{"id":"b","name":"wip","active":true}]}`},
	})
	got, err := api(srv, "/").Labels(context.Background(), "7")
	if err != nil || strings.Join(got, ",") != "graphwake-ack,wip" {
		t.Fatalf("Labels = %v, %v", got, err)
	}
	if (*reqs)[0].Query != "api-version=7.1" {
		t.Errorf("query %q", (*reqs)[0].Query)
	}
}

func TestLabelsBareArray(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET " + repoPath + "/labels": {200, `[{"id":"a","name":"graphwake-ack"}]`},
	})
	got, err := api(srv, "/").Labels(context.Background(), "7")
	if err != nil || strings.Join(got, ",") != "graphwake-ack" {
		t.Fatalf("Labels = %v, %v", got, err)
	}
}

const key = "graphwake:service:svc-1"

func TestFindThreadByProperty(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET " + repoPath + "/threads": {200, `{"count":3,"value":[
			{"id":10,"status":"active","comments":[{"id":1,"parentCommentId":0,"content":"hello"}],"properties":{}},
			{"id":11,"status":"active","comments":[{"id":1,"parentCommentId":0,"content":"other"}],
			 "properties":{"Graphwake.Key":{"$type":"System.String","$value":"graphwake:service:svc-2"}}},
			{"id":12,"status":"fixed","comments":[{"id":3,"parentCommentId":0,"content":"no marker here"}],
			 "properties":{"Graphwake.Key":{"$type":"System.String","$value":"graphwake:service:svc-1"}}}
		]}`},
	})
	tid, cid, found, err := api(srv, "/").FindThread(context.Background(), "7", key)
	if err != nil || !found || tid != 12 || cid != 3 {
		t.Fatalf("FindThread = %d, %d, %v, %v; want 12, 3, true", tid, cid, found, err)
	}
}

func TestFindThreadByMarkerFallback(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET " + repoPath + "/threads": {200, `{"count":2,"value":[
			{"id":20,"status":"active","comments":[{"id":1,"parentCommentId":0,"content":"lgtm"}]},
			{"id":21,"status":"active","comments":[
				{"id":4,"parentCommentId":0,"content":"<!-- graphwake:service:svc-1 -->\n## Graphwake"},
				{"id":5,"parentCommentId":4,"content":"thanks"}]}
		]}`},
	})
	tid, cid, found, err := api(srv, "/").FindThread(context.Background(), "7", key)
	if err != nil || !found || tid != 21 || cid != 4 {
		t.Fatalf("FindThread = %d, %d, %v, %v; want 21, 4, true", tid, cid, found, err)
	}
}

func TestFindThreadIgnoresAnotherServicesComment(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET " + repoPath + "/threads": {200, `{"count":2,"value":[
			{"id":30,"status":"active","comments":[{"id":1,"parentCommentId":0,"content":"<!-- graphwake:service:svc-10 -->\nbody"}]},
			{"id":31,"status":"active","comments":[{"id":1,"parentCommentId":0,"content":"<!-- graphwake:service:svc-2 -->\nbody"}],
			 "properties":{"Graphwake.Key":{"$type":"System.String","$value":"graphwake:service:svc-2"}}}
		]}`},
	})
	_, _, found, err := api(srv, "/").FindThread(context.Background(), "7", key)
	if err != nil || found {
		t.Fatalf("FindThread found=%v err=%v; want not found", found, err)
	}
}

func TestFindThreadSkipsDeleted(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET " + repoPath + "/threads": {200, `{"count":1,"value":[
			{"id":40,"isDeleted":true,"comments":[{"id":1,"parentCommentId":0,"content":"<!-- graphwake:service:svc-1 -->"}],
			 "properties":{"Graphwake.Key":{"$type":"System.String","$value":"graphwake:service:svc-1"}}}
		]}`},
	})
	_, _, found, err := api(srv, "/").FindThread(context.Background(), "7", key)
	if err != nil || found {
		t.Fatalf("FindThread found=%v err=%v; want not found", found, err)
	}
}

func TestCreateThreadBody(t *testing.T) {
	srv, reqs := server(t, map[string]route{
		"POST " + repoPath + "/threads": {200, `{"id":50}`},
	})
	if err := api(srv, "").CreateThread(context.Background(), "7", key, "<!-- graphwake:service:svc-1 -->\nhi"); err != nil {
		t.Fatal(err)
	}
	r := (*reqs)[0]
	if r.Query != "api-version=7.1" || r.Auth != "Bearer secret-token" {
		t.Errorf("query %q auth %q", r.Query, r.Auth)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(r.Body), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"comments": []any{map[string]any{"parentCommentId": float64(0), "content": "<!-- graphwake:service:svc-1 -->\nhi", "commentType": float64(1)}},
		"status":   "active",
		"properties": map[string]any{
			"Graphwake.Key": map[string]any{"$type": "System.String", "$value": key},
		},
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Errorf("body\n got %s\nwant %s", gotJSON, wantJSON)
	}
}

func TestUpdateThread(t *testing.T) {
	for _, status := range []string{"active", "fixed"} {
		srv, reqs := server(t, map[string]route{
			"PATCH " + repoPath + "/threads/21/comments/4": {200, `{"id":4}`},
			"PATCH " + repoPath + "/threads/21":            {200, `{"id":21}`},
		})
		if err := api(srv, "/").UpdateThread(context.Background(), "7", 21, 4, "new body", status); err != nil {
			t.Fatal(err)
		}
		if len(*reqs) != 2 {
			t.Fatalf("%d requests, want 2", len(*reqs))
		}
		c, th := (*reqs)[0], (*reqs)[1]
		if c.Path != repoPath+"/threads/21/comments/4" || th.Path != repoPath+"/threads/21" {
			t.Fatalf("order: %s then %s", c.Path, th.Path)
		}
		if c.Body != `{"content":"new body"}` {
			t.Errorf("comment body %s", c.Body)
		}
		if th.Body != `{"status":"`+status+`"}` {
			t.Errorf("thread body %s", th.Body)
		}
		for _, r := range *reqs {
			if r.Query != "api-version=7.1" || r.Auth != "Bearer secret-token" {
				t.Errorf("query %q auth %q", r.Query, r.Auth)
			}
		}
	}
}

func TestUnauthorizedIsErrorWithoutToken(t *testing.T) {
	srv, _ := server(t, map[string]route{
		"GET /org/_apis/projects/My%20Project":        {401, ``},
		"GET " + repoPath + "/labels":                 {401, ``},
		"GET " + repoPath + "/threads":                {401, ``},
		"POST " + repoPath + "/threads":               {401, ``},
		"PATCH " + repoPath + "/threads/1/comments/1": {401, ``},
	})
	a := api(srv, "/")
	ctx := context.Background()
	_, err1 := a.Visibility(ctx)
	_, err2 := a.Labels(ctx, "7")
	_, _, _, err3 := a.FindThread(ctx, "7", key)
	err4 := a.CreateThread(ctx, "7", key, "b")
	err5 := a.UpdateThread(ctx, "7", 1, 1, "b", "active")
	for i, err := range []error{err1, err2, err3, err4, err5} {
		if err == nil {
			t.Errorf("call %d: want error on 401", i+1)
			continue
		}
		if !strings.Contains(err.Error(), "401") || strings.Contains(err.Error(), "secret-token") {
			t.Errorf("call %d: error %q", i+1, err)
		}
	}
}
