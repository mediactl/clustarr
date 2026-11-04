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

package ui

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// The season table's file columns (owner, 2026-11-04: "Each item should
// also include Languages, Score, Video and Audio Codec, Transcode status,
// Subtitle status, Marker status"). Every cell reads the episode's
// MediaFile alone -- its frozen spec, its probe and the remediation loop's
// three per-file blocks -- as the modal's Files table does, so the two
// never disagree; the Episode's own status carries the quality the status
// cell shows and nothing else of the file.

// episodeFile reads the MediaFile behind ep by name (status.fileRef), one
// cached Get, never a List: nil when the episode has no file, names none,
// or the reader cannot find it.
func (s *Server) episodeFile(ctx context.Context, ep *catalogv1.Episode) *catalogv1.MediaFile {
	ref := ep.Status.FileRef
	if !ep.Status.HasFile || ref == nil || *ref == "" {
		return nil
	}
	var f catalogv1.MediaFile
	if !s.getObject(ctx, types.NamespacedName{Namespace: ep.Namespace, Name: *ref}, &f) {
		return nil
	}
	return &f
}

// episodeFiles reads the file of every episode of one season of series,
// by episode name: a Get per file from the ui's cache (12 to 26 a season),
// where a List would cost a decode of every MediaFile the cache holds.
func (s *Server) episodeFiles(ctx context.Context, episodes []catalogv1.Episode, series string, season int32) map[string]*catalogv1.MediaFile {
	files := map[string]*catalogv1.MediaFile{}
	for i := range episodes {
		ep := &episodes[i]
		if ep.Spec.SeriesRef != series || ep.Spec.SeasonNumber != season {
			continue
		}
		if f := s.episodeFile(ctx, ep); f != nil {
			files[ep.Name] = f
		}
	}
	return files
}

// episodeFileCells is the season table's view of f: nil for no file.
func episodeFileCells(f *catalogv1.MediaFile) *views.EpisodeFile {
	if f == nil {
		return nil
	}
	return &views.EpisodeFile{
		Languages:  f.Spec.Languages,
		Score:      f.Spec.FormatScore,
		VideoCodec: videoCell(f),
		AudioCodec: audioCell(f.Status.MediaInfo),
		Transcode:  transcodeCell(f),
		Subtitles:  subtitlesCell(f.Status.Subtitles),
		Markers:    markersCell(f.Status.Markers),
	}
}

// videoCell is the probe's video: the codec by its common name, its bit
// depth past 8 and its HDR format ("HEVC 10-bit HDR10"), with the
// profile, resolution, frame rate, bitrate and container as the tooltip.
// A codec no GPU here decodes (MediaFile.UndecodableVideo) is warned: its
// transcode is skipped and a release of any quality replaces it. Nothing
// before the probe.
func videoCell(f *catalogv1.MediaFile) views.Cell {
	mi := f.Status.MediaInfo
	if mi == nil || mi.VideoCodec == "" {
		return views.Cell{}
	}
	label := codecName(mi.VideoCodec)
	if mi.VideoBitDepth > 8 {
		label += fmt.Sprintf(" %d-bit", mi.VideoBitDepth)
	}
	if h := hdrShort(mi.Hdr); h != "" {
		label += " " + h
	}
	var facts []string
	if mi.VideoProfile != "" {
		facts = append(facts, mi.VideoProfile)
	}
	if mi.Width > 0 && mi.Height > 0 {
		facts = append(facts, fmt.Sprintf("%dx%d", mi.Width, mi.Height))
	}
	if mi.FpsMilli > 0 {
		facts = append(facts, fpsLabel(mi.FpsMilli)+" fps")
	}
	if mi.VideoBitrateKbps > 0 {
		facts = append(facts, fmt.Sprintf("%d kbps", mi.VideoBitrateKbps))
	}
	if h := hdrLong(mi.Hdr); h != "" {
		facts = append(facts, h)
	}
	if mi.Container != "" {
		facts = append(facts, mi.Container)
	}
	c := views.Cell{Label: label, Title: strings.Join(facts, ", ")}
	if u := f.UndecodableVideo(); u != "" {
		c.Tone = "warn"
		c.Title = joinFacts("; ", "no GPU here decodes "+u+", so a release of any quality the profile takes replaces it", c.Title)
	}
	return c
}

