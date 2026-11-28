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

package download

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// A fetched .nzb is kept (events.BucketPayloads), so a release is fetched
// from its indexer once.
//
// Every request for a payload used to reach the indexer: nothing held what
// the last one brought. A fetch is a grab against the indexer whatever
// becomes of the reply, and the reply has ways to be lost after it: it was
// too large for the bus (until 2026-11-27), the engine restarted before it
// had the bytes on its disk, its request ran out of time, or the engine
// lost the transfer and added it again. The second fetch is then refused:
// nzb-life answers a second fetch of one NZB inside 24 hours with a 429,
// and three One Piece season packs waited a day on it (2026-11-27).
//
// Only an .nzb is kept. A .torrent carries its tracker's passkey, and the
// bucket is readable by every bus client; a magnet or a link is not bytes.
// The name is the indexer's UID and a hash of the GUID, never the GUID: a
// GUID is often the download URL, key and all.

// nzbContentType is the content type classify gives an .nzb body.
const nzbContentType = "application/x-nzb"

// resultKept is a payload answered from BucketPayloads: the indexer was not
// asked.
const resultKept = "kept"

// How long a look at, and a write to, the payload store may take. The look
// is short: a store that does not answer is passed over and the indexer
// asked. The write has its own time, apart from the request's, so a request
// that ran out while the indexer answered still leaves what it fetched.
const (
	keptReadTimeout  = 15 * time.Second
	keptWriteTimeout = 20 * time.Second
)

// PayloadKey is a kept .nzb's name in events.BucketPayloads
// (events.PayloadsKeyScheme).
func PayloadKey(indexer types.UID, guid string) string {
	sum := sha256.Sum256([]byte(guid))
	return "nzb/" + string(indexer) + "/" + hex.EncodeToString(sum[:16])
}

// payloadMeta is the one builder of a BucketPayloads object's metadata
// (ADR-0021 §2): complete on every Put.
func payloadMeta() events.ObjectMeta {
	return events.ObjectMeta{
		Headers: map[string]string{"Content-Type": nzbContentType, "Content-Encoding": "gzip"},
		Metadata: map[string]string{
			events.ObjectMetaKeyVersion:   events.PayloadsMetaVersion,
			events.ObjectMetaKeyKind:      "nzb",
			events.ObjectMetaKeyWrittenBy: "index@" + version.String(),
		},
	}
}

// keptPayload answers a request from the payload store: the .nzb a fetch of
// this release left there. ok is false when there is none, or it cannot be
// read whole as an .nzb (it is then removed, and the indexer asked).
func (s *Service) keptPayload(ctx context.Context, indexer types.UID, guid string, log *slog.Logger) (schema.DownloadResponse, bool) {
	var none schema.DownloadResponse
	if s.Payloads == nil || indexer == "" {
		return none, false
	}
	name := PayloadKey(indexer, guid)
	ctx, cancel := context.WithTimeout(ctx, keptReadTimeout)
	defer cancel()
	_, r, err := s.Payloads.Get(ctx, name)
	if err != nil {
		if !errors.Is(err, events.ErrObjectNotFound) && !errors.Is(err, events.ErrBucketNotFound) {
			log.Warn("app/indexer/download: the payload store did not answer; asking the indexer", "err", err)
		}
		return none, false
	}
	defer func() { _ = r.Close() }()
	body, err := gunzipPayload(r)
	if err != nil || sniffKind(body) != kindNZB {
		log.Warn("app/indexer/download: a kept payload is not a whole .nzb; removing it and asking the indexer", "err", err)
		if err := s.Payloads.Delete(ctx, name); err != nil && !errors.Is(err, events.ErrObjectNotFound) {
			log.Warn("app/indexer/download: the kept payload was not removed", "err", err)
		}
		return none, false
	}
	log.Info("app/indexer/download: answered from the kept payload; the indexer was not asked", "bytes", len(body))
	return schema.DownloadResponse{Bytes: body, ContentType: nzbContentType}, true
}

// gunzipPayload reads a kept body back, under the cap a fetched one is read
// under.
func gunzipPayload(r io.Reader) ([]byte, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return readPayload(zr)
}

// keepPayload stores a fetched .nzb for the requests that follow. Best
// effort: the payload goes to the caller whether or not it was kept, and a
// store with no room is the maintenance loop's to evict.
func (s *Service) keepPayload(ctx context.Context, indexer types.UID, guid string, resp schema.DownloadResponse, log *slog.Logger) {
	if s.Payloads == nil || indexer == "" || resp.Error != "" || resp.ContentType != nzbContentType || len(resp.Bytes) == 0 {
		return
	}
	var zb bytes.Buffer
	zw := gzip.NewWriter(&zb)
	_, _ = zw.Write(resp.Bytes)
	if err := zw.Close(); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), keptWriteTimeout)
	defer cancel()
	if _, err := s.Payloads.Put(ctx, PayloadKey(indexer, guid), &zb, payloadMeta()); err != nil {
		log.Warn("app/indexer/download: the fetched payload was not kept; a second request asks the indexer again", "err", err)
	}
}
