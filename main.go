package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"maps"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

//go:embed static/logo.jpg
var staticFS embed.FS

type EnvironmentRequest struct {
	Name         string            `json:"name"`
	Branch       string            `json:"branch"`
	CPU          string            `json:"cpu"`
	Memory       string            `json:"memory"`
	Replicas     int               `json:"replicas"`
	Dependencies []string          `json:"dependencies"`
	EnvType      string            `json:"env_type"`
	EnvVars      map[string]string `json:"env_vars"`

	Versions  map[string]string   `json:"versions"`  // dependency version, e.g. {"postgresql": "17"}
	Matrix    map[string][]string `json:"matrix"`    // one environment per version, e.g. {"postgresql": ["16", "17"]}
	Benchmark bool                `json:"benchmark"` // run pgbench against postgresql
	TTL       string              `json:"ttl"`       // Go duration, e.g. "4h"; default 24h
	Group     string              `json:"-"`         // set by ExpandRequest on matrix members
	ExpiresAt time.Time           `json:"-"`         // set by GitOps.Create from TTL
}

type EnvironmentResponse struct {
	Status       string       `json:"status"`
	Commit       string       `json:"commit"`
	Group        string       `json:"group,omitempty"`
	Environments []CreatedEnv `json:"environments"`
}

type CreatedEnv struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func createdEnvs(reqs []EnvironmentRequest) []CreatedEnv {
	var out []CreatedEnv
	for _, r := range reqs {
		out = append(out, CreatedEnv{ID: r.Name, URL: envURL(r.Name)})
	}
	return out
}

// Environments is the backend: GitOps (client repo + ArgoCD) for real, MockEnvironments in dev mode.
type Environments interface {
	Create(reqs []EnvironmentRequest) (commitURL string, err error)
	List() (EnvironmentList, error)
	Delete(name string) (commitURL string, err error)
	RepoURL() string
}

type MeeseeksAPI struct {
	envs Environments
}

func (api *MeeseeksAPI) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var req EnvironmentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	reqs, err := ExpandRequest(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	commit, err := api.envs.Create(reqs)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create environment: %v", err), http.StatusInternalServerError)
		return
	}

	// 202: the change is committed; ArgoCD applies it asynchronously.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(EnvironmentResponse{Status: "committed", Commit: commit, Group: reqs[0].Group, Environments: createdEnvs(reqs)})
}

func (api *MeeseeksAPI) listEnvironments(w http.ResponseWriter, r *http.Request) {
	environments, err := api.envs.List()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list environments: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(environments)
}

