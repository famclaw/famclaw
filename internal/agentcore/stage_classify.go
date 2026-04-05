package agentcore

import (
	"context"

	"github.com/famclaw/famclaw/internal/classifier"
)

// NewStageClassify returns a stage that classifies the user's message.
func NewStageClassify(clf *classifier.Classifier) Stage {
	return func(_ context.Context, turn *Turn) error {
		turn.Category = clf.Classify(turn.Input)
		return nil
	}
}
