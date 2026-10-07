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

package xmltv

import (
	"bytes"
	"encoding/xml"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Mapping names an XMLTV channel in one of a provider's guides.
type Mapping struct {
	Guide     string
	ChannelID string
}

// LineupChannel is one active channel of the built guide. A nil Map with
// DummyMinutes > 0 is a dummy: blocks of DummyMinutes titled Name.
type LineupChannel struct {
	Number           string
	Name             string
	Logo             string
	Map              *Mapping
	DummyMinutes     int
	TimeshiftMinutes int
}

// BuildGuide writes the XEPG guide: one <channel id="<number>"> per lineup
// channel, since Plex maps an XMLTV channel to a lineup entry by its
// GuideNumber (as xTeVe does), then each channel's programmes from its
// mapped guide, timeshifted, or dummy blocks, over [from, from+days).
// Channels go in number order, so the same input gives the same bytes.
func BuildGuide(w io.Writer, lineup []LineupChannel, guides map[string]Guide, from time.Time, days int) error {
	chans := slices.Clone(lineup)
	slices.SortStableFunc(chans, func(a, b LineupChannel) int { return CompareNumbers(a.Number, b.Number) })
	to := from.AddDate(0, 0, days)
	var bw bytes.Buffer
	bw.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<tv generator-info-name="clustarr">` + "\n")
	for _, c := range chans {
		bw.WriteString(`<channel id="`)
		escape(&bw, c.Number)
		bw.WriteString(`"><display-name>`)
		escape(&bw, c.Name)
		bw.WriteString(`</display-name>`)
		if c.Logo != "" {
			bw.WriteString(`<icon src="`)
			escape(&bw, c.Logo)
			bw.WriteString(`"></icon>`)
		}
		bw.WriteString("</channel>\n")
	}
	for _, c := range chans {
		if c.Map != nil {
			shift := time.Duration(c.TimeshiftMinutes) * time.Minute
			for _, p := range guides[c.Map.Guide].Programmes[c.Map.ChannelID] {
				start, stop := p.Start.Add(shift), p.Stop.Add(shift)
				if !stop.After(from) || !start.Before(to) {
					continue
				}
				programme(&bw, c.Number, start, stop, p.Inner)
			}
			continue
		}
		if c.DummyMinutes <= 0 {
			continue
		}
		block := time.Duration(c.DummyMinutes) * time.Minute
		var title bytes.Buffer
		title.WriteString("<title>")
		escape(&title, c.Name)
		title.WriteString("</title>")
		for t := from.UTC().Truncate(block); t.Before(to); t = t.Add(block) {
			programme(&bw, c.Number, t, t.Add(block), title.Bytes())
		}
	}
	bw.WriteString("</tv>\n")
	_, err := w.Write(bw.Bytes())
	return err
}

func programme(w *bytes.Buffer, number string, start, stop time.Time, inner []byte) {
	w.WriteString(`<programme start="`)
	w.WriteString(start.UTC().Format(timeLayout))
	w.WriteString(`" stop="`)
	w.WriteString(stop.UTC().Format(timeLayout))
	w.WriteString(`" channel="`)
	escape(w, number)
	w.WriteString(`">`)
	w.Write(inner)
	w.WriteString("</programme>\n")
}

func escape(w io.Writer, s string) { _ = xml.EscapeText(w, []byte(s)) }

// CompareNumbers orders guide numbers numerically: "9" < "9.2" < "10".
func CompareNumbers(a, b string) int {
	am, an := splitNumber(a)
	bm, bn := splitNumber(b)
	if am != bm {
		return compareInt(am, bm)
	}
	if an != bn {
		return compareInt(an, bn)
	}
	return strings.Compare(a, b)
}

func splitNumber(s string) (major, minor int) {
	ma, mi, _ := strings.Cut(s, ".")
	major, _ = strconv.Atoi(ma)
	minor, _ = strconv.Atoi(mi)
	return major, minor
}

func compareInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
