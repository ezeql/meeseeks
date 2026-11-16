package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"maps"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHubRepo commits to the client repo through GitHub's Git Data API, so one request
// (one environment or a whole matrix group) is one atomic commit.
// ponytail: GitHub only. For GitLab/Gitea, put a git library behind snapshot/commit.
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

func (g *GitHubRepo) FileURL(p string) string { return g.URL() + "/blob/" + g.Branch + "/" + p }

// snapshot is the branch head and every file path in it.
type snapshot struct {
	head, tree string
	paths      []string
}

func (g *GitHubRepo) snapshot() (snapshot, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := g.do("GET", "git/ref/heads/"+g.Branch, nil, &ref); err != nil {
		return snapshot{}, err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := g.do("GET", "git/commits/"+ref.Object.SHA, nil, &commit); err != nil {
		return snapshot{}, err
	}
	// ponytail: ignores GitHub's "truncated" flag (>100k entries), far beyond an environments repo.
	var tree struct {
		Tree []struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := g.do("GET", "git/trees/"+commit.Tree.SHA+"?recursive=1", nil, &tree); err != nil {
		return snapshot{}, err
	}
	s := snapshot{head: ref.Object.SHA, tree: commit.Tree.SHA}
	for _, e := range tree.Tree {
		if e.Type == "blob" {
			s.paths = append(s.paths, e.Path)
		}
	}
	return s, nil
}

// read returns a file's content as of s.
func (g *GitHubRepo) read(s snapshot, p string) (string, error) {
	var file struct {
		Content string `json:"content"`
	}
	if err := g.do("GET", "contents/"+p+"?ref="+s.head, nil, &file); err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(file.Content, "\n", ""))
	return string(b), err
}

// commit writes changes (path → content, nil to delete) as one commit on top of s.
// The branch update is a fast-forward, so a concurrent commit makes this fail instead of being overwritten.
func (g *GitHubRepo) commit(s snapshot, message string, changes map[string]*string) (string, error) {
	var entries []map[string]any
	for _, p := range slices.Sorted(maps.Keys(changes)) {
		e := map[string]any{"path": p, "mode": "100644", "type": "blob", "sha": nil}
		if c := changes[p]; c != nil {
			delete(e, "sha")
			e["content"] = *c
		}
		entries = append(entries, e)
	}

	var tree, commit struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
	}
	if err := g.do("POST", "git/trees", map[string]any{"base_tree": s.tree, "tree": entries}, &tree); err != nil {
		return "", err
	}
	if err := g.do("POST", "git/commits", map[string]any{"message": message, "tree": tree.SHA, "parents": []string{s.head}}, &commit); err != nil {
		return "", err
	}
	if err := g.do("PATCH", "git/refs/heads/"+g.Branch, map[string]any{"sha": commit.SHA}, nil); err != nil {
		return "", fmt.Errorf("update %s (changed concurrently? try again): %w", g.Branch, err)
	}
	return commit.HTMLURL, nil
}

