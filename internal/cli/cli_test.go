package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/output"
)

// realPolicy is resolved once, before any test changes directory.
var realPolicy = func() string {
	a, err := filepath.Abs("../../policy")
	if err != nil {
		panic(err)
	}
	return a
}()

type result struct {
	code           int
	stdout, stderr string
}

// run executes tagherd in a temp working directory with only env set.
func run(t *testing.T, env map[string]string, args ...string) result {
	t.Helper()
	t.Chdir(t.TempDir())
	var out, errb bytes.Buffer
	getenv := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	code := Execute(args, &out, &errb, getenv)
	return result{code, out.String(), errb.String()}
}

func TestValidateRealPolicy(t *testing.T) {
	r := run(t, nil, "policy", "validate", "--policy-dir", realPolicy, "-o", "json")
	if r.code != output.ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var res validateResult
	if err := json.Unmarshal([]byte(r.stdout), &res); err != nil {
		t.Fatal(err)
	}
	if !res.Valid || res.Groups != 17 || res.Errors != 0 || res.Warnings != 6 {
		t.Errorf("result = %+v", res)
	}

	r = run(t, nil, "policy", "validate", "--policy-dir", realPolicy, "--strict", "-o", "json")
	if r.code != output.ExitUsage {
		t.Errorf("--strict with warnings: exit %d, want 2", r.code)
	}
}

func TestValidateBrokenPolicy(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "tags.json", `{"DeploymentType": ["Manual", "Automation"]}`)
	write(t, dir, "g.json", `{"name": "G", "description": "x", "tags": ["Owner"], "target": {"AWS": [], "Azure": [], "GCP": []}}`)
	r := run(t, nil, "policy", "validate", "--policy-dir", dir, "-o", "table")
	if r.code != output.ExitUsage {
		t.Fatalf("exit %d, want 2", r.code)
	}
	if !strings.Contains(r.stdout, `tag "Owner" is not in tags.json`) || !strings.Contains(r.stdout, "Policy INVALID") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
}

