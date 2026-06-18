package openapi

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSpecIncludesLabValidation(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test file path")
	}
	specPath := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "openapi.yaml"))
	data, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read openapi spec: %v", err)
	}
	var doc struct {
		OpenAPI    string                    `yaml:"openapi"`
		Paths      map[string]map[string]any `yaml:"paths"`
		Components struct {
			Schemas map[string]any `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse openapi spec: %v", err)
	}
	if doc.OpenAPI == "" {
		t.Fatalf("openapi version is missing")
	}
	for _, path := range []string{"/system/lab-validation", "/system/lab-validation/run"} {
		if _, ok := doc.Paths[path]; !ok {
			t.Fatalf("expected OpenAPI path %s", path)
		}
	}
	for _, schema := range []string{"LabValidationReport", "LabValidationRunRequest", "LabValidationRunResult"} {
		if _, ok := doc.Components.Schemas[schema]; !ok {
			t.Fatalf("expected OpenAPI schema %s", schema)
		}
	}
}
