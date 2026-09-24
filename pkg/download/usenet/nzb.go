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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/javi11/nzbparser"
)

// ErrPayloadTooLarge is returned by [Client.Add] when the .nzb body exceeds
// the configured cap. Same discipline as every HTTP body in this repo: a
// package-level max, a capped read and a sentinel.
var ErrPayloadTooLarge = errors.New("usenet: nzb payload exceeds the size limit")

// ErrEmptyNZB is returned when a payload parses but carries no segments.
var ErrEmptyNZB = errors.New("usenet: nzb contains no articles")

// defaultMaxNZBBytes caps a .nzb body. A 100GB release with 700KiB segments is
// about 140,000 segments, roughly 15MB of XML; 32MiB is generous and still
// bounded.
const defaultMaxNZBBytes = 32 << 20

// fileKind classifies one file inside an NZB. The pipeline branches on it:
// par2 files drive repair, archives drive extraction, and everything else is
// content that is published as-is.
type fileKind int

const (
	kindContent fileKind = iota
	kindPar2Index
	kindPar2Volume
	kindArchive
)

var (
	// par2VolumeRE matches "name.vol000+01.par2", where the second number is
	// how many recovery blocks the volume carries (parchive spec, research
	// note §2.4), and the block-less "name.vol-01.par2" scheme real posts
	// use (nzbgeek, 2026-09-24: seven volumes named vol-01..vol-07, the
	// first of them the 65 KB index). Group 3 is the block count, empty when
	// the name carries none; criticalHealthPercent then estimates from the
	// volumes' size instead of reading the set as unrepairable.
	par2VolumeRE = regexp.MustCompile(`(?i)^(.*)\.vol[+\-]?(\d+)(?:[+\-](\d+))?\.par2$`)

	// rarVolumeRE matches both multi-volume naming schemes: name.partNN.rar
	// and the old name.rNN.
	rarVolumeRE = regexp.MustCompile(`(?i)^(.*?)(?:\.part(\d+)\.rar|\.r(\d+))$`)

	// passwordInNameRE is SABnzbd's "Name{{password}}" convention.
	passwordInNameRE = regexp.MustCompile(`\{\{(.+?)\}\}`)
)

// segment is one article to fetch.
type segment struct {
	// ID is the message-id WITHOUT angle brackets, exactly as the NZB
	// carries it. The wire layer adds them.
	ID string
	// Number is the NZB's 1-based part index. It orders segments; it does
	// NOT decide where the bytes go, which is what yEnc's "=ypart begin"
	// is for.
	Number int
	// Bytes is the article's wire size, used for progress before the part is
	// decoded and for the job's total.
	Bytes int64
}

// nzbFile is one file inside the NZB after classification.
type nzbFile struct {
	Name     string
	Kind     fileKind
	Bytes    int64
	Segments []segment
	// Blocks is the recovery-block count of a par2 volume, zero otherwise.
	// It is what decides whether a damaged set is repairable at all.
	Blocks int
	// Posted is the article's post time, for the propagation-delay check.
	Posted time.Time
}

// nzbJob is a parsed, classified NZB.
type nzbJob struct {
	// ID is the deterministic handle for this payload. It is what makes
	// [Client.Add] idempotent: the same .nzb added twice hashes to the same
	// id and returns the running transfer rather than starting a second copy.
	ID string

	Title    string
	Password string
	Files    []nzbFile

	TotalBytes    int64
	TotalSegments int
	// Posted is the earliest post time across the files, which is what the
	// propagation delay is measured from.
	Posted time.Time
}

// nzbID is the content hash that makes Add idempotent.
//
// It hashes the RAW payload rather than the parsed structure: two byte-equal
// .nzb bodies are the same job by definition, and hashing the parse would make
// the id depend on this package's own classification rules, so a change here
// would orphan every in-flight transfer on upgrade.
func nzbID(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:20])
}

// parseNZB parses and classifies a .nzb body.
func parseNZB(payload []byte, maxBytes int64) (*nzbJob, error) {
	if maxBytes > 0 && int64(len(payload)) > maxBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrPayloadTooLarge, len(payload))
	}

	parsed, err := nzbparser.ParseWithOptions(bytes.NewReader(payload), nzbparser.ParseOptions{RemoveDuplicates: true})
	if err != nil {
		return nil, fmt.Errorf("usenet: parse nzb: %w", err)
	}

	job := &nzbJob{ID: nzbID(payload)}
	job.Title = strings.TrimSpace(parsed.Meta["title"])
	job.Password = strings.TrimSpace(parsed.Meta["password"])

	for i := range parsed.Files {
		f := &parsed.Files[i]
		name := fileName(f)
		if name == "" {
			// Never guess. A file with no derivable name cannot be written,
			// renamed or matched to a par2 description, so it is dropped with
			// its segments rather than written to a made-up path.
			continue
		}
		nf := nzbFile{Name: name, Bytes: f.Bytes, Kind: classify(name)}
		if nf.Kind == kindPar2Volume {
			if m := par2VolumeRE.FindStringSubmatch(name); m != nil {
				nf.Blocks, _ = strconv.Atoi(m[3])
			}
		}
		if f.Date > 0 {
			nf.Posted = time.Unix(int64(f.Date), 0).UTC()
		}
		for _, s := range f.Segments {
			id := strings.TrimSpace(s.ID)
			id = strings.TrimPrefix(id, "<")
			id = strings.TrimSuffix(id, ">")
			if id == "" {
				continue
			}
			nf.Segments = append(nf.Segments, segment{ID: id, Number: s.Number, Bytes: int64(s.Bytes)})
			job.TotalBytes += int64(s.Bytes)
			job.TotalSegments++
		}
		if len(nf.Segments) == 0 {
			continue
		}
		if !nf.Posted.IsZero() && (job.Posted.IsZero() || nf.Posted.Before(job.Posted)) {
			job.Posted = nf.Posted
		}
		job.Files = append(job.Files, nf)
	}

	if job.TotalSegments == 0 {
		return nil, ErrEmptyNZB
	}
	if job.Title == "" {
		job.Title = longestContentName(job.Files)
	}
	if job.Password == "" {
		job.Password = passwordFromTitle(job.Title)
	}
	job.Title = passwordInNameRE.ReplaceAllString(job.Title, "")
	job.Title = strings.TrimSpace(job.Title)
	return job, nil
}

