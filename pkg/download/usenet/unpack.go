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
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrEncrypted is returned when the release turns out to be password
// protected and no usable password is known.
//
// It is a sentinel and not a log line because it is the *arr failed-download
// contract: an encrypted release is reported as such -- Item.IsEncrypted,
// DownloadFailureEncrypted -- so the Download is failed, blocklisted and
// re-searched. Silently unpacking it would write garbage into the library,
// which is the failure mode this exists to prevent.
var ErrEncrypted = errors.New("usenet: archive is password protected")

// ErrUnsafeArchivePath is returned when an archive entry would write outside
// the destination. Archives come from an indexer and are untrusted input.
var ErrUnsafeArchivePath = errors.New("usenet: archive entry escapes the destination")

// unpackDirName is the working directory extraction runs into. SABnzbd uses a
// "_UNPACK_" prefix for the same reason: a half-extracted directory must be
// visibly distinct from a finished one, because the importer watches for
// finished ones.
const unpackDirName = "_UNPACK_"

// rarPartRE matches the "name.partNN.rar" naming scheme and captures NN.
//
// The check for a FIRST volume is deliberately not one regex with an
// alternation: a lazy `(.*?)\.rar$` branch happily matches
// "release.part02.rar" by letting the prefix swallow ".part02", so every
// volume of a set would be treated as an entry point and the tail of the set
// would be extracted once per volume. RE2 has no lookahead to express "a .rar
// that is not a .partNN.rar", so [isFirstRarVolume] decides it in code.
var rarPartRE = regexp.MustCompile(`(?i)^(.*)\.part(\d+)\.rar$`)

// isFirstRarVolume reports whether name is the volume rardecode should be
// pointed at. rardecode follows the chain itself from there, so opening a
// later volume would extract a fragment.
func isFirstRarVolume(name string) bool {
	lower := strings.ToLower(name)
	if m := rarPartRE.FindStringSubmatch(lower); m != nil {
		n, err := strconv.Atoi(m[2])
		return err == nil && n == 1
	}
	// The old scheme is name.rar, name.r00, name.r01: only the .rar starts it.
	return strings.HasSuffix(lower, ".rar")
}

// unpackResult says what extraction concluded.
type unpackResult struct {
	// Extracted is the number of files written.
	Extracted int
	// Encrypted is true when an archive (or its headers) is encrypted and the
	// password did not open it.
	Encrypted bool
}

// unpackProgress counts the bytes an extraction has written so far, which
// the job reports in status.message while its extracting stage runs: an
// unpack of a 20 GB set over NFS takes half an hour, and with nothing in
// status moving it read as a stall (2026-10-07). A nil *unpackProgress
// counts nothing.
type unpackProgress struct{ written atomic.Int64 }

// Written is the bytes extraction has written so far.
func (p *unpackProgress) Written() int64 {
	if p == nil {
		return 0
	}
	return p.written.Load()
}

// countingReader adds every byte read through it to p.
type countingReader struct {
	r io.Reader
	p *unpackProgress
}

func (c countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	if c.p != nil && n > 0 {
		c.p.written.Add(int64(n))
	}
	return n, err
}

// unpackArchives extracts every archive set in srcDir into dstDir, counting
// the bytes it writes into progress, and then every archive those held
// (unpackNested), to maxNestedUnpack levels.
//
// Only FIRST volumes are opened: rardecode.OpenReader follows the volume chain
// itself, so handing it name.part02.rar as well would extract the tail of the
// set a second time.
func unpackArchives(
	ctx context.Context, srcDir, dstDir, password string, files []nzbFile, progress *unpackProgress,
) (unpackResult, error) {
	ctx, span := tracing.Start(ctx, "usenet.unpack")
	defer span.End()

	log := logging.FromContext(ctx)

	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return unpackResult{}, fmt.Errorf("usenet: create unpack dir: %w", err)
	}

	var res unpackResult
	for _, name := range archiveEntryPoints(files) {
		src := filepath.Join(srcDir, safeName(name))
		if _, err := os.Stat(src); err != nil {
			// The file never landed -- a missing article par2 could not
			// repair. Skip it rather than failing here; the health gate has
			// already decided whether this download is viable.
			continue
		}
		n, err := extractArchive(ctx, name, src, dstDir, password, progress)
		res.Extracted += n
		if err != nil {
			if errors.Is(err, ErrEncrypted) {
				res.Encrypted = true
			}
			return res, err
		}
		log.DebugContext(ctx, "unpacked archive", "archive", name, "files", n)
	}
	if err := unpackNested(ctx, dstDir, password, progress, &res); err != nil {
		return res, err
	}
	return res, nil
}

// extractArchive extracts the archive set whose first volume is src, named
// name, into dst.
func extractArchive(ctx context.Context, name, src, dst, password string, progress *unpackProgress) (int, error) {
	switch strings.ToLower(path.Ext(name)) {
	case ".7z":
		return unpack7z(ctx, src, dst, password, progress)
	case ".zip":
		return unpackZip(ctx, src, dst, progress)
	default:
		return unpackRar(ctx, src, dst, password, progress)
	}
}

