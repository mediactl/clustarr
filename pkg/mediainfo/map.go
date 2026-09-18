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

package mediainfo

import (
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// bitmapSubtitleCodecs are the image-based subtitle codecs
// docs/research/transcode.md §2.2 lists ("hdmv_pgs_subtitle" |
// "dvd_subtitle" | "dvb_subtitle"), as opposed to text ones.
var bitmapSubtitleCodecs = map[string]bool{
	"hdmv_pgs_subtitle": true,
	"dvd_subtitle":      true,
	"dvb_subtitle":      true,
}

// toMediaInfo maps raw onto the api/common/v1alpha1.MediaInfo the
// MediaFile status carries. Fields the CRD type has no room for (level,
// colour primaries/transfer/matrix, master-display/max-cll, channel
// layout, per-chapter detail) stay on raw; see this task's "Why Raw
// carries..." note.
func toMediaInfo(raw *Raw) *commonv1.MediaInfo {
	mi := &commonv1.MediaInfo{
		Container:     containerFromPath(raw.Format.Filename),
		RuntimeMillis: int64(raw.Format.DurationSeconds*1000 + 0.5),
		Hdr:           ClassifyHDR(raw),
	}
	if v := firstStream(raw.Streams, ffprobe.StreamVideo); v != nil {
		mi.VideoCodec = v.CodecName
		mi.VideoProfile = v.Profile
		mi.PixelFormat = v.PixFmt
		mi.VideoBitDepth = bitDepthFromPixFmt(v.PixFmt)
		mi.Width = int32(v.Width)
		mi.Height = int32(v.Height)
		mi.FpsMilli = frameRateMilli(v.RFrameRate)
		mi.VideoBitrateKbps = kbpsFromBitRate(v.BitRate)
	}
	if raw.Dovi != nil {
		profile, compat := raw.Dovi.Profile, raw.Dovi.BLSignalCompatibilityID
		mi.DoviProfile = &profile
		mi.DoviBLCompatID = &compat
	}
	for _, s := range raw.Streams {
		switch s.CodecType {
		case string(ffprobe.StreamAudio):
			mi.Audio = append(mi.Audio, toAudioStream(s))
		case string(ffprobe.StreamSubtitle):
			mi.Subtitles = append(mi.Subtitles, toSubtitleStream(s))
		case string(ffprobe.StreamAttachment):
			mi.Attachments++
		}
	}
	mi.Chapters = int32(len(raw.Chapters))
	return mi
}

func firstStream(streams []*ffprobe.Stream, t ffprobe.StreamType) *ffprobe.Stream {
	for _, s := range streams {
		if s.CodecType == string(t) {
			return s
		}
	}
	return nil
}

func toAudioStream(s *ffprobe.Stream) commonv1.AudioStream {
	return commonv1.AudioStream{
		Index:       int32(s.Index),
		Codec:       s.CodecName,
		Profile:     s.Profile,
		Language:    s.Tags.Language,
		Title:       s.Tags.Title,
		Channels:    int32(s.Channels),
		BitrateKbps: kbpsFromBitRate(s.BitRate),
		Default:     s.Disposition.Default == 1,
		Commentary:  s.Disposition.Comment == 1,
	}
}

func toSubtitleStream(s *ffprobe.Stream) commonv1.SubtitleStream {
	return commonv1.SubtitleStream{
		Index:           int32(s.Index),
		Codec:           s.CodecName,
		Language:        s.Tags.Language,
		Title:           s.Tags.Title,
		Forced:          s.Disposition.Forced == 1,
		HearingImpaired: s.Disposition.HearingImpaired == 1,
		Bitmap:          bitmapSubtitleCodecs[s.CodecName],
	}
}
