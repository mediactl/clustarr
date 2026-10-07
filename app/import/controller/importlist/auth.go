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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/importlist"
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
	// again: the poll interval while a device code is pending, the time
	// until the token's refresh is due while authorized, or zero when
	// nothing about the flow needs re-checking before the next scheduled
	// sync.
	RequeueAfter time.Duration

	// Refreshed is true when this step refreshed the token because the
	// last sync reported Trakt refusing it (schema.ListSnapshot.NeedsToken):
	// the controller re-issues the sync at once (ADR-0019 §7.6, W30).
	Refreshed bool
}

// authInput is what the snapshot told the flow.
type authInput struct {
	// NeedsToken is true while the newest sync's report says Trakt refused
	// the token and nothing has answered it yet (Synced reads
	// ReasonTokenRefused).
	NeedsToken bool
	// RefusedAfterRefresh is true when the sync this controller re-issued
	// after refreshing the token was refused again: the refreshed pair is
	// no good either, so it is cleared and a device flow starts, rather
	// than refreshing on every sync.
	RefusedAfterRefresh bool
}

// defaultPollFloor is the minimum poll interval this controller will honour
// even if Trakt's own device-code response asked for less: reconciling
// every tick a misbehaving or misconfigured server asks for would spin the
// controller.
const defaultPollFloor = 5 * time.Second

// TokenRefreshWindow is how long before tokenExpiresAt the controller
// refreshes a Trakt token (ADR-0019 §7.6): within 24 h of its expiry, or at
// half its life when it lives less than two windows, so a short-lived token
// is never refreshed on every pass.
const TokenRefreshWindow = 24 * time.Hour

// refreshRetry is how soon a refresh that failed while the token still
// works is tried again.
const refreshRetry = time.Hour

// refreshDueAt is when tok's refresh is due: TokenRefreshWindow before its
// expiry, or half its life before it when issuedAt says it lives less than
// two windows.
func refreshDueAt(tok importlist.Token, issuedAt time.Time) time.Time {
	window := TokenRefreshWindow
	if !issuedAt.IsZero() && tok.ExpiresAt.After(issuedAt) {
		if half := tok.ExpiresAt.Sub(issuedAt) / 2; half < window {
			window = half
		}
	}
	return tok.ExpiresAt.Add(-window)
}

// refusedByTrakt reports whether a refresh failed because Trakt refused the
// refresh token (a 4xx), as against a transient failure.
func refusedByTrakt(err error) bool {
	var apiErr *trakt.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
}

// reconcileTraktAuth drives one step of the token's life: refreshing a
// stored token within TokenRefreshWindow of its expiry or at once when the
// last sync reported it refused, minting a new device code, polling an
// in-flight one, or confirming a stored token is still usable. It never
// fetches the list itself -- that is the import agent's job -- so this call
// is always the two or three small requests the token flow needs. The
// controller is the token Secret's only writer and the only refresher
// (W30).
//
// previous is the ImportList's current status.auth (nil on a fresh list).
// It exists because the Secret-backed device code holds only DeviceCode,
// ExpiresAt and Interval -- UserCode and VerificationURL are not secret and
// already live in status.auth -- so a "still pending, poll again" outcome
// has to carry those two fields forward from previous rather than leave
// them empty, or the controller's complete-declaration status apply would
// blank the code out from under a user who has not finished typing it in.
func reconcileTraktAuth(
	ctx context.Context, flow *trakt.DeviceFlow, store *SecretTokenStore,
	previous *catalogv1alpha1.DeviceAuth, now time.Time, in authInput,
) (authOutcome, error) {
	log := logging.FromContext(ctx)

	data, _, err := store.Data(ctx)
	if err != nil {
		return authOutcome{}, fmt.Errorf("importlist: load trakt token: %w", err)
	}
	tok, hasToken := importliststate.TokenFrom(data)
	if hasToken && in.RefusedAfterRefresh {
		log.Warn("importlist: trakt refused the refreshed token too; clearing it and starting a device flow")
		if err := store.ClearToken(ctx); err != nil {
			return authOutcome{}, fmt.Errorf("importlist: clear the refused trakt token: %w", err)
		}
		hasToken = false
	}

	if hasToken {
		dueAt := refreshDueAt(tok, importliststate.IssuedAtFrom(data))
		refreshed := false
		requeue := time.Duration(0)
		if in.NeedsToken || !now.Before(dueAt) {
			next, err := refreshToken(ctx, flow, store, tok, in.NeedsToken)
			switch {
			case err == nil:
				tok, refreshed = next, true
				dueAt = refreshDueAt(tok, now)
			case refusedByTrakt(err) && (in.NeedsToken || tok.Expired(now)):
				log.Warn("importlist: trakt refused the refresh token; clearing the token and starting a device flow", "error", err)
				if err := store.ClearToken(ctx); err != nil {
					return authOutcome{}, fmt.Errorf("importlist: clear the refused trakt token: %w", err)
				}
				hasToken = false
			case in.NeedsToken || tok.Expired(now):
				return authOutcome{}, err
			default:
				// The token still works; the refresh is tried again later,
				// and an expired token falls through to a device flow.
				log.Warn("importlist: could not refresh the trakt token ahead of its expiry; retrying",
					"error", err, "in", refreshRetry)
				requeue = refreshRetry
			}
		}
		if hasToken && !tok.Expired(now) {
			if requeue == 0 {
				requeue = max(dueAt.Sub(now), defaultPollFloor)
			}
			return authOutcome{
				Authenticated: true,
				Auth: &catalogv1alpha1.DeviceAuth{
					State:          catalogv1alpha1.DeviceAuthStateAuthorized,
					TokenExpiresAt: ptrTime(tok.ExpiresAt),
				},
				RequeueAfter: requeue,
				Refreshed:    refreshed && in.NeedsToken,
			}, nil
		}
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
	// but dead token (it expired and nothing refreshed it) reaches this same
	// branch, since hasToken alone is not enough to call the flow
	// authorized once Expired is true.
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

// refreshToken exchanges tok's refresh token for a new pair and stores it.
// A token with no refresh token cannot be refreshed: that reads as Trakt
// refusing it.
func refreshToken(
	ctx context.Context, flow *trakt.DeviceFlow, store *SecretTokenStore, tok importlist.Token, needsToken bool,
) (importlist.Token, error) {
	if tok.RefreshToken == "" {
		return importlist.Token{}, &trakt.APIError{StatusCode: 401, Body: "no refresh token is stored"}
	}
	next, err := flow.Refresh(ctx, tok.RefreshToken)
	if err != nil {
		return importlist.Token{}, fmt.Errorf("importlist: refresh trakt token: %w", err)
	}
	if err := store.Save(ctx, next); err != nil {
		return importlist.Token{}, fmt.Errorf("importlist: save refreshed trakt token: %w", err)
	}
	logging.FromContext(ctx).Info("importlist: refreshed the trakt token", "expiresAt", next.ExpiresAt,
		"becauseRefused", needsToken)
	return next, nil
}

// pollTraktDeviceCode polls one in-flight device code and turns the result
// into an authOutcome. previous carries forward the UserCode and
// VerificationURL a pending outcome must keep reporting -- see
// reconcileTraktAuth's doc comment.
func pollTraktDeviceCode(
	ctx context.Context, flow *trakt.DeviceFlow, store *SecretTokenStore,
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
