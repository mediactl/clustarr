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

package rootfolder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPathAccessibleDirectory(t *testing.T) {
	dir := t.TempDir()
	accessible, free, total, err := checkPath(dir)
	if err != nil {
		t.Fatalf("checkPath: %v", err)
	}
	if !accessible {
		t.Error("a writable temp dir reported not accessible")
	}
	if total <= 0 || free <= 0 || free > total {
		t.Errorf("free=%d total=%d, want 0 < free <= total", free, total)
	}
}

func TestCheckPathMissingDirectory(t *testing.T) {
	accessible, _, _, err := checkPath(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("a missing directory returned no error")
	}
	if accessible {
		t.Error("a missing directory reported accessible")
	}
}

func TestCheckPathReadOnlyDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) }) // let t.TempDir clean up
	accessible, _, _, err := checkPath(dir)
	if err == nil {
		t.Fatal("a read-only directory returned no error")
	}
	if accessible {
		t.Error("a read-only directory reported accessible")
	}
}
