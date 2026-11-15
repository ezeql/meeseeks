package main

import (
	"strings"
	"testing"
)

func TestBuildApplication(t *testing.T) {
	app := buildApplication(EnvironmentRequest{
		Name: "x", Branch: "feature/a", CPU: "100m", Replicas: 2,
		Dependencies: []string{"postgresql"}, EnvVars: map[string]string{"LOG_LEVEL": "debug"},
	})

	src := app.Spec.Sources
	if len(src) != 2 || src[0].Path != "deploy/base" || src[1].Path != "deploy/deps/postgresql" {
		t.Fatalf("unexpected sources: %+v", src)
	}
	if src[0].TargetRevision != "feature/a" || src[1].TargetRevision != "feature/a" {
		t.Fatal("all sources must track the requested branch")
	}
	if len(app.Metadata.Finalizers) != 1 || app.Metadata.Labels["meeseeks/gitops"] != "true" {
		t.Fatal("committed apps need the resources finalizer and the gitops label")
	}
	if k := src[1].Kustomize; k == nil || len(k.Images) != 1 || k.Images[0] != "postgres=postgres:18-alpine" {
		t.Fatalf("dependency must pin its image to the default version: %+v", src[1].Kustomize)
	}
	k := src[0].Kustomize
	if k == nil || len(k.Replicas) != 1 || k.Replicas[0].Count != 2 || len(k.Patches) != 1 {
		t.Fatalf("unexpected kustomize: %+v", k)
	}
	for _, want := range []string{`"cpu":"100m"`, `"DATABASE_URL"`, `"LOG_LEVEL"`} {
		if !strings.Contains(k.Patches[0].Patch, want) {
			t.Errorf("patch missing %s: %s", want, k.Patches[0].Patch)
		}
	}

	if plain := buildKustomize(EnvironmentRequest{Name: "y", Branch: "main"}); plain != nil {
		t.Fatalf("no customization should mean no kustomize options, got %+v", plain)
	}
}

func TestValidateRejects(t *testing.T) {
	base := EnvironmentRequest{Name: "ok", Branch: "main"}
	for name, mutate := range map[string]func(*EnvironmentRequest){
		"bad name":      func(r *EnvironmentRequest) { r.Name = "Bad_Name" },
		"unknown dep":   func(r *EnvironmentRequest) { r.Dependencies = []string{"mysql"} },
		"duplicate dep": func(r *EnvironmentRequest) { r.Dependencies = []string{"redis", "redis"} },
		"bad env var":   func(r *EnvironmentRequest) { r.EnvVars = map[string]string{"1-BAD": "x"} },
	} {
		req := base
		mutate(&req)
		if ValidateEnvironmentRequest(req) == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if err := ValidateEnvironmentRequest(base); err != nil {
		t.Fatalf("valid request rejected: %v", err)
	}
}

func TestExpandRequest(t *testing.T) {
	reqs, err := ExpandRequest(EnvironmentRequest{Name: "cmp", Branch: "main", Dependencies: []string{"redis"},
		Matrix: map[string][]string{"postgresql": {"16", "17"}, "redis": {"7", "8"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 4 || reqs[0].Name != "cmp-postgresql-16-redis-7" || reqs[0].Group != "cmp" ||
		reqs[3].Versions["postgresql"] != "17" || reqs[3].Versions["redis"] != "8" || len(reqs[3].Dependencies) != 2 {
		t.Fatalf("unexpected expansion: %+v", reqs)
	}

	for name, req := range map[string]EnvironmentRequest{
		"too many":           {Name: "x", Branch: "main", Matrix: map[string][]string{"postgresql": {"15", "16", "17", "18"}, "redis": {"7", "8"}, "mongodb": {"7", "8"}}},
		"unknown version":    {Name: "x", Branch: "main", Matrix: map[string][]string{"postgresql": {"9"}}},
		"bench without pg":   {Name: "x", Branch: "main", Benchmark: true},
		"version of non-dep": {Name: "x", Branch: "main", Versions: map[string]string{"redis": "7"}},
		"member name length": {Name: strings.Repeat("a", 50), Branch: "main", Matrix: map[string][]string{"postgresql": {"16"}}},
	} {
		if _, err := ExpandRequest(req); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
