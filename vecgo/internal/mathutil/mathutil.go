package mathutil

import "math"

// DotProduct computes the dot product of two vectors.
func DotProduct(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// Norm computes the L2 norm (magnitude) of a vector.
func Norm(v []float32) float32 {
	return float32(math.Sqrt(float64(DotProduct(v, v))))
}

// Normalize returns a unit vector in the same direction.
func Normalize(v []float32) []float32 {
	norm := Norm(v)
	if norm == 0 {
		return v
	}
	result := make([]float32, len(v))
	for i := range v {
		result[i] = v[i] / norm
	}
	return result
}

// CosineSimilarity computes cosine similarity between two vectors.
// Returns 1 for identical directions, 0 for perpendicular, -1 for opposite.
func CosineSimilarity(a, b []float32) float32 {
	dot := DotProduct(a, b)
	normA := Norm(a)
	normB := Norm(b)
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (normA * normB)
}

// CosineDistance converts cosine similarity to a distance metric.
// Returns 0 for identical vectors, 2 for opposite vectors.
func CosineDistance(a, b []float32) float32 {
	return 1 - CosineSimilarity(a, b)
}
