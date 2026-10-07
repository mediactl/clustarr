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

package agent

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/mediainfo"
)

// newScanWorker builds the work.importarr.scan handler with o's sample size
// floor. rescan.NewWorker already defaults the floor, so the assignment
// matters exactly when o carries a different one -- a non-default
// --sample-max-bytes, or 0 to disable the rule. It also gets the manager's
// API reader, which a scan's rename pass re-reads each MediaFile through
// (rescan.Worker.APIReader), and the import domain's one prober.
func newScanWorker(c client.Client, api client.Reader, bus events.Bus, prober mediainfo.Prober, o Options) *rescan.Worker {
	w := rescan.NewWorker(c, bus, prober)
	w.APIReader = api
	w.SampleMaxBytes = o.SampleMaxBytes
	return w
}

// newListWorker builds the work.importarr.list handler with o's Trakt and
// Plex base URLs (see Options.TraktBaseURL) and the manager's API reader,
// which a Series is read through with its managedFields before a sync
// applies it (importlist.Worker.APIReader).
func newListWorker(c client.Client, api client.Reader, bus events.Bus, o Options) *importlist.Worker {
	w := importlist.NewWorker(c, bus)
	w.APIReader = api
	w.TraktBaseURL = o.TraktBaseURL
	w.PlexBaseURL = o.PlexBaseURL
	return w
}

// newImportWorker builds the work.importarr.fileimport handler with o's
// sample size floor, for the reason [newScanWorker] gives, and the
// manager's API reader, which a movie's existing files are read through
// (fileimport.Worker.APIReader), and the import domain's one prober.
func newImportWorker(c client.Client, api client.Reader, bus events.Bus, prober mediainfo.Prober, o Options) *fileimport.Worker {
	w := fileimport.NewWorker(c, bus, prober)
	w.APIReader = api
	w.SampleMaxBytes = o.SampleMaxBytes
	return w
}
