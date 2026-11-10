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

package mediainfo

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// ErrNoPageCount is PageCount's answer for a file that is no comic archive
// or PDF.
var ErrNoPageCount = errors.New("mediainfo: no page count for this file type")

// pageImageExtensions are the entries of a comic archive that are pages.
var pageImageExtensions = map[string]bool{
	".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".bmp": true, ".avif": true, ".jxl": true,
}

// pdfPageRE is a PDF page object's type entry; "/Type /Pages" is the tree
// node and is not a page.
var pdfPageRE = regexp.MustCompile(`/Type\s*/Page\b`)

// maxPDFScan bounds the PDF scan.
const maxPDFScan = 512 << 20

// PageCount is the number of pages of a comic archive (its image entries,
// by listing alone: nothing is extracted) or a PDF (its page objects), for
// MediaInfo.PageCount (2026-11-10 design D9). ErrNoPageCount for any other
// file; a .cb7 or .cbr the library cannot open is an error, never a guess.
func PageCount(path string) (int32, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cbz":
		return zipPages(path)
	case ".cbr":
		return rarPages(path)
	case ".cb7":
		return sevenZipPages(path)
	case ".cbt":
		return tarPages(path)
	case ".pdf":
		return pdfPages(path)
	}
	return 0, ErrNoPageCount
}

func isPage(name string) bool {
	base := filepath.Base(name)
	if strings.HasPrefix(base, ".") || strings.HasPrefix(base, "__MACOSX") || strings.Contains(name, "__MACOSX/") {
		return false
	}
	return pageImageExtensions[strings.ToLower(filepath.Ext(base))]
}

func zipPages(path string) (int32, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("mediainfo: open %s: %w", path, err)
	}
	defer r.Close()
	var n int32
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && isPage(f.Name) {
			n++
		}
	}
	return n, nil
}

func rarPages(path string) (int32, error) {
	r, err := rardecode.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("mediainfo: open %s: %w", path, err)
	}
	defer r.Close()
	var n int32
	for {
		h, err := r.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("mediainfo: list %s: %w", path, err)
		}
		if !h.IsDir && isPage(h.Name) {
			n++
		}
	}
}

func sevenZipPages(path string) (int32, error) {
	r, err := sevenzip.OpenReader(path)
	if err != nil {
		return 0, fmt.Errorf("mediainfo: open %s: %w", path, err)
	}
	defer r.Close()
	var n int32
	for _, f := range r.File {
		if !f.FileInfo().IsDir() && isPage(f.Name) {
			n++
		}
	}
	return n, nil
}

func tarPages(path string) (int32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("mediainfo: open %s: %w", path, err)
	}
	defer f.Close()
	tr := tar.NewReader(bufio.NewReader(f))
	var n int32
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return n, nil
		}
		if err != nil {
			return 0, fmt.Errorf("mediainfo: list %s: %w", path, err)
		}
		if h.Typeflag == tar.TypeReg && isPage(h.Name) {
			n++
		}
	}
}

// pdfPages counts the page objects a PDF declares, scanning at most
// maxPDFScan bytes in chunks with a tail carried over so a token split
// across two chunks is still seen once.
func pdfPages(path string) (int32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("mediainfo: open %s: %w", path, err)
	}
	defer f.Close()
	const chunk = 1 << 20
	const tail = 32
	buf := make([]byte, chunk+tail)
	var carry []byte
	var n int32
	var read int64
	for read < maxPDFScan {
		copy(buf, carry)
		k, err := io.ReadFull(f, buf[len(carry):chunk+len(carry)])
		window := buf[:len(carry)+k]
		read += int64(k)
		// Count the matches whose start lies past the carried tail, so a
		// match wholly inside the tail is not counted twice.
		for _, m := range pdfPageRE.FindAllIndex(window, -1) {
			if m[0] >= len(carry)-tail || len(carry) < tail {
				n++
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return n, nil
			}
			return 0, fmt.Errorf("mediainfo: read %s: %w", path, err)
		}
		if len(window) >= tail {
			carry = append(carry[:0], window[len(window)-tail:]...)
		} else {
			carry = append(carry[:0], window...)
		}
	}
	return n, nil
}
