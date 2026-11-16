package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeGitHub implements the slice of GitHub's Git Data API that GitHubRepo uses,
// including the fast-forward check on ref updates.
type fakeGitHub struct {
	files    map[string]string
	head     string
	trees    map[string]map[string]string
	commits  map[string][2]string // sha → {tree, parent}
	messages []string
}

func newFakeGitHub(files map[string]string) (*fakeGitHub, *httptest.Server) {
	f := &fakeGitHub{files: files, head: "c0", trees: map[string]map[string]string{}, commits: map[string][2]string{}}
	return f, httptest.NewServer(http.HandlerFunc(f.serve))
}

func (f *fakeGitHub) serve(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimPrefix(r.URL.Path, "/repos/o/r/")
	var in map[string]any
	json.NewDecoder(r.Body).Decode(&in)
	reply := func(v any) { json.NewEncoder(w).Encode(v) }

	switch {
	case r.Method == "GET" && p == "git/ref/heads/main":
		reply(map[string]any{"object": map[string]string{"sha": f.head}})
	case r.Method == "GET" && strings.HasPrefix(p, "git/commits/"):
		reply(map[string]any{"tree": map[string]string{"sha": "tree-of-" + strings.TrimPrefix(p, "git/commits/")}})
	case r.Method == "GET" && strings.HasPrefix(p, "git/trees/"):
		var entries []map[string]string
		for path := range f.files {
			entries = append(entries, map[string]string{"path": path, "type": "blob"})
		}
		reply(map[string]any{"tree": entries})
	case r.Method == "GET" && strings.HasPrefix(p, "contents/"):
		c, ok := f.files[strings.TrimPrefix(p, "contents/")]
		if !ok {
			http.Error(w, "Not Found", 404)
			return
		}
		reply(map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(c)), "encoding": "base64"})
	case r.Method == "POST" && p == "git/trees":
		next := maps.Clone(f.files)
		for _, e := range in["tree"].([]any) {
			e := e.(map[string]any)
			if c, ok := e["content"]; ok {
				next[e["path"].(string)] = c.(string)
			} else {
				delete(next, e["path"].(string))
			}
		}
		id := "t" + string(rune('0'+len(f.trees)))
		f.trees[id] = next
		reply(map[string]string{"sha": id})
	case r.Method == "POST" && p == "git/commits":
		id := "c" + string(rune('1'+len(f.commits)))
		f.commits[id] = [2]string{in["tree"].(string), in["parents"].([]any)[0].(string)}
		f.messages = append(f.messages, in["message"].(string))
		reply(map[string]string{"sha": id, "html_url": "https://github.com/o/r/commit/" + id})
	case r.Method == "PATCH" && p == "git/refs/heads/main":
		c := f.commits[in["sha"].(string)]
		if c[1] != f.head {
			http.Error(w, `{"message":"Update is not a fast forward"}`, 422)
			return
		}
		f.head, f.files = in["sha"].(string), f.trees[c[0]]
		reply(map[string]string{})
	default:
		http.Error(w, "unexpected "+r.Method+" "+p, 400)
	}
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

func newTestGitOps(t *testing.T, argoURL string) (*GitOps, *fakeGitHub) {
	t.Helper()
	gh, srv := newFakeGitHub(map[string]string{readmePath: "x"})
	t.Cleanup(srv.Close)
	repo, err := NewGitHubRepo("https://github.com/o/r", "main", "token")
	if err != nil {
		t.Fatal(err)
	}
	repo.API = srv.URL
	return &GitOps{repo: repo, argo: NewArgoCDClient(argoURL, "t")}, gh
}

