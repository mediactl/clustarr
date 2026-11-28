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

// Command transcoderetry clears the transcode verdict of the library's
// MediaFiles whose last transcode failed with a message that matches, by
// setting transcode.clustarr.io/retry on each: what `kubectl annotate
// mediafile` did before the library kinds became rows of the library store
// (ADR-0024), where kubectl does not reach. The annotation is the one-shot
// intent the planner already honours (it clears a Failed or Skipped verdict
// of the profile's current hash); nothing else of a row is written.
//
// It reads and prints by default. With --apply it writes, through the
// store's own Patch, as the field manager "transcoderetry".
//
//	CLUSTARR_LIBRARY_DSN=... go run ./hack/transcoderetry --namespace clustarr-system \
//	    --match 'output is ' [--reason VerifyFailed] [--apply]
//
// Written for the files failed at policy.maxOutputToSourcePercent before
// an encode a file needs to play was allowed three times its source
// (worker.RequiredEncodeLimitPercent, 2026-11-27): 131 of them on talos.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/library"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "transcoderetry:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	namespace := flag.String("namespace", "", "the namespace of the MediaFiles (required)")
	match := flag.String("match", "", "retry the files whose failure message contains this (required)")
	reason := flag.String("reason", "", "only failures with this status.transcode.reason; empty is any")
	apply := flag.Bool("apply", false, "write the retry annotation; without it the files are only listed")
	limit := flag.Int("limit", 0, "retry at most this many; 0 is all")
	flag.Parse()
	if *namespace == "" || *match == "" {
		return fmt.Errorf("--namespace and --match are required")
	}
	dsn := os.Getenv(library.EnvDSN)
	if dsn == "" {
		return fmt.Errorf("set $%s to the library store's address (the manager's role)", library.EnvDSN)
	}
	store, err := library.Open(ctx, dsn, library.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	var sch *library.Schema
	for _, s := range store.Schemas().Sorted() {
		if s.GVK.Kind == "MediaFile" {
			sch = s
		}
	}
	if sch == nil {
		return fmt.Errorf("the store has no MediaFile schema")
	}
	rows, _, err := store.ListRaw(ctx, sch, *namespace)
	if err != nil {
		return fmt.Errorf("list MediaFiles: %w", err)
	}
	nonce := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if !catalogv1.ValidNonce(nonce) {
		return fmt.Errorf("nonce %q is not valid", nonce)
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{
		catalogv1.AnnotationTranscodeRetry: nonce,
	}}})
	if err != nil {
		return err
	}

	matched, written := 0, 0
	for _, r := range rows {
		var mf catalogv1.MediaFile
		if err := json.Unmarshal(r.Body, &mf); err != nil {
			continue
		}
		tr := mf.Status.Transcode
		if tr == nil || tr.Phase != catalogv1.TranscodePhaseFailed || !strings.Contains(tr.Message, *match) ||
			(*reason != "" && tr.Reason != *reason) {
			continue
		}
		if *limit > 0 && matched >= *limit {
			break
		}
		matched++
		fmt.Printf("%s\t%s\t%s\n", r.Name, tr.Reason, tr.Message)
		if !*apply {
			continue
		}
		if _, err := store.Patch(ctx, sch, r.Namespace, r.Name, patch, "transcoderetry", false); err != nil {
			return fmt.Errorf("%s: %w (%d written before it)", r.Name, err, written)
		}
		written++
	}
	if *apply {
		fmt.Printf("%d matched, %d given %s=%s\n", matched, written, catalogv1.AnnotationTranscodeRetry, nonce)
	} else {
		fmt.Printf("%d matched; nothing written (pass --apply)\n", matched)
	}
	return nil
}
