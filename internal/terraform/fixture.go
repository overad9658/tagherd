package terraform

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/overad9658/tagherd/internal/model"
)

// Fixture stands in for the SCMs and Terraform, for tests and the hidden
// --terraform-fixture flag. Repositories come from Dir/<scm>/<org>/<repo>,
// each copied into a fresh git repository under Work that serves as the
// remote; each root's state is its show.json; pull requests are recorded,
// not opened.
type Fixture struct {
	Dir, Work string

	mu      sync.Mutex
	remotes map[string]string
	Opened  []PullRequest
}

func fixtureKey(ref model.RepoRef) string {
	return strings.ToLower(filepath.Join(ref.SCM, ref.Org, ref.Repo))
}

// Remote is the path of a repository's remote, once RemoteURL has made it.
func (f *Fixture) Remote(ref model.RepoRef) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remotes[fixtureKey(ref)]
}

// RemoteURL makes the repository's remote on first use.
func (f *Fixture) RemoteURL(ref model.RepoRef) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := fixtureKey(ref)
	if r, ok := f.remotes[key]; ok {
		return "file://" + r, nil
	}
	src := filepath.Join(f.Dir, key)
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("no fixture repository %s: %w", key, err)
	}
	dst, err := filepath.Abs(filepath.Join(f.Work, "remotes", key))
	if err != nil {
		return "", err
	}
	if err := copyTree(src, dst); err != nil {
		return "", err
	}
	for _, args := range [][]string{
		{"init", "--quiet", "--initial-branch", "main"},
		{"add", "--all"},
		{"commit", "--quiet", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dst
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@localhost",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@localhost")
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("git %s: %w: %s", args[0], err, out)
		}
	}
	if f.remotes == nil {
		f.remotes = map[string]string{}
	}
	f.remotes[key] = dst
	return "file://" + dst, nil
}

// Show returns the root's show.json.
func (f *Fixture) Show(_ context.Context, dir string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, "show.json"))
}

// Open records the pull request.
func (f *Fixture) Open(_ context.Context, ref model.RepoRef, pr PullRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Opened = append(f.Opened, pr)
	return fmt.Sprintf("https://%s.example/%s/%s/pull/%d", scmOf(ref.SCM), ref.Org, ref.Repo, len(f.Opened)), nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
