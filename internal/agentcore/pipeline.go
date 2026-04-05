package agentcore

import "context"

// Stage processes a turn. Returns error to abort the pipeline.
// Stages read from and write to the Turn struct.
type Stage func(ctx context.Context, turn *Turn) error

// Pipeline is an ordered slice of stages.
type Pipeline []Stage

// Run executes each stage in order. Returns the first error encountered.
func (p Pipeline) Run(ctx context.Context, turn *Turn) error {
	for _, stage := range p {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := stage(ctx, turn); err != nil {
			return err
		}
	}
	return nil
}

// Len returns the number of stages in the pipeline.
func (p Pipeline) Len() int {
	return len(p)
}

// Append returns a new pipeline with additional stages appended.
func (p Pipeline) Append(stages ...Stage) Pipeline {
	result := make(Pipeline, len(p)+len(stages))
	copy(result, p)
	copy(result[len(p):], stages)
	return result
}

// Prepend returns a new pipeline with stages prepended before existing ones.
func (p Pipeline) Prepend(stages ...Stage) Pipeline {
	result := make(Pipeline, len(stages)+len(p))
	copy(result, stages)
	copy(result[len(stages):], p)
	return result
}
