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
	"bytes"
	"crypto/md5" //nolint:gosec // PAR2 specifies MD5.
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// par2Sets is the par2 sets of one job, by set id.
//
// A post usually carries one set over all its files. Some carry a set per
// file -- thirteen episodes, each "<episode>.mkv.par2" with its own volumes,
// and five more for the checksum files beside them (talos, 2026-11-27, a
// 98 GB season) -- and a job read as one set gave one file its name, pointed
// par2 at one set with every other file as a candidate for it, and repaired
// nothing of the file that had lost an article: see [job.par2Runs].
type par2Sets struct {
	byID  map[[16]byte]*par2Set
	order [][16]byte // first read first
}

func newPar2Sets() *par2Sets { return &par2Sets{byID: map[[16]byte]*par2Set{}} }

// parse walks the packets of one par2 file into the sets they name, and
// answers the ids it saw.
func (ss *par2Sets) parse(r io.ReaderAt, size int64) [][16]byte {
	var seen [][16]byte
	walkPar2(r, size, func(id [16]byte, typ, body []byte) {
		set, ok := ss.byID[id]
		if !ok {
			set = newPar2Set()
			set.ID = id
			ss.byID[id] = set
			ss.order = append(ss.order, id)
		}
		if len(seen) == 0 || seen[len(seen)-1] != id {
			seen = append(seen, id)
		}
		set.take(typ, body)
	})
	return seen
}

// parseFile is parse of the file at path; a file that cannot be read names
// no set.
func (ss *par2Sets) parseFile(path string) [][16]byte {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = fh.Close() }()
	st, err := fh.Stat()
	if err != nil {
		return nil
	}
	return ss.parse(fh, st.Size())
}

// complete reports whether every set of ids is read whole, and there is one.
func (ss *par2Sets) complete(ids [][16]byte) bool {
	for _, id := range ids {
		if set := ss.byID[id]; set == nil || !set.complete() {
			return false
		}
	}
	return len(ids) > 0
}

// allComplete reports whether every set read so far is read whole, and
// there is one.
func (ss *par2Sets) allComplete() bool { return ss.complete(ss.order) }

// sets lists the sets that describe a file, first read first.
func (ss *par2Sets) sets() []*par2Set {
	out := make([]*par2Set, 0, len(ss.order))
	for _, id := range ss.order {
		if set := ss.byID[id]; len(set.Files) > 0 {
			out = append(out, set)
		}
	}
	return out
}

