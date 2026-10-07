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
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/subtitles"
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
	// clustarr-provider-throttle KV bucket (app/caption/throttle).
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

// Validate reports whether sp can be built into an [Entry], by running
// exactly the checks build.Builder.Entry runs -- the type has a client (ruling
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
	_, _, err := Resolve(ctx, secrets, sp)
	return err
}

// NeedsSecrets returns the Secret keys t's client needs; nil for a type
// that needs none or has no client. It reads [capabilities], which
// build's TestCapabilityTableMatchesTheClients holds to each client.
func NeedsSecrets(t subtitlev1alpha1.SubtitleProviderType) []string {
	return slices.Clone(capabilities[t].needsSecrets)
}

// HIVerifiable reports whether t's client vouches for the
// hearing-impaired flag it puts on candidates (status.hiVerifiable, and
// what the fetch worker's HI filter trusts); false for a type with no
// client.
func HIVerifiable(t subtitlev1alpha1.SubtitleProviderType) bool {
	return capabilities[t].hiVerifiable
}

// Resolve is the one place a SubtitleProvider's type and credentials are
// checked, for Validate and build.Builder.Entry alike. For a remote type it
// returns the Secret's data and a version that changes whenever the Secret
// does; a local type (embedded) reads no Secret at all.
func Resolve(ctx context.Context, secrets client.Reader, sp *subtitlev1alpha1.SubtitleProvider) (map[string][]byte, string, error) {
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
