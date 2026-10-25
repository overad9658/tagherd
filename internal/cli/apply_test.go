package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
)

// makePlan writes plan.json from the fixture into a new current directory,
// then lets edit change it.
func makePlan(t *testing.T, edit func(*model.Plan)) {
	t.Helper()
	t.Chdir(t.TempDir())
	r := runIn(t, nil, "", planArgs("-o", "json")...)
	if r.code != output.ExitIncomplete {
		t.Fatalf("plan: exit %d: %s", r.code, r.stderr)
	}
	if edit == nil {
		return
	}
	var p model.Plan
	data, _ := os.ReadFile(defaultPlanFile)
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	edit(&p)
	data, _ = json.Marshal(p)
	if err := os.WriteFile(defaultPlanFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func applyArgs(plan string, extra ...string) []string {
	return append([]string{"apply", "--config", planConfig, "--fixture", scanFixture, "--plan", plan}, extra...)
}

func TestApplyGolden(t *testing.T) {
	cases := map[string][]string{
		"apply.table":  {"-o", "table"},
		"apply.json":   {"-o", "json"},
		"apply.ndjson": {"-o", "ndjson", "--cloud", "azure"},
		"apply.csv":    {"-o", "csv"},
		"apply.md":     {"-o", "markdown", "--verify"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			makePlan(t, nil)
			plan := defaultPlanFile
			r := runIn(t, nil, "", applyArgs(plan, append(args, "--yes")...)...)
			if r.code != output.ExitOK {
				t.Fatalf("exit %d: %s", r.code, r.stderr)
			}
			golden(t, name, normalizeApply(r.stdout))
		})
	}
}

// normalizeApply drops the plan ID, which differs on every run.
func normalizeApply(s string) string {
	return volatile.ReplaceAllString(strings.ReplaceAll(s, `"plan_id":`, `"id":`), `${1}${2}X"`)
}

func TestApplyAudit(t *testing.T) {
	makePlan(t, nil)
	plan := defaultPlanFile
	r := runIn(t, nil, "", applyArgs(plan, "--yes", "-o", "json", "--verify")...)
	if r.code != output.ExitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	var rep model.ApplyReport
	if err := json.Unmarshal([]byte(r.stdout), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Summary.Written != 5 || rep.Summary.Pending != 3 {
		t.Errorf("summary = %+v", rep.Summary)
	}
	// The bucket's invalid AdminEmail had no fix, so verify still sees it.
	open := false
	for _, f := range rep.Verify {
		if f.Name == "acme-logs" && f.Tag == "AdminEmail" {
			open = true
		}
		if f.Tag == "Environment" && f.Name == "orders" {
			t.Errorf("a written change is still open: %+v", f)
		}
	}
	if !open {
		t.Errorf("verify = %+v", rep.Verify)
	}
	info, err := os.Stat(defaultAuditLog)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(defaultAuditLog)
	if lines := strings.Count(string(data), "\n"); lines != 5 || info.Mode().Perm() != 0o600 {
		t.Errorf("audit: %d lines, mode %v", lines, info.Mode().Perm())
	}
	// The audit log is appended to, never truncated.
	runIn(t, nil, "", applyArgs(plan, "--yes", "--cloud", "azure", "-o", "json")...)
	data, _ = os.ReadFile(defaultAuditLog)
	if lines := strings.Count(string(data), "\n"); lines != 6 {
		t.Errorf("audit after a second apply: %d lines", lines)
	}
}

func TestApplyStaleExits4(t *testing.T) {
	makePlan(t, func(p *model.Plan) {
		for i := range p.Changes {
			if p.Changes[i].Tag == "EndpointType" {
				p.Changes[i].Expect = "workstation" // not what the resource holds
			}
		}
	})
	plan := defaultPlanFile
	r := runIn(t, nil, "", applyArgs(plan, "--yes", "--cloud", "azure", "-o", "json")...)
	if r.code != output.ExitApplyFailed || !strings.Contains(r.stdout, `"outcome": "stale"`) {
		t.Errorf("exit %d\n%s", r.code, r.stdout)
	}
}

func TestApplyGates(t *testing.T) {
	makePlan(t, func(p *model.Plan) { p.Created = time.Now().Add(-25 * time.Hour) })
	old := defaultPlanFile
	data, _ := os.ReadFile(old)
	dir, _ := os.Getwd()
	changed := filepath.Join(dir, "changed.json")
	if err := os.WriteFile(changed, []byte(strings.Replace(string(data), `"policy_hash":"`, `"policy_hash":"0`, 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "junk.json", `{"changes": []}`)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no plan", []string{"apply"}, "--plan is required"},
		{"bad only", applyArgs(old, "--only", "both"), "--only must be"},
		{"terraform", applyArgs(old, "--only", "terraform"), "phase 6"},
		{"pr", applyArgs(old, "--pr"), "phase 6"},
		{"not a plan", applyArgs(filepath.Join(dir, "junk.json")), "not a plan file"},
		{"too old", applyArgs(old, "--yes"), "--allow-stale"},
		{"policy changed", applyArgs(changed, "--yes", "--allow-stale"), "policy changed"},
		{"no terminal", applyArgs(old, "--allow-stale"), "pass --yes"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := runIn(t, nil, "yes\n", tc.args...)
			if r.code != output.ExitUsage || !strings.Contains(r.stderr, tc.want) {
				t.Errorf("exit %d, stderr %q, want %q", r.code, r.stderr, tc.want)
			}
		})
	}
	if r := runIn(t, nil, "", applyArgs(old, "--yes", "--allow-stale", "-o", "json")...); r.code != output.ExitOK {
		t.Errorf("--allow-stale: exit %d: %s", r.code, r.stderr)
	}
}
