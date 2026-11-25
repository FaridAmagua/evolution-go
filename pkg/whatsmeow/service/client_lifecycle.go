package whatsmeow_service

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/clientstate"
)

var ErrClientBusy = errors.New("a connection operation is already in progress; wait or cancel it first")

func (w whatsmeowService) PairingInProgress(id string) bool {
	owner := w.lifecycles.get(id)
	if owner == nil {
		return false
	}
	mycli := clientstate.Get(w.myClientPointer, id)
	return owner.ctx.Err() != nil || mycli == nil || !mycli.paired.Load() || mycli.stopRequested.Load()
}

type clientLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	kill   chan bool
}

type clientLifecycles struct {
	sync.Mutex
	active map[string]*clientLifecycle
}

func (l *clientLifecycles) reserve(id string) (*clientLifecycle, bool) {
	l.Lock()
	defer l.Unlock()
	if l.active[id] != nil {
		return nil, false
	}
	if l.active == nil {
		l.active = make(map[string]*clientLifecycle)
	}
	ctx, cancel := context.WithCancel(context.Background())
	owner := &clientLifecycle{ctx: ctx, cancel: cancel, done: make(chan struct{}), kill: make(chan bool, 1)}
	l.active[id] = owner
	return owner, true
}

func (l *clientLifecycles) get(id string) *clientLifecycle {
	l.Lock()
	defer l.Unlock()
	return l.active[id]
}

func (l *clientLifecycles) finish(id string, owner *clientLifecycle) {
	l.Lock()
	defer l.Unlock()
	if l.active[id] == owner {
		delete(l.active, id)
	}
	close(owner.done)
}

// StopClient cancels even a start that has not yet published a client pointer.
// Ownership is retained until Connect, event callbacks and QR workers have
// drained; no replacement may start in that interval.
func (w whatsmeowService) StopClient(id string) error {
	owner := w.lifecycles.get(id)
	if owner == nil {
		return nil
	}
	owner.cancel()
	select {
	case <-owner.done:
		return nil
	case <-time.After(15 * time.Second):
		return errors.New("connection is still stopping; retry after it has stopped")
	}
}

func (w whatsmeowService) StartClient(cd *ClientData) error {
	// The request handler may still be reading its response model. Runtime
	// events own a separate snapshot and must not mutate that response object.
	copyData := *cd
	copyInstance := *cd.Instance
	copyData.Instance = &copyInstance
	copyData.Subscriptions = append([]string(nil), cd.Subscriptions...)
	cd = &copyData
	owner, ok := w.lifecycles.reserve(cd.Instance.Id)
	if !ok {
		return ErrClientBusy
	}
	clientstate.Store(w.killChannel, cd.Instance.Id, owner.kill)
	go func() {
		select {
		case <-owner.kill:
			owner.cancel()
		case <-owner.ctx.Done():
		}
	}()
	go func() {
		defer w.lifecycles.finish(cd.Instance.Id, owner)
		defer clientstate.DeleteIf(w.killChannel, cd.Instance.Id, owner.kill)
		defer owner.cancel()
		w.runClient(cd, owner)
	}()
	return nil
}

func (mycli *MyClient) signalStop() {
	// Capture the attempt's own signal, never look up a newer attempt by ID.
	if mycli.cancelAttempt != nil {
		mycli.cancelAttempt()
		return
	}
	if mycli.ownKill != nil {
		select {
		case mycli.ownKill <- true:
		default:
		}
	}
}

func (mycli *MyClient) pairingActive() bool {
	return !mycli.stopRequested.Load() && !mycli.paired.Load() && mycli.WAClient != nil && !mycli.WAClient.IsLoggedIn()
}
