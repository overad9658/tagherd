package terraform

import (
	"strings"
	"testing"

	"github.com/overad9658/tagherd/internal/model"
)

func exact(a, b string) bool { return a == b }

func TestEditFile(t *testing.T) {
	bucket := Block{Type: "resource", Labels: []string{"aws_s3_bucket", "data"}, Attr: "tags"}
	tests := []struct {
		name    string
		block   Block
		edits   []Edit
		sameKey func(a, b string) bool
		src     string
		want    string // the edited source; "" means unchanged
		changed []bool
		reason  string
	}{
		{
			name:  "literal map: update and add keep the alignment",
			edits: []Edit{{"Environment", "Production"}, {"OwningOrg", "Platform"}},
			src: `resource "aws_s3_bucket" "data" {
  bucket = "acme-data"

  tags = {
    Environment = "prod"
    Brand       = "Acme"
  }
}
`,
			want: `resource "aws_s3_bucket" "data" {
  bucket = "acme-data"

  tags = {
    Environment = "Production"
    Brand       = "Acme"
    OwningOrg   = "Platform"
  }
}
`,
			changed: []bool{true, true},
		},
		{
			name:  "merge: the last object argument is edited",
			edits: []Edit{{"Exposure", "External"}},
			src: `resource "aws_s3_bucket" "data" {
  tags = merge(local.common_tags, {
    Exposure = "public"
  })
}
`,
			want: `resource "aws_s3_bucket" "data" {
  tags = merge(local.common_tags, {
    Exposure = "External"
  })
}
`,
			changed: []bool{true},
		},
		{
			name:  "merge with no object: one is added last",
			edits: []Edit{{"OwningOrg", "Platform"}},
			src: `resource "aws_s3_bucket" "data" {
  tags = merge(local.common_tags, var.extra_tags)
}
`,
			want: `resource "aws_s3_bucket" "data" {
  tags = merge(local.common_tags, var.extra_tags, { OwningOrg = "Platform" })
}
`,
			changed: []bool{true},
		},
		{
			name:  "one-line object becomes one item per line",
			edits: []Edit{{"OwningOrg", "Platform"}},
			src: `resource "aws_s3_bucket" "data" {
  tags = { Brand = "Acme" }
}
`,
			want: `resource "aws_s3_bucket" "data" {
  tags = {
    Brand     = "Acme"
    OwningOrg = "Platform"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:  "empty object",
			edits: []Edit{{"OwningOrg", "Platform"}},
			src: `resource "aws_s3_bucket" "data" {
  tags = {}
}
`,
			want: `resource "aws_s3_bucket" "data" {
  tags = {
    OwningOrg = "Platform"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:  "no tags argument: one is added",
			edits: []Edit{{"OwningOrg", "Platform"}},
			src: `resource "aws_s3_bucket" "data" {
  bucket = "acme-data"
}
`,
			want: `resource "aws_s3_bucket" "data" {
  bucket = "acme-data"
  tags = {
    OwningOrg = "Platform"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:  "keys that are not identifiers are quoted; values are escaped",
			edits: []Edit{{"cost center", "R&D ${x}"}},
			src: `resource "aws_s3_bucket" "data" {
  tags = {
    "Brand" = "Acme"
  }
}
`,
			want: `resource "aws_s3_bucket" "data" {
  tags = {
    "Brand"       = "Acme"
    "cost center" = "R&D $${x}"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:    "a quoted key matches",
			edits:   []Edit{{"Brand", "Acme"}},
			src:     "resource \"aws_s3_bucket\" \"data\" {\n  tags = {\n    \"Brand\" = \"Acme\"\n  }\n}\n",
			changed: []bool{false},
		},
		{
			name:    "Azure tag names ignore case",
			edits:   []Edit{{"Environment", "Production"}},
			sameKey: strings.EqualFold,
			src:     "resource \"aws_s3_bucket\" \"data\" {\n  tags = {\n    environment = \"Production\"\n  }\n}\n",
			changed: []bool{false},
		},
		{
			name:  "a file not in canonical format is not reformatted",
			edits: []Edit{{"Environment", "Production"}},
			src: `resource "aws_s3_bucket" "data" {
  bucket   =    "acme-data"
  tags = {
    Environment = "prod"
  }
}
`,
			want: `resource "aws_s3_bucket" "data" {
  bucket   =    "acme-data"
  tags = {
    Environment = "Production"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:  "module call",
			block: Block{Type: "module", Labels: []string{"reports"}, Attr: "tags"},
			edits: []Edit{{"OwningOrg", "Platform"}},
			src: `module "reports" {
  source = "./modules/reports"
  tags = {
    Brand = "Acme"
  }
}
`,
			want: `module "reports" {
  source = "./modules/reports"
  tags = {
    Brand     = "Acme"
    OwningOrg = "Platform"
  }
}
`,
			changed: []bool{true},
		},
		{
			name:    "module call with no tags argument",
			block:   Block{Type: "module", Labels: []string{"reports"}, Attr: "tags"},
			edits:   []Edit{{"OwningOrg", "Platform"}},
			src:     "module \"reports\" {\n  source = \"./modules/reports\"\n}\n",
			changed: []bool{false},
			reason:  model.UnroutableUnsupported,
		},
		{
			name:    "tags from a variable",
			edits:   []Edit{{"OwningOrg", "Platform"}},
			src:     "resource \"aws_s3_bucket\" \"data\" {\n  tags = var.tags\n}\n",
			changed: []bool{false},
			reason:  model.UnroutableUnsupported,
		},
		{
			name:    "a value from an expression is not replaced",
			edits:   []Edit{{"Environment", "Production"}},
			src:     "resource \"aws_s3_bucket\" \"data\" {\n  tags = {\n    Environment = var.env\n  }\n}\n",
			changed: []bool{false},
			reason:  model.UnroutableUnsupported,
		},
		{
			name:    "an interpolated value is not replaced",
			edits:   []Edit{{"Environment", "Production"}},
			src:     "resource \"aws_s3_bucket\" \"data\" {\n  tags = {\n    Environment = \"${var.env}-x\"\n  }\n}\n",
			changed: []bool{false},
			reason:  model.UnroutableUnsupported,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := tc.block
			if b.Type == "" {
				b = bucket
			}
			same := tc.sameKey
			if same == nil {
				same = exact
			}
			out, res, found, err := editFile([]byte(tc.src), "main.tf", b, tc.edits, same)
			if err != nil || !found {
				t.Fatalf("found %v, err %v", found, err)
			}
			want := tc.want
			if want == "" {
				want = tc.src
			}
			if string(out) != want {
				t.Errorf("got\n%s\nwant\n%s", out, want)
			}
			for i, r := range res {
				if r.Changed != tc.changed[i] || r.Reason != tc.reason {
					t.Errorf("edit %d: %+v, want changed %v reason %q", i, r, tc.changed[i], tc.reason)
				}
			}
		})
	}
}

func TestEditFileIdempotent(t *testing.T) {
	src := []byte("resource \"aws_s3_bucket\" \"data\" {\n  tags = merge(local.t, var.x)\n}\n")
	b := Block{Type: "resource", Labels: []string{"aws_s3_bucket", "data"}, Attr: "tags"}
	edits := []Edit{{"OwningOrg", "Platform"}}
	once, _, _, _ := editFile(src, "main.tf", b, edits, exact)
	twice, res, _, _ := editFile(once, "main.tf", b, edits, exact)
	if string(once) != string(twice) || res[0].Changed {
		t.Errorf("second edit changed the file:\n%s", twice)
	}
}

func TestEditFileNoBlock(t *testing.T) {
	_, _, found, err := editFile([]byte("resource \"aws_s3_bucket\" \"other\" {}\n"), "main.tf",
		Block{Type: "resource", Labels: []string{"aws_s3_bucket", "data"}, Attr: "tags"}, []Edit{{"A", "b"}}, exact)
	if found || err != nil {
		t.Errorf("found %v, err %v", found, err)
	}
	if _, _, _, err := editFile([]byte("resource {"), "bad.tf", Block{}, []Edit{{"A", "b"}}, exact); err == nil {
		t.Error("a parse error was not reported")
	}
}
