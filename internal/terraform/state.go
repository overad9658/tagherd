package terraform

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
)

// StateReader returns `terraform show -json` output for a root directory.
type StateReader interface {
	Show(ctx context.Context, dir string) ([]byte, error)
}

// CLI reads state with the terraform binary: init, then show -json. Module
// registry tokens are passed as TF_TOKEN_<host>, so remote module sources
// resolve; the backend's own credentials come from the environment.
type CLI struct {
	Bin        string // default: terraform on PATH
	Registries []config.ModuleRegistry
}

func (c CLI) Show(ctx context.Context, dir string) ([]byte, error) {
	bin := c.Bin
	if bin == "" {
		bin = "terraform"
	}
	if _, err := exec.LookPath(bin); err != nil {
		return nil, fmt.Errorf("terraform is needed to read state: %w", err)
	}
	env := append(os.Environ(), "TF_IN_AUTOMATION=1", "TF_INPUT=0")
	for _, r := range c.Registries {
		if host := registryHost(r.URL); host != "" && r.Token != "" {
			env = append(env, tokenVar(host)+"="+r.Token)
		}
	}
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = dir, env
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("terraform %s: %w: %s", args[0], err, lastLines(stderr.String(), 5))
		}
		return out, nil
	}
	if _, err := run("init", "-input=false", "-no-color"); err != nil {
		return nil, err
	}
	return run("show", "-json", "-no-color")
}

func registryHost(u string) string {
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	p, err := url.Parse(u)
	if err != nil {
		return ""
	}
	return p.Hostname()
}

// tokenVar is Terraform's credentials variable for a host: dots become
// underscores and dashes double underscores.
func tokenVar(host string) string {
	return "TF_TOKEN_" + strings.ReplaceAll(strings.ReplaceAll(host, "-", "__"), ".", "_")
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.Join(lines[max(0, len(lines)-n):], " ")
}

// findRoots lists the directories, relative to the repository, whose
// terraform block declares a backend or cloud block.
func findRoots(repo string) ([]string, error) {
	seen := map[string]bool{}
	var roots []string
	err := filepath.WalkDir(repo, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != repo && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".tf" || !hasBackend(path) {
			return nil
		}
		rel, _ := filepath.Rel(repo, filepath.Dir(path))
		if !seen[rel] {
			seen[rel] = true
			roots = append(roots, rel)
		}
		return nil
	})
	sort.Strings(roots)
	return roots, err
}

func hasBackend(path string) bool {
	src, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	file, diags := hclsyntax.ParseConfig(src, path, hcl.InitialPos)
	if diags.HasErrors() {
		return false
	}
	for _, b := range file.Body.(*hclsyntax.Body).Blocks {
		if b.Type != "terraform" {
			continue
		}
		for _, inner := range b.Body.Blocks {
			if inner.Type == "backend" || inner.Type == "cloud" {
				return true
			}
		}
	}
	return false
}

// Address is a managed resource found in state.
type Address struct {
	Full       string   // module.reports.aws_lambda_function.this[0]
	Modules    []string // module call names from the root: [reports]
	Type, Name string
}

type showJSON struct {
	Values *struct {
		RootModule stateModule `json:"root_module"`
	} `json:"values"`
}

type stateModule struct {
	Address      string          `json:"address"`
	Resources    []stateResource `json:"resources"`
	ChildModules []stateModule   `json:"child_modules"`
}

type stateResource struct {
	Address string         `json:"address"`
	Mode    string         `json:"mode"`
	Type    string         `json:"type"`
	Name    string         `json:"name"`
	Values  map[string]any `json:"values"`
}

// locate finds each wanted change's resource in one root's state, keyed by
// resource ID.
func locate(show []byte, want map[string]model.Change) (map[string]Address, error) {
	var s showJSON
	if err := json.Unmarshal(show, &s); err != nil {
		return nil, fmt.Errorf("reading terraform show output: %w", err)
	}
	out := map[string]Address{}
	if s.Values == nil {
		return out, nil // no state yet
	}
	var walk func(m stateModule) error
	walk = func(m stateModule) error {
		mods, err := moduleNames(m.Address)
		if err != nil {
			return err
		}
		for _, r := range m.Resources {
			if r.Mode != "managed" {
				continue
			}
			for id, c := range want {
				if _, done := out[id]; !done && matches(c, r.Values) {
					out[id] = Address{Full: r.Address, Modules: mods, Type: r.Type, Name: r.Name}
				}
			}
		}
		for _, child := range m.ChildModules {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(s.Values.RootModule)
}

// moduleNames splits module.a["x"].module.b into [a b].
func moduleNames(addr string) ([]string, error) {
	var names []string
	for addr != "" {
		rest, ok := strings.CutPrefix(addr, "module.")
		if !ok {
			return nil, fmt.Errorf("unexpected module address %q", addr)
		}
		end := strings.IndexAny(rest, ".[")
		if end < 0 {
			end = len(rest)
		}
		names = append(names, rest[:end])
		rest = rest[end:]
		if strings.HasPrefix(rest, "[") {
			close := indexClose(rest)
			if close < 0 {
				return nil, fmt.Errorf("unexpected module address %q", addr)
			}
			rest = rest[close+1:]
		}
		addr = strings.TrimPrefix(rest, ".")
	}
	return names, nil
}

// indexClose finds the ] closing an index, skipping quoted strings.
func indexClose(s string) int {
	quoted := false
	for i := 0; i < len(s); i++ {
		switch {
		case quoted && s[i] == '\\':
			i++
		case s[i] == '"':
			quoted = !quoted
		case !quoted && s[i] == ']':
			return i
		}
	}
	return -1
}

// matches reports whether a state resource is the change's resource: by
// ARN or ID on AWS, by ID ignoring case on Azure, and on GCP by the asset
// name's path against id or self_link.
func matches(c model.Change, values map[string]any) bool {
	str := func(k string) string { s, _ := values[k].(string); return s }
	id := c.ResourceID
	switch c.Cloud {
	case model.AWS:
		return id != "" && (str("arn") == id || str("id") == id)
	case model.Azure:
		return id != "" && strings.EqualFold(str("id"), id)
	case model.GCP:
		path := gcpPath(id)
		if path == "" {
			return false
		}
		if str("id") == path || strings.HasSuffix(str("self_link"), "/"+path) {
			return true
		}
		// Buckets: //storage.googleapis.com/projects/_/buckets/NAME has id NAME.
		if name, ok := strings.CutPrefix(path, "projects/_/buckets/"); ok {
			return str("id") == name || str("name") == name
		}
	}
	return false
}

// gcpPath strips the service from a Cloud Asset Inventory name:
// //compute.googleapis.com/projects/p/... becomes projects/p/...
func gcpPath(name string) string {
	rest, ok := strings.CutPrefix(name, "//")
	if !ok {
		return ""
	}
	_, path, ok := strings.Cut(rest, "/")
	if !ok {
		return ""
	}
	return path
}
