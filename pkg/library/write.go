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
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"

	structuralpruning "k8s.io/apiextensions-apiserver/pkg/apiserver/schema/pruning"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	"k8s.io/apimachinery/pkg/util/managedfields"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

// The writes below follow the apiserver's handlers for a custom resource
// with a status subresource, step for step: decode (prune, default), field
// ownership, the strategy's preparation (what the endpoint may not set, the
// generation), the precondition on resourceVersion, validation, then the
// row. What each step does is the apiserver's; where it is upstream code it
// is called, not copied.

// optimisticLockMsg is the apiserver's wording for a stale resourceVersion.
const optimisticLockMsg = "the object has been modified; please apply your changes to the latest version and try again"

func notFound(sch *Schema, name string) error {
	return apierrors.NewNotFound(sch.GroupResource(), name)
}

func conflict(sch *Schema, name string) error {
	return apierrors.NewConflict(sch.GroupResource(), name, errors.New(optimisticLockMsg))
}

func invalid(sch *Schema, name string, errs field.ErrorList) error {
	return apierrors.NewInvalid(sch.GVK.GroupKind(), name, errs)
}

// schemaOf is the schema of the object's kind, or an error when the kind is
// not a library kind.
func (s *Store) schemaOf(u *unstructured.Unstructured) (*Schema, error) {
	sch := s.schemas.For(u.GroupVersionKind())
	if sch == nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("library: %s is not a library kind", u.GroupVersionKind()))
	}
	return sch, nil
}

// decode is what the apiserver does to a request body before anything reads
// it: unknown fields pruned, defaults applied, and the ownership records a
// client sent dropped (a write's records are the row's).
func decode(sch *Schema, u *unstructured.Unstructured) {
	structuralpruning.Prune(u.Object, sch.structural, true)
	pruneNulls(u.Object, sch.structural)
	applyDefaults(u.Object, sch.structural)
	coerceMetadata(u)
	unstructured.RemoveNestedField(u.Object, "metadata", "managedFields")
}

// coerceMetadata passes the object's metadata through ObjectMeta, as the
// apiserver does to a custom resource's: an empty map of labels or
// annotations is no field at all (a JSON patch that removes the last
// annotation leaves none), and a field ObjectMeta does not have is dropped.
// Metadata that ObjectMeta cannot hold is left for validation to refuse.
func coerceMetadata(u *unstructured.Unstructured) {
	raw, ok := u.Object["metadata"].(map[string]any)
	if !ok {
		return
	}
	var meta metav1.ObjectMeta
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw, &meta); err != nil {
		return
	}
	out, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&meta)
	if err != nil {
		return
	}
	if ts, found := out["creationTimestamp"]; found && ts == nil {
		delete(out, "creationTimestamp")
	}
	u.Object["metadata"] = out
}

// Create stores a new object as manager.
func (s *Store) Create(ctx context.Context, obj *unstructured.Unstructured, manager string) (*unstructured.Unstructured, error) {
	sch, err := s.schemaOf(obj)
	if err != nil {
		return nil, err
	}
	obj = obj.DeepCopy()
	if obj.GetName() == "" {
		return nil, invalid(sch, "", field.ErrorList{field.Required(field.NewPath("metadata", "name"), "a library object is created by name")})
	}
	if obj.GetResourceVersion() != "" {
		return nil, apierrors.NewBadRequest("resourceVersion should not be set on objects to be created")
	}
	decode(sch, obj)
	empty := &unstructured.Unstructured{}
	empty.SetGroupVersionKind(sch.GVK)
	created, ok := sch.main.UpdateNoErrors(empty, obj, manager).(*unstructured.Unstructured)
	if !ok {
		return nil, fmt.Errorf("library: create %s: field ownership answered no object", sch.GVK.Kind)
	}
	return s.insertNew(ctx, sch, created)
}

