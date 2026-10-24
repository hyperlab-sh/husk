# demo

Build and run from this directory with `go build && ./demo` (run `go mod tidy` first to record dependency checksums). Keep the bundle/ directory beside the executable: it contains ordinary plugins under bundle/plugins/ and distribution config in bundle/config.json and bundle/keymap.json. Press f2 for the guide.

The JSON defaults are embedded in the executable, so they remain available without the directory. When bundle/ exists beside the executable it is loaded automatically, regardless of the working directory. Use `--bundle <dir>` to select another bundle, or `--bundle ''` for embedded defaults only. User configuration overrides both sources, except husk.bundle: the standalone app always uses its adjacent or explicitly selected bundle, or no disk bundle when neither exists.

bundle/ is exactly the format that plain Husk loads: `husk --bundle ./bundle` runs the same plugins and configuration. Replace bundle/plugins/hello with your own plugin. Add optional profile files under bundle/profiles/ and append bundle/profiles to the embed directive in main.go when the first profile file exists. `--dump-frame` renders a non-interactive frame; `--help` lists startup flags.
