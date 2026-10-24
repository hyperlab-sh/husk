package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/hyperlab-sh/husk/adapters/herdr/manifest"
	"github.com/hyperlab-sh/husk/adapters/herdr/registry"
)

type cliRunner func([]string) ([]byte, []byte, int)

// huskCLI always acts as the user, preserving targeting but never credentials.
func (env environment) huskCLI(args []string) ([]byte, []byte, int) {
	if env.runCLI != nil {
		return env.runCLI(args)
	}
	binary := os.Getenv("HUSK_BIN_PATH")
	if binary == "" {
		binary = "husk"
	}
	cmd := exec.Command(binary, args...)
	for _, v := range os.Environ() {
		k, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(k, "HUSK_") || k == "HUSK_SOCKET_PATH" || k == "HUSK_INSTANCE" || k == "HUSK_BIN_PATH" {
			cmd.Env = append(cmd.Env, v)
		}
	}
	var out, diagnostics bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &diagnostics
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = 7
			fmt.Fprintln(&diagnostics, err)
		}
	}
	return out.Bytes(), diagnostics.Bytes(), code
}

func instanceArgs(role string, args []string, diagnostics io.Writer) ([]string, bool) {
	fs := flag.NewFlagSet(role, flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	name := fs.String("instance", "", "Husk instance name")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return nil, false
	}
	var target []string
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "instance" {
			target = []string{"--instance", *name}
		}
	})
	return target, true
}

type instancePlugin struct {
	ID        string `json:"id"`
	LastError string `json:"last_error"`
}

func (env environment) instancePlugins(target []string) (map[string]instancePlugin, string, int) {
	raw, diagnostic, code := env.huskCLI(append([]string{"plugin", "list", "--format", "json"}, target...))
	if code != 0 {
		return nil, strings.TrimSpace(string(diagnostic)), code
	}
	var plugins []instancePlugin
	if err := json.Unmarshal(raw, &plugins); err != nil || plugins == nil {
		return nil, "unreadable plugin.list reply", 6
	}
	present := map[string]instancePlugin{}
	for _, p := range plugins {
		present[p.ID] = p
	}
	return present, "", 0
}

func syncRegistry(args []string, out, diagnostics io.Writer, env environment) int {
	target, ok := instanceArgs("sync", args, diagnostics)
	if !ok {
		return 2
	}
	records, problems := registry.Read(env.registryPath)
	dirs, discovered := registry.Discover(records, env.platform)
	problems = append(problems, discovered...)
	if manifest.WriteDiagnostics(diagnostics, problems, nil) != nil {
		return 1
	}
	present, problem, code := env.instancePlugins(target)
	if code != 0 {
		fmt.Fprintln(diagnostics, problem)
		return code
	}
	result := 0
	for _, dir := range dirs {
		var converted, conversionProblems bytes.Buffer
		if execute([]string{"convert", dir}, &converted, &conversionProblems, env) != 0 {
			fmt.Fprintf(out, "refused %s: %s\n", dir, oneLine(conversionProblems.String()))
			result = max(result, 1)
			continue
		}
		var doc struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(converted.Bytes(), &doc) != nil || doc.ID == "" {
			fmt.Fprintf(out, "refused %s: unreadable conversion\n", dir)
			result = max(result, 1)
			continue
		}
		if _, found := present[doc.ID]; found {
			fmt.Fprintf(out, "present %s\n", doc.ID)
			continue
		}
		raw, diagnostic, exit := env.huskCLI(append([]string{"plugin", "add", dir, "--format", "json"}, target...))
		var reply struct {
			Added []struct{ Plugin, Status string }
		}
		if json.Unmarshal(raw, &reply) == nil {
			for _, added := range reply.Added {
				if added.Plugin == doc.ID && added.Status == "live" && exit == 0 {
					fmt.Fprintf(out, "added %s\n", doc.ID)
					present[doc.ID] = instancePlugin{ID: doc.ID}
					goto next
				}
			}
		}
		problem = oneLine(string(diagnostic))
		if list, _, c := env.instancePlugins(target); c == 0 && list[doc.ID].LastError != "" {
			problem += ": " + list[doc.ID].LastError
		}
		if problem == "" {
			problem = "plugin did not become live: " + oneLine(string(raw))
		}
		fmt.Fprintf(out, "refused %s: %s\n", doc.ID, problem)
		result = max(result, 1, exit)
	next:
	}
	return result
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
