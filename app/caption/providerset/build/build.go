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

package build

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/app/caption/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subdl"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subsource"
	"github.com/mediactl/clustarr/pkg/version"
)

// cached is one remote provider's client, the fingerprint it was built
// from, and the namespace its SubtitleProvider lives in (so a Build for one
// namespace prunes only that namespace's entries).
type cached struct {
	fingerprint string
	namespace   string
	client      subtitles.Provider
}

// Builder builds [providerset.Entry] values from SubtitleProvider objects. It
// is safe for concurrent use; one Builder is meant to live as long as the process,
// since its cache is what keeps a provider's login across fetch tasks.
type Builder struct {
	// Client reads SubtitleProviders. The manager's cached client is fine.
	Client client.Reader

	// SecretReader reads the Secrets named by spec.secretRef. Use the
	// manager's API reader -- see the package doc.
	SecretReader client.Reader

	// HTTPClient is shared by every remote provider. Nil gets a client with
	// [providerset.DefaultHTTPTimeout].
	HTTPClient *http.Client

	// UserAgent is sent to OpenSubtitles.com. Empty gets
	// "clustarr/<version>".
	UserAgent string

	// Extract pulls one embedded text subtitle stream out of a file for the
	// embedded provider. Nil leaves embedded Downloads failing with
	// embedded.ErrNoExtractor. Only the fetch worker's process sets it:
	// app/caption/agent, to embedded/native's in-process Extract.
	Extract embedded.ExtractFunc

	// KV is the clustarr-provider-throttle bucket
	// (events.BucketProviderThrottle). When set, every OpenSubtitles.com
	// client shares its login token through it -- [TokenCache] over
	// app/caption/throttle.Get and throttle.SetAuth -- so N worker replicas
	// using one account log in once between them rather than once each
	// (spec §6.5: the bucket "holds JWT + remaining/reset"). Nil keeps each
	// client's token to itself.
	KV events.KV

	mu    sync.Mutex
	cache map[types.UID]cached
}

// NewBuilder returns a Builder reading providers through c and Secrets
// through secrets.
func NewBuilder(c, secrets client.Reader) *Builder {
	return &Builder{Client: c, SecretReader: secrets}
}

func (b *Builder) httpClient() *http.Client {
	if b.HTTPClient != nil {
		return b.HTTPClient
	}
	return &http.Client{Timeout: providerset.DefaultHTTPTimeout}
}

func (b *Builder) userAgent() string {
	if b.UserAgent != "" {
		return b.UserAgent
	}
	return "clustarr/" + version.String()
}

// Build returns a [providerset.Entry] for every enabled SubtitleProvider in
// namespace that has a client, in spec.priority order (ties by name). A provider this
// phase has no client for, or whose credentials are missing, is left out and
// logged rather than failing the whole set: one misconfigured provider must
// not stop the others from being searched. Only a failure to list is
// returned as an error.
func (b *Builder) Build(ctx context.Context, namespace string) ([]providerset.Entry, error) {
	var list subtitlev1alpha1.SubtitleProviderList
	if err := b.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("providerset: list subtitle providers in %s: %w", namespace, err)
	}

	items := list.Items
	slices.SortFunc(items, func(x, y subtitlev1alpha1.SubtitleProvider) int {
		return cmp.Or(cmp.Compare(x.Spec.Priority, y.Spec.Priority), cmp.Compare(x.Name, y.Name))
	})

	log := logging.FromContext(ctx)
	live := make(map[types.UID]bool, len(items))
	out := make([]providerset.Entry, 0, len(items))
	for i := range items {
		sp := &items[i]
		live[sp.UID] = true
		if !sp.Spec.EnabledOrDefault() {
			continue
		}
		e, err := b.Entry(ctx, sp)
		switch {
		case errors.Is(err, providerset.ErrNoClient):
			log.Debug("providerset: skipping a provider type with no client",
				"provider", sp.Name, "type", sp.Spec.Type)
			continue
		case err != nil:
			log.Warn("providerset: skipping a provider that cannot be built",
				"provider", sp.Name, "type", sp.Spec.Type, "err", err)
			continue
		}
		out = append(out, e)
	}
	b.prune(namespace, live)
	return out, nil
}

// prototype is a zero-config client of type t, for the static facts every
// client of the type shares (Capabilities, HIVerifiable); nil for a type
// with no client. Constructing one makes no request.
func prototype(t subtitlev1alpha1.SubtitleProviderType) subtitles.Provider {
	switch t {
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom:
		return opensubtitlescom.New(opensubtitlescom.Config{})
	case subtitlev1alpha1.SubtitleProviderGestdown:
		return gestdown.New(gestdown.Config{})
	case subtitlev1alpha1.SubtitleProviderSubDL:
		return subdl.New(subdl.Config{})
	case subtitlev1alpha1.SubtitleProviderSubSource:
		return subsource.New(subsource.Config{})
	case subtitlev1alpha1.SubtitleProviderEmbedded:
		return embedded.New(embedded.Config{})
	default:
		return nil
	}
}

