package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
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
}

type EnvironmentResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	URL    string `json:"url"`
	Commit string `json:"commit"`
}

// Environments is the backend: GitOps (client repo + ArgoCD) for real, MockEnvironments in dev mode.
type Environments interface {
	Create(req EnvironmentRequest) (commitURL string, err error)
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

	if err := ValidateEnvironmentRequest(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	commit, err := api.envs.Create(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create environment: %v", err), http.StatusInternalServerError)
		return
	}

	// 202: the change is committed; ArgoCD applies it asynchronously.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(EnvironmentResponse{ID: req.Name, Status: "committed", URL: envURL(req.Name), Commit: commit})
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
            </div>
            <div class="grid field">
                <div><label for="cpu">CPU</label><input id="cpu" name="cpu" value="100m"></div>
                <div><label for="memory">Memory</label><input id="memory" name="memory" value="128Mi"></div>
                <div><label for="replicas">Replicas</label><input id="replicas" name="replicas" type="number" value="1" min="1" max="10"></div>
            </div>
            <div class="field checks">
                <label style="display:block;font-weight:600">Dependencies</label>
                <label><input type="checkbox" name="dependencies" value="postgresql"> PostgreSQL</label>
                <label><input type="checkbox" name="dependencies" value="redis"> Redis</label>
                <label><input type="checkbox" name="dependencies" value="mongodb"> MongoDB</label>
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
	if err := homeTmpl.Execute(w, struct{ Repo string }{api.envs.RepoURL()}); err != nil {
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
	}
	if replicas := r.FormValue("replicas"); replicas != "" {
		n, err := strconv.Atoi(replicas)
		if err != nil {
			htmxError(w, "Validation error", fmt.Errorf("replicas must be a number"))
			return
		}
		req.Replicas = n
	}

	if err := ValidateEnvironmentRequest(req); err != nil {
		htmxError(w, "Validation error", err)
		return
	}

	commit, err := api.envs.Create(req)
	if err != nil {
		htmxError(w, "Failed to create environment", err)
		return
	}

	w.Header().Set("HX-Trigger", "env-changed") // refresh the list
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	successTmpl.Execute(w, struct{ Name, Commit, URL string }{req.Name, commit, envURL(req.Name)})
}

var successTmpl = template.Must(template.New("success").Parse(`<div class="response success">
Committed <a href="{{.Commit}}" target="_blank">environment <strong>{{.Name}}</strong></a> to the GitOps repo.
ArgoCD is syncing it; it will be at <a href="{{.URL}}" target="_blank">{{.URL}}</a></div>`))

var listTmpl = template.Must(template.New("list").Funcs(template.FuncMap{"join": strings.Join}).Parse(`
{{- range .}}
<div class="env-item">
  <div>
    <div class="env-name"><a href="{{.URL}}" target="_blank">{{.Name}}</a></div>
    <div class="env-details">
      <span class="badge {{.Sync}}">{{or .Sync "Unknown"}}</span>
      <span class="badge {{.Status}}">{{or .Status "Unknown"}}</span>
      branch <code>{{.Branch}}</code>{{with .EnvType}} · {{.}}{{end}}{{with .Dependencies}} · {{join . ", "}}{{end}}
      {{with .GitURL}} · <a href="{{.}}" target="_blank">view in Git</a>{{end}}
    </div>
    {{with .Message}}<div class="env-error">{{.}}</div>{{end}}
  </div>
  <button class="delete-btn" hx-delete="/environments/{{.Name}}" hx-target="closest .env-item" hx-swap="outerHTML"
    hx-confirm="Delete environment {{.Name}}?">Delete</button>
</div>
{{- else}}
<div class="hint">No environments yet.</div>
{{- end}}`))

func (api *MeeseeksAPI) listEnvironmentsHTMX(w http.ResponseWriter, r *http.Request) {
	environments, err := api.envs.List()
	if err != nil {
		htmxError(w, "Failed to list environments", err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := listTmpl.Execute(w, environments.Items); err != nil {
		log.Printf("render list: %v", err)
	}
}

// MockEnvironments is a fake backend for UI work without a cluster (make dev-mock).
type MockEnvironments struct{}

func (m *MockEnvironments) Create(req EnvironmentRequest) (string, error) {
	log.Printf("Mock: Creating environment %s", req.Name)
	return "https://github.com/example/environments/commit/0000000", nil
}

func (m *MockEnvironments) List() (EnvironmentList, error) {
	return EnvironmentList{
		Items: []EnvironmentItem{
			{ID: "test-env-1", Name: "test-env-1", Status: "Healthy", Sync: "Synced", Branch: "main", EnvType: "dev", URL: envURL("test-env-1")},
			{ID: "staging-app", Name: "staging-app", Status: "Progressing", Sync: "Synced", Branch: "main", EnvType: "staging", Dependencies: []string{"postgresql"}, URL: envURL("staging-app")},
			{ID: "demo-service", Name: "demo-service", Status: "Healthy", Sync: "Synced", Branch: "demo", EnvType: "dev", Dependencies: []string{"redis", "mongodb"}, URL: envURL("demo-service")},
		},
	}, nil
}

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
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		deletedTmpl.Execute(w, struct{ Name, Commit string }{name, commit})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"id": name, "status": "deletion committed", "commit": commit})
}

var deletedTmpl = template.Must(template.New("deleted").Parse(`<div class="env-item hint">
Removed <strong>{{.Name}}</strong> in <a href="{{.Commit}}" target="_blank">this commit</a>; ArgoCD is pruning it.</div>`))
