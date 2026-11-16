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

package tuner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	neturl "net/url"
	"path"
	"strings"

	"github.com/mediactl/clustarr/pkg/iptv/m3u"
)

// maxStreamsBytes caps the panel's stream list: argon-tv's is 24 MB for
// 53,427 streams (2026-11-16).
const maxStreamsBytes = 256 << 20

// errStreamsTooLarge is a stream list over maxStreamsBytes.
var errStreamsTooLarge = errors.New("the stream list is over the size cap")

// fetchGuideIDs reads the panel's stream list (player_api.php's
// get_live_streams) into each stream's guide channel, by stream id; a
// stream without one is left out. A panel's playlist may carry no tvg-id
// while its stream list does: none of argon-tv's 1,451,700 playlist entries
// had one on 2026-11-16, and 8,422 of its live streams did here.
func (t *Tuner) fetchGuideIDs(ctx context.Context, cfg Config) (map[string]string, error) {
	body, err := t.get(ctx, cfg, cfg.StreamsURL())
	if err != nil {
		return nil, err
	}
	defer func() { _ = body.Close() }()
	lr := &io.LimitedReader{R: body, N: maxStreamsBytes + 1}
	dec := json.NewDecoder(lr)
	if tok, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("read the stream list: %w", err)
	} else if tok != json.Delim('[') {
		return nil, errors.New("the stream list is not a JSON array")
	}
	out := map[string]string{}
	for dec.More() {
		var s struct {
			StreamID json.RawMessage `json:"stream_id"`
			GuideID  *string         `json:"epg_channel_id"`
		}
		if err := dec.Decode(&s); err != nil {
			if lr.N <= 0 {
				return nil, errStreamsTooLarge
			}
			return nil, fmt.Errorf("read the stream list: %w", err)
		}
		id := strings.Trim(strings.TrimSpace(string(s.StreamID)), `"`)
		if id == "" || s.GuideID == nil || strings.TrimSpace(*s.GuideID) == "" {
			continue
		}
		out[id] = strings.TrimSpace(*s.GuideID)
	}
	if lr.N <= 0 {
		return nil, errStreamsTooLarge
	}
	return out, nil
}

// streamID is the Xtream stream id an entry's URL ends in
// (/live/<username>/<password>/<id>.ts), "" for a URL that does not parse.
func streamID(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil {
		return ""
	}
	base := path.Base(u.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

// withGuideIDs sets each entry's GuideID: the stream list's for its stream,
// only where the playlist gave no tvg-id. With no list (ids nil: its fetch
// failed) an entry keeps the id prev, the entries of the fetch before, held
// for its key.
func withGuideIDs(entries []m3u.Entry, ids map[string]string, prev []m3u.Entry) {
	var kept map[string]string
	if ids == nil {
		kept = make(map[string]string, len(prev))
		for _, e := range prev {
			if e.GuideID != "" {
				kept[e.Key] = e.GuideID
			}
		}
	}
	for i := range entries {
		e := &entries[i]
		switch {
		case e.TvgID != "":
		case ids != nil:
			e.GuideID = ids[streamID(e.URL)]
		default:
			e.GuideID = kept[e.Key]
		}
	}
}

// guideID is the guide channel an entry names: its playlist tvg-id, else
// the panel's for its stream.
func guideID(e m3u.Entry) string {
	if e.TvgID != "" {
		return e.TvgID
	}
	return e.GuideID
}