func TestPrecedence(t *testing.T) {
	broken := t.TempDir() // no tags.json
	good := realPolicy

	// Config file only.
	cfgDir := t.TempDir()
	write(t, cfgDir, "tagherd.yaml", "policy_dir: "+good+"\n")
	cfg := filepath.Join(cfgDir, "tagherd.yaml")
	if r := run(t, nil, "policy", "list", "--config", cfg, "-o", "json"); r.code != 0 {
		t.Errorf("config: exit %d: %s", r.code, r.stderr)
	}
	// Env beats config.
	if r := run(t, map[string]string{"TAGHERD_POLICY_DIR": broken}, "policy", "list", "--config", cfg, "-o", "json"); r.code != output.ExitUsage {
		t.Errorf("env over config: exit %d, want 2", r.code)
	}
	// Flag beats env.
	if r := run(t, map[string]string{"TAGHERD_POLICY_DIR": broken}, "policy", "list", "--policy-dir", good, "-o", "json"); r.code != 0 {
		t.Errorf("flag over env: exit %d: %s", r.code, r.stderr)
	}
	// Env reaches config via TAGHERD_CONFIG.
	if r := run(t, map[string]string{"TAGHERD_CONFIG": cfg}, "policy", "list", "-o", "json"); r.code != 0 {
		t.Errorf("TAGHERD_CONFIG: exit %d: %s", r.code, r.stderr)
	}
	// Bad env value is a usage error.
	if r := run(t, map[string]string{"TAGHERD_CONCURRENCY": "lots"}, "catalog", "-o", "json"); r.code != output.ExitUsage || !strings.Contains(r.stderr, "TAGHERD_CONCURRENCY") {
		t.Errorf("bad env: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestListAndShow(t *testing.T) {
	p := realPolicy
	r := run(t, nil, "policy", "list", "--policy-dir", p, "--group", "compute*", "-o", "json")
	var groups []groupSummary
	if err := json.Unmarshal([]byte(r.stdout), &groups); err != nil || len(groups) != 3 {
		t.Fatalf("list: %d groups, err %v, stderr %s", len(groups), err, r.stderr)
	}
	if !strings.Contains(r.stderr, "policy has 6 warning(s)") {
		t.Errorf("warnings should be summarized on stderr: %q", r.stderr)
	}

	r = run(t, nil, "policy", "show", "COMPUTE_ONLY_MACHINE_IMAGES", "--policy-dir", p, "-o", "json")
	var g struct {
		Name      string         `json:"name"`
		Tags      []string       `json:"tags"`
		Overrides map[string]any `json:"overrides"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &g); err != nil {
		t.Fatalf("show: %v\n%s", err, r.stdout)
	}
	if g.Name != "Compute (only machine images)" || len(g.Tags) != 3 || g.Overrides["AWS"] == nil {
		t.Errorf("show = %+v", g)
	}

	r = run(t, nil, "policy", "show", "nope", "--policy-dir", p)
	if r.code != output.ExitUsage || !strings.Contains(r.stderr, `no policy group matches "nope"`) {
		t.Errorf("unknown group: exit %d, stderr %q", r.code, r.stderr)
	}
}

func TestCatalog(t *testing.T) {
	r := run(t, nil, "catalog", "--cloud", "gcp", "--type", "*vertex*", "-o", "csv")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if r.code != 0 || len(lines) != 11 { // header + 10 Vertex AI entries
		t.Fatalf("exit %d, %d lines:\n%s", r.code, len(lines), r.stdout)
	}
	if r := run(t, nil, "catalog", "--cloud", "oci"); r.code != output.ExitUsage {
		t.Errorf("bad --cloud: exit %d", r.code)
	}
}

func TestOutputFlags(t *testing.T) {
	out := filepath.Join(t.TempDir(), "catalog.json")
	r := run(t, nil, "catalog", "--out", out)
	if r.code != 0 || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q", r.code, r.stdout)
	}
	data, err := os.ReadFile(out)
	if err != nil || !json.Valid(data) {
		t.Errorf("--out without --output should write json: %v", err)
	}
	if r := run(t, nil, "catalog", "-o", "yaml"); r.code != output.ExitUsage {
		t.Errorf("bad --output: exit %d", r.code)
	}
	if r := run(t, nil, "version", "-v", "-q"); r.code != output.ExitUsage {
		t.Errorf("-v with -q: exit %d", r.code)
	}
}

func TestInit(t *testing.T) {
	dir := t.TempDir()
	r := run(t, nil, "init", "--dir", dir)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	for _, f := range []string{"tagherd.yaml", "ownership.yaml", "defaults.yaml", "synonyms.yaml", "policy/tags.json", "policy/storage.json"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s not created", f)
		}
	}
	// The scaffold is itself a valid policy and config.
	r = run(t, nil, "policy", "validate", "--config", filepath.Join(dir, "tagherd.yaml"), "-o", "json")
	if r.code != 0 {
		t.Errorf("scaffold does not validate: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}

	// A second run keeps files and never touches the policy directory.
	write(t, dir, "policy/tags.json", `{"Mine": null}`)
	r = run(t, nil, "init", "--dir", dir, "--force")
	if r.code != 0 {
		t.Fatalf("exit %d", r.code)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "policy", "tags.json")); string(b) != `{"Mine": null}` {
		t.Error("--force must not overwrite the policy directory")
	}
	if r := run(t, nil, "init", "--dir", dir); !strings.Contains(r.stdout, "kept") {
		t.Errorf("existing files should be kept: %s", r.stdout)
	}
}

func TestNotYetImplemented(t *testing.T) {
	r := run(t, nil, "scan", "--fail-on", "any")
	if r.code != output.ExitUsage || !strings.Contains(r.stderr, "phase 2") {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
