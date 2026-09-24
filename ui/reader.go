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

package ui

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// readerSchemeFuncs is corev1 plus the five Clustarr API groups: catalog,
// index, download, transcode and subtitle. It is deliberately NOT
// pkg/k8s.AddToSchemeFuncs: that list also carries batchv1, appsv1,
// coordinationv1 and eventsv1 for the controllers that create Jobs,
// Deployments and Leases, none of which ui ever touches, and -- the
// load-bearing reason -- ui/ must never import pkg/k8s at all. Task D3-4
// adds an AST guard asserting exactly that (modelled on
// cmd/clustarr/bus_hooks_guard_test.go, per the design plan); a duplicated,
// narrower list here costs five lines and is what keeps that guard true
// rather than something D3-4 has to go change.
var readerSchemeFuncs = []func(*runtime.Scheme) error{
	corev1.AddToScheme,
	catalogv1alpha1.AddToScheme,
	indexv1alpha1.AddToScheme,
	downloadv1alpha1.AddToScheme,
	transcodev1alpha1.AddToScheme,
	subtitlev1alpha1.AddToScheme,
}

// NewReaderScheme returns the scheme [NewClusterReader]'s cache maps
// GroupVersionKinds through: corev1 plus the five Clustarr API groups. It
// takes no cluster and cannot fail from anything but a programming error in
// a generated AddToScheme.
func NewReaderScheme() (*runtime.Scheme, error) {
	s := runtime.NewScheme()
	for _, add := range readerSchemeFuncs {
		if err := add(s); err != nil {
			return nil, fmt.Errorf("ui: build reader scheme: %w", err)
		}
	}
	return s, nil
}

// MustNewReaderScheme is [NewReaderScheme] for package-level initialisation,
// mirroring pkg/k8s.MustNewScheme. The only way it fails is a programming
// error in a generated AddToScheme.
func MustNewReaderScheme() *runtime.Scheme {
	s, err := NewReaderScheme()
	utilruntime.Must(err)
	return s
}

// NewClusterReader builds ui's one seam onto the cluster: a standalone,
// informer-backed cache over scheme, started against cfg.
//
// It returns a [client.Reader] rather than the [cache.Cache] itself on
// purpose -- a cache.Cache satisfies client.Reader and nothing more, so
// handing back the narrower type is what keeps a caller from ever reaching
// for Get/List's absent siblings Create, Update, Patch or Delete. See
// CLAUDE.md: "The UI never writes status and owns no CRD."
//
// ui deliberately runs no controller-runtime manager (design plan ruling
// R1, cmd/clustarr/all.go and config/manager/ui.yaml): a manager would
// reintroduce the metrics port, the health port and leader election that
// both of those files record ui as intentionally without, for a process
// that only ever reads. cache.New plus a goroutine gives informer-backed
// reads with none of that.
//
// The cache is started in a goroutine bound to ctx and NewClusterReader
// returns immediately -- it does not block on the first sync, so a caller on
// an unreachable or slow cluster is never stuck in construction. The second
// return value is the cache's own WaitForCacheSync, which a readiness probe
// can poll per request; it reports true trivially until something actually
// lists or watches through the reader; the caller does not need to know
// that; nobody here asks the cache to sync anything.
//
// The error log on a Start failure reads through ctx's logger
// (pkg/obs/logging.FromContext, per CLAUDE.md -- no package-level logger).
// Callers that build a reader before their own obs.Bootstrap runs (both
// cmd/clustarr call sites do, so the reader's lifetime matches the whole
// process rather than only the request that happens to trigger obs.Bootstrap
// first) will find that line goes to the discard logger for the ordinary
// case where Start never errors; a genuine failure is also visible through
// controller-runtime's own process-wide logr, which every service's Bootstrap
// bridges to the same structured stream once it runs.
func NewClusterReader(
	ctx context.Context, cfg *rest.Config, scheme *runtime.Scheme,
) (client.Reader, func(context.Context) bool, error) {
	// managedFields are stripped from every cached object: the ui reads
	// none of them, and on a real library they are a third of the bytes of
	// a 57 MB Episode list held whole in this cache.
	c, err := cache.New(cfg, cache.Options{Scheme: scheme, DefaultTransform: cache.TransformStripManagedFields()})
	if err != nil {
		return nil, nil, fmt.Errorf("ui: build cluster cache: %w", err)
	}

	go func() {
		if err := c.Start(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).Error("ui cluster reader cache stopped", "error", err)
		}
	}()

	return c, c.WaitForCacheSync, nil
}
