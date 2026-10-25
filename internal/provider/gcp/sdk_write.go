package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2/google"
	crm "google.golang.org/api/cloudresourcemanager/v3"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	"github.com/overad9658/tagherd/internal/provider"
)

// sdkWriter implements writeBackend with the Google API clients.
type sdkWriter struct {
	opts      []option.ClientOption
	credsFile string
	labels    map[string]labelAPI
	global    *crm.Service

	mu    sync.Mutex
	local map[string]*crm.Service // Resource Manager endpoints by location
}

func newSDKWriter(ctx context.Context, opts Options) (*sdkWriter, error) {
	var clientOpts []option.ClientOption
	if f := opts.Scopes.CredentialsFile; f != "" {
		clientOpts = append(clientOpts, option.WithAuthCredentialsFile(option.ServiceAccount, f))
	}
	apis, err := labelAPIs(ctx, clientOpts)
	if err != nil {
		return nil, fmt.Errorf("GCP clients: %w", err)
	}
	global, err := crm.NewService(ctx, clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("GCP clients: %w", err)
	}
	return &sdkWriter{opts: clientOpts, credsFile: opts.Scopes.CredentialsFile, labels: apis, global: global, local: map[string]*crm.Service{}}, nil
}

// Caller is the service account's email from its key, or the email the
// token was issued to.
func (w *sdkWriter) Caller(ctx context.Context) (string, error) {
	var data []byte
	if w.credsFile != "" {
		b, err := os.ReadFile(w.credsFile)
		if err != nil {
			return "", err
		}
		data = b
	}
	creds, err := google.FindDefaultCredentials(ctx, "https://www.googleapis.com/auth/cloud-platform")
	if data == nil && err == nil {
		data = creds.JSON
	}
	var key struct {
		ClientEmail string `json:"client_email"`
	}
	if json.Unmarshal(data, &key) == nil && key.ClientEmail != "" {
		return key.ClientEmail, nil
	}
	if err != nil {
		return "", err
	}
	tok, err := creds.TokenSource.Token()
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://oauth2.googleapis.com/tokeninfo?access_token="+url.QueryEscape(tok.AccessToken), nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var info struct {
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil || info.Email == "" {
		return "", errors.New("the token carries no email")
	}
	return info.Email, nil
}

func (w *sdkWriter) api(t provider.Target) (labelAPI, string, error) {
	api, ok := w.labels[t.NativeType]
	if !ok {
		return labelAPI{}, "", fmt.Errorf("no label writer for %s yet", t.NativeType)
	}
	path, err := resourcePath(t.ID)
	return api, path, err
}

func (w *sdkWriter) Labels(ctx context.Context, t provider.Target) (map[string]string, error) {
	api, path, err := w.api(t)
	if err != nil {
		return nil, err
	}
	labels, _, err := api.get(ctx, path)
	return labels, err
}

// SetLabels reads the current fingerprint or version and writes the full
// label set with it, so a concurrent change fails instead of being lost.
func (w *sdkWriter) SetLabels(ctx context.Context, t provider.Target, labels map[string]string) error {
	api, path, err := w.api(t)
	if err != nil {
		return err
	}
	_, token, err := api.get(ctx, path)
	if err != nil {
		return err
	}
	return api.set(ctx, path, labels, token)
}

// crmFor returns the Resource Manager client for a resource's location:
// tag bindings on regional and zonal resources use location endpoints.
func (w *sdkWriter) crmFor(ctx context.Context, location string) (*crm.Service, error) {
	location = strings.ToLower(location)
	if location == "" || location == "global" {
		return w.global, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if s, ok := w.local[location]; ok {
		return s, nil
	}
	opts := append([]option.ClientOption{option.WithEndpoint("https://" + location + "-cloudresourcemanager.googleapis.com/")}, w.opts...)
	s, err := crm.NewService(ctx, opts...)
	if err != nil {
		return nil, err
	}
	w.local[location] = s
	return s, nil
}

func (w *sdkWriter) EffectiveTags(ctx context.Context, t provider.Target) ([]rmBinding, error) {
	s, err := w.crmFor(ctx, t.Region)
	if err != nil {
		return nil, err
	}
	var out []rmBinding
	err = s.EffectiveTags.List().Parent(t.ID).Pages(ctx, func(page *crm.ListEffectiveTagsResponse) error {
		for _, et := range page.EffectiveTags {
			out = append(out, rmBinding{
				Key: lastSegment(et.NamespacedTagKey), Value: lastSegment(et.NamespacedTagValue),
				ValueName: et.TagValue, Inherited: et.Inherited,
			})
		}
		return nil
	})
	return out, err
}

// TagValue looks the value up by namespaced name and creates it under the
// key when it is missing. Keys are never created.
func (w *sdkWriter) TagValue(ctx context.Context, namespace, key, value string) (string, error) {
	name := namespace + "/" + key + "/" + value
	v, err := w.global.TagValues.GetNamespaced().Name(name).Context(ctx).Do()
	if err == nil {
		return v.Name, nil
	}
	if !notFound(err) {
		return "", err
	}
	k, err := w.global.TagKeys.GetNamespaced().Name(namespace + "/" + key).Context(ctx).Do()
	if err != nil {
		if notFound(err) {
			return "", fmt.Errorf("tag key %s/%s does not exist; create it first", namespace, key)
		}
		return "", err
	}
	if _, err := w.global.TagValues.Create(&crm.TagValue{Parent: k.Name, ShortName: value}).Context(ctx).Do(); err != nil {
		return "", err
	}
	// Creation is a long-running operation; wait for the value to appear.
	for range 15 {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
		if v, err := w.global.TagValues.GetNamespaced().Name(name).Context(ctx).Do(); err == nil {
			return v.Name, nil
		}
	}
	return "", fmt.Errorf("tag value %s was created but did not appear within 30s", name)
}

func (w *sdkWriter) Bind(ctx context.Context, t provider.Target, valueName string) error {
	s, err := w.crmFor(ctx, t.Region)
	if err != nil {
		return err
	}
	_, err = s.TagBindings.Create(&crm.TagBinding{Parent: t.ID, TagValue: valueName}).Context(ctx).Do()
	return err
}

func (w *sdkWriter) Unbind(ctx context.Context, t provider.Target, valueName string) error {
	s, err := w.crmFor(ctx, t.Region)
	if err != nil {
		return err
	}
	// Binding names are tagBindings/<escaped parent>/tagValues/<id>.
	name := "tagBindings/" + url.PathEscape(t.ID) + "/" + valueName
	_, err = s.TagBindings.Delete(name).Context(ctx).Do()
	return err
}

func notFound(err error) bool {
	var ge *googleapi.Error
	return errors.As(err, &ge) && (ge.Code == http.StatusNotFound || ge.Code == http.StatusForbidden && strings.Contains(ge.Message, "not found"))
}
