package whatsmeow_service

import (
	"context"
	"errors"
	"fmt"
	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var errRestoreNeedsRelink = errors.New("exact saved device unavailable; explicit relinking required")

// Promote a legacy connected row once, before launching its asynchronous owner.
// Otherwise its next temporary outage would save connected=false while leaving
// intent NULL, preventing recovery after a subsequent process restart.
func prepareResumeIntent(instance *instance_model.Instance, restoreOnly bool, repository interface {
	SetResumeOnStartup(string, bool) error
}) error {
	if restoreOnly && ((instance.ResumeOnStartup != nil && !*instance.ResumeOnStartup) ||
		(instance.ResumeOnStartup == nil && !instance.Connected)) {
		return errors.New("automatic restoration was disabled for this instance")
	}
	if !restoreOnly || instance.ResumeOnStartup == nil {
		if err := repository.SetResumeOnStartup(instance.Id, true); err != nil {
			return err
		}
		resume := true
		instance.ResumeOnStartup = &resume
	}
	return nil
}

// Authentication belongs to one exact device JID, not merely to a phone.
// Automatic restoration never creates a device, searches same-phone sessions,
// strips a device suffix, or falls back to QR pairing.
func lookupRestoreDevice(ctx context.Context, savedJID string,
	lookup func(context.Context, types.JID) (*store.Device, error),
) (*store.Device, error) {
	jid, err := types.ParseJID(savedJID)
	if err != nil || jid.User == "" || jid.Device == 0 || jid.Server != types.DefaultUserServer {
		return nil, fmt.Errorf("invalid saved device identity: %w", errRestoreNeedsRelink)
	}
	device, err := lookup(ctx, jid)
	if err != nil {
		return nil, fmt.Errorf("saved device lookup failed: %w", err)
	}
	if device == nil || device.ID == nil || device.Deleted || *device.ID != jid {
		return nil, errRestoreNeedsRelink
	}
	return device, nil
}
