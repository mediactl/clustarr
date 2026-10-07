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
	"context"
	"fmt"
	"slices"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	captionagent "github.com/mediactl/clustarr/app/caption/agent"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	catalogdomain "github.com/mediactl/clustarr/app/catalog/agent/catalog"
	eventsdomain "github.com/mediactl/clustarr/app/catalog/agent/events"
	metadatadomain "github.com/mediactl/clustarr/app/catalog/agent/metadata"
	torrentagent "github.com/mediactl/clustarr/app/grab/agent/torrent"
	usenetagent "github.com/mediactl/clustarr/app/grab/agent/usenet"
	importagent "github.com/mediactl/clustarr/app/import/agent"
	indexeragent "github.com/mediactl/clustarr/app/indexer/agent"
	"github.com/mediactl/clustarr/pkg/events"
)

// registerFunc is one domain's registration (§4.2.1).
type registerFunc func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error)

var domainNames = []string{"catalog", "events", "metadata", "import", "index", "caption", "torrent-engine", "usenet-engine"}

// Domains is every --domain value, in §3.5.1's order.
func Domains() []string { return slices.Clone(domainNames) }

// domains maps each domain name to its registration package's Register.
var domains = map[string]registerFunc{
	"catalog": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return catalogdomain.Register(ctx, mgr, bus, catalogdomain.Options{Options: o.Options})
	},
	"events": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return eventsdomain.Register(ctx, mgr, bus, eventsdomain.Options{Options: o.Options})
	},
	"metadata": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return metadatadomain.Register(ctx, mgr, bus, metadatadomain.Options{Options: o.Options})
	},
	"import": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return importagent.Register(ctx, mgr, bus, importagent.Options{Options: o.Options, DataDir: o.DataDir,
			SampleMaxBytes: o.SampleMaxBytes, TraktBaseURL: o.TraktBaseURL, PlexBaseURL: o.PlexBaseURL})
	},
	"index": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return indexeragent.Register(ctx, mgr, bus, indexeragent.Options{Options: o.Options, IndexPath: o.IndexPath,
			IndexDSN: o.IndexDSN, FacadeBindAddress: o.FacadeBindAddress, FacadeAPIKeySecret: o.FacadeAPIKeySecret})
	},
	"caption": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		return captionagent.Register(ctx, mgr, bus, captionagent.Options{Options: o.Options, DataDir: o.DataDir})
	},
	"torrent-engine": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		client, err := engineClient(o.Engine)
		if err != nil {
			return catalogagent.Registration{}, err
		}
		return torrentagent.Register(ctx, mgr, bus, torrentagent.Options{Options: o.Options, Client: client, Engine: o.Engine,
			DataDir: o.DataDir, ScratchDir: o.ScratchDir})
	},
	"usenet-engine": func(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
		client, err := engineClient(o.Engine)
		if err != nil {
			return catalogagent.Registration{}, err
		}
		return usenetagent.Register(ctx, mgr, bus, usenetagent.Options{Options: o.Options, Client: client, Engine: o.Engine,
			DataDir: o.DataDir, ScratchDir: o.ScratchDir, PublishDir: o.PublishDir})
	},
}

// domainFlags names the domains each domain flag belongs to (§3.5.1).
var domainFlags = map[string][]string{
	"data-dir":              {"import", "caption", "torrent-engine", "usenet-engine"},
	"sample-max-bytes":      {"import"},
	"trakt-base-url":        {"import"},
	"plex-base-url":         {"import"},
	"index-path":            {"index"},
	"index-dsn":             {"index"},
	"facade-bind-address":   {"index"},
	"facade-api-key-secret": {"index"},
	"download-client":       {"torrent-engine"},
	"engine":                {"torrent-engine", "usenet-engine"},
	"scratch-dir":           {"torrent-engine", "usenet-engine"},
	"publish-dir":           {"usenet-engine"},
}

// engineClient is the DownloadClient an engine identity "<client>-<ordinal>"
// names: everything before the last hyphen, since a client's own name may
// hold hyphens. Both engines' Register need it. It is app/grab's
// splitEngineIdentity, moved here with the shim's deletion. A torrent
// identity built from --download-client by torrentagent.EngineIdentity
// always splits back to that client.
func engineClient(engine string) (string, error) {
	i := strings.LastIndex(engine, "-")
	if i <= 0 || i == len(engine)-1 {
		return "", fmt.Errorf("--engine %q is not \"<client>-<ordinal>\"", engine)
	}
	return engine[:i], nil
}

// orList renders "a", "a or b", "a, b, c or d".
func orList(xs []string) string {
	if len(xs) == 1 {
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " or " + xs[len(xs)-1]
}
