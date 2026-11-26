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

package library

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"k8s.io/apiextensions-apiserver/pkg/apis/apiextensions"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	structuralschema "k8s.io/apiextensions-apiserver/pkg/apiserver/schema"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/kube-openapi/pkg/validation/spec"
	"k8s.io/kube-openapi/pkg/validation/strfmt"
	"k8s.io/kube-openapi/pkg/validation/validate"
	"sigs.k8s.io/structured-merge-diff/v6/fieldpath"
	"sigs.k8s.io/yaml"
)

// schemaFiles are the CRDs of the library kinds, as controller-gen renders
// them. They are a kind's schema here and are installed nowhere.
//
//go:embed schemas/*.yaml
var schemaFiles embed.FS

// Schema is one library kind: its names, and the machinery a write to it
// runs through -- defaults, pruning, the value checks, the validation rules
// and field ownership -- all built from the kind's CRD.
type Schema struct {
	GVK      schema.GroupVersionKind
	Resource string // the plural, as RBAC and REST name it (issues)
	// Table is the kind's table in the library schema, unqualified.
	Table string
	// SelectableFields are the JSON paths a field selector may name, beside
	// metadata.name and metadata.namespace (the CRD's selectableFields).
	SelectableFields []string
	// fieldSQL is, for each selectable field the database can filter by,
	// the expression that reads it from a row (fieldExpr).
	fieldSQL map[string]string

	structural      *structuralschema.Structural
	validator       *validate.SchemaValidator
	statusValidator *validate.SchemaValidator
	main, status    *managedfields.FieldManager
}

// GroupResource is the kind's group and plural, as an API error names it.
func (s *Schema) GroupResource() schema.GroupResource {
	return schema.GroupResource{Group: s.GVK.Group, Resource: s.Resource}
}

// ListGVK is the kind of the kind's list.
func (s *Schema) ListGVK() schema.GroupVersionKind {
	return s.GVK.GroupVersion().WithKind(s.GVK.Kind + "List")
}

// Schemas are the library kinds of this build, by kind.
type Schemas map[schema.GroupKind]*Schema

// Embedded builds the schema of every kind under schemas/.
func Embedded() (Schemas, error) {
	out := Schemas{}
	entries, err := fs.ReadDir(schemaFiles, "schemas")
	if err != nil {
		return nil, fmt.Errorf("library: %w", err)
	}
	for _, e := range entries {
		raw, err := fs.ReadFile(schemaFiles, "schemas/"+e.Name())
		if err != nil {
			return nil, fmt.Errorf("library: %w", err)
		}
		s, err := NewSchema(raw)
		if err != nil {
			return nil, fmt.Errorf("library: %s: %w", e.Name(), err)
		}
		out[s.GVK.GroupKind()] = s
	}
	return out, nil
}

// IsResource reports whether group's resource (its plural, as RBAC names
// it) is a library kind of this build. It reads the names of the embedded
// schema files alone -- "<group>_<plural>.yaml", as controller-gen names a
// CRD -- so a caller that only needs to tell the kinds apart builds nothing.
func IsResource(group, resource string) bool {
	_, err := fs.Stat(schemaFiles, "schemas/"+group+"_"+resource+".yaml")
	return err == nil
}

// Sorted is the schemas in the order of their kinds' names.
func (s Schemas) Sorted() []*Schema {
	out := make([]*Schema, 0, len(s))
	for _, sch := range s {
		out = append(out, sch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVK.Kind < out[j].GVK.Kind })
	return out
}

// For is the schema of a kind, nil when the kind is not a library kind.
func (s Schemas) For(gvk schema.GroupVersionKind) *Schema {
	sch := s[gvk.GroupKind()]
	if sch == nil || sch.GVK.Version != gvk.Version {
		return nil
	}
	return sch
}

