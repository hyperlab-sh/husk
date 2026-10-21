package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hyperlab-sh/husk/pkg/huskipc"
	"github.com/hyperlab-sh/husk/pkg/manifest"
)

// stringFlag is a repeatable string flag.
type stringFlag []string

func (m *stringFlag) String() string { return strings.Join(*m, ",") }

func (m *stringFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

// repeatable registers a repeatable string flag and returns its pointer.
func repeatable(name, usage string) *stringFlag {
	f := &stringFlag{}
	flag.Var(f, name, usage)
	return f
}

func main() {
	dumpFrame := flag.Bool("dump-frame", false, "Render one live frame after plugin sync and exit")
	demoFrame := flag.Bool("demo-frame", false, "Render one frame from a built-in demo plugin that declares a border+label+close frame and exit")
	railMode := flag.String("rail", "", "Activate rail mode on startup: modal | full-canvas | stage")
	holdAlt := flag.Bool("hold-alt", false, "Simulate holding Alt/Option key on startup")
	pluginPaths := repeatable("plugin", "Plugin: executable, plugin directory, or husk-plugin.json path (repeatable)")
	pluginDirs := repeatable("plugin-dir", "Load every immediate subdirectory containing a husk-plugin.json (repeatable)")
	demo := flag.String("demo", "", "In-process frame demo: pubslots (requires --dump-frame)")
	flag.Parse()
	cwd, _ := os.Getwd()
	if *demo != "" {
		if *demo != "pubslots" || !*dumpFrame {
			fmt.Fprintln(os.Stderr, "--demo supports pubslots with --dump-frame")
			os.Exit(2)
		}
		model := newWorkbenchModel("", nil, cwd)
		model.recomputeLayout()
		model.seedPubslotsDemo()
		fmt.Println(model.View().Content)
		return
	}

	listener, sockPath, err := listenSocket()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to listen on IPC socket: %v\n", err)
		os.Exit(1)
	}

	model := newWorkbenchModel(sockPath, listener, cwd)
	if *holdAlt {
		model.altLatched = true
		model.modifiers.Alt = true
	}
	model.startAcceptLoop()
	defer model.cleanup()

	if *demoFrame {
		model.demoFrame = true
		model.setupDemoFrame(*railMode)
		fmt.Println(model.View().Content)
		return
	}

	var manifestPlugins []*manifest.Plugin
	loadPlugin := func(p *manifest.Plugin) {
		manifestPlugins = append(manifestPlugins, p)
	}
	for _, path := range *pluginPaths {
		p, err := manifest.Load(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: %v\n", err)
			os.Exit(1)
		}
		loadPlugin(p)
	}
	for _, dir := range *pluginDirs {
		ps, err := manifest.LoadDir(dir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "husk: --plugin-dir %s: %v\n", dir, err)
			os.Exit(1)
		}
		for _, p := range ps {
			loadPlugin(p)
		}
	}

	// Writing to stderr while the TUI owns the terminal corrupts the screen
	// (and the alternate screen hides it), so in TUI mode host log lines,
	// including launch failures, are held and printed once the program
	// exits. Failures also show as toasts.
	var tuiLog bytes.Buffer
	if !*dumpFrame {
		model.logOut = &tuiLog
	}

	launched := 0
	for _, p := range manifestPlugins {
		if model.launchPlugin(p) == nil {
			launched++
		}
	}

	if *dumpFrame {
		deadline := time.Now().Add(3000 * time.Millisecond)
		mode := *railMode
		railTriggered := mode == ""
		for time.Now().Before(deadline) {
			select {
			case msg := <-model.subChan:
				model.Update(msg)
				if !railTriggered && launched > 0 && len(model.plugins) >= launched {
					railTriggered = true
					// Open the first registered rail item with the requested
					// placement; Husk knows no specific plugin.
					if ri, ok := model.firstRailItemWithPlacement(huskipc.RailPlacement(mode)); ok {
						model.activateRailItem(ri)
					} else {
						fmt.Fprintf(os.Stderr, "husk: --rail %s: no loaded plugin declares a %s rail item\n", mode, mode)
						mode = "" // nothing to wait for
					}
				}
				ready := len(model.plugins) >= launched && len(model.slotUpdates) >= min(launched, 1)
				if mode == "modal" && model.slotUpdates[huskipc.SlotModalSurface] == nil {
					ready = false
				}
				if mode == "full-canvas" && model.slotUpdates[huskipc.SlotFullCanvas] == nil {
					ready = false
				}
				if mode == "stage" && model.slotUpdates[huskipc.SlotWorkspaceStage] == nil {
					ready = false
				}
				if ready {
					time.Sleep(150 * time.Millisecond)
					for len(model.subChan) > 0 {
						model.Update(<-model.subChan)
					}
					fmt.Println(model.View().Content)
					return
				}
			case <-time.After(50 * time.Millisecond):
			}
		}
		fmt.Println(model.View().Content)
		return
	}

	p := tea.NewProgram(model)
	_, err = p.Run()
	model.cleanup() // stop plugins before printing, so their exit lines are included
	_, _ = os.Stderr.Write(tuiLog.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "husk error: %v\n", err)
		os.Exit(1)
	}
}
