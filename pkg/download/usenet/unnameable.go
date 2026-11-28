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

package usenet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrUnnameable is a post of several files that nothing names: the NZB and
// the articles call them by hashes, the par2 set records the same hashes,
// and the files carry no title of their own. Fetched whole, such a pack is
// published as "0101688cc115e016dcbeabe25daf4f0c.mkv" and thirty-three
// more, the import cannot say which episode is which, a person cannot
// either (one target a download), and the files sit held for a day before
// they are removed: 56 GB of One Piece's fourteenth season on talos
// (2026-11-28), Higurashi's two seasons before it (2026-11-25). It fails
// the download as importRejected, which blocks the release and has the
// next one searched for, before the bulk of it is fetched.
var ErrUnnameable = errors.New("usenet: the post's files carry no names to import them by")

// unnameableMinBytes is the size from which a file counts toward
// ErrUnnameable: the import's own line between a sample and an episode.
var unnameableMinBytes int64 = 50 << 20

// hashName reports a file name that is a hash for certain (the first three
// tests of isProbablyObfuscated): a name that merely looks unlike words is
// not held against a post.
func hashName(name string) bool {
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return md5NameRE.MatchString(base) || hexDotsRE.MatchString(base) || hexRunRE.MatchString(base)
}

// unnameableGate judges the post once every file's first article is on
// disk (fetchFirstArticles) and before the rest is fetched. It refuses only
// what it can see whole: two or more files of an episode's size, every one
// named by a hash in the NZB and on the wire; no archive, whose names are
// inside it; a par2 set read whole from what has arrived (its index is one
// article) whose names are hashes too, or no par2 at all; and no Matroska
// title in any of the files' first articles. A lone file takes the job's
// name (deobfuscate.go), and anything this cannot read is let through for
// the rename step and the import to judge as before.
func (j *job) unnameableGate() error {
	dir := j.contentDir()
	j.mu.Lock()
	files := append([]nzbFile(nil), j.nzb.Files...)
	wire := append([]string(nil), j.wireNames...)
	j.mu.Unlock()

	var big []int
	par2 := false
	for i, f := range files {
		switch f.Kind {
		case kindArchive:
			return nil
		case kindPar2Index, kindPar2Volume:
			par2 = true
		case kindContent:
			if f.Bytes >= unnameableMinBytes {
				big = append(big, i)
			}
		}
	}
	if len(big) < 2 {
		return nil
	}
	for _, i := range big {
		if !hashName(files[i].Name) || (i < len(wire) && wire[i] != "" && !hashName(wire[i])) {
			return nil
		}
	}
	if par2 {
		sets := newPar2Sets()
		for _, f := range files {
			if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
				sets.parseFile(filepath.Join(dir, safeName(f.Name)))
			}
		}
		if !sets.allComplete() {
			return nil // not read whole yet: the rename step will know
		}
		for _, name := range sets.names() {
			if !hashName(name) {
				return nil
			}
		}
	}
	for _, i := range big {
		if title := matroskaTitle(filepath.Join(dir, safeName(files[i].Name))); title != "" && !hashName(title) {
			return nil
		}
	}
	how := "it has no par2 set"
	if par2 {
		how = "its par2 set records the same hashes"
	}
	return fmt.Errorf("%w: %d files are named by hashes (%s and %d more), %s and none carries a title, so nothing says which is which",
		ErrUnnameable, len(big), files[big[0]].Name, len(big)-1, how)
}

// Matroska element ids, as they are written (marker bits kept).
const (
	ebmlHeaderID  = 0x1A45DFA3
	mkvSegmentID  = 0x18538067
	mkvInfoID     = 0x1549A966
	mkvTitleID    = 0x7BA9
	mkvClusterID  = 0x1F43B675
	matroskaLimit = 1 << 20
)

// matroskaTitle is the Title of the Matroska file at path (Segment, Info),
// read from its first megabyte, or "": what the import reads as the
// container's title. Anything that is not a Matroska file opening in the
// usual order -- header, segment, the segment's information before its
// first cluster -- has none.
func matroskaTitle(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, matroskaLimit)
	n, _ := io.ReadFull(f, buf)
	buf = buf[:n]

	id, size, hdr, ok := ebmlElement(buf)
	if !ok || id != ebmlHeaderID || uint64(hdr)+size > uint64(len(buf)) {
		return ""
	}
	buf = buf[uint64(hdr)+size:]
	id, _, hdr, ok = ebmlElement(buf)
	if !ok || id != mkvSegmentID {
		return ""
	}
	buf = buf[hdr:] // into the segment, whatever length it declares
	for len(buf) > 0 {
		id, size, hdr, ok := ebmlElement(buf)
		if !ok || id == mkvClusterID {
			return ""
		}
		body := buf[hdr:]
		if size > uint64(len(body)) {
			if id != mkvInfoID {
				return ""
			}
			size = uint64(len(body))
		}
		if id == mkvInfoID {
			return ebmlString(body[:size], mkvTitleID)
		}
		buf = body[size:]
	}
	return ""
}

// ebmlString is the text of the first child of body with the id want.
func ebmlString(body []byte, want uint32) string {
	for len(body) > 0 {
		id, size, hdr, ok := ebmlElement(body)
		if !ok || uint64(hdr)+size > uint64(len(body)) {
			return ""
		}
		if id == want {
			return strings.TrimRight(string(body[hdr:uint64(hdr)+size]), "\x00 ")
		}
		body = body[uint64(hdr)+size:]
	}
	return ""
}

// ebmlElement reads one element's id and size at the start of b and how
// many bytes the two take. An element of unknown size reads as the largest.
func ebmlElement(b []byte) (id uint32, size uint64, hdr int, ok bool) {
	if len(b) == 0 {
		return 0, 0, 0, false
	}
	idLen := 1
	for mask := byte(0x80); mask != 0 && b[0]&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || len(b) < idLen+1 {
		return 0, 0, 0, false
	}
	var idBytes [4]byte
	copy(idBytes[4-idLen:], b[:idLen])
	id = binary.BigEndian.Uint32(idBytes[:])
	s := b[idLen:]
	sizeLen := 1
	for mask := byte(0x80); mask != 0 && s[0]&mask == 0; mask >>= 1 {
		sizeLen++
	}
	if sizeLen > 8 || len(s) < sizeLen {
		return 0, 0, 0, false
	}
	size = uint64(s[0] & (0xFF >> sizeLen))
	for _, c := range s[1:sizeLen] {
		size = size<<8 | uint64(c)
	}
	return id, size, idLen + sizeLen, true
}
