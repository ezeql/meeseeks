package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
)

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

	response := EnvironmentResponse{
		ID:     envID,
		Status: "creating",
		URL:    fmt.Sprintf("https://%s.dev.example.com", req.Name),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
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

func (api *MeeseeksAPI) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	envID := r.URL.Path[len("/environments/"):]
	if envID == "" {
		http.Error(w, "Environment ID is required", http.StatusBadRequest)
		return
	}

	if err := api.argoCDClient.DeleteApplication(envID); err != nil {
		http.Error(w, fmt.Sprintf("Failed to delete environment: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (api *MeeseeksAPI) serveHome(w http.ResponseWriter, r *http.Request) {
	tmpl := `<!DOCTYPE html>
<html>
<head>
    <title>Meeseeks - Environment Manager</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <script src="https://unpkg.com/htmx.org@1.9.10"></script>
    <style>
        body { 
            font-family: system-ui, -apple-system, sans-serif; 
            max-width: 1200px; 
            margin: 0 auto; 
            padding: 20px;
            background: #f5f5f5;
        }
        .container { 
            background: white; 
            padding: 30px; 
            border-radius: 8px; 
            box-shadow: 0 2px 10px rgba(0,0,0,0.1);
        }
        h1 { 
            color: #333; 
            margin-bottom: 30px;
            text-align: center;
        }
        .form-group { 
            margin-bottom: 15px; 
        }
        label { 
            display: block; 
            margin-bottom: 5px; 
            font-weight: 500;
        }
        input, select, textarea { 
            width: 100%; 
            padding: 10px; 
            border: 1px solid #ddd; 
            border-radius: 4px;
            font-size: 14px;
        }
        button { 
            background: #007bff; 
            color: white; 
            padding: 12px 24px; 
            border: none; 
            border-radius: 4px; 
            cursor: pointer;
            font-size: 14px;
            margin-right: 10px;
        }
        button:hover { 
            background: #0056b3; 
        }
        .delete-btn { 
            background: #dc3545; 
            padding: 6px 12px;
            font-size: 12px;
        }
        .delete-btn:hover { 
            background: #c82333; 
        }
        .environments { 
            margin-top: 30px; 
        }
        .env-item { 
            background: #f8f9fa; 
            padding: 15px; 
            margin-bottom: 10px; 
            border-radius: 4px;
            border-left: 4px solid #007bff;
        }
        .env-header { 
            display: flex; 
            justify-content: space-between; 
            align-items: center;
        }
        .env-name { 
            font-weight: bold; 
            font-size: 16px;
        }
        .env-details { 
            color: #666; 
            font-size: 14px;
            margin-top: 5px;
        }
        .form-row { 
            display: grid; 
            grid-template-columns: 1fr 1fr; 
            gap: 15px;
        }
        .response { 
            margin-top: 20px; 
            padding: 15px; 
            border-radius: 4px;
        }
        .success { 
            background: #d4edda; 
            color: #155724; 
            border: 1px solid #c3e6cb;
        }
        .error { 
            background: #f8d7da; 
            color: #721c24; 
            border: 1px solid #f5c6cb;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>🧪 Meeseeks Environment Manager</h1>
        
        <form hx-post="/environments" hx-target="#response" hx-trigger="submit">
            <div class="form-row">
                <div class="form-group">
                    <label for="name">Environment Name:</label>
                    <input type="text" id="name" name="name" required>
                </div>
                <div class="form-group">
                    <label for="branch">Branch:</label>
                    <input type="text" id="branch" name="branch" required>
                </div>
            </div>
            
            <div class="form-row">
                <div class="form-group">
                    <label for="cpu">CPU:</label>
                    <input type="text" id="cpu" name="cpu" value="100m" required>
                </div>
                <div class="form-group">
                    <label for="memory">Memory:</label>
                    <input type="text" id="memory" name="memory" value="256Mi" required>
                </div>
            </div>
            
            <div class="form-row">
                <div class="form-group">
                    <label for="replicas">Replicas:</label>
                    <input type="number" id="replicas" name="replicas" value="1" min="1" required>
                </div>
                <div class="form-group">
                    <label for="env_type">Environment Type:</label>
                    <select id="env_type" name="env_type" required>
                        <option value="development">Development</option>
                        <option value="staging">Staging</option>
                        <option value="testing">Testing</option>
                    </select>
                </div>
            </div>
            
            <div class="form-group">
                <label for="dependencies">Dependencies (comma-separated):</label>
                <input type="text" id="dependencies" name="dependencies" placeholder="postgresql,redis">
            </div>
            
            <div class="form-group">
                <label for="env_vars">Environment Variables (JSON format):</label>
                <textarea id="env_vars" name="env_vars" rows="3" placeholder='{"KEY1": "value1", "KEY2": "value2"}'>{}</textarea>
            </div>
            
            <button type="submit">Create Environment</button>
            <button type="button" hx-get="/environments" hx-target="#environments">Refresh List</button>
        </form>
        
        <div id="response"></div>
        
        <div class="environments">
            <h2>Environments</h2>
            <div id="environments" hx-get="/environments" hx-trigger="load">
                Loading environments...
            </div>
        </div>
    </div>
</body>
</html>`

	t, err := template.New("home").Parse(tmpl)
	if err != nil {
		http.Error(w, "Template error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html")
	t.Execute(w, nil)
}

func (api *MeeseeksAPI) createEnvironmentHTMX(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="response error">Error parsing form: %v</div>`, err)
		return
	}

	// Parse dependencies
	var dependencies []string
	if deps := r.FormValue("dependencies"); deps != "" {
		for _, dep := range strings.Split(deps, ",") {
			if trimmed := strings.TrimSpace(dep); trimmed != "" {
				dependencies = append(dependencies, trimmed)
			}
		}
	}

	// Parse environment variables
	var envVars map[string]string
	envVarsStr := r.FormValue("env_vars")
	if envVarsStr == "" {
		envVarsStr = "{}"
	}
	if err := json.Unmarshal([]byte(envVarsStr), &envVars); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="response error">Invalid environment variables JSON: %v</div>`, err)
		return
	}

	req := EnvironmentRequest{
		Name:         r.FormValue("name"),
		Branch:       r.FormValue("branch"),
		CPU:          r.FormValue("cpu"),
		Memory:       r.FormValue("memory"),
		Replicas:     1,
		Dependencies: dependencies,
		EnvType:      r.FormValue("env_type"),
		EnvVars:      envVars,
	}

	// Parse replicas
	if replicas := r.FormValue("replicas"); replicas != "" {
		if r, err := strconv.Atoi(replicas); err == nil {
			req.Replicas = r
		}
	}

	if err := ValidateEnvironmentRequest(req); err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="response error">Validation error: %v</div>`, err)
		return
	}

	envID, err := api.argoCDClient.CreateApplication(req)
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="response error">Failed to create environment: %v</div>`, err)
		return
	}

	response := EnvironmentResponse{
		ID:     envID,
		Status: "creating",
		URL:    fmt.Sprintf("https://%s.dev.example.com", req.Name),
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, `<div class="response success">
		<strong>Environment Created!</strong><br>
		ID: %s<br>
		Status: %s<br>
		URL: <a href="%s" target="_blank">%s</a>
	</div>`, response.ID, response.Status, response.URL, response.URL)
}

