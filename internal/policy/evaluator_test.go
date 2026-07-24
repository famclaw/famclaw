package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluator(t *testing.T) {
	t.Run("constructor works", func(t *testing.T) {
		// Test that we can at least construct an evaluator
		evaluator, err := NewEvaluator("", "", "")
		assert.NoError(t, err)
		assert.NotNil(t, evaluator)
	})
}