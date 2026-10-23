---
type: Design Spec
title: "Bundles and husk create"
description: "A bundle is a directory with a small manifest, one folder per plugin and optional config files; how husk --bundle loads it as plugins plus the distribution layer, and what husk create tui|bundle writes."
status: planned
generated: { by: claude/fable-5-1, at: 2026-10-23T12:00:00Z }
tags: [architecture-v2, pods, substrate]
---

# Bundles and `husk create`

## Level 0: What and why

Husk is a blank canvas: it draws nothing of its own and loads no plugin by default. Turning the canvas into a working application today takes several steps that nothing ties together: a list of plugin directories in `husk.plugins.dirs` or on the command line, a `config.json` with the plugins' settings and a theme, a `keymap.json`, and a profile directory or two. A **bundle** is those pieces in one directory, named, so that one flag or one setting turns a bare `husk` into an application.

The format is deliberately the smallest thing that does this: a directory, a manifest of four fields, one sub-folder per plugin, and optional config files that are the same files a user would put in `~/.config/husk`. It is a plugin directory with a manifest around it, not a package format. There is no registry, no lockfile, no dependency resolution, no versioning scheme beyond the string the manifest states, and no network: a bundle is files on disk that the user or an application put there.

Two things make a bundle more than "a plugin directory plus a config directory":

1. **One name for one set.** `husk --bundle ~/apps/notes` is the whole configuration of an application. A plugin that ships inside a bundle needs nothing special; it is an ordinary plugin directory, and the folder that holds it is exactly what `husk.plugins.dirs` already loads.
2. **The bundle's config files are the distribution layer.** [Store](store.md) merges `distribution < user < profile < instance`. A bundle's `config.json`, `keymap.json` and `profiles/` are mounted as that bottom layer, so a bundle ships defaults and the user's own files still win, exactly as they win over an application's compiled-in defaults.

Two scaffolding commands write the layout so nobody types it by hand: `husk create bundle <name>` writes a bundle with one example plugin and the guide plugin, and `husk create tui <name>` writes a Go module that embeds Husk and carries a bundle of its own.

What this spec is NOT:

- Not a plugin format. The plugin inside a bundle is specified by [Pods](pods.md) ("Pod and manifest conversion"); this spec adds no field to `husk-plugin.json` and no conversion rule.
- Not a layer. The bundle's config files are read as the existing distribution layer; no fifth layer appears in Store, in [Keybindings](keybindings.md) or in `Problem.Layer`.
- Not an installer. Loading a bundle writes no file under the user's directories. Removing a bundle is deleting its directory or unsetting the flag.
- Not live. A bundle loads at start. There is no `bundle.add`; `husk plugin add` ([Pods](pods.md), "Adding a plugin") stays per plugin and may load a bundle's `plugins/` folder like any directory.

Who depends on it: the composition root (`internal/host.New`, which reads the bundle before Store opens), [Store](store.md) (the bundle's config files as the second part of the distribution layer), [Pods](pods.md) (the bundle's `plugins/` as one more plugin directory in the startup batch), [Services](services.md) (the `husk.bundle` setting) and [Instances](instances.md) (the `--bundle` start flag and the `husk create` commands). The bundle package itself depends on the standard library only.

## Level 1: Contract

### Terms

| Term | Meaning |
|---|---|
| **bundle** | A directory that holds a `husk-bundle.json` manifest. |
| **manifest** | That file: `id`, `name`, `description`, `version`, plus `manifest_version`. |
| **plugin folder** | An immediate entry of `<bundle>/plugins/` that is a directory or a symlink to one. Each is an ordinary plugin directory with its own `husk-plugin.json`. |
| **bundle config** | The files of the bundle other than the manifest, `plugins/` and `README.md`, read as a config layer: `<doc>.json` and `profiles/<profile>/<doc>.json`. |
| **bundle layer part** | The bundle config mounted as the second part of Store's distribution layer, above an embedding application's own distribution files and below the user's layers. |
| **bootstrap read** | The read of `husk.bundle` that the composition root performs before Store opens, from the layers that can name a bundle. |

### Layout

```text
<bundle>/
  husk-bundle.json              the manifest (required)
  plugins/                      plugin folders (optional, one per plugin)
    <plugin-id>/
      husk-plugin.json          an ordinary plugin manifest
      ...                       the plugin's own files
  config.json                   bundle config (optional): the same shape as a user config.json
  keymap.json                   bundle config (optional): the same shape as a user keymap.json
  profiles/<profile>/<doc>.json bundle config (optional): profile layers the user can select
  README.md                     ignored by Husk
```

