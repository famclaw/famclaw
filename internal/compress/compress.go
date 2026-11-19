package compress

import "sort"

// Message mirrors the conversation message format.
type Message struct {
	Role         string
	Content      string
	ContentParts []any // multimodal content (images) — preserved through compression
	// Tool identities are opaque strings carried verbatim by callers
	// (llm.Message.ToolCall IDs / ToolCallID). They are used only for
	// group pairing and eviction accounting — never interpreted by name,
	// content or intent.
	CallIDs []string // assistant messages: IDs of the tool calls requested
	// ToolArgs carries the complete serialized function payload per tool
	// call (name + arguments), in the same order as CallIDs. Each entry
	// is the JSON-encoded ToolCallFunction exactly as it appears on the
	// wire. This allows token estimation to account for the full cost of
	// repeated or long tool names and large argument payloads without
	// interpreting their content. Parallel calls to the same tool name
	// each get their own entry — no keying by name.
	ToolArgs   []string
	ToolCallID string // tool messages: ID of the call this result answers
	Pinned     bool   // pinned messages survive truncation
	Prunable   bool   // prefer-drop messages (typically tool results) — Tier 0 evicts before non-Prunable
}

// Options controls compression behavior.
type Options struct {
	ContextWindow   int            // total context window in tokens
	Estimator       TokenEstimator // token estimator (defaults to SimpleEstimator)
	EstimatorMargin float64        // 0..1 safety buffer for SimpleEstimator inaccuracy; default 0.15 when zero
}

func (o *Options) estimator() TokenEstimator {
	if o.Estimator != nil {
		return o.Estimator
	}
	return &SimpleEstimator{}
}

// Compress reduces the message list to fit within the context window.
// Applies Tier 0 (smart truncation) — drops oldest non-pinned messages.
// Returns the compressed message list.
func Compress(messages []Message, opts Options) []Message {
	idx := CompressIndices(messages, opts)
	out := make([]Message, 0, len(idx))
	for _, i := range idx {
		out = append(out, messages[i])
	}
	return out
}

// CompressIndices returns the original global index of each message
// retained by the compression decision. This is the single authoritative
// selection: Compress projects its output from these positions, and
// callers (tool-loop, bridges) project the original LLM messages from
// the SAME positions. No comparator-based matching is used.
//
// The returned slice has one entry per retained message, in original
// order. idx[i] is the index into the input slice.
func CompressIndices(messages []Message, opts Options) []int {
	if opts.ContextWindow <= 0 {
		opts.ContextWindow = 4096
	}
	est := opts.estimator()

	// Calculate total tokens
	total := totalTokens(messages, est)
	budget := int(float64(opts.ContextWindow) * 0.85) // leave 15% for response

	// Apply estimator safety margin. SimpleEstimator is chars/4 — off by
	// 10–20% depending on content. Shrinking the budget by EstimatorMargin
	// absorbs that error so we don't push past n_ctx.
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

	return truncateIndices(messages, budget, est)
}

