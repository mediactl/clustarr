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

package segmentplan

import (
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
)

// PlanMessage is the plan PublishPlan sends for mf: its season's (ep
// non-nil), scheduled at the end of the current 5-minute bucket (at) and
// deduplicated within it, or its own as a movie (at zero, at once). The
// remediation loop's markers planner publishes it as an effect.
func PlanMessage(mf *catalogv1alpha1.MediaFile, ep *catalogv1alpha1.Episode, now time.Time) (subject, msgID string, env *events.Envelope, at time.Time, err error) {
	task := schema.SegmentsPlanTask{Namespace: mf.Namespace}
	var key string
	if ep != nil {
		task.Series, task.Season = ep.Spec.SeriesRef, ep.Spec.SeasonNumber
		key = seasonKey(mf.Namespace, task.Series, task.Season)
		bucket := now.Truncate(planDelay)
		msgID = "segments-plan-" + key + "-" + strconv.FormatInt(bucket.Unix(), 10)
		at = bucket.Add(planDelay)
	} else {
		task.Movie = mf.Name
		key = mf.Namespace + "/" + mf.Name
		msgID = events.MsgIDForObject(string(mf.UID), 0,
			"segments-plan-"+mf.Status.ProbeHash+"-v"+strconv.Itoa(int(segments.AnalyzerVersion)))
	}
	env, err = envelope("catalog.SegmentsPlanTask", key, msgID, now, task)
	if err != nil {
		return "", "", nil, time.Time{}, err
	}
	return events.WorkSegmentsPlanSubject(key), msgID, env, at, nil
}