Rules:

1. A plugin folder is an ordinary plugin directory: whatever `husk.plugins.dirs` and `--plugin-dir` load, through the same converter registry (`Registry.LoadDir`, [Pods](pods.md)). Nothing in a plugin's manifest says or needs to say that it is inside a bundle. Executable converters registered with the instance claim folders in a bundle exactly as they claim folders elsewhere.
2. The folder name is conventionally the plugin's id, as `husk create` writes it; the manifest's `id` is authoritative and the folder name is not checked. A folder with no manifest any converter claims is skipped silently, as under `--plugin-dir`.
3. A plugin folder may be a symlink to a directory anywhere on the machine. `LoadDir` follows it (a symlink to a file, or a dangling one, is skipped). The plugin's own files are resolved from the target directory, with the manifest's existing rule that the manifest must not escape its plugin directory.
4. Bundle config files are HuJSON with the file names and shapes of the user layer ([Store](store.md), "Directory tree": `<doc>.json`, `profiles/<profile>/<doc>.json`). The document names in v1 are `config`, `keymap`, `themes` ([Services](services.md), "Named themes") and `catalog` ([Settings center](settings-center.md), "The catalog document"). A `keymap.json` in a bundle is a distribution-layer keymap, so it may say `"builtin": false` ([Keybindings](keybindings.md)).
5. Everything else in the directory is ignored. The manifest, `plugins/` and `README.md` are hidden from Store so they can never be mistaken for a document.

The layered `catalog` reader and its off-loop host cache are landed; a bundle's `catalog.json` uses the ordinary distribution-layer document mechanism. Catalog validation never installs anything or changes a reload report.

### Manifest

`husk-bundle.json` is plain JSON (not HuJSON: it is data for Husk, like `husk-plugin.json`), one object, at most 64 KiB.

```json
{
  "manifest_version": 1,
  "id": "notes",
  "name": "Notes",
  "description": "A notebook: an editor, a tree and a search box.",
  "version": "0.3.0"
}
```

| Field | Type | Required | Rule |
|---|---|---|---|
| `manifest_version` | integer | yes | Must be `1`. Any other value is `bundle_manifest_version`. |
| `id` | string | yes | Matches `[A-Za-z0-9][A-Za-z0-9._-]{0,63}`, the plugin id rule ([Pods](pods.md) rule 3). Anything else is `bundle_id_invalid`. |
| `name` | string | no | Display name, at most 128 runes. Defaults to `id`. |
| `description` | string | no | One or two sentences, at most 1 KiB. |
| `version` | string | no | Informational, at most 64 runes. Husk compares nothing against it. |

Unknown fields are ignored, so a later `manifest_version` 1 bundle may carry more without failing an older Husk. A field present with the wrong type is `bundle_manifest_invalid`, as is a file that is not a JSON object or is over the size limit.

The manifest states identity and nothing else. In particular it does **not** list the plugins (the `plugins/` folder is the list), does not name a Husk version it requires (a plugin inside it states its protocol major, which is the only compatibility check Husk has), and carries no install, update or dependency information.

### Loading

A bundle is named by the start flag `--bundle <dir>` or by the setting `husk.bundle`. One bundle per instance in v1. Both name a directory; the flag's value is made absolute against the process working directory, a setting's value must already be absolute (`~/` expands to the home directory) and anything else is `bundle_path_relative`.

**The setting.** `husk.bundle` is a built-in host setting ([Services](services.md), "Schema"): type `string`, default `""` (no bundle), `apply: restart`, not `public`. It joins `husk.plugins.paths` and `husk.plugins.dirs` as the settings that are read once at start and `restart_required` afterwards.

