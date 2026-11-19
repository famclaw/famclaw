package compress

import (
	"sort"
)

// Indices returns the original global index for each retained message
// in the compressed slice. The mapping is computed from the same
// group-eviction decision that produced the compressed slice, so
// duplicate-content messages are distinguished by their position, not
// by Role+Content matching.
//
// The returned slice has the same length as the compressed output.
// idx[i] is the original index of the i-th compressed message.
func Indices(messages []Message, opts Options) []int {
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 4096
	}
	est := opts.estimator()

	total := totalTokens(messages, est)
	budget := int(float64(opts.ContextWindow) * 0.85)

	margin := opts.EstimatorMargin
	if margin == 0 {
		margin = 0.15
	}
	if margin < 0 {
		margin = 0
	}
	if margin >= 1 {
		margin = 0.99
	}
	budget = int(float64(budget) * (1 - margin))

	if total <= budget {
		idx := make([]int, len(messages))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}

	if len(messages) <= 4 {
		idx := make([]int, len(messages))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}

	keepEnd := 3
	if keepEnd > len(messages)-1 {
		keepEnd = len(messages) - 1
	}

	protected := make(map[int]bool)
	protected[0] = true
	for i := len(messages) - keepEnd; i < len(messages); i++ {
		protected[i] = true
	}
	for i, m := range messages {
		if m.Pinned {
			protected[i] = true
		}
	}

	groups := buildGroups(messages)

	keepGroup := make([]bool, len(groups))
	protectedGroup := make([]bool, len(groups))
	for g, grp := range groups {
		keepGroup[g] = true
		for _, idx := range grp {
			if protected[idx] {
				protectedGroup[g] = true
				break
			}
		}
	}

	type candidate struct {
		groupIdx int
		firstIdx int
	}
	var cand []candidate
	for g, grp := range groups {
		if !keepGroup[g] || protectedGroup[g] {
			continue
		}
		if messages[grp[0]].Prunable {
			cand = append(cand, candidate{g, grp[0]})
		}
	}
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].firstIdx < cand[j].firstIdx })
	for _, c := range cand {
		if !protectedGroup[c.groupIdx] && keepGroup[c.groupIdx] && currentTokens(messages, keepGroup, groups, est) > budget {
			keepGroup[c.groupIdx] = false
		}
	}

	var cand2c []candidate
	for g, grp := range groups {
		if !keepGroup[g] || protectedGroup[g] {
			continue
		}
		cand2c = append(cand2c, candidate{g, grp[0]})
	}
	sort.SliceStable(cand2c, func(i, j int) bool { return cand2c[i].firstIdx < cand2c[j].firstIdx })
	for _, c := range cand2c {
		if !protectedGroup[c.groupIdx] && keepGroup[c.groupIdx] && currentTokens(messages, keepGroup, groups, est) > budget {
			keepGroup[c.groupIdx] = false
		}
	}

	keptAt := make([]bool, len(messages))
	for g, grp := range groups {
		if !keepGroup[g] {
			continue
		}
		for _, idx := range grp {
			keptAt[idx] = true
		}
	}

	// If even the protected-only result is over budget, return the
	// protected groups whole.
	if currentTokens(messages, keepGroup, groups, est) > budget {
		keptProt := make([]bool, len(messages))
		for g, grp := range groups {
			if !protectedGroup[g] {
				continue
			}
			for _, idx := range grp {
				keptProt[idx] = true
			}
		}
		out := make([]int, 0, len(messages))
		for i := range messages {
			if keptProt[i] {
				out = append(out, i)
			}
		}
		return out
	}

	out := make([]int, 0, len(messages))
	for i := range messages {
		if keptAt[i] {
			out = append(out, i)
		}
	}
	return out
}
