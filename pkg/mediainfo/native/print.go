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

package native

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/obinnaokechukwu/ffgo/avutil"
	ffprobe "gopkg.in/vansante/go-ffprobe.v2"
)

// codecName is codec_name as ffprobe prints it: a codec with no descriptor
// (ffgo's avcodec_get_name gives "none" or "unknown_codec") is omitted.
func codecName(c string) string {
	if c == "none" || c == "unknown_codec" {
		return ""
	}
	return c
}

// profile is ffprobe's rule: the profile's name, else its number unless it
// is AV_PROFILE_UNKNOWN, else omitted.
func profile(id ffgo.CodecID, p int) string {
	if n := ffgo.ProfileName(id, p); n != "" {
		return n
	}
	if p != ffgo.ProfileUnknown {
		return strconv.Itoa(p)
	}
	return ""
}

// ffprobeTime is print_time: "%f" of ts*av_q2d(tb), "" (N/A, which JSON
// omits) at AV_NOPTS_VALUE.
func ffprobeTime(ts int64, tb ffgo.Rational) string {
	if ts == avutil.AV_NOPTS_VALUE || tb.Den == 0 {
		return ""
	}
	return fmt.Sprintf("%f", float64(ts)*(float64(tb.Num)/float64(tb.Den)))
}

// ffprobeSeconds is ffprobeTime as go-ffprobe's `,string` float decodes it.
func ffprobeSeconds(ts int64, tb ffgo.Rational) float64 {
	v, _ := strconv.ParseFloat(ffprobeTime(ts, tb), 64)
	return v
}

// validString is fftools/textformat/avtextformat.c validate_string with
// string_validation=replace and U+FFFD: each sequence av_utf8_decode (flags
// 0) rejects becomes one U+FFFD. Like C, it stops at the first NUL.
func validString(s string) string {
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	if utf8.ValidString(s) && !strings.ContainsAny(s, "￾￿") {
		return s // Go's validity is av_utf8_decode's except for the two noncharacters
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		n, ok := utf8Decode(s[i:])
		if ok {
			b.WriteString(s[i : i+n])
		} else {
			b.WriteString("�")
		}
		i += n
	}
	return b.String()
}

// utf8Decode is libavutil/avstring.c av_utf8_decode with flags 0: how many
// bytes one call consumes, and whether they decode to an accepted code
// point. An incomplete sequence or a bad continuation consumes one byte.
func utf8Decode(s string) (n int, ok bool) {
	code := uint64(s[0])
	if code&0xc0 == 0x80 || code >= 0xfe {
		return 1, false
	}
	top := (code & 128) >> 1
	p, tail := 1, 0
	for code&top != 0 {
		tail++
		if p >= len(s) {
			return 1, false
		}
		tmp := int(s[p]) - 128
		p++
		if tmp>>6 != 0 {
			return 1, false
		}
		code = code<<6 + uint64(tmp)
		top <<= 5
	}
	code &= top<<1 - 1
	overlong := [6]uint64{0, 0x80, 0x800, 0x10000, 0x200000, 0x4000000}
	switch {
	case code < overlong[tail], code >= 1<<31,
		code > 0x10ffff, code >= 0xd800 && code <= 0xdfff, code == 0xfffe || code == 0xffff:
		return p, false
	}
	return p, true
}

// tags is an AVDictionary as ffprobe prints it: keys and values validated.
func tags(md ffgo.Metadata) ffprobe.Tags {
	if len(md) == 0 {
		return nil
	}
	out := make(ffprobe.Tags, len(md))
	for k, v := range md {
		out[validString(k)] = validString(v)
	}
	return out
}

// streamTags is go-ffprobe's StreamTags.setFrom over the validated tags.
func streamTags(t ffprobe.Tags) ffprobe.StreamTags {
	get := func(k string) string { v, _ := t.GetString(k); return v }
	rotate, _ := t.GetInt("rotate")
	return ffprobe.StreamTags{
		Rotate: int(rotate), CreationTime: get("creation_time"), Language: get("language"),
		Title: get("title"), Encoder: get("encoder"), Location: get("location"),
	}
}