**The bootstrap read.** The bundle supplies part of the distribution layer, so it must be known before Store can resolve any document, and the setting that names it therefore cannot be read through the normal resolution. The composition root reads it with `store.ReadBootstrap` ([Store](store.md), "Layers"), which reads only the layers that can be known before Store opens: the user layer's `config.json`, then the named instance's `instances/<name>/config.json`, then the instance-layer documents built from flags (`--bundle` folds into `husk.bundle` there, like every other start flag). Highest wins, with the layer merge rules. The `distribution` layer cannot name a bundle (circular) and the `profile` layer cannot either (the profile is selected inside the first reload, after Store has opened). A `husk.bundle` present in a profile file or in a bundle's own `config.json` is reported by Settings as a `warning` problem `bundle_setting_ignored` (message `husk.bundle is honoured from the user and instance layers only`) and ignored; this mirrors `profile.default`. A file the bootstrap read cannot parse is treated as absent by that read alone; the same file is reported by the first reload with its parse problem as for any bad layer, so the problem is not lost.

**What loading does.** Given the directory, before Store opens:

1. The bundle package validates the directory and the manifest (the error codes below). Any error here stops the start: `husk: bundle <dir>: <message>` on stderr, exit 1, before the TUI starts and before any file under the user's directories is created or written. Rationale: the user wrote a configuration Husk cannot honour; starting without it would silently run a different application than the one asked for.
2. The bundle's config is handed to Store as `Options.Bundle`, an `fs.FS` rooted at the bundle directory that hides the manifest, `plugins/` and `README.md`. Store reads it as the second part of the distribution layer: for every document, the embedding application's `Options.Distribution` is merged first, then the bundle's file, with the ordinary merge rules ([Store](store.md), "Layers"). Both are layer `distribution` in `Resolved.Origins`, in `Problem.Layer` and in `keys.list`; `Store.Profiles` lists profile directories from both sources as `distribution`. So **the bundle wins over the embedded defaults and loses to the user**: an application's compiled-in `dist.*` and theme defaults stay unless the bundle sets the same key, and a user's `~/.config/husk/config.json` overrides both.
3. `<bundle>/plugins/`, when it exists, is appended to the plugin directories of the startup batch, **after** every `--plugin`, `--plugin-dir`, `husk.plugins.paths` and `husk.plugins.dirs` entry and before executable-converter discovery ([Pods](pods.md), "Executable converters"). Duplicate ids are settled by the existing first-loaded-wins rule, so a plugin the user named explicitly shadows the bundle's plugin of the same id: the user's configuration wins for plugins as it does for config. An absent or empty `plugins/` is a warning in the log (`bundle_no_plugins`), not an error; dotfiles do not count as plugin entries. A `plugins` entry that exists but is not a directory is `bundle_plugins_not_a_directory`, fatal.
4. Each plugin folder is converted and launched like any other plugin-directory entry. A folder that fails conversion is refused like any other plugin-dir entry: a log line and one warning toast naming the manifest and the problem, listed in `Frontend.Refusals`, with no Pod ([Pods](pods.md), "Failure modes", "Manifest invalid"). It never stops the start: the bundle is honoured; one of its plugins is not.

After start, a bundle's plugins are ordinary Pods: `husk plugin list` shows them, `husk plugin remove` unloads one, and `husk plugin add --dir <bundle>/plugins` loads them again. The bundle's config files are an ordinary layer: `husk config reload` re-reads them, so a live key changed in a bundle's `config.json` takes effect on reload; its `plugins/` is read only at start. Nothing records which Pods came from the bundle; the open question below records the one piece of introspection this spec leaves out.

### Error codes

Codes follow the `snake_case` vocabulary of the other specs and are what tests match on. Fatal codes stop the start as described above; warnings go to the log.

