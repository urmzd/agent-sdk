package agentsdk

import (
	"fmt"
	"sync"
	"time"
)

// TreeOption configures a Tree during construction.
type TreeOption func(*Tree)

// WithWAL sets the write-ahead log for the tree.
func WithWAL(wal WAL) TreeOption {
	return func(t *Tree) { t.wal = wal }
}

// WithStore sets the persistence store for the tree.
func WithStore(store Store) TreeOption {
	return func(t *Tree) { t.store = store }
}

// Tree is a branching conversation graph rooted at a system message.
type Tree struct {
	mu          sync.RWMutex
	nodes       map[NodeID]*Node
	children    map[NodeID][]NodeID // parent -> ordered children
	rootID      NodeID
	branches    map[BranchID]NodeID // branch -> tip node
	active      BranchID           // the branch Invoke reads from
	checkpoints map[CheckpointID]Checkpoint
	wal         WAL
	store       Store
}

// NewTree creates a new conversation tree rooted at the given system message.
func NewTree(systemMsg SystemMessage, opts ...TreeOption) (*Tree, error) {
	t := &Tree{
		nodes:       make(map[NodeID]*Node),
		children:    make(map[NodeID][]NodeID),
		branches:    make(map[BranchID]NodeID),
		checkpoints: make(map[CheckpointID]Checkpoint),
	}
	for _, opt := range opts {
		opt(t)
	}

	rootID := NodeID(NewID())
	mainBranch := BranchID("main")
	now := time.Now()

	root := &Node{
		ID:        rootID,
		Message:   systemMsg,
		State:     NodeActive,
		Version:   1,
		Depth:     0,
		BranchID:  mainBranch,
		CreatedAt: now,
		UpdatedAt: now,
	}

	t.nodes[rootID] = root
	t.rootID = rootID
	t.branches[mainBranch] = rootID
	t.active = mainBranch

	return t, nil
}

// Active returns the currently active branch ID.
func (t *Tree) Active() BranchID {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.active
}

// SetActive sets the active branch. Returns an error if the branch does not exist.
func (t *Tree) SetActive(branch BranchID) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.branches[branch]; !ok {
		return fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
	}
	t.active = branch
	return nil
}

// Root returns the root node.
func (t *Tree) Root() *Node {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.nodes[t.rootID]
}

// getNode returns a node by ID (caller must hold lock).
func (t *Tree) getNode(id NodeID) (*Node, error) {
	n, ok := t.nodes[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNodeNotFound, id)
	}
	return n, nil
}

