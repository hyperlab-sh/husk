package upstream

import "fmt"

// SubscriptionRefusal identifies a manifest hook that cannot be expressed by a
// name-only subscription. Err has code invalid_subscription and names the hook.
type SubscriptionRefusal struct {
	Name string
	Err  *Error
}

// FilterHookSubscriptions prepares one attach's distinct manifest hook names.
// Only names requiring pane-specific parameters are left out; all other names
// pass through unchecked and in their original order. Each refused name appears
// once, so the shim can log its Err once per attach without dropping other hooks.
// Call this afresh on each attach. It performs no I/O, logging or lifecycle work;
// the shim owns those policies. If allowed is empty, skip Subscribe: there are
// no event hooks to run. Subscribe also rejects empty or unfiltered input.
func FilterHookSubscriptions(names []string) (allowed []string, refused []SubscriptionRefusal) {
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		if err := subscriptionError(name); err != nil {
			refused = append(refused, SubscriptionRefusal{Name: name, Err: err})
			continue
		}
		allowed = append(allowed, name)
	}
	return allowed, refused
}

func subscriptionError(name string) *Error {
	switch name {
	case "pane.agent_status_changed", "pane.output_matched", "pane.scroll_changed":
		return &Error{Code: "invalid_subscription", Message: fmt.Sprintf("events.subscribe: %s requires pane-specific parameters", name)}
	default:
		return nil
	}
}
