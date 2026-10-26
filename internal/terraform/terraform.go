// Package terraform makes a plan's Terraform-route changes. For each
// repository named by the resources' SCM, Org and Repo tags it checks the
// allowlist, makes a shallow clone, finds each resource's address in a
// root's state, and edits the tags where the code sets them. The result is
// one patch per repository and, with pull requests on, one branch and one
// pull request. It never runs terraform plan or apply. A change that has no
// place in code is unroutable, with a reason, and nothing is written for it.
package terraform

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/overad9658/tagherd/internal/config"
	"github.com/overad9658/tagherd/internal/model"
)

const unsupportedReason = model.UnroutableUnsupported

// DefaultBranchPrefix starts the branch name when the config has none.
const DefaultBranchPrefix = "tagherd/"

// Options control a Router.
type Options struct {
	Allowed      []config.Repository
	Tokens       config.SCMTokens
	BranchPrefix string
	PullRequests bool   // push a branch and open a pull request per repository
	PatchDir     string // where patch files go
	Concurrency  int    // repositories worked on in parallel
	WorkDir      string // where clones go; default the system temp directory

	// RemoteURL, States and Pulls default to the real services.
	RemoteURL func(model.RepoRef) (string, error)
	States    StateReader
	Pulls     PullRequester
	Log       *slog.Logger
}

// Router makes Terraform changes.
type Router struct{ opts Options }

// New returns a Router, filling defaults.
func New(opts Options) *Router {
	opts.BranchPrefix = cmp.Or(opts.BranchPrefix, DefaultBranchPrefix)
	opts.PatchDir = cmp.Or(opts.PatchDir, "patches")
	if opts.RemoteURL == nil {
		opts.RemoteURL = RemoteURL
	}
	if opts.States == nil {
		opts.States = CLI{}
	}
	if opts.Pulls == nil {
		opts.Pulls = API{Tokens: opts.Tokens}
	}
	if opts.Log == nil {
		opts.Log = slog.New(slog.DiscardHandler)
	}
	return &Router{opts: opts}
}

// Actor names who made the edits, for the audit log: the local user. The
// pull request itself records the SCM token's owner.
func (rt *Router) Actor() string {
	if u, err := user.Current(); err == nil {
		return "local:" + u.Username
	}
	return "local:unknown"
}

// Run makes the changes and returns one result per change, in order.
// Outcomes are proposed (in a patch, and a pull request when on), noop
// (the code already sets the value), unroutable or failed.
func (rt *Router) Run(ctx context.Context, planID string, cs []model.Change) []model.ChangeResult {
	out := make([]model.ChangeResult, len(cs))
	byRepo := map[string][]int{}
	var order []string
	for i, c := range cs {
		out[i].Change = c
		if c.Repo == nil {
			out[i].Outcome = model.OutcomeUnroutable
			out[i].Detail = model.UnroutableMissingRepoTags + ": the resource needs SCM, Org and Repo tags"
			continue
		}
		if why := allowed(*c.Repo, rt.opts.Allowed); why != "" {
			out[i].Outcome, out[i].Detail = model.OutcomeUnroutable, why
			continue
		}
		k := strings.ToLower(c.Repo.String())
		if byRepo[k] == nil {
			order = append(order, k)
		}
		byRepo[k] = append(byRepo[k], i)
	}

	sem := make(chan struct{}, max(rt.opts.Concurrency, 1))
	var wg sync.WaitGroup
	for _, k := range order {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rt.repo(ctx, planID, byRepo[k], out)
		}()
	}
	wg.Wait()
	return out
}

// located is where a resource was found.
type located struct {
	root string
	addr Address
}

