package control

import (
	"reflect"
	"testing"
)

// TestControlSurfaceParity is the ADR-015 bijection check (MOCK-104.2): every
// Phase 1 operationId in the route table maps to exactly one Backend or
// InstanceBackend method, and every Phase 1 method has exactly one route. A
// route added without a method, or a method added without a route, fails here —
// which is the structural guarantee that the front ends cannot drift.
func TestControlSurfaceParity(t *testing.T) {
	// The intended mapping of operationId → interface method, maintained
	// alongside the route table. The test asserts the route table's opIDs are
	// exactly this key set (no route without a mapping) and that each named
	// method exists on the corresponding interface (no mapping without a
	// method).
	mapping := map[string]struct {
		iface  reflect.Type
		method string
	}{
		opListInstances:   {reflect.TypeOf((*Backend)(nil)).Elem(), "Instances"},
		opGetInstance:     {reflect.TypeOf((*Backend)(nil)).Elem(), "Instance"},
		opGetSeed:         {reflect.TypeOf((*Backend)(nil)).Elem(), "Seed"},
		opGetHealth:       {reflect.TypeOf((*Backend)(nil)).Elem(), "Health"},
		opGetJournal:      {reflect.TypeOf((*InstanceBackend)(nil)).Elem(), "Journal"},
		opClearJournal:    {reflect.TypeOf((*InstanceBackend)(nil)).Elem(), "ClearJournal"},
		opGetCorrelations: {reflect.TypeOf((*InstanceBackend)(nil)).Elem(), "Correlations"},
		// getOpenAPI is a document route with no interface method; it is served
		// specially by the Handler and is deliberately excluded from the method
		// bijection.
	}

	routes := phase1Routes()

	// 1. Every route's opID is unique and (except getOpenAPI) has a mapping.
	seen := map[string]bool{}
	for _, r := range routes {
		if seen[r.opID] {
			t.Errorf("duplicate route opID %q", r.opID)
		}
		seen[r.opID] = true
		if r.opID == opGetOpenAPI {
			continue
		}
		m, ok := mapping[r.opID]
		if !ok {
			t.Errorf("route opID %q has no interface method mapping", r.opID)
			continue
		}
		if _, exists := m.iface.MethodByName(m.method); !exists {
			t.Errorf("mapped method %s not found on interface for opID %q", m.method, r.opID)
		}
	}

	// 2. Every mapped opID has a route.
	for opID := range mapping {
		if !seen[opID] {
			t.Errorf("mapped opID %q has no route", opID)
		}
	}

	// 3. Every Backend/InstanceBackend method is covered by a mapping, so a new
	// method cannot be added without a route (the reverse direction).
	assertAllMethodsMapped(t, reflect.TypeOf((*Backend)(nil)).Elem(), mapping, "For")
	assertAllMethodsMapped(t, reflect.TypeOf((*InstanceBackend)(nil)).Elem(), mapping,
		"JournalStream") // JournalStream is the NDJSON variant of the getJournal route.
}

// assertAllMethodsMapped checks every method of iface (except the named
// exemptions) appears as a mapped method in mapping.
func assertAllMethodsMapped(t *testing.T, iface reflect.Type, mapping map[string]struct {
	iface  reflect.Type
	method string
}, exempt ...string) {
	t.Helper()
	exemptSet := map[string]bool{}
	for _, e := range exempt {
		exemptSet[e] = true
	}
	mapped := map[string]bool{}
	for _, m := range mapping {
		if m.iface == iface {
			mapped[m.method] = true
		}
	}
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if exemptSet[name] || mapped[name] {
			continue
		}
		t.Errorf("interface method %s has no route mapping", name)
	}
}

// TestRouteMatching exercises the segment matcher, including {name} binding and
// the 404-vs-405 path/method distinction.
func TestRouteMatching(t *testing.T) {
	routes := phase1Routes()
	tests := []struct {
		name       string
		method     string
		path       string
		wantOpID   string
		wantName   string
		wantPathOK bool
		wantMethod bool
	}{
		{"list", "GET", "/instances", opListInstances, "", true, true},
		{"get named", "GET", "/instances/foo", opGetInstance, "foo", true, true},
		{"journal named", "GET", "/instances/foo/journal", opGetJournal, "foo", true, true},
		{"clear", "DELETE", "/instances/foo/journal", opClearJournal, "foo", true, true},
		{"path exists wrong method", "POST", "/instances", "", "", true, false},
		{"no such path", "GET", "/does/not/exist", "", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rc reqContext
			r, pathOK, methodOK := matchRoute(routes, tt.method, splitPath(tt.path), &rc)
			if pathOK != tt.wantPathOK || methodOK != tt.wantMethod {
				t.Fatalf("pathOK=%v methodOK=%v, want %v/%v", pathOK, methodOK, tt.wantPathOK, tt.wantMethod)
			}
			if methodOK {
				if r.opID != tt.wantOpID {
					t.Errorf("opID = %q, want %q", r.opID, tt.wantOpID)
				}
				if rc.name != tt.wantName {
					t.Errorf("bound name = %q, want %q", rc.name, tt.wantName)
				}
			}
		})
	}
}

// TestSplitPath covers leading/trailing slash handling.
func TestSplitPath(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"/instances", []string{"instances"}},
		{"/instances/", []string{"instances"}},
		{"instances/foo", []string{"instances", "foo"}},
		{"/a/b/c", []string{"a", "b", "c"}},
		{"", nil},
		{"/", nil},
	}
	for _, tt := range tests {
		got := splitPath(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("splitPath(%q) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitPath(%q)[%d] = %q, want %q", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}
