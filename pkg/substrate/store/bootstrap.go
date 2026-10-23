package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func defaultEnv(env Env) Env {
	if env.LookupEnv == nil {
		env.LookupEnv = os.LookupEnv
	}
	if env.HomeDir == nil {
		env.HomeDir = os.UserHomeDir
	}
	return env
}

// resolveBase is the pure half of Open's directory setup, shared by bootstrap.
func resolveBase(env Env, key, fallback string) (string, error) {
	p, _ := env.LookupEnv(key)
	if p == "" {
		home, err := env.HomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, fallback)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("store: %s must be absolute", key)
	}
	return filepath.Join(p, "husk"), nil
}

// Bootstrap is the launch-only configuration known before Open.
type Bootstrap struct {
	Bundle      string
	BundleLayer Layer
}

// BootstrapError identifies a bundle setting that cannot be honoured at start.
// Store owns this type so it need not import the bundle loader.
type BootstrapError struct{ Code, Message string }

func (e *BootstrapError) Error() string { return "bundle: " + e.Message }

// ReadBootstrap reads user, named-instance and flag config in precedence order.
// It reads no profile or distribution, opens no directory, creates nothing and
// caches nothing. Unparsable files are absent here; Resolve diagnoses them later.
func ReadBootstrap(env Env, inst InstanceRef, instanceDocs map[DocName]Doc) (Bootstrap, error) {
	env = defaultEnv(env)
	root, err := resolveBase(env, "XDG_CONFIG_HOME", ".config")
	if err != nil {
		return Bootstrap{}, err
	}
	merged := Doc{}
	origins := map[string]Layer{}
	var problems []Problem
	read := func(layer Layer, parts ...string) {
		path, err := pathInside(root, parts, false)
		if err != nil {
			return
		}
		raw, _, err := readFile(path, MaxDocBytes, false)
		if err != nil {
			return
		}
		doc, err := ParseDoc(raw)
		if err == nil {
			mergeDoc(merged, doc, layer, origins, &problems, "")
		}
	}
	read(LayerUser, "config.json")
	if inst.Named() {
		read(LayerInstance, "instances", inst.ID, "config.json")
	}
	mergeDoc(merged, instanceDocs["config"], LayerInstance, origins, &problems, "")
	husk, _ := merged["husk"].(map[string]any)
	value, ok := husk["bundle"]
	if !ok {
		return Bootstrap{}, nil
	}
	layer := origins["husk.bundle"]
	fail := func(message string) (Bootstrap, error) {
		return Bootstrap{}, &BootstrapError{Code: "bundle_path_relative", Message: message}
	}
	path, ok := value.(string)
	if !ok {
		return fail(fmt.Sprintf("husk.bundle in the %s layer must be a string", layer))
	}
	if path == "" {
		return Bootstrap{}, nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := env.HomeDir()
		if err != nil {
			return Bootstrap{}, err
		}
		path = filepath.Join(home, path[2:])
	}
	if !filepath.IsAbs(path) {
		return fail(fmt.Sprintf("husk.bundle in the %s layer must be an absolute path: %q", layer, path))
	}
	return Bootstrap{Bundle: path, BundleLayer: layer}, nil
}
