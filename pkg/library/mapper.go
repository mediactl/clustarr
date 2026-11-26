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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// restMapper answers for the library kinds, which the apiserver's discovery
// no longer lists, and passes every other kind to the mapper it wraps. A
// controller-runtime cache and client ask the mapper for a kind's resource
// and scope before they do anything with it.
type restMapper struct {
	meta.RESTMapper
	static *meta.DefaultRESTMapper
	kinds  map[schema.GroupKind]bool
	lists  map[schema.GroupKind]schema.GroupKind // a list kind to its kind
	plural map[schema.GroupResource]bool
}

// NewRESTMapper wraps inner so the library kinds resolve.
func NewRESTMapper(inner meta.RESTMapper, schemas Schemas) meta.RESTMapper {
	m := &restMapper{
		RESTMapper: inner,
		static:     meta.NewDefaultRESTMapper(nil),
		kinds:      map[schema.GroupKind]bool{},
		lists:      map[schema.GroupKind]schema.GroupKind{},
		plural:     map[schema.GroupResource]bool{},
	}
	for _, sch := range schemas {
		m.static.AddSpecific(sch.GVK,
			sch.GVK.GroupVersion().WithResource(sch.Resource),
			sch.GVK.GroupVersion().WithResource(sch.Table),
			meta.RESTScopeNamespace)
		// The list kind is answered too (a cache asks whether a list is
		// namespaced by the list's own kind, and the apiserver's discovery
		// mapper answers that), from the kind's mapping: registering it
		// would make the plural resolve to the list kind.
		m.lists[sch.ListGVK().GroupKind()] = sch.GVK.GroupKind()
		m.kinds[sch.GVK.GroupKind()] = true
		m.plural[sch.GroupResource()] = true
		m.plural[schema.GroupResource{Group: sch.GVK.Group, Resource: sch.Table}] = true
	}
	return m
}

func (m *restMapper) KindFor(resource schema.GroupVersionResource) (schema.GroupVersionKind, error) {
	if m.plural[resource.GroupResource()] {
		return m.static.KindFor(resource)
	}
	return m.RESTMapper.KindFor(resource)
}

func (m *restMapper) KindsFor(resource schema.GroupVersionResource) ([]schema.GroupVersionKind, error) {
	if m.plural[resource.GroupResource()] {
		return m.static.KindsFor(resource)
	}
	return m.RESTMapper.KindsFor(resource)
}

func (m *restMapper) ResourceFor(input schema.GroupVersionResource) (schema.GroupVersionResource, error) {
	if m.plural[input.GroupResource()] {
		return m.static.ResourceFor(input)
	}
	return m.RESTMapper.ResourceFor(input)
}

func (m *restMapper) ResourcesFor(input schema.GroupVersionResource) ([]schema.GroupVersionResource, error) {
	if m.plural[input.GroupResource()] {
		return m.static.ResourcesFor(input)
	}
	return m.RESTMapper.ResourcesFor(input)
}

func (m *restMapper) RESTMapping(gk schema.GroupKind, versions ...string) (*meta.RESTMapping, error) {
	if of, ok := m.lists[gk]; ok {
		mapping, err := m.static.RESTMapping(of, versions...)
		if err != nil {
			return nil, err
		}
		list := *mapping
		list.GroupVersionKind.Kind = gk.Kind
		return &list, nil
	}
	if m.kinds[gk] {
		return m.static.RESTMapping(gk, versions...)
	}
	return m.RESTMapper.RESTMapping(gk, versions...)
}

func (m *restMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	if _, ok := m.lists[gk]; ok {
		mapping, err := m.RESTMapping(gk, versions...)
		if err != nil {
			return nil, err
		}
		return []*meta.RESTMapping{mapping}, nil
	}
	if m.kinds[gk] {
		return m.static.RESTMappings(gk, versions...)
	}
	return m.RESTMapper.RESTMappings(gk, versions...)
}