var homeTmpl = template.Must(template.New("home").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <title>Meeseeks</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        * { box-sizing: border-box; }
        body { font-family: system-ui, -apple-system, sans-serif; max-width: 900px; margin: 0 auto; padding: 24px 16px; background: #f4f6f8; color: #222; }
        header { display: flex; align-items: center; gap: 16px; margin-bottom: 24px; }
        header img { width: 64px; height: 64px; border-radius: 50%; object-fit: cover; }
        header h1 { margin: 0; font-size: 24px; }
        header p { margin: 4px 0 0; color: #666; }
        .card { background: white; padding: 24px; border-radius: 8px; box-shadow: 0 1px 4px rgba(0,0,0,0.08); margin-bottom: 24px; }
        h2 { margin-top: 0; font-size: 18px; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(160px, 1fr)); gap: 12px; }
        label { display: block; font-size: 13px; font-weight: 600; margin-bottom: 4px; }
        input, select, textarea { width: 100%; padding: 8px; border: 1px solid #ccd; border-radius: 4px; font: inherit; }
        .checks label { display: inline-flex; align-items: center; gap: 6px; font-weight: normal; margin-right: 16px; }
        .checks input { width: auto; }
        .field { margin-top: 12px; }
        button { background: #1f6feb; color: white; padding: 10px 20px; border: 0; border-radius: 4px; cursor: pointer; font: inherit; margin-top: 16px; }
        button:hover { background: #1158c7; }
        .htmx-request button[type=submit] { opacity: .6; }
        .delete-btn { background: #d73a49; padding: 6px 12px; font-size: 13px; margin: 0; }
        .delete-btn:hover { background: #b31d28; }
        .env-item { display: flex; justify-content: space-between; align-items: center; gap: 12px; padding: 12px 0; border-top: 1px solid #eee; }
        .env-item:first-child { border-top: 0; }
        .env-name { font-weight: 600; }
        .env-details { color: #666; font-size: 13px; margin-top: 4px; }
        .badge { display: inline-block; padding: 1px 8px; border-radius: 10px; font-size: 12px; background: #eee; }
        .Healthy, .Synced { background: #dcfce7; color: #166534; }
        .Progressing, .OutOfSync, .Deleting, .Unknown { background: #fef9c3; color: #854d0e; }
        .Degraded, .Missing { background: #fee2e2; color: #991b1b; }
        .response { margin-top: 16px; padding: 12px; border-radius: 4px; }
        .success { background: #dcfce7; color: #166534; }
        .error { background: #fee2e2; color: #991b1b; word-break: break-word; }
        .hint { color: #888; font-size: 12px; }
        .env-error { color: #991b1b; font-size: 12px; margin-top: 4px; word-break: break-word; }
        .flow { font-size: 13px; color: #555; }
        .flow code { background: #eef1f4; padding: 1px 6px; border-radius: 4px; }
        .dep-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; margin: 6px 0; }
        .dep-row > label:first-child { min-width: 110px; }
        .dep-row select { width: auto; padding: 4px; }
        .group { border: 1px solid #e3e7ec; border-radius: 6px; padding: 12px; margin-top: 12px; }
        .group-header { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
        table { width: 100%; border-collapse: collapse; margin-top: 8px; font-size: 13px; }
        th, td { text-align: left; padding: 6px 8px; border-top: 1px solid #eee; vertical-align: top; }
        th { color: #666; font-weight: 600; }
        td.num { font-variant-numeric: tabular-nums; }
        .best { color: #166534; font-weight: 700; }
    </style>
</head>
<body>
    <header>
        <img src="/static/logo.jpg" alt="">
        <div>
            <h1>Meeseeks</h1>
            <p>Ephemeral environments on Kubernetes, the GitOps way.</p>
            <p class="flow">Meeseeks commits to <a href="{{.Repo}}" target="_blank"><code>{{.Repo}}</code></a> → ArgoCD syncs it → Kubernetes</p>
        </div>
    </header>

    <div class="card">
        <h2>New environment</h2>
        <form hx-post="/environments" hx-target="#response">
            <div class="grid">
                <div><label for="name">Name</label><input id="name" name="name" placeholder="my-feature" required pattern="[a-z0-9]([a-z0-9\-]*[a-z0-9])?" title="lowercase letters, digits and dashes"></div>
                <div><label for="branch">Branch</label><input id="branch" name="branch" value="main" required></div>
                <div><label for="env_type">Type</label>
                    <select id="env_type" name="env_type"><option>dev</option><option>staging</option><option>prod</option></select></div>
                <div><label for="ttl">Expires after</label>
                    <select id="ttl" name="ttl"><option value="1h">1 hour</option><option value="4h">4 hours</option><option value="24h" selected>1 day</option><option value="72h">3 days</option><option value="168h">7 days</option></select></div>
            </div>
            <div class="grid field">
                <div><label for="cpu">CPU</label><input id="cpu" name="cpu" value="100m"></div>
                <div><label for="memory">Memory</label><input id="memory" name="memory" value="128Mi"></div>
                <div><label for="replicas">Replicas</label><input id="replicas" name="replicas" type="number" value="1" min="1" max="10"></div>
            </div>
            <div class="field">
                <label>Dependencies</label>
                {{- range $d := .Deps}}
                <div class="dep-row checks">
                    <label><input type="checkbox" name="dependencies" value="{{$d.Name}}"> {{$d.Label}}</label>
                    <select name="version_{{$d.Name}}" aria-label="{{$d.Label}} version">
                        {{- range $d.Versions}}<option{{if eq . $d.Default}} selected{{end}}>{{.}}</option>{{end}}
                    </select>
                    <span class="hint">or compare</span>
                    {{- range $d.Versions}}<label><input type="checkbox" name="matrix_{{$d.Name}}" value="{{.}}"> {{.}}</label>{{end}}
                </div>
                {{- end}}
                <div class="hint">Ticking versions under "compare" creates one environment per version, as a group, in a single commit.</div>
                <div class="checks" style="margin-top:8px">
                    <label><input type="checkbox" name="benchmark" value="true"> Run pgbench in each environment (needs PostgreSQL)</label>
                </div>
            </div>
            <div class="field">
                <label for="env_vars">Environment variables</label>
                <textarea id="env_vars" name="env_vars" rows="3" placeholder="LOG_LEVEL=debug&#10;FEATURE_X=true"></textarea>
                <div class="hint">One KEY=value per line. Dependencies inject DATABASE_URL / REDIS_URL / MONGODB_URL.</div>
            </div>
            <button type="submit">Create environment</button>
        </form>
        <div id="response"></div>
    </div>

    <div class="card">
        <h2>Environments</h2>
        <div id="environments" hx-get="/environments" hx-trigger="load, every 5s, env-changed from:body">Loading…</div>
    </div>
</body>
</html>`))

func (api *MeeseeksAPI) serveHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	type depOption struct {
		Name, Label, Default string
		Versions             []string
	}
	var deps []depOption
	for _, name := range slices.Sorted(maps.Keys(dependencies)) {
		d := dependencies[name]
		deps = append(deps, depOption{Name: name, Label: d.label, Default: d.defaultVersion(), Versions: d.versions})
	}
	if err := homeTmpl.Execute(w, struct {
		Repo string
		Deps []depOption
	}{api.envs.RepoURL(), deps}); err != nil {
		log.Printf("render home: %v", err)
	}
}

func htmxError(w http.ResponseWriter, msg string, err error) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<div class="response error">%s: %s</div>`, msg, html.EscapeString(err.Error()))
}

func (api *MeeseeksAPI) createEnvironmentHTMX(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		htmxError(w, "Error parsing form", err)
		return
	}

	envVars := map[string]string{}
	for _, line := range strings.Split(r.FormValue("env_vars"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			htmxError(w, "Invalid environment variables", fmt.Errorf("%q is not KEY=value", line))
			return
		}
		envVars[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}

	req := EnvironmentRequest{
		Name:         r.FormValue("name"),
		Branch:       r.FormValue("branch"),
		CPU:          r.FormValue("cpu"),
		Memory:       r.FormValue("memory"),
		Dependencies: r.Form["dependencies"],
		EnvType:      r.FormValue("env_type"),
		EnvVars:      envVars,
		Versions:     map[string]string{},
		Matrix:       map[string][]string{},
		Benchmark:    r.FormValue("benchmark") == "true",
		TTL:          r.FormValue("ttl"),
	}
	for dep := range dependencies {
		if v := r.FormValue("version_" + dep); v != "" && slices.Contains(req.Dependencies, dep) {
			req.Versions[dep] = v
		}
		if vs := r.Form["matrix_"+dep]; len(vs) > 0 {
			req.Matrix[dep] = vs
		}
	}
	if replicas := r.FormValue("replicas"); replicas != "" {
		n, err := strconv.Atoi(replicas)
		if err != nil {
			htmxError(w, "Validation error", fmt.Errorf("replicas must be a number"))
			return
		}
		req.Replicas = n
	}

	reqs, err := ExpandRequest(req)
	if err != nil {
		htmxError(w, "Validation error", err)
		return
	}

	commit, err := api.envs.Create(reqs)
	if err != nil {
		htmxError(w, "Failed to create environment", err)
		return
	}

	w.Header().Set("HX-Trigger", "env-changed") // refresh the list
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	successTmpl.Execute(w, EnvironmentResponse{Commit: commit, Group: reqs[0].Group, Environments: createdEnvs(reqs)})
}

var successTmpl = template.Must(template.New("success").Parse(`<div class="response success">
{{- if .Group}}
Committed <a href="{{.Commit}}" target="_blank">group <strong>{{.Group}}</strong> ({{len .Environments}} environments)</a>
to the GitOps repo in one commit. ArgoCD is syncing them.
{{- else}}{{with index .Environments 0}}
Committed <a href="{{$.Commit}}" target="_blank">environment <strong>{{.ID}}</strong></a> to the GitOps repo.
ArgoCD is syncing it; it will be at <a href="{{.URL}}" target="_blank">{{.URL}}</a>
{{- end}}{{end}}</div>`))

var listTmpl = template.Must(template.New("list").Parse(`
{{- range $e := .Single}}
<div class="env-item">
  <div>
    <div class="env-name"><a href="{{.URL}}" target="_blank">{{.Name}}</a></div>
    <div class="env-details">
      <span class="badge {{.Sync}}">{{or .Sync "Unknown"}}</span>
      <span class="badge {{.Status}}">{{or .Status "Unknown"}}</span>
      branch <code>{{.Branch}}</code>{{with .EnvType}} · {{.}}{{end}}{{with .DepsLabel}} · {{.}}{{end}}
      {{with .GitURL}} · <a href="{{.}}" target="_blank">view in Git</a>{{end}}{{with .Expiry}} · {{.}}{{end}}
      {{with .Benchmark}} · pgbench {{if .Done}}{{printf "%.0f" .TPS}} TPS, {{printf "%.1f" .LatencyMs}} ms{{else if eq $e.Status "Progressing"}}running…{{else}}no result{{end}}{{end}}
    </div>
    {{with .Message}}<div class="env-error">{{.}}</div>{{end}}
  </div>
  <button class="delete-btn" hx-delete="/environments/{{.Name}}" hx-target="#response"
    hx-confirm="Delete environment {{.Name}}?">Delete</button>
</div>
{{- end}}
{{- range $g := .Groups}}
<div class="group">
  <div class="group-header">
    <div><strong>{{$g.Name}}</strong>
      <span class="hint">{{len $g.Items}} environments · branch <code>{{(index $g.Items 0).Branch}}</code>{{with (index $g.Items 0).Expiry}} · {{.}}{{end}}</span></div>
    <button class="delete-btn" hx-delete="/environments/{{$g.Name}}" hx-target="#response"
      hx-confirm="Delete group {{$g.Name}} and its {{len $g.Items}} environments?">Delete group</button>
  </div>
  <table>
    <tr><th>Environment</th><th>Dependencies</th><th>Status</th>{{if $g.Bench}}<th>pgbench TPS</th><th>Avg latency</th>{{end}}</tr>
    {{- range $e := $g.Items}}
    <tr>
      <td><a href="{{.URL}}" target="_blank">{{.Name}}</a> · <a href="{{.GitURL}}" target="_blank">git</a>
        {{with .Message}}<div class="env-error">{{.}}</div>{{end}}</td>
      <td>{{.DepsLabel}}</td>
      <td><span class="badge {{.Sync}}">{{or .Sync "Unknown"}}</span> <span class="badge {{.Status}}">{{or .Status "Unknown"}}</span></td>
      {{- if $g.Bench}}{{with .Benchmark}}{{if .Done}}
      <td class="num{{if eq .TPS $g.BestTPS}} best{{end}}">{{printf "%.0f" .TPS}}</td><td class="num">{{printf "%.1f" .LatencyMs}} ms</td>
      {{- else}}<td colspan="2" class="hint">{{if eq $e.Status "Progressing"}}running…{{else}}no result{{end}}</td>{{end}}{{else}}<td colspan="2"></td>{{end}}{{end}}
    </tr>
    {{- end}}
  </table>
  {{- if $g.Bench}}<div class="hint">Same pgbench client and settings everywhere, but the runs are concurrent on one node and may not overlap exactly: treat the numbers as indicative.</div>{{end}}
</div>
{{- end}}
{{- if and (not .Single) (not .Groups)}}
<div class="hint">No environments yet.</div>
{{- end}}`))

type envGroup struct {
	Name    string
	Items   []EnvironmentItem
	Bench   bool
	BestTPS float64
}

// groupEnvironments splits the list into standalone environments and matrix groups.
func groupEnvironments(items []EnvironmentItem) (single []EnvironmentItem, groups []envGroup) {
	byName := map[string]*envGroup{}
	for _, e := range items {
		if e.Group == "" {
			single = append(single, e)
			continue
		}
		g := byName[e.Group]
		if g == nil {
			g = &envGroup{Name: e.Group}
			byName[e.Group] = g
		}
		g.Items = append(g.Items, e)
		if e.Benchmark != nil {
			g.Bench = true
			g.BestTPS = max(g.BestTPS, e.Benchmark.TPS)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(byName)) {
		groups = append(groups, *byName[name])
	}
	return single, groups
}

func (api *MeeseeksAPI) listEnvironmentsHTMX(w http.ResponseWriter, r *http.Request) {
	environments, err := api.envs.List()
	if err != nil {
		htmxError(w, "Failed to list environments", err)
		return
	}

	single, groups := groupEnvironments(environments.Items)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := listTmpl.Execute(w, struct {
		Single []EnvironmentItem
		Groups []envGroup
	}{single, groups}); err != nil {
		log.Printf("render list: %v", err)
	}
}

// MockEnvironments is a fake backend for UI work without a cluster (make dev-mock).
type MockEnvironments struct{}

func (m *MockEnvironments) Create(reqs []EnvironmentRequest) (string, error) {
	log.Printf("Mock: Creating %d environment(s), first %s", len(reqs), reqs[0].Name)
	return "https://github.com/example/environments/commit/0000000", nil
}

func (m *MockEnvironments) List() (EnvironmentList, error) {
	return EnvironmentList{
		Items: []EnvironmentItem{
			{ID: "test-env-1", Name: "test-env-1", Status: "Healthy", Sync: "Synced", Branch: "main", EnvType: "dev", URL: envURL("test-env-1"), ExpiresAt: mockExpiry(3*time.Hour + 12*time.Minute)},
			{ID: "staging-app", Name: "staging-app", Status: "Progressing", Sync: "Synced", Branch: "main", EnvType: "staging", Dependencies: []string{"postgresql"}, URL: envURL("staging-app")},
			{ID: "demo-service", Name: "demo-service", Status: "Healthy", Sync: "Synced", Branch: "demo", EnvType: "dev", Dependencies: []string{"redis", "mongodb"}, URL: envURL("demo-service")},
			mockBench("pg-compare", "16", 1180, 6.8), mockBench("pg-compare", "17", 1265, 6.3), mockBench("pg-compare", "18", 0, 0),
		},
	}, nil
}

func mockBench(group, version string, tps, latency float64) EnvironmentItem {
	name := group + "-postgresql-" + version
	return EnvironmentItem{ID: name, Name: name, Group: group, Status: "Healthy", Sync: "Synced", Branch: "main",
		Dependencies: []string{"postgresql"}, Versions: map[string]string{"postgresql": version}, URL: envURL(name),
		Benchmark: &BenchResult{Done: tps > 0, TPS: tps, LatencyMs: latency}, ExpiresAt: mockExpiry(20 * time.Hour)}
}

func mockExpiry(in time.Duration) string { return time.Now().Add(in).Format(time.RFC3339) }

func (m *MockEnvironments) Delete(name string) (string, error) {
	log.Printf("Mock: Deleting environment %s", name)
	return "https://github.com/example/environments/commit/0000000", nil
}

func (m *MockEnvironments) RepoURL() string { return "https://github.com/example/environments" }

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	argoCDToken := os.Getenv("ARGOCD_TOKEN")

	var envs Environments
	if argoCDToken == "" || argoCDToken == "mock-token" || os.Getenv("DEV_MODE") == "true" {
		log.Println("🚀 Starting Meeseeks in Development Mode (mock backend, no Git or ArgoCD calls)")
		envs = &MockEnvironments{}
	} else {
		repo, err := NewGitHubRepo(os.Getenv("GITOPS_REPO"), getenv("GITOPS_BRANCH", "main"), os.Getenv("GITHUB_TOKEN"))
		if err != nil {
			log.Fatal(err)
		}
		if repo.token == "" {
			log.Fatal("GITHUB_TOKEN is required (write access to GITOPS_REPO)")
		}
		gitops := &GitOps{repo: repo, argo: NewArgoCDClient(getenv("ARGOCD_URL", "http://localhost:30080"), argoCDToken)}
		if err := gitops.Bootstrap(); err != nil {
			log.Fatalf("bootstrap: %v", err)
		}
		log.Printf("GitOps repo: %s (branch %s), root app: %s", repo.URL(), repo.Branch, rootApp)
		go func() {
			for range time.Tick(time.Minute) {
				gitops.ReapExpired(time.Now())
			}
		}()
		envs = gitops
	}

	api := &MeeseeksAPI{envs: envs}

	mux := http.NewServeMux()

	// Frontend routes
	mux.HandleFunc("/", api.serveHome)
	mux.Handle("/static/", http.FileServerFS(staticFS))

	// API routes: JSON by default, HTML fragments for HTMX requests
	mux.HandleFunc("/environments", func(w http.ResponseWriter, r *http.Request) {
		htmx := r.Header.Get("HX-Request") == "true"
		switch {
		case r.Method == http.MethodPost && htmx:
			api.createEnvironmentHTMX(w, r)
		case r.Method == http.MethodGet && htmx:
			api.listEnvironmentsHTMX(w, r)
		case r.Method == http.MethodPost:
			api.createEnvironment(w, r)
		case r.Method == http.MethodGet:
			api.listEnvironments(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/environments/", api.deleteEnvironment)

	port := getenv("PORT", "22282")
	log.Printf("Meeseeks API server starting on port %s", port)
	log.Printf("Frontend available at: http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func (api *MeeseeksAPI) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	htmx := r.Header.Get("HX-Request") == "true"
	name := r.URL.Path[len("/environments/"):]
	commit, err := "", validateName(name) // the name becomes a path in the GitOps repo
	if err == nil {
		commit, err = api.envs.Delete(name)
	}
	if err != nil {
		if htmx {
			htmxError(w, "Failed to delete environment", err)
		} else {
			http.Error(w, fmt.Sprintf("Failed to delete environment: %v", err), http.StatusBadRequest)
		}
		return
	}

	if htmx {
		// Feedback goes to #response (the list re-renders every 5 s and could drop a row-level
		// swap); the trigger refreshes the list right away.
		w.Header().Set("HX-Trigger", "env-changed")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		deletedTmpl.Execute(w, struct{ Name, Commit string }{name, commit})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"id": name, "status": "deletion committed", "commit": commit})
}

var deletedTmpl = template.Must(template.New("deleted").Parse(`<div class="response success">
Removed <strong>{{.Name}}</strong> in <a href="{{.Commit}}" target="_blank">this commit</a>; ArgoCD is pruning it.</div>`))
