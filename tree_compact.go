package agentsdk

import (
	"context"
	"fmt"
	"time"
)

// CompactOpts configures tree-aware compaction.
type CompactOpts struct {
	MaxTokens      int  // context window budget
	PreserveShared bool // don't compact shared ancestors (default true)
}

// Compact compresses a branch by summarizing an eligible prefix of messages
// when the total token count exceeds MaxTokens. Instead of mutating the branch
// in-place, it creates a new compacted branch and sets it as active.
// Returns the new branch ID, or the original branch if no compaction was needed.
func (t *Tree) Compact(ctx context.Context, branch BranchID, provider Provider, tokenizer Tokenizer, opts CompactOpts) (BranchID, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tipID, ok := t.branches[branch]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
	}

	path, err := t.pathUnlocked(tipID)
	if err != nil {
		return "", err
	}

	// Collect messages for token counting.
	messages := make([]Message, 0, len(path))
	for _, nid := range path {
		node := t.nodes[nid]
		if node.State == NodeArchived {
			continue
		}
		messages = append(messages, node.Message)
	}

	// Check if we're over budget.
	tokenCount, err := tokenizer.CountTokens(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("counting tokens: %w", err)
	}
	if tokenCount <= opts.MaxTokens {
		return branch, nil // under budget, no compaction needed
	}

	// Identify nodes shared across other branches if PreserveShared.
	shared := make(map[NodeID]bool)
	if opts.PreserveShared {
		for brID, brTip := range t.branches {
			if brID == branch {
				continue
			}
			brPath, err := t.pathUnlocked(brTip)
			if err != nil {
				continue
			}
			for _, nid := range brPath {
				shared[nid] = true
			}
		}
	}

	// Build list of active, non-root, non-shared node IDs on the path.
	type candidate struct {
		id   NodeID
		node *Node
	}
	var candidates []candidate
	for _, nid := range path {
		node := t.nodes[nid]
		if node.ParentID == "" {
			continue // never compact root
		}
		if node.State != NodeActive {
			continue
		}
		if opts.PreserveShared && shared[nid] {
			continue
		}
		candidates = append(candidates, candidate{id: nid, node: node})
	}

	if len(candidates) == 0 {
		return branch, nil
	}

	// Find the smallest prefix of candidates to summarize that brings tokens under budget.
	// We try progressively larger prefixes until summarization would fit.
	// For simplicity, we compact the first half of candidates (or at least 1).
	compactCount := len(candidates) / 2
	if compactCount < 1 {
		compactCount = 1
	}
	toCompact := candidates[:compactCount]

	// Summarize the run via provider.
	msgs := make([]Message, 0, len(toCompact))
	nodeIDs := make([]NodeID, 0, len(toCompact))
	for _, c := range toCompact {
		msgs = append(msgs, c.node.Message)
		nodeIDs = append(nodeIDs, c.id)
	}

	summaryText := messagesToText(msgs)
	summaryReq := []Message{
		NewSystemMessage("Summarize the following conversation concisely, preserving key facts and decisions."),
		NewUserMessage(summaryText),
	}

	rx, err := provider.ChatStream(ctx, summaryReq, nil, "")
	if err != nil {
		return "", fmt.Errorf("summarization: %w", err)
	}

	var summary string
	for delta := range rx {
		if tc, ok := delta.(TextContentDelta); ok {
			summary += tc.Content
		}
	}

	// Create a new branch forking from the parent of the first compacted node.
	first := toCompact[0]
	last := toCompact[len(toCompact)-1]

	newBranchID := BranchID(fmt.Sprintf("compact-%s-%s", branch, NewID()[:8]))

	now := time.Now()
	summaryNode := &Node{
		ID:        NodeID(NewID()),
		ParentID:  first.node.ParentID,
		Message:   NewUserMessage("Summary of previous conversation: " + summary),
		State:     NodeCompacted,
		Version:   1,
		Depth:     first.node.Depth,
		BranchID:  newBranchID,
		CreatedAt: now,
		UpdatedAt: now,
		SummaryOf: nodeIDs,
	}

	t.nodes[summaryNode.ID] = summaryNode
	t.children[first.node.ParentID] = append(t.children[first.node.ParentID], summaryNode.ID)

	// Re-link remaining (non-compacted) nodes after the compacted prefix onto the new branch.
	// Find nodes on the path after the last compacted node.
	var remaining []NodeID
	pastCompacted := false
	for _, nid := range path {
		if nid == last.id {
			pastCompacted = true
			continue
		}
		if pastCompacted {
			remaining = append(remaining, nid)
		}
	}

	// Clone remaining nodes onto the new branch.
	prevID := summaryNode.ID
	var newTipID NodeID
	if len(remaining) == 0 {
		newTipID = summaryNode.ID
	} else {
		for _, nid := range remaining {
			orig := t.nodes[nid]
			cloned := &Node{
				ID:        NodeID(NewID()),
				ParentID:  prevID,
				Message:   orig.Message,
				State:     NodeActive,
				Version:   1,
				Depth:     orig.Depth,
				BranchID:  newBranchID,
				CreatedAt: orig.CreatedAt,
				UpdatedAt: now,
			}
			t.nodes[cloned.ID] = cloned
			t.children[prevID] = append(t.children[prevID], cloned.ID)
			prevID = cloned.ID
			newTipID = cloned.ID
		}
	}

	t.branches[newBranchID] = newTipID
	t.active = newBranchID

	return newBranchID, nil
}
