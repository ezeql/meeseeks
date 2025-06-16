package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

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
	Project     string                  `json:"project"`
	Source      ArgoCDApplicationSource `json:"source"`
	Destination ArgoCDDestination       `json:"destination"`
	SyncPolicy  *ArgoCDSyncPolicy       `json:"syncPolicy,omitempty"`
}

type ArgoCDApplicationSource struct {
	RepoURL        string                 `json:"repoURL"`
	TargetRevision string                 `json:"targetRevision"`
	Path           string                 `json:"path"`
	Kustomize      *ArgoCDKustomizeConfig `json:"kustomize,omitempty"`
	Directory      *ArgoCDDirectory       `json:"directory,omitempty"`
}

type ArgoCDDirectory struct {
	Recurse bool `json:"recurse,omitempty"`
}

type ArgoCDKustomizeConfig struct {
	Images          []string               `json:"images,omitempty"`
	Patches         []ArgoCDKustomizePatch `json:"patches,omitempty"`
	PatchesJSON6902 []ArgoCDJsonPatch      `json:"patchesJson6902,omitempty"`
}

type ArgoCDKustomizePatch struct {
	Patch  string `json:"patch"`
	Target struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"target"`
}

type ArgoCDJsonPatch struct {
	Target struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"target"`
	Patch string `json:"patch"`
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
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	URL    string `json:"url"`
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
		return "", fmt.Errorf("ArgoCD API returned status %d", resp.StatusCode)
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
		return EnvironmentList{}, fmt.Errorf("ArgoCD API returned status %d", resp.StatusCode)
	}

	var rawApps struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&rawApps); err != nil {
		return EnvironmentList{}, fmt.Errorf("failed to decode response: %w", err)
	}

	var environments []EnvironmentItem
	for _, app := range rawApps.Items {
		if app.Metadata.Labels["managed-by"] == "meeseeks" {
			environments = append(environments, EnvironmentItem{
				ID:     app.Metadata.Name,
				Name:   app.Metadata.Name,
				Status: app.Status.Health.Status,
				URL:    fmt.Sprintf("https://%s.dev.example.com", app.Metadata.Name),
			})
		}
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
		return fmt.Errorf("ArgoCD API returned status %d", resp.StatusCode)
	}

	return nil
}

func (c *ArgoCDClient) buildApplication(req EnvironmentRequest) ArgoCDApplication {
	app := ArgoCDApplication{
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
			Source: ArgoCDApplicationSource{
				RepoURL:        "https://github.com/mateothegreat/k8-byexamples-nginx",
				TargetRevision: "master",
				Path:           "manifests",
			},
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
				},
			},
		},
	}

	return app
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func defaultIfEmpty(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func (c *ArgoCDClient) buildKustomizeConfig(req EnvironmentRequest) *ArgoCDKustomizeConfig {
	config := &ArgoCDKustomizeConfig{
		Images: []string{
			fmt.Sprintf("app=your-app:%s", req.Branch),
		},
	}

	if req.CPU != "" || req.Memory != "" || req.Replicas > 0 {
		resourcePatch := c.buildResourcePatch(req)
		config.Patches = append(config.Patches, resourcePatch)
	}

	for _, dep := range req.Dependencies {
		depPatch := c.buildDependencyPatch(dep)
		config.Patches = append(config.Patches, depPatch)
	}

	if len(req.EnvVars) > 0 {
		envPatch := c.buildEnvVarsPatch(req.EnvVars)
		config.Patches = append(config.Patches, envPatch)
	}

	return config
}

func (c *ArgoCDClient) buildResourcePatch(req EnvironmentRequest) ArgoCDKustomizePatch {
	patch := `
- op: replace
  path: /spec/template/spec/containers/0/resources
  value:
    requests:`

	if req.CPU != "" {
		patch += fmt.Sprintf(`
      cpu: "%s"`, req.CPU)
	}
	if req.Memory != "" {
		patch += fmt.Sprintf(`
      memory: "%s"`, req.Memory)
	}

	patch += `
    limits:`

	if req.CPU != "" {
		patch += fmt.Sprintf(`
      cpu: "%s"`, req.CPU)
	}
	if req.Memory != "" {
		patch += fmt.Sprintf(`
      memory: "%s"`, req.Memory)
	}

	if req.Replicas > 0 {
		patch += fmt.Sprintf(`
- op: replace
  path: /spec/replicas
  value: %d`, req.Replicas)
	}

	return ArgoCDKustomizePatch{
		Patch: patch,
		Target: struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}{
			Kind: "Deployment",
			Name: "app",
		},
	}
}

func (c *ArgoCDClient) buildDependencyPatch(dependency string) ArgoCDKustomizePatch {
	var patch string

	switch dependency {
	case "postgresql":
		patch = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgresql
spec:
  replicas: 1
  selector:
    matchLabels:
      app: postgresql
  template:
    metadata:
      labels:
        app: postgresql
    spec:
      containers:
      - name: postgresql
        image: postgres:14
        env:
        - name: POSTGRES_DB
          value: myapp
        - name: POSTGRES_USER
          value: user
        - name: POSTGRES_PASSWORD
          value: password
        ports:
        - containerPort: 5432`
	case "redis":
		patch = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
spec:
  replicas: 1
  selector:
    matchLabels:
      app: redis
  template:
    metadata:
      labels:
        app: redis
    spec:
      containers:
      - name: redis
        image: redis:7
        ports:
        - containerPort: 6379`
	case "mongodb":
		patch = `
apiVersion: apps/v1
kind: Deployment
metadata:
  name: mongodb
spec:
  replicas: 1
  selector:
    matchLabels:
      app: mongodb
  template:
    metadata:
      labels:
        app: mongodb
    spec:
      containers:
      - name: mongodb
        image: mongo:6
        env:
        - name: MONGO_INITDB_ROOT_USERNAME
          value: root
        - name: MONGO_INITDB_ROOT_PASSWORD
          value: password
        ports:
        - containerPort: 27017`
	}

	return ArgoCDKustomizePatch{
		Patch: patch,
		Target: struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}{
			Kind: "Deployment",
			Name: dependency,
		},
	}
}

func (c *ArgoCDClient) buildEnvVarsPatch(envVars map[string]string) ArgoCDKustomizePatch {
	patch := `
- op: add
  path: /spec/template/spec/containers/0/env
  value:`

	for key, value := range envVars {
		patch += fmt.Sprintf(`
  - name: %s
    value: "%s"`, key, value)
	}

	return ArgoCDKustomizePatch{
		Patch: patch,
		Target: struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		}{
			Kind: "Deployment",
			Name: "app",
		},
	}
}