// repo makes one repository's changes; it writes only out[idx].
func (rt *Router) repo(ctx context.Context, planID string, idx []int, out []model.ChangeResult) {
	ref := *out[idx[0]].Repo
	scm := scmOf(ref.SCM)
	fail := func(ids []int, err error) {
		for _, i := range ids {
			out[i].Outcome, out[i].Detail = model.OutcomeFailed, err.Error()
		}
	}
	dir, err := os.MkdirTemp(rt.opts.WorkDir, "tagherd-repo-")
	if err != nil {
		fail(idx, err)
		return
	}
	defer func() { _ = os.RemoveAll(dir) }()

	remote, err := rt.opts.RemoteURL(ref)
	if err != nil {
		fail(idx, err)
		return
	}
	g := newGit(dir, scm, rt.token(scm))
	base, err := g.clone(ctx, remote)
	if err != nil {
		fail(idx, fmt.Errorf("cloning %s: %w", ref, err))
		return
	}
	roots, err := findRoots(dir)
	if err != nil {
		fail(idx, fmt.Errorf("finding Terraform roots in %s: %w", ref, err))
		return
	}

	where, unread := rt.find(ctx, dir, roots, idx, out)
	var found []int
	for _, i := range idx {
		c := out[i].Change
		loc, ok := where[c.ResourceID]
		switch {
		case ok:
			out[i].Terraform = &model.TerraformTarget{Root: loc.root, Address: loc.addr.Full}
			found = append(found, i)
		case len(unread) > 0:
			fail([]int{i}, fmt.Errorf("not in any state that could be read; unreadable: %s", strings.Join(unread, "; ")))
		case len(roots) == 0:
			out[i].Outcome = model.OutcomeUnroutable
			out[i].Detail = model.UnroutableNotInState + ": the repository has no Terraform root (a directory with a backend block)"
		default:
			out[i].Outcome = model.OutcomeUnroutable
			out[i].Detail = fmt.Sprintf("%s: not in the state of %d root(s): %s", model.UnroutableNotInState, len(roots), strings.Join(roots, ", "))
		}
	}

	files := rt.edit(dir, found, where, out)
	if len(files) == 0 {
		return
	}
	var proposed []int
	for _, i := range found {
		if out[i].Outcome == model.OutcomeProposed {
			proposed = append(proposed, i)
		}
	}
	diff, err := g.run(ctx, append([]string{"diff", "--"}, files...)...)
	if err != nil {
		fail(proposed, err)
		return
	}
	patch, err := rt.writePatch(scm, ref, diff)
	if err != nil {
		fail(proposed, err)
		return
	}
	rt.opts.Log.Info("patch written", "repo", ref.String(), "patch", patch, "changes", len(proposed))
	for _, i := range proposed {
		out[i].Terraform.Patch, out[i].Detail = patch, patch
	}
	if !rt.opts.PullRequests {
		return
	}

	pr := rt.pullRequest(planID, base, ref, proposed, out)
	steps := [][]string{
		{"checkout", "--quiet", "-b", pr.Head},
		append([]string{"add", "--"}, files...),
		{"commit", "--quiet", "-m", pr.Title + "\n\n" + commitBody(proposed, out)},
		{"push", "--quiet", "--force", "origin", "HEAD:refs/heads/" + pr.Head},
	}
	for _, s := range steps {
		if _, err := g.run(ctx, s...); err != nil {
			fail(proposed, fmt.Errorf("patch written to %s, but the branch could not be pushed: %w", patch, err))
			return
		}
	}
	url, err := rt.opts.Pulls.Open(ctx, ref, pr)
	if err != nil {
		fail(proposed, fmt.Errorf("patch written to %s and branch %s pushed, but the pull request failed: %w", patch, pr.Head, err))
		return
	}
	rt.opts.Log.Info("pull request opened", "repo", ref.String(), "url", url)
	for _, i := range proposed {
		out[i].Terraform.PullURL, out[i].Detail = url, url
	}
}

func (rt *Router) token(scm string) string {
	if scm == Bitbucket {
		return rt.opts.Tokens.Bitbucket
	}
	return rt.opts.Tokens.GitHub
}

