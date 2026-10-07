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

package busconn

import (
	"fmt"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// ReconnectOptions are the reconnect settings every clustarr process dials
// with (ADR-0020 §6): reconnect forever, wait ReconnectDelay after each pass
// over the server pool, and buffer ReconnectBufSize bytes of publishes
// meanwhile. Connect passes them; cmd/markers and cmd/transcode, which dial
// with nats.Connect so a start without NATS still exits at once, pass them
// too.
func ReconnectOptions() []nats.Option {
	return []nats.Option{
		nats.MaxReconnects(-1),
		nats.CustomReconnectDelay(ReconnectDelay),
		nats.ReconnectBufSize(ReconnectBufSize),
	}
}

// ReconnectDelay is the pause after the attempts-th failed pass over the
// server pool: exponential backoff from ReconnectDelayMin doubling to
// ReconnectDelayMax, with full jitter -- a uniform draw from zero to the
// backoff -- so a fleet that lost NATS together does not return together.
// nats.go calls it with attempts counted from 1.
func ReconnectDelay(attempts int) time.Duration {
	backoff := ReconnectDelayMax
	if attempts < 1 {
		attempts = 1
	}
	if shift := attempts - 1; shift < 32 {
		if d := ReconnectDelayMin << shift; d > 0 && d < ReconnectDelayMax {
			backoff = d
		}
	}
	return rand.N(backoff + 1)
}

// ValidateURL checks a NATS_URL: one server URL, or a comma-separated pool of
// them, as nats.Connect takes it (ADR-0020 §6). Each entry needs a host and,
// when it names a scheme, one of nats, tls, ws or wss; nats.Connect learns a
// cluster's other servers from INFO, so a pool needs only some of them.
func ValidateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("busconn: empty NATS url")
	}
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return fmt.Errorf("busconn: NATS url %q has an empty entry in its pool", raw)
		}
		u := entry
		if !strings.Contains(u, "://") {
			u = "nats://" + u
		}
		parsed, err := url.Parse(u)
		if err != nil {
			return fmt.Errorf("busconn: NATS url entry %q: %w", redactURL(entry), err)
		}
		switch parsed.Scheme {
		case "nats", "tls", "ws", "wss":
		default:
			return fmt.Errorf("busconn: NATS url entry %q: scheme %q is not nats, tls, ws or wss", redactURL(entry), parsed.Scheme)
		}
		if parsed.Hostname() == "" {
			return fmt.Errorf("busconn: NATS url entry %q names no host", redactURL(entry))
		}
	}
	return nil
}

// redactURL drops a URL entry's user info, which may carry a password or
// token, before it is quoted in an error.
func redactURL(entry string) string {
	if at := strings.LastIndex(entry, "@"); at >= 0 {
		if scheme := strings.Index(entry, "://"); scheme >= 0 && scheme < at {
			return entry[:scheme+3] + "REDACTED@" + entry[at+1:]
		}
		return "REDACTED@" + entry[at+1:]
	}
	return entry
}