func (g *GitHubRepo) do(method, apiPath string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("%s/repos/%s/%s/%s", g.API, g.Owner, g.Name, apiPath), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, apiPath, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GitHub %s %s returned %d: %s", method, apiPath, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// GitOps is the real backend: desired state is committed to the client repo,
// ArgoCD reconciles it, and status is read back from ArgoCD.
//
// Layout: environments/<name>.json, and environments/<group>/<name>.json for matrix groups.
type GitOps struct {
	repo *GitHubRepo
	argo *ArgoCDClient
	mu   sync.Mutex // ponytail: one writer at a time; a second instance would rely on the fast-forward check

	benchMu sync.Mutex
	bench   map[string]BenchResult // finished pgbench results by environment; they never change
}

const readmePath = "environments/README.md"

const environmentsReadme = `# Environments

One ArgoCD Application per file, written by Meeseeks. ArgoCD syncs this directory
through the root app ` + "`" + rootApp + "`" + ` (app-of-apps). Matrix groups get a subdirectory.

This README also keeps the directory in Git when there are no environments.
`

func envFile(r EnvironmentRequest) string {
	if r.Group != "" {
		return "environments/" + r.Group + "/" + r.Name + ".json"
	}
	return "environments/" + r.Name + ".json"
}

// usedNames returns every environment and group name present in the repo.
func usedNames(paths []string) map[string]bool {
	used := map[string]bool{}
	for _, p := range paths {
		rel, ok := strings.CutPrefix(p, "environments/")
		if !ok || !strings.HasSuffix(rel, ".json") {
			continue
		}
		dir, file := path.Split(rel)
		used[strings.TrimSuffix(file, ".json")] = true
		if dir != "" {
			used[strings.TrimSuffix(dir, "/")] = true
		}
	}
	return used
}

// Bootstrap makes the client repo and ArgoCD ready. It is idempotent, so it runs on every start.
func (g *GitOps) Bootstrap() error {
	s, err := g.repo.snapshot()
	if err != nil {
		return fmt.Errorf("read client repo: %w", err)
	}
	// Git drops empty directories; without this file, deleting the last environment
	// would remove environments/ and break the root app.
	if !slices.Contains(s.paths, readmePath) {
		readme := environmentsReadme
		if _, err := g.repo.commit(s, "Add environments directory", map[string]*string{readmePath: &readme}); err != nil {
			return fmt.Errorf("prepare client repo: %w", err)
		}
	}
	if err := g.argo.UpsertRepo(g.repo.URL(), "meeseeks", g.repo.token); err != nil {
		return fmt.Errorf("register client repo in ArgoCD: %w", err)
	}
	if err := g.argo.UpsertApplication(buildRootApp(g.repo.URL(), g.repo.Branch)); err != nil {
		return fmt.Errorf("create root app: %w", err)
	}
	return nil
}

// Create commits one environment, or a whole matrix group, in a single commit.
func (g *GitOps) Create(reqs []EnvironmentRequest) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, err := g.repo.snapshot()
	if err != nil {
		return "", err
	}
	used := usedNames(s.paths)
	group := reqs[0].Group
	if group != "" && used[group] {
		return "", fmt.Errorf("%q is already used in %s", group, g.repo.URL())
	}

	ttl, err := parseTTL(reqs[0].TTL)
	if err != nil {
		return "", err
	}
	expires := time.Now().Add(ttl).UTC().Truncate(time.Second) // one expiry for the whole group

	changes := map[string]*string{}
	var lines []string
	for i := range reqs {
		reqs[i].ExpiresAt = expires
		r := reqs[i]
		if used[r.Name] || r.Name == rootApp {
			return "", fmt.Errorf("%q is already used in %s", r.Name, g.repo.URL())
		}
		// An existing app with this name (made outside the client repo) would be adopted
		// by the root app, and a later delete would cascade into it.
		exists, err := g.argo.Exists(r.Name)
		if err != nil {
			return "", fmt.Errorf("check name in ArgoCD: %w", err)
		}
		if exists {
			return "", fmt.Errorf("name %q is already used by an ArgoCD application", r.Name)
		}
		content, _ := json.MarshalIndent(buildApplication(r), "", "  ")
		file := string(content) + "\n"
		changes[envFile(r)] = &file
		lines = append(lines, "- "+r.Name+": "+depsSummary(r.Dependencies, r.Versions))
	}

	msg := fmt.Sprintf("Create environment %s\n\nbranch: %s, type: %s, dependencies: %s, expires: %s",
		reqs[0].Name, reqs[0].Branch, reqs[0].EnvType, depsSummary(reqs[0].Dependencies, reqs[0].Versions), expires.Format(time.RFC3339))
	if group != "" {
		msg = fmt.Sprintf("Create group %s: %d environments\n\nbranch: %s, benchmark: %t, expires: %s\n%s",
			group, len(reqs), reqs[0].Branch, reqs[0].Benchmark, expires.Format(time.RFC3339), strings.Join(lines, "\n"))
	}
	commit, err := g.repo.commit(s, msg, changes)
	if err != nil {
		return "", err
	}
	g.benchMu.Lock()
	for _, r := range reqs {
		delete(g.bench, r.Name) // a reused name must not show an old result
	}
	g.benchMu.Unlock()
	g.refresh()
	return commit, nil
}

// expired reports whether a committed Application's own expires-at has passed.
// No annotation, or one that doesn't parse, means it never expires.
func expired(app ArgoCDApplication, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, app.Metadata.Annotations[expiresAt])
	return err == nil && !now.Before(t)
}

