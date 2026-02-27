package mathutil

import (
	"math"
	"testing"
)

func TestDotProduct(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{4, 5, 6}
	got := DotProduct(a, b)
	want := float32(32) // 1*4 + 2*5 + 3*6
	if got != want {
		t.Errorf("DotProduct(%v, %v) = %v, want %v", a, b, got, want)
	}
}

func TestNorm(t *testing.T) {
	v := []float32{3, 4}
	got := Norm(v)
	want := float32(5) // sqrt(9+16)
	if math.Abs(float64(got-want)) > 0.0001 {
		t.Errorf("Norm(%v) = %v, want %v", v, got, want)
	}
}

func TestNormalize(t *testing.T) {
	v := []float32{3, 4}
	got := Normalize(v)
	// Should be [0.6, 0.8]
	if math.Abs(float64(got[0]-0.6)) > 0.0001 || math.Abs(float64(got[1]-0.8)) > 0.0001 {
		t.Errorf("Normalize(%v) = %v, want [0.6, 0.8]", v, got)
	}
}

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{0, 1}
	got := CosineSimilarity(a, b)
	want := float32(0) // Perpendicular vectors
	if math.Abs(float64(got-want)) > 0.0001 {
		t.Errorf("CosineSimilarity(%v, %v) = %v, want %v", a, b, got, want)
	}

	// Same direction
	c := []float32{1, 1}
	d := []float32{2, 2}
	got2 := CosineSimilarity(c, d)
	if math.Abs(float64(got2-1.0)) > 0.0001 {
		t.Errorf("CosineSimilarity(%v, %v) = %v, want 1.0", c, d, got2)
	}
}

func TestCosineDistance(t *testing.T) {
	a := []float32{1, 0}
	b := []float32{1, 0}
	got := CosineDistance(a, b)
	want := float32(0) // Same vector = 0 distance
	if math.Abs(float64(got-want)) > 0.0001 {
		t.Errorf("CosineDistance(%v, %v) = %v, want %v", a, b, got, want)
	}
}
