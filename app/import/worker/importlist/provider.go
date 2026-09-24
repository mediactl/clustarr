/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package importlist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/arr"
	"github.com/mediactl/clustarr/pkg/importlist/custom"
	"github.com/mediactl/clustarr/pkg/importlist/imdbcsv"
	"github.com/mediactl/clustarr/pkg/importlist/mdblist"
	"github.com/mediactl/clustarr/pkg/importlist/plex"
	"github.com/mediactl/clustarr/pkg/importlist/stevenlu"
	"github.com/mediactl/clustarr/pkg/importlist/tmdb"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
)

// readSecret returns ref's data in namespace ns, or an empty map when ref is
// nil. Mirrors catalogarr/controller/metadataprovider's readSecret and
// indexarr/controller/indexer's own copy: the pattern is small enough that
// this package duplicates it rather than adding a shared helper package for
// three call sites across three services.
func readSecret(ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference) (map[string][]byte, error) {
	if ref == nil {
		return nil, nil
	}
	var sec corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &sec); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("importlist: secret %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return sec.Data, nil
}

// readConfigMap returns ref's data in namespace ns.
func readConfigMap(ctx context.Context, c client.Client, ns string, ref corev1.LocalObjectReference) (map[string]string, error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &cm); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("importlist: configMap %s/%s not found", ns, ref.Name)
		}
		return nil, err
	}
	return cm.Data, nil
}

// ErrUnsupportedProviderKind is returned by BuildProvider when the CRD's
// selected provider has no data for the requested kind (for example
// spec.stevenLu with kind=series). syncKind checks CanYield first, so
// reaching it means that table and a provider constructor disagree; the
// kind fails with it rather than being skipped.
var ErrUnsupportedProviderKind = fmt.Errorf("importlist: provider does not support this kind")

// ProviderOptions carries what BuildProvider threads into the providers
// beyond the ImportList itself.
type ProviderOptions struct {
	// HTTPClient is used by every provider that makes its own HTTP calls.
	// Nil means http.DefaultClient.
	HTTPClient *http.Client

	// TraktBaseURL overrides trakt.DefaultBaseURL for the list fetch and
	// its token refresh. Empty means the production API. It exists so an
	// end-to-end run can point Trakt at an in-cluster fixture; the
	// device-code flow's own override is the ImportList controller's
	// Reconciler.TraktBaseURL, and both should name the same host.
	TraktBaseURL string

	// PlexBaseURL overrides Plex Discover's base URL. Empty means the
	// production service.
	PlexBaseURL string
}

