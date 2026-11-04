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

// Package rawtmdb is the slice of github.com/cyruzin/golang-tmdb v1.9.4
// (MIT, Copyright (c) 2019 Cyro Dubeux; LICENSE in this directory) that
// pkg/metadata/clients/tmdb calls: the GET client and the five endpoints
// behind it, decoded with encoding/json. The library decodes with
// goccy/go-json, whose four encoder VMs were 0.77 MiB of text in the
// manager and the agent for the five requests this package makes
// (docs/superpowers/plans/2026-11-04-binary-size-reduction.md, #7).
//
// Every type keeps the library's field names and JSON tags, so the
// recorded fixtures decode as before; the sub-resources a details struct
// can carry (append_to_response) are only the ones the caller asks for.
// The base URL is a constant: the library kept it in a package variable
// that SetCustomBaseURL mutated for every client in the process, which
// the caller worked around with a transport and never calls.
package rawtmdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// BaseURL is the API every request is built against.
const BaseURL = "https://api.themoviedb.org/3"

const (
	baseURL       = BaseURL
	movieURL      = "/movie/"
	tvURL         = "/tv/"
	searchURL     = "/search/"
	collectionURL = "/collection/"
)

// Client makes authenticated GET requests to TMDB's v3 API.
type Client struct {
	apiKey string
	http   http.Client
}

// Init returns a client for apiKey.
func Init(apiKey string) (*Client, error) {
	if apiKey == "" {
		return nil, errors.New("api key is empty")
	}
	return &Client{apiKey: apiKey}, nil
}

// SetClientConfig sets the http.Client every request goes through.
func (c *Client) SetClientConfig(httpClient http.Client) {
	c.http = httpClient
}

// get fetches url and decodes the body into data. A client without a
// timeout gets ten seconds, as the library gave it.
func (c *Client) get(url string, data any) error {
	if url == "" {
		return errors.New("url field is empty")
	}
	if c.http.Timeout == 0 {
		c.http.Timeout = time.Second * 10
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("could not fetch the url: %w", err)
	}
	req.Header.Add("content-type", "application/json;charset=utf-8")
	res, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		return c.decodeError(res)
	}
	if err = json.NewDecoder(res.Body).Decode(data); err != nil {
		return fmt.Errorf("could not decode the data: %w", err)
	}
	return nil
}

// fmtOptions renders urlOptions as query parameters, in key order so a
// request's URL is the same every time it is made.
func (c *Client) fmtOptions(urlOptions map[string]string) string {
	if len(urlOptions) == 0 {
		return ""
	}
	keys := make([]string, 0, len(urlOptions))
	for key := range urlOptions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var options strings.Builder
	for _, key := range keys {
		options.WriteByte('&')
		options.WriteString(key)
		options.WriteByte('=')
		options.WriteString(url.QueryEscape(urlOptions[key]))
	}
	return options.String()
}

// Error is TMDB's error body, returned for any status but 200 and 204.
type Error struct {
	StatusMessage string `json:"status_message,omitempty"`
	Success       bool   `json:"success,omitempty"`
	StatusCode    int    `json:"status_code,omitempty"`
}

func (e Error) Error() string {
	return fmt.Sprintf(
		"code: %d | success: %t | message: %s",
		e.StatusCode,
		e.Success,
		e.StatusMessage,
	)
}

func (c *Client) decodeError(r *http.Response) error {
	resBody, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("could not read body response: %w", err)
	}
	if len(resBody) == 0 {
		return fmt.Errorf(
			"[%d]: empty body %s",
			r.StatusCode,
			http.StatusText(r.StatusCode),
		)
	}
	var clientError Error
	if err := json.NewDecoder(bytes.NewBuffer(resBody)).Decode(&clientError); err != nil {
		return fmt.Errorf(
			"couldn't decode error: (%d) [%s]",
			len(resBody),
			resBody,
		)
	}
	return clientError
}
