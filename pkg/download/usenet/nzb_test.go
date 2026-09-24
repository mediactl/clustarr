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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// nzbWith builds a minimal NZB body naming the given files, one segment each.
func nzbWith(t *testing.T, head string, names ...string) []byte {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	b.WriteString(head)
	for i, n := range names {
		fmt.Fprintf(&b, `<file poster="p@x" date="1788000000" subject="[1/1] - &#34;%s&#34; yEnc (1/1)">`, n)
		b.WriteString(`<groups><group>alt.binaries.clustarr</group></groups><segments>`)
		fmt.Fprintf(&b, `<segment bytes="1000" number="1">f%d@clustarr.test</segment>`, i)
		b.WriteString(`</segments></file>`)
	}
	b.WriteString(`</nzb>`)
	return []byte(b.String())
}

func TestParseNZBClassifiesFiles(t *testing.T) {
	body := nzbWith(t, "",
		"release.part01.rar",
		"release.part02.rar",
		"release.par2",
		"release.vol000+01.par2",
		"release.vol001+02.par2",
		"readme.nfo",
	)
	job, err := parseNZB(body, defaultMaxNZBBytes)
	require.NoError(t, err)

	kinds := map[string]fileKind{}
	blocks := map[string]int{}
	for _, f := range job.Files {
		kinds[f.Name] = f.Kind
		blocks[f.Name] = f.Blocks
	}
	require.Equal(t, kindArchive, kinds["release.part01.rar"])
	require.Equal(t, kindArchive, kinds["release.part02.rar"])
	require.Equal(t, kindPar2Index, kinds["release.par2"])
	require.Equal(t, kindPar2Volume, kinds["release.vol000+01.par2"])
	require.Equal(t, kindPar2Volume, kinds["release.vol001+02.par2"])
	require.Equal(t, kindContent, kinds["readme.nfo"])

	require.Equal(t, 1, blocks["release.vol000+01.par2"])
	require.Equal(t, 2, blocks["release.vol001+02.par2"])
	require.Equal(t, 3, job.recoveryBlocks())
	require.Equal(t, 6, job.TotalSegments)
	require.Equal(t, int64(6000), job.TotalBytes)
}

func TestParseNZBIsDeterministicOnThePayload(t *testing.T) {
	// The id is what makes Add idempotent, so it must depend on the payload
	// and nothing else.
	body := nzbWith(t, "", "movie.mkv")
	a, err := parseNZB(body, defaultMaxNZBBytes)
	require.NoError(t, err)
	b, err := parseNZB(body, defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, a.ID, b.ID)

	other, err := parseNZB(nzbWith(t, "", "other.mkv"), defaultMaxNZBBytes)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, other.ID)
}

func TestParseNZBReadsThePasswordFromMetaAndFromTheTitle(t *testing.T) {
	fromMeta, err := parseNZB(nzbWith(t,
		`<head><meta type="title">Movie</meta><meta type="password">hunter2</meta></head>`,
		"movie.mkv"), defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, "hunter2", fromMeta.Password)
	require.Equal(t, "Movie", fromMeta.Title)

	fromTitle, err := parseNZB(nzbWith(t,
		`<head><meta type="title">Movie{{letmein}}</meta></head>`, "movie.mkv"), defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, "letmein", fromTitle.Password)
	require.Equal(t, "Movie", fromTitle.Title, "the password marker must not stay in the published name")
}

