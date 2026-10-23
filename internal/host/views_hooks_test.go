package host

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hyperlab-sh/husk/internal/stem/calls"
	"github.com/hyperlab-sh/husk/internal/stem/hooks"
	"github.com/hyperlab-sh/husk/internal/substrate/store"
	"github.com/hyperlab-sh/husk/pkg/protocol"
)

// hookDoc builds a declared plugins document with the given publish entries
// ({id, kind, timeout_ms}) and subscribe entries ({id, priority}).
func hookDoc(publish []hookDecl, subscribe ...hookReg) store.Doc {
	return store.Doc{"hooks_declared": publish, "hook_registrations": subscribe}
}

func gateDecl(id string) hookDecl { return hookDecl{ID: id, Kind: "gate", TimeoutMs: 10000} }

// docsView is a catalogView over a mutable set of declared documents plus a
// log of what it reported.
type docsView struct {
	*catalogView
	docs map[string]store.Doc
	logs []string
}

func newDocsView() *docsView {
	d := &docsView{docs: map[string]store.Doc{}}
	d.catalogView = newCatalogView(nil)
	d.declared = func() map[string]store.Doc { return d.docs }
	d.logf = func(f string, a ...any) { d.logs = append(d.logs, fmt.Sprintf(f, a...)) }
	return d
}

func (d *docsView) set(plugin string, doc store.Doc) *docsView {
	d.docs[plugin] = doc
	d.refresh()
	return d
}

func listIDs(c *catalogView) []string {
	var ids []string
	for _, def := range c.List() {
		ids = append(ids, def.ID)
	}
	return ids
}

func TestCatalogLookupCarriesOwnerKindTimeout(t *testing.T) {
	d := newDocsView()
	d.set("notes", hookDoc([]hookDecl{
		{ID: "notes.resolve", Kind: "transform", TimeoutMs: 2500, Title: "Resolve", Description: "which viewer"},
		gateDecl("notes.before-delete"),
	}))
	def, ok := d.Lookup("notes.resolve")
	want := hooks.HookDef{ID: "notes.resolve", Owner: "notes", Kind: hooks.Transform, Timeout: 2500 * time.Millisecond, Title: "Resolve", Description: "which viewer"}
	if !ok || def != want {
		t.Fatalf("Lookup = %+v %v, want %+v", def, ok, want)
	}
	if def, ok := d.Lookup("notes.before-delete"); !ok || def.Kind != hooks.Gate || def.Timeout != 10*time.Second {
		t.Fatalf("gate = %+v %v", def, ok)
	}
	host, ok := d.Lookup("husk.before-state-clear")
	if !ok || host.Owner != hooks.HostOwner || host.Kind != hooks.Gate || host.Timeout != 2*time.Second || host.Title != "Before state clear" {
		t.Fatalf("host hook = %+v %v", host, ok)
	}
	if got := listIDs(d.catalogView); !slices.Equal(got, []string{"husk.before-state-clear", "notes.before-delete", "notes.resolve"}) {
		t.Fatalf("List = %v", got)
	}
	if _, ok := newDocsView().Lookup("husk.before-state-clear"); !ok {
		t.Fatal("host hook missing before any plugin loads")
	}
}

