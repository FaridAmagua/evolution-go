package whatsmeow_service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	"github.com/patrickmn/go-cache"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

func recoveryClient(paired bool) *whatsmeow.Client {
	device := &store.Device{}
	if paired {
		device.ID = &types.JID{User: "15550000000", Device: 1, Server: types.DefaultUserServer}
	}
	return whatsmeow.NewClient(device, nil)
}

func TestTransportRecoveryPolicy(t *testing.T) {
	for _, paired := range []bool{false, true} {
		t.Run(fmt.Sprintf("paired=%v", paired), func(t *testing.T) {
			client := recoveryClient(paired)
			var stopped atomic.Bool
			configureTransportRecovery(client, &stopped)
			if !client.EnableAutoReconnect || client.InitialAutoReconnect != paired {
				t.Fatal("native recovery must support paired sessions without restarting QR pairing")
			}
			if !client.AutoReconnectHook(errors.New("temporary network error")) {
				t.Fatal("temporary errors must remain retryable")
			}
			if client.AutoReconnectHook(fmt.Errorf("revoked session: %w", store.ErrDeviceDeleted)) {
				t.Fatal("a deleted authentication store must never retry")
			}
			stopped.Store(true)
			if client.AutoReconnectHook(errors.New("temporary network error")) {
				t.Fatal("manual stop/logout must disable further retries")
			}
		})
	}
}

func TestTransportRecoveryDeletedSessionDoesNotInitialRetry(t *testing.T) {
	client := recoveryClient(true)
	client.Store.Deleted = true
	var stopped atomic.Bool
	configureTransportRecovery(client, &stopped)
	if client.InitialAutoReconnect {
		t.Fatal("deleted sessions need explicit relinking, not automatic initial retries")
	}
}

type offlineTransport struct {
	calls atomic.Int32
	seen  chan struct{}
}

func (transport *offlineTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	select {
	case transport.seen <- struct{}{}:
	default:
	}
	// No sockets, DNS, external credentials or live WhatsApp accounts are used.
	return nil, &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
}

func TestTransportRecoveryNativeRetryAndManualStop(t *testing.T) {
	client := recoveryClient(true)
	var stopped atomic.Bool
	configureTransportRecovery(client, &stopped)
	transport := &offlineTransport{seen: make(chan struct{}, 10)}
	client.SetWebsocketHTTPClient(&http.Client{Transport: transport})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer client.Disconnect()
	if err := client.ConnectContext(ctx); err != nil {
		t.Fatalf("retryable initial failure should enter native background recovery: %v", err)
	}
	// Initial dial + immediate retry + one retry after native backoff.
	for i := 0; i < 3; i++ {
		select {
		case <-transport.seen:
		case <-time.After(5 * time.Second):
			t.Fatal("native recovery did not retry the simulated outage")
		}
	}
	mycli := &MyClient{WAClient: client}
	mycli.stopTransportRecovery()
	if !mycli.stopRequested.Load() {
		t.Fatal("stop state was not remembered")
	}
	// stopTransportRecovery also calls Disconnect: it must interrupt the
	// native backoff even though this test's hook has a separate stop flag.
	before := transport.calls.Load()
	// The next retry would be after 4 seconds with this pinned dependency.
	select {
	case <-transport.seen:
		t.Fatal("a stopped client resumed its native backoff")
	case <-time.After(4250 * time.Millisecond):
	}
	if transport.calls.Load() != before {
		t.Fatal("a stopped client started another dial")
	}
	if client.IsConnected() || client.IsLoggedIn() {
		t.Fatal("a failed/stopped transport must never look connected")
	}
}

func TestTransportRecoveryUnpairedFailureDoesNotRetryOrGenerateQR(t *testing.T) {
	client := recoveryClient(false)
	var stopped atomic.Bool
	configureTransportRecovery(client, &stopped)
	transport := &offlineTransport{seen: make(chan struct{}, 10)}
	client.SetPreLoginHTTPClient(&http.Client{Transport: transport})
	defer client.Disconnect()
	if err := client.Connect(); err == nil {
		t.Fatal("an unpaired initial failure must be reported to the caller")
	}
	time.Sleep(100 * time.Millisecond)
	if transport.calls.Load() != 1 || client.Store.ID != nil {
		t.Fatal("an unpaired failure started background pairing/recovery")
	}
}

