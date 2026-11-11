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

// Package ortlog routes ONNX Runtime's own log lines into a process's
// logger under the onnx subsystem (the 2026-11-10 logging design §4),
// through the custom logger the mediactl/onnxruntime-purego fork binds
// (NewEnvWithLogger, Env.SetLogLevel). Only the markers binary and the
// caption agent link it; cmd/manager and cmd/ui link no purego.
package ortlog

import (
	"context"
	"log/slog"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Subsystem is ONNX Runtime's.
const Subsystem = "onnx"

// Level is the record's level for one of ORT's severities: verbose TRACE,
// info INFO, warning WARN, error ERROR, fatal FATAL.
func Level(severity ort.LoggingLevel) slog.Level {
	switch severity {
	case ort.LoggingLevelVerbose:
		return logging.LevelTrace
	case ort.LoggingLevelInfo:
		return slog.LevelInfo
	case ort.LoggingLevelWarning:
		return slog.LevelWarn
	case ort.LoggingLevelError:
		return slog.LevelError
	default:
		return logging.LevelFatal
	}
}

// logFunc writes each of ORT's lines through l.
func logFunc(l *slog.Logger) ort.LogFunc {
	return func(severity ort.LoggingLevel, category, logID, codeLocation, message string) {
		l.Log(context.Background(), Level(severity), message,
			"category", category, "logID", logID, "codeLocation", codeLocation)
	}
}

// NewEnv creates rt's environment with its lines going to ctx's logger under
// the onnx subsystem. The env is created at ORT's warning severity and raised
// to verbose while an onnx: debug (or trace) override is in force, so the
// override reaches ORT's own messages; the callback's record is gated by the
// subsystem's level either way. stop ends the watch; call it when the env is
// closed.
func NewEnv(ctx context.Context, rt *ort.Runtime, logID string) (env *ort.Env, stop func(), err error) {
	l := logging.SubsystemLogger(logging.FromContext(ctx), Subsystem)
	env, err = rt.NewEnvWithLogger(logID, ort.LoggingLevelWarning, logFunc(l))
	if err != nil {
		return nil, nil, err
	}
	stop = logging.Watch(ctx, Subsystem, slog.LevelDebug, func(on bool) {
		level := ort.LoggingLevelWarning
		if on {
			level = ort.LoggingLevelVerbose
		}
		if err := env.SetLogLevel(level); err != nil {
			l.Warn("onnx: ONNX Runtime refused the log level", "err", err)
		}
	})
	return env, stop, nil
}