// fileName derives a file's name from the NZB. nzbparser fills Filename from
// the quoted string in the subject; the fallback re-parses the subject, and if
// that yields nothing the file is unnamed and the caller drops it.
func fileName(f *nzbparser.NzbFile) string {
	if n := strings.TrimSpace(f.Filename); n != "" {
		return path.Base(n)
	}
	if s, err := nzbparser.ParseSubject(f.Subject); err == nil && strings.TrimSpace(s.Filename) != "" {
		return path.Base(strings.TrimSpace(s.Filename))
	}
	return ""
}

func classify(name string) fileKind {
	lower := strings.ToLower(name)
	switch {
	case par2VolumeRE.MatchString(lower):
		return kindPar2Volume
	case strings.HasSuffix(lower, ".par2"):
		return kindPar2Index
	case strings.HasSuffix(lower, ".rar"), strings.HasSuffix(lower, ".7z"), strings.HasSuffix(lower, ".zip"):
		return kindArchive
	case rarVolumeRE.MatchString(lower):
		// name.r00 / name.r01 -- the old multi-volume scheme.
		return kindArchive
	default:
		return kindContent
	}
}

func longestContentName(files []nzbFile) string {
	best := ""
	var bestBytes int64
	for _, f := range files {
		if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
			continue
		}
		if f.Bytes > bestBytes {
			best, bestBytes = f.Name, f.Bytes
		}
	}
	return strings.TrimSuffix(best, path.Ext(best))
}

// passwordFromTitle implements SABnzbd's "Name{{password}}" convention. The
// other conventions it supports ("Name / password", "password=...") are
// ambiguous against real release titles, so they are deliberately not guessed
// at -- the never-guess rule applies here too.
func passwordFromTitle(title string) string {
	if m := passwordInNameRE.FindStringSubmatch(title); m != nil {
		return m[1]
	}
	return ""
}

// recoveryBlocks totals the recovery blocks the NZB offers. A set whose
// missing articles exceed this cannot be repaired however long it runs, which
// is what criticalHealthPercent expresses.
func (j *nzbJob) recoveryBlocks() int {
	total := 0
	for _, f := range j.Files {
		total += f.Blocks
	}
	return total
}

// criticalHealthPercent is the article-health floor below which par2 cannot
// recover the set, as a whole percentage.
//
// NZBGet computes health as 1000 - failed/total*1000 and derives critical
// health from the available recovery blocks. The same shape, on a 0-100 scale
// and in integer arithmetic, because no float reaches api/.
func (j *nzbJob) criticalHealthPercent() int32 {
	if j.TotalSegments == 0 {
		return 100
	}
	if blocks := j.recoveryBlocks(); blocks > 0 {
		// A recovery block replaces roughly one article-sized slice, so the
		// floor is the share of articles that must survive.
		recoverable := min(blocks, j.TotalSegments)
		survive := j.TotalSegments - recoverable
		return int32(survive * 100 / j.TotalSegments) //nolint:gosec // bounded by 100.
	}
	// No volume names its block count (the "name.vol-01.par2" scheme), so
	// estimate the capacity from the volumes' size the way NZBGet's
	// NzbInfo::CalcCriticalHealth does: recovery data replaces about one
	// byte per byte of itself, and the volumes can be damaged too, so their
	// size counts twice against the total. Before this fallback such a set
	// read as unrepairable and the first failed article of 14,169 paused a
	// 10 GB transfer at 1% (2026-09-24). Nothing to estimate from means one
	// missing article really is fatal.
	parBytes := j.par2VolumeBytes()
	if parBytes <= 0 || j.TotalBytes <= 0 {
		return 100
	}
	if 2*parBytes >= j.TotalBytes {
		return 0
	}
	pct := (j.TotalBytes - 2*parBytes) * 100 / (j.TotalBytes - parBytes)
	// NZBGet caps an estimated floor at 999 per mille: a set that carries
	// recovery data is never "one article is fatal".
	return int32(min(pct, 99)) //nolint:gosec // bounded by 99.
}

// par2VolumeBytes totals the size of the recovery volumes, the fallback
// measure of repair capacity when their names carry no block counts.
func (j *nzbJob) par2VolumeBytes() int64 {
	var total int64
	for _, f := range j.Files {
		if f.Kind == kindPar2Volume {
			total += f.Bytes
		}
	}
	return total
}
