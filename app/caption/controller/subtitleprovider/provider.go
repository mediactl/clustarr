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
	"errors"
	"fmt"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/providerset"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Reasons this controller sets on SubtitleProvider.status.conditions, beyond
// k8s.Reason* and the api package's own SubtitleProviderCondition* type
// constants.
const (
	// ReasonNotImplemented is Ready's (and, via k8s.MarkUnknown,
	// Authenticated's and Throttled's) reason for a SubtitleProviderType
	// captionarr has no client for (providerset.ErrNoClient): only whisper,
	// which the design of record defers. "SubtitleProviderStatus mirrors KV
	// throttle/quota into status" only makes sense for a type something
	// actually authenticates and searches against.
	ReasonNotImplemented = "NotImplemented"

	// ReasonNoCredentialsRequired is Authenticated=True's reason for a
	// provider type providerset.NeedsSecrets reports needs none (gestdown,
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

// authResult is [judge]'s verdict: whether captionarr has a client for the
// provider's type, whether its credentials are complete, and the condition
// reason and message to report either way.
type authResult struct {
	implemented   bool
	authenticated bool
	reason        string
	message       string
}

// judge turns captionarr/providerset.Validate's verdict on a provider into
// this controller's conditions. It validates nothing itself: providerset is
// the one validator, shared with the fetch worker's builder, so a provider
// this controller calls Authenticated is exactly one the worker will search
// (plan task F-6 -- the controller once had its own type table and Secret
// check, and they had drifted).
//
// err is Validate's result. Any error wrapping neither
// providerset.ErrNoClient nor providerset.ErrMissingSecret is a failure to
// read the Secret, which the caller returns as a reconcile error instead of
// passing here.
func judge(t subtitlev1alpha1.SubtitleProviderType, err error) authResult {
	switch {
	case errors.Is(err, providerset.ErrNoClient):
		return authResult{
			reason:  ReasonNotImplemented,
			message: fmt.Sprintf("captionarr has no client for provider type %q yet", t),
		}
	case err != nil:
		return authResult{
			implemented: true,
			reason:      k8s.ReasonDependencyNotReady,
			message:     err.Error(),
		}
	case len(providerset.NeedsSecrets(t)) == 0:
		return authResult{
			implemented: true, authenticated: true, reason: ReasonNoCredentialsRequired,
			message: fmt.Sprintf("provider type %q needs no credentials", t),
		}
	default:
		return authResult{
			implemented: true, authenticated: true, reason: ReasonCredentialsPresent,
			message: fmt.Sprintf("spec.secretRef carries every key provider type %q needs", t),
		}
	}
}
