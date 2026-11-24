package whatsmeow_service

import (
	"context"
	"errors"
	"testing"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

func TestRestoreRequiresTheExactSavedDevice(t *testing.T) {
	const saved = "15550000000:14@s.whatsapp.net"
	jid, _ := types.ParseJID(saved)
	otherDevice, _ := types.ParseJID("15550000000:10@s.whatsapp.net")
	lookupError := errors.New("temporary auth database outage")
	for _, test := range []struct {
		name   string
		device *store.Device
		err    error
		wantOK bool
	}{
		{"exact", &store.Device{ID: &jid}, nil, true},
		{"missing", nil, nil, false},
		{"unpaired", &store.Device{}, nil, false},
		{"deleted", &store.Device{ID: &jid, Deleted: true}, nil, false},
		{"older same-phone device", &store.Device{ID: &otherDevice}, nil, false},
		{"database unavailable", nil, lookupError, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			device, err := lookupRestoreDevice(context.Background(), saved, func(_ context.Context, requested types.JID) (*store.Device, error) {
				calls++
				if requested.String() != saved {
					t.Fatalf("device identity changed: %s", requested.String())
				}
				return test.device, test.err
			})
			if calls != 1 || (err == nil) != test.wantOK {
				t.Fatalf("unexpected result: calls=%d err=%v", calls, err)
			}
			if test.wantOK && device != test.device {
				t.Fatal("restore created/replaced the saved device")
			}
			if test.err != nil && !errors.Is(err, test.err) {
				t.Fatal("database failure was not preserved")
			}
			if test.err != nil && errors.Is(err, errRestoreNeedsRelink) {
				t.Fatal("temporary database outage was misclassified as revoked credentials")
			}
		})
	}
}

func TestRestoreRejectsInvalidIdentityBeforeLookingUpAuth(t *testing.T) {
	for _, saved := range []string{"", "garbage", "15550000000@s.whatsapp.net", "15550000000:1@g.us", "15550000000:1@lid"} {
		t.Run(saved, func(t *testing.T) {
			_, err := lookupRestoreDevice(context.Background(), saved, func(context.Context, types.JID) (*store.Device, error) {
				t.Fatal("invalid identity reached the authentication database")
				return nil, nil
			})
			if err == nil {
				t.Fatal("invalid device accepted for automatic restoration")
			}
		})
	}
}
