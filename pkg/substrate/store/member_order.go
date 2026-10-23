package store

import (
	"bytes"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/tailscale/hujson"
)

// memberOrder retains source ordering without changing the decoded document.
func memberOrder(raw []byte) map[string][]string {
	b, err := hujson.Standardize(bytes.Clone(raw))
	if err != nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	out := map[string][]string{}
	var walk func(string)
	walk = func(path string) {
		tok, err := d.Token()
		if err != nil {
			return
		}
		switch tok {
		case json.Delim('{'):
			for d.More() {
				key, _ := d.Token()
				k := key.(string)
				out[path] = append(out[path], k)
				walk(path + "/" + strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1"))
			}
			_, _ = d.Token()
		case json.Delim('['):
			for i := 0; d.More(); i++ {
				walk(path + "/" + strconv.Itoa(i))
			}
			_, _ = d.Token()
		}
	}
	walk("")
	return out
}

func copyOrder(order map[string][]string) map[string][]string {
	out := make(map[string][]string, len(order))
	for p, keys := range order {
		out[p] = append([]string(nil), keys...)
	}
	return out
}

// overlayOrder follows the same first-position rule as object merge: replaced
// members keep their position, newly introduced members follow in source order.
func overlayOrder(lower, higher map[string][]string) map[string][]string {
	out := copyOrder(lower)
	for p, keys := range higher {
		seen := map[string]bool{}
		for _, k := range out[p] {
			seen[k] = true
		}
		for _, k := range keys {
			if !seen[k] {
				out[p] = append(out[p], k)
				seen[k] = true
			}
		}
	}
	return out
}

func docOrder(doc Doc) map[string][]string {
	b, _ := json.Marshal(doc) // encoding/json sorts programmatic object keys.
	return memberOrder(b)
}

// OrderedMembers returns existing keys at pointer in document order. Missing
// metadata (for programmatic layers) is completed in lexical order.
func (l LayerDoc) OrderedMembers(pointer string, object Doc) []string {
	out := make([]string, 0, len(object))
	seen := map[string]bool{}
	for _, k := range l.MemberOrder[pointer] {
		if _, ok := object[k]; ok && !seen[k] {
			out = append(out, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range object {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	return append(out, rest...)
}
