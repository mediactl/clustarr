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
)

// parseDoviRecord extracts the "DOVI configuration record" side data
// from a video stream's side data list. go-ffprobe.v2 v2.3.1 has no
// typed struct for this side_data_type, so it decodes as
// *ffprobe.SideDataUnknown (a map[string]interface{} in disguise);
// Tags(*unknown) recovers the GetInt/GetString accessors.
func parseDoviRecord(list ffprobe.SideDataList) *DoviRecord {
	data, err := list.FindSideData("DOVI configuration record")
	if err != nil {
		return nil
	}
	unknown, ok := data.(*ffprobe.SideDataUnknown)
	if !ok {
		return nil
	}
	tags := ffprobe.Tags(*unknown)
	geti := func(k string) int32 {
		v, _ := tags.GetInt(k)
		return int32(v)
	}
	compression, _ := tags.GetString("dv_md_compression")
	return &DoviRecord{
		VersionMajor:            geti("dv_version_major"),
		VersionMinor:            geti("dv_version_minor"),
		Profile:                 geti("dv_profile"),
		Level:                   geti("dv_level"),
		RPUPresent:              geti("rpu_present_flag") != 0,
		ELPresent:               geti("el_present_flag") != 0,
		BLPresent:               geti("bl_present_flag") != 0,
		BLSignalCompatibilityID: geti("dv_bl_signal_compatibility_id"),
		MDCompression:           compression,
	}
}
