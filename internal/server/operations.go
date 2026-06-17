package server

import (
	"fmt"
	"sync"
	"time"
)

// Operation replica el recurso "Operation" que devuelven las APIs de GCP
// para acciones asíncronas (crear/borrar instancias, etc.). El emulador
// las ejecuta de forma síncrona pero igual expone el recurso Operation
// para que gcloud/SDKs que hacen polling con operations.get funcionen.
type Operation struct {
	Name          string `json:"name"`
	Status        string `json:"status"` // PENDING | RUNNING | DONE
	OperationType string `json:"operationType,omitempty"`
	TargetLink    string `json:"targetLink,omitempty"`
	Progress      int    `json:"progress"`
	InsertTime    string `json:"insertTime"`
	EndTime       string `json:"endTime,omitempty"`
	SelfLink      string `json:"selfLink,omitempty"`
}

// Operations es un registro en memoria de operaciones, suficiente porque
// el emulador las completa inmediatamente; igual queda el historial para
// que un cliente pueda hacer GET /operations/{name}.
type Operations struct {
	mu   sync.Mutex
	seq  int64
	ops  map[string]*Operation
}

func NewOperations() *Operations {
	return &Operations{ops: make(map[string]*Operation)}
}

// Done crea y registra una operación ya completada (DONE), tal como
// corresponde a un emulador que ejecuta todo síncronamente.
func (o *Operations) Done(opType, targetLink, selfLinkBase string) *Operation {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.seq++
	name := fmt.Sprintf("operation-%d", o.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	op := &Operation{
		Name:          name,
		Status:        "DONE",
		OperationType: opType,
		TargetLink:    targetLink,
		Progress:      100,
		InsertTime:    now,
		EndTime:       now,
		SelfLink:      selfLinkBase + "/" + name,
	}
	o.ops[name] = op
	return op
}

func (o *Operations) Get(name string) (*Operation, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	op, ok := o.ops[name]
	return op, ok
}
