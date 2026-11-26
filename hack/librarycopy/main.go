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

// Command librarycopy copies the library kinds from a cluster where they are
// still custom resources into the library store (ADR-0024; the cutover
// runbook, docs/runbooks/library-store-cutover.md). It is the one-time tool
// of that cutover and is deleted after it: no release carries a migration
// (owner, 2026-10-07), and a rescan could not restore what the objects hold
// -- a file's probe and transcode provenance, its markers and subtitles, an
// item's grab history (owner, 2026-11-26: "Copy").
//
// Each object is written as it stands: its uid, its creation time, its
// generation, its finalizers, its owners, its spec and its status, and its
// ownership records, so the manager's first apply to it merges with what
// the apiserver recorded and releases nothing. The store's trigger gives it
// a version. A row that is already there is left alone, so the copy can be
// run again after a failure.
//
// It reads with the kubeconfig's context and pages each kind (--page), so
// the manager must be stopped first: an object written after its page was
// read would be copied stale.
//
//	CLUSTARR_LIBRARY_DSN=... go run ./hack/librarycopy --namespace clustarr-system [--kind MediaFile] [--dry-run]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/library"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "librarycopy:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	namespace := flag.String("namespace", "", "the namespace to copy; empty copies every namespace")
	only := flag.String("kind", "", "copy this kind alone (MediaFile, Episode, Album, Book, Audiobook, Issue); empty copies all")
	page := flag.Int64("page", 500, "objects per list page")
	dryRun := flag.Bool("dry-run", false, "read and count; write nothing")
	flag.Parse()

	dsn := os.Getenv(library.EnvDSN)
	if dsn == "" {
		return fmt.Errorf("set $%s to the library store's address (the manager's role)", library.EnvDSN)
	}
	// Migrate: the copy may run before any manager of the new build has.
	store, err := library.Open(ctx, dsn, library.Options{Migrate: !*dryRun})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}

	for _, sch := range store.Schemas().Sorted() {
		if *only != "" && !strings.EqualFold(*only, sch.GVK.Kind) {
			continue
		}
		read, wrote, err := copyKind(ctx, dyn, store, sch, *namespace, *page, *dryRun)
		fmt.Printf("%-10s read %6d  wrote %6d  already there %6d\n", sch.GVK.Kind, read, wrote, read-wrote)
		if err != nil {
			return fmt.Errorf("%s: %w", sch.GVK.Kind, err)
		}
	}
	return nil
}

func copyKind(ctx context.Context, dyn dynamic.Interface, store *library.Store, sch *library.Schema, namespace string, page int64, dryRun bool) (read, wrote int, err error) {
	ri := dyn.Resource(sch.GVK.GroupVersion().WithResource(sch.Resource)).Namespace(namespace)
	opts := metav1.ListOptions{Limit: page}
	for {
		list, err := ri.List(ctx, opts)
		if err != nil {
			return read, wrote, fmt.Errorf("list: %w", err)
		}
		for i := range list.Items {
			read++
			if dryRun {
				continue
			}
			ok, err := insert(ctx, store, sch, &list.Items[i])
			if err != nil {
				return read, wrote, fmt.Errorf("%s/%s: %w", list.Items[i].GetNamespace(), list.Items[i].GetName(), err)
			}
			if ok {
				wrote++
			}
		}
		if list.GetContinue() == "" {
			return read, wrote, nil
		}
		opts.Continue = list.GetContinue()
	}
}

// insert writes one object as the row the store would hold for it: the body
// without its status, its resourceVersion and its ownership records, and
// those two in their own columns.
func insert(ctx context.Context, store *library.Store, sch *library.Schema, u *unstructured.Unstructured) (bool, error) {
	content := u.DeepCopy().Object
	managed, _, _ := unstructured.NestedSlice(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "managedFields")
	unstructured.RemoveNestedField(content, "metadata", "resourceVersion")
	status, hasStatus := content["status"]
	delete(content, "status")
	body, err := json.Marshal(content)
	if err != nil {
		return false, err
	}
	var statusArg any
	if hasStatus && status != nil {
		raw, err := json.Marshal(status)
		if err != nil {
			return false, err
		}
		statusArg = string(raw)
	}
	if managed == nil {
		managed = []any{}
	}
	owned, err := json.Marshal(managed)
	if err != nil {
		return false, err
	}
	res, err := store.DB().ExecContext(ctx,
		`INSERT INTO library.`+sch.Table+` (namespace, name, body, status, managed)
		 VALUES ($1, $2, $3::jsonb, $4::jsonb, $5::jsonb) ON CONFLICT (namespace, name) DO NOTHING`,
		u.GetNamespace(), u.GetName(), string(body), statusArg, string(owned))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
