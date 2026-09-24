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

// requiredCompletionPerMille is SABnzbd's req_completion_rate default,
// 100.2%: the share of the non-par2 bytes that must be present, counting
// par2 bytes as able to stand in for missing ones, with a 0.2% margin.
const requiredCompletionPerMille = 1002

// maxBadArticles is SABnzbd's MAX_BAD_ARTICLES: this many missing or bad
// articles are ignored before any hopelessness check runs, so a release
// with little or no par2 -- a plain RAR set unrar can verify -- is not
// abandoned for a handful of articles.
const maxBadArticles = 5

// par2Bytes totals every par2 file, index and volumes, the way SABnzbd's
// bytes_par2 does.
func (j *nzbJob) par2Bytes() int64 {
	var total int64
	for _, f := range j.Files {
		if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
			total += f.Bytes
		}
	}
	return total
}

// hopeless is SABnzbd's check_availability_ratio: with missingBytes of
// non-par2 data gone, the job cannot be completed when
//
//	100 * (bytes - missing) / (bytes - par2) < 100.2
//
// that is, when the missing data outweighs the recovery data by more than
// the margin. The caller applies maxBadArticles first. Integer arithmetic,
// so no float reaches api/.
func hopeless(totalBytes, par2Bytes, missingBytes int64) bool {
	nonPar := totalBytes - par2Bytes
	if nonPar <= 0 || missingBytes <= 0 {
		// Nothing missing is never hopeless -- the 0.2% margin would
		// otherwise read a par2-less set as short of 100.2%.
		return false
	}
	return (totalBytes-missingBytes)*1000 < requiredCompletionPerMille*nonPar
}

// criticalHealthPercent is the share of the non-par2 bytes that must be
// present for the job to be completable under hopeless -- 100.2% less the
// par2 share -- as a whole percentage for the status. A set without par2
// reads 100; the maxBadArticles tolerance is applied by the gate, not here.
func (j *nzbJob) criticalHealthPercent() int32 {
	nonPar := j.TotalBytes - j.par2Bytes()
	if nonPar <= 0 {
		return 100
	}
	pct := requiredCompletionPerMille/10 - j.par2Bytes()*100/nonPar
	switch {
	case pct < 0:
		return 0
	case pct > 100:
		return 100
	}
	return int32(pct) //nolint:gosec // bounded by 100.
}
