package catalog_test

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"testing/quick"

	"github.com/vyrodovalexey/mcp-mock-server/internal/catalogue"
	"github.com/vyrodovalexey/mcp-mock-server/internal/determinism"
	"github.com/vyrodovalexey/mcp-mock-server/scenario"
)

// --- helpers ---------------------------------------------------------------

func strp(s string) *string { return &s }
func intp(i int) *int       { return &i }

// goldenSeed/goldenInstance/goldenCount fix the vectors committed in
// testdata/names.golden. They MUST match zz generation and the golden header.
const (
	goldenSeed     = uint64(0x0123456789ABCDEF)
	goldenInstance = "golden"
	goldenCount    = 5000
)

func goldenKey(instance string) determinism.Key {
	return determinism.Root(goldenSeed).Derive(determinism.DomainInstance, []byte(instance))
}

func generatedCatalogue(tb testing.TB, instance string, count int) *catalog.Catalog {
	tb.Helper()
	cfg := &scenario.Catalog{
		Tools:             &scenario.Generated{Count: intp(count)},
		Prompts:           &scenario.Generated{Count: intp(count)},
		Resources:         &scenario.Generated{Count: intp(count)},
		ResourceTemplates: &scenario.Generated{Count: intp(count)},
	}
	return catalog.New(goldenKey(instance), cfg)
}

// --- 221.1: counts ---------------------------------------------------------

func TestLenReportsConfiguredCounts(t *testing.T) {
	t.Parallel()
	cfg := &scenario.Catalog{
		Tools:             &scenario.Generated{Count: intp(10)},
		Prompts:           &scenario.Generated{Count: intp(3)},
		Resources:         &scenario.Generated{Count: intp(7)},
		ResourceTemplates: &scenario.Generated{Count: intp(0)},
	}
	c := catalog.New(goldenKey("counts"), cfg)
	cases := []struct {
		kind catalog.Kind
		want int
	}{
		{catalog.KindTool, 10},
		{catalog.KindPrompt, 3},
		{catalog.KindResource, 7},
		{catalog.KindResourceTemplate, 0},
	}
	for _, tc := range cases {
		if got := c.Len(tc.kind); got != tc.want {
			t.Errorf("Len(%s) = %d, want %d", tc.kind, got, tc.want)
		}
	}
}

func TestNilConfigYieldsEmptyCatalogue(t *testing.T) {
	t.Parallel()
	c := catalog.New(goldenKey("empty"), nil)
	for _, k := range catalog.Kinds() {
		if got := c.Len(k); got != 0 {
			t.Errorf("Len(%s) = %d, want 0 for nil config", k, got)
		}
	}
	if _, ok := c.IndexOf(catalog.KindTool, "tool_00000"); ok {
		t.Error("IndexOf found a name in an empty catalogue")
	}
	if got := c.Len(catalog.Kind("bogus")); got != 0 {
		t.Errorf("Len(unknown kind) = %d, want 0", got)
	}
}

func TestNonNilZeroCountGeneratesNone(t *testing.T) {
	t.Parallel()
	// A non-nil pointer to 0 is a deliberate "generate none", distinct from
	// an absent block; both yield Len 0 but the distinction must not panic.
	cfg := &scenario.Catalog{Tools: &scenario.Generated{Count: intp(0)}}
	c := catalog.New(goldenKey("zero"), cfg)
	if got := c.Len(catalog.KindTool); got != 0 {
		t.Errorf("Len = %d, want 0", got)
	}
}

// --- 221.3: deterministic names, golden ------------------------------------

func TestGeneratedNamesMatchGolden(t *testing.T) {
	t.Parallel()
	c := generatedCatalogue(t, goldenInstance, goldenCount)

	f, err := os.Open("testdata/names.golden")
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	var checked int
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		// format: "<kind> <index> <name> | <description>"
		head, desc, _ := strings.Cut(line, " | ")
		fields := strings.Fields(head)
		if len(fields) != 3 {
			t.Fatalf("malformed golden line: %q", line)
		}
		kind := catalog.Kind(fields[0])
		idx, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("bad index in golden line %q: %v", line, err)
		}
		wantName := fields[2]
		it := c.At(kind, idx)
		if it.Name != wantName {
			t.Errorf("At(%s,%d).Name = %q, want golden %q", kind, idx, it.Name, wantName)
		}
		if it.Description != desc {
			t.Errorf("At(%s,%d).Description = %q, want golden %q", kind, idx, it.Description, desc)
		}
		if it.Authored {
			t.Errorf("At(%s,%d) marked Authored, want generated", kind, idx)
		}
		checked++
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan golden: %v", err)
	}
	if checked == 0 {
		t.Fatal("golden file contained no vectors")
	}
}

