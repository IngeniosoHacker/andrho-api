package insights

import "math"

// The statistics behind the findings. Plain and explainable on purpose:
// the AI only puts them into words.

// Change is the relative change from before to now (0.25 = +25%), and
// whether it's meaningful (before > 0).
func Change(now, before float64) (float64, bool) {
	if before <= 0 {
		return 0, false
	}
	return (now - before) / before, true
}

// MeanStd of xs (population std).
func MeanStd(xs []float64) (mean, std float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	for _, x := range xs {
		mean += x
	}
	mean /= float64(len(xs))
	for _, x := range xs {
		std += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(std / float64(len(xs)))
}

// ZScore of the last value against the ones before it; ok=false when there
// isn't enough history or no variation.
func ZScore(series []float64) (float64, bool) {
	if len(series) < 8 {
		return 0, false
	}
	mean, std := MeanStd(series[:len(series)-1])
	if std == 0 {
		return 0, false
	}
	return (series[len(series)-1] - mean) / std, true
}

// Slope of the least-squares line through series (units per step).
func Slope(series []float64) float64 {
	n := float64(len(series))
	if n < 2 {
		return 0
	}
	var sx, sy, sxy, sxx float64
	for i, y := range series {
		x := float64(i)
		sx, sy, sxy, sxx = sx+x, sy+y, sxy+x*y, sxx+x*x
	}
	d := n*sxx - sx*sx
	if d == 0 {
		return 0
	}
	return (n*sxy - sx*sy) / d
}

// DaysOfCover is how many days stock lasts at the daily sales rate
// (-1 when nothing sells).
func DaysOfCover(stock, soldPerDay float64) float64 {
	if soldPerDay <= 0 {
		return -1
	}
	return stock / soldPerDay
}

// Sum of xs.
func Sum(xs []float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}
