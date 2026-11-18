package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "ghs_secret_token"

func newAPI(t *testing.T, h http.HandlerFunc) API {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return API{BaseURL: srv.URL, Repo: "acme/orders", Token: testToken, HTTP: srv.Client()}
}

func TestMarker(t *testing.T) {
	if got, want := Marker("graphwake:service:svc_1"), "<!-- graphwake:service:svc_1 -->"; got != want {
		t.Fatalf("Marker = %q, want %q", got, want)
	}
}

func TestLabelsParsed(t *testing.T) {
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/repos/acme/orders/issues/42/labels" || r.URL.Query().Get("per_page") != "100" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		fmt.Fprint(w, `[{"id":1,"name":"graphwake:ignore-breaking"},{"id":2,"name":"bug"}]`)
	})
	labels, err := api.Labels(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if len(labels) != 2 || labels[0] != "graphwake:ignore-breaking" || labels[1] != "bug" {
		t.Fatalf("labels = %v", labels)
	}
}

func TestFindIgnoresAnotherServicesComment(t *testing.T) {
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/orders/issues/42/comments" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		fmt.Fprint(w, `[
			{"id":11,"body":"<!-- graphwake:service:other -->\nOther service"},
			{"id":12,"body":"<!-- graphwake:service:mine -->\nMy service"}
		]`)
	})
	id, found, err := api.FindComment(context.Background(), "42", Marker("graphwake:service:mine"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || id != 12 {
		t.Fatalf("FindComment = %d, %v; want 12, true", id, found)
	}
}

func TestFindNotFound(t *testing.T) {
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":11,"body":"<!-- graphwake:service:other -->"}]`)
	})
	_, found, err := api.FindComment(context.Background(), "42", Marker("graphwake:service:mine"))
	if err != nil || found {
		t.Fatalf("FindComment found=%v err=%v; want not found, no error", found, err)
	}
}

func TestFindAcrossPageTwo(t *testing.T) {
	var pages []string
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if r.URL.Query().Get("per_page") != "100" {
			t.Errorf("per_page = %q", r.URL.Query().Get("per_page"))
		}
		var comments []map[string]any
		switch page {
		case "1":
			for i := 0; i < 100; i++ {
				comments = append(comments, map[string]any{"id": i + 1, "body": "unrelated"})
			}
		case "2":
			comments = []map[string]any{{"id": 205, "body": "<!-- graphwake:service:mine -->\nhi"}}
		default:
			t.Errorf("unexpected page %q", page)
		}
		_ = json.NewEncoder(w).Encode(comments)
	})
	id, found, err := api.FindComment(context.Background(), "42", Marker("graphwake:service:mine"))
	if err != nil {
		t.Fatal(err)
	}
	if !found || id != 205 {
		t.Fatalf("FindComment = %d, %v; want 205, true", id, found)
	}
	if strings.Join(pages, ",") != "1,2" {
		t.Fatalf("pages asked = %v", pages)
	}
}

func TestFindStopsAfterTenPages(t *testing.T) {
	calls := 0
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		comments := make([]map[string]any, 100)
		for i := range comments {
			comments[i] = map[string]any{"id": i + 1, "body": "unrelated"}
		}
		_ = json.NewEncoder(w).Encode(comments)
	})
	_, found, err := api.FindComment(context.Background(), "42", Marker("graphwake:service:mine"))
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if calls != 10 {
		t.Fatalf("calls = %d, want 10", calls)
	}
}

func TestCreateCommentSendsJSONWithAuth(t *testing.T) {
	var gotBody map[string]string
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/acme/orders/issues/42/comments" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("X-GitHub-Api-Version"); got != "2022-11-28" {
			t.Errorf("X-GitHub-Api-Version = %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("body %q: %v", raw, err)
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":99}`)
	})
	body := "<!-- graphwake:service:mine -->\n**Breaking** & more"
	if err := api.CreateComment(context.Background(), "42", body); err != nil {
		t.Fatal(err)
	}
	if len(gotBody) != 1 || gotBody["body"] != body {
		t.Fatalf("sent body = %v", gotBody)
	}
}

func TestUpdateCommentPatches(t *testing.T) {
	var gotBody map[string]string
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/repos/acme/orders/issues/comments/12" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		fmt.Fprint(w, `{"id":12}`)
	})
	if err := api.UpdateComment(context.Background(), 12, "new body"); err != nil {
		t.Fatal(err)
	}
	if gotBody["body"] != "new body" {
		t.Fatalf("sent body = %v", gotBody)
	}
}

func TestForbiddenIsErrorWithoutToken(t *testing.T) {
	api := newAPI(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
	})
	ctx := context.Background()
	errs := map[string]error{}
	_, errs["Labels"] = api.Labels(ctx, "42")
	_, _, errs["FindComment"] = api.FindComment(ctx, "42", Marker("graphwake:service:mine"))
	errs["CreateComment"] = api.CreateComment(ctx, "42", "x")
	errs["UpdateComment"] = api.UpdateComment(ctx, 12, "x")
	for name, err := range errs {
		if err == nil {
			t.Errorf("%s: want error on 403", name)
			continue
		}
		if !strings.Contains(err.Error(), "403") {
			t.Errorf("%s: error %q does not mention the status", name, err)
		}
		if strings.Contains(err.Error(), testToken) {
			t.Errorf("%s: error leaks the token: %q", name, err)
		}
	}
}