func TestCatalogSubscribersSortedAndReplacedOnRefresh(t *testing.T) {
	d := newDocsView()
	d.set("notes", hookDoc([]hookDecl{gateDecl("notes.h")}))
	d.set("zed", hookDoc(nil, hookReg{"notes.h", 5}))
	d.set("alpha", hookDoc(nil, hookReg{"notes.h", 10}))
	d.set("mid", hookDoc(nil, hookReg{"notes.h", 10}))
	want := []hooks.Subscription{{Plugin: "zed", Priority: 5}, {Plugin: "alpha", Priority: 10}, {Plugin: "mid", Priority: 10}}
	if got := d.Subscribers("notes.h"); !slices.Equal(got, want) {
		t.Fatalf("Subscribers = %v, want %v", got, want)
	}
	// A reload replaces the table: alpha moves ahead of zed, mid leaves.
	d.docs["alpha"] = hookDoc(nil, hookReg{"notes.h", 1})
	delete(d.docs, "mid")
	d.refresh()
	want = []hooks.Subscription{{Plugin: "alpha", Priority: 1}, {Plugin: "zed", Priority: 5}}
	if got := d.Subscribers("notes.h"); !slices.Equal(got, want) {
		t.Fatalf("Subscribers after reload = %v, want %v", got, want)
	}
	if got := d.Subscribers("nobody.declares"); len(got) != 0 {
		t.Fatalf("Subscribers of unknown hook = %v", got)
	}
	// Mutating the returned slice never reaches the table.
	d.Subscribers("notes.h")[0].Plugin = "evil"
	if got := d.Subscribers("notes.h")[0].Plugin; got != "alpha" {
		t.Fatalf("table aliased: %q", got)
	}
}

func TestCatalogNamespaceRule(t *testing.T) {
	nested := hookDoc([]hookDecl{gateDecl("go.reference.x")})
	t.Run("both declare it, either launch order: the inner plugin keeps it", func(t *testing.T) {
		for _, order := range [][]string{{"go", "go.reference"}, {"go.reference", "go"}} {
			d := newDocsView()
			for _, id := range order {
				d.set(id, nested)
			}
			def, ok := d.Lookup("go.reference.x")
			if !ok || def.Owner != "go.reference" {
				t.Fatalf("order %v: Lookup = %+v %v", order, def, ok)
			}
			if p := d.Problems(); len(p["go"]) != 1 || len(p["go.reference"]) != 0 {
				t.Fatalf("order %v: problems = %v", order, p)
			}
		}
	})
	t.Run("kept while the inner plugin is not loaded, lost once it loads without hooks", func(t *testing.T) {
		d := newDocsView().set("go", nested)
		if def, ok := d.Lookup("go.reference.x"); !ok || def.Owner != "go" {
			t.Fatalf("before: %+v %v", def, ok)
		}
		if len(d.Problems()) != 0 {
			t.Fatalf("problems before = %v", d.Problems())
		}
		d.set("go.reference", store.Doc{})
		if _, ok := d.Lookup("go.reference.x"); ok {
			t.Fatal("refused declaration still in Lookup")
		}
		if slices.Contains(listIDs(d.catalogView), "go.reference.x") {
			t.Fatal("refused declaration still in List")
		}
		if p := d.Problems()["go"]; len(p) != 1 || !strings.Contains(p[0], "go.reference") {
			t.Fatalf("problems = %v", d.Problems())
		}
		// The declaration returns when the conflict ends.
		delete(d.docs, "go.reference")
		d.refresh()
		if def, ok := d.Lookup("go.reference.x"); !ok || def.Owner != "go" {
			t.Fatalf("after: %+v %v", def, ok)
		}
		if len(d.Problems()) != 0 {
			t.Fatalf("problems after = %v", d.Problems())
		}
	})
	t.Run("the longest loaded prefix owns the namespace", func(t *testing.T) {
		d := newDocsView()
		d.set("a", hookDoc([]hookDecl{gateDecl("a.b.c.x")}))
		d.set("a.b", store.Doc{})
		d.set("a.b.c", hookDoc([]hookDecl{gateDecl("a.b.c.x")}))
		if def, ok := d.Lookup("a.b.c.x"); !ok || def.Owner != "a.b.c" {
			t.Fatalf("Lookup = %+v %v", def, ok)
		}
		if p := d.Problems(); len(p["a"]) != 1 || len(p["a.b.c"]) != 0 {
			t.Fatalf("problems = %v", p)
		}
	})
	t.Run("a declaration outside the declarer's own namespace is refused", func(t *testing.T) {
		d := newDocsView().set("notes", hookDoc([]hookDecl{gateDecl("elsewhere.x")}))
		if _, ok := d.Lookup("elsewhere.x"); ok {
			t.Fatal("outside declaration accepted")
		}
		if len(d.Problems()["notes"]) != 1 {
			t.Fatalf("problems = %v", d.Problems())
		}
	})
}

