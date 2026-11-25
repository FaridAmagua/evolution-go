package clientstate

import (
	"sync"
	"testing"
)

func TestConcurrentMapSlotsAndStaleCleanup(t *testing.T) {
	m := map[string]*int{}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			old, replacement := new(int), new(int)
			Store(m, "shared", old)
			Store(m, "shared", replacement)
			Get(m, "shared")
			DeleteIf(m, "shared", old)
		}()
	}
	wg.Wait()
	old, replacement := new(int), new(int)
	Store(m, "shared", replacement)
	if DeleteIf(m, "shared", old) || Get(m, "shared") != replacement {
		t.Fatal("a stale worker removed a replacement client")
	}
}

func TestOverlappingOperationsRejectWithoutQueuingAndOtherChannelIsIndependent(t *testing.T) {
	release, ok := TryOperation("personal-test")
	if !ok {
		t.Fatal("first action rejected")
	}
	defer release()
	if _, ok := TryOperation("personal-test"); ok {
		t.Fatal("second action queued/accepted")
	}
	other, ok := TryOperation("reception-test")
	if !ok {
		t.Fatal("another channel was blocked")
	}
	other()
	release()
	release() // safe even after an early-return cleanup
	next, ok := TryOperation("personal-test")
	if !ok {
		t.Fatal("completed action left a lock behind")
	}
	next()
}