// insertNew finishes a create: the strategy's preparation, validation, the
// row.
func (s *Store) insertNew(ctx context.Context, sch *Schema, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	unstructured.RemoveNestedField(obj.Object, "status")
	obj.SetGeneration(1)
	obj.SetUID(uuid.NewUUID())
	obj.SetCreationTimestamp(metav1.NewTime(s.now().UTC().Truncate(time.Second)))
	unstructured.RemoveNestedField(obj.Object, "metadata", "deletionTimestamp")
	unstructured.RemoveNestedField(obj.Object, "metadata", "deletionGracePeriodSeconds")
	unstructured.RemoveNestedField(obj.Object, "metadata", "resourceVersion")

	errs := validateValues(sch.validator, nil, obj.Object)
	errs = append(errs, validateRules(sch.structural, obj.Object, nil)...)
	if len(errs) > 0 {
		return nil, invalid(sch, obj.GetName(), errs)
	}
	cols, err := split(obj)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("library: create %s: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	version, err := s.insert(ctx, tx, sch, obj.GetNamespace(), obj.GetName(), cols)
	if errors.Is(err, errExists) {
		return nil, apierrors.NewAlreadyExists(sch.GroupResource(), obj.GetName())
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("library: create %s: %w", sch.GVK.Kind, err)
	}
	obj.SetResourceVersion(strconv.FormatInt(version, 10))
	return obj, nil
}

// Update replaces an object (its status alone when status is set) as
// manager. The object carries the resourceVersion it was read at.
func (s *Store) Update(ctx context.Context, obj *unstructured.Unstructured, manager string, status bool) (*unstructured.Unstructured, error) {
	sch, err := s.schemaOf(obj)
	if err != nil {
		return nil, err
	}
	if obj.GetResourceVersion() == "" {
		return nil, invalid(sch, obj.GetName(), field.ErrorList{
			field.Invalid(field.NewPath("metadata", "resourceVersion"), 0, "must be specified for an update"),
		})
	}
	obj = obj.DeepCopy()
	decode(sch, obj)
	return s.mutate(ctx, sch, obj.GetNamespace(), obj.GetName(), status, func(live *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		return fieldManager(sch, status).UpdateNoErrors(live, obj, manager).(*unstructured.Unstructured), nil
	})
}

// Patch applies a JSON merge patch (RFC 7386) to an object, or to its status
// when status is set, as manager.
func (s *Store) Patch(ctx context.Context, sch *Schema, namespace, name string, mergePatch []byte, manager string, status bool) (*unstructured.Unstructured, error) {
	var patch any
	if err := utiljson.Unmarshal(mergePatch, &patch); err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("library: the merge patch is not JSON: %v", err))
	}
	patchMap, ok := patch.(map[string]any)
	if !ok {
		return nil, apierrors.NewBadRequest("library: a merge patch of an object is an object")
	}
	return s.mutate(ctx, sch, namespace, name, status, func(live *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		patched := &unstructured.Unstructured{Object: mergeJSON(runtime.DeepCopyJSON(live.Object), patchMap).(map[string]any)}
		if patched.GetName() != name || patched.GetNamespace() != namespace || patched.GroupVersionKind() != sch.GVK {
			return nil, apierrors.NewBadRequest("library: a patch may not change an object's kind, namespace or name")
		}
		decode(sch, patched)
		return fieldManager(sch, status).UpdateNoErrors(live, patched, manager).(*unstructured.Unstructured), nil
	})
}

// PatchJSON applies a JSON patch (RFC 6902) to an object, or to its status
// when status is set, as manager. A patch that does not apply -- a failed
// test operation above all, which is how a caller removes an annotation only
// while it still holds the value it handled -- is refused as the apiserver
// refuses it, with 422.
func (s *Store) PatchJSON(ctx context.Context, sch *Schema, namespace, name string, jsonPatchDoc []byte, manager string, status bool) (*unstructured.Unstructured, error) {
	patch, err := jsonpatch.DecodePatch(jsonPatchDoc)
	if err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("library: the JSON patch does not decode: %v", err))
	}
	return s.mutate(ctx, sch, namespace, name, status, func(live *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		doc, err := json.Marshal(live.Object)
		if err != nil {
			return nil, fmt.Errorf("library: encode %s: %w", name, err)
		}
		out, err := patch.Apply(doc)
		if err != nil {
			return nil, apierrors.NewGenericServerResponse(http.StatusUnprocessableEntity, "patch", sch.GroupResource(), name, err.Error(), 0, true)
		}
		patched := &unstructured.Unstructured{Object: map[string]any{}}
		if err := utiljson.Unmarshal(out, &patched.Object); err != nil {
			return nil, apierrors.NewBadRequest(fmt.Sprintf("library: the patched object is not an object: %v", err))
		}
		if patched.GetName() != name || patched.GetNamespace() != namespace || patched.GroupVersionKind() != sch.GVK {
			return nil, apierrors.NewBadRequest("library: a patch may not change an object's kind, namespace or name")
		}
		decode(sch, patched)
		return fieldManager(sch, status).UpdateNoErrors(live, patched, manager).(*unstructured.Unstructured), nil
	})
}