func TestCatalogDuplicateRuleRefusesEveryDeclarer(t *testing.T) {
	d := newDocsView().set("p", hookDoc([]hookDecl{gateDecl("p.x"), gateDecl("p.x"), gateDecl("p.y")}))
	if _, ok := d.Lookup("p.x"); ok {
		t.Fatal("contested hook still fireable")
	}
	if _, ok := d.Lookup("p.y"); !ok {
		t.Fatal("uncontested hook lost")
	}
	got := d.Problems()["p"]
	if len(got) != 1 || !strings.Contains(got[0], "declared more than once") || strings.Contains(got[0], "plugin") {
		t.Fatalf("problems = %v, want one line saying the id is declared more than once", d.Problems())
	}
}

func TestCatalogRefusesUnknownKindAndHostHookIDs(t *testing.T) {
	d := newDocsView().set("p", hookDoc([]hookDecl{
		{ID: "p.empty", TimeoutMs: 1000},
		{ID: "p.odd", Kind: "weird", TimeoutMs: 1000},
		gateDecl("p.ok"),
	}))
	for _, id := range []string{"p.empty", "p.odd"} {
		if _, ok := d.Lookup(id); ok {
			t.Fatalf("%s entered the catalog", id)
		}
	}
	if _, ok := d.Lookup("p.ok"); !ok {
		t.Fatal("valid sibling lost")
	}
	if len(d.Problems()["p"]) != 2 {
		t.Fatalf("problems = %v", d.Problems())
	}

	// A plugin declaring the host's id is refused and the host definition
	// stays as the host wrote it, whichever id the plugin carries.
	d = newDocsView().set("husk", hookDoc([]hookDecl{{ID: "husk.before-state-clear", Kind: "transform", TimeoutMs: 9000}}))
	def, ok := d.Lookup("husk.before-state-clear")
	if !ok || def.Owner != hooks.HostOwner || def.Kind != hooks.Gate || def.Timeout != 2*time.Second {
		t.Fatalf("host hook = %+v %v", def, ok)
	}
	if len(d.Problems()["husk"]) != 1 {
		t.Fatalf("problems = %v", d.Problems())
	}
}

func TestCatalogLogsProblemsInPluginOrder(t *testing.T) {
	d := newDocsView()
	for _, id := range []string{"zed", "mid", "alpha", "beta"} {
		d.docs[id] = hookDoc([]hookDecl{gateDecl("elsewhere." + id)})
	}
	d.refresh()
	want := []string{"alpha", "beta", "mid", "zed"}
	if len(d.logs) != len(want) {
		t.Fatalf("logs = %q", d.logs)
	}
	for i, id := range want {
		if !strings.Contains(d.logs[i], "plugin "+id+":") {
			t.Fatalf("log %d = %q, want plugin %s first", i, d.logs[i], id)
		}
	}
}

func TestCatalogLogsZeroTimeoutsInHookOrder(t *testing.T) {
	d := newDocsView()
	d.set("p", hookDoc([]hookDecl{{ID: "p.c", Kind: "gate"}, {ID: "p.a", Kind: "gate"}, {ID: "p.b", Kind: "gate"}}))
	if len(d.logs) != 3 {
		t.Fatalf("logs = %q", d.logs)
	}
	for i, id := range []string{"p.a", "p.b", "p.c"} {
		if !strings.Contains(d.logs[i], id) {
			t.Fatalf("log %d = %q, want %s", i, d.logs[i], id)
		}
	}
}

