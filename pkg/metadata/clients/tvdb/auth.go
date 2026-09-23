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

package tvdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/mediactl/clustarr/pkg/metadata"
)

type loginRequest struct {
	APIKey string `json:"apikey"`
	Pin    string `json:"pin,omitempty"`
}

type loginResponse struct {
	Data struct {
		Token string `json:"token"`
	} `json:"data"`
}

// authState holds the client's cached bearer token, embedded (not named)
// into Client so authenticate, currentToken and doRequest can all reach it
// as c.mu / c.token without a level of indirection.
type authState struct {
	mu    sync.Mutex
	token string
}

// authenticate exchanges apiKey (+ pin, for user-supported licences) for a
// bearer JWT and caches it; it is called lazily by doRequest on the first
// call and again exactly once after a 401, never proactively on a timer --
// the JWT lives about a month (§2.2) so most processes never see a second
// login.
func (c *Client) authenticate(ctx context.Context) error {
	body, err := json.Marshal(loginRequest{APIKey: c.apiKey, Pin: c.pin})
	if err != nil {
		return fmt.Errorf("tvdb: encode login request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/login", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("tvdb: build login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tvdb: login: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tvdb: login status %d: %w", resp.StatusCode, metadata.ErrAuth)
	}

	var out loginResponse
	if err := metadata.DecodeJSON(resp.Body, metadata.MaxResponseBytes, &out); err != nil {
		return fmt.Errorf("tvdb: login response: %w", err)
	}

	c.mu.Lock()
	c.token = out.Data.Token
	c.mu.Unlock()
	return nil
}

func (c *Client) currentToken() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.token
}