// names is every file name any set records, sorted.
func (ss *par2Sets) names() []string {
	seen := map[string]bool{}
	var out []string
	for _, set := range ss.sets() {
		for _, d := range set.Files {
			if !seen[d.Name] {
				seen[d.Name] = true
				out = append(out, d.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// nameByHash16k returns the name a set records for the file whose first
// 16 KiB hash to h, or "".
func (ss *par2Sets) nameByHash16k(h [16]byte) string {
	for _, set := range ss.sets() {
		if name := set.nameByHash16k(h); name != "" {
			return name
		}
	}
	return ""
}

// matchBySlice names a damaged file by an intact slice, as
// [par2Set.matchBySlice] does for one set. Across several sets a slice
// counts only when one file of one set owns it: the sets of one post share
// slices -- every episode of that season opens with the same megabytes of
// reserved zeros, one whole slice of them in each set -- and such a slice
// names every file, so it names none.
func (ss *par2Sets) matchBySlice(path string) string {
	sets := ss.sets()
	if len(sets) == 1 {
		return sets[0].matchBySlice(path)
	}
	bySize := map[uint64][]*par2Set{}
	var sizes []uint64
	for _, set := range sets {
		if set.SliceSize == 0 || set.SliceSize > 1<<30 || len(set.Slices) == 0 {
			continue
		}
		if _, ok := bySize[set.SliceSize]; !ok {
			sizes = append(sizes, set.SliceSize)
		}
		bySize[set.SliceSize] = append(bySize[set.SliceSize], set)
	}
	for _, size := range sizes {
		if name := matchSliceAcross(path, size, bySize[size]); name != "" {
			return name
		}
	}
	return ""
}

// matchSliceAcross hashes path in slices of size until one belongs to one
// file of one of sets alone, and returns that file's name.
func matchSliceAcross(path string, size uint64, sets []*par2Set) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, size)
	for {
		n, err := io.ReadFull(f, buf)
		if n == 0 {
			return ""
		}
		for i := n; i < len(buf); i++ {
			buf[i] = 0
		}
		sum := md5.Sum(buf) //nolint:gosec // PAR2 specifies MD5.
		owner, owners := "", 0
		for _, set := range sets {
			if id, ok := set.Slices[sum]; ok {
				if d, ok := set.Files[id]; ok {
					owner = d.Name
					owners++
				}
			}
		}
		if owners == 1 {
			return owner
		}
		if err != nil {
			return ""
		}
	}
}

// par2Base is the set a par2 file's name puts it in, lower case: "x" for
// x.par2 and x.vol003+04.par2 alike. It only orders the reading: the set a
// file belongs to is the one its packets name.
func par2Base(name string) string {
	lower := strings.ToLower(name)
	if m := par2VolumeRE.FindStringSubmatch(lower); m != nil {
		return m[1]
	}
	return strings.TrimSuffix(lower, ".par2")
}

// par2FileSetID is the id of the set the par2 file at path belongs to: its
// first packet header's, looked for through the file's first megabyte when
// the file does not open with one (a hole where its first article was).
func par2FileSetID(path string) (id [16]byte, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return id, false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 64)
	if n, _ := io.ReadFull(f, buf); n == len(buf) && bytes.Equal(buf[:8], par2Magic) {
		copy(id[:], buf[32:48])
		return id, true
	}
	head := make([]byte, par2ScanChunk)
	n, _ := f.ReadAt(head, 0)
	i := bytes.Index(head[:n], par2Magic)
	if i < 0 || i+48 > n {
		return id, false
	}
	copy(id[:], head[i+32:i+48])
	return id, true
}

// par2Run is one set of a job that carries several, as par2 is to be run
// on it.
type par2Run struct {
	// index is the file par2 is pointed at, a name in the content
	// directory: the set's index, or its smallest volume.
	index string
	// volumes are the set's other par2 files.
	volumes []string
	// targets are the names the set records for its files; nil when no
	// file of the set was read whole, and what it covers is unknown.
	targets []string
	// bytes is the targets' length, zero when unknown.
	bytes int64
}

// par2Runs lists the job's par2 sets, one run each, when it carries more
// than one; nil for the usual job of one set (or of none that can be read),
// which [job.repair] hands to par2 as it always has.
//
// The set of a par2 file is the one its packets name, read here from one
// header a file: names are the poster's, and an index and its volumes need
// not share one. This runs only when something is to be repaired.
func (j *job) par2Runs() []par2Run {
	dir := j.contentDir()
	j.mu.Lock()
	files := append([]nzbFile(nil), j.nzb.Files...)
	sets := j.par2Sets
	j.mu.Unlock()

	type member struct {
		name  string
		index bool
		bytes int64
	}
	byID := map[[16]byte][]member{}
	var order [][16]byte
	for _, f := range files {
		if f.Kind != kindPar2Index && f.Kind != kindPar2Volume {
			continue
		}
		name := safeName(f.Name)
		id, ok := par2FileSetID(filepath.Join(dir, name))
		if !ok {
			continue
		}
		if _, seen := byID[id]; !seen {
			order = append(order, id)
		}
		byID[id] = append(byID[id], member{name: name, index: f.Kind == kindPar2Index, bytes: f.Bytes})
	}
	if len(order) < 2 {
		return nil
	}
	runs := make([]par2Run, 0, len(order))
	for _, id := range order {
		members := byID[id]
		// The index first, then the smallest: an index holds no recovery
		// data, and a set posted without one is entered by any volume.
		sort.SliceStable(members, func(a, b int) bool {
			if members[a].index != members[b].index {
				return members[a].index
			}
			if members[a].bytes != members[b].bytes {
				return members[a].bytes < members[b].bytes
			}
			return members[a].name < members[b].name
		})
		run := par2Run{index: members[0].name}
		for _, m := range members[1:] {
			run.volumes = append(run.volumes, m.name)
		}
		if sets != nil {
			if set := sets.byID[id]; set != nil && set.complete() {
				for _, d := range set.Files {
					run.targets = append(run.targets, safeName(d.Name))
					run.bytes += int64(d.Length) //nolint:gosec // a file's length
				}
				sort.Strings(run.targets)
			}
		}
		runs = append(runs, run)
	}
	// The largest first: the set over the content, before a checksum
	// file's.
	sort.SliceStable(runs, func(a, b int) bool { return runs[a].bytes > runs[b].bytes })
	return runs
}

// par2RunPlan is which of a job's sets par2 is run on, and with what.
type par2RunPlan struct {
	// runs are the sets to repair, in order.
	runs []par2Run
	// unclaimed are the files no set names: the candidates each run is
	// given beside its own volumes, for par2 to match by content.
	unclaimed []string
	// skipped is how many sets are left alone, every file of theirs whole.
	skipped int
}

// planPar2Runs decides which sets need par2. failed holds the content files
// that lost an article, onDisk every regular file in the content directory,
// both by name there.
//
// A set every file of which is on disk under the set's name with every
// article verified on receipt needs nothing: that is the quick check
// [job.repair] makes of a whole job, one set at a time. A set is run when
// one of its files lost an article, when what it covers is unknown, when a
// file's start is damaged (damagedHead: which file is not known here), or
// when a file of its own is not on disk while some file no set names lost
// an article -- that file may be the one, under another name or never
// written at all.
func planPar2Runs(runs []par2Run, failed, onDisk map[string]bool, damagedHead bool) par2RunPlan {
	claimed := map[string]bool{}
	par2Files := map[string]bool{}
	for _, r := range runs {
		par2Files[r.index] = true
		for _, v := range r.volumes {
			par2Files[v] = true
		}
		for _, t := range r.targets {
			claimed[t] = true
		}
	}
	var plan par2RunPlan
	for name := range onDisk {
		if !claimed[name] && !par2Files[name] {
			plan.unclaimed = append(plan.unclaimed, name)
		}
	}
	sort.Strings(plan.unclaimed)
	unclaimedFailed := false
	for name := range failed {
		if !claimed[name] && !par2Files[name] {
			unclaimedFailed = true
		}
	}
	for _, r := range runs {
		need := damagedHead || r.targets == nil
		for _, t := range r.targets {
			if failed[t] || (!onDisk[t] && unclaimedFailed) {
				need = true
			}
		}
		if need {
			plan.runs = append(plan.runs, r)
		} else {
			plan.skipped++
		}
	}
	return plan
}
