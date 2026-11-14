package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub implements the slice of the Contents API that GitHubRepo uses.
func fakeGitHub(files map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/repos/o/r/contents/")
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		_, exists := files[path]
		commit := `{"commit":{"html_url":"https://github.com/o/r/commit/abc"}}`
		switch {
		case r.Method == "PUT" && exists:
			http.Error(w, `{"message":"Invalid request.\n\n\"sha\" wasn't supplied."}`, 422)
		case r.Method == "PUT":
			b, _ := base64.StdEncoding.DecodeString(in["content"])
			files[path] = string(b)
			io.WriteString(w, commit)
		case !exists:
			http.Error(w, `{"message":"Not Found"}`, 404)
		case r.Method == "GET":
			io.WriteString(w, `{"sha":"sha-`+path+`"}`)
		case r.Method == "DELETE" && in["sha"] == "sha-"+path:
			delete(files, path)
			io.WriteString(w, commit)
		default:
			http.Error(w, "unexpected", 400)
		}
	}))
}

// fakeArgoCD knows one pre-existing app and answers 403 for anything else, like ArgoCD.
func fakeArgoCD(refreshes *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("refresh") != "":
			*refreshes++
		case r.URL.Path == "/api/v1/applications/legacy":
		default:
			http.Error(w, "permission denied", 403)
		}
	}))
}

func TestGitOpsCreateDelete(t *testing.T) {
	files := map[string]string{}
	gh, refreshes := fakeGitHub(files), 0
	argo := fakeArgoCD(&refreshes)
	defer gh.Close()
	defer argo.Close()

	repo, err := NewGitHubRepo("https://github.com/o/r", "main", "token")
	if err != nil {
		t.Fatal(err)
	}
	repo.API = gh.URL
	g := &GitOps{repo: repo, argo: NewArgoCDClient(argo.URL, "t")}

	req := EnvironmentRequest{Name: "feat", Branch: "main", Replicas: 2}
	commit, err := g.Create(req)
	if err != nil || commit == "" {
		t.Fatalf("create: %q %v", commit, err)
	}
	var app ArgoCDApplication
	if err := json.Unmarshal([]byte(files["environments/feat.json"]), &app); err != nil || app.Metadata.Name != "feat" {
		t.Fatalf("committed file is not the Application: %v %+v", err, app.Metadata)
	}
	if refreshes != 1 {
		t.Fatalf("root app should be refreshed after a commit, got %d", refreshes)
	}

	if _, err := g.Create(req); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("duplicate create: %v", err)
	}
	for _, name := range []string{"legacy", rootApp} {
		if _, err := g.Create(EnvironmentRequest{Name: name, Branch: "main"}); err == nil {
			t.Fatalf("%s: names of existing ArgoCD apps must be rejected", name)
		}
	}
	if _, ok := files["environments/legacy.json"]; ok {
		t.Fatal("rejected create must not commit")
	}

	if _, err := g.Delete("feat"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := files["environments/feat.json"]; ok {
		t.Fatal("delete should remove the file")
	}
	if _, err := g.Delete("feat"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestNewGitHubRepoRejectsNonGitHub(t *testing.T) {
	for _, u := range []string{"", "https://gitlab.com/o/r", "https://github.com/o", "::"} {
		if _, err := NewGitHubRepo(u, "main", "t"); err == nil {
			t.Errorf("%q: expected error", u)
		}
	}
}
