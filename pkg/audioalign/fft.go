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

package audioalign

import (
	"math"
	"math/bits"
	"sync"
)

// fft transforms x in place (radix-2, len(x) a power of two); inverse
// computes the unscaled inverse transform.
func fft(x []complex128, inverse bool) {
	n := len(x)
	if n <= 1 {
		return
	}
	shift := 64 - bits.TrailingZeros(uint(n))
	for i := 0; i < n; i++ {
		j := int(bits.Reverse64(uint64(i)) >> shift)
		if j > i {
			x[i], x[j] = x[j], x[i]
		}
	}
	tw := twiddles(n)
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				w := tw[k*step]
				if inverse {
					w = complex(real(w), -imag(w))
				}
				a, b := x[start+k], x[start+k+half]*w
				x[start+k], x[start+k+half] = a+b, a-b
			}
		}
	}
}

var twiddleCache sync.Map // int -> []complex128

func twiddles(n int) []complex128 {
	if t, ok := twiddleCache.Load(n); ok {
		return t.([]complex128)
	}
	t := make([]complex128, n/2)
	for k := range t {
		a := -2 * math.Pi * float64(k) / float64(n)
		t[k] = complex(math.Cos(a), math.Sin(a))
	}
	twiddleCache.Store(n, t)
	return t
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}
