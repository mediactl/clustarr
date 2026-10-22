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

package projection

import (
	"context"
	"path"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// DashboardRows caps each list the dashboard shows; the totals count all.
const DashboardRows = 8

// Dashboard is the dashboard's view of one projection round: the
// MediaFiles' transcodes and the grab entries' downloads, counted by state,
// with the first DashboardRows of each list.
type Dashboard struct {
	Transcodes TranscodeSummary
	Downloads  DownloadSummary
}

// TranscodeSummary is every MediaFile's status.transcode by state.
type TranscodeSummary struct {
	Running   TranscodeList // Running, Swapping; the longest-running first
	Pending   TranscodeList // Pending, Planned, Queued; the longest-waiting first
	Failed    TranscodeList // the newest first
	Skipped   TranscodeList // the newest first
	Succeeded int
}

// TranscodeList is one state's rows and how many files are in it.
type TranscodeList struct {
	Rows  []TranscodeRow
	Total int
}

// TranscodeRow is one MediaFile's transcode.
type TranscodeRow struct {
	File    types.NamespacedName
	Name    string // the file's base name
	ItemURL string // the library page of the item it backs
	Phase   catalogv1.TranscodePhase
	Profile string
	Class   catalogv1.TranscodeClass
	Plan    string // TranscodePlan.Describe, or empty
	Reason  string
	Message string
	Since   time.Time
	Percent int32 // a running transcode's progress, else -1

	uid types.UID // for the progress read
}

// DownloadSummary is every grab entry by phase.
type DownloadSummary struct {
	Active   DownloadList // Downloading; the oldest grab first
	Pending  DownloadList // Pending, Assigned, Queued, Paused; the oldest first
	Failed   DownloadList // Failed, Blocklisted; the newest first
	Imported int
}

// DownloadList is one state's rows and how many entries are in it.
type DownloadList struct {
	Rows  []DownloadRow
	Total int
}

// buildDashboard counts files and rows into a Dashboard. progress, when
// set, gives each listed running transcode its percent.
func buildDashboard(ctx context.Context, files []*catalogv1.MediaFile, rows []DownloadRow, progress TranscodeProgressFunc) Dashboard {
	var running, pending, failed, skipped []TranscodeRow
	succeeded := 0
	for _, mf := range files {
		ts := mf.Status.Transcode
		if ts == nil {
			continue
		}
		switch ts.Phase {
		case catalogv1.TranscodePhaseRunning, catalogv1.TranscodePhaseSwapping:
			running = append(running, transcodeRow(mf, ts.StartedAt, ts.PlannedAt))
		case catalogv1.TranscodePhasePending, catalogv1.TranscodePhasePlanned, catalogv1.TranscodePhaseQueued:
			pending = append(pending, transcodeRow(mf, ts.PlannedAt, nil))
		case catalogv1.TranscodePhaseFailed:
			failed = append(failed, transcodeRow(mf, ts.FinishedAt, ts.PlannedAt))
		case catalogv1.TranscodePhaseSkipped:
			skipped = append(skipped, transcodeRow(mf, ts.FinishedAt, ts.PlannedAt))
		case catalogv1.TranscodePhaseSucceeded:
			succeeded++
		}
	}
	d := Dashboard{Transcodes: TranscodeSummary{
		Running:   transcodeList(running, oldestFirst),
		Pending:   transcodeList(pending, oldestFirst),
		Failed:    transcodeList(failed, newestFirst),
		Skipped:   transcodeList(skipped, newestFirst),
		Succeeded: succeeded,
	}}
	if progress != nil {
		for i := range d.Transcodes.Running.Rows {
			r := &d.Transcodes.Running.Rows[i]
			if pct, ok, err := progress(ctx, r.uid); err == nil && ok {
				r.Percent = pct
			}
		}
	}

	var active, waiting, broken []DownloadRow
	for _, row := range rows {
		switch row.Entry.Phase {
		case commonv1.DownloadPhaseDownloading:
			active = append(active, row)
		case commonv1.DownloadPhasePending, commonv1.DownloadPhaseAssigned, commonv1.DownloadPhaseQueued, commonv1.DownloadPhasePaused:
			waiting = append(waiting, row)
		case commonv1.DownloadPhaseFailed, commonv1.DownloadPhaseBlocklisted:
			broken = append(broken, row)
		case commonv1.DownloadPhaseImported:
			d.Downloads.Imported++
		}
	}
	d.Downloads.Active = downloadList(active, oldestFirst)
	d.Downloads.Pending = downloadList(waiting, oldestFirst)
	d.Downloads.Failed = downloadList(broken, newestFirst)
	return d
}

const (
	oldestFirst = false
	newestFirst = true
)

// transcodeRow is mf's row, dated by the first of at that is set.
func transcodeRow(mf *catalogv1.MediaFile, at ...*metav1.Time) TranscodeRow {
	ts := mf.Status.Transcode
	r := TranscodeRow{
		File:    types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name},
		Name:    path.Base(mf.Spec.Path),
		ItemURL: "/library/" + mf.Namespace + "/" + string(mf.Spec.MediaRef.Kind) + "/" + mf.Spec.MediaRef.Name,
		Phase:   ts.Phase,
		Profile: ts.Profile,
		Class:   ts.Class,
		Reason:  ts.Reason,
		Message: ts.Message,
		Percent: -1,
		uid:     mf.UID,
	}
	if ts.Plan != nil {
		r.Plan = ts.Plan.Describe()
	}
	for _, t := range at {
		if t != nil && !t.IsZero() {
			r.Since = t.UTC()
			break
		}
	}
	return r
}

// transcodeList sorts rows by Since and keeps the first DashboardRows.
func transcodeList(rows []TranscodeRow, newest bool) TranscodeList {
	sort.SliceStable(rows, func(i, j int) bool {
		if newest {
			return rows[i].Since.After(rows[j].Since)
		}
		return rows[i].Since.Before(rows[j].Since)
	})
	return TranscodeList{Rows: rows[:min(len(rows), DashboardRows)], Total: len(rows)}
}

// downloadList sorts rows by when each was grabbed and keeps the first
// DashboardRows.
func downloadList(rows []DownloadRow, newest bool) DownloadList {
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i].Entry.GrabbedAt.Time, rows[j].Entry.GrabbedAt.Time
		if newest {
			return a.After(b)
		}
		return a.Before(b)
	})
	return DownloadList{Rows: rows[:min(len(rows), DashboardRows)], Total: len(rows)}
}
