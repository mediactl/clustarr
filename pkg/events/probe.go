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

package events

import (
	"fmt"
	"time"
)

// The MediaFile probe queue (spec 2026-10-06 §6.5.1). catalogarr's MediaFile
// reconciler publishes a ProbeTask for each probe it needs; the import
// domain's probe worker answers it into BucketProbes, which the reconciler
// reads (pkg/probestore).
const (
	// StreamWorkProbe holds probe tasks: WorkQueue, file-backed and Durable,
	// so ForSingleNode keeps it out of the 64 MiB memory budget whose
	// discard-oldest would drop other services' work (the 2026-10-01
	// gotcha), and DiscardNew, so a full queue refuses the publish and the
	// reconcile retries, rather than dropping a queued probe.
	StreamWorkProbe = "CLUSTARR_WORK_PROBE"
	FilterWorkProbe = "clustarr.work.probe.>"

	// FilterProbeHigh is a new file's, changed bytes' or a transcode swap's
	// probe; FilterProbeLow a ProbeVersion re-probe of unchanged bytes, which
	// never queues ahead of a new import's.
	FilterProbeHigh = "clustarr.work.probe.file.high.>"
	FilterProbeLow  = "clustarr.work.probe.file.low.>"

	ConsumerImportProbeHigh = "importarr-probe-high"
	ConsumerImportProbeLow  = "importarr-probe-low"

	// BucketProbes holds one probe record per MediaFile UID.
	BucketProbes = "clustarr-probes"
)

// probeMaxBytes bounds StreamWorkProbe. A task is about 0.8 KB, so this holds
// some 80,000; a re-probe of the whole 11,958-file library is about 10 MiB.
const probeMaxBytes = 64 * MiB

// probeRecordTTL retires a record nobody has rewritten for a week. It must
// outlive the longest outstanding request (the low lane's 72 h,
// app/catalog/controller/mediafile's probeRequestTimeoutLow), or a request
// would vanish before it times out.
const probeRecordTTL = 7 * 24 * time.Hour

// probeSlots is one pod's handlers per probe lane: two lanes of four give a
// pod the eight concurrent probes mediafile.MaxConcurrentReconciles ran.
const probeSlots = 4

// WorkProbeSubject builds clustarr.work.probe.file.<high|low>.<mediaKey>.
// The probe queue has two lanes: any priority but PriorityHigh is low.
func WorkProbeSubject(p Priority, mediaKey string) string {
	lane := PriorityLow
	if p == PriorityHigh {
		lane = PriorityHigh
	}
	return "clustarr.work.probe.file." + string(lane) + "." + tok(mediaKey)
}

// ProbeKey is a MediaFile's key in BucketProbes: its UID through KVKeyToken.
func ProbeKey(uid string) string { return KVKeyToken(uid) }

// MsgIDForProbe builds a probe task's deduplication ID,
// "probe/<uid>/<probeHash>/v<version>/<seq>". seq is the requested record's
// sequence (pkg/probestore): a republish of one request is absorbed by the
// stream's one-hour window, and the next request never is.
func MsgIDForProbe(uid, probeHash string, version int32, seq int64) string {
	return fmt.Sprintf("probe/%s/%s/v%d/%d", uid, probeHash, version, seq)
}

func probeStream() StreamSpec {
	return StreamSpec{
		Name:        StreamWorkProbe,
		Description: "MediaFile probe tasks catalogarr asked the import domain for.",
		Subjects:    []string{FilterWorkProbe},
		Retention:   RetentionWorkQueue,
		Storage:     StorageFile,
		Discard:     DiscardNew,
		MaxBytes:    probeMaxBytes,
		Duplicates:  time.Hour,
		Replicas:    3,
		Durable:     true,
	}
}

// probeConsumers are the import domain's two lanes. AckWait 60 s is the
// import worker's terminationGracePeriodSeconds floor (the importarr block in
// defaultConsumers). BackOff[0] is the first delivery's deadline, which
// probe.TaskTimeout (45 s) fits inside, so there is no Heartbeat. Four
// slots per lane give one pod the eight concurrent probes
// mediafile.MaxConcurrentReconciles ran; MaxAckPending is Slots x
// AutoscaleReplicaCeiling, so every autoscaled import pod fills its slots
// (spec 2026-10-06 §6.5.1, §9.2).
func probeConsumers() []ConsumerSpec {
	c := func(name, filter, desc string) ConsumerSpec {
		return ConsumerSpec{
			Name: name, Stream: StreamWorkProbe, Description: desc,
			Filters: []string{filter},
			AckWait: 60 * time.Second, MaxDeliver: 4,
			BackOff:       []time.Duration{60 * time.Second, 5 * time.Minute, 30 * time.Minute},
			Slots:         probeSlots,
			MaxAckPending: probeSlots * AutoscaleReplicaCeiling,
		}
	}
	return []ConsumerSpec{
		c(ConsumerImportProbeHigh, FilterProbeHigh, "Probes of new and changed files."),
		c(ConsumerImportProbeLow, FilterProbeLow, "ProbeVersion re-probes of unchanged files."),
	}
}

func probeBucket() BucketSpec {
	return BucketSpec{
		Name: BucketProbes, Description: "One probe record per MediaFile UID.",
		TTL: probeRecordTTL, History: 1, Storage: StorageFile, Replicas: 3,
		LimitMarkerTTL: 5 * time.Minute, Durable: true,
		// A records bucket (loop spec 2026-10-06 §4.3): Validate holds it to
		// the records rule.
		Records: true, MaxBytes: ProbesMaxBytes, MaxValueSize: ProbesMaxValueSize,
	}
}
