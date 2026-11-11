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

package ortlog

import (
	"bytes"
	"context"
	"strings"
	"testing"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// ORT's five severities are the record's levels (design §4), under the onnx
// subsystem with the line's category, log id and code location as
// attributes.
func TestORTSeverityNames(t *testing.T) {
	var buf bytes.Buffer
	root := logging.New(logging.Options{Output: &buf, Level: logging.LevelTrace})
	fn := logFunc(logging.SubsystemLogger(root, Subsystem))
	for sev, want := range map[ort.LoggingLevel]string{
		ort.LoggingLevelVerbose: "TRACE", ort.LoggingLevelInfo: "INFO", ort.LoggingLevelWarning: "WARN",
		ort.LoggingLevelError: "ERROR", ort.LoggingLevelFatal: "FATAL",
	} {
		buf.Reset()
		fn(sev, "onnxruntime", "clustarr-ocr", "session.cc:123 Load", "model loaded")
		line := buf.String()
		require.Contains(t, line, `"level":"`+want+`"`, line)
		require.Contains(t, line, `"subsystem":"onnx"`)
		require.Contains(t, line, `"msg":"model loaded"`)
		require.Contains(t, line, `"category":"onnxruntime"`)
		require.Contains(t, line, `"logID":"clustarr-ocr"`)
		require.Contains(t, line, `"codeLocation":"session.cc:123 Load"`)
	}
	require.Equal(t, 1, strings.Count(buf.String(), "\n"))
	_ = context.Background()
}
