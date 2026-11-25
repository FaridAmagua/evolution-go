// Package clientstate protects the runtime maps shared by the HTTP services.
// Keep the existing map-based constructors, but never access those maps directly
// once the server starts. The lock protects map slots, not client network calls.
package clientstate

import "sync"

var mapsMu sync.RWMutex

func Load[V any](m map[string]V, key string) (V, bool) {
	mapsMu.RLock()
	defer mapsMu.RUnlock()
	v, ok := m[key]
	return v, ok
}

func Get[V any](m map[string]V, key string) V {
	v, _ := Load(m, key)
	return v
}

func Store[V any](m map[string]V, key string, value V) {
	mapsMu.Lock()
	defer mapsMu.Unlock()
	m[key] = value
}

func DeleteIf[V comparable](m map[string]V, key string, expected V) bool {
	mapsMu.Lock()
	defer mapsMu.Unlock()
	if current, ok := m[key]; !ok || current != expected {
		return false
	}
	delete(m, key)
	return true
}

// TryOperation rejects overlapping user actions instead of queuing them. A
// cancellation/stop may run after the preceding HTTP action returns; long-lived
// client ownership is managed separately by the transport service.
var operations = struct {
	sync.Mutex
	active map[string]bool
}{active: make(map[string]bool)}

func TryOperation(id string) (release func(), ok bool) {
	operations.Lock()
	defer operations.Unlock()
	if operations.active[id] {
		return nil, false
	}
	operations.active[id] = true
	var once sync.Once
	return func() {
		once.Do(func() {
			operations.Lock()
			defer operations.Unlock()
			delete(operations.active, id)
		})
	}, true
}