// Apply is a server-side apply of applied, a partial object naming its kind
// and its name, as manager: to the object, creating it when it does not
// exist, or to its status when status is set. force takes the fields another
// manager owns, as every apply clustarr makes does.
func (s *Store) Apply(ctx context.Context, applied *unstructured.Unstructured, manager string, status, force bool) (*unstructured.Unstructured, error) {
	sch, err := s.schemaOf(applied)
	if err != nil {
		return nil, err
	}
	if manager == "" {
		return nil, apierrors.NewBadRequest("library: an apply names its field manager")
	}
	if applied.GetName() == "" {
		return nil, apierrors.NewBadRequest("library: an apply names its object")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(applied.Object, "metadata", "managedFields"); found {
		return nil, apierrors.NewBadRequest("metadata.managedFields must be nil")
	}
	// The apiserver reads an applied configuration as YAML, whatever sent
	// it, and YAML takes no raw C1 control character: an apply that carries
	// one in a string is refused there before anything else looks at it
	// (which is why mediafilestatus.Render scrubs them). The store reads the
	// same bytes the same way, so what is refused there is refused here.
	body, err := json.Marshal(applied.Object)
	if err != nil {
		return nil, fmt.Errorf("library: encode the applied configuration: %w", err)
	}
	var asYAML map[string]any
	if err := yaml.Unmarshal(body, &asYAML); err != nil {
		return nil, apierrors.NewBadRequest(fmt.Sprintf("error decoding YAML: %v", err))
	}
	apply := func(live *unstructured.Unstructured) (*unstructured.Unstructured, error) {
		out, err := fieldManager(sch, status).Apply(live, applied.DeepCopy(), manager, force)
		if err != nil {
			var statusErr *apierrors.StatusError
			if errors.As(err, &statusErr) {
				return nil, err
			}
			return nil, apierrors.NewBadRequest(fmt.Sprintf("library: apply %s %s as %q: %v", sch.GVK.Kind, applied.GetName(), manager, err))
		}
		u, ok := out.(*unstructured.Unstructured)
		if !ok {
			return nil, fmt.Errorf("library: apply %s: field ownership answered a %T", sch.GVK.Kind, out)
		}
		return u, nil
	}
	// An apply to an object that is not there creates it; another writer may
	// create it first, and then this one applies to that.
	for range 3 {
		out, err := s.mutate(ctx, sch, applied.GetNamespace(), applied.GetName(), status, apply)
		if !apierrors.IsNotFound(err) || status {
			return out, err
		}
		if applied.GetResourceVersion() != "" {
			// A precondition names a version of an object that is gone.
			return nil, conflict(sch, applied.GetName())
		}
		empty := &unstructured.Unstructured{}
		empty.SetGroupVersionKind(sch.GVK)
		created, err := apply(empty)
		if err != nil {
			return nil, err
		}
		out, err = s.insertNew(ctx, sch, created)
		if !apierrors.IsAlreadyExists(err) {
			return out, err
		}
	}
	return nil, conflict(sch, applied.GetName())
}

// Preconditions are what a delete may require of the object it removes.
type Preconditions struct {
	UID             types.UID
	ResourceVersion string
}

// Delete removes an object. One with finalizers is marked as being deleted
// (a deletion timestamp, as the apiserver sets) and goes when the last
// finalizer is removed.
func (s *Store) Delete(ctx context.Context, sch *Schema, namespace, name string, pre Preconditions) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("library: delete %s: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := s.read(ctx, tx, sch, namespace, name, true)
	if err != nil {
		return err
	}
	if r == nil {
		return notFound(sch, name)
	}
	live, err := r.object(true)
	if err != nil {
		return err
	}
	was, err := split(live)
	if err != nil {
		return err
	}
	if pre.UID != "" && pre.UID != live.GetUID() {
		return apierrors.NewConflict(sch.GroupResource(), name,
			fmt.Errorf("precondition failed: UID in precondition: %v, UID in object meta: %v", pre.UID, live.GetUID()))
	}
	if pre.ResourceVersion != "" && pre.ResourceVersion != live.GetResourceVersion() {
		return apierrors.NewConflict(sch.GroupResource(), name,
			fmt.Errorf("precondition failed: ResourceVersion in precondition: %v, ResourceVersion in object meta: %v", pre.ResourceVersion, live.GetResourceVersion()))
	}
	if len(live.GetFinalizers()) == 0 {
		if _, err := s.remove(ctx, tx, sch, namespace, name); err != nil {
			return err
		}
		return commit(tx, sch, "delete")
	}
	if live.GetDeletionTimestamp() != nil {
		return nil // already going; its finalizers hold it
	}
	now := metav1.NewTime(s.now().UTC().Truncate(time.Second))
	live.SetDeletionTimestamp(&now)
	zero := int64(0)
	live.SetDeletionGracePeriodSeconds(&zero)
	if g := live.GetGeneration(); g > 0 {
		live.SetGeneration(g + 1)
	}
	cols, err := split(live)
	if err != nil {
		return err
	}
	if _, _, err := s.update(ctx, tx, sch, namespace, name, cols, was); err != nil {
		return err
	}
	return commit(tx, sch, "delete")
}

func commit(tx *sql.Tx, sch *Schema, verb string) error {
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("library: %s %s: %w", verb, sch.GVK.Kind, err)
	}
	return nil
}

