package compress

import "fmt"

// Config controls the compression behaviour of [Document].
type Config struct {
	// MinSimilarity is the minimum Jaccard similarity (0..1) for two schemas
	// to be considered for merging. 1.0 means exact equality only (default).
	// Values below 1.0 allow merging schemas with significant structural overlap.
	MinSimilarity float64

	// SimilarityStep is the amount by which the similarity threshold is reduced
	// between rounds when no merges are found at the current threshold.
	// Default is 0.05.
	SimilarityStep float64

	// SkipNameShortening disables the post-compression step that shortens the
	// names of canonical schemas produced by merging. By default (false) names
	// are shortened; set to true to preserve the original names.
	SkipNameShortening bool

	// TrimExamples caps every schema's own Example at this many representative
	// array elements, at any depth -- see
	// [github.com/MarkRosemaker/openapi-edit.TrimExample] for how elements are
	// chosen. 0 (default) leaves examples as they are.
	TrimExamples int
}

func (c *Config) setDefaults() {
	if c.MinSimilarity == 0 {
		c.MinSimilarity = 1.0
	}

	if c.SimilarityStep == 0 {
		c.SimilarityStep = 0.05
	}
}

func (c Config) validate() error {
	if c.MinSimilarity < 0 || c.MinSimilarity > 1 {
		return fmt.Errorf("MinSimilarity must be in [0, 1], got %v", c.MinSimilarity)
	}

	if c.SimilarityStep <= 0 {
		return fmt.Errorf("SimilarityStep must be > 0, got %v", c.SimilarityStep)
	}

	return nil
}
