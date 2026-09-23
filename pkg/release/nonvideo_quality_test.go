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

package release

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// The title-only cases of Lidarr's QualityParserFixture
// (src/NzbDrone.Core.Test/ParserTests/QualityParserFixture.cs, develop,
// fetched 2026-09-23), with Lidarr's expected quality for each. The
// description-and-bitrate cases (TagLib input) are AudioFileQuality's.
func TestMusicQualityMatchesLidarrFixture(t *testing.T) {
	cases := map[string][]string{
		"MP3-192": {
			"VA - The Best 101 Love Ballads (2017) MP3 [192 kbps]",
			"ATCQ - The Love Movement 1998 2CD 192kbps  RIP",
			"A Tribe Called Quest - The Love Movement 1998 2CD [192kbps] RIP",
			"Maula - Jism 2 [2012] Mp3 - 192Kbps [Extended]- TK",
			"VA - Complete Clubland - The Ultimate Ride Of Your Lfe [2014][MP3][192 kbps]",
			"Complete Clubland - The Ultimate Ride Of Your Lfe [2014][MP3](192kbps)",
			"The Ultimate Ride Of Your Lfe [192 KBPS][2014][MP3]",
			"Gary Clark Jr - Live North America 2016 (2017) MP3 192kbps",
			"Some Song [192][2014][MP3]",
			"Other Song (192)[2014][MP3]",
		},
		"MP3-256": {
			"Caetano Veloso Discografia Completa MP3 @256",
			"Ricky Martin - A Quien Quiera Escuchar (2015) 256 kbps [GloDLS]",
			"Jake Bugg - Jake Bugg (Album) [2012] {MP3 256 kbps}",
			"Clean Bandit - New Eyes [2014] [Mp3-256]-V3nom [GLT]",
			"Armin van Buuren - A State Of Trance 810 (20.04.2017) 256 kbps",
			"PJ Harvey - Let England Shake [mp3-256-2011][trfkad]",
		},
		"MP3-320": {
			"Beyoncé Lemonade [320] 2016 Beyonce Lemonade [320] 2016",
			"Childish Gambino - Awaken, My Love Album 2016 mp3 320 Kbps",
			"Maluma – Felices Los 4 MP3 320 Kbps 2017 Download",
			"Ricardo Arjona - APNEA (Single 2014) (320 kbps)",
			"Kehlani - SweetSexySavage (Deluxe Edition) (2017) 320",
			"Anderson Paak - Malibu (320)(2016)",
			"Zeynep_Erbay-Flashlights_On_Love-WEB-2022-BABAS",
		},
		"MP3-VBR-V0": {
			"Sia - This Is Acting (Standard Edition) [2016-Web-MP3-V0(VBR)]",
			"Mount Eerie - A Crow Looked at Me (2017) [MP3 V0 VBR)]",
		},
		"FLAC": {
			"Kendrick Lamar - DAMN (2017) FLAC",
			"Kid_Cudi-Entergalactic-WEBFLAC-2022-NACHOS",
			"Alicia Keys - Vault Playlist Vol. 1 (2017) [FLAC CD]",
			"Gorillaz - Humanz (Deluxe) - lossless FLAC Tracks - 2017 - CDrip",
			"David Bowie - Blackstar (2016) [FLAC]",
			"The Cure - Greatest Hits (2001) FLAC Soup",
			"Slowdive- Souvlaki (FLAC)",
			"John Coltrane - Kulu Se Mama (1965) [EAC-FLAC]",
			"The Rolling Stones - The Very Best Of '75-'94 (1995) {FLAC}",
			"Migos-No_Label_II-CD-FLAC-2014-FORSAKEN",
			"ADELE 25 CD FLAC 2015 PERFECT",
			"Audio Adrinaline - Audio Adrinaline [Mixtape FLAC]",
			"Brain Ape - Rig it [2014][flac]",
			"Coil - The Ape Of Naples(2005) (FLAC)",
			"Opus - Drums Unlimited (1966) [Flac]",
		},
		"FLAC 24bit": {
			"Beck.-.Guero.2005.[2016.Remastered].24bit.96kHz.LOSSLESS.FLAC",
			"[R.E.M - Lifes Rich Pageant(1986) [24bit192kHz 2016 Remaster]LOSSLESS FLAC]",
			"Kid_Cudi-Entergalactic-24BIT-WEBFLAC-2022-NACHOS",
			"Foghat-Foghat_Live-24-192-WEB-FLAC-REMASTERED-2016-OBZEN",
			"John Mellencamp-Plain Spoken From The Chicago Theatre-24-48-WEB-FLAC-2018-OBZEN",
			"Nazareth-Close Enough For Rock N Roll-24-96-WEB-FLAC-REMASTERED-2021-OBZEN",
			"Green_Day-Father_Of_All-24-44-WEB-FLAC-2020-OBZEN",
			"[TR24][OF] Good Charlotte - Generation Rx - 2018 (Pop-Punk | Alternative Rock)",
			"Green Day - Father Of All [FLAC (M4A) 24-bit Lossless]",
			"Green_Day-Father_Of_All_FLAC_M4A_24_bit_Lossless",
			"Green.Day-Father.Of.All.FLAC.M4A.24.bit.Lossless",
			"Linkin Park - Studio Collection 2000-2012 (2013) [WEB FLAC24-44.1]",
			"Linkin Park - Studio Collection 2000-2012 (2013) [WEB FLAC24bit]",
			"Linkin Park - Studio Collection 2000-2012 (2013) [WEB FLAC24-bit]",
		},
		"ALAC": {
			"Chuck Berry Discography ALAC",
			"A$AP Rocky - LONG LIVE A$AP Deluxe asap[ALAC]",
		},
		"APE": {
			"Stevie Ray Vaughan Discography (1981-1987) [APE]",
			"Brain Ape - Rig it [2014][ape]",
		},
		"WavPack": {
			"Max Roach - Drums Unlimited (1966) [WavPack]",
			"Roxette - Charm School(2011) (2CD) [WV]",
		},
		"AAC-256": {
			"Milky Chance - Sadnecessary [256 Kbps] [M4A]",
			"Little Mix - Salute [Deluxe Edition] [2013] [M4A-256]-V3nom [GLT",
			"X-Men Soundtracks (2006-2014) AAC, 256 kbps",
			"The Weeknd - The Hills - Single[iTunes Plus AAC M4A]",
			"Walk the Line Soundtrack (2005) [AAC, 256 kbps]",
			"Firefly Soundtrack(2007 (2002-2003)) [AAC, 256 kbps VBR]",
		},
		"OGG Vorbis Q10": {"Kirlian Camera - The Ice Curtain - Album 1998 - Ogg-Vorbis Q10"},
		"OGG Vorbis Q8":  {"Various Artists - No New York [1978/Ogg/q8]"},
		"OGG Vorbis Q7":  {"Masters_At_Work-Nuyorican_Soul-.Talkin_Loud.-1997-OGG.Q7"},
		"Unknown": {
			"Roberta Flack 2006 - The Very Best of",
			"The Chainsmokers & Coldplay - Something Just Like This",
			"Frank Ocean Blonde 2016",
			"Queen - The Ultimate Best Of Queen(2011)[mp3]",
			"Maroon 5 Ft Kendrick Lamar -Dont Wanna Know MP3 2016",
			"Arctic Monkeys - AM {2013-Album}",
			"Audio Adrinaline - Audio Adrinaline",
		},
	}
	for want, titles := range cases {
		for _, title := range titles {
			assert.Equal(t, want, musicQuality(title).Name, title)
		}
	}
}

