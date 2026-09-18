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

package logging

import (
	"log/slog"

	"github.com/go-logr/logr"
)

// LogrBridge adapts l to a logr.Logger via logr.FromSlogHandler.
//
// controller-runtime speaks logr, not slog. A service passes the result of
// this call to ctrl.SetLogger once at startup so controller-runtime's own
// log lines join the same slog stream, in the same format, as the rest of
// the service, instead of going to a separately configured logr backend.
func LogrBridge(l *slog.Logger) logr.Logger {
	return logr.FromSlogHandler(l.Handler())
}
