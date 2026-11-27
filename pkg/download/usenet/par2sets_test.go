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
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/test/par2test"
)

// recordingRepairer is a Repairer that notes what par2 was run on.
type recordingRepairer struct {
	Repairer
	mu    sync.Mutex
	whole []string
	sets  map[string][]string
}

func (r *recordingRepairer) Repair(ctx context.Context, dir, index string) (Par2Result, error) {
	r.mu.Lock()
	r.whole = append(r.whole, index)
	r.mu.Unlock()
	return r.Repairer.Repair(ctx, dir, index)
}

func (r *recordingRepairer) RepairSet(ctx context.Context, dir, index string, candidates []string) (Par2Result, error) {
	r.mu.Lock()
	if r.sets == nil {
		r.sets = map[string][]string{}
	}
	r.sets[index] = append([]string(nil), candidates...)
	r.mu.Unlock()
	return r.Repairer.RepairSet(ctx, dir, index, candidates)
}

// seasonOfSets builds a post with a par2 set per episode, as the 98 GB
// season on talos was posted (2026-11-27): each episode opens with its own
// 16 KiB, then the same run of zeros -- three whole blocks of them in every
// set -- and each has its own index and volumes. The third episode's set
// has the most recovery blocks. It returns the episodes' names in their
// sets, their bytes, and every par2 file's name.
func seasonOfSets(t *testing.T, dir string) (episodes []string, data map[string][]byte, par2 []string) {
	t.Helper()
	data = map[string][]byte{}
	for n, redundancy := range []uint32{10, 20, 50} {
		name := fmt.Sprintf("[grp] Some Show - S01E%02d [BD][%08X].mkv", n+1, 0xA0+n)
		b := make([]byte, 256<<10)
		_, _ = rand.New(rand.NewSource(int64(31 + n))).Read(b)
		clear(b[16<<10 : 80<<10]) // a reserved gap: the same zeros in each
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), b, 0o644))
		par2test.CreateSet(t, dir, name+".par2", []string{name}, 16, redundancy, 0)
		episodes, data[name] = append(episodes, name), b
	}
	found, err := filepath.Glob(filepath.Join(dir, "*.par2"))
	require.NoError(t, err)
	for _, p := range found {
		par2 = append(par2, filepath.Base(p))
	}
	sort.Strings(par2)
	require.Greater(t, len(par2), 3, "an index and at least one volume a set")
	return episodes, data, par2
}

// A job that carries a par2 set per file gives each file its own set's
// name, and repairs the set of the file that lost an article, on that
// set's files alone. Read as one set, the job named one file, pointed par2
// at the set with the most recovery blocks -- another episode's -- with
// every other file as a candidate for it, and published the damaged
// episode as it was once par2 had searched 90 GB for nothing (owner,
// 2026-11-27: "investigate it", of a repair of one missing article that
// held eight cores for 77 minutes).
func TestAJobOfSeveralPar2SetsRepairsTheSetOfTheDamagedFile(t *testing.T) {
	rec := &recordingRepairer{Repairer: libraryRepairer(t)}
	c, _, _ := newTestClient(t, Config{Providers: []Provider{{Name: "p", Host: "127.0.0.1", Port: 1, Connections: 1}}, Repairer: rec})

	work := t.TempDir()
	episodes, data, par2 := seasonOfSets(t, work)

	// On the wire the episodes carry other names; the par2 files their own.
	wire := []string{"Some.Show.S01.[BD] [BD][000000A0].mkv", "Some.Show.S01.[BD] [BD][000000A1].mkv", "Some.Show.S01.[BD] [BD][000000A2].mkv"}
	nzb := nzbWith(t, "", append(append([]string(nil), wire...), par2...)...)
	parsed, err := parseNZB(nzb, defaultMaxNZBBytes)
	require.NoError(t, err)
	j := c.newJob("job1", "download-1", "tv", filepath.Join(c.cfg.ScratchDir, "tv", "job1"), parsed)
	require.NoError(t, os.MkdirAll(j.contentDir(), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(j.dir, nzbName), nzb, 0o644))
	for i, name := range episodes {
		require.NoError(t, os.Rename(filepath.Join(work, name), filepath.Join(j.contentDir(), wire[i])))
	}
	for _, name := range par2 {
		require.NoError(t, os.Rename(filepath.Join(work, name), filepath.Join(j.contentDir(), name)))
	}
	// The first episode lost an article: a hole of zeros, and its part failed.
	holed := append([]byte(nil), data[episodes[0]]...)
	clear(holed[150<<10 : 154<<10])
	require.NoError(t, os.WriteFile(filepath.Join(j.contentDir(), wire[0]), holed, 0o644))
	j.failedSegs[0].set(0)

	j.renameObfuscated(context.Background())
	for _, name := range episodes {
		_, err := os.Stat(filepath.Join(j.contentDir(), name))
		require.NoError(t, err, "every episode carries its own set's name: %s", name)
	}
	require.False(t, j.damagedHead, "each was known by its first 16 KiB")
	require.Equal(t, episodes, j.par2Names)

	require.NoError(t, j.repair(context.Background()))

	got, err := os.ReadFile(filepath.Join(j.contentDir(), episodes[0]))
	require.NoError(t, err)
	require.True(t, bytes.Equal(data[episodes[0]], got), "the episode that lost an article is repaired")
	require.Empty(t, rec.whole, "par2 is not handed the whole directory for one set")
	require.Len(t, rec.sets, 1, "the sets whose files are whole are left alone: %v", rec.sets)
	candidates, ok := rec.sets[episodes[0]+".par2"]
	require.True(t, ok, "par2 ran on the damaged episode's set: %v", rec.sets)
	for _, cand := range candidates {
		require.True(t, strings.HasPrefix(cand, episodes[0]), "a candidate of another set: %s", cand)
	}
	require.NotEmpty(t, candidates, "the set's own volumes")
}

