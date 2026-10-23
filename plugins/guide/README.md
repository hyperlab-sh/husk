# Layout guide

An optional developer plugin that annotates the current layout with numbered
surface boxes, occupants, effective keys and a bottom-right legend. It claims
only `app-overlay` and never takes focus or handles mouse input.

From the repository root:

```sh
go build -o plugins/guide/guide ./plugins/guide/cmd/guide
husk --plugin ./plugins/guide
```

Toggle **Toggle layout guide** in the command palette, or from another shell:

```sh
husk action invoke guide.toggle
husk state get guide.state
```

For a keyboard shortcut, add a binding in a keymap layer (not the manifest):

```json
{"version": 1, "bind": [{"key": "f2", "action": "guide.toggle", "hint": "guide"}]}
```

The guide watches geometry, surface resolution, focus and plugin changes over
one persistent connection. It reconnects and replaces its baseline after a
socket drop. State notifications from one read are coalesced best effort, not
atomically across a host tick. Keys refresh on toggle-on and plugin changes;
a keymap-only reload appears on the next toggle-on. A failed key listing shows
`?` without hiding the layout information.

`guide.state` is non-persistent. Toggle-off replaces the overlay with an empty
frame and keeps the State watch current. `Run` is the reusable plugin entry
point; `Files` provides source and the manifest for scaffolds, without a built
executable.
