package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/overad9658/tagherd/internal/model"
	"github.com/overad9658/tagherd/internal/output"
	"github.com/overad9658/tagherd/internal/provider"
	awsprovider "github.com/overad9658/tagherd/internal/provider/aws"
	azureprovider "github.com/overad9658/tagherd/internal/provider/azure"
	"github.com/overad9658/tagherd/internal/provider/fake"
	gcpprovider "github.com/overad9658/tagherd/internal/provider/gcp"
	"github.com/overad9658/tagherd/internal/scan"
	"github.com/overad9658/tagherd/internal/wiz"
)

// providers returns the discovery adapters: the fake provider for a
// fixture, else one adapter per cloud that tagherd.yaml has a scopes block
// for, limited by --cloud.
func (a *app) providers(ctx context.Context, fixture string) (map[model.Cloud]provider.Provider, scan.OverrideRunner, error) {
	if fixture != "" {
		env, err := fake.Load(fixture)
		if err != nil {
			return nil, nil, output.Usagef("--fixture: %v", err)
		}
		out := map[model.Cloud]provider.Provider{}
		for c, p := range env.Providers {
			out[c] = p
		}
		if env.Wiz == nil {
			return out, nil, nil
		}
		return out, env.Wiz, nil
	}

	sc := a.cfg.Scopes
	configured := map[model.Cloud]bool{model.AWS: sc.AWS != nil, model.Azure: sc.Azure != nil, model.GCP: sc.GCP != nil}
	var missing []string
	for _, c := range a.clouds {
		if !configured[c] {
			missing = append(missing, "scopes."+strings.ToLower(string(c)))
		}
	}
	where := "tagherd.yaml"
	if a.cfg.Path != "" {
		where = a.cfg.Path
	}
	if len(missing) > 0 {
		return nil, nil, &output.ExitError{Code: output.ExitUsage, Err: fmt.Errorf("--cloud: %s has no %s", where, strings.Join(missing, " or "))}
	}

	out := map[model.Cloud]provider.Provider{}
	var errs []error
	for _, c := range model.Clouds {
		if !configured[c] || !a.wantCloud(c) {
			continue
		}
		if err := a.cfg.Require("scopes." + strings.ToLower(string(c))); err != nil {
			errs = append(errs, err)
			continue
		}
		var (
			p   provider.Provider
			err error
		)
		switch c {
		case model.AWS:
			p, err = awsprovider.New(ctx, awsprovider.Options{Scopes: *sc.AWS, Profile: a.g.profile, Log: a.log})
		case model.Azure:
			p, err = azureprovider.New(azureprovider.Options{Scopes: *sc.Azure, Log: a.log})
		case model.GCP:
			p, err = gcpprovider.New(ctx, gcpprovider.Options{Scopes: *sc.GCP, Log: a.log})
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		out[c] = p
	}
	if len(errs) > 0 {
		return nil, nil, &output.ExitError{Code: output.ExitUsage, Err: errors.Join(errs...)}
	}
	if len(out) == 0 {
		return nil, nil, &output.ExitError{Code: output.ExitUsage, Err: fmt.Errorf(
			"no clouds to scan: %s has no scopes.aws, scopes.azure or scopes.gcp (run `tagherd init` for an example)", where)}
	}
	return out, a.wizClient(), nil
}

// wizClient is the override runner from the wiz block, or nil when there is
// none or it is incomplete; overrides then fall back to the default tag test,
// which reports more resources, never fewer.
func (a *app) wizClient() scan.OverrideRunner {
	w := a.cfg.Wiz
	if w == nil {
		return nil
	}
	fallback := func(err error) scan.OverrideRunner {
		a.log.Warn("Wiz is not usable; overrides fall back to the default tag test", "error", err)
		return nil
	}
	if err := a.cfg.Require("wiz"); err != nil {
		return fallback(err)
	}
	var timeout time.Duration
	if w.Timeout != "" {
		d, err := time.ParseDuration(w.Timeout)
		if err != nil || d <= 0 {
			return fallback(fmt.Errorf("wiz.timeout %q is not a duration such as 60s", w.Timeout))
		}
		timeout = d
	}
	c, err := wiz.NewClient(wiz.Options{
		APIURL: w.APIURL, AuthURL: w.AuthURL, ClientID: w.ClientID, ClientSecret: w.ClientSecret, Timeout: timeout,
	})
	if err != nil {
		return fallback(err)
	}
	return c
}

// scanOptions are the discovery settings shared by scan and inventory.
func (a *app) scanOptions(overrides scan.OverrideRunner) scan.Options {
	return scan.Options{
		Clouds: a.clouds,
		ScopePatterns: map[model.Cloud][]string{
			model.AWS: a.g.accounts, model.Azure: a.g.subscriptions, model.GCP: a.g.projects,
		},
		Types:       a.g.types,
		Concurrency: a.g.concurrency,
		Overrides:   overrides,
		Log:         a.log,
	}
}
