package whatsmeow_service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/clientstate"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func TestOnlyOneOwnerWinsConcurrentStarts(t *testing.T) {
	l := &clientLifecycles{}
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := l.reserve("personal"); ok {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 {
		t.Fatalf("accepted %d starts", accepted.Load())
	}
	owner := l.get("personal")
	owner.cancel()
	if _, ok := l.reserve("personal"); ok {
		t.Fatal("replacement started before cleanup drained")
	}
	l.finish("personal", owner)
	next, ok := l.reserve("personal")
	if !ok || next == owner {
		t.Fatal("new owner not created after cleanup")
	}
	next.cancel()
	l.finish("personal", next)
}

func TestCancelBeforeClientPublicationStopsDelayedStart(t *testing.T) {
	l := &clientLifecycles{}
	owner, _ := l.reserve("personal")
	w := whatsmeowService{lifecycles: l}
	drained := make(chan struct{})
	go func() {
		<-owner.ctx.Done()
		// Simulate a pending auth DB/Connect call that still has to return.
		<-drained
		l.finish("personal", owner)
	}()
	stopped := make(chan error, 1)
	go func() { stopped <- w.StopClient("personal") }()
	select {
	case <-owner.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel did not reach delayed start")
	}
	if _, ok := l.reserve("personal"); ok {
		t.Fatal("a cancelled-but-running start was replaced")
	}
	close(drained)
	if err := <-stopped; err != nil {
		t.Fatal(err)
	}
	if l.get("personal") != nil {
		t.Fatal("stopped owner leaked")
	}
}

func TestOldAttemptSignalsOnlyItsOwnChannel(t *testing.T) {
	oldKill, newKill := make(chan bool, 1), make(chan bool, 1)
	mycli, _, _ := recoveryEventClient(t)
	mycli.ownKill = oldKill
	clientstate.Store(mycli.killChannel, mycli.userID, newKill)
	mycli.signalStop()
	select {
	case <-oldKill:
	default:
		t.Fatal("old attempt not stopped")
	}
	select {
	case <-newKill:
		t.Fatal("old timeout stopped the new connection")
	default:
	}
}

func TestPairedOrStoppedAttemptCannotExpireNewSession(t *testing.T) {
	for _, paired := range []bool{false, true} {
		mycli, repo, observer := recoveryEventClient(t)
		mycli.paired.Store(paired)
		mycli.stopRequested.Store(!paired)
		mycli.teardownQR("stale QR timeout", false)
		if repo.updates.Load() != 0 {
			t.Fatal("old QR timeout changed current status")
		}
		select {
		case <-observer.webhooks:
			t.Fatal("stale timeout emitted a webhook")
		default:
		}
		select {
		case <-mycli.ownKill:
			t.Fatal("paired/stopped attempt signalled teardown")
		default:
		}
	}
}

type qrRecoveryRepository struct {
	recoveryRepository
	wrote chan struct{}
}

func (r *qrRecoveryRepository) UpdateQrcode(_ string, _ string) error {
	select {
	case r.wrote <- struct{}{}:
	default:
	}
	return nil
}

func TestQRRotationDrainsImmediatelyOnStopInsteadOfSleepingSixtySeconds(t *testing.T) {
	mycli, _, _ := recoveryEventClient(t)
	mycli.WAClient = recoveryClient(false)
	mycli.stopSignal = make(chan struct{})
	mycli.config.LogType = "json" // no QR secrets in test output
	r := &qrRecoveryRepository{wrote: make(chan struct{}, 1)}
	mycli.instanceRepository = r
	mycli.handleQRCodes([]string{"synthetic-qr-code"})
	select {
	case <-r.wrote:
	case <-time.After(time.Second):
		t.Fatal("QR worker did not start")
	}
	mycli.stopRequested.Store(true)
	close(mycli.stopSignal)
	done := make(chan struct{})
	go func() { mycli.qrWorkers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("old QR timer did not drain")
	}
}

func TestReconnectDoesNotReplacePendingOwner(t *testing.T) {
	l := &clientLifecycles{}
	owner, _ := l.reserve("personal")
	w := whatsmeowService{lifecycles: l}
	if err := w.ReconnectClient("personal"); err != ErrClientBusy {
		t.Fatalf("got %v", err)
	}
	if l.get("personal") != owner {
		t.Fatal("reconnect replaced the active attempt")
	}
	owner.cancel()
	if owner.ctx.Err() != context.Canceled {
		t.Fatal("owner not cancelled")
	}
	l.finish("personal", owner)
}

type stoppedRestoreRepository struct {
	instance_repository.InstanceRepository
	writes atomic.Int32
	legacy bool
}

func (r *stoppedRestoreRepository) GetInstanceByID(id string) (*instance_model.Instance, error) {
	resume := false
	if r.legacy {
		return &instance_model.Instance{Id: id, Connected: false}, nil
	}
	return &instance_model.Instance{Id: id, ResumeOnStartup: &resume}, nil
}
func (r *stoppedRestoreRepository) SetResumeOnStartup(_ string, _ bool) error {
	r.writes.Add(1)
	return nil
}

func TestAutomaticReadOrSendCannotUndoManualStop(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		repository := &stoppedRestoreRepository{legacy: legacy}
		w := whatsmeowService{instanceRepository: repository, lifecycles: &clientLifecycles{}}
		if err := w.RestoreInstance("manually-stopped"); err == nil {
			t.Fatal("automatic request resumed a stopped session")
		}
		if repository.writes.Load() != 0 || w.lifecycles.get("manually-stopped") != nil {
			t.Fatal("automatic request changed intent or started pairing")
		}
	}
}

func TestDuplicateStartCannotReplaceKillSignal(t *testing.T) {
	l := &clientLifecycles{}
	owner, _ := l.reserve("personal")
	kills := map[string]chan bool{"personal": owner.kill}
	w := whatsmeowService{lifecycles: l, killChannel: kills}
	if err := w.StartClient(&ClientData{Instance: &instance_model.Instance{Id: "personal"}}); err != ErrClientBusy {
		t.Fatalf("got %v", err)
	}
	if clientstate.Get(kills, "personal") != owner.kill {
		t.Fatal("second click overwrote the running attempt's signal")
	}
	owner.cancel()
	l.finish("personal", owner)
}

type latePairRepository struct {
	recoveryRepository
	jid string
}

func (r *latePairRepository) UpdateJid(_ string, jid string) error { r.jid = jid; return nil }

func TestPairSuccessRacingCancellationKeepsDeviceWithoutReportingConnected(t *testing.T) {
	mycli, _, observer := recoveryEventClient(t)
	repository := &latePairRepository{}
	mycli.instanceRepository = repository
	mycli.stopRequested.Store(true)
	jid := types.JID{User: "15550000000", Device: 15, Server: types.DefaultUserServer}
	mycli.myEventHandler(&events.PairSuccess{ID: jid})
	if repository.jid != jid.String() || !mycli.paired.Load() {
		t.Fatal("late pair success lost its exact saved device")
	}
	if repository.updates.Load() != 0 {
		t.Fatal("late pair success revived a cancelled connection")
	}
	select {
	case <-observer.webhooks:
		t.Fatal("cancelled client reported a successful connection")
	default:
	}
}
