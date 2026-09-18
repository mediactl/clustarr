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

package trakt

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// PollStatus is the outcome of one DeviceFlow.Poll call.
type PollStatus string

// Poll statuses.
//
// The ImportList CRD's DeviceAuthState only has {none,pending,authorized,
// expired}; the controller collapses denied/invalid_code/already_used/
// slow_down into "expired" when it writes status.auth. This package keeps
// them distinct for logging and for a controller that wants to react
// differently (e.g. no retry on denied).
const (
	PollStatusPending     PollStatus = "pending"
	PollStatusAuthorized  PollStatus = "authorized"
	PollStatusExpired     PollStatus = "expired"      // 410: user code expired
	PollStatusDenied      PollStatus = "denied"       // 418: user denied the app
	PollStatusSlowDown    PollStatus = "slow_down"    // 429: poll less often
	PollStatusInvalidCode PollStatus = "invalid_code" // 404
	PollStatusAlreadyUsed PollStatus = "already_used" // 409
)

// deniedStatusCode is Trakt's non-standard "I'm a teapot" status for a user
// who denied the app at the verification URL. net/http has no named
// constant for 418.
const deniedStatusCode = 418

// DeviceCode is the result of DeviceFlow.Start: the code the user enters at
// VerificationURL, and the code this flow polls with.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	ExpiresAt       time.Time
	Interval        time.Duration
}

// DeviceFlow drives Trakt's OAuth device-code flow: Start requests a code,
// Poll is called once per reconcile tick to check whether the user has
// approved it yet, and Refresh exchanges a refresh token for a new access
// token.
type DeviceFlow struct {
	creds Credentials
	opts  options
}

// NewDeviceFlow returns a DeviceFlow using creds, and DefaultBaseURL /
// http.DefaultClient unless overridden by opts.
func NewDeviceFlow(creds Credentials, opts ...Option) *DeviceFlow {
	return &DeviceFlow{creds: creds, opts: newOptions(opts)}
}

// Start requests a new device code (POST /oauth/device/code).
func (f *DeviceFlow) Start(ctx context.Context) (DeviceCode, error) {
	ctx, span := tracing.Start(ctx, "importlist.trakt.device.start")
	defer span.End()

	body, err := json.Marshal(map[string]string{"client_id": f.creds.ClientID})
	if err != nil {
		tracing.RecordError(span, err)
		return DeviceCode{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.opts.baseURL+"/oauth/device/code", bytes.NewReader(body))
	if err != nil {
		tracing.RecordError(span, err)
		return DeviceCode{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURL string `json:"verification_url"`
		ExpiresIn       int64  `json:"expires_in"`
		Interval        int64  `json:"interval"`
	}
	resp, err := doJSON(ctx, f.opts.client, req, &out)
	if err != nil {
		tracing.RecordError(span, err)
		return DeviceCode{}, err
	}
	if resp.StatusCode != http.StatusOK {
		err := &APIError{StatusCode: resp.StatusCode}
		tracing.RecordError(span, err)
		return DeviceCode{}, err
	}
	now := time.Now()
	return DeviceCode{
		DeviceCode:      out.DeviceCode,
		UserCode:        out.UserCode,
		VerificationURL: out.VerificationURL,
		ExpiresAt:       now.Add(time.Duration(out.ExpiresIn) * time.Second),
		Interval:        time.Duration(out.Interval) * time.Second,
	}, nil
}

// Poll checks whether dc has been approved yet (POST /oauth/device/token).
// It does not sleep or loop internally — the caller (a controller) requeues
// after DeviceCode.Interval and calls Poll again.
func (f *DeviceFlow) Poll(ctx context.Context, dc DeviceCode) (PollStatus, importlist.Token, error) {
	ctx, span := tracing.Start(ctx, "importlist.trakt.device.poll")
	defer span.End()

	body, err := json.Marshal(map[string]string{
		"code": dc.DeviceCode, "client_id": f.creds.ClientID, "client_secret": f.creds.ClientSecret,
	})
	if err != nil {
		tracing.RecordError(span, err)
		return "", importlist.Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.opts.baseURL+"/oauth/device/token", bytes.NewReader(body))
	if err != nil {
		tracing.RecordError(span, err)
		return "", importlist.Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out traktTokenResponse
	resp, err := doJSON(ctx, f.opts.client, req, &out)
	if err != nil {
		tracing.RecordError(span, err)
		return "", importlist.Token{}, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return PollStatusAuthorized, out.toToken(), nil
	case http.StatusBadRequest:
		return PollStatusPending, importlist.Token{}, nil
	case http.StatusNotFound:
		return PollStatusInvalidCode, importlist.Token{}, nil
	case http.StatusConflict:
		return PollStatusAlreadyUsed, importlist.Token{}, nil
	case http.StatusGone:
		return PollStatusExpired, importlist.Token{}, nil
	case deniedStatusCode:
		return PollStatusDenied, importlist.Token{}, nil
	case http.StatusTooManyRequests:
		return PollStatusSlowDown, importlist.Token{}, nil
	default:
		err := &APIError{StatusCode: resp.StatusCode}
		tracing.RecordError(span, err)
		return "", importlist.Token{}, err
	}
}

// Refresh exchanges refreshToken for a new access token (POST /oauth/token,
// grant_type=refresh_token). Trakt refresh tokens are single-use: the
// returned Token carries a new refresh token that replaces the one passed
// in.
func (f *DeviceFlow) Refresh(ctx context.Context, refreshToken string) (importlist.Token, error) {
	ctx, span := tracing.Start(ctx, "importlist.trakt.device.refresh")
	defer span.End()

	body, err := json.Marshal(map[string]string{
		"refresh_token": refreshToken, "client_id": f.creds.ClientID,
		"client_secret": f.creds.ClientSecret, "grant_type": "refresh_token",
	})
	if err != nil {
		tracing.RecordError(span, err)
		return importlist.Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.opts.baseURL+"/oauth/token", bytes.NewReader(body))
	if err != nil {
		tracing.RecordError(span, err)
		return importlist.Token{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	var out traktTokenResponse
	resp, err := doJSON(ctx, f.opts.client, req, &out)
	if err != nil {
		tracing.RecordError(span, err)
		return importlist.Token{}, err
	}
	if resp.StatusCode != http.StatusOK {
		err := &APIError{StatusCode: resp.StatusCode}
		tracing.RecordError(span, err)
		return importlist.Token{}, err
	}

	tok := out.toToken()
	// Never log the token values themselves -- only that a refresh
	// happened and when the new token expires.
	logging.FromContext(ctx).Info("trakt: refreshed access token", "expires_at", tok.ExpiresAt)
	return tok, nil
}

type traktTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	CreatedAt    int64  `json:"created_at"`
}

func (r traktTokenResponse) toToken() importlist.Token {
	return importlist.Token{
		AccessToken:  r.AccessToken,
		RefreshToken: r.RefreshToken,
		ExpiresAt:    time.Unix(r.CreatedAt, 0).Add(time.Duration(r.ExpiresIn) * time.Second),
	}
}
