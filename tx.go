package agentsdk

import (
	"fmt"
	"sync"
)

// TxID uniquely identifies a WAL transaction.
type TxID string

// TxOpKind describes the type of operation in a transaction.
type TxOpKind string

const (
	TxOpAddNode      TxOpKind = "add_node"
	TxOpUpdateNode   TxOpKind = "update_node"
	TxOpSetBranch    TxOpKind = "set_branch"
	TxOpAddChild     TxOpKind = "add_child"
	TxOpAddCheckpoint TxOpKind = "add_checkpoint"
)

// TxOp is a single operation within a WAL transaction.
type TxOp struct {
	Kind     TxOpKind
	NodeID   NodeID
	ParentID NodeID
	Node     *Node
	BranchID BranchID
	TipID    NodeID
	Checkpoint *Checkpoint
}

// WAL provides write-ahead logging for atomic tree mutations.
type WAL interface {
	Begin() (TxID, error)
	Append(txID TxID, op TxOp) error
	Commit(txID TxID) error
	Abort(txID TxID) error
	Recover() ([]TxID, error)
	Replay(txID TxID) ([]TxOp, error)
}

// txState tracks the state of an in-flight transaction.
type txState struct {
	ops       []TxOp
	committed bool
	aborted   bool
}

// InMemoryWAL is a WAL implementation backed by an in-memory map.
// Suitable for testing; offers no crash durability.
type InMemoryWAL struct {
	mu    sync.Mutex
	txns  map[TxID]*txState
	nextID uint64
}

// NewInMemoryWAL creates a new in-memory WAL.
func NewInMemoryWAL() *InMemoryWAL {
	return &InMemoryWAL{
		txns: make(map[TxID]*txState),
	}
}

func (w *InMemoryWAL) Begin() (TxID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.nextID++
	id := TxID(fmt.Sprintf("tx-%d", w.nextID))
	w.txns[id] = &txState{}
	return id, nil
}

func (w *InMemoryWAL) Append(txID TxID, op TxOp) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, ok := w.txns[txID]
	if !ok {
		return fmt.Errorf("unknown transaction: %s", txID)
	}
	if tx.committed || tx.aborted {
		return fmt.Errorf("transaction %s is already finalized", txID)
	}
	tx.ops = append(tx.ops, op)
	return nil
}

func (w *InMemoryWAL) Commit(txID TxID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, ok := w.txns[txID]
	if !ok {
		return fmt.Errorf("unknown transaction: %s", txID)
	}
	if tx.aborted {
		return fmt.Errorf("transaction %s was aborted", txID)
	}
	tx.committed = true
	return nil
}

func (w *InMemoryWAL) Abort(txID TxID) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, ok := w.txns[txID]
	if !ok {
		return fmt.Errorf("unknown transaction: %s", txID)
	}
	tx.aborted = true
	return nil
}

func (w *InMemoryWAL) Recover() ([]TxID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var committed []TxID
	for id, tx := range w.txns {
		if tx.committed {
			committed = append(committed, id)
		}
	}
	return committed, nil
}

func (w *InMemoryWAL) Replay(txID TxID) ([]TxOp, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	tx, ok := w.txns[txID]
	if !ok {
		return nil, fmt.Errorf("unknown transaction: %s", txID)
	}
	ops := make([]TxOp, len(tx.ops))
	copy(ops, tx.ops)
	return ops, nil
}