// BuildProvider is pkg/importlist/config.go:112-115's "wiring a Config to a
// concrete provider constructor" -- this task's namesake job. It reads
// il.Spec's selected provider (exactly one is set; the CRD's own CEL rule
// and pkg/importlist.Config.Validate both enforce that upstream of here),
// resolves spec.secretRef and, for imdbCSV, spec.imdbCSV.configMapRef, and
// returns the concrete pkg/importlist.ImportList for kind.
//
// tokenStore is used only by the trakt branch; every other provider ignores
// it. opts.HTTPClient is threaded through the branches that make their own
// HTTP calls (every provider except imdbCSV, which is parsed locally -- see
// the doc comment on configMapCSVList) and defaults to http.DefaultClient
// when nil; opts' base URLs override Trakt's and Plex Discover's hosts.
func BuildProvider(
	ctx context.Context,
	c client.Client,
	il *catalogv1alpha1.ImportList,
	kind commonv1.MediaKind,
	tokenStore *SecretTokenStore,
	opts ProviderOptions,
) (pkgimportlist.ImportList, error) {
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	spec := il.Spec
	name := il.Name

	switch {
	case spec.Trakt != nil:
		secret, err := readSecret(ctx, c, il.Namespace, spec.SecretRef)
		if err != nil {
			return nil, err
		}
		creds := trakt.Credentials{
			ClientID:     string(secret["clientID"]),
			ClientSecret: string(secret["clientSecret"]),
		}
		if creds.ClientID == "" || creds.ClientSecret == "" {
			return nil, fmt.Errorf(
				"importlist: trakt requires spec.secretRef with clientID and clientSecret keys")
		}
		cfg := pkgimportlist.TraktConfig{
			ListType: pkgimportlist.TraktListType(spec.Trakt.ListType), Username: spec.Trakt.Username,
			ListSlug: spec.Trakt.ListSlug, Limit: spec.Trakt.Limit,
		}
		traktOpts := []trakt.Option{trakt.WithHTTPClient(httpClient)}
		if opts.TraktBaseURL != "" {
			traktOpts = append(traktOpts, trakt.WithBaseURL(opts.TraktBaseURL))
		}
		// A nil flow makes trakt.New build its refresh flow from these same
		// options, so a token refresh mid-fetch goes to the same host.
		list, err := trakt.New(name, kind, cfg, creds, tokenStore, nil, traktOpts...)
		if err != nil {
			return nil, wrapUnsupportedKind(err, trakt.ErrUnsupportedKind)
		}
		return list, nil

	case spec.Plex != nil:
		secret, err := readSecret(ctx, c, il.Namespace, spec.SecretRef)
		if err != nil {
			return nil, err
		}
		token, clientID := string(secret["token"]), string(secret["clientID"])
		if token == "" {
			return nil, fmt.Errorf("importlist: plex requires spec.secretRef with a token key")
		}
		plexOpts := []plex.Option{plex.WithHTTPClient(httpClient)}
		if opts.PlexBaseURL != "" {
			plexOpts = append(plexOpts, plex.WithBaseURL(opts.PlexBaseURL))
		}
		list, err := plex.New(name, kind, token, clientID, plexOpts...)
		if err != nil {
			return nil, wrapUnsupportedKind(err, plex.ErrUnsupportedKind)
		}
		return list, nil

	case spec.Tmdb != nil:
		cfg := pkgimportlist.TmdbConfig{ListID: spec.Tmdb.ListID, Discover: spec.Tmdb.Discover}
		return tmdb.New(name, kind, cfg), nil

	case spec.Mdblist != nil:
		secret, err := readSecret(ctx, c, il.Namespace, spec.SecretRef)
		if err != nil {
			return nil, err
		}
		apiKey := string(secret["apiKey"])
		if apiKey == "" {
			return nil, fmt.Errorf("importlist: mdblist requires spec.secretRef with an apiKey key")
		}
		cfg := pkgimportlist.MdblistConfig{URL: spec.Mdblist.URL}
		list, err := mdblist.New(name, kind, cfg, apiKey, mdblist.WithHTTPClient(httpClient))
		if err != nil {
			return nil, wrapUnsupportedKind(err, mdblist.ErrUnsupportedKind)
		}
		return list, nil

	case spec.StevenLu != nil:
		if kind != commonv1.MediaKindMovie {
			return nil, fmt.Errorf("importlist: stevenLu covers movies only: %w", ErrUnsupportedProviderKind)
		}
		return stevenlu.New(name, stevenlu.WithHTTPClient(httpClient)), nil

	case spec.ImdbCSV != nil:
		cmData, err := readConfigMap(ctx, c, il.Namespace, spec.ImdbCSV.ConfigMapRef)
		if err != nil {
			return nil, err
		}
		content, err := csvContent(cmData)
		if err != nil {
			return nil, err
		}
		return &configMapCSVList{name: name, kind: kind, content: content}, nil

	case spec.Custom != nil:
		cfg := pkgimportlist.CustomConfig{
			URL: spec.Custom.URL, Format: pkgimportlist.CustomListFormat(spec.Custom.Format),
		}
		return custom.New(name, kind, cfg), nil

	case spec.Arr != nil:
		cfg := pkgimportlist.ArrConfig{
			BaseURL: spec.Arr.BaseURL, Kind: pkgimportlist.ArrKind(spec.Arr.Kind),
		}
		return arr.New(name, kind, cfg), nil

	default:
		// pkg/importlist.Config.Validate and the CRD's own CEL rule both
		// forbid this; reaching it means one of them regressed.
		return nil, fmt.Errorf("importlist: %s/%s sets no provider", il.Namespace, il.Name)
	}
}

// wrapUnsupportedKind normalises every provider's own "kind must be movie or
// series" sentinel to ErrUnsupportedProviderKind, so callers can test for it
// with one errors.Is regardless of which provider raised it.
func wrapUnsupportedKind(err, sentinel error) error {
	if errors.Is(err, sentinel) {
		return fmt.Errorf("%w: %w", ErrUnsupportedProviderKind, err)
	}
	return err
}

// csvContent picks the one entry out of an IMDb-CSV ConfigMap's Data. The
// CRD requires only a configMapRef, not a key, so the ConfigMap is expected
// to hold exactly one file the way `kubectl create configmap --from-file`
// produces; more than one key is rejected rather than guessed at, per the
// never-guess rule.
func csvContent(data map[string]string) (string, error) {
	switch len(data) {
	case 0:
		return "", fmt.Errorf("importlist: imdbCSV configMap has no data")
	case 1:
		for _, v := range data {
			return v, nil
		}
	}
	return "", fmt.Errorf(
		"importlist: imdbCSV configMap has %d keys, want exactly 1 (the CSV export)", len(data))
}

// configMapCSVList adapts a ConfigMap's IMDb CSV export to
// pkg/importlist.ImportList by parsing it directly with imdbcsv.Parse,
// rather than through imdbcsv.List's HTTP fetch. imdbcsv/list.go's own doc
// comment names this the controller's job: "the ImportList controller
// resolves the CRD's ConfigMapRef to a URL (or serves the ConfigMap's
// content itself, before constructing a List's Config)". Standing up an
// HTTP server to hand imdbcsv.List a URL just so it can GET data already in
// hand would be needless machinery and a needless network hop for content
// that already lives in memory; parsing it directly is the "serves it
// locally" option that comment names.
type configMapCSVList struct {
	name    string
	kind    commonv1.MediaKind
	content string
}

// Name implements pkg/importlist.ImportList.
func (l *configMapCSVList) Name() string { return l.name }

// Kind implements pkg/importlist.ImportList.
func (l *configMapCSVList) Kind() commonv1.MediaKind { return l.kind }

// Fetch implements pkg/importlist.ImportList.
func (l *configMapCSVList) Fetch(context.Context) ([]pkgimportlist.Item, error) {
	return imdbcsv.Parse(strings.NewReader(l.content), l.kind)
}

var _ pkgimportlist.ImportList = (*configMapCSVList)(nil)
