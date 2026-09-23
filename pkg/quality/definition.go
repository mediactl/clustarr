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

package quality

import common "github.com/mediactl/clustarr/api/common/v1alpha1"

// Definition is one row of a media kind's canonical quality table.
// MaxMBPerMin of 0 means unlimited (TRaSH's own "0 = unlimited" convention).
type Definition struct {
	Quality      common.Quality
	Name         string
	Aliases      []string // e.g. Sonarr's "Bluray-1080p Remux" aliases "Remux-1080p"
	Weight       int
	MinMBPerMin  float64
	PrefMBPerMin float64
	MaxMBPerMin  float64

	// Group is the default tie-group name (e.g. "WEB 1080p"), empty when the
	// Definition does not default-group with any other. Profiles override tie
	// behavior explicitly via Tiers, so this is display/lookup metadata only.
	Group string
}

// videoDefinitions is the canonical video quality ladder,
// docs/research/quality.md §1.1, transcribed verbatim: 26 weight slots, 4 of
// which (the WEB 480p/720p/1080p/2160p groups) hold two Definitions apiece
// (WEBDL and WEBRip variants), for 30 rows total.
//
// Every row without a TRaSH modifier sets Modifier: common.ModifierNone
// explicitly -- common.ModifierNone is the string "none", not Go's zero
// value "", so leaving the field unset would make def.Quality.Modifier ""
// rather than the documented sentinel.
var videoDefinitions = []Definition{
	{Name: "Unknown", Weight: 1, Quality: common.Quality{Name: "Unknown", Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WORKPRINT", Weight: 2, Quality: common.Quality{Name: "WORKPRINT", Source: common.SourceWorkprint, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "CAM", Weight: 3, Quality: common.Quality{Name: "CAM", Source: common.SourceCam, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "TELESYNC", Weight: 4, Quality: common.Quality{Name: "TELESYNC", Source: common.SourceTelesync, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "TELECINE", Weight: 5, Quality: common.Quality{Name: "TELECINE", Source: common.SourceTelecine, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "REGIONAL", Weight: 6, Quality: common.Quality{Name: "REGIONAL", Source: common.SourceDVD, Resolution: common.Resolution480p, Modifier: common.ModifierRegional}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "DVDSCR", Weight: 7, Quality: common.Quality{Name: "DVDSCR", Source: common.SourceDVD, Resolution: common.Resolution480p, Modifier: common.ModifierScreener}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "SDTV", Weight: 8, Quality: common.Quality{Name: "SDTV", Source: common.SourceTV, Resolution: common.Resolution480p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "DVD", Weight: 9, Quality: common.Quality{Name: "DVD", Source: common.SourceDVD, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "DVD-R", Weight: 10, Quality: common.Quality{Name: "DVD-R", Source: common.SourceDVD, Resolution: common.Resolution480p, Modifier: common.ModifierRemux}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBDL-480p", Weight: 11, Group: "WEB 480p", Quality: common.Quality{Name: "WEBDL-480p", Source: common.SourceWebDL, Resolution: common.Resolution480p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBRip-480p", Weight: 11, Group: "WEB 480p", Quality: common.Quality{Name: "WEBRip-480p", Source: common.SourceWebRip, Resolution: common.Resolution480p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "Bluray-480p", Weight: 12, Quality: common.Quality{Name: "Bluray-480p", Source: common.SourceBluray, Resolution: common.Resolution480p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "Bluray-576p", Weight: 13, Quality: common.Quality{Name: "Bluray-576p", Source: common.SourceBluray, Resolution: common.Resolution576p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "HDTV-720p", Weight: 14, Quality: common.Quality{Name: "HDTV-720p", Source: common.SourceTV, Resolution: common.Resolution720p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBDL-720p", Weight: 15, Group: "WEB 720p", Quality: common.Quality{Name: "WEBDL-720p", Source: common.SourceWebDL, Resolution: common.Resolution720p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBRip-720p", Weight: 15, Group: "WEB 720p", Quality: common.Quality{Name: "WEBRip-720p", Source: common.SourceWebRip, Resolution: common.Resolution720p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "Bluray-720p", Weight: 16, Quality: common.Quality{Name: "Bluray-720p", Source: common.SourceBluray, Resolution: common.Resolution720p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "HDTV-1080p", Weight: 17, Quality: common.Quality{Name: "HDTV-1080p", Source: common.SourceTV, Resolution: common.Resolution1080p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBDL-1080p", Weight: 18, Group: "WEB 1080p", Quality: common.Quality{Name: "WEBDL-1080p", Source: common.SourceWebDL, Resolution: common.Resolution1080p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "WEBRip-1080p", Weight: 18, Group: "WEB 1080p", Quality: common.Quality{Name: "WEBRip-1080p", Source: common.SourceWebRip, Resolution: common.Resolution1080p, Modifier: common.ModifierNone}, MaxMBPerMin: 100, PrefMBPerMin: 95},
	{Name: "Bluray-1080p", Weight: 19, Quality: common.Quality{Name: "Bluray-1080p", Source: common.SourceBluray, Resolution: common.Resolution1080p, Modifier: common.ModifierNone}}, // no default max/pref (unlimited)
	{Name: "Remux-1080p", Weight: 20, Aliases: []string{"Bluray-1080p Remux"}, Quality: common.Quality{Name: "Remux-1080p", Source: common.SourceBluray, Resolution: common.Resolution1080p, Modifier: common.ModifierRemux}},
	{Name: "HDTV-2160p", Weight: 21, Quality: common.Quality{Name: "HDTV-2160p", Source: common.SourceTV, Resolution: common.Resolution2160p, Modifier: common.ModifierNone}},
	{Name: "WEBDL-2160p", Weight: 22, Group: "WEB 2160p", Quality: common.Quality{Name: "WEBDL-2160p", Source: common.SourceWebDL, Resolution: common.Resolution2160p, Modifier: common.ModifierNone}},
	{Name: "WEBRip-2160p", Weight: 22, Group: "WEB 2160p", Quality: common.Quality{Name: "WEBRip-2160p", Source: common.SourceWebRip, Resolution: common.Resolution2160p, Modifier: common.ModifierNone}},
	{Name: "Bluray-2160p", Weight: 23, Quality: common.Quality{Name: "Bluray-2160p", Source: common.SourceBluray, Resolution: common.Resolution2160p, Modifier: common.ModifierNone}},
	{Name: "Remux-2160p", Weight: 24, Aliases: []string{"Bluray-2160p Remux"}, Quality: common.Quality{Name: "Remux-2160p", Source: common.SourceBluray, Resolution: common.Resolution2160p, Modifier: common.ModifierRemux}},
	{Name: "BR-DISK", Weight: 25, Quality: common.Quality{Name: "BR-DISK", Source: common.SourceBluray, Resolution: common.Resolution1080p, Modifier: common.ModifierBRDisk}},
	{Name: "Raw-HD", Weight: 26, Quality: common.Quality{Name: "Raw-HD", Source: common.SourceTV, Resolution: common.Resolution1080p, Modifier: common.ModifierRawHD}},
}

// nonVideoDefinitions holds the non-video quality ladders. A non-video
// Quality is identified by Name alone, and a release's Quality.Name is the
// upstream app's own quality name -- Lidarr's for music, Readarr's for books
// and audiobooks, the file format for comics (pkg/release) -- so a Definition
// lists every upstream name it stands for in Aliases, and Profile.Index
// places a release by Name or alias.
//
// music collapses Lidarr's 38 qualities into spec §9's eight tiers --
// Trash/Poor/Low/Mid/High lossy, Lossless, 24-bit, WAV -- which are Lidarr's
// own groups (Qualities/Quality.cs, DefaultQualityDefinitions, develop,
// fetched 2026-09-23), with 24-bit split out of Lossless as the spec does
// (Lidarr already weights it above the rest of the group). Each tier lists
// Lidarr's member names, and its group name, as aliases:
//
//	Trash          <- Trash Quality Lossy: MP3-8 .. MP3-80
//	Poor           <- Poor Quality Lossy: MP3-96, MP3-112, MP3-128, MP3-160, OGG Vorbis Q5
//	Low            <- Low Quality Lossy: MP3-192, MP3-224, AAC-192, WMA, OGG Vorbis Q6
//	Mid            <- Mid Quality Lossy: MP3-256, MP3-VBR-V2, AAC-256, OGG Vorbis Q7, OGG Vorbis Q8
//	High           <- High Quality Lossy: MP3-320, MP3-VBR-V0, AAC-320, AAC-VBR, OGG Vorbis Q9, OGG Vorbis Q10
//	FLAC           <- Lossless: FLAC, ALAC, APE, WavPack
//	24bit Lossless <- FLAC 24bit, ALAC 24bit
//	WAV            <- WAV
//
// So every lossy and lossless quality pkg/release can name sits on exactly
// one tier (TestEveryLidarrQualityFitsTheBuiltinMusicProfiles). Lidarr's
// "Unknown" -- a codec the parser did not recognise, or MP1/MP2 -- is on
// none, as it is in Lidarr's own Standard and Lossless profiles.
//
// MP3-192 is a member of Low, as in Lidarr. An earlier ladder had a tier
// NAMED "MP3-192" ranked above Mid, which put a 192 kbps MP3 above a 256 kbps
// one; the name now resolves to Low. The built-in music-standard profile
// keeps spec §9's cutoff "MP3-192" as the name of the tier that holds Low,
// which is what Lidarr's Standard profile's MP3-192 cutoff selects too.
var nonVideoDefinitions = map[string][]Definition{
	"music": {
		{Name: "Trash", Weight: 1, Quality: common.Quality{Name: "Trash"}, Aliases: []string{
			"Trash Quality Lossy", "MP3-8", "MP3-16", "MP3-24", "MP3-32", "MP3-40", "MP3-48", "MP3-56", "MP3-64", "MP3-80",
		}},
		{Name: "Poor", Weight: 2, Quality: common.Quality{Name: "Poor"}, Aliases: []string{
			"Poor Quality Lossy", "MP3-96", "MP3-112", "MP3-128", "MP3-160", "OGG Vorbis Q5",
		}},
		{Name: "Low", Weight: 3, Quality: common.Quality{Name: "Low"}, Aliases: []string{
			"Low Quality Lossy", "MP3-192", "MP3-224", "AAC-192", "WMA", "OGG Vorbis Q6",
		}},
		{Name: "Mid", Weight: 4, Quality: common.Quality{Name: "Mid"}, Aliases: []string{
			"Mid Quality Lossy", "MP3-256", "MP3-VBR-V2", "AAC-256", "OGG Vorbis Q7", "OGG Vorbis Q8",
		}},
		{Name: "High", Weight: 5, Quality: common.Quality{Name: "High"}, Aliases: []string{
			"High Quality Lossy", "MP3-320", "MP3-VBR-V0", "AAC-320", "AAC-VBR", "OGG Vorbis Q9", "OGG Vorbis Q10",
		}},
		{Name: "FLAC", Weight: 6, Quality: common.Quality{Name: "FLAC"}, Aliases: []string{"Lossless", "ALAC", "APE", "WavPack"}},
		{Name: "24bit Lossless", Weight: 7, Quality: common.Quality{Name: "24bit Lossless"}, Aliases: []string{"FLAC 24bit", "ALAC 24bit"}},
		{Name: "WAV", Weight: 8, Quality: common.Quality{Name: "WAV"}},
	},
	"book": {
		{Name: "PDF", Weight: 1, Quality: common.Quality{Name: "PDF"}},
		{Name: "MOBI", Weight: 2, Quality: common.Quality{Name: "MOBI"}},
		{Name: "EPUB", Weight: 3, Quality: common.Quality{Name: "EPUB"}},
		{Name: "AZW3", Weight: 4, Quality: common.Quality{Name: "AZW3"}},
	},
	"audiobook": {
		{Name: "Unknown Audio", Weight: 1, Quality: common.Quality{Name: "Unknown Audio"}},
		{Name: "MP3", Weight: 2, Quality: common.Quality{Name: "MP3"}},
		{Name: "M4B", Weight: 3, Quality: common.Quality{Name: "M4B"}},
		{Name: "FLAC", Weight: 4, Quality: common.Quality{Name: "FLAC"}},
	},
	"comic": {
		{Name: "PDF", Weight: 1, Quality: common.Quality{Name: "PDF"}},
		{Name: "CBR", Weight: 2, Quality: common.Quality{Name: "CBR"}},
		{Name: "CBZ", Weight: 3, Quality: common.Quality{Name: "CBZ"}},
	},
}

// Lookup finds a Definition by media kind ("video", "music", "book",
// "audiobook" or "comic" -- the same strings as
// catalogv1alpha1.ProfileMediaKind's enum values) and a canonical or alias
// name. Video lookups accept both Radarr's and Sonarr's names; music lookups
// accept Lidarr's quality names where the ladder maps them (see
// nonVideoDefinitions).
func Lookup(kind, name string) (Definition, bool) {
	var defs []Definition
	switch kind {
	case "video":
		defs = videoDefinitions
	case "music", "book", "audiobook", "comic":
		defs = nonVideoDefinitions[kind]
	}
	for _, d := range defs {
		if d.named(name) {
			return d, true
		}
	}
	return Definition{}, false
}

// named reports whether name is d's canonical name or one of its aliases.
func (d Definition) named(name string) bool {
	if name == "" {
		return false
	}
	if d.Name == name {
		return true
	}
	for _, a := range d.Aliases {
		if a == name {
			return true
		}
	}
	return false
}