// Which of several sets par2 is run on.
func TestPlanPar2Runs(t *testing.T) {
	runs := []par2Run{
		{index: "e1.mkv.par2", volumes: []string{"e1.mkv.vol0+1.par2"}, targets: []string{"e1.mkv"}, bytes: 3},
		{index: "e2.mkv.par2", volumes: []string{"e2.mkv.vol0+1.par2"}, targets: []string{"e2.mkv"}, bytes: 2},
		{index: "sums.par2", targets: []string{"sums.md5"}, bytes: 1},
	}
	all := map[string]bool{
		"e1.mkv": true, "e2.mkv": true, "sums.md5": true, "show.nfo": true,
		"e1.mkv.par2": true, "e1.mkv.vol0+1.par2": true, "e2.mkv.par2": true, "e2.mkv.vol0+1.par2": true, "sums.par2": true,
	}
	without := func(names ...string) map[string]bool {
		out := map[string]bool{}
		for k := range all {
			out[k] = true
		}
		for _, n := range names {
			delete(out, n)
		}
		return out
	}
	indexes := func(p par2RunPlan) []string {
		var out []string
		for _, r := range p.runs {
			out = append(out, r.index)
		}
		return out
	}
	for _, tc := range []struct {
		name        string
		runs        []par2Run
		failed      map[string]bool
		onDisk      map[string]bool
		damagedHead bool
		want        []string
		skipped     int
	}{
		{name: "the set of the file that lost an article", runs: runs, failed: map[string]bool{"e2.mkv": true}, onDisk: all, want: []string{"e2.mkv.par2"}, skipped: 2},
		{name: "a par2 volume alone lost one", runs: runs, failed: map[string]bool{"e1.mkv.vol0+1.par2": true}, onDisk: all, skipped: 3},
		{name: "a file no set names lost one, every set's files on disk", runs: runs, failed: map[string]bool{"show.nfo": true}, onDisk: all, skipped: 3},
		{
			// The file may be e2 under a name the sets did not give it, or
			// never written: the set missing a file is run.
			name: "a file no set names lost one, and a set misses a file", runs: runs,
			failed: map[string]bool{"0a1b2c": true}, onDisk: func() map[string]bool { m := without("e2.mkv"); m["0a1b2c"] = true; return m }(),
			want: []string{"e2.mkv.par2"}, skipped: 2,
		},
		{name: "a set misses a file and nothing lost an article elsewhere", runs: runs, failed: map[string]bool{"e1.mkv": true}, onDisk: without("sums.md5"), want: []string{"e1.mkv.par2"}, skipped: 2},
		{name: "a file's start is damaged: which is not known", runs: runs, onDisk: all, damagedHead: true, want: []string{"e1.mkv.par2", "e2.mkv.par2", "sums.par2"}},
		{
			name: "a set that was never read whole is run",
			runs: []par2Run{runs[0], {index: "e2.mkv.par2"}}, failed: map[string]bool{"e1.mkv.vol0+1.par2": true}, onDisk: all,
			want: []string{"e2.mkv.par2"}, skipped: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := planPar2Runs(tc.runs, tc.failed, tc.onDisk, tc.damagedHead)
			require.Equal(t, tc.want, indexes(plan))
			require.Equal(t, tc.skipped, plan.skipped)
			require.NotContains(t, plan.unclaimed, "e1.mkv", "a set's own file is no candidate for another")
			require.NotContains(t, plan.unclaimed, "e1.mkv.vol0+1.par2")
		})
	}
	plan := planPar2Runs(runs, map[string]bool{"e1.mkv": true}, all, false)
	require.Equal(t, []string{"show.nfo"}, plan.unclaimed, "the files no set names are par2's to place")
}

// The set of a par2 file is the one its packets name, found past a hole
// where the file's first article was.
func TestPar2FileSetID(t *testing.T) {
	work := t.TempDir()
	_, _, par2 := seasonOfSets(t, work)
	ids := map[[16]byte][]string{}
	for _, name := range par2 {
		id, ok := par2FileSetID(filepath.Join(work, name))
		require.True(t, ok, name)
		ids[id] = append(ids[id], name)
	}
	require.Len(t, ids, 3, "three sets")
	for _, names := range ids {
		base := par2Base(names[0])
		for _, n := range names {
			require.Equal(t, base, par2Base(n), "a set's files share its name here")
		}
	}

	index := filepath.Join(work, par2[0])
	want, _ := par2FileSetID(index)
	b, err := os.ReadFile(index)
	require.NoError(t, err)
	clear(b[:40])
	require.NoError(t, os.WriteFile(index, b, 0o644))
	got, ok := par2FileSetID(index)
	require.True(t, ok)
	require.Equal(t, want, got)

	require.NoError(t, os.WriteFile(index, make([]byte, 4096), 0o644))
	_, ok = par2FileSetID(index)
	require.False(t, ok, "a file of zeros names no set")
}
