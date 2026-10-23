package protocol_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/hyperlab-sh/husk/pkg/frame"
	"github.com/hyperlab-sh/husk/pkg/protocol"
	"github.com/hyperlab-sh/husk/pkg/theme"
)

// wireField is what must agree between a wire struct and its host twin.
type wireField struct {
	json string
	typ  reflect.Type
}

// wireFields maps each exported field's full json tag (name and options, so
// omitempty counts) to its type. A field without a json tag is keyed by its Go
// name, which is what encoding/json would emit.
func wireFields(t *testing.T, typ reflect.Type) map[string]wireField {
	t.Helper()
	out := make(map[string]wireField)
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "" {
			tag = f.Name
		}
		if _, dup := out[tag]; dup {
			t.Fatalf("%v: duplicate json tag %q", typ, tag)
		}
		out[tag] = wireField{json: tag, typ: f.Type}
	}
	return out
}

// TestWireAndHostTypesStayInParity fails when a field is added, removed,
// renamed, retagged or retyped on one side of a wire/host pair only. The host
// converts these structs field by field, so a one-sided change would be dropped
// silently on the wire. No pair needs a type mapping today; add one to
// typeMapping, with the reason, instead of loosening the comparison.
func TestWireAndHostTypesStayInParity(t *testing.T) {
	typeMapping := map[string]map[reflect.Type]reflect.Type{}
	for _, tc := range []struct {
		name       string
		wire, host reflect.Type
	}{
		{"FrameSpec/frame.Spec", reflect.TypeFor[protocol.FrameSpec](), reflect.TypeFor[frame.Spec]()},
		{"Palette/theme.Palette", reflect.TypeFor[protocol.Palette](), reflect.TypeFor[theme.Palette]()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire, host := wireFields(t, tc.wire), wireFields(t, tc.host)
			var tags []string
			for tag := range wire {
				tags = append(tags, tag)
			}
			for tag := range host {
				if _, ok := wire[tag]; !ok {
					tags = append(tags, tag)
				}
			}
			sort.Strings(tags)
			for _, tag := range tags {
				w, wok := wire[tag]
				h, hok := host[tag]
				switch {
				case !hok:
					t.Errorf("json %q is on %v only", tag, tc.wire)
				case !wok:
					t.Errorf("json %q is on %v only", tag, tc.host)
				case w.typ != h.typ && typeMapping[tc.name][w.typ] != h.typ:
					t.Errorf("json %q: wire type %v, host type %v", tag, w.typ, h.typ)
				}
			}
		})
	}
}
