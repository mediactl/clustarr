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

// Package subarchivetest builds the ZIP and RAR archives subtitle provider
// tests serve, in memory, so no binary archive has to live under test/data/.
package subarchivetest

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

// Zip returns a ZIP archive of the given name, content pairs, in order.
func Zip(t testing.TB, nameContent ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for i := 0; i+1 < len(nameContent); i += 2 {
		w, err := zw.Create(nameContent[i])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(nameContent[i+1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Rar returns a RAR 4.x archive of the given name, content pairs, each
// stored uncompressed (method 0x30). Go has no RAR writer, so the blocks
// are laid out by hand from the RAR 4.x technote: a marker block, an
// archive header, one file header plus data per file, and an end block,
// each header carrying the low 16 bits of the CRC-32 of its bytes from the
// type field on.
func Rar(t testing.TB, nameContent ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("Rar!\x1a\x07\x00")
	writeBlock(&buf, 0x73, 0x0000, make([]byte, 6)) // archive header: two reserved fields
	for i := 0; i+1 < len(nameContent); i += 2 {
		name, data := nameContent[i], []byte(nameContent[i+1])
		var h bytes.Buffer
		le := binary.LittleEndian
		_ = binary.Write(&h, le, uint32(len(data)))        // PACK_SIZE
		_ = binary.Write(&h, le, uint32(len(data)))        // UNP_SIZE
		h.WriteByte(3)                                     // HOST_OS: Unix
		_ = binary.Write(&h, le, crc32.ChecksumIEEE(data)) // FILE_CRC
		_ = binary.Write(&h, le, uint32(0))                // FTIME
		h.WriteByte(29)                                    // UNP_VER 2.9
		h.WriteByte(0x30)                                  // METHOD: store
		_ = binary.Write(&h, le, uint16(len(name)))        // NAME_SIZE
		_ = binary.Write(&h, le, uint32(0x20))             // ATTR
		h.WriteString(name)
		writeBlock(&buf, 0x74, 0x8000, h.Bytes()) // 0x8000: PACK_SIZE present, data follows
		buf.Write(data)
	}
	writeBlock(&buf, 0x7b, 0x0000, nil)
	return buf.Bytes()
}

// writeBlock writes one RAR 4.x block header: HEAD_CRC, HEAD_TYPE,
// HEAD_FLAGS, HEAD_SIZE, then rest.
func writeBlock(buf *bytes.Buffer, typ byte, flags uint16, rest []byte) {
	var h bytes.Buffer
	h.WriteByte(typ)
	_ = binary.Write(&h, binary.LittleEndian, flags)
	_ = binary.Write(&h, binary.LittleEndian, uint16(7+len(rest)))
	h.Write(rest)
	_ = binary.Write(buf, binary.LittleEndian, uint16(crc32.ChecksumIEEE(h.Bytes())))
	buf.Write(h.Bytes())
}