// Entry builds (or reuses) the [providerset.Entry] for one SubtitleProvider.
// It returns [providerset.ErrNoClient] for a type with no client and wraps
// [providerset.ErrMissingSecret] for absent credentials --
// [providerset.Validate]'s verdicts, from the same checks -- so
// the SubtitleProvider controller can surface either as a Ready=False
// reason instead of an error loop (ruling R5).
func (b *Builder) Entry(ctx context.Context, sp *subtitlev1alpha1.SubtitleProvider) (providerset.Entry, error) {
	e := providerset.Entry{
		Name:      sp.Name,
		Namespace: sp.Namespace,
		UID:       string(sp.UID),
		Type:      sp.Spec.Type,
		Priority:  sp.Spec.Priority,
		RateMilli: sp.Spec.RequestsPerSecondMilli,
		Languages: slices.Clone(sp.Spec.Languages),
		Options:   cloneMap(sp.Spec.Options),
	}

	secret, secretVersion, err := providerset.Resolve(ctx, b.SecretReader, sp)
	if err != nil {
		return providerset.Entry{}, err
	}
	if sp.Spec.Type == subtitlev1alpha1.SubtitleProviderEmbedded {
		extract := b.Extract
		e.ForFile = func(f providerset.FileSource) subtitles.Provider {
			return embedded.New(embedded.Config{
				Path: f.Path, Info: f.Info,
				IgnoreASS: f.IgnoreASS, SkipCommentary: f.SkipCommentary,
				Extract: extract,
			})
		}
		return e, nil
	}
	fp := strconv.FormatInt(sp.Generation, 10) + "/" + secretVersion

	b.mu.Lock()
	defer b.mu.Unlock()
	if c, ok := b.cache[sp.UID]; ok && c.fingerprint == fp {
		e.Client = c.client
		return e, nil
	}

	// Every client is built with a nil limiter; see the package doc.
	var pc subtitles.Provider
	switch sp.Spec.Type {
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom:
		cfg := opensubtitlescom.Config{
			APIKey:     string(secret[subtitlev1alpha1.ProviderSecretKeyAPIKey]),
			Username:   string(secret[subtitlev1alpha1.ProviderSecretKeyUsername]),
			Password:   string(secret[subtitlev1alpha1.ProviderSecretKeyPassword]),
			UserAgent:  b.userAgent(),
			Endpoint:   deref(sp.Spec.Endpoint),
			HTTPClient: b.httpClient(),
		}
		if b.KV != nil {
			cfg.TokenCache = TokenCache{KV: b.KV, ProviderUID: string(sp.UID)}
		}
		pc = opensubtitlescom.New(cfg)
	case subtitlev1alpha1.SubtitleProviderGestdown:
		pc = gestdown.New(gestdown.Config{
			Endpoint:   deref(sp.Spec.Endpoint),
			HTTPClient: b.httpClient(),
		})
	case subtitlev1alpha1.SubtitleProviderSubDL:
		// An overridden endpoint also moves the download host to its
		// scheme://host (subdl.Config.DownloadEndpoint), so one
		// spec.endpoint serves a mirror or an in-cluster fixture whole.
		pc = subdl.New(subdl.Config{
			APIKey:     string(secret[subtitlev1alpha1.ProviderSecretKeyAPIKey]),
			UserAgent:  b.userAgent(),
			Endpoint:   deref(sp.Spec.Endpoint),
			HTTPClient: b.httpClient(),
		})
	case subtitlev1alpha1.SubtitleProviderSubSource:
		pc = subsource.New(subsource.Config{
			APIKey:     string(secret[subtitlev1alpha1.ProviderSecretKeyAPIKey]),
			UserAgent:  b.userAgent(),
			Endpoint:   deref(sp.Spec.Endpoint),
			HTTPClient: b.httpClient(),
		})
	}
	if b.cache == nil {
		b.cache = map[types.UID]cached{}
	}
	b.cache[sp.UID] = cached{fingerprint: fp, namespace: sp.Namespace, client: pc}
	e.Client = pc
	return e, nil
}

// TokenCache is opensubtitlescom.TokenCache over one SubtitleProvider's entry
// in the clustarr-provider-throttle KV bucket: the State's JWT and
// TokenExpiresAt, read through app/caption/throttle.Get and written through
// throttle.SetAuth. Keyed by the provider's UID like the rest of that
// entry, so two SubtitleProviders -- two accounts -- never share a token.
//
// The token is a credential: it lives only in the KV, never in a log line
// or in SubtitleProvider.status (the provider controller projects
// TokenExpiresAt and nothing else of it).
type TokenCache struct {
	KV          events.KV
	ProviderUID string
}

var _ opensubtitlescom.TokenCache = TokenCache{}

// LoadToken returns the shared token, the API host it was issued with and
// its expiry; an empty token when none has been stored. The client judges
// freshness itself.
func (c TokenCache) LoadToken(ctx context.Context) (string, string, time.Time, error) {
	st, err := throttle.Get(ctx, c.KV, c.ProviderUID)
	if err != nil {
		return "", "", time.Time{}, err
	}
	var exp time.Time
	if st.TokenExpiresAt != nil {
		exp = *st.TokenExpiresAt
	}
	return st.JWT, st.APIServer, exp, nil
}

// StoreToken records a token the client has just obtained and its API host.
func (c TokenCache) StoreToken(ctx context.Context, token, server string, expiresAt time.Time) error {
	_, err := throttle.SetAuth(ctx, c.KV, c.ProviderUID, token, server, expiresAt)
	return err
}

// prune drops cached clients for providers in namespace that no longer
// exist, so a deleted provider's client (and its login) does not outlive it.
// Entries from other namespaces are left alone: a Build only knows which
// providers are live in the namespace it listed.
func (b *Builder) prune(namespace string, live map[types.UID]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for uid, c := range b.cache {
		if c.namespace == namespace && !live[uid] {
			delete(b.cache, uid)
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
