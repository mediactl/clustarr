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

package chromaprint

import (
	"math"
	"math/bits"
)

// fftPlan computes the power spectrum of a real frame of n samples through
// an n/2-point complex FFT (iterative radix-2) and the standard split of
// its output into the real input's spectrum: half the work of a full
// complex transform of real data.
type fftPlan struct {
	n, m       int // n real samples, m = n/2 complex points
	rev        []int
	twr, twi   []float64 // the m-point FFT's twiddles, e^{-2*pi*i*k/m}
	splr, spli []float64 // the split's twiddles, e^{-2*pi*i*k/n}
	re, im     []float64
}

func newFFT(n int) *fftPlan {
	m := n / 2
	p := &fftPlan{
		n: n, m: m, rev: make([]int, m),
		twr: make([]float64, m/2), twi: make([]float64, m/2),
		splr: make([]float64, m+1), spli: make([]float64, m+1),
		re: make([]float64, m), im: make([]float64, m),
	}
	shift := 64 - bits.TrailingZeros(uint(m))
	for i := range m {
		p.rev[i] = int(bits.Reverse64(uint64(i)) >> shift)
	}
	for k := range m / 2 {
		a := -2 * math.Pi * float64(k) / float64(m)
		p.twr[k], p.twi[k] = math.Cos(a), math.Sin(a)
	}
	for k := 0; k <= m; k++ {
		a := -2 * math.Pi * float64(k) / float64(n)
		p.splr[k], p.spli[k] = math.Cos(a), math.Sin(a)
	}
	return p
}

// power writes |X[k]|^2 for k in [0, n/2] of the real input x.
func (p *fftPlan) power(x, out []float64) {
	m, re, im, rev := p.m, p.re, p.im, p.rev
	for i := range m {
		j := rev[i]
		re[j], im[j] = x[2*i], x[2*i+1]
	}
	twr, twi := p.twr, p.twi
	for size := 2; size <= m; size <<= 1 {
		half, step := size/2, m/size
		for start := 0; start < m; start += size {
			for k := range half {
				wr, wi := twr[k*step], twi[k*step]
				i, j := start+k, start+k+half
				tr := wr*re[j] - wi*im[j]
				ti := wr*im[j] + wi*re[j]
				re[j], im[j] = re[i]-tr, im[i]-ti
				re[i], im[i] = re[i]+tr, im[i]+ti
			}
		}
	}
	// X[k] = E[k] + W^k O[k], E = (Z[k] + conj Z[m-k])/2, O = (Z[k] - conj Z[m-k])/2i.
	for k := 0; k <= m; k++ {
		zr, zi := re[k%m], im[k%m]
		cr, ci := re[(m-k)%m], -im[(m-k)%m]
		er, ei := (zr+cr)/2, (zi+ci)/2
		or, oi := (zi-ci)/2, -(zr-cr)/2
		wr, wi := p.splr[k], p.spli[k]
		xr := er + wr*or - wi*oi
		xi := ei + wr*oi + wi*or
		out[k] = xr*xr + xi*xi
	}
}