// AddChild appends a message as a child of the given parent node.
func (t *Tree) AddChild(parentID NodeID, msg Message) (*Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	parent, err := t.getNode(parentID)
	if err != nil {
		return nil, err
	}
	if parent.State == NodeArchived {
		return nil, fmt.Errorf("%w: %s", ErrNodeArchived, parentID)
	}

	now := time.Now()
	child := &Node{
		ID:        NodeID(NewID()),
		ParentID:  parentID,
		Message:   msg,
		State:     NodeActive,
		Version:   1,
		Depth:     parent.Depth + 1,
		BranchID:  parent.BranchID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := t.walAddNode(child); err != nil {
		return nil, err
	}

	t.nodes[child.ID] = child
	t.children[parentID] = append(t.children[parentID], child.ID)
	t.branches[child.BranchID] = child.ID

	return child, nil
}

// Branch creates a new branch diverging from the given node.
func (t *Tree) Branch(fromNodeID NodeID, name string, msg Message) (BranchID, *Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	from, err := t.getNode(fromNodeID)
	if err != nil {
		return "", nil, err
	}
	if from.State == NodeArchived {
		return "", nil, fmt.Errorf("%w: %s", ErrNodeArchived, fromNodeID)
	}

	branchID := BranchID(name)
	if _, exists := t.branches[branchID]; exists {
		branchID = BranchID(fmt.Sprintf("%s-%s", name, NewID()[:8]))
	}

	now := time.Now()
	child := &Node{
		ID:        NodeID(NewID()),
		ParentID:  fromNodeID,
		Message:   msg,
		State:     NodeActive,
		Version:   1,
		Depth:     from.Depth + 1,
		BranchID:  branchID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := t.walAddNode(child); err != nil {
		return "", nil, err
	}

	t.nodes[child.ID] = child
	t.children[fromNodeID] = append(t.children[fromNodeID], child.ID)
	t.branches[branchID] = child.ID

	return branchID, child, nil
}

// UpdateUserMessage edits a user message by creating a new branch from the parent.
func (t *Tree) UpdateUserMessage(nodeID NodeID, newMsg UserMessage) (BranchID, *Node, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.getNode(nodeID)
	if err != nil {
		return "", nil, err
	}
	if node.ParentID == "" {
		return "", nil, fmt.Errorf("%w: cannot update root", ErrRootImmutable)
	}
	if _, ok := node.Message.(UserMessage); !ok {
		return "", nil, fmt.Errorf("%w: node is not a user message", ErrInvalidBranchPoint)
	}

	parent, err := t.getNode(node.ParentID)
	if err != nil {
		return "", nil, err
	}

	branchID := BranchID(fmt.Sprintf("edit-%s", NewID()[:8]))
	now := time.Now()
	child := &Node{
		ID:        NodeID(NewID()),
		ParentID:  node.ParentID,
		Message:   newMsg,
		State:     NodeActive,
		Version:   1,
		Depth:     parent.Depth + 1,
		BranchID:  branchID,
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := t.walAddNode(child); err != nil {
		return "", nil, err
	}

	t.nodes[child.ID] = child
	t.children[node.ParentID] = append(t.children[node.ParentID], child.ID)
	t.branches[branchID] = child.ID

	return branchID, child, nil
}

// Tip returns the tip node of the given branch.
func (t *Tree) Tip(branch BranchID) (*Node, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	tipID, ok := t.branches[branch]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
	}
	return t.getNode(tipID)
}

// Path returns the node IDs from root to the given node.
func (t *Tree) Path(nodeID NodeID) ([]NodeID, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pathUnlocked(nodeID)
}

func (t *Tree) pathUnlocked(nodeID NodeID) ([]NodeID, error) {
	var path []NodeID
	current := nodeID
	for current != "" {
		node, err := t.getNode(current)
		if err != nil {
			return nil, err
		}
		path = append(path, current)
		current = node.ParentID
	}
	// Reverse to get root-first order
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, nil
}

// Children returns the child nodes of the given node.
func (t *Tree) Children(nodeID NodeID) ([]*Node, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if _, err := t.getNode(nodeID); err != nil {
		return nil, err
	}

	childIDs := t.children[nodeID]
	result := make([]*Node, 0, len(childIDs))
	for _, cid := range childIDs {
		if n, ok := t.nodes[cid]; ok {
			result = append(result, n)
		}
	}
	return result, nil
}

// Branches returns a copy of the branch-to-tip mapping.
func (t *Tree) Branches() map[BranchID]NodeID {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[BranchID]NodeID, len(t.branches))
	for k, v := range t.branches {
		result[k] = v
	}
	return result
}

// Archive soft-deletes a node. If recursive is true, all descendants are also archived.
func (t *Tree) Archive(nodeID NodeID, archivedBy string, recursive bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.getNode(nodeID)
	if err != nil {
		return err
	}
	if node.ParentID == "" {
		return fmt.Errorf("%w: cannot archive root", ErrRootImmutable)
	}

	return t.archiveNode(node, archivedBy, recursive)
}

func (t *Tree) archiveNode(node *Node, archivedBy string, recursive bool) error {
	now := time.Now()
	node.State = NodeArchived
	node.ArchivedAt = &now
	node.ArchivedBy = archivedBy
	node.Version++
	node.UpdatedAt = now

	if recursive {
		for _, childID := range t.children[node.ID] {
			child, err := t.getNode(childID)
			if err != nil {
				return err
			}
			if err := t.archiveNode(child, archivedBy, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// Restore un-archives a node. If recursive is true, all descendants are also restored.
func (t *Tree) Restore(nodeID NodeID, recursive bool) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	node, err := t.getNode(nodeID)
	if err != nil {
		return err
	}

	return t.restoreNode(node, recursive)
}

func (t *Tree) restoreNode(node *Node, recursive bool) error {
	now := time.Now()
	node.State = NodeActive
	node.ArchivedAt = nil
	node.ArchivedBy = ""
	node.Version++
	node.UpdatedAt = now

	if recursive {
		for _, childID := range t.children[node.ID] {
			child, err := t.getNode(childID)
			if err != nil {
				return err
			}
			if err := t.restoreNode(child, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// Checkpoint creates a named checkpoint at the current tip of a branch.
func (t *Tree) Checkpoint(branch BranchID, name string) (CheckpointID, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	tipID, ok := t.branches[branch]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrBranchNotFound, branch)
	}

	cpID := CheckpointID(NewID())
	cp := Checkpoint{
		ID:        cpID,
		Branch:    branch,
		NodeID:    tipID,
		Name:      name,
		CreatedAt: time.Now(),
	}
	t.checkpoints[cpID] = cp

	return cpID, nil
}

// Rewind creates a new branch starting from the checkpoint's node.
func (t *Tree) Rewind(cp CheckpointID) (BranchID, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	checkpoint, ok := t.checkpoints[cp]
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrCheckpointNotFound, cp)
	}

	if _, err := t.getNode(checkpoint.NodeID); err != nil {
		return "", err
	}

	branchID := BranchID(fmt.Sprintf("rewind-%s-%s", checkpoint.Name, NewID()[:8]))
	t.branches[branchID] = checkpoint.NodeID

	return branchID, nil
}

// NodePath returns the TreePath (child indices from root) for the given node.
func (t *Tree) NodePath(nodeID NodeID) (TreePath, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.nodePathUnlocked(nodeID)
}

func (t *Tree) nodePathUnlocked(nodeID NodeID) (TreePath, error) {
	nodePath, err := t.pathUnlocked(nodeID)
	if err != nil {
		return nil, err
	}
	if len(nodePath) <= 1 {
		return TreePath{}, nil // root has empty path
	}

	treePath := make(TreePath, 0, len(nodePath)-1)
	for i := 1; i < len(nodePath); i++ {
		parentID := nodePath[i-1]
		childID := nodePath[i]
		siblings := t.children[parentID]
		found := false
		for idx, sid := range siblings {
			if sid == childID {
				treePath = append(treePath, idx)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("child %s not found in parent %s children", childID, parentID)
		}
	}
	return treePath, nil
}

// walAddNode writes a node addition to the WAL if configured.
func (t *Tree) walAddNode(node *Node) error {
	if t.wal == nil {
		return nil
	}
	txID, err := t.wal.Begin()
	if err != nil {
		return err
	}
	if err := t.wal.Append(txID, TxOp{Kind: TxOpAddNode, Node: node}); err != nil {
		_ = t.wal.Abort(txID)
		return err
	}
	return t.wal.Commit(txID)
}