// find reads root states in order until every resource is found. It
// returns where each one is, and the roots that could not be read.
func (rt *Router) find(ctx context.Context, dir string, roots []string, idx []int, out []model.ChangeResult) (map[string]located, []string) {
	want := map[string]model.Change{}
	for _, i := range idx {
		want[out[i].ResourceID] = out[i].Change
	}
	where := map[string]located{}
	var unread []string
	for _, root := range roots {
		if len(want) == 0 {
			break
		}
		show, err := rt.opts.States.Show(ctx, filepath.Join(dir, root))
		var addrs map[string]Address
		if err == nil {
			addrs, err = locate(show, want)
		}
		if err != nil {
			rt.opts.Log.Warn("Terraform state could not be read", "root", root, "error", err)
			unread = append(unread, root+": "+err.Error())
			continue
		}
		for id, a := range addrs {
			where[id] = located{root: root, addr: a}
			delete(want, id)
		}
	}
	return where, unread
}

type blockKey struct {
	root  string
	block string
}

// edit makes the located changes in code and returns the files changed,
// relative to the repository.
func (rt *Router) edit(dir string, idx []int, where map[string]located, out []model.ChangeResult) []string {
	groups := map[blockKey][]int{}
	blocks := map[blockKey]Block{}
	var order []blockKey
	for _, i := range idx {
		c := out[i].Change
		b, why := blockFor(c, where[c.ResourceID].addr)
		if why != "" {
			out[i].Outcome, out[i].Detail = model.OutcomeUnroutable, unsupportedReason+": "+why
			continue
		}
		k := blockKey{out[i].Terraform.Root, b.String()}
		if groups[k] == nil {
			order = append(order, k)
			blocks[k] = b
		}
		groups[k] = append(groups[k], i)
	}

	var files []string
	for _, k := range order {
		if f := rt.editBlock(dir, k.root, blocks[k], groups[k], out); f != "" && !slices.Contains(files, f) {
			files = append(files, f)
		}
	}
	slices.Sort(files)
	return files
}

// editBlock makes one block's changes. Changes that set one key to
// different values in the same block, as two resources behind one module
// call can, are unroutable.
func (rt *Router) editBlock(dir, root string, b Block, idx []int, out []model.ChangeResult) string {
	values := map[string]string{}
	conflict := map[string]bool{}
	for _, i := range idx {
		e := editFor(out[i].Change)
		if v, ok := values[e.Key]; ok && v != e.Value {
			conflict[e.Key] = true
		}
		values[e.Key] = e.Value
	}
	var edits []Edit
	for _, k := range sortedKeys(values) {
		if !conflict[k] {
			edits = append(edits, Edit{Key: k, Value: values[k]})
		}
	}
	for _, i := range idx {
		if e := editFor(out[i].Change); conflict[e.Key] {
			out[i].Outcome = model.OutcomeUnroutable
			out[i].Detail = fmt.Sprintf("%s: changes behind %s set %s to different values", unsupportedReason, b, e.Key)
		}
	}
	if len(edits) == 0 {
		return ""
	}

	sameKey := func(a, b string) bool { return a == b }
	if out[idx[0]].Cloud == model.Azure {
		sameKey = strings.EqualFold
	}
	tfFiles, _ := filepath.Glob(filepath.Join(dir, root, "*.tf"))
	slices.Sort(tfFiles)
	for _, path := range tfFiles {
		src, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(dir, path)
		edited, res, found, err := editFile(src, rel, b, edits, sameKey)
		if err != nil {
			rt.opts.Log.Warn("Terraform file could not be parsed", "file", rel, "error", err)
			continue
		}
		if !found {
			continue
		}
		changed := string(edited) != string(src)
		if changed {
			if err := os.WriteFile(path, edited, 0o644); err != nil {
				for _, i := range idx {
					if !conflict[editFor(out[i].Change).Key] {
						out[i].Outcome, out[i].Detail = model.OutcomeFailed, err.Error()
					}
				}
				return ""
			}
		}
		byKey := map[string]EditResult{}
		for j, e := range edits {
			byKey[e.Key] = res[j]
		}
		for _, i := range idx {
			r, ok := byKey[editFor(out[i].Change).Key]
			if !ok {
				continue // a conflict, already reported
			}
			out[i].Terraform.File, out[i].Terraform.Block = rel, b.String()
			switch {
			case r.Reason != "":
				out[i].Outcome, out[i].Detail = model.OutcomeUnroutable, r.Reason+": "+r.Detail
			case r.Changed:
				out[i].Outcome = model.OutcomeProposed
			default:
				out[i].Outcome, out[i].Detail = model.OutcomeNoop, "already set in "+rel
			}
		}
		if changed {
			return rel
		}
		return ""
	}
	for _, i := range idx {
		if !conflict[editFor(out[i].Change).Key] {
			out[i].Outcome = model.OutcomeUnroutable
			out[i].Detail = fmt.Sprintf("%s: no %s block in %s", model.UnroutableNotInCode, b, root)
		}
	}
	return ""
}

