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

package subtitleprovider

import (
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Reasons this controller sets on SubtitleProvider.status.conditions, beyond
// k8s.Reason* and the api package's own SubtitleProviderCondition* type
// constants.
const (
	// ReasonNotImplemented is Ready's (and, via k8s.MarkUnknown,
	// Authenticated's and Throttled's) reason for a SubtitleProviderType
	// ruling R5 names as having no client in this phase: subdl, subsource and
	// whisper. "SubtitleProviderStatus mirrors KV throttle/quota into status"
	// only makes sense for a type something actually authenticates and
	// searches against -- see [implementedTypes].
	ReasonNotImplemented = "NotImplemented"

	// ReasonNoCredentialsRequired is Authenticated=True's reason for a
	// provider type [requiredSecretKeys] reports needs none (gestdown,
	// embedded).
	ReasonNoCredentialsRequired = "NoCredentialsRequired"

	// ReasonCredentialsPresent is Authenticated=True's reason once every
	// required Secret key is present and non-empty.
	ReasonCredentialsPresent = "CredentialsPresent"

	// ReasonNotThrottled is Throttled=False's reason once the shared KV
	// throttle state, per [throttle.State.Throttled], reports no active
	// throttle.
	ReasonNotThrottled = "NotThrottled"
)

// implementedTypes lists the SubtitleProviderTypes captionarr has an
// in-process client for, mirroring the packages actually shipped under
// pkg/subtitles/providers/. Ruling R5: subdl, subsource and whisper are real
// SubtitleProviderType enum values (the CRD accepts them) with no client
// behind them yet -- a SubtitleProvider of one of those types must report
// Ready=False with a clear reason, never loop retrying an auth check or a
// search that can never succeed.
var implementedTypes = map[subtitlev1alpha1.SubtitleProviderType]bool{
	subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom: true,
	subtitlev1alpha1.SubtitleProviderGestdown:         true,
	subtitlev1alpha1.SubtitleProviderEmbedded:         true,
}

// implemented reports whether t is one of [implementedTypes].
func implemented(t subtitlev1alpha1.SubtitleProviderType) bool {
	return implementedTypes[t]
}

// requiredSecretKeys lists the Secret keys t's real provider needs, mirroring
// its Capabilities().NeedsSecrets (pkg/subtitles/providers/opensubtitlescom
// /client.go's Capabilities: NeedsSecrets: []string{"apiKey", "username",
// "password"}; gestdown and embedded declare none). This package
// deliberately does not import pkg/subtitles/providers/* or construct a
// client to get this list -- task F-3 "validates and reports only"; F-5
// owns turning a SubtitleProvider + Secret into a real pkg/subtitles.Provider.
func requiredSecretKeys(t subtitlev1alpha1.SubtitleProviderType) []string {
	switch t {
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom:
		return []string{
			subtitlev1alpha1.ProviderSecretKeyAPIKey,
			subtitlev1alpha1.ProviderSecretKeyUsername,
			subtitlev1alpha1.ProviderSecretKeyPassword,
		}
	default:
		return nil
	}
}

// hiVerifiable reports the static HIVerifiable() value t's real provider
// returns (pkg/subtitles/providers/{opensubtitlescom,gestdown,embedded}
// /provider.go all currently return true, each citing research note §4.1).
// It is a fact about the provider TYPE, not a per-object computation, so it
// is looked up here rather than by constructing a client -- see
// [requiredSecretKeys]'s identical reasoning.
func hiVerifiable(t subtitlev1alpha1.SubtitleProviderType) bool {
	switch t {
	case subtitlev1alpha1.SubtitleProviderOpenSubtitlesCom, subtitlev1alpha1.SubtitleProviderGestdown, subtitlev1alpha1.SubtitleProviderEmbedded:
		return true
	default:
		return false
	}
}

// authResult is [checkAuthentication]'s verdict: whether spec.secretRef
// names a Secret carrying every key t's real provider needs, and the
// condition reason/message to report either way.
type authResult struct {
	authenticated bool
	reason        string
	message       string
}

// checkAuthentication validates -- it never authenticates against the real
// upstream provider, which would mean building the client task F-3
// explicitly does not own. secret is the already-fetched Secret spec.secretRef
// names, or nil; secretErr is the error from fetching it (apierrors.IsNotFound
// for a missing Secret; any other error is the caller's to treat as a real
// reconcile failure rather than passed here).
func checkAuthentication(t subtitlev1alpha1.SubtitleProviderType, secretRef *corev1.LocalObjectReference, secret *corev1.Secret, secretErr error) authResult {
	required := requiredSecretKeys(t)
	if len(required) == 0 {
		return authResult{
			authenticated: true, reason: ReasonNoCredentialsRequired,
			message: fmt.Sprintf("provider type %q needs no credentials", t),
		}
	}
	if secretRef == nil {
		return authResult{
			reason:  k8s.ReasonDependencyNotReady,
			message: fmt.Sprintf("provider type %q requires a Secret (spec.secretRef is unset)", t),
		}
	}
	if secretErr != nil {
		return authResult{
			reason:  k8s.ReasonDependencyNotReady,
			message: fmt.Sprintf("secret %q: %s", secretRef.Name, secretErr),
		}
	}
	if secret == nil {
		return authResult{
			reason:  k8s.ReasonDependencyNotReady,
			message: fmt.Sprintf("secret %q: not fetched", secretRef.Name),
		}
	}
	var missing []string
	for _, key := range required {
		if len(secret.Data[key]) == 0 {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return authResult{
			reason:  k8s.ReasonDependencyNotReady,
			message: fmt.Sprintf("secret %q is missing key(s): %s", secretRef.Name, strings.Join(missing, ", ")),
		}
	}
	return authResult{
		authenticated: true, reason: ReasonCredentialsPresent,
		message: fmt.Sprintf("secret %q carries every required key", secretRef.Name),
	}
}
