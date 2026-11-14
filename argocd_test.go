package main

import (
	"strings"
	"testing"
)

func TestBuildApplication(t *testing.T) {
	app := (&ArgoCDClient{}).buildApplication(EnvironmentRequest{
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
