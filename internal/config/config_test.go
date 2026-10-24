package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookup(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestParseExpandsEnv(t *testing.T) {
	src := `
policy_dir: ./policy
scopes:
  azure:
    tenant_id: "${AZURE_TENANT_ID}"
    subscriptions: ["sub-${ENV}-*"]
terraform:
  scm_tokens:
    github: ${GITHUB_TOKEN}
    bitbucket: "${BITBUCKET_TOKEN}"
  module_registries:
    - { type: jfrog, url: "${JFROG_URL}", token: "$NOT_EXPANDED" }
`
	cfg, err := Parse([]byte(src), lookup(map[string]string{
		"AZURE_TENANT_ID": "tenant-1", "ENV": "prod", "GITHUB_TOKEN": "12345",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scopes.Azure.TenantID != "tenant-1" || cfg.Scopes.Azure.Subscriptions[0] != "sub-prod-*" {
		t.Errorf("expansion failed: %+v", cfg.Scopes.Azure)
	}
	if cfg.Terraform.SCMTokens.GitHub != "12345" {
		t.Errorf("a numeric-looking value must stay a string, got %q", cfg.Terraform.SCMTokens.GitHub)
	}
	if got := cfg.Terraform.ModuleRegistries[0].Token; got != "$NOT_EXPANDED" {
		t.Errorf("bare $NAME must be left alone, got %q", got)
	}
	want := map[string]string{
		"terraform.scm_tokens.bitbucket":     "BITBUCKET_TOKEN",
		"terraform.module_registries[0].url": "JFROG_URL",
	}
	if len(cfg.Unset) != len(want) {
		t.Fatalf("unset = %v, want %v", cfg.Unset, want)
	}
	for p, name := range want {
		if cfg.Unset[p] != name {
			t.Errorf("unset[%s] = %q, want %q", p, cfg.Unset[p], name)
		}
	}
}

func TestRequire(t *testing.T) {
	cfg, err := Parse([]byte("terraform:\n  scm_tokens:\n    github: ${GH}\n"), lookup(nil))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Require("scopes"); err != nil {
		t.Errorf("unrelated path: %v", err)
	}
	err = cfg.Require("terraform.scm_tokens")
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "${GH}") {
		t.Errorf("err = %v", err)
	}
}

func TestParseRejectsUnknownKeys(t *testing.T) {
	_, err := Parse([]byte("policy_dirs: ./policy\n"), lookup(nil))
	if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), "policy_dirs") {
		t.Fatalf("err = %v", err)
	}
}

func TestEmptyFile(t *testing.T) {
	cfg, err := Parse(nil, lookup(nil))
	if err != nil || cfg.PolicyDir != "" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestLoadResolvesRelativePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	if err := os.WriteFile(path, []byte("policy_dir: ./rules\nsynonyms_file: /abs/syn.yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PolicyDir != filepath.Join(dir, "rules") {
		t.Errorf("policy_dir = %q", cfg.PolicyDir)
	}
	if cfg.SynonymsFile != "/abs/syn.yaml" {
		t.Errorf("synonyms_file = %q", cfg.SynonymsFile)
	}
}

func TestFind(t *testing.T) {
	if _, err := Find("/nonexistent/tagherd.yaml"); !errors.Is(err, ErrInvalid) {
		t.Errorf("explicit missing file: err = %v", err)
	}
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("HOME", dir)
	if p, err := Find(""); p != "" || err != nil {
		t.Errorf("no file: p = %q, err = %v", p, err)
	}
	home := filepath.Join(dir, ".config", "tagherd")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, FileName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(""); p != filepath.Join(home, FileName) {
		t.Errorf("home fallback: p = %q", p)
	}
	if err := os.WriteFile(FileName, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(""); p != FileName {
		t.Errorf("./tagherd.yaml should win: p = %q", p)
	}
}
