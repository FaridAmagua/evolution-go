package whatsmeow_service

import (
	"errors"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	"testing"
)

type legacyIntentWriter struct {
	writes int
	value  bool
	err    error
}

func (r *legacyIntentWriter) SetResumeOnStartup(_ string, value bool) error {
	r.writes++
	r.value = value
	return r.err
}

func TestLegacyRestartIntentPromotion(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name                         string
		intent                       *bool
		connected, explicit, allowed bool
		writes                       int
	}{
		{"legacy connected", nil, true, false, true, 1},
		{"legacy stopped", nil, false, false, false, 0},
		{"opted in during outage", &yes, false, false, true, 0},
		{"explicitly stopped", &no, true, false, false, 0},
		{"explicit user resume", &no, false, true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &legacyIntentWriter{}
			instance := &instance_model.Instance{Id: "synthetic", Connected: tc.connected, ResumeOnStartup: tc.intent}
			err := prepareResumeIntent(instance, !tc.explicit, r)
			if (err == nil) != tc.allowed || r.writes != tc.writes {
				t.Fatalf("allowed=%v writes=%d err=%v", tc.allowed, r.writes, err)
			}
			if tc.writes > 0 && (!r.value || instance.ResumeOnStartup == nil || !*instance.ResumeOnStartup) {
				t.Fatal("recovery intent was not made durable")
			}
			if tc.allowed {
				instance.Connected = false // a temporary socket outage
				if err := prepareResumeIntent(instance, true, r); err != nil {
					t.Fatal("later restart lost recovery intent")
				}
			}
		})
	}
}

func TestLegacyIntentPersistenceFailureStopsRestoration(t *testing.T) {
	want := errors.New("database unavailable")
	instance := &instance_model.Instance{Id: "synthetic", Connected: true}
	err := prepareResumeIntent(instance, true, &legacyIntentWriter{err: want})
	if !errors.Is(err, want) || instance.ResumeOnStartup != nil {
		t.Fatal("failed persistence was silently treated as durable")
	}
}
