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

package schema

import "time"

// MaxRecordFailure bounds RecordHeader.Failure, in bytes (loop spec §4.5).
const MaxRecordFailure = 1024

// RecordHeader is embedded, flat, in every records-bucket value (loop spec
// 2026-10-06 §4.5; pkg/records). Its keys are the probe record's own, so the
// probe record keeps its JSON: no existing key is renamed, and every key the
// header adds is omitted when unset.
type RecordHeader struct {
	// Schema is "records.<remediation>.v1"; absent on probe records.
	Schema string `json:"schema,omitempty"`
	// MediaFile is the file whose status incorporates the record.
	MediaFile Ref `json:"mediaFile"`
	// Sub is the unescaped second key token: a subtitle's langKey.
	Sub string `json:"sub,omitempty"`
	// Seq is the loop's sequence for the request (records.NextSeq).
	Seq int64 `json:"seq"`
	// State is one of records.State*; the probe keeps requested, probed and
	// failed.
	State string `json:"state"`
	// RequestedAt keeps the probe record's omitzero: a seed answers no
	// request and carries no time.
	RequestedAt   time.Time  `json:"requestedAt,omitzero"`
	ClaimedAt     *time.Time `json:"claimedAt,omitempty"`
	DeferredUntil *time.Time `json:"deferredUntil,omitempty"`
	AnsweredAt    *time.Time `json:"answeredAt,omitempty"`
	// Writer is the pod name of the last worker write, WriterVersion its
	// version.String(), for skew.
	Writer        string `json:"writer,omitempty"`
	WriterVersion string `json:"writerVersion,omitempty"`
	// Failure is at most MaxRecordFailure bytes.
	Failure   string `json:"failure,omitempty"`
	Transient bool   `json:"transient,omitempty"`
	// Failures counts consecutive failures for the same inputs.
	Failures int32 `json:"failures,omitempty"`
}

// Header gives pkg/records the header of any record that embeds it.
func (h *RecordHeader) Header() *RecordHeader { return h }
