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
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// SubtitleSummary is every MediaFile's status.subtitles by state, the
// Activity page's Subtitles tabs (owner, 2026-11-10).
type SubtitleSummary struct {
	Searching FileList // a fetch outstanding; the longest-waiting first
	Wanted    FileList // a language missing, its next search ahead; the soonest first
	Failed    FileList // Blocked, or a language whose fetch failed; the newest first
	Satisfied FileList // every wanted language present; the newest download first
}

// MarkerSummary is every movie's and episode's file by its skip segments,
// the Activity page's Markers tabs (owner, 2026-11-10).
type MarkerSummary struct {
	Pending  FileList // probed, nothing has looked yet; the oldest file first
	Found    FileList // segments found; the newest first
	NotFound FileList // TheIntroDB and the analysis found none; the newest first
	Error    FileList // TheIntroDB could not be asked, or the analysis failed; the newest first
}

// FileList is one state's rows and how many files are in it.
type FileList struct {
	Rows  []FileRow
	Total int
}

// FileRow is one MediaFile's subtitles or markers.
type FileRow struct {
	File    types.NamespacedName
	Name    string // the file's base name
	ItemURL string // the library page of the item it backs
	// State is the row's badge: the subtitles' phase, or the markers'
	// result.
	State string
	// Detail is the row's one line: the languages, or the segments.
	Detail string
	// Message is a failure's text, "" for none.
	Message string
	// Since dates the row: when its state began, or (Wanted) when its next
	// search is due.
	Since time.Time
}

// buildSubtitles sorts every file with a subtitles block into its tab.
func buildSubtitles(files []*catalogv1.MediaFile) SubtitleSummary {
	var searching, wanted, failed, satisfied []FileRow
	for _, mf := range files {
		st := mf.Status.Subtitles
		if st == nil {
			continue
		}
		row := fileRow(mf)
		row.State = string(st.Phase)
		row.Detail = subtitleDetail(st)
		if st.Reason != "" || st.Message != "" {
			row.Message = strings.TrimSpace(st.Reason + ": " + st.Message)
			row.Message = strings.TrimPrefix(row.Message, ": ")
		}
		failedItem := false
		for _, it := range st.Items {
			if it.State == catalogv1.SubtitleStateFailed {
				failedItem = true
				if row.Message == "" && it.LastError != "" {
					row.Message = it.LangKey + ": " + it.LastError
				}
			}
		}
		switch {
		case st.Phase == catalogv1.SubtitlesPhaseSearching:
			row.Since = earliest(st.Items, func(it catalogv1.SubtitleItemStatus) *metav1.Time {
				if it.Dispatch == nil || it.State != catalogv1.SubtitleStateSearching {
					return nil
				}
				return &it.Dispatch.DispatchedAt
			})
			searching = append(searching, row)
		case st.Phase == catalogv1.SubtitlesPhaseBlocked || (failedItem && st.Phase == catalogv1.SubtitlesPhaseWanted):
			row.Since = latest(st.Items, func(it catalogv1.SubtitleItemStatus) *metav1.Time { return it.Attempts.Latest })
			if row.Since.IsZero() {
				row.Since = mf.CreationTimestamp.UTC()
			}
			if st.Phase == catalogv1.SubtitlesPhaseWanted {
				row.State = "Failed"
			}
			failed = append(failed, row)
		case st.Phase == catalogv1.SubtitlesPhaseWanted:
			row.Since = earliest(st.Items, func(it catalogv1.SubtitleItemStatus) *metav1.Time { return it.NextSearchAt })
			wanted = append(wanted, row)
		case st.Phase == catalogv1.SubtitlesPhaseSatisfied:
			row.Since = latest(st.Items, func(it catalogv1.SubtitleItemStatus) *metav1.Time { return it.DownloadedAt })
			satisfied = append(satisfied, row)
		}
	}
	return SubtitleSummary{
		Searching: fileList(searching, oldestFirst, DashboardRows),
		Wanted:    fileList(wanted, oldestFirst, DashboardRows),
		Failed:    fileList(failed, newestFirst, DashboardRows),
		Satisfied: fileList(satisfied, newestFirst, DashboardRows),
	}
}

// subtitleDetail is a subtitles block's line: the languages missing, else
// the ones present with their providers.
func subtitleDetail(st *catalogv1.SubtitlesStatus) string {
	var missing, present []string
	for _, it := range st.Items {
		switch it.State {
		case catalogv1.SubtitleStateDownloaded, catalogv1.SubtitleStateUpgradable:
			p := it.LangKey
			if it.Provider != "" {
				p += " (" + it.Provider + ")"
			}
			present = append(present, p)
		default:
			missing = append(missing, it.LangKey+" "+string(it.State))
		}
	}
	parts := make([]string, 0, 3)
	if st.Profile != "" {
		parts = append(parts, st.Profile)
	}
	if len(missing) > 0 {
		parts = append(parts, "missing "+strings.Join(missing, ", "))
	}
	if len(present) > 0 {
		parts = append(parts, strings.Join(present, ", "))
	}
	return strings.Join(parts, " · ")
}

