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
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Publish asks the marker worker to fetch mf's segments.
func Publish(ctx context.Context, bus events.Publisher, mf *catalogv1alpha1.MediaFile, now time.Time) error {
	name, data, err := schema.Encode(schema.MarkersTask{MediaFile: mf.Name})
	if err != nil {
		return err
	}
	mediaKey := events.MediaKey("mediafile", mf.Namespace, mf.Name)
	env := &events.Envelope{
		ID:     MsgID(mf),
		Type:   "catalog.MarkersTask",
		Schema: name,
		Source: "catalogarr@" + version.String(),
		Key:    mf.Namespace + "/" + mf.Name,
		Time:   now,
		Data:   data,
	}
	if _, err := bus.Publish(ctx, events.WorkMarkersSubject(mediaKey), env); err != nil {
		return fmt.Errorf("markers: publish for %s/%s: %w", mf.Namespace, mf.Name, err)
	}
	return nil
}
