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

package hdhr_test

import (
	"encoding/xml"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/iptv/hdhr"
)

func device() hdhr.Device {
	return hdhr.Device{FriendlyName: "Clustarr news", DeviceID: "12AB34CD", BaseURL: "http://10.96.0.5/livetv/media/news", TunerCount: 2}
}

func TestDiscover(t *testing.T) {
	assert.JSONEq(t, `{"FriendlyName":"Clustarr news","Manufacturer":"Silicondust","ModelNumber":"HDTC-2US",
		"FirmwareName":"hdhomeruntc_atsc","FirmwareVersion":"20150826","DeviceID":"12AB34CD","DeviceAuth":"clustarr",
		"TunerCount":2,"BaseURL":"http://10.96.0.5/livetv/media/news","LineupURL":"http://10.96.0.5/livetv/media/news/lineup.json"}`,
		string(hdhr.Discover(device())))
}

func TestLineupStatus(t *testing.T) {
	assert.JSONEq(t, `{"ScanInProgress":0,"ScanPossible":1,"Source":"Cable","SourceList":["Cable"]}`, string(hdhr.LineupStatus()))
}

func TestLineup(t *testing.T) {
	assert.JSONEq(t, `[{"GuideNumber":"1001","GuideName":"BBC One","URL":"http://10.96.0.5/livetv/media/news/stream/1001"}]`,
		string(hdhr.Lineup([]hdhr.LineupEntry{{GuideNumber: "1001", GuideName: "BBC One", URL: "http://10.96.0.5/livetv/media/news/stream/1001"}})))
	assert.JSONEq(t, `[]`, string(hdhr.Lineup(nil)), "an empty lineup is an empty array, never null")
}

func TestDeviceXMLNamesTheDevice(t *testing.T) {
	d := device()
	d.FriendlyName = "News & Sport"
	x := string(hdhr.DeviceXML(d))
	assert.Contains(t, x, `<URLBase>http://10.96.0.5/livetv/media/news</URLBase>`)
	assert.Contains(t, x, `<friendlyName>News &amp; Sport</friendlyName>`)
	assert.Contains(t, x, `<UDN>uuid:12AB34CD</UDN>`)
	var root struct {
		XMLName xml.Name
	}
	require.NoError(t, xml.Unmarshal([]byte(x), &root), "well-formed")
	assert.Equal(t, "root", root.XMLName.Local)
}

func TestABlocksDeviceIDIsStableAndDistinct(t *testing.T) {
	a := hdhr.BlockDeviceID("media", "news", "1000")
	assert.Regexp(t, `^[0-9A-F]{8}$`, a)
	assert.Equal(t, a, hdhr.BlockDeviceID("media", "news", "1000"), "a recreated provider gets the same devices")
	assert.NotEqual(t, a, hdhr.BlockDeviceID("media", "news", "1400"), "each block is its own device")
	assert.NotEqual(t, a, hdhr.BlockDeviceID("media", "sports", "1000"))
	assert.NotEqual(t, a, hdhr.BlockDeviceID("other", "news", "1000"))
}
