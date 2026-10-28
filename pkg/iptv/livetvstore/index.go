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

package livetvstore

import (
	"encoding/xml"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
)

// GuideIndex is a provider's XEPG guide in the form the ui's TV Guide reads
// (the design's §5.1): its channels in number order and their programmes,
// each channel's in start order. From is the guide's first day.
type GuideIndex struct {
	From       time.Time        `json:"from"`
	Channels   []IndexChannel   `json:"channels"`
	Programmes []IndexProgramme `json:"programmes"`
}

// IndexChannel is one channel of the guide: its number, name and the logo
// Plex is given.
type IndexChannel struct {
	Number string `json:"number"`
	Name   string `json:"name"`
	Logo   string `json:"logo,omitempty"`
}

// IndexProgramme is one programme. Channel is its channel's number; Episode
// reads as Plex shows it ("S01E02", "E05"); Image is the programme's icon.
type IndexProgramme struct {
	Channel    string    `json:"channel"`
	Start      time.Time `json:"start"`
	Stop       time.Time `json:"stop"`
	Title      string    `json:"title"`
	SubTitle   string    `json:"subTitle,omitempty"`
	Desc       string    `json:"desc,omitempty"`
	Episode    string    `json:"episode,omitempty"`
	Image      string    `json:"image,omitempty"`
	Categories []string  `json:"categories,omitempty"`
	Live       bool      `json:"live,omitempty"`
	New        bool      `json:"new,omitempty"`
}

// maxDescRunes bounds a programme's description in the index: the guide's
// dialog shows that much, and a week of 400 channels stays a few MiB.
const maxDescRunes = 600

// timeLayout is XMLTV's time format, as the tuner writes it.
const timeLayout = "20060102150405 -0700"

type xmlChannel struct {
	ID          string   `xml:"id,attr"`
	DisplayName []string `xml:"display-name"`
	Icon        []struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
}

type xmlProgramme struct {
	Start      string   `xml:"start,attr"`
	Stop       string   `xml:"stop,attr"`
	Channel    string   `xml:"channel,attr"`
	Title      []string `xml:"title"`
	SubTitle   []string `xml:"sub-title"`
	Desc       []string `xml:"desc"`
	Categories []string `xml:"category"`
	EpisodeNum []struct {
		System string `xml:"system,attr"`
		Value  string `xml:",chardata"`
	} `xml:"episode-num"`
	Icon []struct {
		Src string `xml:"src,attr"`
	} `xml:"icon"`
	Live *struct{} `xml:"live"`
	New  *struct{} `xml:"new"`
}

// IndexGuide reads the XEPG guide the tuner built (xmltv.BuildGuideWith)
// into its index, from being the guide's first day.
func IndexGuide(r io.Reader, from time.Time) (GuideIndex, error) {
	ix := GuideIndex{From: from}
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return GuideIndex{}, fmt.Errorf("livetvstore: read the guide: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var c xmlChannel
			if err := dec.DecodeElement(&c, &se); err != nil {
				return GuideIndex{}, fmt.Errorf("livetvstore: read a channel: %w", err)
			}
			ch := IndexChannel{Number: c.ID, Name: first(c.DisplayName)}
			if len(c.Icon) > 0 {
				ch.Logo = c.Icon[0].Src
			}
			ix.Channels = append(ix.Channels, ch)
		case "programme":
			var p xmlProgramme
			if err := dec.DecodeElement(&p, &se); err != nil {
				return GuideIndex{}, fmt.Errorf("livetvstore: read a programme: %w", err)
			}
			start, err1 := time.Parse(timeLayout, p.Start)
			stop, err2 := time.Parse(timeLayout, p.Stop)
			if err1 != nil || err2 != nil {
				continue
			}
			ip := IndexProgramme{
				Channel: p.Channel, Start: start.UTC(), Stop: stop.UTC(),
				Title: first(p.Title), SubTitle: first(p.SubTitle), Desc: cut(first(p.Desc), maxDescRunes),
				Categories: p.Categories, Live: p.Live != nil, New: p.New != nil,
			}
			if len(p.Icon) > 0 {
				ip.Image = p.Icon[0].Src
			}
			for _, e := range p.EpisodeNum {
				if label := episodeLabel(e.System, e.Value); label != "" {
					ip.Episode = label
					break
				}
			}
			ix.Programmes = append(ix.Programmes, ip)
		}
	}
	slices.SortStableFunc(ix.Programmes, func(a, b IndexProgramme) int {
		if c := xmltv.CompareNumbers(a.Channel, b.Channel); c != 0 {
			return c
		}
		return a.Start.Compare(b.Start)
	})
	return ix, nil
}

// episodeLabel is an episode-num as Plex shows it: xmltv_ns "S.E.P", zero
// based, reads S01E02 (E05 without a season); onscreen is taken as it is;
// any other system is no label.
func episodeLabel(system, value string) string {
	value = strings.TrimSpace(value)
	switch system {
	case "onscreen":
		return value
	case "xmltv_ns":
		parts := strings.Split(value, ".")
		if len(parts) < 2 {
			return ""
		}
		season, sok := nsNumber(parts[0])
		episode, eok := nsNumber(parts[1])
		switch {
		case sok && eok:
			return fmt.Sprintf("S%02dE%02d", season+1, episode+1)
		case eok:
			return fmt.Sprintf("E%02d", episode+1)
		}
	}
	return ""
}

// nsNumber is an xmltv_ns part's number, its "/total" dropped.
func nsNumber(s string) (int, bool) {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "/")
	n, err := strconv.Atoi(strings.TrimSpace(s))
	return n, err == nil && n >= 0
}

func first(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	return strings.TrimSpace(ss[0])
}

// cut is s at most max runes, its last one "…" when cut.
func cut(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
