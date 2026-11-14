package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Base manifests live in this repo: deploy/base is the app, deploy/deps/<name> each dependency.
const manifestsRepo = "https://github.com/ezeql/meeseeks"

// rootApp is the app-of-apps that syncs the client repo's environments/ directory.
const rootApp = "meeseeks-environments"

// dependency is a service an environment can add, from deploy/deps/<name>.
type dependency struct {
	label     string
	image     string   // image repository, matched by the kustomize image override
	tagSuffix string   // e.g. "-alpine"
	versions  []string // allowed, oldest first; the newest is the default
	envName   string   // injected into the app so it can reach the dependency
	envValue  string
}

func (d dependency) defaultVersion() string { return d.versions[len(d.versions)-1] }

// imageOverride is a kustomize image override, e.g. "postgres=postgres:17-alpine".
func (d dependency) imageOverride(version string) string {
	return d.image + "=" + d.image + ":" + version + d.tagSuffix
}

var dependencies = map[string]dependency{
	"postgresql": {"PostgreSQL", "postgres", "-alpine", []string{"15", "16", "17", "18"},
		"DATABASE_URL", "postgres://postgres:meeseeks@postgresql:5432/app"},
	"redis":   {"Redis", "redis", "-alpine", []string{"7", "8"}, "REDIS_URL", "redis://redis:6379"},
	"mongodb": {"MongoDB", "mongo", "", []string{"7", "8"}, "MONGODB_URL", "mongodb://mongodb:27017"},
}

// benchJob is the pgbench Job in deploy/bench/pgbench.
const benchJob = "pgbench"

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
	Recurse bool `json:"recurse,omitempty"`
}

type ArgoCDKustomize struct {
	Images   []string               `json:"images,omitempty"`
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
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Status       string            `json:"status"` // ArgoCD health, or "Deleting"
	Sync         string            `json:"sync"`
	Message      string            `json:"message,omitempty"` // ArgoCD error, e.g. a branch that doesn't exist
	Branch       string            `json:"branch"`
	EnvType      string            `json:"env_type"`
	Dependencies []string          `json:"dependencies"`
	Versions     map[string]string `json:"versions,omitempty"`
	Group        string            `json:"group,omitempty"`
	Benchmark    *BenchResult      `json:"benchmark,omitempty"` // nil when no pgbench was requested
	URL          string            `json:"url"`
	GitURL       string            `json:"git_url,omitempty"`
}

// DepsLabel renders "postgresql 17, redis 8" for the UI.
func (e EnvironmentItem) DepsLabel() string {
	var out []string
	for _, d := range e.Dependencies {
		if v := e.Versions[d]; v != "" {
			d += " " + v
		}
		out = append(out, d)
	}
	return strings.Join(out, ", ")
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

// JobLogs returns the logs of a Job's pods, read through ArgoCD, so Meeseeks needs no
// cluster credentials. ArgoCD streams one JSON object per log line.
func (c *ArgoCDClient) JobLogs(app, namespace, job string) (string, error) {
	q := url.Values{"namespace": {namespace}, "kind": {"Job"}, "group": {"batch"},
		"resourceName": {job}, "follow": {"false"}, "tailLines": {"50"}}
	req, err := http.NewRequest("GET", c.baseURL+"/api/v1/applications/"+app+"/logs?"+q.Encode(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", apiError(resp)
	}

	var sb strings.Builder
	dec := json.NewDecoder(resp.Body)
	for {
		var line struct {
			Result struct {
				Content string `json:"content"`
			} `json:"result"`
		}
		if err := dec.Decode(&line); err == io.EOF {
			return sb.String(), nil
		} else if err != nil {
			return sb.String(), err
		}
		sb.WriteString(line.Result.Content + "\n")
	}
}

// ListApplications returns the environments that came from the client repo.
func (c *ArgoCDClient) ListApplications() (EnvironmentList, error) {
	type rawSource struct {
		TargetRevision string `json:"targetRevision"`
		Path           string `json:"path"`
		Kustomize      struct {
			Images []string `json:"images"`
		} `json:"kustomize"`
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
			Group:   app.Metadata.Labels["meeseeks/group"],
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
			if s.Path == "deploy/bench/"+benchJob {
				env.Benchmark = &BenchResult{}
			}
			dep, ok := strings.CutPrefix(s.Path, "deploy/deps/")
			if !ok {
				continue
			}
			env.Dependencies = append(env.Dependencies, dep)
			for _, img := range s.Kustomize.Images { // "postgres=postgres:17-alpine" → "17"
				if _, tag, ok := strings.Cut(img[strings.LastIndex(img, "=")+1:], ":"); ok {
					if env.Versions == nil {
						env.Versions = map[string]string{}
					}
					env.Versions[dep] = strings.TrimSuffix(tag, dependencies[dep].tagSuffix)
				}
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
				Directory:      &ArgoCDDirectory{Recurse: true}, // groups live in subdirectories; README.md is ignored
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
		version := req.Versions[dep]
		if version == "" {
			version = dependencies[dep].defaultVersion()
		}
		sources = append(sources, ArgoCDApplicationSource{
			RepoURL:        manifestsRepo,
			TargetRevision: req.Branch,
			Path:           "deploy/deps/" + dep,
			Kustomize:      &ArgoCDKustomize{Images: []string{dependencies[dep].imageOverride(version)}},
		})
	}
	if req.Benchmark {
		sources = append(sources, ArgoCDApplicationSource{
			RepoURL:        manifestsRepo,
			TargetRevision: req.Branch,
			Path:           "deploy/bench/" + benchJob,
		})
	}

	labels := map[string]string{
		"managed-by":      "meeseeks",
		"meeseeks/gitops": "true",
		"env-type":        req.EnvType,
	}
	if req.Group != "" {
		labels["meeseeks/group"] = req.Group
	}

	return ArgoCDApplication{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "Application",
		Metadata: ArgoCDApplicationMetadata{
			Name:      req.Name,
			Namespace: "argocd",
			Labels:    labels,
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
		d := dependencies[dep]
		vars[d.envName] = d.envValue
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
