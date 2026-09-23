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
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	worker "github.com/mediactl/clustarr/importarr/worker/importlist"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// authOutcome is what one reconcile's Trakt device-flow step decided:
// whether a sync may proceed this cycle, the ImportList.status.auth value
// to apply, and how soon to look at the flow again.
type authOutcome struct {
	// Authenticated is true once a usable token is stored. A sync is only
	// scheduled while this is true.
	Authenticated bool

	// Auth is the status.auth value to apply, or nil to leave status.auth
	// untouched (a provider other than Trakt never sets it at all).
	Auth *catalogv1alpha1.DeviceAuth

	// RequeueAfter is how soon the controller should look at the flow
	// again: the poll interval while a device code is pending, or zero
	// when nothing about the flow needs re-checking before the next
	// scheduled sync.
	RequeueAfter time.Duration
}

// defaultPollFloor is the minimum poll interval this controller will honour
// even if Trakt's own device-code response asked for less: reconciling
// every tick a misbehaving or misconfigured server asks for would spin the
// controller.
const defaultPollFloor = 5 * time.Second

// reconcileTraktAuth drives one step of the device-code flow: minting a new
// code, polling an in-flight one, or confirming an already-authorized token
// is still usable. It never fetches the list itself -- that is the
// worker's job, on its own schedule -- so this call is always the two or
// three small, fast HTTP requests the device-code flow itself needs, never
// the list fetch.
//
// previous is the ImportList's current status.auth (nil on a fresh list).
// It exists because the Secret-backed device code (store.LoadDeviceCode)
// deliberately holds only DeviceCode, ExpiresAt and Interval -- UserCode and
// VerificationURL are not secret and already live in status.auth, per
// SecretTokenStore.LoadDeviceCode's doc comment -- so a "still pending, poll
// again" outcome has to carry those two fields forward from previous rather
// than leave them empty, or the controller's complete-declaration status
// apply would blank the code out from under a user who has not finished
// typing it in yet.
func reconcileTraktAuth(
	ctx context.Context, flow *trakt.DeviceFlow, store *worker.SecretTokenStore,
	previous *catalogv1alpha1.DeviceAuth, now time.Time,
) (authOutcome, error) {
	log := logging.FromContext(ctx)

	tok, hasToken, err := store.Load(ctx)
	if err != nil {
		return authOutcome{}, fmt.Errorf("importlist: load trakt token: %w", err)
	}
	if hasToken && !tok.Expired(now) {
		// Fetch's own refresh-on-401 keeps a near-expiry token fresh
		// lazily; a still-valid token here needs nothing from this
		// controller beyond reporting it.
		return authOutcome{
			Authenticated: true,
			Auth: &catalogv1alpha1.DeviceAuth{
				State:          catalogv1alpha1.DeviceAuthStateAuthorized,
				TokenExpiresAt: ptrTime(tok.ExpiresAt),
			},
		}, nil
	}

	dc, hasCode, err := store.LoadDeviceCode(ctx)
	if err != nil {
		return authOutcome{}, fmt.Errorf("importlist: load trakt device code: %w", err)
	}
	if hasCode && now.Before(dc.ExpiresAt) {
		return pollTraktDeviceCode(ctx, flow, store, dc, previous)
	}

	// No usable token and no live device code (never started, or the last
	// one expired/was denied/was already used): mint a fresh one. A stored
	// but truly dead token (refresh also failed, or the refresh token was
	// itself revoked) reaches this same branch, since hasToken alone is not
	// enough to call the flow authorized once Expired is true and nothing
	// refreshed it.
	newDC, err := flow.Start(ctx)
	if err != nil {
		return authOutcome{}, fmt.Errorf("importlist: start trakt device flow: %w", err)
	}
	if err := store.SaveDeviceCode(ctx, newDC); err != nil {
		return authOutcome{}, fmt.Errorf("importlist: save trakt device code: %w", err)
	}
	log.Info("importlist: trakt device code issued", "verificationURL", newDC.VerificationURL,
		"expiresAt", newDC.ExpiresAt)
	return authOutcome{
		Auth: &catalogv1alpha1.DeviceAuth{
			State:           catalogv1alpha1.DeviceAuthStatePending,
			UserCode:        newDC.UserCode,
			VerificationURL: newDC.VerificationURL,
			ExpiresAt:       ptrTime(newDC.ExpiresAt),
		},
		RequeueAfter: pollInterval(newDC.Interval),
	}, nil
}

// pollTraktDeviceCode polls one in-flight device code and turns the result
// into an authOutcome. previous carries forward the UserCode and
// VerificationURL a pending outcome must keep reporting -- see
// reconcileTraktAuth's doc comment.
func pollTraktDeviceCode(
	ctx context.Context, flow *trakt.DeviceFlow, store *worker.SecretTokenStore,
	dc trakt.DeviceCode, previous *catalogv1alpha1.DeviceAuth,
) (authOutcome, error) {
	log := logging.FromContext(ctx)
	status, tok, err := flow.Poll(ctx, dc)
	if err != nil {
		return authOutcome{}, fmt.Errorf("importlist: poll trakt device flow: %w", err)
	}

	pending := catalogv1alpha1.DeviceAuth{
		State: catalogv1alpha1.DeviceAuthStatePending, ExpiresAt: ptrTime(dc.ExpiresAt),
	}
	if previous != nil {
		pending.UserCode, pending.VerificationURL = previous.UserCode, previous.VerificationURL
	}

	switch status {
	case trakt.PollStatusAuthorized:
		if err := store.Save(ctx, tok); err != nil {
			return authOutcome{}, fmt.Errorf("importlist: save trakt token: %w", err)
		}
		if err := store.ClearDeviceCode(ctx); err != nil {
			log.Warn("importlist: could not clear the spent trakt device code", "error", err)
		}
		return authOutcome{
			Authenticated: true,
			Auth: &catalogv1alpha1.DeviceAuth{
				State: catalogv1alpha1.DeviceAuthStateAuthorized, TokenExpiresAt: ptrTime(tok.ExpiresAt),
			},
		}, nil

	case trakt.PollStatusPending:
		return authOutcome{Auth: &pending, RequeueAfter: pollInterval(dc.Interval)}, nil

	case trakt.PollStatusSlowDown:
		return authOutcome{Auth: &pending, RequeueAfter: pollInterval(dc.Interval * 2)}, nil

	default: // Expired, Denied, InvalidCode, AlreadyUsed
		if err := store.ClearDeviceCode(ctx); err != nil {
			log.Warn("importlist: could not clear the dead trakt device code", "error", err)
		}
		log.Info("importlist: trakt device code did not complete", "status", status)
		return authOutcome{
			Auth:         &catalogv1alpha1.DeviceAuth{State: catalogv1alpha1.DeviceAuthStateExpired},
			RequeueAfter: requeueFor(time.Minute),
		}, nil
	}
}

func pollInterval(d time.Duration) time.Duration {
	if d < defaultPollFloor {
		return defaultPollFloor
	}
	return d
}

func ptrTime(t time.Time) *metav1.Time {
	mt := metav1.NewTime(t)
	return &mt
}
