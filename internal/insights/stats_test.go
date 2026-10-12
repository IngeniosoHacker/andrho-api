package insights

import (
	"math"
	"testing"
)

func TestStats(t *testing.T) {
	if c, ok := Change(125, 100); !ok || math.Abs(c-0.25) > 1e-9 {
		t.Errorf("Change = %v", c)
	}
	if _, ok := Change(5, 0); ok {
		t.Error("change from 0 must not be meaningful")
	}
	if z, ok := ZScore([]float64{10, 11, 9, 10, 10, 11, 9, 10, 30}); !ok || z < 5 {
		t.Errorf("spike z = %v", z)
	}
	if _, ok := ZScore([]float64{1, 2, 3}); ok {
		t.Error("short series")
	}
	if s := Slope([]float64{1, 2, 3, 4}); math.Abs(s-1) > 1e-9 {
		t.Errorf("Slope = %v", s)
	}
	if DaysOfCover(30, 3) != 10 || DaysOfCover(5, 0) != -1 {
		t.Error("DaysOfCover")
	}
}
