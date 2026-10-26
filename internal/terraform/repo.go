package terraform

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
)

// The SCMs tagherd opens pull requests on.
const (
	GitHub    = "github"
	Bitbucket = "bitbucket"
)

// scmOf is the canonical SCM name, or "" when tagherd does not support it.
func scmOf(s string) string {
	switch strings.ToLower(s) {
	case GitHub:
		return GitHub
	case Bitbucket:
		return Bitbucket
	}
	return ""
}

var repoName = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// allowed checks a repository against the allowlist and returns the
// unroutable detail when it fails. SCM, org and repo match ignoring case;
// org and repo patterns are path.Match globs.
func allowed(ref model.RepoRef, list []config.Repository) string {
	if scmOf(ref.SCM) == "" {
		return fmt.Sprintf("%s: SCM %q is not GitHub or BitBucket", model.UnroutableNotAllowlisted, ref.SCM)
	}
	for _, s := range []string{ref.Org, ref.Repo} {
		if !repoName.MatchString(s) || s == "." || s == ".." {
			return fmt.Sprintf("%s: %q is not a valid repository name", model.UnroutableNotAllowlisted, s)
		}
	}
	glob := func(pattern, s string) bool {
		ok, err := path.Match(strings.ToLower(pattern), strings.ToLower(s))
		return ok && err == nil
	}
	for _, a := range list {
		if strings.EqualFold(a.SCM, ref.SCM) && glob(a.Org, ref.Org) && glob(a.Repo, ref.Repo) {
			return ""
		}
	}
	return fmt.Sprintf("%s: %s is not in terraform.allowed_repositories", model.UnroutableNotAllowlisted, ref)
}

// RemoteURL is the HTTPS clone URL of a repository.
func RemoteURL(ref model.RepoRef) (string, error) {
	switch scmOf(ref.SCM) {
	case GitHub:
		return "https://github.com/" + ref.Org + "/" + ref.Repo + ".git", nil
	case Bitbucket:
		return "https://bitbucket.org/" + ref.Org + "/" + ref.Repo + ".git", nil
	}
	return "", fmt.Errorf("unsupported SCM %q", ref.SCM)
}

// gitRepo runs git in a clone. The SCM token goes in an HTTP header set
// through the environment, never in a URL or an argument.
type gitRepo struct {
	dir string
	env []string
}

func newGit(dir, scm, token string) gitRepo {
	env := []string{"GIT_TERMINAL_PROMPT=0"}
	if token != "" {
		user := "x-access-token"
		if scm == Bitbucket {
			user = "x-token-auth"
		}
		basic := base64.StdEncoding.EncodeToString([]byte(user + ":" + token))
		env = append(env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader",
			"GIT_CONFIG_VALUE_0=Authorization: Basic "+basic)
	}
	return gitRepo{dir: dir, env: env}
}

func (g *gitRepo) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(), g.env...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, lastLines(stderr.String(), 3))
	}
	return string(out), nil
}

// clone makes a shallow clone of the default branch into g.dir and
// returns the branch name.
func (g *gitRepo) clone(ctx context.Context, remote string) (string, error) {
	if _, err := g.run(ctx, "clone", "--quiet", "--depth", "1", remote, "."); err != nil {
		return "", err
	}
	// Commits need an identity; use tagherd's when git has none.
	if _, err := g.run(ctx, "config", "user.email"); err != nil {
		g.env = append(g.env, "GIT_AUTHOR_NAME=tagherd", "GIT_AUTHOR_EMAIL=tagherd@localhost",
			"GIT_COMMITTER_NAME=tagherd", "GIT_COMMITTER_EMAIL=tagherd@localhost")
	}
	branch, err := g.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	return strings.TrimSpace(branch), err
}

// PullRequest is one pull request to open.
type PullRequest struct {
	Title, Body string
	Head, Base  string // branch names
}

// PullRequester opens pull requests.
type PullRequester interface {
	Open(ctx context.Context, ref model.RepoRef, pr PullRequest) (string, error)
}

// API opens pull requests through the GitHub REST API and the Bitbucket
// Cloud 2.0 API.
type API struct {
	Tokens       config.SCMTokens
	Client       *http.Client
	GitHubURL    string // default https://api.github.com
	BitbucketURL string // default https://api.bitbucket.org/2.0
}

func (a API) Open(ctx context.Context, ref model.RepoRef, pr PullRequest) (string, error) {
	switch scmOf(ref.SCM) {
	case GitHub:
		return a.github(ctx, ref, pr)
	case Bitbucket:
		return a.bitbucket(ctx, ref, pr)
	}
	return "", fmt.Errorf("unsupported SCM %q", ref.SCM)
}

func (a API) github(ctx context.Context, ref model.RepoRef, pr PullRequest) (string, error) {
	if a.Tokens.GitHub == "" {
		return "", errors.New("terraform.scm_tokens.github is not set")
	}
	base := cmp.Or(a.GitHubURL, "https://api.github.com")
	repo := base + "/repos/" + url.PathEscape(ref.Org) + "/" + url.PathEscape(ref.Repo)
	var created struct {
		HTMLURL string `json:"html_url"`
	}
	status, err := a.call(ctx, http.MethodPost, repo+"/pulls", a.Tokens.GitHub, map[string]string{
		"title": pr.Title, "body": pr.Body, "head": pr.Head, "base": pr.Base,
	}, &created)
	if status == http.StatusUnprocessableEntity {
		// Most often the pull request is already open, from an earlier run.
		var open []struct {
			HTMLURL string `json:"html_url"`
		}
		q := url.Values{"head": {ref.Org + ":" + pr.Head}, "state": {"open"}}
		if _, lerr := a.call(ctx, http.MethodGet, repo+"/pulls?"+q.Encode(), a.Tokens.GitHub, nil, &open); lerr == nil && len(open) > 0 {
			return open[0].HTMLURL, nil
		}
	}
	return created.HTMLURL, err
}

func (a API) bitbucket(ctx context.Context, ref model.RepoRef, pr PullRequest) (string, error) {
	if a.Tokens.Bitbucket == "" {
		return "", errors.New("terraform.scm_tokens.bitbucket is not set")
	}
	base := cmp.Or(a.BitbucketURL, "https://api.bitbucket.org/2.0")
	branch := func(name string) map[string]any { return map[string]any{"branch": map[string]string{"name": name}} }
	var created struct {
		Links struct {
			HTML struct {
				Href string `json:"href"`
			} `json:"html"`
		} `json:"links"`
	}
	_, err := a.call(ctx, http.MethodPost,
		base+"/repositories/"+url.PathEscape(ref.Org)+"/"+url.PathEscape(ref.Repo)+"/pullrequests", a.Tokens.Bitbucket,
		map[string]any{
			"title": pr.Title, "description": pr.Body,
			"source": branch(pr.Head), "destination": branch(pr.Base), "close_source_branch": true,
		}, &created)
	return created.Links.HTML.Href, err
}

// call sends a JSON request and decodes a 2xx JSON response into out.
func (a API) call(ctx context.Context, method, u, token string, body, out any) (int, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, r)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := a.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, fmt.Errorf("%s %s: %s: %s", method, req.URL.Path, resp.Status, strings.TrimSpace(string(data[:min(len(data), 300)])))
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}