// buildMarkers sorts every probed movie or episode file into its markers
// tab.
func buildMarkers(files []*catalogv1.MediaFile) MarkerSummary {
	var pending, found, notFound, failed []FileRow
	for _, mf := range files {
		if k := mf.Spec.MediaRef.Kind; k != commonv1.MediaKindMovie && k != commonv1.MediaKindEpisode {
			continue
		}
		if mf.Status.ProbeHash == "" {
			continue
		}
		row := fileRow(mf)
		m := mf.Status.Markers
		if m == nil {
			row.State, row.Detail = "Pending", "not looked at yet"
			row.Since = mf.CreationTimestamp.UTC()
			pending = append(pending, row)
			continue
		}
		row.Since = m.FetchedAt.UTC()
		if a := m.Analysis; a != nil && a.AnalyzedAt.After(row.Since) {
			row.Since = a.AnalyzedAt.UTC()
		}
		switch {
		case len(m.Segments) > 0:
			row.State, row.Detail = "Found", segmentsDetail(m.Segments)
			found = append(found, row)
		case m.Result == catalogv1.MarkersError || (m.Result == "" && m.Analysis != nil && m.Analysis.Result == catalogv1.MarkersError):
			row.State, row.Detail = "Error", markersSources(m)
			row.Message = m.Message
			if row.Message == "" && m.Analysis != nil {
				row.Message = m.Analysis.Message
			}
			failed = append(failed, row)
		default:
			row.State, row.Detail = "None found", markersSources(m)
			notFound = append(notFound, row)
		}
	}
	return MarkerSummary{
		Pending:  fileList(pending, oldestFirst, DashboardRows),
		Found:    fileList(found, newestFirst, DashboardRows),
		NotFound: fileList(notFound, newestFirst, DashboardRows),
		Error:    fileList(failed, newestFirst, DashboardRows),
	}
}

// segmentsDetail names a file's segments with their times and sources:
// "intro 0:42-2:12 (theintrodb) · credits 41:10-42:30 (chapters)".
func segmentsDetail(segs []catalogv1.MarkerSegment) string {
	parts := make([]string, 0, len(segs))
	for _, s := range segs {
		src := string(s.Source)
		if src == "" {
			src = "theintrodb"
		}
		parts = append(parts, fmt.Sprintf("%s %s-%s (%s)", s.Kind, clock(s.StartMs), clock(s.EndMs), src))
	}
	return strings.Join(parts, " · ")
}

// markersSources says who looked: TheIntroDB's result and the analysis'.
func markersSources(m *catalogv1.FileMarkers) string {
	var parts []string
	if m.Result != "" {
		parts = append(parts, "TheIntroDB: "+string(m.Result))
	}
	if a := m.Analysis; a != nil {
		parts = append(parts, "analysis: "+string(a.Result))
	}
	return strings.Join(parts, " · ")
}

// clock is a millisecond offset as m:ss, or h:mm:ss past an hour.
func clock(ms int64) string {
	s := ms / 1000
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// fileRow is mf's row without its state.
func fileRow(mf *catalogv1.MediaFile) FileRow {
	return FileRow{
		File:    types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name},
		Name:    path.Base(mf.Spec.Path),
		ItemURL: "/library/" + mf.Namespace + "/" + string(mf.Spec.MediaRef.Kind) + "/" + mf.Spec.MediaRef.Name,
	}
}

// earliest is the earliest of the items' times at, zero for none.
func earliest(items []catalogv1.SubtitleItemStatus, at func(catalogv1.SubtitleItemStatus) *metav1.Time) time.Time {
	var out time.Time
	for _, it := range items {
		if t := at(it); t != nil && !t.IsZero() && (out.IsZero() || t.Before(&metav1.Time{Time: out})) {
			out = t.UTC()
		}
	}
	return out
}

// latest is the latest of the items' times at, zero for none.
func latest(items []catalogv1.SubtitleItemStatus, at func(catalogv1.SubtitleItemStatus) *metav1.Time) time.Time {
	var out time.Time
	for _, it := range items {
		if t := at(it); t != nil && t.After(out) {
			out = t.UTC()
		}
	}
	return out
}

// fileList sorts rows by Since and keeps the first keep.
func fileList(rows []FileRow, newest bool, keep int) FileList {
	sort.SliceStable(rows, func(i, j int) bool {
		if newest {
			return rows[i].Since.After(rows[j].Since)
		}
		return rows[i].Since.Before(rows[j].Since)
	})
	return FileList{Rows: rows[:min(len(rows), keep)], Total: len(rows)}
}
