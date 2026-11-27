/*
Copyright 2026 The clustarr Authors.

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

package usenet

import (
	"encoding/binary"
	"hash/crc32"
)

// storedRarVolumes packs payload as the one file name of a RAR 3 archive,
// stored (method 0x30, no compression) and split into volumes carrying at
// most perVolume bytes of it each: what a scene release's x.rar, x.r00,
// x.r01, ... are. The main header says old numbering unless newNumbering,
// which is what decides the name an unpacker looks for the next volume
// under. The repository has no RAR writer and may ship none (the format's
// compressor is not free); a stored archive needs only the block layout,
// which unrar's technote gives and `unrar t` accepts.
func storedRarVolumes(name string, payload []byte, perVolume int, newNumbering bool) [][]byte {
	const (
		blockMain, blockFile, blockEnd = 0x73, 0x74, 0x7b
		mainVolume, mainNewNumbering   = 0x0001, 0x0010
		mainFirstVolume                = 0x0100
		fileSplitBefore, fileSplitNext = 0x0001, 0x0002
		longBlock, skipIfUnknown       = 0x8000, 0x4000
		endNextVolume                  = 0x0001
	)
	block := func(typ byte, flags uint16, body []byte) []byte {
		b := make([]byte, 7, 7+len(body))
		b[2] = typ
		binary.LittleEndian.PutUint16(b[3:], flags)
		binary.LittleEndian.PutUint16(b[5:], uint16(7+len(body)))
		b = append(b, body...)
		binary.LittleEndian.PutUint16(b[0:], uint16(crc32.ChecksumIEEE(b[2:])))
		return b
	}
	var parts [][]byte
	for rest := payload; ; {
		n := min(perVolume, len(rest))
		parts = append(parts, rest[:n])
		if rest = rest[n:]; len(rest) == 0 {
			break
		}
	}
	vols := make([][]byte, 0, len(parts))
	for i, part := range parts {
		last := i == len(parts)-1
		v := []byte("Rar!\x1a\x07\x00")

		mainFlags := uint16(mainVolume)
		if i == 0 {
			mainFlags |= mainFirstVolume
		}
		if newNumbering {
			mainFlags |= mainNewNumbering
		}
		v = append(v, block(blockMain, mainFlags, make([]byte, 6))...)

		// A part that continues in the next volume carries the checksum of
		// its own bytes; the last carries the whole file's.
		fileFlags, sum := uint16(longBlock), crc32.ChecksumIEEE(payload)
		if i > 0 {
			fileFlags |= fileSplitBefore
		}
		if !last {
			fileFlags |= fileSplitNext
			sum = crc32.ChecksumIEEE(part)
		}
		body := make([]byte, 25, 25+len(name))
		binary.LittleEndian.PutUint32(body[0:], uint32(len(part)))    // packed size
		binary.LittleEndian.PutUint32(body[4:], uint32(len(payload))) // unpacked size
		body[8] = 3                                                   // host OS: Unix
		binary.LittleEndian.PutUint32(body[9:], sum)
		binary.LittleEndian.PutUint32(body[13:], 0x5b7b0000) // 2025-11-27 00:00, DOS time
		body[17], body[18] = 29, 0x30                        // version to unpack, stored
		binary.LittleEndian.PutUint16(body[19:], uint16(len(name)))
		binary.LittleEndian.PutUint32(body[21:], 0o100644)
		body = append(body, name...)
		v = append(v, block(blockFile, fileFlags, body)...)
		v = append(v, part...)

		endFlags := uint16(skipIfUnknown)
		if !last {
			endFlags |= endNextVolume
		}
		v = append(v, block(blockEnd, endFlags, nil)...)
		vols = append(vols, v)
	}
	return vols
}
