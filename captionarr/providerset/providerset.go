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

package providerset

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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/throttle"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/gestdown"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/opensubtitlescom"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subdl"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/subsource"
	"github.com/mediactl/clustarr/pkg/version"
)

// DefaultHTTPTimeout bounds one provider HTTP request. It sits well inside
// the fetch consumers' 90s AckWait (pkg/events/topology.go), so a hung
// provider fails its own request before the broker gives up on the delivery
// and hands it to another worker mid-search.
const DefaultHTTPTimeout = 45 * time.Second

var (
	// ErrNoClient is returned for a SubtitleProviderType captionarr ships no
	// client for. Only whisper remains: it generates subtitles rather than
	// finding them, and the design of record defers it (gap-fix ruling R-1).
	// subdl and subsource had none until gap-fix task X11b (ruling R5 of
	// Phase F named all three).
	ErrNoClient = errors.New("providerset: no client for this provider type")

	// ErrMissingSecret is returned when a provider that needs credentials has
	// no secretRef, the Secret does not exist, or a required key is absent or
	// empty.
	ErrMissingSecret = errors.New("providerset: missing provider credentials")
)

// FileSource is what a local provider needs to know about the media file it
// reads. Remote providers ignore it.
type FileSource struct {
	// Path is the media file's path in THIS process's filesystem (already
	// mapped through the data directory), which ffmpeg opens.
	Path string

	// Info is the MediaFile's already-probed stream table.
	Info commonv1.MediaInfo

	// IgnoreASS and SkipCommentary mirror SubtitleProfileSpec.Embedded.
	IgnoreASS, SkipCommentary bool
}

// Entry is one enabled SubtitleProvider resolved to a client.
type Entry struct {
	// Name and Namespace identify the SubtitleProvider object. Name is what
	// SubtitleRequest.status.items[].provider records.
	Name, Namespace string

	// UID is the SubtitleProvider's UID: the key of its state in the
	// clustarr-provider-throttle KV bucket (captionarr/throttle).
	UID string

	// Type is spec.type, which pkg/subtitles.ThrottleFor keys its
	// per-provider durations on.
	Type subtitlev1alpha1.SubtitleProviderType

	// Priority is spec.priority; lower is searched first.
	Priority int32

	// RateMilli is spec.requestsPerSecondMilli, the shared token bucket's
	// rate.
	RateMilli int32

	// Languages is spec.languages: the BCP-47 tags this provider is
	// restricted to. Empty means every language.
	Languages []string

	// Options is spec.options.
	Options map[string]string

	// Client is a remote provider's long-lived client. Nil for a local
	// provider.
	Client subtitles.Provider

	// ForFile builds a local provider's client for one media file. Nil for a
	// remote provider.
	ForFile func(FileSource) subtitles.Provider
}

// Local reports whether the provider reads the media file itself (embedded)
// rather than calling a remote service. A local provider has no upstream
// rate limit and no upstream throttle: its failures are about one file, not
// about the provider, so the caller must not record them in the shared
// throttle table.
func (e Entry) Local() bool { return e.ForFile != nil }

// Provider returns the client to search with for the media file f.
func (e Entry) Provider(f FileSource) subtitles.Provider {
	if e.ForFile != nil {
		return e.ForFile(f)
	}
	return e.Client
}

// Serves reports whether spec.languages admits any of tags. An empty
// spec.languages admits everything. Both sides are compared through
// pkg/lang.Normalize, so "pt_br", "pt-BR" and "PT-br" are one tag; an entry
// that does not normalise is compared verbatim, never guessed at.
func (e Entry) Serves(tags []string) bool {
	if len(e.Languages) == 0 {
		return true
	}
	for _, want := range tags {
		w := canonical(want)
		for _, have := range e.Languages {
			if canonical(have) == w {
				return true
			}
		}
	}
	return false
}

// canonical normalises a language tag for comparison, falling back to the
// raw string when pkg/lang cannot resolve it.
func canonical(tag string) string {
	if t, ok := lang.Normalize(tag); ok {
		return string(t)
	}
	return tag
}

// Order applies a SubtitleProfile's spec.providers to entries: when names is
// non-empty, only the named providers are kept, in the order named;
// otherwise entries is returned unchanged (already in priority order). A
// name with no matching entry -- a disabled, missing or client-less
// provider -- is skipped.
func Order(entries []Entry, names []string) []Entry {
	if len(names) == 0 {
		return entries
	}
	byName := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byName[e.Name] = e
	}
	out := make([]Entry, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if e, ok := byName[n]; ok && !seen[n] {
			out = append(out, e)
			seen[n] = true
		}
	}
	return out
}