// truncateIndices returns the original indices of messages retained after
// Tier-0 group-aware eviction. This is the single authoritative selection.
//
// Tool-call groups are preserved as units. A group consists of one
// assistant message carrying opaque tool-call IDs (CallIDs) and every
// tool result answering any of those IDs (ToolCallID). Eviction removes
// an entire group — it never leaves a result without its call, or a call
// without a result. Orphaned tool results (no matching assistant CallIDs
// in the list, e.g. from a partially truncated history) are treated as
// single-member groups and are also dropped together with any matching
// call that does exist.
//
// Protected: system prompt (index 0), pinned messages, last 3 messages,
// and the current user message (the most recent user-role message, which
// may carry images). ANY protected/in-flight group member protects the
// whole group.
//
// A group is evictable in Pass 1 if ANY of its members is Prunable
// (typically tool results are Prunable, not the assistant anchor).
// Pass 2 evicts any remaining non-protected group, oldest first.
func truncateIndices(messages []Message, budget int, est TokenEstimator) []int {
	if len(messages) <= 4 {
		idx := make([]int, len(messages))
		for i := range idx {
			idx[i] = i
		}
		return idx
	}

	// Always keep: system prompt (first), last 3 messages,
	// and the current user message (most recent user-role entry).
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
	// Explicitly protect the most recent user-role message (the current
	// request, which may carry images). After tool results accumulate,
	// this message can fall outside the last-3 window; protect it here.
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			protected[i] = true
			break
		}
	}

	groups := buildGroups(messages)

	// Pre-compute token cost per group (single pass over all messages).
	groupCost := make([]int, len(groups))
	for g, grp := range groups {
		for _, idx := range grp {
			groupCost[g] += messageTokens(messages[idx], est)
		}
	}

	// keepGroup[g] starts true for every group; protected groups are
	// immutable.
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

	// Track the current retained total incrementally.
	retained := 0
	for g := range groups {
		if keepGroup[g] {
			retained += groupCost[g]
		}
	}

	// Pass 1: drop whole groups that contain ANY Prunable member
	// (typically tool results are Prunable), oldest group first.
	type candidate struct {
		groupIdx int
		firstIdx int
	}
	var cand []candidate
	for g, grp := range groups {
		if !keepGroup[g] || protectedGroup[g] {
			continue
		}
		anyPrunable := false
		for _, idx := range grp {
			if messages[idx].Prunable {
				anyPrunable = true
				break
			}
		}
		if anyPrunable {
			cand = append(cand, candidate{g, grp[0]})
		}
	}
	sort.SliceStable(cand, func(i, j int) bool { return cand[i].firstIdx < cand[j].firstIdx })
	for _, c := range cand {
		if keepGroup[c.groupIdx] && retained > budget {
			keepGroup[c.groupIdx] = false
			retained -= groupCost[c.groupIdx]
		}
	}

	// Pass 2: if still over budget, drop whole non-protected groups
	// oldest-first, including non-Prunable user/assistant turns.
	var cand2c []candidate
	for g, grp := range groups {
		if !keepGroup[g] || protectedGroup[g] {
			continue
		}
		cand2c = append(cand2c, candidate{g, grp[0]})
	}
	sort.SliceStable(cand2c, func(i, j int) bool { return cand2c[i].firstIdx < cand2c[j].firstIdx })
	for _, c := range cand2c {
		if keepGroup[c.groupIdx] && retained > budget {
			keepGroup[c.groupIdx] = false
			retained -= groupCost[c.groupIdx]
		}
	}

	// Assemble result in original global index order.
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
	// protected groups whole — a safe, well-formed transcript the model
	// can still respond to, rather than a partial group that orphans
	// tool-call IDs.
	if retained > budget {
		for g := range groups {
			if !protectedGroup[g] {
				for _, idx := range groups[g] {
					keptAt[idx] = false
				}
			}
		}
	}

	out := make([]int, 0, len(messages))
	for i := range messages {
		if keptAt[i] {
			out = append(out, i)
		}
	}
	return out
}

