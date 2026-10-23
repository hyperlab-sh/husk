package host

import "github.com/hyperlab-sh/husk/pkg/host/contracts"

// pluginSettings is the single flag-layer builder used by host fixtures. Calls
// may set paths and dirs independently without erasing the other array.
func pluginSettings(c *contracts.Config) map[string]any {
	if c.InstanceDocs == nil {
		c.InstanceDocs = map[string]map[string]any{}
	}
	if c.InstanceDocs["config"] == nil {
		c.InstanceDocs["config"] = map[string]any{}
	}
	doc := c.InstanceDocs["config"]
	if doc["husk"] == nil {
		doc["husk"] = map[string]any{}
	}
	husk := doc["husk"].(map[string]any)
	if husk["plugins"] == nil {
		husk["plugins"] = map[string]any{}
	}
	return husk["plugins"].(map[string]any)
}
