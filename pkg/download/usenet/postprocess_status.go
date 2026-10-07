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

package usenet

import (
	"fmt"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
)

// postProcessMessageLocked is status.message while the job post-processes.
// par2 and the unpack move no downloaded bytes, so progressPercent,
// lastProgressAt and the download rate all stand still for as long as they
// run -- 42 minutes for a 20 GB set over NFS on 2026-10-07, which read as a
// stall. The message says what is running and for how long, and for the
// unpack how much it has written. It is "" outside those stages. j.mu must
// be held.
func (j *job) postProcessMessageLocked(now time.Time) string {
	var elapsed time.Duration
	if !j.stageStarted.IsZero() {
		elapsed = now.Sub(j.stageStarted).Round(time.Second)
	}
	switch j.stage {
	case downloadv1alpha1.DownloadStageRepairing:
		return "verifying and repairing with par2" + forDuration(elapsed)
	case downloadv1alpha1.DownloadStageExtracting:
		written := j.unpack.Written()
		msg := "extracting: " + decimalBytes(written) + " written"
		if j.archiveBytes > 0 {
			msg += " of about " + decimalBytes(j.archiveBytes)
		}
		msg += forDuration(elapsed)
		if s := int64(elapsed / time.Second); s > 0 {
			msg += ", " + decimalBytes(written/s) + "/s"
		}
		return msg
	case downloadv1alpha1.DownloadStagePublishing:
		return "publishing" + forDuration(elapsed)
	default:
		return ""
	}
}

// forDuration is " for <d>", or "" when the stage's start is not known (a
// job re-attached from its manifest records no stage start).
func forDuration(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return " for " + d.String()
}

// decimalBytes renders n in decimal units, as the transfer sizes in status
// are read: "20.2 GB", "15.4 MB".
func decimalBytes(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1f MB", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.1f kB", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// archiveBytes is the NZB's size of every archive volume, the set the unpack
// reads: the articles' wire size, so a little over what lands on disk.
func archiveBytes(files []nzbFile) int64 {
	var n int64
	for _, f := range files {
		if f.Kind == kindArchive {
			n += f.Bytes
		}
	}
	return n
}