func (api *MeeseeksAPI) listEnvironmentsHTMX(w http.ResponseWriter, r *http.Request) {
	environments, err := api.argoCDClient.ListApplications()
	if err != nil {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<div class="error">Failed to list environments: %v</div>`, err)
		return
	}

	w.Header().Set("Content-Type", "text/html")

	if len(environments.Items) == 0 {
		fmt.Fprint(w, `<div>No environments found.</div>`)
		return
	}

	for _, env := range environments.Items {
		fmt.Fprintf(w, `
		<div class="env-item">
			<div class="env-header">
				<div>
					<div class="env-name">%s</div>
					<div class="env-details">Status: %s</div>
				</div>
				<button class="delete-btn" 
					hx-delete="/environments/%s" 
					hx-target="closest .env-item"
					hx-confirm="Are you sure you want to delete this environment?">
					Delete
				</button>
			</div>
		</div>`, env.Name, env.Status, env.Name)
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
			{
				ID:     "test-env-1",
				Name:   "test-env-1",
				Status: "Healthy",
				URL:    "https://test-env-1.dev.example.com",
			},
			{
				ID:     "staging-app",
				Name:   "staging-app",
				Status: "Progressing",
				URL:    "https://staging-app.dev.example.com",
			},
			{
				ID:     "demo-service",
				Name:   "demo-service",
				Status: "Healthy",
				URL:    "https://demo-service.dev.example.com",
			},
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

	// API routes - existing JSON endpoints
	mux.HandleFunc("/environments", func(w http.ResponseWriter, r *http.Request) {
		// Check if request is from HTMX
		if r.Header.Get("HX-Request") == "true" {
			switch r.Method {
			case http.MethodPost:
				api.createEnvironmentHTMX(w, r)
			case http.MethodGet:
				api.listEnvironmentsHTMX(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		} else {
			// Original JSON API
			switch r.Method {
			case http.MethodPost:
				api.createEnvironment(w, r)
			case http.MethodGet:
				api.listEnvironments(w, r)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})

	mux.HandleFunc("/environments/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			envID := r.URL.Path[len("/environments/"):]
			if envID == "" {
				http.Error(w, "Environment ID is required", http.StatusBadRequest)
				return
			}

			if err := api.argoCDClient.DeleteApplication(envID); err != nil {
				if r.Header.Get("HX-Request") == "true" {
					w.Header().Set("Content-Type", "text/html")
					fmt.Fprintf(w, `<div class="error">Failed to delete environment: %v</div>`, err)
				} else {
					http.Error(w, fmt.Sprintf("Failed to delete environment: %v", err), http.StatusInternalServerError)
				}
				return
			}

			if r.Header.Get("HX-Request") == "true" {
				// Return empty content to remove the element from DOM
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNoContent)
			}
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
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
