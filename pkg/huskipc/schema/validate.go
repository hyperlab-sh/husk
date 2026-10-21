package schema

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

// Validate checks one JSON document against a schema produced by Generate and
// returns every violation, each prefixed with the JSON path it occurred at. It
// implements only the keywords the generator emits: $ref, type, properties,
// required, items, additionalProperties, enum, const, allOf, if and then.
func Validate(schemaDoc, instance []byte) ([]string, error) {
	var root map[string]any
	if err := json.Unmarshal(schemaDoc, &root); err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	var inst any
	if err := json.Unmarshal(instance, &inst); err != nil {
		return nil, fmt.Errorf("instance: %w", err)
	}
	v := &validator{root: root}
	v.check(root, inst, "$")
	return v.errs, nil
}

type validator struct {
	root map[string]any
	errs []string
}

func (v *validator) errf(path, format string, args ...any) {
	v.errs = append(v.errs, path+": "+fmt.Sprintf(format, args...))
}

// matches reports whether inst satisfies s without recording errors.
func (v *validator) matches(s map[string]any, inst any) bool {
	sub := &validator{root: v.root}
	sub.check(s, inst, "$")
	return len(sub.errs) == 0
}

func (v *validator) check(s map[string]any, inst any, path string) {
	if ref, ok := s["$ref"].(string); ok {
		target, ok := v.resolve(ref)
		if !ok {
			v.errf(path, "unresolvable $ref %q", ref)
			return
		}
		v.check(target, inst, path)
	}
	if want, ok := s["type"]; ok && !typeMatches(want, inst) {
		v.errf(path, "want type %v, got %s", want, jsonType(inst))
		return
	}
	if c, ok := s["const"]; ok && !reflect.DeepEqual(c, inst) {
		v.errf(path, "want %v, got %v", c, inst)
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if reflect.DeepEqual(e, inst) {
				found = true
			}
		}
		if !found {
			v.errf(path, "%v is not one of %v", inst, enum)
		}
	}
	switch val := inst.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, present := val[r.(string)]; !present {
					v.errf(path, "missing required property %q", r)
				}
			}
		}
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		extra, hasExtra := s["additionalProperties"].(map[string]any)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				v.check(ps, val[k], path+"."+k)
			} else if hasExtra {
				v.check(extra, val[k], path+"."+k)
			}
		}
	case []any:
		if items, ok := s["items"].(map[string]any); ok {
			for i, el := range val {
				v.check(items, el, fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, sub := range all {
			v.check(sub.(map[string]any), inst, path)
		}
	}
	if cond, ok := s["if"].(map[string]any); ok && v.matches(cond, inst) {
		if then, ok := s["then"].(map[string]any); ok {
			v.check(then, inst, path)
		}
	}
}

func (v *validator) resolve(ref string) (map[string]any, bool) {
	rest, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return nil, false
	}
	var cur any = v.root
	for _, part := range strings.Split(rest, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur = m[part]
	}
	m, ok := cur.(map[string]any)
	return m, ok
}

func typeMatches(want any, inst any) bool {
	switch w := want.(type) {
	case string:
		return oneType(w, inst)
	case []any:
		for _, t := range w {
			if s, ok := t.(string); ok && oneType(s, inst) {
				return true
			}
		}
	}
	return false
}

func oneType(t string, inst any) bool {
	switch t {
	case "object":
		_, ok := inst.(map[string]any)
		return ok
	case "array":
		_, ok := inst.([]any)
		return ok
	case "string":
		_, ok := inst.(string)
		return ok
	case "boolean":
		_, ok := inst.(bool)
		return ok
	case "null":
		return inst == nil
	case "number":
		_, ok := inst.(float64)
		return ok
	case "integer":
		f, ok := inst.(float64)
		return ok && f == math.Trunc(f)
	}
	return false
}

func jsonType(inst any) string {
	switch inst.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	}
	return fmt.Sprintf("%T", inst)
}
