package whatsmeow_service

import (
	"context"
	"errors"
	"fmt"

	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
)

var errRestoreNeedsRelink = errors.New("exact saved device unavailable; explicit relinking required")

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