// cached is one remote provider's client, the fingerprint it was built
// from, and the namespace its SubtitleProvider lives in (so a Build for one
// namespace prunes only that namespace's entries).
type cached struct {
	fingerprint string
	namespace   string
	client      subtitles.Provider
}

// Builder builds [Entry] values from SubtitleProvider objects. It is safe
// for concurrent use; one Builder is meant to live as long as the process,
// since its cache is what keeps a provider's login across fetch tasks.
type Builder struct {
	// Client reads SubtitleProviders. The manager's cached client is fine.
	Client client.Reader

	// SecretReader reads the Secrets named by spec.secretRef. Use the
	// manager's API reader -- see the package doc.
	SecretReader client.Reader

	// HTTPClient is shared by every remote provider. Nil gets a client with
	// [DefaultHTTPTimeout].
	HTTPClient *http.Client

	// UserAgent is sent to OpenSubtitles.com. Empty gets
	// "clustarr/<version>".
	UserAgent string

	// FFmpeg is the ffmpeg binary the embedded provider runs. Empty means
	// "ffmpeg" from PATH.
	FFmpeg string

	// KV is the clustarr-provider-throttle bucket
	// (events.BucketProviderThrottle). When set, every OpenSubtitles.com
	// client shares its login token through it -- [TokenCache] over
	// captionarr/throttle.Get and throttle.SetAuth -- so N worker replicas
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
	return &http.Client{Timeout: DefaultHTTPTimeout}
}

func (b *Builder) userAgent() string {
	if b.UserAgent != "" {
		return b.UserAgent
	}
	return "clustarr/" + version.String()
}