func fieldManager(sch *Schema, status bool) *managedfields.FieldManager {
	if status {
		return sch.status
	}
	return sch.main
}

// mutate is every write to an object that exists: under the row's lock it
// asks next for the object the write makes of the live one (field ownership
// already settled), prepares it as the endpoint's strategy does, checks the
// resourceVersion it names, validates it and writes it. A write that
// changes nothing but the times in the ownership records writes nothing and
// answers the object as it stands.
func (s *Store) mutate(ctx context.Context, sch *Schema, namespace, name string, status bool,
	next func(live *unstructured.Unstructured) (*unstructured.Unstructured, error),
) (*unstructured.Unstructured, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("library: write %s: %w", sch.GVK.Kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	r, err := s.read(ctx, tx, sch, namespace, name, true)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, notFound(sch, name)
	}
	live, err := r.object(true)
	if err != nil {
		return nil, err
	}
	obj, err := next(live.DeepCopy())
	if err != nil {
		return nil, err
	}

	if rv := obj.GetResourceVersion(); rv != "" && rv != live.GetResourceVersion() {
		return nil, conflict(sch, name)
	}
	if uid := obj.GetUID(); uid != "" && uid != live.GetUID() {
		return nil, apierrors.NewConflict(sch.GroupResource(), name,
			fmt.Errorf("precondition failed: UID in precondition: %v, UID in object meta: %v", uid, live.GetUID()))
	}
	prepare(obj, live, status)

	var errs field.ErrorList
	if status {
		errs = validateValues(sch.statusValidator, field.NewPath("status"), obj.Object["status"])
	} else {
		errs = validateValues(sch.validator, nil, obj.Object)
	}
	errs = append(errs, validateRules(sch.structural, obj.Object, live.Object)...)
	if len(errs) > 0 {
		return nil, invalid(sch, name, errs)
	}

	if live.GetDeletionTimestamp() != nil && len(obj.GetFinalizers()) == 0 {
		// The last finalizer is off an object being deleted: it goes.
		seq, err := s.remove(ctx, tx, sch, namespace, name)
		if err != nil {
			return nil, err
		}
		if err := commit(tx, sch, "write"); err != nil {
			return nil, err
		}
		obj.SetResourceVersion(strconv.FormatInt(seq, 10))
		return obj, nil
	}

	cols, err := split(obj)
	if err != nil {
		return nil, err
	}
	was, err := split(live)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(cols.body, was.body) && bytes.Equal(cols.status, was.status) && sameOwnership(cols.managed, was.managed) {
		return live, nil
	}
	version, changed, err := s.update(ctx, tx, sch, namespace, name, cols, was)
	if err != nil {
		return nil, err
	}
	if !changed {
		return live, nil
	}
	if err := commit(tx, sch, "write"); err != nil {
		return nil, err
	}
	obj.SetResourceVersion(strconv.FormatInt(version, 10))
	return obj, nil
}

