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

package agent

// agentServiceName is the process's service.name, on its records and its
// spans (the 2026-11-10 logging design §3): agent-<domain>, except that an
// engine is named for the DownloadClient it runs, engine-<client>, since a
// cluster runs one engine process per client.
func agentServiceName(domain, engine string) string {
	switch domain {
	case "torrent-engine", "usenet-engine":
		if engine != "" {
			return "engine-" + engine
		}
	}
	return "agent-" + domain
}