// Build returns an [Entry] for every enabled SubtitleProvider in namespace
// that has a client, in spec.priority order (ties by name). A provider this
// phase has no client for, or whose credentials are missing, is left out and
// logged rather than failing the whole set: one misconfigured provider must
// not stop the others from being searched. Only a failure to list is
// returned as an error.
func (b *Builder) Build(ctx context.Context, namespace string) ([]Entry, error) {
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
	out := make([]Entry, 0, len(items))
	for i := range items {
		sp := &items[i]
		live[sp.UID] = true
		if !sp.Spec.EnabledOrDefault() {
			continue
		}
		e, err := b.Entry(ctx, sp)
		switch {
		case errors.Is(err, ErrNoClient):
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

// Validate reports whether sp can be built into an [Entry], by running
// exactly the checks [Builder.Entry] runs -- the type has a client (ruling
// R5), and spec.secretRef names a Secret carrying every key that client
// needs -- without building one. It returns nil; an error wrapping
// [ErrNoClient]; one wrapping [ErrMissingSecret] (no secretRef, no such
// Secret, or a key absent or empty); or a failure to read the Secret.
//
// It is the SubtitleProvider controller's validator, and it is this function
// rather than a copy of it so that the controller's Ready and Authenticated
// cannot disagree with what the fetch worker will actually search: until
// plan task F-6 the controller had a second type table and secret check of
// its own, and they had already drifted (a gestdown provider whose
// secretRef named a missing Secret was Authenticated=True there and skipped
// here). secrets should be the manager's API reader, for the reason the
// package doc gives.
func Validate(ctx context.Context, secrets client.Reader, sp *subtitlev1alpha1.SubtitleProvider) error {
	_, _, err := resolve(ctx, secrets, sp)
	return err
}

// NeedsSecrets returns the Secret keys t's client needs, from that client's
// own Capabilities().NeedsSecrets; nil for a type that needs none or has no
// client.
func NeedsSecrets(t subtitlev1alpha1.SubtitleProviderType) []string {
	if c := prototype(t); c != nil {
		return c.Capabilities().NeedsSecrets
	}
	return nil
}

// HIVerifiable reports whether t's client vouches for the hearing-impaired
// flag it puts on candidates: that client's own HIVerifiable(), read from a
// zero-config instance (no request is made), and false for a type with no
// client. It is SubtitleProvider.status.hiVerifiable, and the value the
// fetch worker's HI filter trusts -- from the client itself, so the two
// can never disagree with a table kept somewhere else.
func HIVerifiable(t subtitlev1alpha1.SubtitleProviderType) bool {
	if c := prototype(t); c != nil {
		return c.HIVerifiable()
	}
	return false
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

// resolve is the one place a SubtitleProvider's type and credentials are
// checked, for [Validate] and [Builder.Entry] alike. For a remote type it
// returns the Secret's data and a version that changes whenever the Secret
// does; a local type (embedded) reads no Secret at all.
func resolve(ctx context.Context, secrets client.Reader, sp *subtitlev1alpha1.SubtitleProvider) (map[string][]byte, string, error) {
	switch sp.Spec.Type {
	case subtitlev1alpha1.SubtitleProviderEmbedded:
		return nil, "", nil
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, subtitlev1alpha1.SubtitleProviderGestdown,
		subtitlev1alpha1.SubtitleProviderSubDL, subtitlev1alpha1.SubtitleProviderSubSource:
	default:
		return nil, "", fmt.Errorf("%w: %s", ErrNoClient, sp.Spec.Type)
	}
	data, version, err := readSecret(ctx, secrets, sp)
	if err != nil {
		return nil, "", err
	}
	if err := requireKeys(sp, data, NeedsSecrets(sp.Spec.Type)); err != nil {
		return nil, "", err
	}
	return data, version, nil
}

// Entry builds (or reuses) the [Entry] for one SubtitleProvider. It returns
// [ErrNoClient] for a type with no client and wraps [ErrMissingSecret] for
// absent credentials -- [Validate]'s verdicts, from the same checks -- so
// the SubtitleProvider controller can surface either as a Ready=False
// reason instead of an error loop (ruling R5).
func (b *Builder) Entry(ctx context.Context, sp *subtitlev1alpha1.SubtitleProvider) (Entry, error) {
	e := Entry{
		Name:      sp.Name,
		Namespace: sp.Namespace,
		UID:       string(sp.UID),
		Type:      sp.Spec.Type,
		Priority:  sp.Spec.Priority,
		RateMilli: sp.Spec.RequestsPerSecondMilli,
		Languages: slices.Clone(sp.Spec.Languages),
		Options:   cloneMap(sp.Spec.Options),
	}

	secret, secretVersion, err := resolve(ctx, b.SecretReader, sp)
	if err != nil {
		return Entry{}, err
	}
	if sp.Spec.Type == subtitlev1alpha1.SubtitleProviderEmbedded {
		ffmpeg := b.FFmpeg
		e.ForFile = func(f FileSource) subtitles.Provider {
			return embedded.New(embedded.Config{
				FFmpeg: ffmpeg, Path: f.Path, Info: f.Info,
				IgnoreASS: f.IgnoreASS, SkipCommentary: f.SkipCommentary,
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
// TokenExpiresAt, read through captionarr/throttle.Get and written through
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

// LoadToken returns the shared token and its expiry; an empty token when
// none has been stored. The client judges freshness itself.
func (c TokenCache) LoadToken(ctx context.Context) (string, time.Time, error) {
	st, err := throttle.Get(ctx, c.KV, c.ProviderUID)
	if err != nil {
		return "", time.Time{}, err
	}
	var exp time.Time
	if st.TokenExpiresAt != nil {
		exp = *st.TokenExpiresAt
	}
	return st.JWT, exp, nil
}

// StoreToken records a token the client has just obtained.
func (c TokenCache) StoreToken(ctx context.Context, token string, expiresAt time.Time) error {
	_, err := throttle.SetAuth(ctx, c.KV, c.ProviderUID, token, expiresAt)
	return err
}

// readSecret returns spec.secretRef's data and a version string that changes
// whenever the Secret does. A provider with no secretRef has no data and a
// constant version.
func readSecret(ctx context.Context, secrets client.Reader, sp *subtitlev1alpha1.SubtitleProvider) (map[string][]byte, string, error) {
	if sp.Spec.SecretRef == nil || sp.Spec.SecretRef.Name == "" {
		return nil, "-", nil
	}
	var s corev1.Secret
	key := types.NamespacedName{Namespace: sp.Namespace, Name: sp.Spec.SecretRef.Name}
	if err := secrets.Get(ctx, key, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, "", fmt.Errorf("%w: secret %s not found", ErrMissingSecret, key)
		}
		return nil, "", fmt.Errorf("providerset: read secret %s: %w", key, err)
	}
	return s.Data, string(s.UID) + "@" + s.ResourceVersion, nil
}

// requireKeys checks every key a provider's Capabilities.NeedsSecrets names
// is present and non-empty.
func requireKeys(sp *subtitlev1alpha1.SubtitleProvider, data map[string][]byte, keys []string) error {
	if len(keys) > 0 && (sp.Spec.SecretRef == nil || sp.Spec.SecretRef.Name == "") {
		return fmt.Errorf("%w: %s needs spec.secretRef with %v", ErrMissingSecret, sp.Spec.Type, keys)
	}
	var missing []string
	for _, k := range keys {
		if len(data[k]) == 0 {
			missing = append(missing, k)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: secret %s/%s has no %v", ErrMissingSecret, sp.Namespace, sp.Spec.SecretRef.Name, missing)
	}
	return nil
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
