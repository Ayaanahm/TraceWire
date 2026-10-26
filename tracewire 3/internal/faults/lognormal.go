package faults

import "math"

// lognormal returns a sample with median 1 and sigma 0.5, using Box-Muller
// over the injected uniform source so tests stay deterministic.
func lognormal(u func() float64) float64 {
	u1 := u()
	if u1 < 1e-12 {
		u1 = 1e-12
	}
	z := math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u())
	return math.Exp(0.5 * z)
}