func TestGitOpsCreateDelete(t *testing.T) {
	refreshes := 0
	argo := fakeArgoCD(&refreshes)
	defer argo.Close()
	g, gh := newTestGitOps(t, argo.URL)

	req := EnvironmentRequest{Name: "feat", Branch: "main", Replicas: 2}
	commit, err := g.Create([]EnvironmentRequest{req})
	if err != nil || commit == "" {
		t.Fatalf("create: %q %v", commit, err)
	}
	var app ArgoCDApplication
	if err := json.Unmarshal([]byte(gh.files["environments/feat.json"]), &app); err != nil || app.Metadata.Name != "feat" {
		t.Fatalf("committed file is not the Application: %v %+v", err, app.Metadata)
	}
	if exp, err := time.Parse(time.RFC3339, app.Metadata.Annotations[expiresAt]); err != nil ||
		time.Until(exp) < defaultTTL-time.Minute || time.Until(exp) > defaultTTL {
		t.Fatalf("no ttl should mean the default expiry, got %q", app.Metadata.Annotations[expiresAt])
	}
	if refreshes != 1 {
		t.Fatalf("root app should be refreshed after a commit, got %d", refreshes)
	}

	if _, err := g.Create([]EnvironmentRequest{req}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate create: %v", err)
	}
	for _, name := range []string{"legacy", rootApp} {
		if _, err := g.Create([]EnvironmentRequest{{Name: name, Branch: "main"}}); err == nil {
			t.Fatalf("%s: names of existing ArgoCD apps must be rejected", name)
		}
	}
	if len(gh.messages) != 1 {
		t.Fatalf("rejected creates must not commit, got %v", gh.messages)
	}

	if _, err := g.Delete("feat"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := gh.files["environments/feat.json"]; ok {
		t.Fatal("delete should remove the file")
	}
	if _, err := g.Delete("feat"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("delete missing: %v", err)
	}
}

func TestGitOpsMatrixGroupIsOneCommit(t *testing.T) {
	refreshes := 0
	argo := fakeArgoCD(&refreshes)
	defer argo.Close()
	g, gh := newTestGitOps(t, argo.URL)

	reqs, err := ExpandRequest(EnvironmentRequest{Name: "pg", Branch: "main", Benchmark: true,
		Matrix: map[string][]string{"postgresql": {"16", "17", "18"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Create(reqs); err != nil {
		t.Fatal(err)
	}
	if len(gh.messages) != 1 || !strings.HasPrefix(gh.messages[0], "Create group pg: 3 environments") {
		t.Fatalf("want one group commit, got %q", gh.messages)
	}
	f := gh.files["environments/pg/pg-postgresql-17.json"]
	for _, want := range []string{`"postgres=postgres:17-alpine"`, `"deploy/bench/pgbench"`, `"meeseeks/group": "pg"`} {
		if !strings.Contains(f, want) {
			t.Errorf("group member missing %s:\n%s", want, f)
		}
	}

	// The group name is taken, and so are the member names.
	for _, name := range []string{"pg", "pg-postgresql-16"} {
		if _, err := g.Create([]EnvironmentRequest{{Name: name, Branch: "main"}}); err == nil {
			t.Errorf("%s should be taken", name)
		}
	}

	if _, err := g.Delete("pg-postgresql-16"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Delete("pg"); err != nil {
		t.Fatal(err)
	}
	if last := gh.messages[len(gh.messages)-1]; last != "Delete group pg: 2 environments" {
		t.Fatalf("group delete message: %q", last)
	}
	if len(gh.files) != 1 { // only the README is left
		t.Fatalf("group delete left %v", gh.files)
	}
}

func TestGitOpsCreateFailsClosedWhenArgoCDErrors(t *testing.T) {
	argo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer argo.Close()
	g, gh := newTestGitOps(t, argo.URL)

	if _, err := g.Create([]EnvironmentRequest{{Name: "test", Branch: "main"}}); err == nil {
		t.Fatal("create must fail when the name can't be checked")
	}
	if len(gh.messages) != 0 {
		t.Fatalf("nothing may be committed, got %v", gh.messages)
	}
}

func TestGitHubCommitRejectsNonFastForward(t *testing.T) {
	g, gh := newTestGitOps(t, "http://unused")
	s, err := g.repo.snapshot()
	if err != nil {
		t.Fatal(err)
	}
	x := "x"
	if _, err := g.repo.commit(s, "first", map[string]*string{"a": &x}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.repo.commit(s, "stale", map[string]*string{"b": &x}); err == nil {
		t.Fatal("a commit on a stale head must not overwrite the branch")
	}
	if _, ok := gh.files["b"]; ok {
		t.Fatal("stale commit leaked into the branch")
	}
}

func TestListApplications(t *testing.T) {
	argo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"items":[
		  {"metadata":{"name":"legacy","labels":{"managed-by":"meeseeks"}}},
		  {"metadata":{"name":"bad","labels":{"meeseeks/gitops":"true","env-type":"dev","meeseeks/group":"g"}},
		   "spec":{"sources":[{"targetRevision":"nope","path":"deploy/base"},
		     {"targetRevision":"nope","path":"deploy/deps/postgresql","kustomize":{"images":["postgres=postgres:16-alpine"]}},
		     {"targetRevision":"nope","path":"deploy/bench/pgbench"}]},
		   "status":{"sync":{"status":"Unknown"},"health":{"status":"Healthy"},
		     "conditions":[{"type":"ComparisonError","message":"Unable to resolve 'nope' to a commit SHA"}]}},
		  {"metadata":{"name":"going","labels":{"meeseeks/gitops":"true"},"deletionTimestamp":"2026-01-01T00:00:00Z"},
		   "status":{"health":{"status":"Healthy"}}}
		]}`)
	}))
	defer argo.Close()

	list, err := NewArgoCDClient(argo.URL, "t").ListApplications()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("only client-repo apps should be listed, got %+v", list.Items)
	}
	bad, going := list.Items[0], list.Items[1]
	if !strings.Contains(bad.Message, "Unable to resolve") || bad.Branch != "nope" || bad.Group != "g" ||
		bad.DepsLabel() != "postgresql 16" || bad.Benchmark == nil {
		t.Errorf("bad: %+v", bad)
	}
	if going.Status != "Deleting" || going.Benchmark != nil {
		t.Errorf("going: %+v", going)
	}
}

func TestParsePgbench(t *testing.T) {
	logs := `pgbench (18.1)
transaction type: <builtin: TPC-B (sort of)>
number of clients: 8
duration: 30 s
number of transactions actually processed: 36912
latency average = 6.497 ms
initial connection time = 12.345 ms
tps = 1231.315200 (without initial connection time)`
	r := parsePgbench(logs)
	if !r.Done || r.TPS != 1231.3152 || r.LatencyMs != 6.497 {
		t.Fatalf("got %+v", r)
	}
	if parsePgbench("starting vacuum...").Done {
		t.Fatal("unfinished run must not be Done")
	}
}

func TestNewGitHubRepoRejectsNonGitHub(t *testing.T) {
	for _, u := range []string{"", "https://gitlab.com/o/r", "https://github.com/o", "::"} {
		if _, err := NewGitHubRepo(u, "main", "t"); err == nil {
			t.Errorf("%q: expected error", u)
		}
	}
}

func TestReapExpired(t *testing.T) {
	past, future := "2026-01-01T00:00:00Z", "2099-01-01T00:00:00Z"
	labels := func(group string) string {
		l := `"meeseeks/gitops":"true"`
		if group != "" {
			l += `,"meeseeks/group":"` + group + `"`
		}
		return l
	}
	app := func(name, group, expires string) string { // as listed by Argo CD and as committed to Git
		ann := ""
		if expires != "" {
			ann = `,"annotations":{"meeseeks/expires-at":"` + expires + `"}`
		}
		return `{"metadata":{"name":"` + name + `","labels":{` + labels(group) + `}` + ann + `}}`
	}

	// Argo CD's view, which can lag behind Git.
	listOK := true
	argoApps := []string{
		app("old", "", past), app("fresh", "", future), app("forever", "", ""), app("garbled", "", "next tuesday"),
		app("grp-a", "grp", past), app("grp-b", "grp", future), // members expire on their own
		app("stale-g-16", "stale-g", past), // group stale-g already left Git; a new single env took the name
		app("stale-x", "", past),           // single stale-x already left Git; a new group took the name
		app("kept", "", past),              // expired in Argo CD, but its file in Git no longer has the annotation
	}
	argo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Query().Get("refresh") != "":
		case r.URL.Path == "/api/v1/applications" && listOK:
			io.WriteString(w, `{"items":[`+strings.Join(argoApps, ",")+`]}`)
		default:
			http.Error(w, "boom", 500)
		}
	}))
	defer argo.Close()

	g, gh := newTestGitOps(t, argo.URL)
	for path, content := range map[string]string{
		"old": app("old", "", past), "fresh": app("fresh", "", future),
		"forever": app("forever", "", ""), "garbled": app("garbled", "", "next tuesday"),
		"grp/grp-a": app("grp-a", "grp", past), "grp/grp-b": app("grp-b", "grp", future),
		"stale-g":                  app("stale-g", "", future),
		"stale-x/stale-x-postgres": app("stale-x-postgres", "stale-x", future),
		"kept":                     app("kept", "", ""),
	} {
		gh.files["environments/"+path+".json"] = content
	}

	listOK = false
	g.ReapExpired(time.Now())
	if len(gh.messages) != 0 {
		t.Fatalf("a failed list must not delete anything, got %v", gh.messages)
	}

	listOK = true
	g.ReapExpired(time.Now())
	if want := []string{"Expire 2 environments\n\ngrp-a\nold"}; !slices.Equal(gh.messages, want) {
		t.Fatalf("commits = %q, want %q", gh.messages, want)
	}
	for _, f := range []string{"old", "grp/grp-a"} {
		if _, ok := gh.files["environments/"+f+".json"]; ok {
			t.Errorf("%s expired in Git and should be gone", f)
		}
	}
	for _, f := range []string{"fresh", "forever", "garbled", "grp/grp-b", "stale-g", "stale-x/stale-x-postgres", "kept"} {
		if _, ok := gh.files["environments/"+f+".json"]; !ok {
			t.Errorf("%s must survive: only a file whose own expiry has passed is deleted", f)
		}
	}
}

func TestExpiredReadsOnlyAnnotations(t *testing.T) {
	// replicas count as a string is valid for Argo CD (IntOrString) but not for ArgoCDApplication.
	ok, err := expired(`{"metadata":{"annotations":{"meeseeks/expires-at":"2026-01-01T00:00:00Z"}},
		"spec":{"sources":[{"kustomize":{"replicas":[{"name":"app","count":"2"}]}}]}}`, time.Now())
	if err != nil || !ok {
		t.Fatalf("an expired file must expire whatever else it holds: %v %v", ok, err)
	}
	if _, err := expired("not json", time.Now()); err == nil {
		t.Fatal("a file that isn't JSON must report an error (logged by the reaper)")
	}
}
