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

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Probe record states (ProbeRecord.State).
const (
	ProbeRequested = "requested"
	ProbeProbed    = "probed"
	ProbeFailed    = "failed"
)

// Probe record sources (ProbeRecord.Source): the import domain's probe
// worker answering a request, or an importer seeding the probe it ran.
const (
	ProbeSourceProbe  = "probe"
	ProbeSourceImport = "import"
)

// MaxProbeFailure bounds ProbeRecord.Failure, in bytes: every record's
// bound (loop spec 2026-10-06 §4.5).
const MaxProbeFailure = MaxRecordFailure

// ProbeTask asks the import domain to probe one MediaFile's file. Subject
// clustarr.work.probe.file.<high|low>.<mediaKey> (events.WorkProbeSubject),
// Msg-Id events.MsgIDForProbe, consumed by importarr-probe-high and
// importarr-probe-low (spec 2026-10-06 §6.5.1). catalogarr's MediaFile
// reconciler publishes it after writing the matching requested record
// (pkg/probestore); the answer goes to that record, never to the apiserver.
type ProbeTask struct {
	// MediaFile names the MediaFile, UID included: records are keyed by it.
	MediaFile Ref `json:"mediaFile"`
	// Path is the file to probe: spec.path, or a transcode swap's target.
	Path string `json:"path"`
	// ProbeHash is mediainfo.ProbeHash of the stat the reconciler saw.
	ProbeHash string `json:"probeHash"`
	// ProbeVersion is the lowest probe version the reconciler accepts.
	ProbeVersion int32 `json:"probeVersion"`
	// Seq is the requested record's sequence. An answer for an older one is
	// dropped.
	Seq int64 `json:"seq"`
	// Lane is "high" or "low".
	Lane string `json:"lane"`
}

// Schema implements Payload.
func (ProbeTask) Schema() string { return "importarr.ProbeTask.v1" }

// ProbeRecord is clustarr-probes' value for one MediaFile UID: a request, then
// its answer, every write a compare-and-swap (pkg/probestore). It is KV state,
// never a message on a stream. An undecodable value reads as no record.
type ProbeRecord struct {
	// RecordHeader carries MediaFile, Seq, State, RequestedAt, Failure and
	// Transient under the keys W4.3 gave them (TestProbeRecordWireFormatIsUnchanged).
	// Failure is the probe's error, at most MaxProbeFailure bytes; Transient
	// is a failure worth retrying soon: a timeout inside the grace, an
	// incomplete probe, a file that changed while it was read.
	RecordHeader
	Path      string `json:"path"`
	ProbeHash string `json:"probeHash"`
	// ProbeVersion is the version of the probe that produced the answer.
	ProbeVersion int32 `json:"probeVersion,omitempty"`
	// RequestedVersion is the ProbeTask.ProbeVersion the answer was for; 0
	// for an importer's seed, which answers no request.
	RequestedVersion int32               `json:"requestedVersion,omitempty"`
	Lane             string              `json:"lane,omitempty"`
	ProbedAt         time.Time           `json:"probedAt,omitzero"`
	MediaInfo        *commonv1.MediaInfo `json:"mediaInfo,omitempty"`
	// Abandoned is a probe that outlived its deadline and the grace after it
	// (mediainfo.ErrProbeAbandoned); never Transient.
	Abandoned bool `json:"abandoned,omitempty"`
	// AbandonedCount counts consecutive abandoned probes of this path and
	// hash; Request and Answer carry it forward (probestore's Carry).
	AbandonedCount int32  `json:"abandonedCount,omitempty"`
	Source         string `json:"source,omitempty"`
	// Prober is the pod that answered.
	Prober string `json:"prober,omitempty"`
}

// Schema implements Payload, so the payload guards in schematest cover the
// record; it is never published. It shadows the promoted RecordHeader.Schema
// field, which the probe never sets; pkg/records reaches the field through
// Header().
func (ProbeRecord) Schema() string { return "importarr.ProbeRecord.v1" }
