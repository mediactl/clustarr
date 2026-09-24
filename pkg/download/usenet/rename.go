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
	// SliceSize is the Main packet's slice size, zero until one is seen.
	SliceSize uint64
	// Files is keyed by file id.
	Files map[[16]byte]par2FileDesc
	// Slices maps a slice's MD5 to the id of the file it belongs to.
	Slices map[[16]byte][16]byte
}

func newPar2Set() *par2Set {
	return &par2Set{Files: map[[16]byte]par2FileDesc{}, Slices: map[[16]byte][16]byte{}}
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
	header := make([]byte, 64)
	var off int64
	for off+64 <= size {
		if _, err := r.ReadAt(header, off); err != nil {
			return
		}
		if !bytes.Equal(header[:8], par2Magic) {
			off = s.nextMagic(r, size, off+1)
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
		s.take(typ, body)
		off += length
	}
}

// nextMagic returns the offset of the next packet magic at or after from,
// or size when there is none.
func (s *par2Set) nextMagic(r io.ReaderAt, size, from int64) int64 {
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
		// slice size 8, file count 4, then the file ids.
		if len(body) >= 12 {
			s.SliceSize = binary.LittleEndian.Uint64(body[:8])
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
// the magic step: what the importer, the unpacker and the cleanup know.
var knownExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".wmv": true, ".flv": true,
	".webm": true, ".ts": true, ".m2ts": true, ".mpg": true, ".mpeg": true, ".iso": true, ".img": true,
	".srt": true, ".sub": true, ".idx": true, ".ass": true, ".ssa": true, ".vtt": true,
	".nfo": true, ".sfv": true, ".txt": true, ".jpg": true, ".jpeg": true, ".png": true, ".gif": true,
	".mp3": true, ".flac": true, ".m4a": true, ".m4b": true, ".ogg": true, ".wav": true, ".aac": true,
	".epub": true, ".pdf": true, ".mobi": true, ".azw3": true, ".cbz": true, ".cbr": true,
	".rar": true, ".zip": true, ".7z": true, ".par2": true,
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

	set := newPar2Set()
	for _, f := range j.nzb.Files {
		p := filepath.Join(dir, safeName(f.Name))
		if f.Kind != kindPar2Index && f.Kind != kindPar2Volume && !isPar2File(p) {
			continue
		}
		fh, err := os.Open(p)
		if err != nil {
			continue
		}
		if st, err := fh.Stat(); err == nil {
			set.parse(fh, st.Size())
		}
		_ = fh.Close()
	}

	rename := func(i int, to string) {
		from := safeName(j.nzb.Files[i].Name)
		to = safeName(to)
		if from == to {
			return
		}
		if _, err := os.Lstat(filepath.Join(dir, to)); err == nil {
			log.WarnContext(ctx, "usenet: rename collides with an existing file; leaving it", "from", from, "to", to)
			return
		}
		if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, to)); err != nil {
			log.WarnContext(ctx, "usenet: rename failed", "from", from, "to", to, "error", err)
			return
		}
		j.mu.Lock()
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
		j.mu.Unlock()
		log.InfoContext(ctx, "usenet: renamed", "from", from, "to", to)
	}

	renamed := 0
	if len(set.Files) > 0 {
		for i, f := range j.nzb.Files {
			if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
				continue
			}
			p := filepath.Join(dir, safeName(f.Name))
			h, err := hash16k(p)
			if err != nil {
				continue
			}
			want := set.nameByHash16k(h)
			if want == "" {
				want = set.matchBySlice(p)
			}
			if want != "" && want != f.Name {
				rename(i, want)
				renamed++
			}
		}
		names := make([]string, 0, len(set.Files))
		for _, d := range set.Files {
			names = append(names, d.Name)
		}
		sort.Strings(names)
		j.mu.Lock()
		j.par2Names = names
		j.mu.Unlock()
	}
	for i, f := range j.nzb.Files {
		if hasUsableExtension(f.Name) {
			continue
		}
		if ext := sniffExtension(filepath.Join(dir, safeName(f.Name))); ext != "" {
			rename(i, f.Name+ext)
			renamed++
		}
	}
	if renamed > 0 {
		if err := j.checkpoint(); err != nil {
			log.WarnContext(ctx, "usenet checkpoint failed", "download", j.id, "error", err)
		}
	}
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
		if f.Kind != kindArchive || isSet[f.Name] || j.failedSegs[i].count() == 0 {
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
		if b.count() > 0 {
			return true
		}
	}
	return false
}

// checkDiskSpace is the pre-flight udl runs before a grab: the working area
// needs twice the release (the articles and the unpacked result) plus
// headroom, the publish area once plus headroom. A release that cannot fit
// fails as diskFull before it moves 10 GB, not after; diskFull is a local
// fault and is never blocklisted.
func (j *job) checkDiskSpace() error {
	free := j.client.freeBytes
	need := func(mult int64) int64 { return j.nzb.TotalBytes*mult + diskHeadroomBytes }
	for _, chk := range []struct {
		dir  string
		mult int64
	}{{j.client.cfg.ScratchDir, 2}, {j.client.cfg.PublishDir, 1}} {
		got, err := free(chk.dir)
		if err != nil {
			continue // a volume that cannot be asked is judged by its writes, as before
		}
		if got < need(chk.mult) {
			return fmt.Errorf("%w: %d bytes free on %s, this release needs %d (%dx its size plus %d headroom)",
				errPreflightSpace, got, chk.dir, need(chk.mult), chk.mult, diskHeadroomBytes)
		}
	}
	return nil
}
