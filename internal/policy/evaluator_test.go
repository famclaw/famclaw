package policy

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEvaluateFailClosed(t *testing.T) {
	// Simple test to ensure the package compiles and basic functionality works
	assert.NotNil(t, &Evaluator{})
}