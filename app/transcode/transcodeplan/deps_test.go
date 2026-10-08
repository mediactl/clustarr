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

package transcodeplan

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTranscodePlanIsPure holds §3.1 ("transcodeplan (pure)") and §5.3 (it
// never imports admission): no client, no bus connection, no filesystem --
// the adapter in app/remediation/transcode gathers everything Plan reads.
// It reads the package's own imports, test files aside.
func TestTranscodePlanIsPure(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").CombinedOutput()
	require.NoError(t, err, string(out))
	imports := strings.Fields(string(out))
	require.Contains(t, imports, "github.com/mediactl/clustarr/pkg/transcode/standard", "the guard reads the real import list")
	for _, imp := range imports {
		for _, bad := range []string{
			"os", "io/fs", "net/http", "sigs.k8s.io/controller-runtime", "k8s.io/client-go",
			"github.com/nats-io/nats.go", "github.com/mediactl/clustarr/pkg/events/natsbus",
			"github.com/mediactl/clustarr/pkg/events/membus", "github.com/mediactl/clustarr/pkg/k8s",
			"github.com/mediactl/clustarr/app/transcode/admission", "github.com/mediactl/clustarr/app/transcode/worker",
			"github.com/mediactl/clustarr/app/remediation",
		} {
			assert.False(t, imp == bad || strings.HasPrefix(imp, bad+"/"), "transcodeplan imports %s", imp)
		}
	}
}
