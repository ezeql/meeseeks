package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Manifests live in this repo: deploy/base is the app, deploy/deps/<name> each dependency.
const manifestsRepo = "https://github.com/ezeql/meeseeks"

// Supported dependencies and the env var injected into the app so it can reach each one.
var dependencyEnv = map[string][2]string{
	"postgresql": {"DATABASE_URL", "postgres://postgres:meeseeks@postgresql:5432/app"},
	"redis":      {"REDIS_URL", "redis://redis:6379"},
	"mongodb":    {"MONGODB_URL", "mongodb://mongodb:27017"},
}

// envURL is the environment's in-cluster Service. OrbStack makes *.svc.cluster.local
// reachable from the Mac; on other clusters use kubectl port-forward.
func envURL(name string) string {
	return "http://app.env-" + name + ".svc.cluster.local"
}

type ArgoCDClient struct {
	baseURL string
	token   string
	client  *http.Client
}

type ArgoCDApplication struct {
	APIVersion string                    `json:"apiVersion"`
	Kind       string                    `json:"kind"`
	Metadata   ArgoCDApplicationMetadata `json:"metadata"`
	Spec       ArgoCDApplicationSpec     `json:"spec"`
}

type ArgoCDApplicationMetadata struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type ArgoCDApplicationSpec struct {
	Project     string                    `json:"project"`
	Sources     []ArgoCDApplicationSource `json:"sources"`
	Destination ArgoCDDestination         `json:"destination"`
	SyncPolicy  *ArgoCDSyncPolicy         `json:"syncPolicy,omitempty"`
}

type ArgoCDApplicationSource struct {
	RepoURL        string           `json:"repoURL"`
	TargetRevision string           `json:"targetRevision"`
	Path           string           `json:"path"`
	Kustomize      *ArgoCDKustomize `json:"kustomize,omitempty"`
}

type ArgoCDKustomize struct {
	Replicas []ArgoCDReplica        `json:"replicas,omitempty"`
	Patches  []ArgoCDKustomizePatch `json:"patches,omitempty"`
}

type ArgoCDReplica struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type ArgoCDKustomizePatch struct {
	Patch  string       `json:"patch"`
	Target ArgoCDTarget `json:"target"`
}

type ArgoCDTarget struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

type ArgoCDDestination struct {
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
}

type ArgoCDSyncPolicy struct {
	Automated   *ArgoCDAutomatedSync `json:"automated,omitempty"`
	SyncOptions []string             `json:"syncOptions,omitempty"`
}

type ArgoCDAutomatedSync struct {
	SelfHeal bool `json:"selfHeal"`
	Prune    bool `json:"prune"`
}

type EnvironmentList struct {
	Items []EnvironmentItem `json:"items"`
}

type EnvironmentItem struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"` // ArgoCD health, or "Deleting"
	Sync         string   `json:"sync"`
	Branch       string   `json:"branch"`
	EnvType      string   `json:"env_type"`
	Dependencies []string `json:"dependencies"`
	URL          string   `json:"url"`
}

// apiError includes ArgoCD's response body so the real reason isn't lost.
func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("ArgoCD API returned status %d: %s", resp.StatusCode, bytes.TrimSpace(body))
}