// blockFor is the block a change is made in: the resource itself, or the
// call in the root to the module it sits in. It returns why there is none.
func blockFor(c model.Change, a Address) (Block, string) {
	attr := "tags"
	if c.Cloud == model.GCP {
		if c.Kind == model.KindRMTag {
			return Block{}, "Resource Manager tags are bound by google_tags_tag_binding resources, which tagherd does not edit"
		}
		attr = "labels"
	}
	if len(a.Modules) > 0 {
		return Block{Type: "module", Labels: []string{a.Modules[0]}, Attr: attr}, ""
	}
	if a.Type == "" {
		return Block{}, "the state address could not be read"
	}
	return Block{Type: "resource", Labels: []string{a.Type, a.Name}, Attr: attr}, ""
}

// editFor is the key and value written in code: GCP labels are lowercase.
func editFor(c model.Change) Edit {
	if c.Cloud == model.GCP && c.Kind == model.KindLabel {
		return Edit{Key: strings.ToLower(c.Tag), Value: strings.ToLower(c.New)}
	}
	return Edit{Key: c.Tag, Value: c.New}
}

func (rt *Router) writePatch(scm string, ref model.RepoRef, diff string) (string, error) {
	if err := os.MkdirAll(rt.opts.PatchDir, 0o755); err != nil {
		return "", fmt.Errorf("--patch-dir: %w", err)
	}
	name := strings.ToLower(scm + "-" + ref.Org + "-" + ref.Repo + ".patch")
	path := filepath.Join(rt.opts.PatchDir, name)
	if err := os.WriteFile(path, []byte(diff), 0o600); err != nil {
		return "", fmt.Errorf("writing the patch: %w", err)
	}
	return path, nil
}

func (rt *Router) pullRequest(planID, base string, ref model.RepoRef, idx []int, out []model.ChangeResult) PullRequest {
	var b strings.Builder
	fmt.Fprintf(&b, "tagherd plan `%s` sets these tags in %s:\n\n", planID, ref)
	b.WriteString("| Resource | Address | Tag | Old | New | Rule |\n|---|---|---|---|---|---|\n")
	for _, i := range idx {
		c := out[i]
		fmt.Fprintf(&b, "| %s | `%s` | %s | %s | %s | %s |\n", cell(cmp.Or(c.Name, c.ResourceID)), c.Terraform.Address,
			cell(c.Tag), cell(c.Old), cell(c.New), c.Rule)
	}
	b.WriteString("\nOnly tags change. Review and merge, then run Terraform as usual; tagherd never runs terraform apply.\n")
	return PullRequest{
		Title: "tagherd: set tags from plan " + planID,
		Body:  b.String(),
		Head:  rt.opts.BranchPrefix + planID,
		Base:  base,
	}
}

func commitBody(idx []int, out []model.ChangeResult) string {
	var lines []string
	for _, i := range idx {
		c := out[i]
		lines = append(lines, fmt.Sprintf("- %s: %s = %q (%s)", c.Terraform.Address, c.Tag, c.New, c.Rule))
	}
	return strings.Join(lines, "\n")
}

func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
