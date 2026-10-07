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

// Package hdhr renders the HDHomeRun HTTP surface Plex's DVR setup reads
// (Live TV spec §2, §4.4), with the values Plex accepts from xTeVe.
package hdhr

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strings"
)

// Device is one emulated HDHomeRun.
type Device struct {
	FriendlyName string
	DeviceID     string
	BaseURL      string
	TunerCount   int
}

// LineupEntry is one lineup.json channel.
type LineupEntry struct {
	GuideNumber string `json:"GuideNumber"`
	GuideName   string `json:"GuideName"`
	URL         string `json:"URL"`
}

// BlockDeviceID is the device id of the block starting at start in provider
// namespace/name (Live TV spec §3.1): eight uppercase hex digits of a SHA-256
// of "namespace/name/start", so a recreated provider gives the same devices.
func BlockDeviceID(namespace, name, start string) string {
	sum := sha256.Sum256([]byte(namespace + "/" + name + "/" + start))
	return strings.ToUpper(hex.EncodeToString(sum[:4]))
}

type discover struct {
	FriendlyName    string `json:"FriendlyName"`
	Manufacturer    string `json:"Manufacturer"`
	ModelNumber     string `json:"ModelNumber"`
	FirmwareName    string `json:"FirmwareName"`
	FirmwareVersion string `json:"FirmwareVersion"`
	DeviceID        string `json:"DeviceID"`
	DeviceAuth      string `json:"DeviceAuth"`
	TunerCount      int    `json:"TunerCount"`
	BaseURL         string `json:"BaseURL"`
	LineupURL       string `json:"LineupURL"`
}

// Discover renders discover.json.
func Discover(d Device) []byte {
	return mustJSON(discover{
		FriendlyName: d.FriendlyName, Manufacturer: "Silicondust", ModelNumber: "HDTC-2US",
		FirmwareName: "hdhomeruntc_atsc", FirmwareVersion: "20150826", DeviceID: d.DeviceID,
		DeviceAuth: "clustarr", TunerCount: d.TunerCount, BaseURL: d.BaseURL, LineupURL: d.BaseURL + "/lineup.json",
	})
}

type lineupStatus struct {
	ScanInProgress int      `json:"ScanInProgress"`
	ScanPossible   int      `json:"ScanPossible"`
	Source         string   `json:"Source"`
	SourceList     []string `json:"SourceList"`
}

// LineupStatus renders lineup_status.json: never scanning, cable.
func LineupStatus() []byte {
	return mustJSON(lineupStatus{ScanPossible: 1, Source: "Cable", SourceList: []string{"Cable"}})
}

// Lineup renders lineup.json; an empty lineup is [], never null.
func Lineup(entries []LineupEntry) []byte {
	if entries == nil {
		entries = []LineupEntry{}
	}
	return mustJSON(entries)
}

// DeviceXML renders the UPnP device description.
func DeviceXML(d Device) []byte {
	esc := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	return fmt.Appendf(nil, `<?xml version="1.0" encoding="UTF-8"?>
<root xmlns="urn:schemas-upnp-org:device-1-0">
<specVersion><major>1</major><minor>0</minor></specVersion>
<URLBase>%s</URLBase>
<device>
<deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>
<friendlyName>%s</friendlyName>
<manufacturer>Silicondust</manufacturer>
<modelName>HDTC-2US</modelName>
<modelNumber>HDTC-2US</modelNumber>
<serialNumber>%s</serialNumber>
<UDN>uuid:%s</UDN>
</device>
</root>
`, esc(d.BaseURL), esc(d.FriendlyName), esc(d.DeviceID), esc(d.DeviceID))
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("hdhr: marshal %T: %v", v, err)) // plain structs of strings and ints cannot fail
	}
	return b
}