// Lidarr's parsing_our_own_quality_enum_name: "Some album [<name>]" parses
// back to the quality it names.
func TestMusicQualityParsesLidarrsOwnNames(t *testing.T) {
	for _, name := range []string{
		"MP3-192", "MP3-VBR-V0", "MP3-256", "MP3-320", "MP3-VBR-V2", "WAV", "WMA",
		"AAC-192", "AAC-256", "AAC-320", "AAC-VBR", "ALAC", "FLAC",
	} {
		assert.Equal(t, name, musicQuality("Some album ["+name+"]").Name)
	}
}

// Lidarr's TagLib cases, restated as what a probe reports: ffprobe's
// codec_name for the description, the stream bitrate, the sample size.
func TestAudioFileQualityMatchesLidarrFindQuality(t *testing.T) {
	tests := []struct {
		codec        string
		kbps, sample int
		want         string
	}{
		{"mp3", 96, 0, "MP3-96"},
		{"mp3", 128, 0, "MP3-128"},
		{"mp3", 160, 0, "MP3-160"},
		{"mp3", 192, 0, "MP3-192"},
		{"mp3", 256, 0, "MP3-256"},
		{"mp3", 320, 0, "MP3-320"},
		{"mp3", 245, 0, "Unknown"}, // a VBR average is not a Lidarr value
		{"flac", 1057, 0, "FLAC"},
		{"flac", 5057, 24, "FLAC 24bit"},
		{"alac", 0, 0, "ALAC"},
		{"alac", 0, 24, "ALAC 24bit"},
		{"wmav2", 218, 0, "WMA"},
		{"pcm_s16le", 1411, 0, "WAV"},
		{"ape", 0, 0, "APE"},
		{"wavpack", 0, 0, "WavPack"},
		{"aac", 320, 0, "AAC-320"},
		{"aac", 321, 0, "AAC-VBR"},
		{"vorbis", 500, 0, "OGG Vorbis Q10"},
		{"opus", 501, 0, "OGG Vorbis Q10"},
		{"vorbis", 320, 0, "OGG Vorbis Q9"},
		{"opus", 321, 0, "OGG Vorbis Q9"},
		{"vorbis", 256, 0, "OGG Vorbis Q8"},
		{"opus", 257, 0, "OGG Vorbis Q8"},
		{"vorbis", 224, 0, "OGG Vorbis Q7"},
		{"opus", 225, 0, "OGG Vorbis Q7"},
		{"vorbis", 192, 0, "OGG Vorbis Q6"},
		{"opus", 193, 0, "OGG Vorbis Q6"},
		{"vorbis", 160, 0, "OGG Vorbis Q5"},
		{"opus", 161, 0, "OGG Vorbis Q5"},
		{"opus", 96, 0, "Unknown"},
		{"mp2", 192, 0, "Unknown"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, AudioFileQuality(tt.codec, tt.kbps, tt.sample).Name, "%+v", tt)
	}
}