| Code | Severity | Condition |
|---|---|---|
| `bundle_not_found` | fatal | `<dir>` does not exist or is not a directory (a symlink to a directory is followed). |
| `bundle_unreadable` | fatal | `<dir>` or the manifest cannot be read (permissions, I/O). |
| `bundle_manifest_missing` | fatal | No `husk-bundle.json` in `<dir>`. A directory of plugins is not a bundle; pass it as `--plugin-dir`. |
| `bundle_manifest_invalid` | fatal | The manifest is not a JSON object, is over 64 KiB, or a known field has the wrong type or exceeds its limit. The message names the field. |
| `bundle_manifest_version` | fatal | `manifest_version` absent or not `1`. The message names the value found. |
| `bundle_id_invalid` | fatal | `id` absent, empty or outside the id rule. |
| `bundle_path_relative` | fatal | `husk.bundle` set in a layer file to a path that is neither absolute nor `~/`-prefixed, or set in the user or instance layer to a value that is not a string. The message names the layer. |
| `bundle_plugins_not_a_directory` | fatal | `<dir>/plugins` exists and is not a directory. |
| `bundle_no_plugins` | warning | `<dir>/plugins` is absent or empty. |
| `bundle_setting_ignored` | warning (Settings) | `husk.bundle` appears in a layer that cannot set it (`profile`, or the bundle's own config in `distribution`). |

The fatal codes are printed as `husk: bundle <dir>: <message>` on stderr; the exit status is 1 (a start failure, as for an invalid instance name after flag parsing). `husk store check` does not inspect bundles: a bundle is the user's or the application's, not a file Husk owns.

### `husk create`

`husk create` is a local command, like `husk store`: it talks to no instance, rejects `--instance`, `--socket` and `--timeout` with exit 2, and accepts `--format` ([Instances](instances.md), "Global flags"). It writes files only inside the directory it creates.

| Synopsis | Writes | Status |
|---|---|---|
| `husk create bundle <name>` | `<name>/`, the bundle layout with one example plugin, the guide plugin and a README | lands with this spec |
| `husk create tui <name> [<module>]` | `<name>/`, a Go module that embeds Husk, with a `bundle/` in the same layout | specified here; implementation waits on the public-package move (everything under `internal/` moving to `pkg/`), because the generated `main.go` imports public packages that do not exist yet. The verb is not registered until that move lands. |

Shared rules:

1. `<name>` matches `[a-z0-9][a-z0-9._-]{0,63}`: a valid bundle id, lowercase so the directory name is the same on every filesystem. Anything else is exit 2 (`usage`) before the filesystem is touched. It is both the directory name (relative to the working directory) and the bundle `id`.
2. The target directory must not exist, or must exist and be empty. Any other state (a non-empty directory, a file, a symlink, an unreadable path) is exit 1 with error code `create` and the message `husk: <dir> exists and is not empty` (or the OS error). Nothing is written in that case. There is no `--force`; the user deletes the directory.
3. Files are written with mode 0644, executables 0755, directories 0755, in one pass; a write failure stops the command with exit 1 `create` and leaves what was written (the directory is the user's to delete; the message names the file that failed). Nothing is written outside `<dir>`.
4. Output on stdout: in `json` format, `{"dir": "<absolute dir>", "files": ["<relative path>", ...]}`; in `table` format, one relative path per line. Exit 0.
5. `husk create` reads no configuration and no instance state; the running binary's version (`host_version`) is the one input it takes from the build.

**`husk create bundle <name>`** writes:

| File | Content |
|---|---|
| `husk-bundle.json` | `{"manifest_version": 1, "id": "<name>", "name": "<name>", "description": "", "version": "0.1.0"}`, indented. |
| `config.json` | A HuJSON file whose every entry is a comment: the layer order, where the user's files live, and one commented-out `plugins.<id>.<key>` example setting. It must parse to `{}`. |
| `keymap.json` | `{"version": 1, "bind": [{"key": "f2", "action": "guide.toggle"}]}`, with a comment showing the `builtin` switch. |
| `README.md` | The layout above in three paragraphs, how to run it (`husk --bundle .`) and how a plugin folder may be a symlink. |
| `plugins/hello/husk-plugin.json` | The example plugin's manifest (below). |
| `plugins/hello/hello` | The example plugin's executable (below), mode 0755. |
| `plugins/guide/...` | The guide plugin (specified separately), copied from the `fs.FS` the package `plugins/guide` exports from its `embed.go`. `guide-plugin.md` owns the plugin's contents; that package only embeds them. |

The example plugin is deliberately the smallest valid v2 plugin: a POSIX shell script, no build step, no language runtime beyond `/bin/sh`, so the bundle runs the moment it is written. Its manifest:

```json
{
  "manifest_version": 2,
  "protocol": 2,
  "id": "hello",
  "name": "Hello",
  "version": "0.1.0",
  "description": "The example plugin husk create wrote. Replace it with your own.",
  "command": ["./hello"],
  "surfaces": [
    {"id": "main-stage", "frame": {"border": true, "label": "Hello", "close": false}}
  ]
}
```

Its executable:

```sh
#!/bin/sh
# The example plugin "husk create" wrote. It draws two lines into the surface
# it occupies and then waits for Husk to close its stdin.
"$HUSK_BIN_PATH" rpc surface.update '{"surface":"main-stage","lines":["Hello from <name>","","Edit plugins/hello to make this plugin yours."]}' >/dev/null
exec cat >/dev/null
```

What it exercises, on purpose: `HUSK_BIN_PATH` ([Instances](instances.md), "Plugin and shell environment") so the plugin calls the CLI of the same build; `surface.update` through `husk rpc`, attributed to the plugin by its token; and the stdio contract, since `cat` drains stdin until Husk closes it at shutdown and the plugin exits 0 (`exited`, no toast). It declares no actions and no settings, so Husk never waits on a reply from it and the bundle's `config.json` can start empty. The `protocol` field is the running build's major, written by `husk create`, not a constant in a template.

**`husk create tui <name> [<module>]`** writes everything `create bundle` writes, under `bundle/`, plus:

| File | Content |
|---|---|
| `go.mod` | `module <module>` (the optional second argument, default `<name>`), the Go version of the running build, and `require github.com/hyperlab-sh/husk <host_version>`. When the build carries no version (`host_version` is `""`), the `require` line is omitted and the README says to run `go get github.com/hyperlab-sh/husk@latest`. |
| `main.go` | Embeds Husk as a library. It declares `//go:embed bundle/*.json bundle/profiles` as the application's distribution layer, so the binary carries its defaults even when no bundle directory is beside it; takes `--bundle <dir>` with the default `bundle/` next to the executable when that directory exists (`os.Executable`), so the plugins of the scaffolded bundle load without flags from a build directory; and runs the Bubble Tea program. The identifiers it imports are the ones the public-package move exports; this spec fixes the behaviour, and a test pins the generated file against a golden once the move lands. |
| `README.md` | How to build and run (`go build && ./<name>`), where the plugins and config live, and that `bundle/` is the format `husk --bundle` loads, so the same directory runs under a plain `husk` too. |

The scaffolded application therefore has exactly two configuration inputs, both already specified: an embedded distribution layer and a bundle. Its `bundle/` is byte-for-byte what `husk create bundle` writes, so a developer can move between "my own binary" and "plain husk with a bundle" without changing a file. `husk create tui` adds no settings schema (`dist.*`) and no host action; those are the application's to add in `main.go`.

### Invariants

1. A bundle is loaded at start or not at all. No method, flag or setting changes the loaded bundle while an instance runs; `husk.bundle` is `restart_required` like `husk.plugins.*`.
2. Loading a bundle writes nothing under `$XDG_CONFIG_HOME`, `$XDG_STATE_HOME` or `$XDG_DATA_HOME` and nothing inside the bundle. A bundle directory may be read-only.
3. A plugin inside a bundle is converted, launched, listed, removed and re-added exactly as a plugin outside one. No code path branches on "came from a bundle".
4. The bundle's config is layer `distribution` everywhere it is observable, and it is merged above the embedding application's distribution files and below `user`. The layer order of [Store](store.md) does not change.
5. A bad bundle (any fatal code) stops the start before Store opens; a bad plugin inside a good bundle refuses that plugin only.
6. One bundle per instance. A second `--bundle` flag replaces the first (last wins, as for every flag); the setting and the flag resolve to one value by layer order.
7. `husk create` writes only inside the directory it creates and never overwrites a non-empty one.

### Failure modes

| Condition | Who detects | What happens | What the user sees |
|---|---|---|---|
| `--bundle` names a missing or non-directory path | bundle package, at start | Start stops before Store opens | `husk: bundle <dir>: not found`, exit 1 |
| Directory has no `husk-bundle.json` | bundle package | Start stops | `husk: bundle <dir>: no husk-bundle.json; a directory of plugins is loaded with --plugin-dir`, exit 1 |
| Manifest is not JSON, over 64 KiB, wrong field type | bundle package | Start stops | `husk: bundle <dir>: husk-bundle.json: <field>: <problem>`, exit 1 |
| `manifest_version` is not 1 | bundle package | Start stops | `husk: bundle <dir>: manifest_version 2 is not supported by this husk (want 1)`, exit 1 |
| `id` invalid | bundle package | Start stops | `husk: bundle <dir>: id "My Bundle" is invalid: use [A-Za-z0-9][A-Za-z0-9._-]{0,63}`, exit 1 |
| `husk.bundle` set to a relative path in a layer file | bootstrap read | Start stops | `husk: bundle: husk.bundle in the user layer must be an absolute path: "./notes"`, exit 1 |
| `husk.bundle` set to a non-string value in the user or instance layer | bootstrap read | Start stops | `husk: bundle: husk.bundle in the user layer must be a string`, exit 1 |
| `husk.bundle` set in a profile or bundle config | Settings, first reload | Ignored | `warning` `bundle_setting_ignored` in the reload report; startup toast only if other errors exist |
| `plugins/` absent or empty | bundle package | Bundle loads with its config only | Log line `bundle <id>: no plugins` |
| `plugins` is a file | bundle package | Start stops | `husk: bundle <dir>: plugins is not a directory`, exit 1 |
| A plugin folder fails conversion | converter | That plugin is refused; the bundle and the other plugins load | Warning toast and log line naming the manifest, as for any plugin-dir entry |
| A plugin folder is a symlink to a file or dangling | `LoadDir` | Skipped silently, as any non-directory entry | Nothing (log at debug level) |
| A bundle plugin's id equals one the user named with `--plugin` or `husk.plugins.*` | Host, duplicate rule | The user's plugin was loaded first and wins; the bundle's is refused `duplicate plugin id` | Warning toast `<id>: duplicate plugin id`, as today |
| Bundle `config.json` fails to parse | Store, at resolve | The distribution layer is skipped for that document, as for any bad layer file; the start proceeds | Reload problem naming the file; startup toast |
| Bundle config over 1 MiB or depth over 64 | Store | Same as a parse failure | Same |
| `husk create` with an invalid name | create command | Nothing written | `husk: invalid name "My App": use [a-z0-9][a-z0-9._-]{0,63}`, exit 2 |
| `husk create` target exists and is not empty | create command | Nothing written | `husk: <dir> exists and is not empty`, exit 1, code `create` |
| `husk create` write fails midway | create command | Stops at the failed file; written files stay | `husk: write <file>: <err>`, exit 1, code `create` |
| `husk create tui` before the public-package move | CLI | Verb unknown | Usage on stderr, exit 2 |

### Limits

| Limit | Value | Reason |
|---|---|---|
| Manifest file | 64 KiB | Four short fields; larger is a mistake |
| `id` | 64 characters, plugin id rule | One rule for ids in Husk |
| `name` / `version` / `description` | 128 runes / 64 runes / 1 KiB | Fits a list row and a tooltip |
| Bundles per instance | 1 | v1; an application composes one bundle |
| Plugin folders per bundle | no cap of its own | The same as a `--plugin-dir` directory |
| Bundle config files | Store's limits (1 MiB per file, depth 64) | They are layer files |

### Exposed to the CLI

| Command or flag | Local | Spec |
|---|---|---|
| `husk --bundle <dir>` | start mode | this spec; the flag folds into `husk.bundle` in the instance layer |
| `husk settings get husk.bundle` | no | [Services](services.md): the value in force, and `restart_required` when a layer now says otherwise |
| `husk create bundle <name>` | yes | this spec |
| `husk create tui <name>` | yes | this spec, after the public-package move |

### Dependency directions

- The bundle package (`internal/bundle` today; public with the move) imports the standard library only. It exports `Load(dir string) (*Bundle, []Warning, error)`, with `Bundle{Dir string; Manifest Manifest; PluginsDir string; Config fs.FS}`, and `Manifest{ID, Name, Description, Version string}`. Errors carry one of the codes above; the message is for people.
- The composition root (`internal/host.New`, which opens Store today) calls `store.ReadBootstrap`, then `bundle.Load`, then `store.Open` with `Options.Bundle`, as its first stages, and appends `Bundle.PluginsDir` to the plugin directories it hands the converter registry. `cmd/husk` only folds `--bundle` into the instance-layer documents it passes through `contracts.Config`. Nothing else imports the bundle package; Pods and Store never do.
- The `create` noun lives in its own command package (`internal/cli/createcmd`), which imports the output package, `pkg/protocol` (for the running major), the guide package `plugins/guide` and the standard library only (its templates through `embed`), never `internal/cli`, Store or the bundle package: it writes a layout, it does not load one. It exports `Command(version string) output.Command`, the version being the build's `host_version`; `cmd/husk` wires it with `cli.Register("create", createcmd.Command(buildVersion()))` beside the `store` registration ([Instances](instances.md), "Dependency directions"). A test pins its import list.

## Level 2: Migration from today

- **Landed — `cmd/husk/main.go`:** `bundleFlag` normalizes the start flag `--bundle <dir>`; `pluginInstanceDocs` folds it into `husk.bundle` alongside plugin flags. Repeated values keep the last, and an explicit empty value disables a configured bundle. There is no `--distribution` flag.
- **Landed — `internal/host`:** `contracts.Config.InstanceDocs` carries flag documents. `Root.buildBundle` calls `store.ReadBootstrap` and `bundle.Load` as the first stage, before `Root.buildStore`; typed startup errors retain the bundle diagnostic without a host-stage prefix. `Root.loadSpecs` appends the bundle plugin directory after explicit sources. Runtime lookup is non-creating before host construction; creation follows bundle validation.
- **Landed — `internal/substrate/store`:** `Options.Bundle fs.FS`, two-part distribution merging in `Store.loadLayers`, profile discovery in `Store.ProfileSources`, and non-creating `ReadBootstrap` sharing pure XDG resolution with `Open`.
- **Landed — `internal/pods/manifest`:** `Loader.LoadDir` follows symlinked directories while preserving the existing manifest escape boundary.
- **Landed — `internal/services/settings`:** `Builtins` registers private restart-only `husk.bundle`; `Service.Rebuild` uses only user/instance values and reports `bundle_setting_ignored` for distribution/profile declarations.
- **Landed — `internal/bundle`:** standard-library-only `Load`, `Bundle`, `Manifest`, `Warning` and coded `Error`, with a filtered config filesystem.
- `internal/cli/createcmd`: new command package; `husk create bundle` lands now, `husk create tui` with the public-package move.
- `plugins/`: the example plugin that `husk create` writes is a template inside `createcmd`, not a directory in this repository. New: `plugins/guide/embed.go`, a small package that embeds the guide plugin's files and exports them as an `fs.FS` for `createcmd`; the host still loads no plugin by default.
- **Landed loading tests:** `TestLoadFatalCodesAndMessages`, `TestManifestFieldTypesAndLimits`, `TestManifestSizeBoundaryAndUnknownFields`, `TestBundleDirectorySymlink`, `TestUnreadableDirectoryAndManifest`, `TestPluginWarningsAndConfigVisibility`, `TestImportsStandardLibraryOnly`; `TestReadBootstrapPrecedencePathsAndNoWrites`, `TestBundleDistributionPrecedenceProfilesOriginsAndReload`, `TestBundleTypeClashDoesNotFreezeReloadOrLoseLastGood`; `TestBundleSettingAllowedLayersIgnoredWarningsAndRestart`, `TestBundleSettingInstanceOverridesUserAndNullClears`; `TestLoadDirFollowsSymlinkedPluginAndKeepsEscapeBoundary`; `TestBundleFatalErrorsBeforeAnyDirectoryCreation`, `TestBundlePrecedenceActuallyLaunchesSelectedPlugins`, `TestBundlePluginRefusalAndExplicitPriority`, `TestBundleKeymapReachesKeysList`, `TestBundleWithoutPluginsLogsWarningWithoutRefusal`; `TestBundleFlagInstanceDocument`.
- Remaining scaffold tests: `husk create bundle` output matches a golden tree and the written bundle loads under `--bundle`; `husk create` refuses a non-empty directory and writes nothing; the example plugin's `protocol` equals the running major.

## Decisions made here

1. **A bundle is a directory with a manifest, one folder per plugin and optional layer files; nothing more.** Rejected: an archive format, a manifest that lists its plugins (the folder is the list and cannot disagree with itself), a lockfile, a version constraint on Husk (the plugins' protocol majors are the only compatibility fact Husk checks), and any network fetch.
2. **The manifest has `manifest_version`.** The ruling lists four fields; this spec adds the one integer that lets a later format fail an older Husk with a precise message instead of a guess, the same way `husk-plugin.json` carries `manifest_version: 2`. Unknown fields are ignored for the same forward-growth reason.
3. **The bundle's config is the second part of the distribution layer, not a new layer.** The distribution layer becomes two parts merged in order, embedded files then bundle files, exactly as the instance layer is already two parts (named-instance file, then flags; [Store](store.md) decision 10). The bundle wins over the embedded defaults and loses to the user, as the brief recommended, and no layer enum, rank table or `Problem.Layer` value changes anywhere.
4. **`--bundle <dir>` replaces the planned `--distribution <dir>` flag.** Two flags that both mount a directory as the distribution layer would differ only in whether the directory also carries plugins. The directory form is a bundle; the `fs.FS` form (`Options.Distribution`) stays for applications that embed Husk. [Store](store.md) open question 3 is resolved on these terms.
5. **`husk.bundle` is read by a bootstrap read from the user and instance layers only.** The setting names what the distribution layer is made of, so it cannot come from that layer or from a profile that is selected after Store opens. The read goes through Store (`ReadBootstrap`), so "only Store opens files" holds. Rejected: opening Store twice, or a mutable `Store.SetDistribution`, both of which make Store's state depend on the order of calls.
6. **Bundle plugins load after the user's explicit plugins.** First loaded wins on a duplicate id, so loading the bundle last lets `--plugin` or `husk.plugins.paths` shadow a bundle's plugin; the user's configuration wins for plugins as it does for config keys. The bundle still precedes executable-converter discovery, which already runs after every listed directory.
7. **A bad bundle is fatal; a bad plugin inside it is not.** The bundle is the configuration the user asked for and cannot be partially honoured without running a different application; a plugin inside it is one of many and is refused like any plugin-dir entry.
8. **`plugins/` is optional, with a warning.** A bundle that ships only config is legal (a theme-and-keymap bundle) but unusual, so its absence is logged and nothing else.
9. **A plugin folder may be a symlink**, as ruled. `LoadDir` therefore follows symlinked directories, which it does not today; this is the only behaviour change outside the new code.
10. **The scaffolded example plugin is a shell script.** It needs no toolchain, runs on the first `husk --bundle .`, and exercises `HUSK_BIN_PATH`, `surface.update` and the stdio contract, so a developer sees each seam once. Rejected: a Go example (needs a build before the bundle works) and an empty plugin folder (teaches nothing).
11. **`husk create tui` embeds its bundle's config and loads its plugins from a `bundle/` beside the binary.** Plugins are processes, so they cannot run from an embedded filesystem; the config can be embedded and is, so the binary has its defaults with or without the directory. The `bundle/` is byte-for-byte a `husk create bundle` output.
12. **`husk create` is its own command package, like `store`.** It imports neither the socket client nor Store; it writes files from embedded templates and nothing else, which keeps the import-list tests of [Instances](instances.md) meaningful.
13. **`<name>` is lowercase.** A bundle id may be mixed case, but a scaffolded directory name should spell the same on case-insensitive filesystems, and the id is derived from the name.
14. **No `--force` on `husk create`.** Deleting a directory is one command the user already knows; an overwrite flag that recursively deletes is a footgun the scaffolder does not need.
15. **Nothing records a Pod's origin.** The bundle is a loading convenience; marking Pods "from bundle X" would be the first field in `husk.pods` that is not about the process, and `husk plugin add --dir` of the same folder would then produce unmarked twins. Left to the open question.

## Open questions

1. **Bundle introspection.** Should the loaded bundle's manifest be visible at run time, as a `husk.bundle` core State slice (`{id, name, version, dir}`) or in `session.ping`? This spec exposes only the directory (`husk settings get husk.bundle`). Recommended: add the slice when a plugin or the frontend needs the name, for example a header that shows the application's name; it is a read-only core slice and a protocol minor.
2. **Several bundles.** v1 loads one. Composing several (`--bundle a --bundle b`) would need a merge order for their config files and a duplicate-id rule across bundles. Recommended: wait for a real case; a user who wants two bundles' plugins today passes the second's `plugins/` as `--plugin-dir`.
3. **Resolved: the guide plugin in the scaffolds.** Its files are embedded in the `husk` binary; `husk create bundle|tui` writes them under the scaffold bundle's `plugins/guide/`, and the scaffold's `keymap.json` binds `f2` to `guide.toggle` ("`husk create`" above).
4. **`husk create tui` with a `dist.*` schema.** The scaffold registers no application settings. Recommended: add a commented example to `main.go` once the public Services constructor's `Schema []Entry` has its final shape.
5. **Marking bundle plugins in `husk plugin list`.** See decision 15. Recommended: not until a user asks; `plugin.list` could then gain a `dir` field (the plugin's directory), which answers "where did this plugin come from" for `--plugin-dir` entries too, without a bundle-specific field.
