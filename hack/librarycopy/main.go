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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
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
	verify := flag.Bool("verify", false, "read the store alone: decode every row as the manager will and report what a write of it would be refused for")
	flag.Parse()

	dsn := os.Getenv(library.EnvDSN)
	if dsn == "" {
		return fmt.Errorf("set $%s to the library store's address (the manager's role)", library.EnvDSN)
	}
	// Migrate: the copy may run before any manager of the new build has.
	store, err := library.Open(ctx, dsn, library.Options{Migrate: !*dryRun && !*verify})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if *verify {
		return verifyRows(ctx, store, *namespace, *only)
	}
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
			ok, err := store.CopyIn(ctx, sch, &list.Items[i])
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

// verifyRows reads every row of the store and answers for each kind how
// many decode into the kind's Go type, as the manager's informers decode
// them (one that does not fails the whole list), and how many a write would
// be refused for, with the first few of each.
func verifyRows(ctx context.Context, store *library.Store, namespace, only string) error {
	scheme := runtime.NewScheme()
	if err := catalogv1.AddToScheme(scheme); err != nil {
		return err
	}
	bad := 0
	for _, sch := range store.Schemas().Sorted() {
		if only != "" && !strings.EqualFold(only, sch.GVK.Kind) {
			continue
		}
		rows, version, err := store.ListRaw(ctx, sch, namespace)
		if err != nil {
			return fmt.Errorf("%s: %w", sch.GVK.Kind, err)
		}
		undecodable, refused := 0, 0
		reasons := map[string]int{}
		for _, r := range rows {
			typed, err := scheme.New(sch.GVK)
			if err != nil {
				return err
			}
			if err := json.Unmarshal(r.Body, typed); err != nil {
				if undecodable++; undecodable <= 5 {
					fmt.Printf("  %s %s/%s does not decode: %v\n", sch.GVK.Kind, r.Namespace, r.Name, err)
				}
				continue
			}
			var obj map[string]any
			if err := json.Unmarshal(r.Body, &obj); err != nil {
				return err
			}
			if errs := sch.Check(obj); len(errs) > 0 {
				if refused++; refused <= 5 {
					fmt.Printf("  %s %s/%s would be refused: %v\n", sch.GVK.Kind, r.Namespace, r.Name, errs.ToAggregate())
				}
				for _, e := range errs {
					reasons[e.Field+": "+string(e.Type)]++
				}
			}
		}
		fmt.Printf("%-10s rows %6d  at version %6d  undecodable %d  refused %d\n", sch.GVK.Kind, len(rows), version, undecodable, refused)
		for reason, n := range reasons {
			fmt.Printf("  %6d  %s\n", n, reason)
		}
		bad += undecodable + refused
	}
	if bad > 0 {
		return fmt.Errorf("%d rows need attention", bad)
	}
	return nil
}
