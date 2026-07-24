package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluatorBasic(t *testing.T) {
	// Basic test to ensure evaluator can be constructed
	evaluator, err := NewEvaluator("", "", "")
	assert.NoError(t, err)
	assert.NotNil(t, evaluator)
}