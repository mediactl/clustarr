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

// Command record-metadata-fixtures records live TMDB and TVDB responses as
// test fixtures, so the metadata clients are tested against what the APIs
// really return rather than JSON shaped like the answer (CLAUDE.md,
// "A test built from a fixture shaped like the answer cannot fail").
//
//	TMDB_API_KEY=… TVDB_API_KEY=… [TVDB_PIN=…] go run ./hack/record-metadata-fixtures \
//	  'tmdb:movie/79120?language=en-US&append_to_response=credits=test/data/metadata/tmdb/movie-79120.json' \
//	  'tvdb:series/78804/extended?meta=translations=test/data/metadata/tvdb/series-78804-extended.json'
//
// Each argument is <api>:<path and query>=<output file>, split at the last
// "=". The keys are read from the environment only and are never written:
// TMDB takes its key as the api_key query parameter, which is stripped from
// nothing because TMDB never echoes it; TVDB's key is exchanged for a bearer
// token at /login.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tmdbBase = "https://api.themoviedb.org/3"
	tvdbBase = "https://api4.thetvdb.com/v4"
	maxBody  = 32 << 20
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "record-metadata-fixtures:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	hc := &http.Client{Timeout: 60 * time.Second}
	var tvdbToken string
	for _, arg := range args {
		i := strings.LastIndex(arg, "=")
		api, rest, ok := strings.Cut(arg[:max(i, 0)], ":")
		if i < 0 || !ok {
			return fmt.Errorf("%q: want <api>:<path>=<file>", arg)
		}
		out := arg[i+1:]
		var req *http.Request
		var err error
		switch api {
		case "tmdb":
			key := os.Getenv("TMDB_API_KEY")
			if key == "" {
				return fmt.Errorf("TMDB_API_KEY is not set")
			}
			u, perr := url.Parse(tmdbBase + "/" + rest)
			if perr != nil {
				return perr
			}
			q := u.Query()
			q.Set("api_key", key)
			u.RawQuery = q.Encode()
			req, err = http.NewRequest(http.MethodGet, u.String(), nil)
		case "tvdb":
			if tvdbToken == "" {
				if tvdbToken, err = tvdbLogin(hc); err != nil {
					return err
				}
			}
			req, err = http.NewRequest(http.MethodGet, tvdbBase+"/"+rest, nil)
			if err == nil {
				req.Header.Set("Authorization", "Bearer "+tvdbToken)
			}
		default:
			return fmt.Errorf("%q: unknown api %q", arg, api)
		}
		if err != nil {
			return err
		}
		req.Header.Set("Accept", "application/json")
		body, err := fetch(hc, req)
		if err != nil {
			return fmt.Errorf("%s:%s: %w", api, rest, err)
		}
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body, "", "  "); err != nil {
			return fmt.Errorf("%s:%s: not JSON: %w", api, rest, err)
		}
		pretty.WriteByte('\n')
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(out, pretty.Bytes(), 0o644); err != nil {
			return err
		}
		fmt.Println("wrote", out)
	}
	return nil
}

func tvdbLogin(hc *http.Client) (string, error) {
	key := os.Getenv("TVDB_API_KEY")
	if key == "" {
		return "", fmt.Errorf("TVDB_API_KEY is not set")
	}
	payload, _ := json.Marshal(map[string]string{"apikey": key, "pin": os.Getenv("TVDB_PIN")})
	req, err := http.NewRequest(http.MethodPost, tvdbBase+"/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := fetch(hc, req)
	if err != nil {
		return "", fmt.Errorf("tvdb login: %w", err)
	}
	var r struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Data.Token == "" {
		return "", fmt.Errorf("tvdb login: no token in the response")
	}
	return r.Data.Token, nil
}

func fetch(hc *http.Client, req *http.Request) ([]byte, error) {
	resp, err := hc.Do(req)
	if err != nil {
		// The URL may carry the TMDB key; report the path only.
		return nil, fmt.Errorf("request failed: %v", strings.ReplaceAll(err.Error(), req.URL.RawQuery, "…"))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBody {
		return nil, fmt.Errorf("response over %d bytes", maxBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return body, nil
}
