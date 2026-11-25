package instance_service

import (
	"errors"
	"github.com/evolution-foundation/evolution-go/pkg/clientstate"
	"testing"

	"github.com/evolution-foundation/evolution-go/pkg/config"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_repository "github.com/evolution-foundation/evolution-go/pkg/instance/repository"
	logger_wrapper "github.com/evolution-foundation/evolution-go/pkg/logger"
	whatsmeow_service "github.com/evolution-foundation/evolution-go/pkg/whatsmeow/service"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

type intentRepository struct {
	instance_repository.InstanceRepository
	resume      *bool
	connected   *bool
	intentError error
}

func (r *intentRepository) SetResumeOnStartup(_ string, enabled bool) error {
	if r.intentError != nil {
		return r.intentError
	}
	r.resume = &enabled
	return nil
}
func (r *intentRepository) UpdateConnected(_ string, connected bool, _ string) error {
	r.connected = &connected
	return nil
}
func (r *intentRepository) Update(instance *instance_model.Instance) error {
	r.resume = instance.ResumeOnStartup
	return nil
}

type intentTransport struct {
	whatsmeow_service.WhatsmeowService
	starts     int
	reconnects int
	pending    bool
}

func (s *intentTransport) ReconnectClient(_ string) error  { s.reconnects++; return nil }
func (s *intentTransport) StopClient(_ string) error       { return nil }
func (s *intentTransport) PairingInProgress(_ string) bool { return s.pending }

func (s *intentTransport) StartInstance(_ string) error {
	s.starts++
	return errors.New("unexpected client start")
}
func (s *intentTransport) UpdateInstanceSettings(_ string) error { return nil }

func TestDisconnectOfflineNeverStartsAClientAndPreservesSettings(t *testing.T) {
	for _, exists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing socket", true: "offline socket"}[exists], func(t *testing.T) {
			repo, transport := &intentRepository{}, &intentTransport{}
			clients := map[string]*whatsmeow.Client{}
			if exists {
				clients["test"] = whatsmeow.NewClient(&store.Device{}, nil)
			}
			service := instances{instanceRepository: repo, whatsmeowService: transport, clientPointer: clients}
			instance := &instance_model.Instance{Id: "test", Connected: true, Events: "CONNECTION,MESSAGE", Webhook: "https://example.invalid/personal"}
			if _, err := service.Disconnect(instance); err != nil {
				t.Fatal(err)
			}
			if transport.starts != 0 || repo.resume == nil || *repo.resume || repo.connected == nil || *repo.connected {
				t.Fatal("manual stop was lost or started a connection")
			}
			if instance.Events != "CONNECTION,MESSAGE" || instance.Webhook != "https://example.invalid/personal" {
				t.Fatal("disconnect changed configured subscriptions/webhook")
			}
		})
	}
}

func TestDisconnectReportsFailureToPersistIntent(t *testing.T) {
	want := errors.New("database unavailable")
	service := instances{instanceRepository: &intentRepository{intentError: want}}
	if _, err := service.Disconnect(&instance_model.Instance{Id: "test"}); !errors.Is(err, want) {
		t.Fatalf("expected persistence error, got %v", err)
	}
}

func TestLogoutMissingClientDoesNotResurrectPairing(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{}
	service := instances{instanceRepository: repo, whatsmeowService: transport, clientPointer: map[string]*whatsmeow.Client{}}
	if _, err := service.Logout(&instance_model.Instance{Id: "test"}); err == nil {
		t.Fatal("missing authentication must not be reported as successfully unlinked")
	}
	if transport.starts != 0 || repo.resume == nil || *repo.resume {
		t.Fatal("logout restarted a stopped/missing client")
	}
}

func TestLogoutOfflineClientDoesNotClaimRemoteUnlinkSucceeded(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{}
	client := whatsmeow.NewClient(&store.Device{}, nil)
	service := instances{instanceRepository: repo, whatsmeowService: transport, clientPointer: map[string]*whatsmeow.Client{"test": client}}
	if _, err := service.Logout(&instance_model.Instance{Id: "test", Connected: true}); err == nil {
		t.Fatal("offline logout was falsely reported as confirmed")
	}
	if repo.resume == nil || *repo.resume || repo.connected == nil || *repo.connected || transport.starts != 0 {
		t.Fatal("offline logout lost the persisted stop state or restarted pairing")
	}
}

func TestExplicitConnectPersistsResumeIntent(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{}
	cfg := &config.Config{LogDirectory: t.TempDir()}
	service := instances{instanceRepository: repo, whatsmeowService: transport, clientPointer: map[string]*whatsmeow.Client{}, loggerWrapper: logger_wrapper.NewLoggerManager(cfg)}
	if _, _, _, err := service.Connect(&ConnectStruct{Subscribe: []string{"CONNECTION"}}, &instance_model.Instance{Id: "test"}); err != nil {
		t.Fatal(err)
	}
	if repo.resume == nil || !*repo.resume {
		t.Fatal("explicit connect did not persist resume intent")
	}
}

func TestExplicitReconnectCanResumeAnOfflineInstance(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{}
	service := instances{instanceRepository: repo, whatsmeowService: transport, clientPointer: map[string]*whatsmeow.Client{}}
	if err := service.Reconnect(&instance_model.Instance{Id: "test"}); err != nil {
		t.Fatal(err)
	}
	if repo.resume == nil || !*repo.resume || transport.starts != 0 || transport.reconnects != 1 {
		t.Fatal("explicit reconnect did not restore the user's intent through the reconnect path")
	}
}

func TestConcurrentUserActionsAreRejectedBeforeDatabaseOrProviderWrites(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{}
	service := instances{instanceRepository: repo, whatsmeowService: transport}
	instance := &instance_model.Instance{Id: "same-channel"}
	release, ok := clientstate.TryOperation(instance.Id)
	if !ok {
		t.Fatal("could not set up first action")
	}
	defer release()
	if err := service.Reconnect(instance); err != whatsmeow_service.ErrClientBusy {
		t.Fatal("second reconnect was not rejected")
	}
	if _, err := service.Disconnect(instance); err != whatsmeow_service.ErrClientBusy {
		t.Fatal("overlapping disconnect was not rejected")
	}
	if _, err := service.Logout(instance); err != whatsmeow_service.ErrClientBusy {
		t.Fatal("overlapping logout was not rejected")
	}
	if _, _, _, err := service.Connect(&ConnectStruct{}, instance); err != whatsmeow_service.ErrClientBusy {
		t.Fatal("overlapping connect was not rejected")
	}
	if repo.resume != nil || transport.starts != 0 || transport.reconnects != 0 {
		t.Fatal("rejected action made changes")
	}
}

func TestNewConnectCannotChangeSettingsOfAnExistingPairing(t *testing.T) {
	repo, transport := &intentRepository{}, &intentTransport{pending: true}
	service := instances{instanceRepository: repo, whatsmeowService: transport}
	instance := &instance_model.Instance{Id: "pairing-channel", Webhook: "https://example.invalid/original"}
	_, _, _, err := service.Connect(&ConnectStruct{WebhookUrl: "https://example.invalid/replacement"}, instance)
	if err != whatsmeow_service.ErrClientBusy {
		t.Fatalf("got %v", err)
	}
	if instance.Webhook != "https://example.invalid/original" || repo.resume != nil {
		t.Fatal("rejected connect overwrote pairing settings")
	}
}
