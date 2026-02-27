package embedder

import (
	"context"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

// TFIDF is a TF-IDF based text embedder.
type TFIDF struct {
	vocabulary map[string]int // word -> index
	idf        []float32      // IDF values
	maxDims    int
	trained    bool
	mu         sync.RWMutex
}

// NewTFIDF creates a new TF-IDF embedder with max vocabulary size.
func NewTFIDF(maxDims int) *TFIDF {
	if maxDims <= 0 {
		maxDims = 4096
	}
	return &TFIDF{
		vocabulary: make(map[string]int),
		maxDims:    maxDims,
	}
}

// Train builds the vocabulary from a corpus.
func (t *TFIDF) Train(documents []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Count document frequency for each term
	df := make(map[string]int)
	for _, doc := range documents {
		seen := make(map[string]bool)
		for _, word := range tokenize(doc) {
			if !seen[word] {
				df[word]++
				seen[word] = true
			}
		}
	}

	// Sort by frequency and take top maxDims
	type wordFreq struct {
		word string
		freq int
	}
	wf := make([]wordFreq, 0, len(df))
	for w, f := range df {
		wf = append(wf, wordFreq{w, f})
	}
	sort.Slice(wf, func(i, j int) bool {
		return wf[i].freq > wf[j].freq
	})

	if len(wf) > t.maxDims {
		wf = wf[:t.maxDims]
	}

	// Build vocabulary and IDF
	t.vocabulary = make(map[string]int)
	t.idf = make([]float32, len(wf))
	n := float64(len(documents))

	for i, w := range wf {
		t.vocabulary[w.word] = i
		// IDF = log(N / df)
		t.idf[i] = float32(math.Log(n / float64(w.freq)))
	}

	t.trained = true
	return nil
}

// Embed converts texts to TF-IDF vectors.
func (t *TFIDF) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	t.mu.RLock()
	trained := t.trained
	t.mu.RUnlock()

	if !trained {
		// Auto-train on provided texts
		t.Train(texts)
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	dims := len(t.vocabulary)
	vectors := make([][]float32, len(texts))

	for i, text := range texts {
		vec := make([]float32, dims)
		words := tokenize(text)

		// Count term frequency
		tf := make(map[string]int)
		for _, w := range words {
			tf[w]++
		}

		// Compute TF-IDF
		for word, count := range tf {
			if idx, ok := t.vocabulary[word]; ok {
				// TF = count / total words
				tfVal := float32(count) / float32(len(words))
				vec[idx] = tfVal * t.idf[idx]
			}
		}

		// Normalize
		var norm float32
		for _, v := range vec {
			norm += v * v
		}
		if norm > 0 {
			norm = float32(math.Sqrt(float64(norm)))
			for j := range vec {
				vec[j] /= norm
			}
		}

		vectors[i] = vec
	}

	return vectors, nil
}

// Dimensions returns the vocabulary size.
func (t *TFIDF) Dimensions() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.vocabulary)
}

// Name returns the embedder name.
func (t *TFIDF) Name() string {
	return "tfidf"
}

// tokenize splits text into lowercase words.
func tokenize(text string) []string {
	var words []string
	var word strings.Builder

	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			word.WriteRune(r)
		} else if word.Len() > 0 {
			words = append(words, word.String())
			word.Reset()
		}
	}
	if word.Len() > 0 {
		words = append(words, word.String())
	}

	return words
}