// The title-only cases of Readarr's QualityParserFixture (develop, fetched
// 2026-09-23).
func TestBookQualityMatchesReadarrFixture(t *testing.T) {
	cases := map[string][]string{
		"MP3": {
			"VA - The Best 101 Love Ballads (2017) MP3 [192 kbps]",
			"Maula - Jism 2 [2012] Mp3 - 192Kbps [Extended]- TK",
			"Gary Clark Jr - Live North America 2016 (2017) MP3 192kbps",
			"Some Song [192][2014][MP3]",
			"Caetano Veloso Discografia Completa MP3 @256",
			"Jake Bugg - Jake Bugg (Book) [2012] {MP3 256 kbps}",
			"Clean Bandit - New Eyes [2014] [Mp3-256]-V3nom [GLT]",
			"Sia - This Is Acting (Standard Edition) [2016-Web-MP3-V0(VBR)]",
			"Queen - The Ultimate Best Of Queen(2011)[mp3]",
			"Maroon 5 Ft Kendrick Lamar -Dont Wanna Know MP3 2016",
		},
		"FLAC": {
			"Kendrick Lamar - DAMN (2017) FLAC",
			"David Bowie - Blackstar (2016) [FLAC]",
			"The Rolling Stones - The Very Best Of '75-'94 (1995) {FLAC}",
			"ADELE 25 CD FLAC 2015 PERFECT",
		},
		"M4B":  {"Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]", "Little Mix - Salute [M4A-256]"},
		"EPUB": {"Some book [EPUB]"},
		"MOBI": {"Some book [MOBI]"},
		"AZW3": {"Some book [AZW3]", "Some book [AZW]"},
		"PDF":  {"Some book [PDF]"},
	}
	for want, titles := range cases {
		for _, title := range titles {
			q, ok := bookQuality(title)
			require.True(t, ok, title)
			assert.Equal(t, want, q.Name, title)
		}
	}
	for _, title := range []string{"Roberta Flack 2006 - The Very Best of", "Frank Ocean Blonde 2016"} {
		_, ok := bookQuality(title)
		assert.False(t, ok, title)
	}
}

