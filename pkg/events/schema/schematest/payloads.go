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

// Package schematest is test support for every package whose tests must
// range over the payloads the bus carries: pkg/events/schema's own
// uniqueness and no-float guards, and app/catalog/history's guard that the
// DLQ projector can resolve every payload a durable consumer may
// dead-letter. It lives outside a _test.go file so both can import one list.
package schematest

import "github.com/mediactl/clustarr/pkg/events/schema"

// Payloads is every versioned payload the bus carries. A new payload is
// added here, and every guard that ranges over this list sees it the day it
// appears -- which is the point: ArtworkFetchTask and RenderOverlayTask were
// in schema's own list and absent from the DLQ projector's resolvers, so
// their dead letters named no object.
func Payloads() []schema.Payload {
	return []schema.Payload{
		schema.ItemEvent{},
		schema.ReleaseEvent{},
		schema.MediaFileEvent{},
		schema.ImportListSynced{},
		schema.SearchTask{},
		schema.GrabTask{},
		schema.ImportTask{},
		schema.MetadataTask{},
		schema.WantedScan{},
		schema.MetadataRequest{},
		schema.MetadataResponse{},
		schema.Release{},
		schema.IndexerEvent{},
		schema.RssTask{},
		schema.SearchRequest{},
		schema.SearchResponse{},
		schema.DownloadRequest{},
		schema.DownloadResponse{},
		schema.QueryRequest{},
		schema.QueryResponse{},
		schema.DownloadEvent{},
		schema.DownloadProgress{},
		schema.JobEvent{},
		schema.TranscodeProgress{},
		schema.SubtitleEvent{},
		schema.FetchTask{},
		schema.ScanTask{},
		schema.ListTask{},
		schema.ArtworkFetchTask{},
		schema.RenderOverlayTask{},
	}
}