// NewSchema builds a kind's schema from its CRD manifest: one served
// version, namespaced, with a status subresource, as every library kind is.
func NewSchema(crdYAML []byte) (*Schema, error) {
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.UnmarshalStrict(crdYAML, &crd); err != nil {
		return nil, fmt.Errorf("decode the CRD: %w", err)
	}
	if len(crd.Spec.Versions) != 1 {
		return nil, fmt.Errorf("%s has %d versions; a library kind has one", crd.Name, len(crd.Spec.Versions))
	}
	v := crd.Spec.Versions[0]
	switch {
	case crd.Spec.Scope != apiextensionsv1.NamespaceScoped:
		return nil, fmt.Errorf("%s is not namespaced", crd.Name)
	case v.Subresources == nil || v.Subresources.Status == nil:
		return nil, fmt.Errorf("%s has no status subresource", crd.Name)
	case v.Schema == nil || v.Schema.OpenAPIV3Schema == nil:
		return nil, fmt.Errorf("%s has no schema", crd.Name)
	}
	gvk := schema.GroupVersionKind{Group: crd.Spec.Group, Version: v.Name, Kind: crd.Spec.Names.Kind}

	var internal apiextensions.JSONSchemaProps
	if err := apiextensionsv1.Convert_v1_JSONSchemaProps_To_apiextensions_JSONSchemaProps(v.Schema.OpenAPIV3Schema, &internal, nil); err != nil {
		return nil, fmt.Errorf("%s: convert the schema: %w", gvk.Kind, err)
	}
	structural, err := structuralschema.NewStructural(&internal)
	if err != nil {
		return nil, fmt.Errorf("%s: the schema is not structural: %w", gvk.Kind, err)
	}
	if errs := structuralschema.ValidateStructural(nil, structural); len(errs) > 0 {
		return nil, fmt.Errorf("%s: the schema is not structural: %w", gvk.Kind, errs.ToAggregate())
	}

	s := &Schema{
		GVK:        gvk,
		Resource:   crd.Spec.Names.Plural,
		Table:      strings.ToLower(gvk.Kind),
		structural: structural,
	}
	s.fieldSQL = map[string]string{}
	for _, f := range v.SelectableFields {
		path := strings.TrimPrefix(f.JSONPath, ".")
		s.SelectableFields = append(s.SelectableFields, path)
		if expr, ok := fieldExpr(structural, path); ok {
			s.fieldSQL[path] = expr
		}
	}
	if err := checkRules(structural, gvk.Kind); err != nil {
		return nil, err
	}

	openAPI := structural.ToKubeOpenAPI()
	s.validator = validate.NewSchemaValidator(openAPI, nil, "", strfmt.Default)
	if st, ok := openAPI.Properties["status"]; ok {
		s.statusValidator = validate.NewSchemaValidator(&st, nil, "", strfmt.Default)
	}

	converter, err := typeConverter(gvk, structural)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", gvk.Kind, err)
	}
	scheme := unstructuredScheme{structural: structural}
	apiVersion := fieldpath.APIVersion(gvk.GroupVersion().String())
	// What each endpoint may not set, as the apiserver's strategies for a
	// custom resource have it (k8s.io/apiextensions-apiserver,
	// pkg/registry/customresource): the main resource never sets status, the
	// status subresource never sets metadata or spec.
	reset := func(top ...string) map[fieldpath.APIVersion]fieldpath.Filter {
		paths := make([]fieldpath.Path, 0, len(top))
		for _, f := range top {
			paths = append(paths, fieldpath.MakePathOrDie(f))
		}
		return fieldpath.NewExcludeFilterSetMap(map[fieldpath.APIVersion]*fieldpath.Set{
			apiVersion: fieldpath.NewSet(paths...),
		})
	}
	if s.main, err = managedfields.NewDefaultCRDFieldManager(converter, scheme, scheme, scheme, gvk, gvk.GroupVersion(), "", reset("status")); err != nil {
		return nil, fmt.Errorf("%s: %w", gvk.Kind, err)
	}
	if s.status, err = managedfields.NewDefaultCRDFieldManager(converter, scheme, scheme, scheme, gvk, gvk.GroupVersion(), "status", reset("metadata", "spec")); err != nil {
		return nil, fmt.Errorf("%s: %w", gvk.Kind, err)
	}
	return s, nil
}

// objectMetaSchema is the part of ObjectMeta's schema that field ownership
// reads: which lists merge by key (ownerReferences by uid), which as a set
// (finalizers), and which are one value (managedFields).
const objectMetaSchema = `{
  "type": "object",
  "properties": {
    "name": {"type": "string"},
    "generateName": {"type": "string"},
    "namespace": {"type": "string"},
    "selfLink": {"type": "string"},
    "uid": {"type": "string"},
    "resourceVersion": {"type": "string"},
    "generation": {"type": "integer", "format": "int64"},
    "creationTimestamp": {"type": "string", "format": "date-time"},
    "deletionTimestamp": {"type": "string", "format": "date-time"},
    "deletionGracePeriodSeconds": {"type": "integer", "format": "int64"},
    "labels": {"type": "object", "additionalProperties": {"type": "string"}},
    "annotations": {"type": "object", "additionalProperties": {"type": "string"}},
    "finalizers": {"type": "array", "items": {"type": "string"}, "x-kubernetes-list-type": "set"},
    "ownerReferences": {
      "type": "array",
      "x-kubernetes-list-type": "map",
      "x-kubernetes-list-map-keys": ["uid"],
      "items": {
        "type": "object",
        "x-kubernetes-map-type": "atomic",
        "required": ["apiVersion", "kind", "name", "uid"],
        "properties": {
          "apiVersion": {"type": "string"},
          "kind": {"type": "string"},
          "name": {"type": "string"},
          "uid": {"type": "string"},
          "controller": {"type": "boolean"},
          "blockOwnerDeletion": {"type": "boolean"}
        }
      }
    },
    "managedFields": {
      "type": "array",
      "x-kubernetes-list-type": "atomic",
      "items": {"type": "object", "x-kubernetes-preserve-unknown-fields": true}
    }
  }
}`