func TestCatalogZeroTimeoutIsLoggedOnceAndLeftOut(t *testing.T) {
	d := newDocsView()
	d.set("p", hookDoc([]hookDecl{{ID: "p.x", Kind: "gate"}, gateDecl("p.y")}))
	d.refresh()
	d.refresh()
	if _, ok := d.Lookup("p.x"); ok {
		t.Fatal("zero-timeout hook in the catalog")
	}
	if slices.Contains(listIDs(d.catalogView), "p.x") {
		t.Fatal("zero-timeout hook in List")
	}
	if _, ok := d.Lookup("p.y"); !ok {
		t.Fatal("sibling lost")
	}
	if len(d.logs) != 1 || !strings.Contains(d.logs[0], "p.x") {
		t.Fatalf("logs = %q, want one line naming p.x", d.logs)
	}
	// Fixed, then broken again: logged again.
	d.set("p", hookDoc([]hookDecl{{ID: "p.x", Kind: "gate", TimeoutMs: 1000}}))
	d.set("p", hookDoc([]hookDecl{{ID: "p.x", Kind: "gate"}}))
	if len(d.logs) != 2 {
		t.Fatalf("logs = %q", d.logs)
	}
}

func TestCatalogProblemsAreLoggedOncePerRebuildChange(t *testing.T) {
	d := newDocsView()
	d.set("go", hookDoc([]hookDecl{gateDecl("go.reference.x")}))
	d.set("go.reference", store.Doc{})
	d.refresh()
	if len(d.logs) != 1 {
		t.Fatalf("logs = %q, want the problem once", d.logs)
	}
}

type recordingOutbox struct {
	sent []struct {
		plugin calls.PluginID
		msg    protocol.Message
	}
}

func (o *recordingOutbox) Send(p calls.PluginID, m protocol.Message) error {
	o.sent = append(o.sent, struct {
		plugin calls.PluginID
		msg    protocol.Message
	}{p, m})
	return nil
}

type allLive struct{}

func (allLive) Live(string) bool { return true }

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

// A call that already started keeps the subscribers it chose, whatever a
// reload does to the catalog meanwhile (invariant 10).
func TestCatalogRebuildKeepsAnInFlightCallsSubscribers(t *testing.T) {
	d := newDocsView()
	d.set("owner", hookDoc([]hookDecl{{ID: "owner.t", Kind: "transform", TimeoutMs: 10000}}))
	d.set("b", hookDoc(nil, hookReg{"owner.t", 5}))
	d.set("c", hookDoc(nil, hookReg{"owner.t", 10}))
	box := &recordingOutbox{}
	table := calls.New(box, calls.TableOptions{Clock: fixedClock{time.Unix(1000, 0)}})
	runner, err := hooks.New(hooks.Options{Catalog: d.catalogView, Liveness: allLive{}, Calls: table})
	if err != nil {
		t.Fatal(err)
	}
	var out *hooks.Outcome
	if err := runner.Call(hooks.Caller{Kind: hooks.CallerPlugin, PluginID: "owner"}, "owner.t", map[string]any{"n": 0}, func(o hooks.Outcome) { out = &o }); err != nil {
		t.Fatal(err)
	}
	if len(box.sent) != 1 || box.sent[0].plugin != "b" {
		t.Fatalf("sent = %+v", box.sent)
	}
	// Reload: c unsubscribes, b leaves the catalog entirely.
	delete(d.docs, "b")
	d.docs["c"] = hookDoc(nil)
	d.refresh()
	if got := d.Subscribers("owner.t"); len(got) != 0 {
		t.Fatalf("catalog after rebuild = %v", got)
	}
	reply := func(i int, data string) {
		t.Helper()
		res, ok := table.Resolve(box.sent[i].plugin, protocol.Message{ID: box.sent[i].msg.ID, Result: json.RawMessage(data)})
		if !ok {
			t.Fatalf("reply %d unmatched", i)
		}
		runner.Complete(res)
	}
	reply(0, `{"data":{"n":1}}`)
	if len(box.sent) != 2 || box.sent[1].plugin != "c" {
		t.Fatalf("second stage not sent to c: %+v", box.sent)
	}
	reply(1, `{"data":{"n":2}}`)
	if out == nil || fmt.Sprint(out.Data["n"]) != "2" || len(out.Results) != 2 {
		t.Fatalf("outcome = %+v", out)
	}
}
