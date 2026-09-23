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

package bundle

import (
	"context"
	"fmt"
	"io/fs"
	"os"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// LabelBundled marks an IndexerDefinition the loader created from the
// bundle, and so may update. See the package doc.
const LabelBundled = "index.clustarr.io/bundled"

// Loader applies a bundle directory's definitions as IndexerDefinitions.
type Loader struct {
	// Client reads and writes IndexerDefinitions.
	Client client.Client

	// Dir is the bundle directory (--cardigann-definitions-dir).
	Dir string

	// FS overrides Dir for a test; nil reads Dir from disk.
	FS fs.FS
}

// Result counts one SyncOnce.
type Result struct {
	Applied int // created or updated
	Kept    int // an operator's same-named object, left alone
	Refused int // files LoadBundle refused, or ids with no valid name
}

// Run syncs the bundle once, then waits for ctx: a bundle changes with a
// redeploy, and a restart reloads it. A directory that cannot be read ends
// the Run with an error, which stops the manager.
func (l *Loader) Run(ctx context.Context) error {
	res, err := l.SyncOnce(ctx)
	if err != nil {
		return err
	}
	logging.FromContext(ctx).Info("bundle: Cardigann definitions loaded",
		"dir", l.Dir, "applied", res.Applied, "kept", res.Kept, "refused", res.Refused)
	<-ctx.Done()
	return nil
}

// SyncOnce loads the bundle and applies every accepted definition.
func (l *Loader) SyncOnce(ctx context.Context) (Result, error) {
	ctx, span := tracing.Start(ctx, "bundle.Loader.SyncOnce")
	defer span.End()
	log := logging.FromContext(ctx)

	fsys := l.FS
	if fsys == nil {
		fsys = os.DirFS(l.Dir)
	}
	defs, issues, err := cardigann.LoadBundle(fsys)
	if err != nil {
		tracing.RecordError(span, err)
		return Result{}, fmt.Errorf("bundle: load %s: %w", l.Dir, err)
	}

	var res Result
	for _, issue := range issues {
		res.Refused++
		log.Warn("bundle: refused a definition", "file", issue.File, "reason", issue.Err.Error())
	}
	seen := map[string]string{}
	for _, def := range defs {
		name := cardigann.ObjectName(def.ID())
		if name == "" {
			res.Refused++
			log.Warn("bundle: refused a definition whose id yields no object name", "file", def.File, "id", def.ID())
			continue
		}
		if first, dup := seen[name]; dup {
			res.Refused++
			log.Warn("bundle: refused a definition whose object name another already has",
				"file", def.File, "name", name, "first", first)
			continue
		}
		seen[name] = def.File

		var existing indexv1alpha1.IndexerDefinition
		err := l.Client.Get(ctx, client.ObjectKey{Name: name}, &existing)
		switch {
		case err == nil && existing.Labels[LabelBundled] != "true":
			res.Kept++
			log.Info("bundle: an IndexerDefinition of this name is not the bundle's; leaving it",
				"name", name, "file", def.File)
			continue
		case err != nil && !apierrors.IsNotFound(err):
			tracing.RecordError(span, err)
			return res, fmt.Errorf("bundle: get IndexerDefinition %s: %w", name, err)
		}

		ac := indexac.IndexerDefinition(name).
			WithLabels(map[string]string{LabelBundled: "true"}).
			WithSpec(indexac.IndexerDefinitionSpec().WithYAML(string(def.YAML)))
		if _, err := k8s.Apply(ctx, l.Client, k8s.ManagerIndexarr, ac); err != nil {
			tracing.RecordError(span, err)
			return res, fmt.Errorf("bundle: apply IndexerDefinition %s: %w", name, err)
		}
		res.Applied++
	}
	return res, nil
}