// codecName is a video codec as people write it: ffprobe's "h264" reads
// H.264, "hevc" HEVC, "mpeg4" MPEG-4; one with no common name is
// upper-cased.
func codecName(codec string) string {
	switch strings.ToLower(codec) {
	case "h264", "avc":
		return "H.264"
	case "hevc", "h265":
		return "HEVC"
	case "av1":
		return "AV1"
	case "vp9":
		return "VP9"
	case "vp8":
		return "VP8"
	case "mpeg4":
		return "MPEG-4"
	case "msmpeg4v1", "msmpeg4v2", "msmpeg4v3":
		return "MS-MPEG-4"
	case "mpeg2video":
		return "MPEG-2"
	case "mpeg1video":
		return "MPEG-1"
	case "vc1":
		return "VC-1"
	}
	return strings.ToUpper(codec)
}

// hdrShort is the HDR format for the cell: "HDR10", "HDR10+", "HLG",
// "PQ10", "DV" and its base layer ("DV/HDR10"); "" for SDR.
func hdrShort(h commonv1.HdrFormat) string {
	switch h {
	case commonv1.HdrFormatHDR10:
		return "HDR10"
	case commonv1.HdrFormatHDR10Plus:
		return "HDR10+"
	case commonv1.HdrFormatHLG10:
		return "HLG"
	case commonv1.HdrFormatPQ10:
		return "PQ10"
	case commonv1.HdrFormatDolbyVision:
		return "DV"
	case commonv1.HdrFormatDolbyVisionHDR10:
		return "DV/HDR10"
	case commonv1.HdrFormatDolbyVisionSDR:
		return "DV/SDR"
	case commonv1.HdrFormatDolbyVisionHLG:
		return "DV/HLG"
	case commonv1.HdrFormatDolbyVisionHDR10Plus:
		return "DV/HDR10+"
	}
	return ""
}

// hdrLong spells the HDR format out for the tooltip; "" for SDR.
func hdrLong(h commonv1.HdrFormat) string {
	switch h {
	case commonv1.HdrFormatDolbyVision:
		return "Dolby Vision"
	case commonv1.HdrFormatDolbyVisionHDR10:
		return "Dolby Vision over HDR10"
	case commonv1.HdrFormatDolbyVisionSDR:
		return "Dolby Vision over SDR"
	case commonv1.HdrFormatDolbyVisionHLG:
		return "Dolby Vision over HLG"
	case commonv1.HdrFormatDolbyVisionHDR10Plus:
		return "Dolby Vision over HDR10+"
	}
	return hdrShort(h)
}

// fpsLabel writes a frame rate in thousandths as people do: 23976 reads
// 23.976, 25000 reads 25.
func fpsLabel(milli int32) string {
	s := fmt.Sprintf("%d.%03d", milli/1000, milli%1000)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}

// audioCell is the first audio track as Radarr labels it ("EAC3 - 5.1")
// and how many more the file carries; the tooltip lists every track with
// its language and title, one a line. Nothing before the probe.
func audioCell(mi *commonv1.MediaInfo) views.Cell {
	if mi == nil || len(mi.Audio) == 0 {
		return views.Cell{}
	}
	label := audioLabel(mi.Audio[0])
	if n := len(mi.Audio) - 1; n > 0 {
		label += fmt.Sprintf(" +%d", n)
	}
	lines := make([]string, 0, len(mi.Audio))
	for _, a := range mi.Audio {
		line := audioLabel(a)
		if a.Language != "" {
			line = a.Language + ": " + line
		}
		if a.Title != "" {
			line += " (" + a.Title + ")"
		}
		lines = append(lines, line)
	}
	return views.Cell{Label: label, Title: strings.Join(lines, "\n")}
}

// transcodeCell is the file's transcode: status.transcode's phase while
// the loop holds one -- Succeeded reads Transcoded, as the item's phase
// does; Failed and Skipped keep their names -- else Transcoded for a file
// already tagged or another tool's (MediaFile.Transcoded), and nothing for
// a file no transcode has reached (outside its profile's window, or
// selected by none). The tooltip is the reason and message, the plan's
// description, the profile and the pool.
func transcodeCell(f *catalogv1.MediaFile) views.Cell {
	t := f.Status.Transcode
	if t == nil {
		if !f.Transcoded() {
			return views.Cell{}
		}
		c := views.Cell{Label: "Transcoded", Tone: "done", Title: "final: no automatic grab replaces it"}
		if f.Upgradeable() {
			c.Title = "upgradeable: read as the release it was imported from"
		}
		if mi := f.Status.MediaInfo; mi != nil && mi.TranscodeProfile != "" {
			c.Title = joinFacts("; ", "tagged "+mi.TranscodeProfile, c.Title)
		} else if mi != nil && mi.TranscodedElsewhere() {
			c.Title = joinFacts("; ", "transcoded by another tool ("+mi.VideoEncoder+")", c.Title)
		}
		return c
	}
	c := views.Cell{Label: string(t.Phase)}
	switch t.Phase {
	case catalogv1.TranscodePhaseSucceeded:
		c.Label, c.Tone = "Transcoded", "done"
	case catalogv1.TranscodePhaseFailed:
		c.Tone = "fail"
	case catalogv1.TranscodePhaseSkipped:
		c.Tone = ""
	case "":
		return views.Cell{}
	default:
		c.Tone = "busy"
	}
	var facts []string
	if t.Reason != "" {
		facts = append(facts, t.Reason)
	}
	if t.Message != "" {
		facts = append(facts, clampRunes(t.Message, 200))
	}
	if d := t.Plan.Describe(); d != "" {
		facts = append(facts, d)
	}
	if t.Profile != "" {
		facts = append(facts, "profile "+t.Profile)
	}
	if t.Pool != "" {
		facts = append(facts, "pool "+t.Pool)
	}
	if t.Suspended {
		facts = append(facts, "suspended")
	}
	c.Title = strings.Join(facts, "; ")
	return c
}

