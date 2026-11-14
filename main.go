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

//go:embed static
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
}

type ArgoCDClientInterface interface {
	CreateApplication(req EnvironmentRequest) (string, error)
	ListApplications() (EnvironmentList, error)
	DeleteApplication(name string) error
}

type MeeseeksAPI struct {
	argoCDClient ArgoCDClientInterface
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

	envID, err := api.argoCDClient.CreateApplication(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to create environment: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(EnvironmentResponse{ID: envID, Status: "creating", URL: envURL(req.Name)})
}

func (api *MeeseeksAPI) listEnvironments(w http.ResponseWriter, r *http.Request) {
	environments, err := api.argoCDClient.ListApplications()
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to list environments: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(environments)
}

const homeHTML = `<!DOCTYPE html>
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
    </style>
</head>
<body>
    <header>
        <img src="/static/meeseeks.jpeg" alt="">
        <div>
            <h1>Meeseeks</h1>
            <p>Ephemeral environments on Kubernetes, deployed through ArgoCD.</p>
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
</html>`

func (api *MeeseeksAPI) serveHome(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, homeHTML)
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

	if _, err := api.argoCDClient.CreateApplication(req); err != nil {
		htmxError(w, "Failed to create environment", err)
		return
	}

	w.Header().Set("HX-Trigger", "env-changed") // refresh the list
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	url := html.EscapeString(envURL(req.Name))
	fmt.Fprintf(w, `<div class="response success">Environment <strong>%s</strong> created. ArgoCD is syncing it; it will be at <a href="%s" target="_blank">%s</a></div>`,
		html.EscapeString(req.Name), url, url)
}

var listTmpl = template.Must(template.New("list").Funcs(template.FuncMap{"join": strings.Join}).Parse(`
{{- range .}}
<div class="env-item">
  <div>
    <div class="env-name"><a href="{{.URL}}" target="_blank">{{.Name}}</a></div>
    <div class="env-details">
      <span class="badge {{.Sync}}">{{or .Sync "Unknown"}}</span>
      <span class="badge {{.Status}}">{{or .Status "Unknown"}}</span>
      branch <code>{{.Branch}}</code>{{with .EnvType}} · {{.}}{{end}}{{with .Dependencies}} · {{join . ", "}}{{end}}
    </div>
  </div>
  <button class="delete-btn" hx-delete="/environments/{{.Name}}" hx-target="closest .env-item" hx-swap="outerHTML"
    hx-confirm="Delete environment {{.Name}}?">Delete</button>
</div>
{{- else}}
<div class="hint">No environments yet.</div>
{{- end}}`))

func (api *MeeseeksAPI) listEnvironmentsHTMX(w http.ResponseWriter, r *http.Request) {
	environments, err := api.argoCDClient.ListApplications()
	if err != nil {
		htmxError(w, "Failed to list environments", err)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := listTmpl.Execute(w, environments.Items); err != nil {
		log.Printf("render list: %v", err)
	}
}

// Mock ArgoCD client for development
type MockArgoCDClient struct{}

func (m *MockArgoCDClient) CreateApplication(req EnvironmentRequest) (string, error) {
	log.Printf("Mock: Creating environment %s", req.Name)
	return req.Name, nil
}

func (m *MockArgoCDClient) ListApplications() (EnvironmentList, error) {
	log.Printf("Mock: Listing applications")
	return EnvironmentList{
		Items: []EnvironmentItem{
			{ID: "test-env-1", Name: "test-env-1", Status: "Healthy", Sync: "Synced", Branch: "main", EnvType: "dev", URL: envURL("test-env-1")},
			{ID: "staging-app", Name: "staging-app", Status: "Progressing", Sync: "Synced", Branch: "main", EnvType: "staging", Dependencies: []string{"postgresql"}, URL: envURL("staging-app")},
			{ID: "demo-service", Name: "demo-service", Status: "Healthy", Sync: "Synced", Branch: "demo", EnvType: "dev", Dependencies: []string{"redis", "mongodb"}, URL: envURL("demo-service")},
		},
	}, nil
}

func (m *MockArgoCDClient) DeleteApplication(name string) error {
	log.Printf("Mock: Deleting application %s", name)
	return nil
}

func main() {
	argoCDURL := os.Getenv("ARGOCD_URL")
	if argoCDURL == "" {
		argoCDURL = "http://localhost:30080"
	}

	argoCDToken := os.Getenv("ARGOCD_TOKEN")

	var client ArgoCDClientInterface

	// Check if running in development mode
	if argoCDToken == "" || argoCDToken == "mock-token" || os.Getenv("DEV_MODE") == "true" {
		log.Println("🚀 Starting Meeseeks in Development Mode (Mock ArgoCD)")
		log.Println("💡 Running in mock mode - no real ArgoCD calls will be made")
		client = &MockArgoCDClient{}
	} else {
		client = NewArgoCDClient(argoCDURL, argoCDToken)
	}

	api := &MeeseeksAPI{argoCDClient: client}

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

	mux.HandleFunc("/environments/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		envID := r.URL.Path[len("/environments/"):]
		if envID == "" {
			http.Error(w, "Environment ID is required", http.StatusBadRequest)
			return
		}

		htmx := r.Header.Get("HX-Request") == "true"
		if err := api.argoCDClient.DeleteApplication(envID); err != nil {
			if htmx {
				htmxError(w, "Failed to delete environment", err)
			} else {
				http.Error(w, fmt.Sprintf("Failed to delete environment: %v", err), http.StatusInternalServerError)
			}
			return
		}

		if htmx {
			w.WriteHeader(http.StatusOK) // empty body: HTMX removes the row
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "22282"
	}

	log.Printf("Meeseeks API server starting on port %s", port)
	log.Printf("Frontend available at: http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
