# Terminal plugin

A small plugin that hosts an arbitrary terminal program in `main-stage`.
The plugin owns the process and PTY; Husk owns the terminal emulator and cell
rendering. Linux and macOS are supported; Windows ConPTY is not implemented.

Build the executable beside its manifest:

```sh
go build -o plugins/terminal/terminal ./plugins/terminal
husk --plugin ./plugins/terminal
```

The default program is `$SHELL` (or `/bin/sh` when unset). To run another program,
change the manifest command, preserving argv boundaries:

```json
"command": ["./terminal", "--", "sh", "-l"]
```

The child receives `TERM=xterm-256color`, `COLORTERM=truecolor`,
`TERM_PROGRAM=husk` and the inherited `HUSK_SOCKET_PATH`, but never the plugin's
`HUSK_PLUGIN_TOKEN`. PTY output is sent in bounded base64 chunks. Resizes update
the PTY's winsize and signal the foreground program with `SIGWINCH` so it can
redraw. Previously printed lines are not reflowed by the host emulator.

Full capture sends ordinary keys, Escape and Ctrl+C to the program. Press
Ctrl+] and then one host shortcut to temporarily release capture; the release
expires after 1500 ms. To send Ctrl+] itself, press it twice.

Child exit detaches and closes the surface. Plugin shutdown hangs up the whole
child process group. The reusable plugin-side pump lives in `pkg/ptyhost`;
core host packages never allocate a PTY or launch the child program.