// subtitlesCell is status.subtitles: its phase, Wanted and Searching
// naming the languages still missing ("Wanted fr"), and nothing for a
// file the subtitles planner wants nothing for (no block). The tooltip
// is each language's state, provider and score, and the block's reason.
func subtitlesCell(st *catalogv1.SubtitlesStatus) views.Cell {
	if st == nil {
		return views.Cell{}
	}
	var missing, lines []string
	for _, it := range st.Items {
		line := it.LangKey + ": " + string(it.State)
		if it.Provider != "" {
			line += " (" + it.Provider + ")"
		}
		if it.ScoreOutOf > 0 {
			line += fmt.Sprintf(" %d/%d", it.Score, it.ScoreOutOf)
		}
		if it.LastError != "" {
			line += ": " + clampRunes(it.LastError, 120)
		}
		lines = append(lines, line)
		switch it.State {
		case catalogv1.SubtitleStateDownloaded, catalogv1.SubtitleStateUpgradable:
		default:
			missing = append(missing, it.LangKey)
		}
	}
	c := views.Cell{Label: string(st.Phase)}
	switch st.Phase {
	case catalogv1.SubtitlesPhaseSatisfied:
		c.Tone = "done"
	case catalogv1.SubtitlesPhaseSearching:
		c.Tone = "busy"
	case catalogv1.SubtitlesPhaseWanted:
		c.Tone = "warn"
	case catalogv1.SubtitlesPhaseBlocked:
		c.Tone = "fail"
	case catalogv1.SubtitlesPhaseNotWanted:
		c.Label = "Not wanted"
	case "":
		return views.Cell{}
	}
	if len(missing) > 0 && (st.Phase == catalogv1.SubtitlesPhaseWanted || st.Phase == catalogv1.SubtitlesPhaseSearching) {
		c.Label += " " + strings.Join(missing, ", ")
	}
	head := joinFacts(": ", st.Reason, clampRunes(st.Message, 200))
	c.Title = joinFacts("\n", head, strings.Join(lines, "\n"))
	return c
}

// markersCell names the file's skip segments by kind ("intro · credits"),
// with their times and sources as the tooltip (markersLabel); "None found"
// once TheIntroDB or clustarr's own analysis looked and found none,
// "Error" when TheIntroDB could not be asked, and nothing before either
// has looked.
func markersCell(m *catalogv1.FileMarkers) views.Cell {
	if m == nil {
		return views.Cell{}
	}
	if len(m.Segments) > 0 {
		var kinds []string
		seen := map[catalogv1.MarkerKind]bool{}
		for _, s := range m.Segments {
			if !seen[s.Kind] {
				seen[s.Kind] = true
				kinds = append(kinds, string(s.Kind))
			}
		}
		return views.Cell{Label: strings.Join(kinds, " · "), Title: markersLabel(m), Tone: "done"}
	}
	switch {
	case m.Result == catalogv1.MarkersError:
		title := clampRunes(m.Message, 200)
		if title == "" {
			title = "TheIntroDB unavailable"
		}
		return views.Cell{Label: "Error", Title: title, Tone: "fail"}
	case m.Result == catalogv1.MarkersNotFound || (m.Analysis != nil && m.Analysis.Result == catalogv1.MarkersNotFound):
		return views.Cell{Label: "None found", Title: "neither TheIntroDB nor the analysis found a segment"}
	}
	return views.Cell{}
}

// joinFacts joins the non-empty parts with sep.
func joinFacts(sep string, parts ...string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// clampRunes cuts s to n runes, on a rune boundary, with an ellipsis.
func clampRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
