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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/episode"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/version"
)

// planDelay holds a season's plan so a season being imported is planned
// once, after its episodes are probed.
const planDelay = 5 * time.Minute

// PublishPlan asks for mf's analysis: its season's (ep non-nil), held to the
// end of the current 5-minute bucket and deduplicated within it, or its own
// as a movie, at once.
func PublishPlan(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, ep *catalogv1alpha1.Episode, now time.Time) error {
	task := schema.SegmentsPlanTask{Namespace: mf.Namespace}
	var key, id string
	var opts []events.PublishOption
	if ep != nil {
		task.Series, task.Season = ep.Spec.SeriesRef, ep.Spec.SeasonNumber
		key = seasonKey(mf.Namespace, task.Series, task.Season)
		bucket := now.Truncate(planDelay)
		id = "segments-plan-" + key + "-" + strconv.FormatInt(bucket.Unix(), 10)
		opts = append(opts, events.WithScheduleAt(bucket.Add(planDelay)))
	} else {
		task.Movie = mf.Name
		key = mf.Namespace + "/" + mf.Name
		id = events.MsgIDForObject(string(mf.UID), 0,
			"segments-plan-"+mf.Status.ProbeHash+"-v"+strconv.Itoa(int(segments.AnalyzerVersion)))
	}
	return publish(ctx, bus, events.WorkSegmentsPlanSubject(key), "catalog.SegmentsPlanTask", key, id, now, task, opts...)
}

func seasonKey(ns, series string, season int32) string {
	return fmt.Sprintf("%s/%s-s%02d", ns, series, season)
}

func publish(ctx context.Context, bus events.Publisher, subject, typ, key, id string, now time.Time, p schema.Payload, opts ...events.PublishOption) error {
	name, data, err := schema.Encode(p)
	if err != nil {
		return err
	}
	env := &events.Envelope{ID: id, Type: typ, Schema: name, Source: "catalogarr@" + version.String(), Key: key, Time: now, Data: data}
	if _, err := bus.Publish(ctx, subject, env, append(opts, events.WithMsgID(id))...); err != nil {
		return fmt.Errorf("segmenting: publish %s: %w", key, err)
	}
	return nil
}

// Planner is the catalogarr-segments-plan durable's handler: it turns a plan
// into one AnalyzeTask. It reads through the controller manager's cache,
// whose field indexes find a season's episodes and their files.
type Planner struct {
	Reader client.Reader
	Bus    events.Publisher
	Clock  func() time.Time
}

// Handle implements events.Handler.
func (p *Planner) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var task schema.SegmentsPlanTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable plan", err)
	}
	now := time.Now()
	if p.Clock != nil {
		now = p.Clock()
	}
	var files []*catalogv1alpha1.MediaFile
	kind, key, anime := "movie", task.Namespace+"/"+task.Movie, false
	if task.Movie != "" {
		var mf catalogv1alpha1.MediaFile
		if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Movie}, &mf); err != nil {
			return client.IgnoreNotFound(err)
		}
		files = []*catalogv1alpha1.MediaFile{&mf}
	} else {
		kind, key = "episode", seasonKey(task.Namespace, task.Series, task.Season)
		var err error
		if files, anime, err = p.season(ctx, task); err != nil {
			return err
		}
	}
	out := schema.AnalyzeTask{Namespace: task.Namespace, Key: key, Kind: kind}
	due := sha256.New()
	for _, mf := range files {
		if !analyzable(mf) {
			continue
		}
		f := schema.AnalyzeFile{
			MediaFile: mf.Name, UID: string(mf.UID), Path: mf.Spec.Path, ProbeHash: mf.Status.ProbeHash,
			DurationMs: mf.Status.MediaInfo.RuntimeMillis, Chapters: mf.Status.MediaInfo.ChapterList,
			Due: segments.Due(mf, now), Anime: anime,
		}
		if f.Due {
			due.Write([]byte(f.ProbeHash))
		}
		out.Files = append(out.Files, f)
	}
	if !anyDue(out.Files) {
		return nil
	}
	id := "segments-analyze-" + key + "-" + hex.EncodeToString(due.Sum(nil))[:16]
	return publish(ctx, p.Bus, events.WorkSegmentsAnalyzeSubject(key), "catalog.AnalyzeTask", key, id, now, out)
}

// season is the season's files in episode order, each once, and whether the
// series is anime.
func (p *Planner) season(ctx context.Context, task schema.SegmentsPlanTask) ([]*catalogv1alpha1.MediaFile, bool, error) {
	var sr catalogv1alpha1.Series
	if err := p.Reader.Get(ctx, client.ObjectKey{Namespace: task.Namespace, Name: task.Series}, &sr); err != nil {
		return nil, false, client.IgnoreNotFound(err)
	}
	var eps catalogv1alpha1.EpisodeList
	if err := p.Reader.List(ctx, &eps, client.InNamespace(task.Namespace),
		client.MatchingFields{series.EpisodeBySeriesRefIndex: task.Series}); err != nil {
		return nil, false, fmt.Errorf("segmenting: episodes of %s: %w", task.Series, err)
	}
	sort.Slice(eps.Items, func(i, j int) bool { return eps.Items[i].Spec.EpisodeNumber < eps.Items[j].Spec.EpisodeNumber })
	var out []*catalogv1alpha1.MediaFile
	seen := map[string]bool{}
	for _, ep := range eps.Items {
		if ep.Spec.SeasonNumber != task.Season {
			continue
		}
		var mfs catalogv1alpha1.MediaFileList
		if err := p.Reader.List(ctx, &mfs, client.InNamespace(task.Namespace),
			client.MatchingFields{episode.MediaFileByEpisodeIndex: ep.Name}); err != nil {
			return nil, false, fmt.Errorf("segmenting: files of %s: %w", ep.Name, err)
		}
		for i := range mfs.Items {
			if mf := &mfs.Items[i]; !seen[mf.Name] {
				seen[mf.Name] = true
				out = append(out, mf)
			}
		}
	}
	return out, sr.Spec.SeriesType == catalogv1alpha1.SeriesTypeAnime, nil
}

// analyzable is a probed file whose file the reconciler has not found
// missing.
func analyzable(mf *catalogv1alpha1.MediaFile) bool {
	if mf.Status.MediaInfo == nil || mf.Status.ProbeHash == "" || mf.Status.MediaInfo.RuntimeMillis <= 0 {
		return false
	}
	c := meta.FindStatusCondition(mf.Status.Conditions, catalogv1alpha1.MediaFileConditionReady)
	return c == nil || c.Reason != "FileMissing"
}

func anyDue(files []schema.AnalyzeFile) bool {
	for _, f := range files {
		if f.Due {
			return true
		}
	}
	return false
}
