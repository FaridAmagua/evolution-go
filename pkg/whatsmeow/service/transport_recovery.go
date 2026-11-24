package whatsmeow_service

import (
	"errors"
	"sync/atomic"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// Keep transport recovery inside whatsmeow. Recreating the entire instance from
// a Disconnected handler races its old client's cleanup, subscriptions and maps.
// Configure once, before Connect; do not mutate these fields while it is running.
func configureTransportRecovery(client *whatsmeow.Client, stopped *atomic.Bool) {
	client.EnableAutoReconnect = true
	// Initial retries are useful for a saved session during a network outage,
	// but must never silently start/restart an unpaired QR flow.
	client.InitialAutoReconnect = client.Store != nil && client.Store.ID != nil && !client.Store.Deleted
	client.AutoReconnectHook = func(err error) bool {
		return !stopped.Load() && !errors.Is(err, store.ErrDeviceDeleted)
	}
}

func (mycli *MyClient) stopTransportRecovery() {
	mycli.stopRequested.Store(true)
	// Disconnect also wakes whatsmeow's retry backoff immediately. A stop flag
	// alone is insufficient: its hook only runs after a failed retry.
	mycli.WAClient.Disconnect()
}