func NewArgoCDClient(baseURL, token string) *ArgoCDClient {
	return &ArgoCDClient{
		baseURL: baseURL,
		token:   token,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *ArgoCDClient) CreateApplication(req EnvironmentRequest) (string, error) {
	app := c.buildApplication(req)

	appJSON, err := json.Marshal(app)
	if err != nil {
		return "", fmt.Errorf("failed to marshal application: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.baseURL+"/api/v1/applications", bytes.NewBuffer(appJSON))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to create application: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return "", apiError(resp)
	}

	return req.Name, nil
}

func (c *ArgoCDClient) ListApplications() (EnvironmentList, error) {
	httpReq, err := http.NewRequest("GET", c.baseURL+"/api/v1/applications", nil)
	if err != nil {
		return EnvironmentList{}, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return EnvironmentList{}, fmt.Errorf("failed to list applications: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return EnvironmentList{}, apiError(resp)
	}

	type rawSource struct {
		TargetRevision string `json:"targetRevision"`
		Path           string `json:"path"`
	}
	var rawApps struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				Labels            map[string]string `json:"labels"`
				DeletionTimestamp string            `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Source  *rawSource  `json:"source"`
				Sources []rawSource `json:"sources"`
			} `json:"spec"`
			Status struct {
				Sync struct {
					Status string `json:"status"`
				} `json:"sync"`
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawApps); err != nil {
		return EnvironmentList{}, fmt.Errorf("failed to decode response: %w", err)
	}

	environments := []EnvironmentItem{}
	for _, app := range rawApps.Items {
		if app.Metadata.Labels["managed-by"] != "meeseeks" {
			continue
		}
		sources := app.Spec.Sources
		if app.Spec.Source != nil { // apps created before multi-source
			sources = append([]rawSource{*app.Spec.Source}, sources...)
		}
		env := EnvironmentItem{
			ID:      app.Metadata.Name,
			Name:    app.Metadata.Name,
			Status:  app.Status.Health.Status,
			Sync:    app.Status.Sync.Status,
			EnvType: app.Metadata.Labels["env-type"],
			URL:     envURL(app.Metadata.Name),
		}
		if app.Metadata.DeletionTimestamp != "" {
			env.Status = "Deleting"
		}
		if len(sources) > 0 {
			env.Branch = sources[0].TargetRevision
		}
		for _, s := range sources {
			if dep, ok := strings.CutPrefix(s.Path, "deploy/deps/"); ok {
				env.Dependencies = append(env.Dependencies, dep)
			}
		}
		environments = append(environments, env)
	}

	return EnvironmentList{Items: environments}, nil
}

func (c *ArgoCDClient) DeleteApplication(name string) error {
	httpReq, err := http.NewRequest("DELETE", c.baseURL+"/api/v1/applications/"+name, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.token)

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to delete application: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return apiError(resp)
	}

	return nil
}

// buildApplication makes one multi-source Application: the app (kustomize, customized
// per request) plus a plain directory source per dependency, all from the same branch.
func (c *ArgoCDClient) buildApplication(req EnvironmentRequest) ArgoCDApplication {
	sources := []ArgoCDApplicationSource{{
		RepoURL:        manifestsRepo,
		TargetRevision: req.Branch,
		Path:           "deploy/base",
		Kustomize:      buildKustomize(req),
	}}
	for _, dep := range req.Dependencies {
		sources = append(sources, ArgoCDApplicationSource{
			RepoURL:        manifestsRepo,
			TargetRevision: req.Branch,
			Path:           "deploy/deps/" + dep,
		})
	}

	return ArgoCDApplication{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: ArgoCDApplicationMetadata{
			Name:      req.Name,
			Namespace: "argocd",
			Labels: map[string]string{
				"managed-by": "meeseeks",
				"env-type":   req.EnvType,
			},
		},
		Spec: ArgoCDApplicationSpec{
			Project: "default",
			Sources: sources,
			Destination: ArgoCDDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: fmt.Sprintf("env-%s", req.Name),
			},
			SyncPolicy: &ArgoCDSyncPolicy{
				Automated: &ArgoCDAutomatedSync{
					SelfHeal: true,
					Prune:    true,
				},
				SyncOptions: []string{
					"CreateNamespace=true",
					"PrunePropagationPolicy=foreground",
					"PruneLast=true",
				},
			},
		},
	}
}

// buildKustomize turns the request into ArgoCD kustomize options: a replica count and
// one strategic-merge patch on Deployment "app" for resources and env vars.
// Strategic merge (unlike JSON patch) works whether or not the base sets those fields.
func buildKustomize(req EnvironmentRequest) *ArgoCDKustomize {
	k := &ArgoCDKustomize{}
	if req.Replicas > 0 {
		k.Replicas = []ArgoCDReplica{{Name: "app", Count: req.Replicas}}
	}

	container := map[string]any{"name": "app"}

	resources := map[string]string{}
	if req.CPU != "" {
		resources["cpu"] = req.CPU
	}
	if req.Memory != "" {
		resources["memory"] = req.Memory
	}
	if len(resources) > 0 {
		container["resources"] = map[string]any{"requests": resources, "limits": resources}
	}

	vars := map[string]string{}
	for _, dep := range req.Dependencies {
		kv := dependencyEnv[dep]
		vars[kv[0]] = kv[1]
	}
	maps.Copy(vars, req.EnvVars) // user values override dependency defaults
	if len(vars) > 0 {
		var env []map[string]string
		for _, name := range slices.Sorted(maps.Keys(vars)) {
			env = append(env, map[string]string{"name": name, "value": vars[name]})
		}
		container["env"] = env
	}

	if len(container) > 1 {
		patch, _ := json.Marshal(map[string]any{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata":   map[string]string{"name": "app"},
			"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
				"containers": []any{container},
			}}},
		})
		k.Patches = []ArgoCDKustomizePatch{{
			Patch:  string(patch),
			Target: ArgoCDTarget{Kind: "Deployment", Name: "app"},
		}}
	}

	if k.Replicas == nil && k.Patches == nil {
		return nil
	}
	return k
}