// prepare is the apiserver's preparation of an update: the metadata no
// write changes is the live object's, the main resource keeps the live
// status and the status subresource changes nothing else, and a change
// outside metadata and status is a new generation.
func prepare(obj, live *unstructured.Unstructured, status bool) {
	if status {
		st, has := obj.Object["status"]
		managed, hasManaged, _ := unstructured.NestedFieldNoCopy(obj.Object, "metadata", "managedFields")
		obj.Object = runtime.DeepCopyJSON(live.Object)
		if has {
			obj.Object["status"] = st
		} else {
			delete(obj.Object, "status")
		}
		if hasManaged {
			_ = unstructured.SetNestedField(obj.Object, managed, "metadata", "managedFields")
		} else {
			unstructured.RemoveNestedField(obj.Object, "metadata", "managedFields")
		}
		return
	}
	obj.SetUID(live.GetUID())
	obj.SetCreationTimestamp(live.GetCreationTimestamp())
	obj.SetDeletionTimestamp(live.GetDeletionTimestamp())
	obj.SetDeletionGracePeriodSeconds(live.GetDeletionGracePeriodSeconds())
	obj.SetGeneration(live.GetGeneration())
	if st, has := live.Object["status"]; has {
		obj.Object["status"] = runtime.DeepCopyJSONValue(st)
	} else {
		delete(obj.Object, "status")
	}
	if !sameOutsideMetadata(obj.Object, live.Object) {
		obj.SetGeneration(live.GetGeneration() + 1)
	}
}

func sameOutsideMetadata(a, b map[string]any) bool {
	strip := func(m map[string]any) []byte {
		c := make(map[string]any, len(m))
		for k, v := range m {
			if k != "metadata" {
				c[k] = v
			}
		}
		out, _ := json.Marshal(c)
		return out
	}
	return bytes.Equal(strip(a), strip(b))
}

// split is the object as the row holds it: the body without the status,
// the resourceVersion and the ownership records; the status; the records.
func split(obj *unstructured.Unstructured) (stored, error) {
	var c stored
	content := runtime.DeepCopyJSON(obj.Object)
	records, _, _ := unstructured.NestedSlice(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "resourceVersion")
	status, hasStatus := content["status"]
	delete(content, "status")
	var err error
	if c.body, err = json.Marshal(content); err != nil {
		return c, fmt.Errorf("library: encode %s: %w", obj.GetName(), err)
	}
	if hasStatus && status != nil {
		if c.status, err = json.Marshal(status); err != nil {
			return c, fmt.Errorf("library: encode %s: %w", obj.GetName(), err)
		}
	}
	if records == nil {
		records = []any{}
	}
	if c.managed, err = json.Marshal(records); err != nil {
		return c, fmt.Errorf("library: encode %s: %w", obj.GetName(), err)
	}
	return c, nil
}

// sameOwnership reports whether two sets of ownership records differ in
// nothing but their times: field ownership stamps the time of every write,
// and the apiserver drops a write whose only change is that stamp.
func sameOwnership(a, b []byte) bool {
	if bytes.Equal(a, b) {
		return true
	}
	strip := func(raw []byte) []byte {
		var records []map[string]any
		if err := utiljson.Unmarshal(raw, &records); err != nil {
			return raw
		}
		for _, r := range records {
			delete(r, "time")
		}
		out, _ := json.Marshal(records)
		return out
	}
	return bytes.Equal(strip(a), strip(b))
}

// mergeJSON applies a JSON merge patch (RFC 7386) to a decoded document.
func mergeJSON(target, patch any) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	t, ok := target.(map[string]any)
	if !ok {
		t = map[string]any{}
	}
	for k, v := range p {
		if v == nil {
			delete(t, k)
			continue
		}
		t[k] = mergeJSON(t[k], v)
	}
	return t
}
