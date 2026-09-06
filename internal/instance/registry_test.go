package instance_test

import (
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/internal/instance"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

func mkInst(name, mount string, id int) *instance.Instance {
	return instance.New(instance.Config{
		Name: name, MountPath: mount, ID: id,
		Root: determinism.Root(uint64(id)), Spec: scenario.InstanceSpec{},
	})
}

// TestRegistryAddLookupRemove covers the basic registry lifecycle: add, look up,
// remove (acceptance criterion 5 mechanics).
func TestRegistryAddLookupRemove(t *testing.T) {
	reg := instance.NewRegistry()
	if reg.Len() != 0 {
		t.Fatalf("new registry not empty: %d", reg.Len())
	}
	a := mkInst("a", "/mock/a/mcp", 1)
	if !reg.Add(a) {
		t.Fatal("Add of a fresh instance must succeed")
	}
	if reg.Len() != 1 {
		t.Fatalf("Len after add = %d, want 1", reg.Len())
	}
	got, ok := reg.Lookup("/mock/a/mcp")
	if !ok || got != a {
		t.Fatal("Lookup did not return the added instance")
	}
	if _, ok := reg.Lookup("/mock/missing/mcp"); ok {
		t.Fatal("Lookup of an unregistered path must report not found")
	}
	if !reg.Remove("/mock/a/mcp") {
		t.Fatal("Remove of a registered path must succeed")
	}
	if _, ok := reg.Lookup("/mock/a/mcp"); ok {
		t.Fatal("Lookup after Remove must report not found")
	}
	if reg.Len() != 0 {
		t.Fatalf("Len after remove = %d, want 0", reg.Len())
	}
}

// TestRegistryDuplicateRegistration asserts a duplicate mount path is refused,
// not silently overwritten.
func TestRegistryDuplicateRegistration(t *testing.T) {
	reg := instance.NewRegistry()
	a := mkInst("a", "/mock/x/mcp", 1)
	bAtSamePath := mkInst("b", "/mock/x/mcp", 2)
	if !reg.Add(a) {
		t.Fatal("first Add must succeed")
	}
	if reg.Add(bAtSamePath) {
		t.Fatal("duplicate Add at the same path must be refused")
	}
	got, _ := reg.Lookup("/mock/x/mcp")
	if got != a {
		t.Fatal("duplicate Add overwrote the original instance")
	}
	if reg.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after refused duplicate", reg.Len())
	}
}

// TestRegistryRemoveMissing asserts removing an unregistered path is a no-op
// returning false.
func TestRegistryRemoveMissing(t *testing.T) {
	reg := instance.NewRegistry()
	if reg.Remove("/nope") {
		t.Fatal("Remove of an unregistered path must return false")
	}
}

// TestRegistryDefaultMountPath asserts an instance with no explicit mount path
// registers under its name.
func TestRegistryDefaultMountPath(t *testing.T) {
	reg := instance.NewRegistry()
	inst := instance.New(instance.Config{Name: "byname", Root: determinism.Root(1), Spec: scenario.InstanceSpec{}})
	if inst.MountPath() != "byname" {
		t.Fatalf("default mount path = %q, want %q", inst.MountPath(), "byname")
	}
	if !reg.Add(inst) {
		t.Fatal("add must succeed")
	}
	if _, ok := reg.Lookup("byname"); !ok {
		t.Fatal("instance not registered under its name")
	}
}

// TestRegistryConcurrentAddRemoveLookup asserts that adding and removing
// instances does not disturb concurrent lookups on other instances (acceptance
// criterion 5), under -race. A fixed set of "stable" instances stays registered
// throughout; lookups on them must always succeed while a churner adds and
// removes a separate "transient" instance.
func TestRegistryConcurrentAddRemoveLookup(t *testing.T) {
	reg := instance.NewRegistry()
	const stable = 32
	stablePaths := make([]string, stable)
	for i := 0; i < stable; i++ {
		p := "/mock/stable" + strconv.Itoa(i) + "/mcp"
		stablePaths[i] = p
		if !reg.Add(mkInst("stable"+strconv.Itoa(i), p, i)) {
			t.Fatalf("seed add %d failed", i)
		}
	}

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Churner: repeatedly add and remove a transient instance.
	wg.Add(1)
	go func() {
		defer wg.Done()
		tp := "/mock/transient/mcp"
		for !stop.Load() {
			reg.Add(mkInst("transient", tp, 9999))
			reg.Remove(tp)
		}
	}()

	// Lookers: stable instances must always be found.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20000; i++ {
				p := stablePaths[i%stable]
				if _, ok := reg.Lookup(p); !ok {
					t.Errorf("stable instance %s vanished under concurrent churn", p)
					return
				}
			}
		}()
	}

	time.Sleep(20 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	if reg.Len() != stable {
		t.Fatalf("Len = %d after churn, want %d", reg.Len(), stable)
	}
}

// TestRegistryInstances asserts Instances returns a fresh, complete slice.
func TestRegistryInstances(t *testing.T) {
	reg := instance.NewRegistry()
	for i := 0; i < 5; i++ {
		reg.Add(mkInst("i"+strconv.Itoa(i), "/mock/i"+strconv.Itoa(i)+"/mcp", i))
	}
	got := reg.Instances()
	if len(got) != 5 {
		t.Fatalf("Instances len = %d, want 5", len(got))
	}
	// Mutating the returned slice must not affect the registry.
	got[0] = nil
	if reg.Len() != 5 {
		t.Fatal("mutating the returned slice affected the registry")
	}
}