type recoveryRepository struct {
	instance_repository.InstanceRepository
	updates atomic.Int32
}

func (repository *recoveryRepository) UpdateConnected(_ string, connected bool, _ string) error {
	if connected {
		return errors.New("an outage must not be persisted as connected")
	}
	repository.updates.Add(1)
	return nil
}

func (repository *recoveryRepository) SetResumeOnStartup(_ string, _ bool) error { return nil }

type recoveryObserver struct {
	WhatsmeowService
	reconnects atomic.Int32
	webhooks   chan []byte
}

func (observer *recoveryObserver) ReconnectClient(_ string) error {
	observer.reconnects.Add(1)
	return nil
}

func (observer *recoveryObserver) CallWebhook(_ *instance_model.Instance, _ string, payload []byte) {
	observer.webhooks <- payload
}

func recoveryEventClient(t *testing.T) (*MyClient, *recoveryRepository, *recoveryObserver) {
	t.Helper()
	cfg := &config.Config{LogDirectory: t.TempDir()}
	repository := &recoveryRepository{}
	observer := &recoveryObserver{webhooks: make(chan []byte, 10)}
	mycli := &MyClient{
		WAClient: recoveryClient(true), service: observer, userID: "recovery-test",
		Instance: &instance_model.Instance{Id: "recovery-test", Token: "synthetic-test-token", Webhook: "https://example.invalid/events", Events: "CONNECTION"},
		config:   cfg, loggerWrapper: logger_wrapper.NewLoggerManager(cfg),
		instanceRepository: repository, userInfoCache: cache.New(time.Minute, 0),
		killChannel: map[string]chan bool{"recovery-test": make(chan bool, 1)},
	}
	configureTransportRecovery(mycli.WAClient, &mycli.stopRequested)
	return mycli, repository, observer
}

func TestDisconnectedEventsPreserveSessionAndWebhookWithoutRebuilding(t *testing.T) {
	mycli, repository, observer := recoveryEventClient(t)
	client, device, original := mycli.WAClient, mycli.WAClient.Store, *mycli.Instance
	for i := 0; i < 3; i++ {
		mycli.myEventHandler(&events.Disconnected{})
		select {
		case payload := <-observer.webhooks:
			var event struct {
				Event string `json:"event"`
			}
			if err := json.Unmarshal(payload, &event); err != nil || event.Event != "Disconnected" {
				t.Fatalf("disconnection webhook was lost: %s (%v)", event.Event, err)
			}
		case <-time.After(time.Second):
			t.Fatal("disconnection webhook was not dispatched")
		}
	}
	time.Sleep(100 * time.Millisecond)
	if observer.reconnects.Load() != 0 || repository.updates.Load() != 3 {
		t.Fatal("disconnect handler rebuilt an instance or lost its status updates")
	}
	if mycli.WAClient != client || mycli.WAClient.Store != device ||
		mycli.Instance.Webhook != original.Webhook || mycli.Instance.Events != original.Events ||
		mycli.Instance.Token != original.Token || mycli.stopRequested.Load() {
		t.Fatal("a temporary outage changed the session, webhook, subscriptions or stop state")
	}
}

func TestLoggedOutStopsTransportAndStillDispatchesWebhook(t *testing.T) {
	mycli, _, observer := recoveryEventClient(t)
	mycli.myEventHandler(&events.LoggedOut{})
	if !mycli.stopRequested.Load() || mycli.WAClient.AutoReconnectHook(errors.New("network error")) {
		t.Fatal("logout left automatic recovery enabled")
	}
	select {
	case <-mycli.killChannel[mycli.userID]:
	default:
		t.Fatal("logout did not signal the owning client to stop")
	}
	select {
	case payload := <-observer.webhooks:
		var event struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(payload, &event); err != nil || event.Event != "LoggedOut" {
			t.Fatalf("logout webhook was lost: %s (%v)", event.Event, err)
		}
	case <-time.After(time.Second):
		t.Fatal("logout webhook was not dispatched")
	}
}
