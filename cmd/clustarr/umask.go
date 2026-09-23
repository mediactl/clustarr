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
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// umaskEnv is design §11's UMASK: every media-touching pod creates files
// with UMASK 002, so the other services -- running as other users in the
// shared fsGroup -- can rewrite what one wrote. config/manager and the chart
// set it on every Deployment that mounts /data; squasharr passes it on to
// its transcode Jobs and grabarr to its engine workloads.
const umaskEnv = "UMASK"

// parseUmask reads an octal umask such as "002", "0002" or "0o002". Empty
// means "leave the process umask alone" (ok false). Anything that is not
// octal, or is outside 0..0777, is an error rather than a guess: a typo'd
// UMASK silently falling back to the image default is exactly the class of
// permission bug §11 exists to prevent.
func parseUmask(s string) (mask int, ok bool, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false, nil
	}
	digits := strings.TrimPrefix(strings.TrimPrefix(s, "0o"), "0O")
	v, err := strconv.ParseUint(digits, 8, 32)
	if err != nil || v > 0o777 {
		return 0, false, fmt.Errorf("$%s=%q is not an octal umask between 000 and 777", umaskEnv, s)
	}
	return int(v), true, nil
}

// applyUmaskFromEnv sets the process umask from $UMASK before any service
// starts. (syscall.Umask is Unix-only; so is the rest of the tree --
// pkg/fsops reads syscall.Stat_t.) Every subcommand -- each service, the engines
// and the transcode worker -- inherits it from the root command, which is
// how "every service" in §11 is met by one binary.
func applyUmaskFromEnv() error {
	mask, ok, err := parseUmask(os.Getenv(umaskEnv))
	if err != nil || !ok {
		return err
	}
	syscall.Umask(mask)
	return nil
}