// TestParseGivesNonVideoReleasesAQuality is the carried defect "A non-video
// release cannot be quality-decided": pkg/release filled Quality only for
// movie, TV and anime titles, so every music, book, audiobook and comic
// release carried the zero Quality no profile tier could hold.
func TestParseGivesNonVideoReleasesAQuality(t *testing.T) {
	tests := []struct {
		title   string
		kind    commonv1.MediaKind
		want    string
		version int32
	}{
		{"Pink Floyd - The Dark Side of the Moon (1973) [FLAC]", commonv1.MediaKindAlbum, "FLAC", 1},
		{"Pink Floyd - The Dark Side of the Moon (1973) [FLAC 24bit]", commonv1.MediaKindAlbum, "FLAC 24bit", 1},
		{"Pink Floyd - The Dark Side of the Moon (1973) [MP3 320]", commonv1.MediaKindAlbum, "MP3-320", 1},
		{"Pink Floyd - The Dark Side of the Moon (1973) [MP3 V0 VBR]", commonv1.MediaKindAlbum, "MP3-VBR-V0", 1},
		{"Pink Floyd - The Dark Side of the Moon (1973) [FLAC] REPACK", commonv1.MediaKindAlbum, "FLAC", 2},
		{"Andy Weir - Project Hail Mary (2021) [EPUB]", commonv1.MediaKindBook, "EPUB", 1},
		{"Andy Weir - Project Hail Mary (2021) [AZW3]", commonv1.MediaKindBook, "AZW3", 1},
		{"Andy Weir - Project Hail Mary (2021) [DJVU]", commonv1.MediaKindBook, "Unknown Text", 1},
		{"Andy Weir - Project Hail Mary (Unabridged) [M4B 64kbps]", commonv1.MediaKindAudiobook, "M4B", 1},
		{"Andy Weir - Project Hail Mary (Unabridged) [MP3]", commonv1.MediaKindAudiobook, "MP3", 1},
		{"Andy Weir - Project Hail Mary (Unabridged) [OPUS]", commonv1.MediaKindAudiobook, "MP3", 1}, // Readarr: every other audio codec is MP3
		{"Andy Weir - Project Hail Mary (Unabridged) [AAX]", commonv1.MediaKindAudiobook, "Unknown Audio", 1},
		{"Project Hail Mary - Andy Weir {Ray Porter} [ASIN B08G9PRS1K] [M4B]", commonv1.MediaKindAudiobook, "M4B", 1},
		{"Saga 001 (2012) (Digital) (Zone-Empire).cbz", commonv1.MediaKindIssue, "CBZ", 1},
		{"Saga 001 (2012) (Digital) (Zone-Empire).cbr", commonv1.MediaKindIssue, "CBR", 1},
		{"Saga 001 (2012) (Digital) [CBZ]", commonv1.MediaKindIssue, "CBZ", 1},
		{"Saga 001 (2012) (Digital)", commonv1.MediaKindIssue, "Unknown", 1},
		{"One Piece v107 c1088 (2023) [PDF]", commonv1.MediaKindComic, "PDF", 1},
	}
	for _, tt := range tests {
		p, err := Parse(tt.title, Options{Kind: tt.kind})
		require.NoError(t, err, tt.title)
		assert.Equal(t, commonv1.Quality{Name: tt.want}, p.Quality, tt.title)
		assert.Equal(t, tt.version, p.Revision.Version, tt.title)
	}
}
