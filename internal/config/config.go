// Package config loads tagherd.yaml. Secrets never live in the file: any
// string value may reference an environment variable as ${NAME}.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// FileName is the config file tagherd looks for.
const FileName = "tagherd.yaml"

// ErrInvalid is wrapped by every configuration error.
var ErrInvalid = errors.New("invalid configuration")

// Config mirrors tagherd.yaml.
type Config struct {
	PolicyDir     string    `yaml:"policy_dir"`
	OwnershipFile string    `yaml:"ownership_file"`
	DefaultsFile  string    `yaml:"defaults_file"`
	SynonymsFile  string    `yaml:"synonyms_file"`
	Scopes        Scopes    `yaml:"scopes"`
	Terraform     Terraform `yaml:"terraform"`
	Wiz           *Wiz      `yaml:"wiz"`

	// Path is the file this config came from; empty when none was found.
	Path string `yaml:"-"`
	// Unset lists the ${NAME} references whose variable was not set, by
	// YAML path. Values that need them must call Require before use.
	Unset map[string]string `yaml:"-"`
}

type Scopes struct {
	AWS   *AWSScopes   `yaml:"aws"`
	Azure *AzureScopes `yaml:"azure"`
	GCP   *GCPScopes   `yaml:"gcp"`
}

type AWSScopes struct {
	Organization    bool     `yaml:"organization"`
	Accounts        []string `yaml:"accounts"`
	ExcludeAccounts []string `yaml:"exclude_accounts"`
	ReadRoleName    string   `yaml:"read_role_name"`
	WriteRoleName   string   `yaml:"write_role_name"`
	Profile         string   `yaml:"profile"`
	// Regions to read; default: the regions enabled in each account.
	Regions []string `yaml:"regions"`
	// ConfigAggregator is queried for every account when set; without it,
	// AWS Config is queried in each account and region.
	ConfigAggregator *AWSAggregator `yaml:"config_aggregator"`
}

// AWSAggregator names an AWS Config aggregator, read with the base credentials.
type AWSAggregator struct {
	Name   string `yaml:"name"`
	Region string `yaml:"region"`
}

type AzureScopes struct {
	TenantID      string   `yaml:"tenant_id"`
	Subscriptions []string `yaml:"subscriptions"`
}

type GCPScopes struct {
	OrganizationID string   `yaml:"organization_id"`
	Folders        []string `yaml:"folders"`
	Projects       []string `yaml:"projects"`
	// CredentialsFile is a service account key; default: Application
	// Default Credentials.
	CredentialsFile string `yaml:"credentials_file"`
}

type Terraform struct {
	AllowedRepositories []Repository     `yaml:"allowed_repositories"`
	PullRequests        PullRequests     `yaml:"pull_requests"`
	ModuleRegistries    []ModuleRegistry `yaml:"module_registries"`
	SCMTokens           SCMTokens        `yaml:"scm_tokens"`
}

type Repository struct {
	SCM  string `yaml:"scm"`
	Org  string `yaml:"org"`
	Repo string `yaml:"repo"`
}

type PullRequests struct {
	BranchPrefix string `yaml:"branch_prefix"`
}

type ModuleRegistry struct {
	Type  string `yaml:"type"`
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

type SCMTokens struct {
	GitHub    string `yaml:"github"`
	Bitbucket string `yaml:"bitbucket"`
}

type Wiz struct {
	APIURL       string `yaml:"api_url"`
	AuthURL      string `yaml:"auth_url"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	Timeout      string `yaml:"timeout"`
}

// Require returns an error naming every listed YAML path whose ${NAME}
// reference was unset, so a missing secret fails only the feature using it.
func (c *Config) Require(paths ...string) error {
	var missing []string
	for _, p := range paths {
		for up, name := range c.Unset {
			if up == p || strings.HasPrefix(up, p+".") || strings.HasPrefix(up, p+"[") {
				missing = append(missing, fmt.Sprintf("%s (${%s})", up, name))
			}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return fmt.Errorf("%w: environment variable not set for %s", ErrInvalid, strings.Join(missing, ", "))
}

// Find returns the config file to load: explicit when given (it must exist),
// else ./tagherd.yaml, else ~/.config/tagherd/tagherd.yaml. An empty result
// with no error means no file exists and defaults apply.
func Find(explicit string) (string, error) {
	if explicit != "" {
		if _, err := os.Stat(explicit); err != nil {
			return "", fmt.Errorf("%w: config file %s: %w", ErrInvalid, explicit, errors.Unwrap(err))
		}
		return explicit, nil
	}
	candidates := []string{FileName}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, filepath.Join(home, ".config", "tagherd", FileName))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", nil
}

// Load reads path (see Find). Relative file paths inside the config resolve
// against the config file's directory.
func Load(path string) (*Config, error) {
	if path == "" {
		return &Config{Unset: map[string]string{}}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	cfg, err := Parse(data, os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	base := filepath.Dir(path)
	for _, p := range []*string{&cfg.PolicyDir, &cfg.OwnershipFile, &cfg.DefaultsFile, &cfg.SynonymsFile} {
		if *p != "" && !filepath.IsAbs(*p) {
			*p = filepath.Join(base, *p)
		}
	}
	return cfg, nil
}

// Parse decodes YAML after expanding ${NAME} references with lookup.
func Parse(data []byte, lookup func(string) (string, bool)) (*Config, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	cfg := &Config{Unset: map[string]string{}}
	if len(root.Content) == 0 {
		return cfg, nil // empty file
	}
	expand(root.Content[0], "", lookup, cfg.Unset)

	// Re-encode the expanded tree so the decoder can reject unknown keys.
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(root.Content[0]); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return cfg, nil
}

// envRef matches ${NAME}; a bare $NAME is left alone.
var envRef = regexp.MustCompile(`\$\{[A-Za-z_][A-Za-z0-9_]*\}`)

// expand replaces ${NAME} in every scalar and records unset names by path.
func expand(n *yaml.Node, path string, lookup func(string) (string, bool), unset map[string]string) {
	switch n.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			p := key
			if path != "" {
				p = path + "." + key
			}
			expand(n.Content[i+1], p, lookup, unset)
		}
	case yaml.SequenceNode:
		for i, c := range n.Content {
			expand(c, fmt.Sprintf("%s[%d]", path, i), lookup, unset)
		}
	case yaml.ScalarNode:
		if !strings.Contains(n.Value, "${") {
			return
		}
		n.Value = envRef.ReplaceAllStringFunc(n.Value, func(ref string) string {
			name := ref[2 : len(ref)-1]
			v, ok := lookup(name)
			if !ok {
				unset[path] = name
			}
			return v
		})
		n.Style = yaml.DoubleQuotedStyle // keep expanded values as strings
		n.Tag = "!!str"
	}
}
