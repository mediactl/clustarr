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

package transcode

import "errors"

// ErrDeviceUnavailable is an in-process encode whose GPU device could not
// be opened (no device mounted, a driver library missing): a fault of the
// node, not of the file, so the worker reports it as the class being
// unavailable rather than as a failed encode.
var ErrDeviceUnavailable = errors.New("transcode: the GPU device could not be opened")

// Measurement is what a pool pod measured of its own device: the tier its
// class encodes on there (Intel's QSV, else VAAPI) and the device's limits,
// NVDEC's decodable formats among them.
type Measurement struct {
	Tier   Tier
	Limits Limits
}
