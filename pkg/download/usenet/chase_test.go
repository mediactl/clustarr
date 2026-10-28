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
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

func fourParts() [][]byte {
	return [][]byte{partPayload(1, 400), partPayload(2, 400), partPayload(3, 400), partPayload(4, 400)}
}

// An article the granted server lacks is fetched from the next server in
// order, under a grant of that server's own: the job parks for it rather
// than fail, and takes nothing else from there (spec 2026-10-16 §4.2).
func TestAMissingArticleIsChasedOnTheNextServerUnderAGrant(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Chased", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	second.mirror(first)
	first.refuse["f0-p2@clustarr.test"] = 430

	fp, sp := first.provider("first", 2, 1), second.provider("second", 2, 2)
	ledger := newGateLedger(sp.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{fp, sp}, Ledger: ledger, HealthAction: downloadv1alpha1.HealthActionDelete})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Chased", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	require.Zero(t, second.askedCount("f0-p2@clustarr.test"), "the second server was asked before it granted connections")

	ledger.open(sp.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 1, second.servedCount("f0-p2@clustarr.test"))
	require.Equal(t, 1, second.servedTotal(), "the chase fetches only the missing articles")
	held, _ := ledger.Held(ctx, sp.Account())
	require.Zero(t, held, "a chase grant is given back")
}

// The job fails as missing only once every configured server has been
// asked for the articles still missing.
func TestAJobFailsAsMissingOnlyAfterEveryServerWasTried(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Gone", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	second.mirror(first)
	for _, srv := range []*stubServer{first, second} {
		srv.refuse["f0-p1@clustarr.test"] = 430
		srv.refuse["f0-p2@clustarr.test"] = 430
	}
	c, _, _ := newTestClient(t, Config{
		Providers:    []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2)},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Gone", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureMissingArticles, it.FailureReason)
	require.GreaterOrEqual(t, first.askedCount("f0-p1@clustarr.test"), 1)
	require.GreaterOrEqual(t, second.askedCount("f0-p1@clustarr.test"), 1, "the verdict came before the second server was tried")
}

// With one server there is nothing to chase: the job fails as missing at
// once, taking no second grant and never parking.
func TestASingleServerJobFailsAsMissingWithoutParking(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Solo.Gone", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	srv.refuse["f0-p1@clustarr.test"] = 430
	srv.refuse["f0-p2@clustarr.test"] = 430
	ledger := newGateLedger()
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{srv.provider("solo", 2, 1)}, Ledger: ledger,
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	start := time.Now()
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Solo.Gone", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureMissingArticles, it.FailureReason)
	require.Less(t, time.Since(start), ledgerPollInterval, "the job parked for a server it had already tried")
	// The transfer's grant, then the end-of-transfer retry's own on the same
	// server, taken after the transfer gave its slot and grant back; the
	// chase takes none, there being no other server.
	require.Equal(t, int32(2), ledger.takes.Load(), "only the transfer's grant and the retry's are taken")
}

// A backup server fills exactly the missing articles, under a grant.
func TestABackupFillsOnlyTheMissingArticles(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Filled", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	backup.mirror(primary)
	primary.refuse["f0-p2@clustarr.test"] = 430
	bp := backup.provider("backup", 2, 2)
	bp.Backup = true
	ledger := NewLocalLedger()
	c, _, _ := newTestClient(t, Config{Providers: []Provider{primary.provider("primary", 2, 1), bp}, Ledger: ledger})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Filled", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 1, backup.servedTotal())
	require.Equal(t, 3, primary.servedTotal())
}

