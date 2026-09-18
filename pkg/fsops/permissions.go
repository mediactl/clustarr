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

package fsops

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// geteuid and chownFunc are seams: white-box tests override both so the
// root and non-root branches are exercised deterministically regardless
// of the UID the test binary actually runs as.
var (
	geteuid   = os.Geteuid
	chownFunc = os.Chown
)

// SetPermissions applies fileMode to every regular file and dirMode to
// every directory at or under path. uid and gid are passed to os.Chown
// verbatim (-1 leaves that ID unchanged, os.Chown's own convention --
// RootFolderSpec.Permissions has no UID field, only Group *int64, so
// every real caller passes uid=-1). Ownership changes are skipped
// entirely -- a silent no-op, chmod still applies -- when the process's
// effective UID is not 0, since chown to an arbitrary UID/GID requires
// root or CAP_CHOWN; the RWX volume's fsGroup/UMASK 002 setup (spec §11)
// is the primary permission mechanism and this is only a best-effort
// supplement for a root-running worker.
func SetPermissions(path string, fileMode, dirMode os.FileMode, uid, gid int) error {
	root := geteuid() == 0
	return filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		mode := fileMode
		if d.IsDir() {
			mode = dirMode
		}
		if err := os.Chmod(p, mode); err != nil {
			return fmt.Errorf("fsops: chmod %s: %w", p, err)
		}
		if !root || (uid < 0 && gid < 0) {
			return nil
		}
		if err := chownFunc(p, uid, gid); err != nil {
			return fmt.Errorf("fsops: chown %s: %w", p, err)
		}
		return nil
	})
}
