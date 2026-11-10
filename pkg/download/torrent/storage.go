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

package torrent

import (
	"context"
	"errors"
	"io"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// exactWriteTo is anacrolix's file storage with one correction to the path
// a piece is hashed through. Under v1.61's classic file I/O -- the engine's,
// since the mmap I/O never unmaps and was OOM-killed (2026-10-15) -- a
// piece's WriteTo hands os.File.WriteTo a writer limited to the piece, and
// that writer refuses the file's bytes past the piece as io.ErrShortWrite.
// So every piece but a file's last hashed correctly and still reported an
// I/O error: a WARN line per piece (125k a day on talos, 2026-11-10) and no
// smart-ban attribution, which anacrolix skips on a storage error. Upstream
// fixed writeToN in v1.62.0 (a Go 1.26 floor and 196 commits away); until
// that bump, the error is cleared here when exactly the piece was written.
// Remove this with the bump.
type exactWriteTo struct {
	storage.ClientImplCloser
}

func (s exactWriteTo) OpenTorrent(ctx context.Context, info *metainfo.Info, infoHash metainfo.Hash) (storage.TorrentImpl, error) {
	t, err := s.ClientImplCloser.OpenTorrent(ctx, info, infoHash)
	if err != nil {
		return t, err
	}
	if piece := t.Piece; piece != nil {
		t.Piece = func(p metainfo.Piece) storage.PieceImpl { return wrapPiece(piece(p), p) }
	}
	if withHash := t.PieceWithHash; withHash != nil {
		t.PieceWithHash = func(p metainfo.Piece, hash g.Option[[]byte]) storage.PieceImpl {
			return wrapPiece(withHash(p, hash), p)
		}
	}
	return t, nil
}

// wrapPiece corrects impl's WriteTo when it has one; a piece without is left
// as it is, since anacrolix then copies through ReadAt, which has no such
// fault.
func wrapPiece(impl storage.PieceImpl, p metainfo.Piece) storage.PieceImpl {
	wt, ok := impl.(io.WriterTo)
	if !ok {
		return impl
	}
	return exactPiece{PieceImpl: impl, writeTo: wt, length: p.Length()}
}

// exactPiece forwards everything to the storage's piece but WriteTo, whose
// io.ErrShortWrite after exactly the piece's bytes is the limit being
// reached, not a failed write.
type exactPiece struct {
	storage.PieceImpl
	writeTo io.WriterTo
	length  int64
}

func (p exactPiece) WriteTo(w io.Writer) (int64, error) {
	n, err := p.writeTo.WriteTo(w)
	if errors.Is(err, io.ErrShortWrite) && n == p.length {
		err = nil
	}
	return n, err
}

// Flush forwards the file piece's flush, which anacrolix finds by assertion.
func (p exactPiece) Flush() error {
	if f, ok := p.PieceImpl.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}
