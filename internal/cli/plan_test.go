package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
)

var planConfig = func() string {
	a, err := filepath.Abs("../../testdata/fixtures/plan/tagherd.yaml")
	if err != nil {
		panic(err)
	}
	return a
}()

func planArgs(extra ...string) []string {
	return append([]string{"plan", "--config", planConfig, "--fixture", scanFixture}, extra...)
}

// The plan ID and creation time differ on every run.
var volatile = regexp.MustCompile(`("id": ?")[0-9A-Z]{26}"|("created": ?")[^"]+"`)

func normalize(s string) string {
	return volatile.ReplaceAllString(s, `${1}${2}X"`)
}

func TestPlanGolden(t *testing.T) {
	cases := map[string][]string{
		"plan.table":  {"-o", "table"},
		"plan.json":   {"-o", "json"},
		"plan.ndjson": {"-o", "ndjson", "--cloud", "gcp"},
		"plan.csv":    {"-o", "csv"},
		"plan.md":     {"-o", "markdown"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "plan.json")
			r := run(t, nil, planArgs(append(args, "--out", out)...)...)
			want := output.ExitIncomplete // the fixture's unreadable AWS scope
			if strings.Contains(name, "ndjson") {
				want = output.ExitOK
			}
			if r.code != want {
				t.Fatalf("exit %d, want %d: %s", r.code, want, r.stderr)
			}
			golden(t, name, normalize(r.stdout))
		})
	}
}

func TestPlanFile(t *testing.T) {
	r := run(t, nil, planArgs("-o", "json")...)
	if r.code != output.ExitIncomplete {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	wd, _ := os.Getwd()
	path := filepath.Join(wd, defaultPlanFile) // run's working directory
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if string(data) != r.stdout {
		t.Error("plan file differs from the json output")
	}
	var p model.Plan
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.ID) != 26 || p.PolicyHash == "" || p.Grace != "5m0s" || len(p.Scopes) != 4 {
		t.Errorf("header = %+v", p)
	}
	if !strings.Contains(r.stderr, "wrote plan") {
		t.Errorf("stderr = %s", r.stderr)
	}
}

func TestPlanSources(t *testing.T) {
	out := func(t *testing.T) string { return filepath.Join(t.TempDir(), "p.json") }
	decode := func(t *testing.T, r result) model.Plan {
		t.Helper()
		var p model.Plan
		if err := json.Unmarshal([]byte(r.stdout), &p); err != nil {
			t.Fatalf("%v: %s", err, r.stderr)
		}
		return p
	}

	// --set wins over defaults.yaml.
	r := run(t, nil, planArgs("--cloud", "gcp", "-o", "json", "--out", out(t), "--set", "AdminEmail=lead@example.com")...)
	p := decode(t, r)
	for _, c := range p.Changes {
		if c.Tag == "AdminEmail" && (c.New != "lead@example.com" || c.Rule != "set") {
			t.Errorf("--set not applied: %+v", c)
		}
	}

	// --defaults replaces the configured file; this one supplies nothing.
	empty := filepath.Join(t.TempDir(), "d.yaml")
	write(t, filepath.Dir(empty), "d.yaml", "# none\n")
	p = decode(t, run(t, nil, planArgs("--cloud", "gcp", "-o", "json", "--out", out(t), "--defaults", empty)...))
	if p.Summary.Changes != 1 || p.Unresolved[0].Tag != "AdminEmail" || p.Unresolved[0].Reason != "no value source" {
		t.Errorf("plan = %+v", p)
	}

	// A huge grace skips the bucket created on 2026-10-01; 0 skips nothing.
	p = decode(t, run(t, nil, planArgs("--cloud", "aws", "-o", "json", "--out", out(t), "--grace", "100000h")...))
	if p.Summary.SkippedGrace != 1 {
		t.Errorf("skipped = %d", p.Summary.SkippedGrace)
	}
	p = decode(t, run(t, nil, planArgs("--cloud", "aws", "-o", "json", "--out", out(t), "--grace", "0")...))
	if p.Summary.SkippedGrace != 0 || p.Grace != "0s" {
		t.Errorf("grace 0: skipped = %d", p.Summary.SkippedGrace)
	}
}

func TestPlanErrors(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "tagherd.yaml", "ownership_file: missing.yaml\n")
	write(t, dir, "bad.yaml", "aws: {'1': {Environment: Prod}}\n")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"bad --set", planArgs("--set", "Exposure=Secret"), "--set: Exposure"},
		{"--set twice", planArgs("--set", "Brand=a", "--set", "brand=b"), "set more than once"},
		{"negative grace", planArgs("--grace", "-1m"), "--grace"},
		{"bad defaults", planArgs("--defaults", filepath.Join(dir, "bad.yaml")), `Environment="Prod"`},
		{"missing ownership file", []string{"plan", "--config", filepath.Join(dir, "tagherd.yaml"), "--policy-dir", realPolicy, "--fixture", scanFixture}, "ownership_file"},
		{"unwritable --out", planArgs("--out", filepath.Join(dir, "no", "such", "dir", "plan.json")), "--out"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, nil, tc.args...)
			if r.code != output.ExitUsage || !strings.Contains(r.stderr, tc.want) {
				t.Errorf("exit %d, stderr %q, want %q", r.code, r.stderr, tc.want)
			}
		})
	}
}
