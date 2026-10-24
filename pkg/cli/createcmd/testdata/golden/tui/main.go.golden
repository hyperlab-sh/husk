package main

import (
	"bytes"
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyperlab-sh/husk/pkg/cli"
	"github.com/hyperlab-sh/husk/pkg/cli/createcmd"
	"github.com/hyperlab-sh/husk/pkg/host"
	"github.com/hyperlab-sh/husk/pkg/host/contracts"
	"github.com/hyperlab-sh/husk/pkg/workbench"
)

// Append bundle/profiles to the directive once the bundle has a profile file.
//
//go:embed bundle/*.json
var defaults embed.FS

func main() {
	// Plugins use this same executable for socket calls and the guide runner.
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		cli.Register("__guide-plugin", createcmd.GuideCommand())
		os.Exit(cli.Main(context.Background(), os.Args[1:], cli.OSEnv()))
	}
	bundle := ""
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "bundle")
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			bundle = candidate
		}
	}
	flag.StringVar(&bundle, "bundle", bundle, "Load a bundle directory; empty uses embedded defaults only")
	dump := flag.Bool("dump-frame", false, "Render one settled frame and exit")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional argument")
		os.Exit(2)
	}
	if bundle != "" {
		var err error
		bundle, err = filepath.Abs(bundle)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	distribution, err := fs.Sub(defaults, "bundle")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var log bytes.Buffer
	opt := workbench.Options{Log: &log, NewHost: func(cfg contracts.Config, opts ...host.Option) (host.Looped, error) {
		cfg.Distribution = distribution
		cfg.InstanceDocs = map[string]map[string]any{"config": {"husk": map[string]any{"bundle": bundle}}}
		return host.NewInstance(cfg, append([]host.Option{host.WithVersion("v1.2.3")}, opts...)...)
	}}
	if *dump {
		var frame string
		frame, err = workbench.DumpFrame(context.Background(), opt)
		fmt.Fprintln(os.Stdout, frame)
	} else {
		err = workbench.Run(context.Background(), opt)
	}
	_, _ = os.Stderr.Write(log.Bytes())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