func TestParseNZBRefusesAnOversizedOrEmptyPayload(t *testing.T) {
	body := nzbWith(t, "", "movie.mkv")
	_, err := parseNZB(body, 16)
	require.ErrorIs(t, err, ErrPayloadTooLarge)

	_, err = parseNZB([]byte(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb"></nzb>`), defaultMaxNZBBytes)
	require.ErrorIs(t, err, ErrEmptyNZB)
}

func TestCriticalHealthPercentFollowsTheRecoveryBlocks(t *testing.T) {
	// With no par2 in the set a single missing article is already fatal.
	none, err := parseNZB(nzbWith(t, "", "movie.mkv", "movie2.mkv"), defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, int32(100), none.criticalHealthPercent())

	// Four articles, one recovery block: 75% of the articles must survive.
	withPar2, err := parseNZB(nzbWith(t, "",
		"a.rar", "b.rar", "release.par2", "release.vol000+01.par2"), defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, 4, withPar2.TotalSegments)
	require.Equal(t, 1, withPar2.recoveryBlocks())
	require.Equal(t, int32(75), withPar2.criticalHealthPercent())
}

// TestCriticalHealthPercentEstimatesFromVolumeSizesWithoutBlockCounts pins
// the first real NZB grab on the owner's cluster (nzbgeek, 2026-09-24): 99
// rar parts and seven par2 volumes named vol-01..vol-07 -- no "+NN" block
// count anywhere -- so the set read as having no recovery capacity, the
// floor came out at 100%, and the first failed article of 14,169 paused a
// 10 GB transfer at 1%. The volumes' size says the set can lose about 2%.
func TestCriticalHealthPercentEstimatesFromVolumeSizesWithoutBlockCounts(t *testing.T) {
	sizes := []struct {
		name  string
		bytes int64
	}{
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-01.par2", 65340},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-02.par2", 10197048},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-03.par2", 5098576},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-04.par2", 20328756},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-05.par2", 40526936},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-06.par2", 80858060},
		{"13th.2016.1080p.WEBRip.X264-DEFLATE.vol-07.par2", 40526936},
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	file := func(name string, bytes int64) {
		fmt.Fprintf(&b, `<file poster="p@x" date="1788000000" subject="[1/1] - &#34;%s&#34; yEnc %d (1/1)">`, name, bytes)
		b.WriteString(`<groups><group>alt.binaries.clustarr</group></groups><segments>`)
		fmt.Fprintf(&b, `<segment bytes="%d" number="1">%s@clustarr.test</segment>`, bytes, name)
		b.WriteString(`</segments></file>`)
	}
	for i := 1; i <= 10; i++ {
		file(fmt.Sprintf("13th.2016.1080p.WEBRip.X264-DEFLATE.part%02d.rar", i), 1_000_000_000)
	}
	for _, s := range sizes {
		file(s.name, s.bytes)
	}
	b.WriteString(`</nzb>`)

	job, err := parseNZB([]byte(b.String()), defaultMaxNZBBytes)
	require.NoError(t, err)

	volumes := 0
	for _, f := range job.Files {
		if strings.HasSuffix(f.Name, ".par2") {
			require.Equal(t, kindPar2Volume, f.Kind, f.Name)
			require.Zero(t, f.Blocks, "%s names no block count", f.Name)
			volumes++
		}
	}
	require.Equal(t, 7, volumes)
	require.Zero(t, job.recoveryBlocks())
	require.Equal(t, int64(197_601_652), job.par2VolumeBytes())

	// NZBGet: (size - 2*par) / (size - par) = 9,802,398,348 / 10,000,000,000.
	require.Equal(t, int32(98), job.criticalHealthPercent())

	// The set has no separate index, so repair takes its smallest volume.
	require.Equal(t, "13th.2016.1080p.WEBRip.X264-DEFLATE.vol-01.par2", par2IndexFile(job.Files))

	// An estimated floor never reads "one article is fatal", and a set whose
	// recovery data outweighs its content can lose anything.
	generous, err := parseNZB(nzbWith(t, "", "a.rar", "a.vol-01.par2", "a.vol-02.par2", "a.vol-03.par2"),
		defaultMaxNZBBytes)
	require.NoError(t, err)
	require.Equal(t, int32(0), generous.criticalHealthPercent())
	tiny := &nzbJob{
		TotalSegments: 100, TotalBytes: 1_000_000,
		Files: []nzbFile{{Name: "a.vol-01.par2", Kind: kindPar2Volume, Bytes: 1}},
	}
	require.Equal(t, int32(99), tiny.criticalHealthPercent())
}

func TestPar2IndexFilePrefersTheSetWithTheMostRecoveryBlocks(t *testing.T) {
	files := []nzbFile{
		{Name: "sample.par2", Kind: kindPar2Index},
		{Name: "sample.vol000+01.par2", Kind: kindPar2Volume, Blocks: 1},
		{Name: "release.par2", Kind: kindPar2Index},
		{Name: "release.vol000+20.par2", Kind: kindPar2Volume, Blocks: 20},
	}
	require.Equal(t, "release.par2", par2IndexFile(files))
	require.Empty(t, par2IndexFile([]nzbFile{{Name: "movie.mkv", Kind: kindContent}}))
}

func TestSafeNameKeepsEverythingInsideTheJobDirectory(t *testing.T) {
	// NZB subjects are attacker-controlled input from an indexer.
	require.Equal(t, "passwd", safeName("../../etc/passwd"))
	require.Equal(t, "movie.mkv", safeName("/abs/movie.mkv"))
	require.Equal(t, "evil.sh", safeName(`..\..\evil.sh`))
	require.Equal(t, "unnamed", safeName(""))
	require.Equal(t, "unnamed", safeName("/"))
	require.Equal(t, "movie.mkv", safeName("movie.mkv"))
}

func TestClassifyMatchesTheOldRarNamingScheme(t *testing.T) {
	require.Equal(t, kindArchive, classify("release.rar"))
	require.Equal(t, kindArchive, classify("release.r00"))
	require.Equal(t, kindArchive, classify("release.r15"))
	require.Equal(t, kindArchive, classify("release.7z"))
	require.Equal(t, kindArchive, classify("release.zip"))
	require.Equal(t, kindContent, classify("release.mkv"))
}

func TestArchiveEntryPointsReturnsOneEntryPerSet(t *testing.T) {
	// rardecode follows the volume chain from the first volume, so opening a
	// later one would extract the tail of the set a second time.
	files := []nzbFile{
		{Name: "release.part01.rar", Kind: kindArchive},
		{Name: "release.part02.rar", Kind: kindArchive},
		{Name: "release.part03.rar", Kind: kindArchive},
		{Name: "other.rar", Kind: kindArchive},
		{Name: "other.r00", Kind: kindArchive},
		{Name: "extras.zip", Kind: kindArchive},
		{Name: "movie.mkv", Kind: kindContent},
	}
	require.Equal(t, []string{"extras.zip", "other.rar", "release.part01.rar"}, archiveEntryPoints(files))
}