// maxNestedUnpack is how many levels of archive inside the NZB's archives
// the unpack opens. A rar posted inside a rar was published as an archive,
// which the import refused as the release's fault; SABnzbd and NZBGet open
// nested archives too. A bound keeps an archive that nests without end from
// running the unpack forever.
const maxNestedUnpack = 3

// unpackNested extracts the archives the unpack wrote under dir, each into
// the directory it sits in, removes the set it opened, and does the same for
// what those held, to maxNestedUnpack levels. An archive still nested past
// the last level is left as it is, logged.
func unpackNested(ctx context.Context, dir, password string, progress *unpackProgress, res *unpackResult) error {
	log := logging.FromContext(ctx)
	for depth := 1; ; depth++ {
		entries := nestedEntryPoints(dir)
		if len(entries) == 0 {
			return nil
		}
		if depth > maxNestedUnpack {
			log.WarnContext(ctx, "usenet: archives nested past the levels the unpack opens are published as they are",
				"levels", maxNestedUnpack, "archives", entries)
			return nil
		}
		for _, rel := range entries {
			src := filepath.Join(dir, rel)
			n, err := extractArchive(ctx, rel, src, filepath.Dir(src), password, progress)
			res.Extracted += n
			if err != nil {
				if errors.Is(err, ErrEncrypted) {
					res.Encrypted = true
				}
				return err
			}
			for _, v := range archiveSetFiles(filepath.Dir(src), filepath.Base(rel)) {
				if err := fsops.SafeRemove(ctx, dir, filepath.Join(filepath.Dir(rel), v)); err != nil && !errors.Is(err, os.ErrNotExist) {
					return err
				}
			}
			log.InfoContext(ctx, "usenet: unpacked a nested archive", "archive", rel, "files", n, "depth", depth)
		}
	}
}

// nestedEntryPoints is the first volume of every archive set under dir,
// relative to it.
func nestedEntryPoints(dir string) []string {
	var files []nzbFile
	for _, rel := range regularFiles(dir) {
		files = append(files, nzbFile{Name: rel, Kind: classify(rel)})
	}
	return archiveEntryPoints(files)
}

// archiveSetStem is the name an archive set's volumes share: "x" for
// x.part01.rar, x.r00, x.rar, x.7z and x.zip. ok is false for a name that
// is no archive volume.
func archiveSetStem(name string) (stem string, ok bool) {
	if m := rarVolumeRE.FindStringSubmatch(name); m != nil {
		return m[1], true
	}
	lower := strings.ToLower(name)
	for _, ext := range []string{".rar", ".7z", ".zip"} {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)], true
		}
	}
	return "", false
}

// archiveSetFiles names the archive volumes in dir that belong to entry's
// set, entry included: what goes once the set is extracted.
func archiveSetFiles(dir, entry string) []string {
	stem, ok := archiveSetStem(entry)
	if !ok {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if !e.Type().IsRegular() || classify(e.Name()) != kindArchive {
			continue
		}
		if s, ok := archiveSetStem(e.Name()); ok && strings.EqualFold(s, stem) {
			out = append(out, e.Name())
		}
	}
	return out
}

// archiveEntryPoints returns one entry per archive SET, in a stable order.
func archiveEntryPoints(files []nzbFile) []string {
	var out []string
	for _, f := range files {
		if f.Kind != kindArchive {
			continue
		}
		lower := strings.ToLower(f.Name)
		switch {
		case strings.HasSuffix(lower, ".7z"), strings.HasSuffix(lower, ".zip"):
			out = append(out, f.Name)
		case isFirstRarVolume(lower):
			out = append(out, f.Name)
		default:
			// name.r00 / name.part02.rar and friends: continuation volumes,
			// reached through the first one.
		}
	}
	sort.Strings(out)
	return out
}

func unpackRar(ctx context.Context, src, dst, password string, progress *unpackProgress) (int, error) {
	opts := []rardecode.Option{}
	if password != "" {
		opts = append(opts, rardecode.Password(password))
	}
	rc, err := rardecode.OpenReader(src, opts...)
	if err != nil {
		if errors.Is(err, rardecode.ErrBadPassword) {
			// Encrypted HEADERS: even the file list is unreadable.
			return 0, fmt.Errorf("%w: %s: %w", ErrEncrypted, filepath.Base(src), err)
		}
		return 0, fmt.Errorf("usenet: open rar %s: %w", filepath.Base(src), err)
	}
	defer func() { _ = rc.Close() }()

	written := 0
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		hdr, err := rc.Next()
		if errors.Is(err, io.EOF) {
			return written, nil
		}
		if err != nil {
			if errors.Is(err, rardecode.ErrBadPassword) {
				return written, fmt.Errorf("%w: %s: %w", ErrEncrypted, filepath.Base(src), err)
			}
			return written, fmt.Errorf("usenet: read rar %s: %w", filepath.Base(src), err)
		}
		if hdr.IsDir {
			continue
		}
		if (hdr.Encrypted || hdr.HeaderEncrypted) && password == "" {
			return written, fmt.Errorf("%w: %s holds encrypted entry %q", ErrEncrypted, filepath.Base(src), hdr.Name)
		}
		if err := writeArchiveEntry(dst, hdr.Name, rc, progress); err != nil {
			if errors.Is(err, rardecode.ErrBadPassword) {
				return written, fmt.Errorf("%w: %s: %w", ErrEncrypted, filepath.Base(src), err)
			}
			return written, err
		}
		written++
	}
}

