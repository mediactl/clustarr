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
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// This file is SABnzbd's two name recoveries after the unpack, run on the
// directory about to be published (finalNames). renameObfuscated, before
// repair, is the first of its three: the outer par2 set names the files
// on the wire. An obfuscated season pack is posted as archives of hashed
// .mkv files ("02fb98288e35eea0f5b10ea420980959.mkv") beside a par2 set
// the uploader built against their real names, so:
//
//   - the par2 set an archive unpacked names the files it describes, by
//     the MD5 of their first 16 KiB or by any intact slice
//     (SABnzbd's process_unpacked_par2, recover_par2_names), and the spent
//     par2 files go;
//   - a file with no usable extension takes one from its magic bytes;
//   - one big file whose name is probably obfuscated takes the job's name
//     (SABnzbd's "Deobfuscate final filenames").
//
// A pack's hashed files keep their names when no par2 set names them:
// the job's name cannot say which episode each is, and the importer reads
// each file's container title for that (fileimport.inspectEpisodes).
// Nothing here fails the job: a name that cannot be recovered is left, and
// the importer judges the file as it is.

// obfuscatedMinBytes is the smallest file the job-name rename considers,
// SABnzbd's MIN_FILE_SIZE: a sample, an nfo or a subtitle beside the
// release is not the release.
const obfuscatedMinBytes = 10 << 20

// structuralExts name files by their place in a structure (a disc's
// streams and playlists, an archive's volumes, a par2 set), never for a
// release: SABnzbd's excluded_file_exts.
var structuralExts = map[string]bool{
	".vob": true, ".rar": true, ".par2": true, ".mts": true, ".m2ts": true,
	".cpi": true, ".clpi": true, ".mpl": true, ".mpls": true, ".bdm": true, ".bdmv": true,
}

var (
	md5NameRE = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	hexDotsRE = regexp.MustCompile(`^[a-f0-9.]{40,}$`)
	hexRunRE  = regexp.MustCompile(`[a-f0-9]{30}`)
)

// isProbablyObfuscated reads a file name as SABnzbd's is_probably_obfuscated
// does: a hash is obfuscated for certain, a name with the shape of words
// (capitals and lower case with a separator, three separators, digits with
// a separator, a capitalised word) is not, and a name that is neither is
// taken for obfuscated.
func isProbablyObfuscated(name string) bool {
	base := filepath.Base(name)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if md5NameRE.MatchString(base) || hexDotsRE.MatchString(base) || hexRunRE.MatchString(base) {
		return true
	}
	var digits, upper, lower, separators int
	for _, r := range base {
		switch {
		case unicode.IsDigit(r):
			digits++
		case unicode.IsUpper(r):
			upper++
		case unicode.IsLower(r):
			lower++
		case r == ' ' || r == '.' || r == '_':
			separators++
		}
	}
	switch {
	case upper >= 2 && lower >= 2 && separators >= 1: // "Great Distro"
		return false
	case separators >= 3: // "this is a download"
		return false
	case upper+digits >= 4 && lower >= 4 && separators >= 1: // "Beast 2020"
		return false
	case base != "" && unicode.IsUpper([]rune(base)[0]) && lower > 2 && float64(upper)/float64(lower) <= 0.25: // "Catullus"
		return false
	}
	return true
}

// finalNames recovers the names of what dir, the directory about to be
// published, holds: the content directory when nothing was unpacked, else
// the unpack directory. A content file's rename reaches the job's file
// list and the manifest (renameContent), as renameObfuscated's do.
func (j *job) finalNames(ctx context.Context, dir string) {
	content := dir == j.contentDir()
	var names []string
	if content {
		for _, f := range j.nzb.Files {
			if f.Kind == kindContent {
				names = append(names, safeName(f.Name))
			}
		}
	} else {
		recoverUnpackedNames(ctx, dir)
		names = regularFiles(dir)
	}
	from, to, ok := loneObfuscated(dir, names, j.publishName())
	if !ok {
		return
	}
	if !content {
		renamePath(ctx, dir, from, to)
		return
	}
	for i, f := range j.nzb.Files {
		if safeName(f.Name) == from {
			if j.renameContent(ctx, i, to) {
				if err := j.checkpoint(); err != nil {
					logging.FromContext(ctx).WarnContext(ctx, "usenet checkpoint failed", "download", j.id, "error", err)
				}
			}
			return
		}
	}
}