// ReapExpired removes every expired environment in one commit. Argo CD only nominates
// candidates (it can lag behind Git); each one is deleted only if its file is still in Git
// and the file itself says it has expired.
func (g *GitOps) ReapExpired(now time.Time) {
	list, err := g.argo.ListApplications()
	if err != nil {
		log.Printf("reaper: %v", err)
		return
	}
	candidates := map[string]bool{}
	for _, e := range list.Items {
		t, err := time.Parse(time.RFC3339, e.ExpiresAt)
		if err == nil && e.Status != "Deleting" && !now.Before(t) {
			candidates[envFile(EnvironmentRequest{Name: e.Name, Group: e.Group})] = true
		}
	}
	if len(candidates) == 0 {
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	s, err := g.repo.snapshot()
	if err != nil {
		log.Printf("reaper: %v", err)
		return
	}
	changes := map[string]*string{}
	var names []string
	for _, p := range s.paths {
		if !candidates[p] {
			continue
		}
		content, err := g.repo.read(s, p)
		if err != nil {
			log.Printf("reaper: %v", err)
			continue
		}
		var app ArgoCDApplication
		if json.Unmarshal([]byte(content), &app) == nil && expired(app, now) {
			changes[p] = nil
			names = append(names, strings.TrimSuffix(path.Base(p), ".json"))
		}
	}
	if len(changes) == 0 {
		return
	}
	slices.Sort(names)
	msg := "Expire environment " + names[0]
	if len(names) > 1 {
		msg = fmt.Sprintf("Expire %d environments\n\n%s", len(names), strings.Join(names, "\n"))
	}
	commit, err := g.repo.commit(s, msg, changes)
	if err != nil {
		log.Printf("reaper: %v", err)
		return
	}
	log.Printf("reaper: expired %s in %s", strings.Join(names, ", "), commit)
	g.refresh()
}

// Delete removes an environment, or every environment of a group, in one commit.
func (g *GitOps) Delete(name string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	s, err := g.repo.snapshot()
	if err != nil {
		return "", err
	}
	changes := map[string]*string{}
	for _, p := range s.paths {
		rel, ok := strings.CutPrefix(p, "environments/")
		if ok && strings.HasSuffix(rel, ".json") &&
			(rel == name+".json" || strings.HasSuffix(rel, "/"+name+".json") || strings.HasPrefix(rel, name+"/")) {
			changes[p] = nil
		}
	}
	if len(changes) == 0 {
		return "", fmt.Errorf("environment %q not found in %s", name, g.repo.URL())
	}

	msg := "Delete environment " + name
	if len(changes) > 1 || strings.HasPrefix(slices.Collect(maps.Keys(changes))[0], "environments/"+name+"/") {
		msg = fmt.Sprintf("Delete group %s: %d environments", name, len(changes))
	}
	commit, err := g.repo.commit(s, msg, changes)
	if err != nil {
		return "", err
	}
	g.refresh()
	return commit, nil
}

func (g *GitOps) List() (EnvironmentList, error) {
	list, err := g.argo.ListApplications()
	for i := range list.Items {
		e := &list.Items[i]
		e.GitURL = g.repo.FileURL(envFile(EnvironmentRequest{Name: e.Name, Group: e.Group}))
		if e.Benchmark != nil {
			r := g.benchResult(e.Name)
			e.Benchmark = &r
		}
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

// BenchResult is what the pgbench Job reported, read from its logs through ArgoCD.
type BenchResult struct {
	Done      bool    `json:"done"`
	TPS       float64 `json:"tps,omitempty"`
	LatencyMs float64 `json:"latency_ms,omitempty"`
}

func (g *GitOps) benchResult(name string) BenchResult {
	g.benchMu.Lock()
	r, ok := g.bench[name]
	g.benchMu.Unlock()
	if ok {
		return r
	}
	logs, err := g.argo.JobLogs(name, "env-"+name, benchJob)
	if err != nil {
		return BenchResult{} // the Job may not have started yet
	}
	r = parsePgbench(logs)
	if r.Done {
		g.benchMu.Lock()
		if g.bench == nil {
			g.bench = map[string]BenchResult{}
		}
		g.bench[name] = r
		g.benchMu.Unlock()
	}
	return r
}

var (
	tpsRe     = regexp.MustCompile(`tps = ([\d.]+) \(without initial connection time\)`)
	latencyRe = regexp.MustCompile(`latency average = ([\d.]+) ms`)
)

// parsePgbench reads the summary pgbench prints at the end of a run.
func parsePgbench(logs string) BenchResult {
	tps, lat := tpsRe.FindStringSubmatch(logs), latencyRe.FindStringSubmatch(logs)
	if tps == nil || lat == nil {
		return BenchResult{}
	}
	r := BenchResult{Done: true}
	r.TPS, _ = strconv.ParseFloat(tps[1], 64)
	r.LatencyMs, _ = strconv.ParseFloat(lat[1], 64)
	return r
}

// depsSummary renders "postgresql 17, redis 8" with the effective versions.
func depsSummary(deps []string, versions map[string]string) string {
	if len(deps) == 0 {
		return "none"
	}
	var out []string
	for _, d := range deps {
		v := versions[d]
		if v == "" {
			v = dependencies[d].defaultVersion()
		}
		out = append(out, d+" "+v)
	}
	return strings.Join(out, ", ")
}
