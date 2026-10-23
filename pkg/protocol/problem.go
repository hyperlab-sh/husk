package protocol

// Problem is the one diagnostic type every Service, the coordinator,
// `keys.list`, `settings.validate` and `config.reload` carry.
type Problem struct {
	// Severity is "error" or "warning".
	Severity string `json:"severity"`
	// Code is a stable snake_case id; tools and tests match on it.
	Code string `json:"code"`
	// Layer is "husk", "distribution", "user", "profile", "instance",
	// "plugin:<id>", or "" when the problem is not layer-specific.
	Layer string `json:"layer"`
	// Key is a settings key, palette role, secret name or keymap keys.
	Key string `json:"key,omitempty"`
	// Message is for people, never for matching; it never holds a secret.
	Message string `json:"message"`
	// Source locates the offending text when the reporter knows it.
	Source *Source `json:"source,omitempty"`
}

// Source locates a declaration. Keybindings use it for every keymap entry.
type Source struct {
	// Layer is "husk", "distribution", "user", "profile", "instance" or
	// "focus-local".
	Layer string `json:"layer"`
	// File is config-relative, "builtin" for husk, empty for focus-local.
	File string `json:"file,omitempty"`
	// Line is 1-based when the parser reports one, else 0.
	Line int `json:"line,omitempty"`
	// Plugin is the declaring plugin (focus-local and plugin:<id> only).
	Plugin string `json:"plugin,omitempty"`
	// Order is the index in the layer's array.
	Order int `json:"order,omitempty"`
}
