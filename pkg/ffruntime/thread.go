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

package ffruntime

import (
	"runtime"
	"sync"
)

// callFiles is the file each FFmpeg call in flight reads, by the OS thread
// the call runs on (DoOn).
var callFiles sync.Map // int -> string

// onThread pins the calling goroutine to its OS thread and says file is
// what that thread's FFmpeg calls read, until release. FFmpeg's log callback
// is one for the process and is handed no file: a demuxer's complaint
// arrives as "0x00 at pos 618735295 invalid as first byte of an EBML number"
// and nothing else, so with two import pods reading eleven damaged episodes
// end to end no line said which file it was about (talos, 2026-11-27;
// owner: "Do all 3"). FFmpeg calls the callback on the thread that made the
// call, so the thread is the link. A line from a thread FFmpeg started
// itself -- a decoder's frame or slice worker -- matches no call and
// carries no file, rather than a guess.
func onThread(file string) (release func()) {
	if file == "" {
		return func() {}
	}
	runtime.LockOSThread()
	tid := threadID()
	if tid == 0 {
		runtime.UnlockOSThread()
		return func() {}
	}
	callFiles.Store(tid, file)
	return func() {
		callFiles.Delete(tid)
		runtime.UnlockOSThread()
	}
}

// fileOnThread is the file the calling thread's FFmpeg call reads, or "".
func fileOnThread() string {
	tid := threadID()
	if tid == 0 {
		return ""
	}
	if f, ok := callFiles.Load(tid); ok {
		return f.(string)
	}
	return ""
}
