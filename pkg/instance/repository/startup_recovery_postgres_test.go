package instance_repository

import (
	"net/url"
	"os"
	"strings"
	"testing"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func isolatedRecoveryDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("EVOGO_RECOVERY_TEST_DSN")
	if dsn == "" {
		t.Skip("EVOGO_RECOVERY_TEST_DSN not set")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Path != "/evogo_recovery_test" ||
		(u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "evogo-recovery-postgres-test") {
		t.Fatal("refusing non-isolated recovery test database")
	}
	return dsn
}

func TestStartupIntentSurvivesNewRepositoryWithPostgres(t *testing.T) {
	dsn := isolatedRecoveryDSN(t)
	namespace := "recovery_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	open := func() *gorm.DB {
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: namespace + "."}})
		if err != nil {
			t.Fatal(err)
		}
		pool, err := db.DB()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = pool.Close() })
		return db
	}
	db := open()
	if err := db.Exec(`CREATE SCHEMA "` + namespace + `"`).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec(`DROP SCHEMA "` + namespace + `" CASCADE`).Error; err != nil {
			t.Error(err)
		}
	})
	if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
		t.Fatal(err)
	}
	var nullable string
	if err := db.Raw("SELECT is_nullable FROM information_schema.columns WHERE table_schema = ? AND table_name = 'instances' AND column_name = 'resume_on_startup'", namespace).Scan(&nullable).Error; err != nil || nullable != "YES" {
		t.Fatalf("intent must remain nullable for conservative legacy migration: %s %v", nullable, err)
	}
	yes, no := true, false
	fixtures := []instance_model.Instance{
		{Name: "offline opted in", Token: uuid.NewString(), ClientName: "a", Jid: "15550000000:1@s.whatsapp.net", ResumeOnStartup: &yes},
		{Name: "manually stopped stale green", Token: uuid.NewString(), ClientName: "a", Jid: "15550000000:2@s.whatsapp.net", Connected: true, ResumeOnStartup: &no},
		{Name: "legacy connected", Token: uuid.NewString(), ClientName: "a", Jid: "15550000000:3@s.whatsapp.net", Connected: true},
		{Name: "legacy disconnected", Token: uuid.NewString(), ClientName: "a", Jid: "15550000000:4@s.whatsapp.net"},
		{Name: "never paired", Token: uuid.NewString(), ClientName: "a", ResumeOnStartup: &yes},
		{Name: "other client", Token: uuid.NewString(), ClientName: "b", Jid: "15550000000:5@s.whatsapp.net", ResumeOnStartup: &yes},
	}
	if err := db.Create(&fixtures).Error; err != nil {
		t.Fatal(err)
	}
	repository := &instanceRepository{db: db}
	assertCandidates := func(repo *instanceRepository, client string, names ...string) {
		t.Helper()
		rows, err := repo.GetStartupCandidates(client)
		if err != nil {
			t.Fatal(err)
		}
		found := map[string]bool{}
		for _, row := range rows {
			found[row.Name] = true
		}
		if len(rows) != len(names) {
			t.Fatalf("unexpected startup candidates: %#v", found)
		}
		for _, name := range names {
			if !found[name] {
				t.Fatalf("missing startup candidate %q: %#v", name, found)
			}
		}
	}
	assertCandidates(repository, "a", "offline opted in", "legacy connected")
	assertCandidates(repository, "", "offline opted in", "legacy connected", "other client")
	if err := repository.SetResumeOnStartup(fixtures[0].Id, false); err != nil {
		t.Fatal(err)
	}
	// A fresh connection/repository has no access to prior in-memory state.
	fresh := &instanceRepository{db: open()}
	assertCandidates(fresh, "a", "legacy connected")
	if err := fresh.SetResumeOnStartup(fixtures[0].Id, true); err != nil {
		t.Fatal(err)
	}
	if err := fresh.UpdateConnected(fixtures[0].Id, false, "temporary websocket outage"); err != nil {
		t.Fatal(err)
	}
	assertCandidates(&instanceRepository{db: open()}, "a", "offline opted in", "legacy connected")
}

// Run "before", restart the disposable PostgreSQL container, then run "after".
// Unlike the ordinary tests, the before phase intentionally retains its schema.
func TestStartupIntentAcrossActualPostgresRestart(t *testing.T) {
	phase := os.Getenv("EVOGO_RECOVERY_RESTART_PHASE")
	if phase != "before" && phase != "after" {
		t.Skip("two-phase disposable PostgreSQL restart probe not requested")
	}
	dsn := isolatedRecoveryDSN(t)
	const namespace = "recovery_restart_probe"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: namespace + "."}})
	if err != nil {
		t.Fatal(err)
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	repo := &instanceRepository{db: db}
	if phase == "before" {
		if err := db.Exec(`CREATE SCHEMA recovery_restart_probe`).Error; err != nil {
			t.Fatal(err)
		}
		// Existing deployments have no intent column yet. Migration must not
		// reinterpret their manually stopped rows as opted in.
		if err := db.Exec(`CREATE TABLE recovery_restart_probe.instances (id uuid PRIMARY KEY, name text, token text UNIQUE, client_name text, jid text, connected boolean)`).Error; err != nil {
			t.Fatal(err)
		}
		legacyID := uuid.NewString()
		if err := db.Exec(`INSERT INTO recovery_restart_probe.instances (id,name,token,client_name,jid,connected) VALUES (?,?,?,'restart-test','15550000000:1@s.whatsapp.net',true)`, legacyID, "legacy retained", uuid.NewString()).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&instance_model.Instance{}); err != nil {
			t.Fatal(err)
		}
		yes, no := true, false
		fixtures := []instance_model.Instance{
			{Name: "offline resume", Token: uuid.NewString(), ClientName: "restart-test", Jid: "15550000000:2@s.whatsapp.net", ResumeOnStartup: &yes},
			{Name: "manual stop", Token: uuid.NewString(), ClientName: "restart-test", Jid: "15550000000:3@s.whatsapp.net", Connected: true, ResumeOnStartup: &no},
		}
		if err := db.Create(&fixtures).Error; err != nil {
			t.Fatal(err)
		}
		t.Log("restart probe persisted; restart only the disposable test PostgreSQL before the after phase")
		return
	}
	t.Cleanup(func() {
		if err := db.Exec(`DROP SCHEMA recovery_restart_probe CASCADE`).Error; err != nil {
			t.Error(err)
		}
	})
	rows, err := repo.GetStartupCandidates("restart-test")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, row := range rows {
		found[row.Name] = true
	}
	if len(found) != 2 || !found["offline resume"] || !found["legacy retained"] {
		t.Fatalf("startup choices changed after PostgreSQL restart: %#v", found)
	}
	var stopped instance_model.Instance
	if err := db.Where("name = ?", "manual stop").First(&stopped).Error; err != nil {
		t.Fatal(err)
	}
	if stopped.ResumeOnStartup == nil || *stopped.ResumeOnStartup {
		t.Fatal("manual stop intent did not survive PostgreSQL restart")
	}
}
