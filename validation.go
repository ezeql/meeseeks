package main

import (
	"fmt"
	"regexp"
	"strings"
)

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

	if len(name) > 63 {
		return fmt.Errorf("name cannot be longer than 63 characters")
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
		if _, ok := dependencyEnv[dep]; !ok {
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