// typeConverter builds the converter field ownership merges with, from the
// kind's schema with ObjectMeta's laid in where the CRD says only "object".
func typeConverter(gvk schema.GroupVersionKind, structural *structuralschema.Structural) (managedfields.TypeConverter, error) {
	var objectMeta spec.Schema
	if err := json.Unmarshal([]byte(objectMetaSchema), &objectMeta); err != nil {
		return nil, fmt.Errorf("the ObjectMeta schema: %w", err)
	}
	root := structural.ToKubeOpenAPI()
	if root.Properties == nil {
		root.Properties = map[string]spec.Schema{}
	}
	root.Properties["apiVersion"] = *spec.StringProperty()
	root.Properties["kind"] = *spec.StringProperty()
	root.Properties["metadata"] = *spec.RefSchema("#/components/schemas/ObjectMeta")
	root.AddExtension("x-kubernetes-group-version-kind", []any{
		map[string]any{"group": gvk.Group, "version": gvk.Version, "kind": gvk.Kind},
	})
	return managedfields.NewTypeConverter(map[string]*spec.Schema{
		gvk.Kind:     root,
		"ObjectMeta": &objectMeta,
	}, false)
}

// unstructuredScheme is the converter, the defaulter and the maker of empty
// objects that field ownership asks for, for a kind with one version held as
// unstructured content: conversion is the identity and defaulting is the
// schema's.
type unstructuredScheme struct {
	structural *structuralschema.Structural
}

func (unstructuredScheme) New(gvk schema.GroupVersionKind) (runtime.Object, error) {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(gvk)
	return u, nil
}

func (s unstructuredScheme) Default(obj runtime.Object) {
	if u, ok := obj.(*unstructured.Unstructured); ok {
		// No null is pruned here: an applied configuration that names a
		// null where the schema takes none is refused, as the apiserver
		// refuses it (the differential test holds both to that).
		applyDefaults(u.Object, s.structural)
	}
}

func (unstructuredScheme) Convert(in, out, _ any) error {
	src, ok := in.(*unstructured.Unstructured)
	dst, ok2 := out.(*unstructured.Unstructured)
	if !ok || !ok2 {
		return fmt.Errorf("library: convert %T to %T: only unstructured objects are held", in, out)
	}
	dst.Object = runtime.DeepCopyJSON(src.Object)
	return nil
}

func (unstructuredScheme) ConvertToVersion(in runtime.Object, _ runtime.GroupVersioner) (runtime.Object, error) {
	return in, nil
}

func (unstructuredScheme) ConvertFieldLabel(_ schema.GroupVersionKind, label, value string) (string, string, error) {
	return label, value, nil
}

// pruneNulls drops every null a schema does not allow and has no default
// for, as the apiserver does before it defaults a custom resource: a typed
// object's zero time or nil pointer without omitempty encodes as null, and
// the field is then simply absent. A null in a list is left: dropping it
// would move the items after it.
func pruneNulls(x any, s *structuralschema.Structural) {
	if s == nil {
		return
	}
	switch x := x.(type) {
	case map[string]any:
		for k, v := range x {
			var prop *structuralschema.Structural
			if p, found := s.Properties[k]; found {
				prop = &p
			} else if s.AdditionalProperties != nil {
				prop = s.AdditionalProperties.Structural
			}
			if prop == nil {
				continue
			}
			if v == nil && !prop.Nullable && prop.Default.Object == nil {
				delete(x, k)
				continue
			}
			pruneNulls(v, prop)
		}
	case []any:
		for _, v := range x {
			pruneNulls(v, s.Items)
		}
	}
}

// applyDefaults sets every absent property that has a default, as the
// apiserver does when it decodes a custom resource
// (k8s.io/apiextensions-apiserver/pkg/apiserver/schema/defaulting, whose
// package links the CEL runtime; the algorithm is these lines).
func applyDefaults(x any, s *structuralschema.Structural) {
	if s == nil {
		return
	}
	switch x := x.(type) {
	case map[string]any:
		for k, prop := range s.Properties {
			if prop.Default.Object == nil {
				continue
			}
			if v, found := x[k]; !found || (v == nil && !prop.Nullable) {
				x[k] = runtime.DeepCopyJSONValue(prop.Default.Object)
			}
		}
		for k := range x {
			if prop, found := s.Properties[k]; found {
				applyDefaults(x[k], &prop)
			} else if s.AdditionalProperties != nil && s.AdditionalProperties.Structural != nil {
				ap := s.AdditionalProperties.Structural
				if x[k] == nil && !ap.Nullable && ap.Default.Object != nil {
					x[k] = runtime.DeepCopyJSONValue(ap.Default.Object)
				}
				applyDefaults(x[k], ap)
			}
		}
	case []any:
		for i := range x {
			if x[i] == nil && s.Items != nil && !s.Items.Nullable && s.Items.Default.Object != nil {
				x[i] = runtime.DeepCopyJSONValue(s.Items.Default.Object)
			}
			applyDefaults(x[i], s.Items)
		}
	}
}