// publishName is the name a lone obfuscated file is published under: the
// release's title, else the NZB's when it is no hash itself (nzbgeek
// obfuscates its title meta), else none. Never the job's own name, which is
// the grab entry's id: "86-eighty-six-cb83c58857-50e5c90a59.mkv" parsed as
// absolute episode 50, and the import held it for a person (2026-10-27).
func (j *job) publishName() string {
	if j.title != "" {
		return j.title
	}
	if t := j.nzb.Title; t != "" && !isProbablyObfuscated(t) {
		return t
	}
	return ""
}

// recoverUnpackedNames names the files under dir by the par2 set unpacked
// beside them and removes the set, then gives a file with no usable
// extension one from its magic bytes.
func recoverUnpackedNames(ctx context.Context, dir string) {
	log := logging.FromContext(ctx)
	files := regularFiles(dir)
	set := newPar2Set()
	par2s := map[string]bool{}
	for _, rel := range files {
		p := filepath.Join(dir, rel)
		if !strings.EqualFold(filepath.Ext(rel), ".par2") && !isPar2File(p) {
			continue
		}
		par2s[rel] = true
		fh, err := os.Open(p)
		if err != nil {
			continue
		}
		if st, err := fh.Stat(); err == nil {
			set.parse(fh, st.Size())
		}
		_ = fh.Close()
	}
	if len(set.Files) > 0 {
		for _, rel := range files {
			if par2s[rel] {
				continue
			}
			p := filepath.Join(dir, rel)
			h, err := hash16k(p)
			if err != nil {
				continue
			}
			want := set.nameByHash16k(h)
			if want == "" {
				want = set.matchBySlice(p)
			}
			if want == "" || safeName(want) == filepath.Base(rel) {
				continue
			}
			renamePath(ctx, dir, rel, filepath.Join(filepath.Dir(rel), safeName(want)))
		}
	}
	for rel := range par2s {
		if err := fsops.SafeRemove(ctx, dir, rel); err != nil {
			log.WarnContext(ctx, "usenet: could not remove an unpacked par2 file", "file", rel, "error", err)
		}
	}
	for _, rel := range regularFiles(dir) {
		if hasUsableExtension(filepath.Base(rel)) {
			continue
		}
		if ext := sniffExtension(filepath.Join(dir, rel)); ext != "" {
			renamePath(ctx, dir, rel, rel+ext)
		}
	}
}

// loneObfuscated is the one file under dir, among names, that the job-name
// rename applies to: the only file of obfuscatedMinBytes or more outside
// the structural extensions, when its name is probably obfuscated. It
// returns the rename, relative to dir.
func loneObfuscated(dir string, names []string, jobName string) (from, to string, ok bool) {
	var big []string
	for _, rel := range names {
		st, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || !st.Mode().IsRegular() || st.Size() < obfuscatedMinBytes {
			continue
		}
		if structuralExts[strings.ToLower(filepath.Ext(rel))] {
			continue
		}
		big = append(big, rel)
	}
	if len(big) != 1 || jobName == "" || !isProbablyObfuscated(big[0]) {
		return "", "", false
	}
	from = big[0]
	to = filepath.Join(filepath.Dir(from), safeName(jobName)+filepath.Ext(from))
	if to == from {
		return "", "", false
	}
	return from, to, true
}

// regularFiles lists the regular files under dir, relative to it, sorted.
func regularFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		if rel, err := filepath.Rel(dir, p); err == nil {
			out = append(out, rel)
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// renamePath renames from to to under dir, both relative, and reports
// whether it did: a name already taken is left, as renameContent leaves it.
func renamePath(ctx context.Context, dir, from, to string) bool {
	log := logging.FromContext(ctx)
	if from == to {
		return false
	}
	if _, err := os.Lstat(filepath.Join(dir, to)); err == nil {
		log.WarnContext(ctx, "usenet: rename collides with an existing file; leaving it", "from", from, "to", to)
		return false
	}
	if err := os.Rename(filepath.Join(dir, from), filepath.Join(dir, to)); err != nil {
		log.WarnContext(ctx, "usenet: rename failed", "from", from, "to", to, "error", err)
		return false
	}
	log.InfoContext(ctx, "usenet: renamed", "from", from, "to", to)
	return true
}
