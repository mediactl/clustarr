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

import (
	"encoding/json"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/transcode/task"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/version"
)

// effectsOf renders d's owed writes in §3.8's one order: the record request,
// the task publish, the ledger's confirmation or release, the history, and
// the R9 observation.
func (a *Adapter) effectsOf(v *remediation.View, in In, d transcodeplan.Decision) []remediation.Effect {
	var out []remediation.Effect
	mf := v.File
	uid := string(mf.UID)
	if d.Request != nil {
		if b, err := json.Marshal(d.Request); err == nil {
			out = append(out, remediation.RecordWrite{
				Bucket: events.BucketTranscodes, Key: events.RecordKey(uid), Revision: d.RequestRev, Value: b,
				Remediation: task.RecordSpec.Remediation, Lane: d.Request.Inputs.Class,
			})
		}
	}
	if d.Withdraw != 0 {
		out = append(out, remediation.Withdraw{UID: uid, Seq: d.Withdraw})
	}
	if d.Publish != nil {
		if pub, ok := a.publish(v, in, d); ok {
			out = append(out, pub)
		}
	}
	if in.Took {
		if d.Grant == transcodeplan.GrantConfirm {
			out = append(out, remediation.LedgerConfirm{UID: mf.UID, Seq: d.ConfirmSeq})
		} else {
			out = append(out, remediation.LedgerRelease{UID: mf.UID})
		}
	}
	for _, h := range d.History {
		out = append(out, history(mf, d.Block, h, v.Now.Time))
	}
	if t := d.Observe; t != nil {
		obs := *t
		out = append(out, remediation.Observation{Observe: func() { observe(obs) }})
	}
	if d.Dispatched != "" && d.Block != nil {
		class, outcome := string(d.Block.Class), d.Dispatched
		out = append(out, remediation.Observation{Observe: func() {
			metrics.TranscodeDispatches.WithLabelValues(class, outcome).Inc()
		}})
	}
	return out
}

// publish is the task publish of d's dispatch, on the pool subject of the
// block's profile and class, under transcode/<mfUID>/<seq>.
func (a *Adapter) publish(v *remediation.View, in In, d transcodeplan.Decision) (remediation.Publish, bool) {
	t := d.Publish
	profileUID := types.UID("")
	for i := range in.Profiles {
		if in.Profiles[i].Name == t.Profile.Name {
			profileUID = in.Profiles[i].UID
		}
	}
	if profileUID == "" {
		return remediation.Publish{}, false
	}
	name, data, err := schema.Encode(*t)
	if err != nil {
		return remediation.Publish{}, false
	}
	id := events.MsgIDForTranscodeTask(t.File.UID, t.Seq)
	outcome := "published"
	if d.Republish {
		outcome = "republished"
	}
	uid, seq, class := v.File.UID, t.Seq, t.Class
	return remediation.Publish{
		Subject: events.WorkTranscodeTaskSubject(string(profileUID), t.Class, t.File.UID),
		MsgID:   id, ExpectStream: events.StreamWorkTranscode,
		Envelope: &events.Envelope{
			ID: id, Type: "transcode.Task", Schema: name, Source: "manager@" + version.String(),
			Key: t.File.Key(), Time: v.Now.Time, Data: data,
		},
		Result: func(err error) {
			if a.o.Ledger != nil {
				a.o.Ledger.PublishFailed(uid, seq, err)
			}
			if err == nil {
				metrics.TranscodeDispatches.WithLabelValues(class, outcome).Inc()
			}
		},
	}, true
}

// history is the clustarr.evt.transcode.job.<action>.<mfUID> event of one
// transition, Msg-Id <mfUID>/transcode/<seq>/<action> (§5.16).
func history(mf *catalogv1alpha1.MediaFile, b *catalogv1alpha1.TranscodeState, h transcodeplan.HistoryEvent, now time.Time) remediation.History {
	uid := string(mf.UID)
	ev := schema.JobEvent{
		File: schema.Ref{Namespace: mf.Namespace, Name: mf.Name, UID: uid}, Seq: h.Seq, Action: h.Action,
		InputPath: mf.Spec.Path, InputBytes: mf.Spec.SizeBytes, Reason: h.Reason, At: now,
	}
	if b != nil {
		if b.Profile != "" {
			ev.ProfileRef = &schema.Ref{Name: b.Profile}
		}
		if b.Plan != nil {
			ev.Mode, ev.Encoder = string(b.Plan.Mode), b.Plan.Encoder
		}
		if r := b.Result; r != nil {
			ev.OutputPath, ev.OutputBytes = r.OutputPath, r.OutputSizeBytes
			if mf.Spec.SizeBytes > 0 && r.OutputSizeBytes > 0 && r.OutputSizeBytes < mf.Spec.SizeBytes {
				ev.SavedPercent = int32(100 - r.OutputSizeBytes*100/mf.Spec.SizeBytes) //nolint:gosec // 0..100
			}
		}
		if b.StartedAt != nil && b.FinishedAt != nil {
			ev.DurationSeconds = int64(b.FinishedAt.Sub(b.StartedAt.Time).Seconds())
		}
	}
	id := uid + "/transcode/" + strconv.FormatInt(h.Seq, 10) + "/" + h.Action
	name, data, _ := schema.Encode(ev) // strings, ints and times: Encode cannot fail
	return remediation.History{
		Subject: events.TranscodeJobSubject(h.Action, uid), MsgID: id,
		Envelope: &events.Envelope{
			ID: id, Type: "transcode.JobEvent", Schema: name, Source: "manager@" + version.String(),
			Key: mf.Namespace + "/" + mf.Name, Time: now, Data: data,
		},
	}
}

// observe records the R9 metrics of a dispatch that just became terminal
// (ruling R9: the manager owns them; the pool's pods are scraped by no one).
// Labels are the slot class, the resolution class and the outcome only.
func observe(t transcodeplan.Terminal) {
	outcome := "failed"
	if t.Succeeded {
		outcome = "succeeded"
	}
	metrics.TranscodeDuration.WithLabelValues(t.Class, t.Resolution, outcome).Observe(t.Wall.Seconds())
	if !t.Succeeded {
		return
	}
	if t.Runtime > 0 && t.Wall > 0 {
		metrics.TranscodeSpeedRatio.WithLabelValues(t.Class).Set(float64(t.Runtime) / float64(t.Wall))
	}
	if t.SizeRatio > 0 {
		metrics.TranscodeSizeRatio.WithLabelValues(t.Class, t.Resolution).Observe(t.SizeRatio)
	}
}