func TestSameSeedAndConfigYieldsIdenticalNames(t *testing.T) {
	t.Parallel()
	a := generatedCatalogue(t, "same", 256)
	b := generatedCatalogue(t, "same", 256)
	for _, k := range catalog.Kinds() {
		for i := 0; i < 256; i++ {
			if a.At(k, i).Name != b.At(k, i).Name {
				t.Fatalf("name mismatch at %s[%d]", k, i)
			}
		}
	}
}

func TestDifferentInstanceKeysDifferOnlyInContentNotNames(t *testing.T) {
	t.Parallel()
	// Names are a pure function of template+index, so two instances share
	// names but their generated descriptions differ (seed differs). This pins
	// the boundary between the name function and the content function.
	a := generatedCatalogue(t, "instA", 64)
	b := generatedCatalogue(t, "instB", 64)
	var descDiffers bool
	for i := 0; i < 64; i++ {
		ia, ib := a.At(catalog.KindTool, i), b.At(catalog.KindTool, i)
		if ia.Name != ib.Name {
			t.Fatalf("names must match across instances: %q vs %q", ia.Name, ib.Name)
		}
		if ia.Description != ib.Description {
			descDiffers = true
		}
	}
	if !descDiffers {
		t.Error("descriptions identical across distinct instance keys; content not seeded per instance")
	}
}

// crossProcessChild re-executes this test binary and prints a fingerprint of
// generated names+descriptions so a separate process (fresh Go map seed,
// scheduler) is proven to produce byte-identical output.
const crossProcessChildEnv = "CATALOGUE_CROSSPROCESS_CHILD"

