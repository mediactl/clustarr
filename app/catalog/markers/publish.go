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

package markers

import (
	"context"
	"fmt"
	"strconv"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Publish asks the marker worker to fetch mf's segments.
func Publish(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, now time.Time) error {
	return publish(ctx, bus, mf, now, MsgID(mf))
}

// PublishAt asks again at at: a task deferred to a spent key's reset, under
// a message id of its own so the dedup window does not absorb it.
func PublishAt(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, now, at time.Time) error {
	return publish(ctx, bus, mf, now, MsgID(mf)+"-at-"+strconv.FormatInt(at.Unix(), 10), events.WithScheduleAt(at))
}

func publish(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, now time.Time, id string, opts ...events.PublishOption) error {
	name, data, err := schema.Encode(schema.MarkersTask{MediaFile: mf.Name})
	if err != nil {
		return err
	}
	mediaKey := events.MediaKey("mediafile", mf.Namespace, mf.Name)
	env := &events.Envelope{
		ID:     id,
		Type:   "catalog.MarkersTask",
		Schema: name,
		Source: "catalogarr@" + version.String(),
		Key:    mf.Namespace + "/" + mf.Name,
		Time:   now,
		Data:   data,
	}
	if _, err := bus.Publish(ctx, events.WorkMarkersSubject(mediaKey), env, opts...); err != nil {
		return fmt.Errorf("markers: publish for %s/%s: %w", mf.Namespace, mf.Name, err)
	}
	return nil
}