// The pre-check judges "missing everywhere": a shortfall on the granted
// server is STATed on the next server under a one-connection grant, parking
// for it, and a release that server holds is not refused.
func TestThePreCheckChasesTheShortfallBeforeJudging(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	parts := manyParts(1, 12, 300)
	nzb := buildNZB(t, first, "Checked", []fileSpec{{name: "movie.mkv", parts: parts}})
	second.mirror(first)
	for i := range 7 {
		first.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	fp, sp := first.provider("first", 2, 1), second.provider("second", 2, 2)
	ledger := newGateLedger(sp.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{fp, sp}, Ledger: ledger, PreCheck: true, HealthAction: downloadv1alpha1.HealthActionDelete})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Checked", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	require.Zero(t, first.servedTotal(), "the pre-check must park before any article moves")

	ledger.open(sp.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	for i := range 7 {
		require.Equal(t, 1, second.servedCount(fmt.Sprintf("f0-p%d@clustarr.test", i)), "the second server serves what the first refuses")
	}
	// The chase recovers more than the engine tolerates missing, so the
	// transfer moves to the second server (moveGrant); each article is
	// fetched once, from whichever server held the grant.
	require.Equal(t, 12, first.servedTotal()+second.servedTotal())
}

// Articles the primary refuses while a chase is running are chased before
// they are judged. Cyberpunk: Edgerunners (talos, 2026-10-22): frugal lacked
// about 2% of every post's articles, easynews had them all, and each job
// still failed as missing -- the verdict after a chase counted the articles
// the primary's workers had failed meanwhile, which no second server had
// been asked for. A primary refusing every third of 120 articles and a
// backup slow enough for new failures to pile up during each chase must
// complete, and the backup serves exactly the refused ones.
func TestFailuresDuringAChaseAreChasedBeforeTheyAreJudged(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Piling", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 600, 300)}})
	backup.mirror(primary)
	for i := 0; i < 600; i++ {
		if i%3 != 0 {
			primary.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
		}
	}
	// Batches of twenty on a primary that takes a few milliseconds per
	// article: a worker mid-batch records its failures while the chase
	// runs on the slow backup.
	primary.bodyDelay = 3 * time.Millisecond
	backup.bodyDelay = 20 * time.Millisecond
	bp := backup.provider("backup", 2, 2)
	bp.Backup = true
	c, _, _ := newTestClient(t, Config{
		Providers:     []Provider{primary.provider("primary", 16, 1), bp},
		PipelineDepth: 20, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Piling", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 400, backup.servedTotal(), "the backup serves each refused article once")
}

// An article every server refused is what the verdict counts: a set with
// no par2 whose articles are gone everywhere still fails as missing.
func TestArticlesMissingOnEveryServerStillFailTheJob(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Gone.Everywhere", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 40, 300)}})
	second.mirror(first)
	for i := 0; i < 40; i += 4 {
		for _, srv := range []*stubServer{first, second} {
			srv.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
		}
	}
	c, _, _ := newTestClient(t, Config{
		Providers:     []Provider{first.provider("first", 4, 1), second.provider("second", 2, 2)},
		PipelineDepth: 1, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Gone.Everywhere", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureMissingArticles, it.FailureReason)
	require.Contains(t, it.Message, "on every server")
}

// A chase never stops the transfer: while one worker waits on the backup,
// the others fetch on the primary. A Sign of Affection (talos, 2026-10-26):
// frugal lacked about a quarter of each episode, every worker queued behind
// each chase, and the two transfer slots took turns moving bytes. Here the
// chase parks on a backup with no room while the primary serves the rest.
func TestAChaseNeverStopsTheTransfer(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Beside", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 200, 300)}})
	backup.mirror(primary)
	for i := range 10 {
		primary.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	pp, bp := primary.provider("primary", 4, 1), backup.provider("backup", 2, 2)
	bp.Backup = true
	ledger := newGateLedger(bp.Account())
	old := ledgerPollInterval
	ledgerPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { ledgerPollInterval = old })
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{pp, bp}, Ledger: ledger,
		PipelineDepth: 2, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Beside", Payload: nzb})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return primary.servedTotal() == 190 }, 5*time.Second, 5*time.Millisecond,
		"the primary stopped while the chase waited for the backup")
	require.Zero(t, backup.servedTotal(), "the backup was asked before it granted connections")

	ledger.open(bp.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 10, backup.servedTotal())
}

// A chase asks each server only for the articles it began with: one the
// primary fails during the walk waits for the next chase, which starts at
// the first backup again. On talos (2026-10-26) easynews held every article
// frugal lacked, and three quarters of each job's time went to asking
// frugal-bonus, which held none of them, for the ones frugal had failed
// while easynews was being asked.
func TestALaterServerIsAskedOnlyForWhatTheEarlierOnesLacked(t *testing.T) {
	primary := newStubServer(t)
	full := newStubServer(t)
	empty := newStubServer(t)
	nzb := buildNZB(t, primary, "Walked", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 600, 300)}})
	full.mirror(primary)
	for i := 0; i < 600; i++ {
		if i%3 != 0 {
			primary.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
		}
	}
	// As in TestFailuresDuringAChaseAreChasedBeforeTheyAreJudged: the
	// primary's workers record failures while a chase runs on the slow
	// backup ahead of the empty one.
	primary.bodyDelay = 3 * time.Millisecond
	full.bodyDelay = 20 * time.Millisecond
	fp, ep := full.provider("full", 2, 2), empty.provider("empty", 2, 3)
	fp.Backup, ep.Backup = true, true
	c, _, _ := newTestClient(t, Config{
		Providers:     []Provider{primary.provider("primary", 16, 1), fp, ep},
		PipelineDepth: 20, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Walked", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 400, full.servedTotal(), "the full backup serves each refused article once")
	require.Zero(t, empty.askedTotal(), "a later server was asked for articles an earlier one holds")
}
