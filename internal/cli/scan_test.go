package cli

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/output"
)

var update = flag.Bool("update", false, "rewrite golden files")

var scanFixture = func() string {
	a, err := filepath.Abs("../../testdata/fixtures/scan.yaml")
	if err != nil {
		panic(err)
	}
	return a
}()

var goldenDir = func() string {
	a, err := filepath.Abs("testdata/golden")
	if err != nil {
		panic(err)
	}
	return a
}()

func scanArgs(extra ...string) []string {
	return append([]string{"scan", "--policy-dir", realPolicy, "--fixture", scanFixture}, extra...)
}

// TestScanGolden pins every output format. Run with -update to rewrite.
func TestScanGolden(t *testing.T) {
	cases := map[string][]string{
		"scan.table":         {"-o", "table"},
		"scan.json":          {"-o", "json"},
		"scan.ndjson":        {"-o", "ndjson"},
		"scan.csv":           {"-o", "csv"},
		"scan.md":            {"-o", "markdown"},
		"scan-summary.table": {"-o", "table", "--summary"},
		"scan-summary.json":  {"-o", "json", "--summary"},
		"scan-summary.csv":   {"-o", "csv", "--summary"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			r := run(t, nil, scanArgs(args...)...)
			if r.code != output.ExitIncomplete {
				t.Fatalf("exit %d, want 3: %s", r.code, r.stderr)
			}
			path := filepath.Join(goldenDir, name)
			if *update {
				if err := os.MkdirAll(goldenDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(r.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run go test ./internal/cli -update)", err)
			}
			if r.stdout != string(want) {
				t.Errorf("output differs from %s (run go test ./internal/cli -update after checking the change)\n--- got\n%s", path, r.stdout)
			}
		})
	}
}

func TestScanExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{"unreadable scope wins", nil, output.ExitIncomplete},
		{"findings", []string{"--cloud", "azure,gcp"}, output.ExitFindings},
		{"fail-on none", []string{"--cloud", "azure,gcp", "--fail-on", "none"}, output.ExitOK},
		{"fail-on missing: azure has only a case mismatch", []string{"--cloud", "azure", "--fail-on", "missing"}, output.ExitOK},
		{"fail-on invalid", []string{"--cloud", "azure", "--fail-on", "invalid"}, output.ExitFindings},
		{"not taggable and unmapped never fail", []string{"--account", "111*", "--type", "AWS IAM AWS Managed Policy"}, output.ExitOK},
		{"compliant scope", []string{"--cloud", "aws", "--account", "111*", "--group", "database", "--type", "*dynamodb*"}, output.ExitOK},
		{"bad fail-on", []string{"--fail-on", "some"}, output.ExitUsage},
		{"unknown group", []string{"--group", "nope"}, output.ExitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := run(t, nil, scanArgs(append(tc.args, "-o", "json")...)...)
			if r.code != tc.want {
				t.Errorf("exit %d, want %d\nstderr: %s", r.code, tc.want, r.stderr)
			}
		})
	}
}

func TestScanWithoutFixture(t *testing.T) {
	r := run(t, nil, "scan", "--policy-dir", realPolicy)
	if r.code != output.ExitUsage || !strings.Contains(r.stderr, "phase 3") {
		t.Errorf("exit %d, stderr %q", r.code, r.stderr)
	}
	r = run(t, nil, "scan", "--policy-dir", realPolicy, "--fixture", "/nonexistent.yaml")
	if r.code != output.ExitUsage {
		t.Errorf("missing fixture: exit %d", r.code)
	}
}

func TestScanFixtureFromEnvAndOut(t *testing.T) {
	out := filepath.Join(t.TempDir(), "findings.ndjson")
	r := run(t, map[string]string{"TAGHERD_POLICY_DIR": realPolicy}, "scan", "--fixture", scanFixture, "--cloud", "gcp", "--out", out, "-o", "ndjson")
	if r.code != output.ExitFindings || r.stdout != "" {
		t.Fatalf("exit %d, stdout %q, stderr %s", r.code, r.stdout, r.stderr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[2], `{"summary":`) {
		t.Errorf("ndjson = %q", data)
	}
}
