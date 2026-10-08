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

// Command agent runs one Clustarr worker domain per process (spec §3.5).
package main

import (
	"github.com/mediactl/clustarr/internal/cli"
	"github.com/mediactl/clustarr/internal/cli/agent"
	"github.com/mediactl/clustarr/pkg/par2child"
)

func main() {
	// The usenet engine's par2 repair runs in a child of this binary
	// ("agent par2-repair", pkg/par2child): dispatched before cobra, flags,
	// logging or tracing exist (split spec 2026-10-06 §8.4.4).
	par2child.RunIfChild()
	cli.Main(agent.NewCommand)
}