func fingerprint() string {
	cfg := &scenario.Catalog{
		Tools:             &scenario.Generated{Count: intp(512)},
		Prompts:           &scenario.Generated{Count: intp(512)},
		Resources:         &scenario.Generated{Count: intp(512)},
		ResourceTemplates: &scenario.Generated{Count: intp(512)},
	}
	c := catalog.New(goldenKey(goldenInstance), cfg)
	var b strings.Builder
	for _, k := range catalog.Kinds() {
		for i := 0; i < 512; i++ {
			it := c.At(k, i)
			b.WriteString(string(k))
			b.WriteByte(' ')
			b.WriteString(it.Name)
			b.WriteByte('|')
			b.WriteString(it.Description)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func TestMain(m *testing.M) {
	if os.Getenv(crossProcessChildEnv) != "" {
		os.Stdout.WriteString(fingerprint())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNamesStableAcrossProcessesAndGOMAXPROCS(t *testing.T) {
	t.Parallel()
	want := fingerprint()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	for _, procs := range []string{"1", "8"} {
		procs := procs
		t.Run("GOMAXPROCS="+procs, func(t *testing.T) {
			t.Parallel()
			for run := 0; run < 3; run++ {
				cmd := exec.Command(exe, "-test.run=xxxDOES_NOT_MATCHxxx") //nolint:gosec // our own test binary
				cmd.Env = append(os.Environ(), crossProcessChildEnv+"=1", "GOMAXPROCS="+procs)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("child run %d: %v", run, err)
				}
				if string(out) != want {
					t.Fatalf("GOMAXPROCS=%s run %d: fingerprint differs from parent", procs, run)
				}
			}
		})
	}
}

// --- 221.5: IndexOf(At(i)) == i and inverse round-trips --------------------

func TestIndexOfAtRoundTripFullRange(t *testing.T) {
	t.Parallel()
	const count = goldenCount
	c := generatedCatalogue(t, goldenInstance, count)
	for _, k := range catalog.Kinds() {
		for i := 0; i < count; i++ {
			name := c.At(k, i).Name
			got, ok := c.IndexOf(k, name)
			if !ok {
				t.Fatalf("IndexOf(%s,%q) not found, want %d", k, name, i)
			}
			if got != i {
				t.Fatalf("IndexOf(%s,%q) = %d, want %d", k, name, got, i)
			}
		}
	}
}

func TestIndexOfRejectsNonGeneratedNames(t *testing.T) {
	t.Parallel()
	c := generatedCatalogue(t, goldenInstance, 100)
	bad := []string{
		"",               // empty
		"tool_",          // no index
		"tool_00100",     // out of range (count 100 => max 99)
		"tool_-1",        // negative
		"tool_0",         // wrong padding (canonical is tool_00000)
		"tool_000000",    // over-padded
		"prompt_00000_x", // trailing junk
		"nope_00000",     // wrong prefix
		"tool_0000a",     // non-digit
		"tool_ 00000",    // space
	}
	for _, name := range bad {
		if idx, ok := c.IndexOf(catalog.KindTool, name); ok {
			t.Errorf("IndexOf(%q) = (%d,true), want not found", name, idx)
		}
	}
}

// TestIndexOfInvariantQuick property-checks IndexOf(At(i))==i over random
// indices and random counts, so the invariant is exercised beyond the golden
// count. It would catch an off-by-one in the padding or an index/name aliasing
// bug, not merely run the code.
func TestIndexOfInvariantQuick(t *testing.T) {
	t.Parallel()
	f := func(rawCount, rawIdx uint16) bool {
		count := int(rawCount%2000) + 1
		idx := int(rawIdx) % count
		c := generatedCatalogue(t, "quick", count)
		name := c.At(catalog.KindTool, idx).Name
		got, ok := c.IndexOf(catalog.KindTool, name)
		return ok && got == idx
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 300}); err != nil {
		t.Fatal(err)
	}
}

// --- 222.1/222.2: authored overlay & precedence ----------------------------

func TestAuthoredAppendWhenNameDoesNotCollide(t *testing.T) {
	t.Parallel()
	cfg := &scenario.Catalog{
		Tools: &scenario.Generated{Count: intp(3)},
		Items: []scenario.AuthoredItem{
			{Name: "zeta_authored", Kind: strp("tool"), Title: strp("Zeta")},
			{Name: "alpha_authored", Kind: strp("tool"), Title: strp("Alpha")},
		},
	}
	c := catalog.New(goldenKey("append"), cfg)
	if got := c.Len(catalog.KindTool); got != 5 {
		t.Fatalf("Len = %d, want 5 (3 generated + 2 authored)", got)
	}
	// Generated range untouched.
	for i := 0; i < 3; i++ {
		it := c.At(catalog.KindTool, i)
		if it.Authored {
			t.Errorf("At(%d) authored, want generated", i)
		}
	}
	// Appended authored items are sorted by name: alpha_authored then zeta.
	if it := c.At(catalog.KindTool, 3); it.Name != "alpha_authored" || !it.Authored {
		t.Errorf("At(3) = %q authored=%v, want alpha_authored authored", it.Name, it.Authored)
	}
	if it := c.At(catalog.KindTool, 4); it.Name != "zeta_authored" {
		t.Errorf("At(4) = %q, want zeta_authored", it.Name)
	}
	// Round-trip holds across generated and authored.
	for i := 0; i < c.Len(catalog.KindTool); i++ {
		name := c.At(catalog.KindTool, i).Name
		if idx, ok := c.IndexOf(catalog.KindTool, name); !ok || idx != i {
			t.Errorf("IndexOf(%q)=(%d,%v), want (%d,true)", name, idx, ok, i)
		}
	}
}

func TestAuthoredReplacesGeneratedOnNameCollision(t *testing.T) {
	t.Parallel()
	// An authored item named exactly like generated index 2 replaces it in
	// place: same index, authored content, no length change, no duplicate.
	cfg := &scenario.Catalog{
		Tools: &scenario.Generated{Count: intp(5)},
		Items: []scenario.AuthoredItem{
			{Name: "tool_00002", Kind: strp("tool"), Title: strp("Overridden"), Description: strp("hand-written")},
		},
	}
	c := catalog.New(goldenKey("replace"), cfg)
	if got := c.Len(catalog.KindTool); got != 5 {
		t.Fatalf("Len = %d, want 5 (replacement does not extend)", got)
	}
	it := c.At(catalog.KindTool, 2)
	if !it.Authored {
		t.Fatal("At(2) not authored; overlay did not replace")
	}
	if it.Title != "Overridden" || it.Description != "hand-written" {
		t.Errorf("At(2) = %+v, want authored title/description", it)
	}
	// The generated name maps back to index 2 exactly once.
	idx, ok := c.IndexOf(catalog.KindTool, "tool_00002")
	if !ok || idx != 2 {
		t.Errorf("IndexOf(tool_00002) = (%d,%v), want (2,true)", idx, ok)
	}
	// It must not also appear appended.
	names := map[string]int{}
	for i := 0; i < c.Len(catalog.KindTool); i++ {
		names[c.At(catalog.KindTool, i).Name]++
	}
	if names["tool_00002"] != 1 {
		t.Errorf("tool_00002 appears %d times, want exactly 1", names["tool_00002"])
	}
}

func TestAuthoredKindRouting(t *testing.T) {
	t.Parallel()
	// An item with no explicit kind defaults to tool; explicit kinds route to
	// their own overlay and do not leak across kinds.
	cfg := &scenario.Catalog{
		Items: []scenario.AuthoredItem{
			{Name: "defaulted"},                              // -> tool
			{Name: "a_prompt", Kind: strp("prompt")},         // -> prompt
			{Name: "a_res", Kind: strp("resource")},          // -> resource
			{Name: "a_tmpl", Kind: strp("resourceTemplate")}, // -> resourceTemplate
		},
	}
	c := catalog.New(goldenKey("routing"), cfg)
	want := map[catalog.Kind]string{
		catalog.KindTool:             "defaulted",
		catalog.KindPrompt:           "a_prompt",
		catalog.KindResource:         "a_res",
		catalog.KindResourceTemplate: "a_tmpl",
	}
	for k, name := range want {
		if got := c.Len(k); got != 1 {
			t.Errorf("Len(%s) = %d, want 1", k, got)
		}
		if it := c.At(k, 0); it.Name != name {
			t.Errorf("At(%s,0) = %q, want %q", k, it.Name, name)
		}
	}
}

func TestDuplicateAuthoredNameLastWins(t *testing.T) {
	t.Parallel()
	cfg := &scenario.Catalog{
		Items: []scenario.AuthoredItem{
			{Name: "dup", Title: strp("first")},
			{Name: "dup", Title: strp("second")},
		},
	}
	c := catalog.New(goldenKey("dup"), cfg)
	if got := c.Len(catalog.KindTool); got != 1 {
		t.Fatalf("Len = %d, want 1 (duplicate collapsed)", got)
	}
	if it := c.At(catalog.KindTool, 0); it.Title != "second" {
		t.Errorf("Title = %q, want last-wins %q", it.Title, "second")
	}
}

// --- 222.3/222.4: verbatim raw payloads ------------------------------------

func TestAuthoredRawFieldsEmittedVerbatim(t *testing.T) {
	t.Parallel()
	// Keys deliberately NOT in alphabetical order; must survive byte-for-byte.
	rawSchema := json.RawMessage(`{"zeta":1,"alpha":2,"nested":{"y":true,"x":false}}`)
	rawAnnot := json.RawMessage(`{"b":"2","a":"1"}`)
	rawIcons := json.RawMessage(`[{"src":"data:...","sizes":"48x48"}]`)
	rawOut := json.RawMessage(`{"result":{"type":"string"}}`)
	cfg := &scenario.Catalog{
		Items: []scenario.AuthoredItem{{
			Name:         "verbatim",
			InputSchema:  rawSchema,
			OutputSchema: rawOut,
			Annotations:  rawAnnot,
			Icons:        rawIcons,
			URI:          strp("mcp://res/1"),
		}},
	}
	c := catalog.New(goldenKey("verbatim"), cfg)
	it := c.At(catalog.KindTool, 0)
	if string(it.InputSchema) != string(rawSchema) {
		t.Errorf("InputSchema = %s, want verbatim %s", it.InputSchema, rawSchema)
	}
	if string(it.OutputSchema) != string(rawOut) {
		t.Errorf("OutputSchema = %s, want verbatim %s", it.OutputSchema, rawOut)
	}
	if string(it.Annotations) != string(rawAnnot) {
		t.Errorf("Annotations = %s, want verbatim %s", it.Annotations, rawAnnot)
	}
	if string(it.Icons) != string(rawIcons) {
		t.Errorf("Icons = %s, want verbatim %s", it.Icons, rawIcons)
	}
	if it.URI != "mcp://res/1" {
		t.Errorf("URI = %q, want mcp://res/1", it.URI)
	}
}

// --- ordering guarantee: no map-randomness leak ----------------------------

func TestIterationOrderDeterministicAcrossConstructions(t *testing.T) {
	t.Parallel()
	// Build the SAME overlay many times; the appended order (sorted by name)
	// must be identical every time regardless of Go map iteration randomness.
	mk := func() []string {
		cfg := &scenario.Catalog{
			Tools: &scenario.Generated{Count: intp(2)},
			Items: []scenario.AuthoredItem{
				{Name: "m_item"}, {Name: "a_item"}, {Name: "z_item"},
				{Name: "b_item"}, {Name: "k_item"},
			},
		}
		c := catalog.New(goldenKey("order"), cfg)
		var got []string
		for i := 0; i < c.Len(catalog.KindTool); i++ {
			got = append(got, c.At(catalog.KindTool, i).Name)
		}
		return got
	}
	first := mk()
	for run := 0; run < 50; run++ {
		if got := mk(); strings.Join(got, ",") != strings.Join(first, ",") {
			t.Fatalf("iteration order not stable: run %d = %v, first = %v", run, got, first)
		}
	}
	// Appended part sorted: a_item, b_item, k_item, m_item, z_item after 2 gen.
	wantTail := []string{"a_item", "b_item", "k_item", "m_item", "z_item"}
	for j, w := range wantTail {
		if first[2+j] != w {
			t.Errorf("appended[%d] = %q, want %q", j, first[2+j], w)
		}
	}
}

// --- template configuration ------------------------------------------------

func TestCustomNameTemplate(t *testing.T) {
	t.Parallel()
	cfg := &scenario.Catalog{
		Tools: &scenario.Generated{Count: intp(3), NameTemplate: strp("svc-{{i:03d}}-tool")},
	}
	c := catalog.New(goldenKey("tmpl"), cfg)
	want := []string{"svc-000-tool", "svc-001-tool", "svc-002-tool"}
	for i, w := range want {
		if got := c.At(catalog.KindTool, i).Name; got != w {
			t.Errorf("At(%d).Name = %q, want %q", i, got, w)
		}
		if idx, ok := c.IndexOf(catalog.KindTool, w); !ok || idx != i {
			t.Errorf("IndexOf(%q) = (%d,%v), want (%d,true)", w, idx, ok, i)
		}
	}
}

// --- out-of-range panic contract -------------------------------------------

func TestAtPanicsOutOfRange(t *testing.T) {
	t.Parallel()
	c := generatedCatalogue(t, "panic", 3)
	for _, i := range []int{-1, 3, 100} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("At(%d) did not panic on out-of-range", i)
				}
			}()
			_ = c.At(catalog.KindTool, i)
		}()
	}
}

func TestKindFromString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		want   catalog.Kind
		wantOK bool
	}{
		{"tool", catalog.KindTool, true},
		{"prompt", catalog.KindPrompt, true},
		{"resource", catalog.KindResource, true},
		{"resourceTemplate", catalog.KindResourceTemplate, true},
		{"", catalog.KindTool, true},
		{"bogus", catalog.KindTool, false},
	}
	for _, tc := range cases {
		got, ok := catalog.KindFromString(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("catalog.KindFromString(%q) = (%s,%v), want (%s,%v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
