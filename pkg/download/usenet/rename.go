/*
Copyright 2026 The clustarr Authors.

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
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // PAR2's hash16k is MD5 by specification.
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// This file is the de-obfuscation step the research note (§2.4, §4.3 step
// 3) asked for and udl's postprocess.renameByPAR2/renameByMagic showed the
// shape of: before repair, give every file the name the par2 set records
// for it, matched by the MD5 of its first 16 KiB (FileDesc.hash16k); then
// give a file whose name carries no usable extension one from its magic
// bytes. Obfuscated posts name their files "z75QO...part070.rar" inside the
// par2 set and something else on the wire; par2 can match them by content
// but recreates targets by copying blocks, and archive detection keys on
// the extension, so a set that arrived whole and obfuscated was never
// unpacked.

var (
	par2Magic        = []byte("PAR2\x00PKT")
	par2MainType     = []byte("PAR 2.0\x00Main\x00\x00\x00\x00")
	par2FileDescType = []byte("PAR 2.0\x00FileDesc")
	par2IFSCType     = []byte("PAR 2.0\x00IFSC\x00\x00\x00\x00")
)

// par2FileDesc is one FileDesc packet: the name the set records for a file
// and the MD5 of its first 16 KiB (or of the whole file when smaller).
type par2FileDesc struct {
	ID      [16]byte
	Name    string
	Hash16k [16]byte
	Length  uint64
}

// par2Set is what a job's par2 files say about the set: the files it
// describes and, from the IFSC packets, the MD5 of every slice of every
// file -- the check par2 itself runs to recognise a file whose name it does
// not know. Every volume repeats the packets, so a set merges by file id.
type par2Set struct {
	// ID is the set's id, the one every packet of it carries. A set read
	// through parse alone (one file, whatever set its packets name) has
	// none.
	ID [16]byte
	// SliceSize is the Main packet's slice size, zero until one is seen.
	SliceSize uint64
	// Files is keyed by file id.
	Files map[[16]byte]par2FileDesc
	// Slices maps a slice's MD5 to the id of the file it belongs to.
	Slices map[[16]byte][16]byte
	// recovery and other are the Main packet's file ids: the files the
	// recovery data covers, and the ones it only names.
	recovery, other [][16]byte
	// sliced holds the ids whose IFSC packet has been seen.
	sliced map[[16]byte]bool
}

func newPar2Set() *par2Set {
	return &par2Set{Files: map[[16]byte]par2FileDesc{}, Slices: map[[16]byte][16]byte{}, sliced: map[[16]byte]bool{}}
}

// complete reports whether the set holds all renameObfuscated reads: the
// Main packet, a FileDesc for every file it lists and the slice hashes of
// every file in the recovery set. Every par2 file of a set carries them all,
// so one intact file completes it.
func (s *par2Set) complete() bool {
	if s.SliceSize == 0 || len(s.recovery) == 0 {
		return false
	}
	for _, id := range s.recovery {
		if _, ok := s.Files[id]; !ok || !s.sliced[id] {
			return false
		}
	}
	for _, id := range s.other {
		if _, ok := s.Files[id]; !ok {
			return false
		}
	}
	return true
}

// parsePar2FileDescs walks the packets of one par2 file and returns its
// FileDesc packets, sorted by name.
func parsePar2FileDescs(r io.Reader) ([]par2FileDesc, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPar2Bytes+1))
	if err != nil {
		return nil, err
	}
	set := newPar2Set()
	set.parse(bytes.NewReader(data), int64(len(data)))
	out := make([]par2FileDesc, 0, len(set.Files))
	for _, d := range set.Files {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// maxPar2Bytes bounds one par2 file read whole; parse itself reads by offset.
const maxPar2Bytes = 1 << 30

// par2ScanChunk is how far parse reads ahead when looking for the next packet
// header after a corrupt one.
const par2ScanChunk = 1 << 20

// parse walks the packets of one par2 file into the set.
//
// It is a resynchronising scan, the way parchive-go and par2cmdline read a
// file: a packet whose header is not where the previous packet's length
// said, whose length runs past the file, or whose MD5 (over everything
// after the digest) does not match is corrupt, and the scan resumes at the
// byte after its magic rather than stopping. A par2 volume with a missing
// article is ordinary on a frugal provider -- the hole is one article's
// worth of zeros -- and a scan that stopped at the first bad header lost
// every FileDesc and IFSC packet after it, so nothing was renamed; a scan
// that took a FileDesc with a hole in its name at face value would rename
// a file to garbage. Only the packets the set consumes are hashed; a
// recovery packet's body is skipped by its length.
func (s *par2Set) parse(r io.ReaderAt, size int64) {
	walkPar2(r, size, func(_ [16]byte, typ, body []byte) { s.take(typ, body) })
}

// walkPar2 walks the packets of one par2 file as parse describes, handing
// take each verified packet of a consumed type with the id of its set.
func walkPar2(r io.ReaderAt, size int64, take func(setID [16]byte, typ, body []byte)) {
	header := make([]byte, 64)
	var off int64
	for off+64 <= size {
		if _, err := r.ReadAt(header, off); err != nil {
			return
		}
		if !bytes.Equal(header[:8], par2Magic) {
			off = nextPar2Magic(r, size, off+1)
			continue
		}
		length := int64(binary.LittleEndian.Uint64(header[8:16])) //nolint:gosec // bounded below
		if length < 64 || length%4 != 0 || length > size-off {
			off++
			continue
		}
		typ := header[48:64]
		consumed := bytes.Equal(typ, par2MainType) || bytes.Equal(typ, par2FileDescType) || bytes.Equal(typ, par2IFSCType)
		if !consumed {
			off += length
			continue
		}
		body := make([]byte, length-64)
		if _, err := r.ReadAt(body, off+64); err != nil {
			return
		}
		h := md5.New() //nolint:gosec // PAR2 specifies MD5.
		h.Write(header[32:64])
		h.Write(body)
		if !bytes.Equal(h.Sum(nil), header[16:32]) {
			off++
			continue
		}
		var id [16]byte
		copy(id[:], header[32:48])
		take(id, typ, body)
		off += length
	}
}

// nextPar2Magic returns the offset of the next packet magic at or after
// from, or size when there is none.
func nextPar2Magic(r io.ReaderAt, size, from int64) int64 {
	buf := make([]byte, par2ScanChunk)
	for from < size {
		n, err := r.ReadAt(buf, from)
		if n == 0 {
			return size
		}
		if i := bytes.Index(buf[:n], par2Magic); i >= 0 {
			return from + int64(i)
		}
		if err != nil || n < len(par2Magic) {
			return size
		}
		// Keep the tail: a magic can straddle two reads.
		from += int64(n - (len(par2Magic) - 1))
	}
	return size
}

// take records one verified packet of a consumed type.
func (s *par2Set) take(typ, body []byte) {
	switch {
	case bytes.Equal(typ, par2MainType):
		// slice size 8, recovery set file count 4, then the recovery set's
		// file ids and the non-recovery set's.
		if len(body) < 12 {
			return
		}
		s.SliceSize = binary.LittleEndian.Uint64(body[:8])
		n := int(binary.LittleEndian.Uint32(body[8:12]))
		ids := body[12:]
		s.recovery, s.other = nil, nil
		for i := 0; (i+1)*16 <= len(ids); i++ {
			var id [16]byte
			copy(id[:], ids[i*16:(i+1)*16])
			if i < n {
				s.recovery = append(s.recovery, id)
			} else {
				s.other = append(s.other, id)
			}
		}
	case bytes.Equal(typ, par2FileDescType):
		// file id 16, hash 16, hash16k 16, length 8, name.
		if len(body) < 56 {
			return
		}
		var d par2FileDesc
		copy(d.ID[:], body[:16])
		copy(d.Hash16k[:], body[32:48])
		d.Length = binary.LittleEndian.Uint64(body[48:56])
		d.Name = strings.TrimRight(string(body[56:]), "\x00")
		d.Name = filepath.Base(strings.ReplaceAll(d.Name, "\\", "/"))
		if d.Name == "" || d.Name == "." || d.Name == "/" {
			return
		}
		s.Files[d.ID] = d
	case bytes.Equal(typ, par2IFSCType):
		// file id 16, then (MD5 16, CRC32 4) per slice.
		if len(body) < 16 {
			return
		}
		var id [16]byte
		copy(id[:], body[:16])
		s.sliced[id] = true
		for o := 16; o+20 <= len(body); o += 20 {
			var h [16]byte
			copy(h[:], body[o:o+16])
			s.Slices[h] = id
		}
	}
}

// nameByHash16k returns the set's name for the file whose first 16 KiB
// hash to h, or "".
func (s *par2Set) nameByHash16k(h [16]byte) string {
	for _, d := range s.Files {
		if d.Hash16k == h {
			return d.Name
		}
	}
	return ""
}

// matchBySlice names a damaged file by any intact slice: it hashes the file
// slice by slice (the last one zero-padded, as the IFSC rule has it) until
// one MD5 is in the set. A hole in the first 16 KiB defeats hash16k, but
// nearly every slice of a file missing a few articles is intact, and this
// is the check par2 runs itself when it verifies an extra file. Without it
// par2 rebuilt such a file under the set's name and left the holed copy
// under the wire name, and the unpacker started from the holed copy.
func (s *par2Set) matchBySlice(path string) string {
	if s.SliceSize == 0 || s.SliceSize > 1<<30 || len(s.Slices) == 0 {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, s.SliceSize)
	for {
		n, err := io.ReadFull(f, buf)
		if n == 0 {
			return ""
		}
		for i := n; i < len(buf); i++ {
			buf[i] = 0
		}
		sum := md5.Sum(buf) //nolint:gosec // PAR2 specifies MD5.
		if id, ok := s.Slices[sum]; ok {
			if d, ok := s.Files[id]; ok {
				return d.Name
			}
		}
		if err != nil {
			return ""
		}
	}
}

// hash16k is the MD5 of a file's first 16 KiB, the FileDesc.hash16k rule.
func hash16k(path string) ([16]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [16]byte{}, err
	}
	defer func() { _ = f.Close() }()
	h := md5.New() //nolint:gosec // PAR2 specifies MD5.
	if _, err := io.CopyN(h, f, 16<<10); err != nil && !errors.Is(err, io.EOF) {
		return [16]byte{}, err
	}
	var out [16]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// isPar2File reports whether the file starts with a par2 packet header, for
// a par2 file whose name says otherwise.
func isPar2File(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 8)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, par2Magic)
}

// sniffExtension returns an extension for a file from its first bytes, or
// "" when the bytes say nothing this recognises.
func sniffExtension(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	head := make([]byte, 16)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	switch {
	case n >= 4 && bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
		return ".mkv" // EBML; WebM is Matroska too, and .mkv is what the importer probes
	case n >= 7 && bytes.HasPrefix(head, []byte("Rar!\x1a\x07")):
		return ".rar"
	case n >= 8 && bytes.Equal(head[:8], par2Magic):
		return ".par2"
	case n >= 8 && bytes.Equal(head[4:8], []byte("ftyp")):
		return ".mp4"
	case n >= 12 && bytes.Equal(head[:4], []byte("RIFF")) && bytes.Equal(head[8:12], []byte("AVI ")):
		return ".avi"
	case n >= 6 && bytes.Equal(head[:6], []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}):
		return ".7z"
	case n >= 4 && bytes.Equal(head[:4], []byte("PK\x03\x04")):
		return ".zip"
	default:
		return ""
	}
}

// knownExtensions are the extensions a file may carry and be left alone by
// the magic step: every media extension the importer classifies, taken
// from pkg/fsops' own table for every kind so the two cannot drift (until
// 2026-11-10 this map was a copy of it that never learned .azw, .cb7,
// .cbt, .wv, .ape or .wma, so a .cb7 -- a 7z container -- was renamed
// .cb7.7z), plus what the unpacker and the cleanup know: the video
// containers fsops does not import (.flv, disc images), subtitles, notes,
// images, archives and par2.
var knownExtensions = withMediaExtensions(map[string]bool{
	".flv": true, ".iso": true, ".img": true,
	".srt": true, ".sub": true, ".idx": true, ".ass": true, ".ssa": true, ".vtt": true,
	".nfo": true, ".sfv": true, ".txt": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".rar": true, ".zip": true, ".7z": true, ".par2": true,
})

// withMediaExtensions is extra plus every extension of every kind in
// fsops.MediaExtensions.
func withMediaExtensions(extra map[string]bool) map[string]bool {
	out := maps.Clone(extra)
	for _, exts := range fsops.MediaExtensions {
		maps.Copy(out, exts)
	}
	return out
}

// hasUsableExtension reports whether a name carries an extension the rest
// of the pipeline understands, rar volumes and par2 sets included.
func hasUsableExtension(name string) bool {
	if classify(name) != kindContent {
		return true
	}
	return knownExtensions[strings.ToLower(filepath.Ext(name))]
}

// renameObfuscated gives the job's files the names the par2 set records for
// them, then extensions from magic bytes to whatever still has none, on disk
// and in j.nzb.Files, and records the renames in the manifest so a restart
// sees the same names. It never fails the job: a rename that cannot be done
// is logged and left, and par2 or the unpacker judges the result as before.
func (j *job) renameObfuscated(ctx context.Context) {
	log := logging.FromContext(ctx)
	dir := j.contentDir()

	sets := newPar2Sets()
	// One intact par2 file describes its whole set, so the walk of a set's
	// files stops at the first that completes it: the index, then the
	// volumes smallest first. Each packet header is a read, and on NFS every
	// one was a round trip queued behind the transfers' writes, across
	// volumes holding thousands of packets (talos, 2026-10-28: a job held a
	// post-processing slot for minutes here). A job may carry several sets
	// (par2Sets), so the walk stops per set, the files of one taken to be
	// those that share its name; the set a packet belongs to is still the
	// one it names. A file whose name hides that it is par2 is opened only
	// when the named ones leave a set incomplete, or name none.
	named := make([]nzbFile, 0, len(j.nzb.Files))
	for _, f := range j.nzb.Files {
		if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
			named = append(named, f)
		}
	}
	sort.SliceStable(named, func(a, b int) bool {
		if ia, ib := named[a].Kind == kindPar2Index, named[b].Kind == kindPar2Index; ia != ib {
			return ia
		}
		return named[a].Bytes < named[b].Bytes
	})
	read, done := map[string][][16]byte{}, map[string]bool{}
	for _, f := range named {
		base := par2Base(f.Name)
		if done[base] {
			continue
		}
		read[base] = append(read[base], sets.parseFile(filepath.Join(dir, safeName(f.Name)))...)
		done[base] = sets.complete(read[base])
	}
	for _, f := range j.nzb.Files {
		if sets.allComplete() {
			break
		}
		if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
			continue
		}
		if p := filepath.Join(dir, safeName(f.Name)); isPar2File(p) {
			sets.parseFile(p)
		}
	}

	renamed := 0
	if len(sets.sets()) > 0 {
		var plan []plannedRename
		damaged := false
		for i, f := range j.nzb.Files {
			if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
				continue
			}
			p := filepath.Join(dir, safeName(f.Name))
			h, err := hash16k(p)
			if err != nil {
				continue
			}
			want := sets.nameByHash16k(h)
			if want == "" {
				// Known by a later slice only: a set's file, its start
				// damaged.
				if want = sets.matchBySlice(p); want != "" {
					damaged = true
				}
			}
			if want != "" && want != f.Name {
				plan = append(plan, plannedRename{i: i, to: want})
			}
		}
		// Several files that are each one file of the set are that file
		// posted in pieces (splitPlan): they keep their names, and par2
		// builds the file from them.
		plan, parts := splitPlan(plan)
		if len(parts) > 0 {
			damaged = true
			j.mu.Lock()
			j.splitParts = parts
			j.mu.Unlock()
			log.InfoContext(ctx, "usenet: the post carries a file in pieces; par2 joins it", "download", j.id, "pieces", len(parts))
		}
		if damaged {
			j.mu.Lock()
			j.damagedHead = true
			j.mu.Unlock()
		}
		renamed += j.renameSet(ctx, plan)
		names := sets.names()
		j.mu.Lock()
		j.par2Names = names
		j.par2Sets = sets
		j.mu.Unlock()
	}
	renamed += j.renameStrayVolumes(ctx)
	j.mu.Lock()
	inSet := make(map[string]bool, len(j.par2Names))
	for _, n := range j.par2Names {
		inSet[n] = true
	}
	parts := j.splitParts
	j.mu.Unlock()
	for i, f := range j.nzb.Files {
		if _, piece := parts[i]; piece {
			continue // a piece of a file par2 joins keeps its name
		}
		// A name the par2 set records is the file's name whatever it ends
		// in: given another, par2 no longer finds it, rebuilds or renames it
		// back, and the job's list names a file that is gone.
		if hasUsableExtension(f.Name) || inSet[f.Name] {
			continue
		}
		if ext := sniffExtension(filepath.Join(dir, safeName(f.Name))); ext != "" {
			j.renameContent(ctx, i, f.Name+ext)
			renamed++
		}
	}
	if renamed > 0 {
		if err := j.checkpoint(); err != nil {
			log.WarnContext(ctx, "usenet checkpoint failed", "download", j.id, "err", err)
		}
	}
}

// rarSetStem is the multi-volume set a RAR volume's name puts it in, lower
// case: "x" for x.rar, x.r00 and x.part03.rar alike. ok is false for a name
// that is no RAR volume's.
func rarSetStem(name string) (stem string, ok bool) {
	lower := strings.ToLower(name)
	if m := rarVolumeRE.FindStringSubmatch(lower); m != nil {
		return m[1], true
	}
	if strings.HasSuffix(lower, ".rar") {
		return strings.TrimSuffix(lower, ".rar"), true
	}
	return "", false
}

// renameStrayVolumes gives a RAR volume the name its poster gave it on the
// wire -- its first article's yEnc name= -- when the NZB lists it under a
// name that leaves it out of its set, and reports how many it renamed.
//
// An indexer's NZB can name a file its own way. nzbgeek lists the first
// volume of an old-style scene set as "<release>.part01.rar" and leaves
// x.r00, x.r01, ... as posted, the par2 set beside them covers the sample
// alone, and the archive's own header says old numbering, so the volume
// after "<release>.part01.rar" was looked for as "<release>.part01.r00"
// and the unpack failed "no such file or directory" with every article in
// hand (talos, the week to 2026-11-27: 225 failures, 216 of them one
// series' episodes, each grabbed again and again). SABnzbd takes the yEnc
// name for any file unless it looks obfuscated (verify_nzf_filename); this
// is that for the one case it decides an unpack in, and no wider, because
// an obfuscated post puts a different name on every article and the
// importer reads a media file's name:
//
//   - the file is a RAR volume by its bytes;
//   - its listed name is not already in a set with another of the job's
//     files (a consistent listing is left alone, whatever the wire says);
//   - its wire name is a RAR volume's, in a set another of the job's files
//     is listed in, and no file holds that name.
//
// It runs after the par2 names, which are the stronger evidence where a
// set has them. The unpacker is the judge of the result, as before: every
// volume's header and the packed file's checksum still have to agree.
func (j *job) renameStrayVolumes(ctx context.Context) int {
	j.mu.Lock()
	wire := append([]string(nil), j.wireNames...)
	j.mu.Unlock()

	held := map[string]bool{}
	sets := map[string]int{}
	for _, f := range j.nzb.Files {
		held[strings.ToLower(safeName(f.Name))] = true
		if stem, ok := rarSetStem(f.Name); ok {
			sets[stem]++
		}
	}
	var plan []plannedRename
	for i, f := range j.nzb.Files {
		if i >= len(wire) || wire[i] == "" {
			continue
		}
		to := safeName(wire[i])
		if strings.EqualFold(to, safeName(f.Name)) || held[strings.ToLower(to)] {
			continue
		}
		if listed, ok := rarSetStem(f.Name); ok && sets[listed] > 1 {
			continue
		}
		if set, ok := rarSetStem(to); !ok || sets[set] == 0 {
			continue
		}
		if sniffExtension(filepath.Join(j.contentDir(), safeName(f.Name))) != ".rar" {
			continue
		}
		held[strings.ToLower(to)] = true
		plan = append(plan, plannedRename{i: i, to: to})
	}
	return j.renameSet(ctx, plan)
}

// plannedRename is the name content file i is to take.
type plannedRename struct {
	i  int
	to string
}

// splitPlan takes out of plan the files that are pieces of one file: two or
// more planned for the same name of the set. A file posted in pieces --
// "Show - 19 [720p].mkv.001" to ".007", cut at a size, with a par2 set made
// of the whole file -- has every piece match that file, the first by its
// start and the rest by a slice. Renamed, the first took the file's name,
// the others collided with it, par2 then built the whole file over the
// first piece, and the job failed "scratch files are gone or altered: ...
// .mkv is 336575388 bytes, its yEnc header declared 52480000" (talos,
// 2026-11-28, every such episode from two anime indexers). The pieces keep
// their names instead (parts: file index to the name they are pieces of),
// par2 joins them, and dropJoinedPieces removes them once it has.
func splitPlan(plan []plannedRename) (kept []plannedRename, parts map[int]string) {
	n := map[string]int{}
	for _, p := range plan {
		n[p.to]++
	}
	for _, p := range plan {
		if n[p.to] < 2 {
			kept = append(kept, p)
			continue
		}
		if parts == nil {
			parts = map[int]string{}
		}
		parts[p.i] = p.to
	}
	return kept, parts
}

// dropJoinedPieces removes the pieces of every file par2 has built whole
// (splitPlan), after a repair: the file is on disk under the set's name,
// verified by par2, and adoptRepairedSet has put it on the job's list. A
// piece whose file par2 did not build stays, for the import to judge.
func (j *job) dropJoinedPieces(ctx context.Context) error {
	dir := j.contentDir()
	j.mu.Lock()
	defer j.mu.Unlock()
	removed := 0
	for i, target := range j.splitParts {
		st, err := os.Lstat(filepath.Join(dir, safeName(target)))
		if err != nil || !st.Mode().IsRegular() || i >= len(j.nzb.Files) {
			continue
		}
		if err := fsops.SafeRemove(ctx, dir, safeName(j.nzb.Files[i].Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if i < len(j.sizes) {
			j.sizes[i] = 0 // gone on purpose: verifyContent does not look for it
		}
		removed++
	}
	if removed > 0 {
		logging.FromContext(ctx).InfoContext(ctx, "usenet: removed the pieces of a file par2 joined", "download", j.id, "pieces", removed)
	}
	return nil
}

// renamingSuffix marks a file moved aside by renameSet until its name frees.
const renamingSuffix = ".clustarr-renaming"

// renameSet gives each planned file its name and reports how many it
// renamed. A name another planned file holds frees only once that file
// moves, so the file moves aside under a temporary name first and takes its
// name after every other: a swap or a longer cycle of names comes out
// whole. One rename at a time found each such name taken and left the
// set's volumes swapped (talos, 2026-10-28: "bad volume number" on six
// jobs). A name held by a file the plan does not move is left, as before.
func (j *job) renameSet(ctx context.Context, plan []plannedRename) int {
	held := map[string]bool{}
	for _, p := range plan {
		held[safeName(j.nzb.Files[p.i].Name)] = true
	}
	renamed := 0
	type movedAside struct {
		plannedRename
		from string
	}
	var aside []movedAside
	for _, p := range plan {
		if held[safeName(p.to)] {
			from := j.nzb.Files[p.i].Name
			if j.renameContent(ctx, p.i, safeName(p.to)+renamingSuffix) {
				aside = append(aside, movedAside{p, from})
			}
			continue
		}
		if j.renameContent(ctx, p.i, p.to) {
			renamed++
		}
	}
	for _, p := range aside {
		if j.renameContent(ctx, p.i, p.to) {
			renamed++
			continue
		}
		// The name stayed taken (its holder could not move): back to the
		// name the file had, unless a planned file took that meanwhile, when
		// the temporary name stays and the unpacker judges the set.
		j.renameContent(ctx, p.i, p.from)
	}
	return renamed
}

// renameContent renames content file i to to, on disk and in j.nzb.Files,
// and records it in the manifest's renames keyed on the wire name, so a
// restart sees the same names. It reports whether it renamed: a name
// already taken is left, and so is one that cannot be renamed.
func (j *job) renameContent(ctx context.Context, i int, to string) bool {
	from := safeName(j.nzb.Files[i].Name)
	to = safeName(to)
	if !renamePath(ctx, j.contentDir(), from, to) {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.renames == nil {
		j.renames = map[string]string{}
	}
	orig := from
	for o, cur := range j.renames { // keep the chain keyed on the wire name
		if cur == from {
			orig = o
			break
		}
	}
	j.renames[orig] = to
	j.nzb.Files[i].Name = to
	j.nzb.Files[i].Kind = classify(to)
	return true
}

// applyRenames replays a manifest's renames onto freshly parsed files, so a
// job re-attached after a restart sees the names on disk.
func applyRenames(files []nzbFile, renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for i := range files {
		if to, ok := renames[safeName(files[i].Name)]; ok {
			files[i].Name = to
			files[i].Kind = classify(to)
		}
	}
}

// failedInsideArchive reports whether any missing article belongs to an
// archive volume -- the case where par2 failing to repair is final. A
// missing article in an nfo, a sample or a par2 volume leaves the archive
// whole, and its own checksums can judge it.
// adoptRepairedSet brings the job's file list into line with the set par2
// has just verified. par2 recreates a target it cannot find under the set's
// own name -- a file that never reached the disk because every one of its
// articles failed, or one so holed that no slice matched -- and leaves a
// holed copy where it was, under the wire name. The unpacker keys its entry
// points and its volume chain on the file list, so the 2026-09-24 grab read
// part01 from the holed copy and then could not find part03, which par2 had
// renamed: a verified, repairable set failed as writeError. After a repair
// the set's names are the content: every set file on disk that no entry
// carries is adopted, and once one was, an archive entry with failed
// articles that is not a set file is the holed copy par2 replaced -- it is
// removed, and it is no longer an entry point. The gate on an adoption is
// what keeps a damaged archive the set never covered (a subs.rar posted
// without recovery data) where it is.
func (j *job) adoptRepairedSet(ctx context.Context) error {
	dir := j.contentDir()
	log := logging.FromContext(ctx)
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.par2Names) == 0 {
		return nil
	}
	carried := make(map[string]bool, len(j.nzb.Files))
	for _, f := range j.nzb.Files {
		carried[f.Name] = true
	}
	adopted := 0
	for _, n := range j.par2Names {
		if carried[n] {
			continue
		}
		st, err := os.Lstat(filepath.Join(dir, safeName(n)))
		if err != nil || !st.Mode().IsRegular() {
			continue
		}
		j.nzb.Files = append(j.nzb.Files, nzbFile{Name: n, Kind: classify(n), Bytes: st.Size()})
		adopted++
		log.InfoContext(ctx, "usenet: adopted a file par2 rebuilt under the set's name", "download", j.id, "file", n)
	}
	if adopted == 0 {
		return nil
	}
	isSet := make(map[string]bool, len(j.par2Names))
	for _, n := range j.par2Names {
		isSet[n] = true
	}
	for i := range j.failedSegs {
		f := &j.nzb.Files[i]
		if f.Kind != kindArchive || isSet[f.Name] || (j.failedSegs[i].count() == 0 && f.Unlisted == 0) {
			continue
		}
		if _, err := os.Lstat(filepath.Join(dir, safeName(f.Name))); err == nil {
			if err := fsops.SafeRemove(ctx, dir, safeName(f.Name)); err != nil {
				return err
			}
			log.InfoContext(ctx, "usenet: removed the holed copy of a file par2 rebuilt under the set's name", "download", j.id, "file", f.Name)
		}
		f.Kind = kindContent
	}
	return nil
}

func (j *job) failedInsideArchive() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	for fi, b := range j.failedSegs {
		if j.nzb.Files[fi].Kind != kindArchive {
			continue
		}
		if b.count() > 0 || j.nzb.Files[fi].Unlisted > 0 {
			return true
		}
	}
	return false
}

// checkDiskSpace is the pre-flight udl runs before a grab: the working area
// needs twice the release (the articles and the unpacked result) plus
// headroom, the publish area once plus headroom, and a release whose peak is
// more than the whole scratch budget can never start. A release that cannot
// fit fails as diskFull before it moves 10 GB, not after; diskFull is a local
// fault and is never blocklisted.
func (j *job) checkDiskSpace() error {
	if b := j.client.cfg.ScratchBudget; b > 0 && j.scratchNeed() > b {
		return fmt.Errorf("%w: this release needs %d bytes of scratch (2x its size plus %d headroom), more than the whole scratch budget, %d",
			errPreflightSpace, j.scratchNeed(), diskHeadroomBytes, b)
	}
	free := j.client.freeBytes
	for _, chk := range []struct {
		dir  string
		mult int64
		need int64
	}{
		{j.client.cfg.ScratchDir, 2, j.scratchNeed()},
		{j.client.cfg.PublishDir, 1, j.nzb.TotalBytes + diskHeadroomBytes},
	} {
		got, err := free(chk.dir)
		if err != nil {
			continue // a volume that cannot be asked is judged by its writes, as before
		}
		if got < chk.need {
			return fmt.Errorf("%w: %d bytes free on %s, this release needs %d (%dx its size plus %d headroom)",
				errPreflightSpace, got, chk.dir, chk.need, chk.mult, diskHeadroomBytes)
		}
	}
	return nil
}
