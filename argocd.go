package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"
)

// Base manifests live in this repo: deploy/base is the app, deploy/deps/<name> each dependency.
const manifestsRepo = "https://github.com/ezeql/meeseeks"

// rootApp is the app-of-apps that syncs the client repo's environments/ directory.
const rootApp = "meeseeks-environments"

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
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Labels     map[string]string `json:"labels"`
	Finalizers []string          `json:"finalizers,omitempty"`
}

type ArgoCDApplicationSpec struct {
	Project     string                    `json:"project"`
	Source      *ArgoCDApplicationSource  `json:"source,omitempty"`
	Sources     []ArgoCDApplicationSource `json:"sources,omitempty"`
	Destination ArgoCDDestination         `json:"destination"`
	SyncPolicy  *ArgoCDSyncPolicy         `json:"syncPolicy,omitempty"`
}

type ArgoCDApplicationSource struct {
	RepoURL        string           `json:"repoURL"`
	TargetRevision string           `json:"targetRevision"`
	Path           string           `json:"path"`
	Kustomize      *ArgoCDKustomize `json:"kustomize,omitempty"`
	Directory      *ArgoCDDirectory `json:"directory,omitempty"`
}

type ArgoCDDirectory struct {
	Include string `json:"include,omitempty"`
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
	SelfHeal   bool `json:"selfHeal"`
	Prune      bool `json:"prune"`
	AllowEmpty bool `json:"allowEmpty,omitempty"`
}

type EnvironmentList struct {
	Items []EnvironmentItem `json:"items"`
}

type EnvironmentItem struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Status       string   `json:"status"` // ArgoCD health, or "Deleting"
	Sync         string   `json:"sync"`
	Message      string   `json:"message,omitempty"` // ArgoCD error, e.g. a branch that doesn't exist
	Branch       string   `json:"branch"`
	EnvType      string   `json:"env_type"`
	Dependencies []string `json:"dependencies"`
	URL          string   `json:"url"`
	GitURL       string   `json:"git_url,omitempty"`
}

// argoError keeps the status code and ArgoCD's response body so the real reason isn't lost.
type argoError struct {
	Status int
	Body   string
}

func (e *argoError) Error() string {
	return fmt.Sprintf("ArgoCD API returned status %d: %s", e.Status, e.Body)
}

func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return &argoError{Status: resp.StatusCode, Body: string(bytes.TrimSpace(body))}
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

// call sends in (if any) as JSON and decodes the response into out (if any).
func (c *ArgoCDClient) call(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("ArgoCD %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Exists reports whether ArgoCD has an Application with this name. ArgoCD answers 403
// rather than 404 for missing apps. Any other failure is an error, never "free".
func (c *ArgoCDClient) Exists(name string) (bool, error) {
	err := c.call("GET", "/api/v1/applications/"+name, nil, nil)
	var ae *argoError
	if errors.As(err, &ae) && (ae.Status == http.StatusForbidden || ae.Status == http.StatusNotFound) {
		return false, nil
	}
	return err == nil, err
}

// Refresh makes ArgoCD re-read Git now instead of on its next poll (~3 min).
func (c *ArgoCDClient) Refresh(name string) error {
	return c.call("GET", "/api/v1/applications/"+name+"?refresh=normal", nil, nil)
}

// UpsertRepo registers a private Git repo's credentials in ArgoCD.
func (c *ArgoCDClient) UpsertRepo(repoURL, username, password string) error {
	return c.call("POST", "/api/v1/repositories?upsert=true", map[string]string{
		"type": "git", "repo": repoURL, "username": username, "password": password,
	}, nil)
}

// UpsertApplication creates or updates an Application. validate=false because the
// root app's path may not exist in Git yet.
func (c *ArgoCDClient) UpsertApplication(app ArgoCDApplication) error {
	return c.call("POST", "/api/v1/applications?upsert=true&validate=false", app, nil)
}

// ListApplications returns the environments that came from the client repo.
func (c *ArgoCDClient) ListApplications() (EnvironmentList, error) {
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
				Sources []rawSource `json:"sources"`
			} `json:"spec"`
			Status struct {
				Sync struct {
					Status string `json:"status"`
				} `json:"sync"`
				Health struct {
					Status string `json:"status"`
				} `json:"health"`
				Conditions []struct {
					Message string `json:"message"`
				} `json:"conditions"`
				OperationState struct {
					Phase   string `json:"phase"`
					Message string `json:"message"`
				} `json:"operationState"`
			} `json:"status"`
		} `json:"items"`
	}

	if err := c.call("GET", "/api/v1/applications", nil, &rawApps); err != nil {
		return EnvironmentList{}, err
	}

	environments := []EnvironmentItem{}
	for _, app := range rawApps.Items {
		if app.Metadata.Labels["meeseeks/gitops"] != "true" {
			continue
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
		if len(app.Status.Conditions) > 0 {
			env.Message = app.Status.Conditions[0].Message
		} else if p := app.Status.OperationState.Phase; p == "Failed" || p == "Error" {
			env.Message = app.Status.OperationState.Message
		}
		if len(app.Spec.Sources) > 0 {
			env.Branch = app.Spec.Sources[0].TargetRevision
		}
		for _, s := range app.Spec.Sources {
			if dep, ok := strings.CutPrefix(s.Path, "deploy/deps/"); ok {
				env.Dependencies = append(env.Dependencies, dep)
			}
		}
		environments = append(environments, env)
	}

	return EnvironmentList{Items: environments}, nil
}

// buildRootApp is the app-of-apps: it syncs every environments/*.json from the client
// repo into the argocd namespace. allowEmpty lets it prune the last environment;
// without it ArgoCD refuses an auto-sync that would leave zero resources.
func buildRootApp(repoURL, branch string) ArgoCDApplication {
	return ArgoCDApplication{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: ArgoCDApplicationMetadata{
			Name:      rootApp,
			Namespace: "argocd",
			Labels:    map[string]string{"managed-by": "meeseeks"},
		},
		Spec: ArgoCDApplicationSpec{
			Project: "default",
			Source: &ArgoCDApplicationSource{
				RepoURL:        repoURL,
				TargetRevision: branch,
				Path:           "environments",
				Directory:      &ArgoCDDirectory{Include: "*.json"},
			},
			Destination: ArgoCDDestination{
				Server:    "https://kubernetes.default.svc",
				Namespace: "argocd",
			},
			SyncPolicy: &ArgoCDSyncPolicy{
				Automated: &ArgoCDAutomatedSync{SelfHeal: true, Prune: true, AllowEmpty: true},
			},
		},
	}
}

// buildApplication is the environment as it gets committed to the client repo: one
// multi-source Application with the app (kustomize, customized per request) plus a
// plain directory source per dependency, all from the same branch.
func buildApplication(req EnvironmentRequest) ArgoCDApplication {
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
				"managed-by":      "meeseeks",
				"meeseeks/gitops": "true",
				"env-type":        req.EnvType,
			},
			// Removing the file from Git prunes this Application; the finalizer makes
			// ArgoCD delete its workloads too instead of orphaning them.
			Finalizers: []string{"resources-finalizer.argocd.argoproj.io"},
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
