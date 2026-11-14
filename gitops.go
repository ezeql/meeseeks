package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

var (
	errExists   = errors.New("already exists")
	errNotFound = errors.New("not found")
)

// GitHubRepo writes files to the client repo, one commit per call.
// ponytail: GitHub Contents API only, one file per commit. For GitLab/Gitea or
// multi-file commits, put a git library behind CreateFile/DeleteFile.
type GitHubRepo struct {
	API    string // https://api.github.com; tests point it at a fake
	Owner  string
	Name   string
	Branch string
	token  string
	client *http.Client
}

// NewGitHubRepo parses https://github.com/<owner>/<name>[.git].
func NewGitHubRepo(repoURL, branch, token string) (*GitHubRepo, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host != "github.com" {
		return nil, fmt.Errorf("GITOPS_REPO must look like https://github.com/<owner>/<repo>, got %q", repoURL)
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("GITOPS_REPO must look like https://github.com/<owner>/<repo>, got %q", repoURL)
	}
	return &GitHubRepo{
		API: "https://api.github.com", Owner: parts[0], Name: parts[1], Branch: branch,
		token: token, client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (g *GitHubRepo) URL() string { return "https://github.com/" + g.Owner + "/" + g.Name }

func (g *GitHubRepo) FileURL(path string) string { return g.URL() + "/blob/" + g.Branch + "/" + path }

type commitResponse struct {
	Commit struct {
		HTMLURL string `json:"html_url"`
	} `json:"commit"`
}

// CreateFile commits a new file and returns the commit URL. errExists if it's there already.
func (g *GitHubRepo) CreateFile(path string, content []byte, message string) (string, error) {
	var out commitResponse
	err := g.do("PUT", path, map[string]string{
		"message": message, "branch": g.Branch, "content": base64.StdEncoding.EncodeToString(content),
	}, &out)
	return out.Commit.HTMLURL, err
}

// DeleteFile commits the removal of a file and returns the commit URL.
func (g *GitHubRepo) DeleteFile(path, message string) (string, error) {
	var file struct {
		SHA string `json:"sha"`
	}
	if err := g.do("GET", path+"?ref="+url.QueryEscape(g.Branch), nil, &file); err != nil {
		return "", err
	}
	var out commitResponse
	err := g.do("DELETE", path, map[string]string{"message": message, "branch": g.Branch, "sha": file.SHA}, &out)
	return out.Commit.HTMLURL, err
}

func (g *GitHubRepo) do(method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%s/repos/%s/%s/contents/%s", g.API, g.Owner, g.Name, path), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		switch {
		case resp.StatusCode == http.StatusUnprocessableEntity && bytes.Contains(msg, []byte(`\"sha\"`)):
			return errExists // PUT without sha: the file is already there
		case resp.StatusCode == http.StatusNotFound:
			return errNotFound
		}
		return fmt.Errorf("GitHub %s %s returned %d: %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GitOps is the real backend: desired state is written to the client repo,
// ArgoCD reconciles it, and status is read back from ArgoCD.
type GitOps struct {
	repo *GitHubRepo
	argo *ArgoCDClient
	mu   sync.Mutex // ponytail: one writer at a time; concurrent commits to one branch can conflict
}

func envPath(name string) string { return "environments/" + name + ".json" }

const environmentsReadme = `# Environments

One ArgoCD Application per file, written by Meeseeks. ArgoCD syncs this directory
through the root app ` + "`" + rootApp + "`" + ` (app-of-apps).

This README also keeps the directory in Git when there are no environments.
`

// Bootstrap makes the client repo and ArgoCD ready. It is idempotent, so it runs on every start.
func (g *GitOps) Bootstrap() error {
	// Git drops empty directories; without this file, deleting the last environment
	// would remove environments/ and break the root app.
	if _, err := g.repo.CreateFile("environments/README.md", []byte(environmentsReadme), "Add environments directory"); err != nil && !errors.Is(err, errExists) {
		return fmt.Errorf("prepare client repo: %w", err)
	}
	if err := g.argo.UpsertRepo(g.repo.URL(), "meeseeks", g.repo.token); err != nil {
		return fmt.Errorf("register client repo in ArgoCD: %w", err)
	}
	if err := g.argo.UpsertApplication(buildRootApp(g.repo.URL(), g.repo.Branch)); err != nil {
		return fmt.Errorf("create root app: %w", err)
	}
	return nil
}

func (g *GitOps) Create(req EnvironmentRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	// An existing app with this name (made outside the client repo, or the root app)
	// would be adopted by the root app, and a later delete would cascade into it.
	if req.Name == rootApp || g.argo.Exists(req.Name) {
		return "", fmt.Errorf("name %q is already used by an ArgoCD application", req.Name)
	}

	content, _ := json.MarshalIndent(buildApplication(req), "", "  ")
	msg := fmt.Sprintf("Create environment %s\n\nbranch: %s, type: %s, dependencies: %s",
		req.Name, req.Branch, req.EnvType, strings.Join(req.Dependencies, ", "))
	commit, err := g.repo.CreateFile(envPath(req.Name), append(content, '\n'), msg)
	if errors.Is(err, errExists) {
		return "", fmt.Errorf("environment %q already exists in %s", req.Name, g.repo.URL())
	}
	if err != nil {
		return "", err
	}
	g.refresh()
	return commit, nil
}

func (g *GitOps) Delete(name string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	commit, err := g.repo.DeleteFile(envPath(name), "Delete environment "+name)
	if errors.Is(err, errNotFound) {
		return "", fmt.Errorf("environment %q not found in %s", name, g.repo.URL())
	}
	if err != nil {
		return "", err
	}
	g.refresh()
	return commit, nil
}

func (g *GitOps) List() (EnvironmentList, error) {
	list, err := g.argo.ListApplications()
	for i := range list.Items {
		list.Items[i].GitURL = g.repo.FileURL(envPath(list.Items[i].Name))
	}
	return list, err
}

func (g *GitOps) RepoURL() string { return g.repo.URL() }

// refresh is best effort: if it fails, ArgoCD still picks the commit up on its next poll.
func (g *GitOps) refresh() {
	if err := g.argo.Refresh(rootApp); err != nil {
		log.Printf("refresh %s: %v", rootApp, err)
	}
}