func unpack7z(ctx context.Context, src, dst, password string, progress *unpackProgress) (int, error) {
	var (
		rc  *sevenzip.ReadCloser
		err error
	)
	if password != "" {
		rc, err = sevenzip.OpenReaderWithPassword(src, password)
	} else {
		rc, err = sevenzip.OpenReader(src)
	}
	if err != nil {
		// sevenzip reports an encrypted header as a read failure on open.
		// There is no sentinel to match on, so the failure is reported as
		// encrypted only when no password was supplied, which is the case a
		// human can act on.
		if password == "" {
			return 0, fmt.Errorf("%w: %s: %w", ErrEncrypted, filepath.Base(src), err)
		}
		return 0, fmt.Errorf("usenet: open 7z %s: %w", filepath.Base(src), err)
	}
	defer func() { _ = rc.Close() }()

	written := 0
	for _, f := range rc.File {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		r, err := f.Open()
		if err != nil {
			return written, fmt.Errorf("usenet: open 7z entry %q: %w", f.Name, err)
		}
		err = writeArchiveEntry(dst, f.Name, r, progress)
		_ = r.Close()
		if err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

func unpackZip(ctx context.Context, src, dst string, progress *unpackProgress) (int, error) {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return 0, fmt.Errorf("usenet: open zip %s: %w", filepath.Base(src), err)
	}
	defer func() { _ = zr.Close() }()

	written := 0
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if f.FileInfo().IsDir() {
			continue
		}
		// Bit 0 of the general-purpose flags is "encrypted". archive/zip
		// cannot decrypt, so an encrypted entry must be reported rather than
		// extracted as ciphertext.
		if f.Flags&0x1 != 0 {
			return written, fmt.Errorf("%w: %s holds encrypted entry %q", ErrEncrypted, filepath.Base(src), f.Name)
		}
		r, err := f.Open()
		if err != nil {
			return written, fmt.Errorf("usenet: open zip entry %q: %w", f.Name, err)
		}
		err = writeArchiveEntry(dst, f.Name, r, progress)
		_ = r.Close()
		if err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// writeArchiveEntry writes one entry under dst, refusing any name that would
// land outside it, and counts what it writes into progress.
func writeArchiveEntry(dst, name string, r io.Reader, progress *unpackProgress) error {
	target, err := safeJoin(dst, name)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("usenet: create %s: %w", filepath.Dir(target), err)
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("usenet: create %s: %w", target, err)
	}
	if _, err := io.Copy(f, countingReader{r: r, p: progress}); err != nil {
		_ = f.Close()
		return fmt.Errorf("usenet: write %s: %w", target, err)
	}
	return f.Close()
}

// safeJoin resolves an archive entry name under root, or refuses it.
//
// This is the zip-slip guard, and it REFUSES rather than re-roots. Silently
// rewriting "../../etc/cron.d/x" into a file inside the destination would be
// safe, but it would also publish an entry whose name says plainly that the
// archive is hostile or broken -- and a release like that belongs in the
// failed-download path, not in the library under a laundered name.
//
// An absolute entry name is refused for the same reason: no legitimate
// release archive names "/etc/passwd".
func safeJoin(root, name string) (string, error) {
	bad := func() (string, error) { return "", fmt.Errorf("%w: %q", ErrUnsafeArchivePath, name) }

	slashed := strings.ReplaceAll(name, "\\", "/")
	if slashed == "" || strings.HasPrefix(slashed, "/") || filepath.IsAbs(name) {
		return bad()
	}
	cleaned := path.Clean(slashed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return bad()
	}
	target := filepath.Join(root, filepath.FromSlash(cleaned))
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return bad()
	}
	return target, nil
}

// cleanupAfterUnpack removes the source archives, the par2 volumes and
// anything matching the configured junk patterns.
//
// Archives and par2 are separate flags because they are separate decisions: a
// release that was not packed at all has no archives to delete, but its par2
// volumes are still junk nobody wants in the library.
func cleanupAfterUnpack(ctx context.Context, dir string, files []nzbFile, removeArchives, removePar2 bool, patterns []string) error {
	for _, f := range files {
		remove := false
		switch f.Kind {
		case kindArchive:
			remove = removeArchives
		case kindPar2Index, kindPar2Volume:
			remove = removePar2
		case kindContent:
		}
		if !remove {
			continue
		}
		if err := fsops.SafeRemove(ctx, dir, safeName(f.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if len(patterns) == 0 {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("usenet: read %s: %w", dir, err)
	}
	for _, e := range entries {
		for _, pat := range patterns {
			ok, mErr := path.Match(strings.ToLower(pat), strings.ToLower(e.Name()))
			if mErr != nil || !ok {
				continue
			}
			if err := fsops.SafeRemove(ctx, dir, e.Name()); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			break
		}
	}
	return nil
}
