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

// Package crdbases embeds the CRDs controller-gen generates into this
// directory (make manifests), so a binary can read the very schema the
// apiserver enforces: the UI derives its settings forms from it (settings
// CRUD design, 2026-09-24). The YAML files are the source of truth for the
// chart and kustomize too; this package only reads them.
package crdbases

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"sync"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

//go:embed *.yaml
var files embed.FS

var (
	once sync.Once
	all  []*apiextensionsv1.CustomResourceDefinition
	load error
)

func decodeAll() {
	names, err := fs.Glob(files, "*.yaml")
	if err != nil {
		load = err
		return
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := files.ReadFile(name)
		if err != nil {
			load = fmt.Errorf("crdbases: read %s: %w", name, err)
			return
		}
		var crd apiextensionsv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			load = fmt.Errorf("crdbases: decode %s: %w", name, err)
			return
		}
		all = append(all, &crd)
	}
}

// All returns every embedded CRD, in file-name order. The slice and its
// elements are shared: callers must not modify them.
func All() []*apiextensionsv1.CustomResourceDefinition {
	once.Do(decodeAll)
	return all
}

// Load returns the CRD of kind in group, or an error naming what was asked
// for when none is embedded.
func Load(group, kind string) (*apiextensionsv1.CustomResourceDefinition, error) {
	once.Do(decodeAll)
	if load != nil {
		return nil, load
	}
	for _, crd := range all {
		if crd.Spec.Group == group && crd.Spec.Names.Kind == kind {
			return crd, nil
		}
	}
	return nil, fmt.Errorf("crdbases: no embedded CRD for %s in %s", kind, group)
}

// SpecSchema returns the OpenAPI schema of .spec for the named version of
// crd, or nil when the version or the schema is missing.
func SpecSchema(crd *apiextensionsv1.CustomResourceDefinition, version string) *apiextensionsv1.JSONSchemaProps {
	for i := range crd.Spec.Versions {
		v := &crd.Spec.Versions[i]
		if v.Name != version || v.Schema == nil || v.Schema.OpenAPIV3Schema == nil {
			continue
		}
		spec, ok := v.Schema.OpenAPIV3Schema.Properties["spec"]
		if !ok {
			return nil
		}
		return &spec
	}
	return nil
}
