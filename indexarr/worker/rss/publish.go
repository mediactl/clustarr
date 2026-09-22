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

package rss

import (
	"context"
	"fmt"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// releaseEnvelopeType is the Clustarr-Type header on a firehose message.
const releaseEnvelopeType = "index.Release"

// PublishReleases publishes each release to the firehose. It is exported so
// the e2e suite can drive it directly without a full reconcile.
//
// published counts the messages the broker STORED: a receipt with Duplicate
// set means the stream's 2h deduplication window already held this
// indexer+guid, so nothing new reached the matcher. This is a bus-level
// figure and is NOT Indexer.status.lastRssNewCount -- see the Worker, which
// takes that count from relindex.Upsert instead.
func PublishReleases(
	ctx context.Context,
	bus events.Bus,
	ns, indexerName string,
	rels []schema.Release,
) (published int, err error) {
	if ns == "" || indexerName == "" {
		// Refuse rather than publish an uncuttable key. The matcher would
		// Discard every one of these to the DLQ without a retry, and nothing
		// downstream would report it.
		return 0, fmt.Errorf(
			"rss: publish releases: namespace=%q indexer=%q: both are required to build the envelope key",
			ns, indexerName)
	}
	// Built explicitly, once, in its own variable. A media-key-style token is
	// NOT an envelope key: it has been through events' subject tokeniser and
	// has no slash left to cut on. Conflating the two dead-lettered every
	// metadata refresh in Phase C, silently, because the consumer discards on
	// a failed split.
	key := ns + "/" + indexerName

	for _, rel := range rels {
		schemaName, data, encErr := schema.Encode(rel)
		if encErr != nil {
			return published, fmt.Errorf("rss: encode %s/%s: %w", indexerName, rel.Info.GUID, encErr)
		}
		env := &events.Envelope{
			ID:     events.MsgIDForRelease(indexerName, rel.Info.GUID),
			Type:   releaseEnvelopeType,
			Schema: schemaName,
			Source: "indexarr@" + version.String(),
			Key:    key,
			Time:   rel.FetchedAt,
			Data:   data,
		}
		// The matcher calls tracing.Extract before Start so its span
		// continues indexarr's poll. Without this Inject the RSS leg of every
		// trace is orphaned.
		tracing.Inject(ctx, env)

		rcpt, pubErr := bus.Publish(ctx, subjectFor(rel, indexerName), env, events.WithMsgID(env.ID))
		if pubErr != nil {
			return published, fmt.Errorf("rss: publish %s/%s: %w", indexerName, rel.Info.GUID, pubErr)
		}
		if !rcpt.Duplicate {
			published++
		}
	}
	return published, nil
}

// subjectFor builds clustarr.rel.<protocol>.<indexerName>.<newznabTop>.
//
// newznabTop is the 1000-aligned PARENT category (newznab.CategoryID.Parent,
// which returns a custom id >= 100000 unchanged), because the subject exists
// so a future consumer can filter on "movies" without knowing that 2040 is
// Movies/HD. A release with no category publishes under 0, which
// clustarr.rel.> still matches.
func subjectFor(rel schema.Release, indexerName string) string {
	var top newznab.CategoryID
	if len(rel.Info.Categories) > 0 {
		top = newznab.CategoryID(rel.Info.Categories[0]).Parent()
	}
	return events.ReleaseSubject(string(rel.Info.Protocol), indexerName, int(top))
}