// buildGroups partitions messages into tool-call groups in a single pass
// using an index from tool-result ID to the positions that carry it.
//
// A group consists of one assistant message with CallIDs plus every tool
// result whose ToolCallID matches one of those CallIDs AND whose position
// is strictly after the assistant message and strictly before the next
// assistant that also contains that CallID (the round boundary). This
// ensures reused IDs across separate rounds are not joined: in the
// ordering [assistant_X_old, result_X_old, assistant_X_new, result_X_new],
// each result stays with its own chronological round.
//
// Orphan tool results (no matching assistant CallIDs before them) become
// single-member groups. All other messages are single-member groups.
//
// The returned groups preserve global order: groups are anchored at their
// first (smallest) member index, and within a group members are sorted
// ascending.
func buildGroups(messages []Message) [][]int {
	n := len(messages)
	// Index tool results by ID: id -> list of positions.
	toolIdx := make(map[string][]int)
	for i, m := range messages {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolIdx[m.ToolCallID] = append(toolIdx[m.ToolCallID], i)
		}
	}

	// Precompute round boundaries: for each (assistant position i, call ID
	// cid), the exclusive upper bound is the position of the next assistant
	// that also contains cid (or n if none). Results at j where i < j < ub
	// belong to this round; results at j >= ub belong to a later round.
	assistants := make([]int, 0, n)
	for i, m := range messages {
		if m.Role == "assistant" && len(m.CallIDs) > 0 {
			assistants = append(assistants, i)
		}
	}
	cidAssistants := make(map[string][]int)
	for _, i := range assistants {
		for _, cid := range messages[i].CallIDs {
			cidAssistants[cid] = append(cidAssistants[cid], i)
		}
	}
	// boundary[i] maps cid -> exclusive upper bound for assistant at i.
	boundary := make(map[int]map[string]int, len(assistants))
	for _, i := range assistants {
		bm := make(map[string]int)
		for _, cid := range messages[i].CallIDs {
			pos := cidAssistants[cid]
			// Find i's index in pos.
			ub := n
			for k, p := range pos {
				if p == i {
					if k+1 < len(pos) {
						ub = pos[k+1]
					}
					break
				}
			}
			bm[cid] = ub
		}
		boundary[i] = bm
	}

	groupOf := make([]int, n)
	for i := range groupOf {
		groupOf[i] = -1
	}
	nextGroup := 0
	var groups [][]int

	// Track which tool-result positions have been assigned to a group.
	assigned := make([]bool, n)

	for i, m := range messages {
		if groupOf[i] != -1 {
			continue
		}
		if m.Role != "assistant" || len(m.CallIDs) == 0 {
			// Single-member group (user, system, assistant without calls,
			// or already-handled tool).
			groupOf[i] = nextGroup
			assigned[i] = true
			groups = append(groups, []int{i})
			nextGroup++
			continue
		}

		// Assistant with tool calls: gather matching tool results that
		// follow this message, are not yet assigned, and are before the
		// round boundary for each call ID.
		grp := []int{i}
		assigned[i] = true
		bm := boundary[i]
		for _, cid := range m.CallIDs {
			ub := bm[cid]
			positions := toolIdx[cid]
			for _, j := range positions {
				if j <= i {
					continue // result before the call — belongs to an earlier round
				}
				if j >= ub {
					continue // result belongs to a later round with same ID
				}
				if assigned[j] {
					continue // already grouped
				}
				grp = append(grp, j)
				assigned[j] = true
			}
		}
		// Sort group members ascending (CallIDs order may not match position).
		sort.Ints(grp)
		for _, idx := range grp {
			groupOf[idx] = nextGroup
		}
		groups = append(groups, grp)
		nextGroup++
	}

	// Second pass: any remaining unassigned messages (orphan tool results,
	// etc.) become single-member groups.
	for i := 0; i < n; i++ {
		if groupOf[i] != -1 {
			continue
		}
		groupOf[i] = nextGroup
		groups = append(groups, []int{i})
		nextGroup++
	}

	return groups
}

func totalTokens(messages []Message, est TokenEstimator) int {
	total := 0
	for _, m := range messages {
		total += messageTokens(m, est)
	}
	return total
}

// messageTokens estimates the token count for a single message, including
// both the Content string and any multimodal ContentParts. Without this,
// messages that carry only ContentParts (e.g. a user image whose Content is
// empty) would count as ~4 tokens — the image's real cost is hidden and
// context-window management would keep too many messages, overshooting n_ctx.
// messageTokens estimates the token count for a single message, including
// Content, multimodal ContentParts, tool-call identity strings, and
// complete serialized function payloads (ToolArgs: name + arguments per
// call). Tool call IDs (assistant CallIDs, tool ToolCallID) are counted
// as chars/4 plus a small per-ID overhead. ToolArgs entries are the actual
// wire-form function payloads, so their cost is counted opaquely without
// interpreting names or intent.
func messageTokens(m Message, est TokenEstimator) int {
	tokens := est.Estimate(m.Content) + 4 // ~4 tokens overhead per message
	if len(m.ContentParts) > 0 {
		tokens += contentPartsTokens(m.ContentParts, est)
	}
	for _, cid := range m.CallIDs {
		tokens += est.Estimate(cid) + 4
	}
	for _, arg := range m.ToolArgs {
		tokens += est.Estimate(arg) + 4
	}
	if m.ToolCallID != "" {
		tokens += est.Estimate(m.ToolCallID) + 4
	}
	return tokens
}

// contentPartsTokens estimates tokens for multimodal content parts.
// Text parts use the same estimator as Content; image_url parts use a
// fixed per-image estimate. Vision language models typically charge
// ~1000 tokens per image regardless of base64 byte length, so this is a
// conservative budget estimate that keeps truncation decisions sound.
func contentPartsTokens(parts []any, est TokenEstimator) int {
	total := 0
	for _, p := range parts {
		m, ok := p.(map[string]any)
		if !ok {
			continue
		}
		switch m["type"] {
		case "text":
			if t, ok := m["text"].(string); ok {
				total += est.Estimate(t)
			}
		case "image_url":
			total += 1000
		}
	}
	return total
}
