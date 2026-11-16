package main

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

// maxMatrix caps how many environments one request can create.
const maxMatrix = 8

// Environments expire after defaultTTL unless the request asks for another TTL, up to maxTTL.
const (
	defaultTTL = 24 * time.Hour
	maxTTL     = 7 * 24 * time.Hour
)

// parseTTL turns the request's ttl into a duration; empty means defaultTTL.
func parseTTL(s string) (time.Duration, error) {
	if s == "" {
		return defaultTTL, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < time.Minute || d > maxTTL {
		return 0, fmt.Errorf("ttl must be a duration between 1m and 168h, e.g. 4h, got %q", s)
	}
	return d, nil
}

var (
	nameRegex = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	cpuRegex  = regexp.MustCompile(`^\d+(\.\d+)?[m]?$`)
	memRegex  = regexp.MustCompile(`^\d+(\.\d+)?[KMGT]i?$`)
	envRegex  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

func ValidateEnvironmentRequest(req EnvironmentRequest) error {
	if err := validateName(req.Name); err != nil {
		return fmt.Errorf("invalid name: %w", err)
	}

	if err := validateBranch(req.Branch); err != nil {
		return fmt.Errorf("invalid branch: %w", err)
	}

	if req.CPU != "" {
		if err := validateCPU(req.CPU); err != nil {
			return fmt.Errorf("invalid CPU: %w", err)
		}
	}

	if req.Memory != "" {
		if err := validateMemory(req.Memory); err != nil {
			return fmt.Errorf("invalid memory: %w", err)
		}
	}

	if req.Replicas < 0 || req.Replicas > 10 {
		return fmt.Errorf("replicas must be between 0 and 10")
	}

	if err := validateDependencies(req.Dependencies); err != nil {
		return fmt.Errorf("invalid dependencies: %w", err)
	}

	for dep, version := range req.Versions {
		if !slices.Contains(req.Dependencies, dep) {
			return fmt.Errorf("version given for %s, which is not a dependency", dep)
		}
		if err := validateVersion(dep, version); err != nil {
			return err
		}
	}

	for dep, versions := range req.Matrix {
		if len(versions) == 0 {
			return fmt.Errorf("matrix for %s has no versions", dep)
		}
		for i, v := range versions {
			if err := validateVersion(dep, v); err != nil {
				return fmt.Errorf("matrix: %w", err)
			}
			if slices.Contains(versions[:i], v) {
				return fmt.Errorf("matrix: duplicate %s version %s", dep, v)
			}
		}
	}

	if req.Benchmark && !slices.Contains(req.Dependencies, "postgresql") && req.Matrix["postgresql"] == nil {
		return fmt.Errorf("benchmark needs the postgresql dependency")
	}

	if _, err := parseTTL(req.TTL); err != nil {
		return err
	}

	if err := validateEnvType(req.EnvType); err != nil {
		return fmt.Errorf("invalid environment type: %w", err)
	}

	for name := range req.EnvVars {
		if !envRegex.MatchString(name) {
			return fmt.Errorf("invalid env var name %q: use letters, digits and underscores", name)
		}
	}

	return nil
}

func validateName(name string) error {
	if name == "" {
		return fmt.Errorf("name cannot be empty")
	}

	// The namespace is "env-<name>" and namespaces are DNS labels (max 63).
	if len(name) > 59 {
		return fmt.Errorf("name cannot be longer than 59 characters")
	}

	if !nameRegex.MatchString(name) {
		return fmt.Errorf("name must contain only lowercase alphanumeric characters and hyphens, and must start and end with an alphanumeric character")
	}

	return nil
}

func validateBranch(branch string) error {
	if branch == "" {
		return fmt.Errorf("branch cannot be empty")
	}

	if len(branch) > 255 {
		return fmt.Errorf("branch name cannot be longer than 255 characters")
	}

	invalidChars := []string{" ", "\t", "\n", "\r", "~", "^", ":", "?", "*", "[", "\\"}
	for _, char := range invalidChars {
		if strings.Contains(branch, char) {
			return fmt.Errorf("branch name contains invalid character: %s", char)
		}
	}

	return nil
}

func validateCPU(cpu string) error {
	if !cpuRegex.MatchString(cpu) {
		return fmt.Errorf("CPU must be in format like '100m', '0.5', '1', '2'")
	}
	return nil
}

func validateMemory(memory string) error {
	if !memRegex.MatchString(memory) {
		return fmt.Errorf("memory must be in format like '128Mi', '1Gi', '512M', '1G'")
	}
	return nil
}

func validateDependencies(deps []string) error {
	seen := map[string]bool{}
	for _, dep := range deps {
		if _, ok := dependencies[dep]; !ok {
			return fmt.Errorf("unsupported dependency: %s. Supported: postgresql, redis, mongodb", dep)
		}
		if seen[dep] {
			return fmt.Errorf("duplicate dependency: %s", dep)
		}
		seen[dep] = true
	}

	return nil
}

func validateEnvType(envType string) error {
	if envType == "" {
		return nil
	}

	validTypes := map[string]bool{
		"development": true,
		"staging":     true,
		"testing":     true,
		"dev":         true,
		"prod":        true,
	}

	if !validTypes[envType] {
		return fmt.Errorf("unsupported environment type: %s. Supported: development, staging, testing, dev, prod", envType)
	}

	return nil
}

func validateVersion(dep, version string) error {
	d, ok := dependencies[dep]
	if !ok {
		return fmt.Errorf("unsupported dependency: %s", dep)
	}
	if !slices.Contains(d.versions, version) {
		return fmt.Errorf("unsupported %s version %q. Supported: %s", dep, version, strings.Join(d.versions, ", "))
	}
	return nil
}

// ExpandRequest validates a request and turns a matrix into one request per combination,
// e.g. {name: pg-compare, matrix: {postgresql: [16, 17]}} becomes pg-compare-postgresql-16
// and pg-compare-postgresql-17 in group pg-compare.
func ExpandRequest(req EnvironmentRequest) ([]EnvironmentRequest, error) {
	if err := ValidateEnvironmentRequest(req); err != nil {
		return nil, err
	}
	if len(req.Matrix) == 0 {
		return []EnvironmentRequest{req}, nil
	}

	base := req
	base.Matrix, base.Group = nil, req.Name
	reqs := []EnvironmentRequest{base}
	for _, dep := range slices.Sorted(maps.Keys(req.Matrix)) {
		var next []EnvironmentRequest
		for _, r := range reqs {
			for _, v := range req.Matrix[dep] {
				c := r
				c.Name = r.Name + "-" + dep + "-" + v
				c.Versions = maps.Clone(r.Versions)
				if c.Versions == nil {
					c.Versions = map[string]string{}
				}
				c.Versions[dep] = v
				if !slices.Contains(c.Dependencies, dep) {
					c.Dependencies = append(slices.Clone(c.Dependencies), dep)
				}
				next = append(next, c)
			}
		}
		reqs = next
	}

	if len(reqs) > maxMatrix {
		return nil, fmt.Errorf("matrix creates %d environments; the limit is %d", len(reqs), maxMatrix)
	}
	for _, r := range reqs {
		if err := validateName(r.Name); err != nil {
			return nil, fmt.Errorf("environment %s: %w", r.Name, err)
		}
	}
	return reqs, nil
}
