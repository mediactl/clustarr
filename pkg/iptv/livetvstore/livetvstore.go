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

// Package livetvstore is the clustarr-livetv object store's names and JSON
// shapes: the tuner writes every object, and the ui reads them (the
// direct-tuners and ui design 2026-10-27 §5.1). Each JSON object is gzip;
// a logo is a PNG.
package livetvstore

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// GuideName is the name of a provider's one guide: its spec.guideURL's, or
// its panel's xmltv.php.
const GuideName = "xmltv"

// PlaylistObject is the object caching a provider's playlist: every live
// entry, its stream URL with the account's username and password replaced
// by placeholders, so no credential is stored.
func PlaylistObject(key types.NamespacedName) string {
	return "playlist/" + key.Namespace + "/" + key.Name
}

// GuideCacheObject is the object caching a provider's parsed guide: every
// channel, and programmes for the channels the lineup maps.
func GuideCacheObject(key types.NamespacedName) string {
	return "guide-cache/" + key.Namespace + "/" + key.Name
}

// GuideChannelsObject is the object listing the guide's channels, for the
// ui's mapping picker.
func GuideChannelsObject(key types.NamespacedName) string {
	return "guide-channels/" + key.Namespace + "/" + key.Name + "/" + GuideName
}

// GuideObject is the object holding a provider's whole rendered XEPG guide,
// for the UI and curl.
func GuideObject(key types.NamespacedName) string {
	return "guide/" + key.Namespace + "/" + key.Name
}

// GuideIndexObject is the object holding a provider's guide index, the
// compact form of its XEPG guide the ui's TV Guide reads.
func GuideIndexObject(key types.NamespacedName) string {
	return "guide-index/" + key.Namespace + "/" + key.Name
}

// LogoObject is the object caching one provider's processed logo, by its
// source URL.
func LogoObject(key types.NamespacedName, source string) string {
	sum := sha256.Sum256([]byte(source))
	return "logo/" + key.Namespace + "/" + key.Name + "/" + hex.EncodeToString(sum[:])[:32]
}

// Playlist is the playlist object.
type Playlist struct {
	// Account fingerprints the server and username the playlist was fetched
	// with; a cache of another account is not served.
	Account   string          `json:"account"`
	Hash      string          `json:"hash"`
	FetchedAt time.Time       `json:"fetchedAt"`
	Entries   []PlaylistEntry `json:"entries"`
}

// PlaylistEntry is one live entry of the playlist object. URL is templated
// with {username} and {password}.
type PlaylistEntry struct {
	Key       string `json:"key"`
	Name      string `json:"name"`
	Group     string `json:"group,omitempty"`
	TvgID     string `json:"tvgID,omitempty"`
	TvgName   string `json:"tvgName,omitempty"`
	Logo      string `json:"logo,omitempty"`
	URL       string `json:"url"`
	UserAgent string `json:"userAgent,omitempty"`
}

// GuideChannel is one channel of the guide-channels object.
type GuideChannel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Icon        string `json:"icon,omitempty"`
}

// MaxObjectBytes caps an object read back, compressed and decompressed: the
// playlist object of a 53,000-entry m3u_plus is about 16 MB of JSON.
const MaxObjectBytes = 256 << 20

// ErrTooLarge is an object over MaxObjectBytes.
var ErrTooLarge = errors.New("livetvstore: object over its size cap")

// Encode writes v as gzip JSON.
func Encode(w io.Writer, v any) error {
	zw := gzip.NewWriter(w)
	if err := json.NewEncoder(zw).Encode(v); err != nil {
		_ = zw.Close()
		return err
	}
	return zw.Close()
}

// Decode reads the gzip JSON object r into v, at most MaxObjectBytes on
// either side.
func Decode(r io.Reader, v any) error { return decodeLimit(r, v, MaxObjectBytes) }

func decodeLimit(r io.Reader, v any, max int64) error {
	zr, err := gzip.NewReader(&capped{r: r, left: max})
	if err != nil {
		return err
	}
	return json.NewDecoder(&capped{r: zr, left: max}).Decode(v)
}

// capped reads r and fails with ErrTooLarge past left bytes.
type capped struct {
	r    io.Reader
	left int64
}

func (c *capped) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}
