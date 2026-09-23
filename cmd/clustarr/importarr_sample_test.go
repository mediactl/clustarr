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

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestImportarrSampleMaxBytesReachesTheOptions pins the one link of the
// sample size floor's wiring that fails silently. newImportarrCommand builds
// importarr.Options as a bare literal rather than from DefaultOptions(), so
// a field it leaves out is zero -- and for SampleMaxBytes zero is not "unset"
// but a working value: the rule switched off on every replica, every video
// under 50 MiB imported as a feature film, with nothing in the logs.
// importarr's own TestWorkersGetTheSampleSizeFloor holds the next link,
// Options to both workers.
func TestImportarrSampleMaxBytesReachesTheOptions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  int64
	}{
		{name: "no flag keeps the 50 MiB default", want: fsops.DefaultSampleMaxBytes},
		{name: "an explicit floor", flags: []string{"--sample-max-bytes", "1048576"}, want: 1 << 20},
		{name: "0 disables the rule", flags: []string{"--sample-max-bytes=0"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := stub(t, &runImportarr)
			args := append([]string{"importarr", "--role", "worker", "--namespace", "clustarr"}, tc.flags...)
			if _, err := execute(t, args...); err != nil {
				t.Fatalf("clustarr %v: %v", args, err)
			}
			if got.SampleMaxBytes != tc.want {
				t.Errorf("importarr.Options.SampleMaxBytes = %d, want %d", got.SampleMaxBytes, tc.want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("the parsed options are invalid: %v", err)
			}
		})
	}

	t.Run("a negative floor is refused", func(t *testing.T) {
		got := stub(t, &runImportarr)
		if _, err := execute(t, "importarr", "--role", "worker", "--namespace", "clustarr",
			"--sample-max-bytes=-1"); err != nil {
			t.Fatalf("clustarr importarr: %v", err)
		}
		if err := got.Validate(); err == nil || !strings.Contains(err.Error(), "sample-max-bytes") {
			t.Errorf("Validate() = %v, want an error naming --sample-max-bytes", err)
		}
	})

	// `clustarr all` starts from importarr.DefaultOptions(), so it keeps the
	// default without a flag of its own -- asserted through its own closure,
	// not a restatement of it.
	t.Run("clustarr all", func(t *testing.T) {
		got := stub(t, &runImportarr)
		if err := allServiceRun(t, "importarr")(context.Background(), k8s.DefaultOptions()); err != nil {
			t.Fatalf("run: %v", err)
		}
		if got.SampleMaxBytes != fsops.DefaultSampleMaxBytes {
			t.Errorf("`clustarr all` gives importarr SampleMaxBytes = %d, want %d",
				got.SampleMaxBytes, fsops.DefaultSampleMaxBytes)
		}
	})

	// And through what ships: both importarr Deployments' own argv.
	t.Run("config/manager", func(t *testing.T) {
		t.Setenv(natsURLEnv, "nats://nats.clustarr-system.svc:4222")
		t.Setenv(namespaceEnv, "clustarr-system")
		var checked int
		for _, file := range []string{"importarr.yaml", "importarr-worker.yaml"} {
			for _, d := range deploymentsIn(t, filepath.Join("../../config/manager", file)) {
				argv := d.Spec.Template.Spec.Containers[0].Args
				got := stub(t, &runImportarr)
				if _, err := execute(t, argv...); err != nil {
					t.Fatalf("clustarr %v: %v", argv, err)
				}
				if got.SampleMaxBytes != fsops.DefaultSampleMaxBytes {
					t.Errorf("Deployment %q runs importarr with SampleMaxBytes = %d, want the %d default",
						d.Name, got.SampleMaxBytes, fsops.DefaultSampleMaxBytes)
				}
				checked++
			}
		}
		if checked != 2 {
			t.Fatalf("checked %d importarr Deployments, want 2 (importarr, importarr-worker)", checked)
		}
	})
}
