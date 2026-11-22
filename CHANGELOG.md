# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
`npm/package.json`.

## [0.10.0] - 2026-11-22

No plugin protocol change, but the plugin code changed: re-import it to get
these.

### Changed

- **New plugin window.** 320 px wide, larger type, one heading per block:
  - the header shows the file, the open page and the state in one word
    (Connected, Editing while a write runs, Searching, Outdated), with a
    button to collapse the window to that header;
  - Read-only is a switch with a line saying what it means;
  - when Read-only blocks the AI, the window says which command and offers
    "Allow edits"; a version mismatch names the side to update and links to
    the latest plugin;
  - selected layers and the activity log are lists of rows, the log in plain
    words with the layer's name, 20 entries deep;
  - it follows Figma's light or dark theme, and a button in the footer
    switches it by hand; the choice is kept.
- `set_auto_layout` takes `node_ids` like the other `set_*` tools, so a list
  of frames gets the same layout in one call. `node_id` works as before.
- **`audit_design` touch targets** (`min_touch`): the outermost control is the
  tap target, so icons inside a button or menu item are no longer candidates;
  icons that act on their own (close, trash, edit, eye, copy, calendar, …)
  now are, unless a wrapper of the required size holds only that icon. Each
  finding says why the layer counts (`because`: prototype link, name, action
  icon). On a real password screen this reports the two 20 px eye icons and
  none of the decorative ticks.
- **`audit_design` `measure_text`**: measures fixed-size text exactly by
  letting a copy grow to the height it needs, then removing the copy in the
  same step. It is a write, so it only runs with Read-only off; otherwise the
  estimate is used and the result says so.

## [0.9.0] - 2026-11-21

### Upgrading from 0.8.x

Re-import the Figma plugin: protocol 5 → 6.

### Added

- **`set_layout_child`** — how layers size and sit in their parent: `FILL` /
  `HUG` / `FIXED` per axis, min and max sizes, `ABSOLUTE` positioning inside
  auto layout, and constraints for frames without it. Until now the AI could
  build auto layout but not make a child stretch with it. Errors say why a
  value does not apply (FILL without an auto-layout parent, HUG on a shape).
- **`set_node_properties`** — visibility, opacity, rotation, lock, and a
  frame's clipping.
- `set_auto_layout`: `layout_wrap` and `counter_axis_spacing` for rows that wrap.
- **`create_section`**, placed in free space when no position is given.
- **`update_style`** and **`remove_styles`**: edit a style's name,
  description, color, font or effects in place, or delete it.
- `set_text_properties`: `text_decoration` (underline, strikethrough) and
  `text_case`.
- `set_node_properties`: `description` for components and component sets.
- Tool count: 65 → 70.

## [0.8.1] - 2026-11-21

No plugin re-import needed: the protocol is unchanged.

### Security

- Paths the tools read or write (`download_assets`, `out`, `import_tokens`,
  `import_image`, `diff_design` baselines) are now checked after resolving
  symlinks: a symlinked folder or file inside the project that points outside
  it is refused. Before, only the path's text was checked.

## [0.8.0] - 2026-11-21

### Upgrading from 0.7.x

Re-import the Figma plugin: protocol 4 → 5, for the page tools.
`import_image` now fetches only public `https` URLs (see Security).

### Added

- **`create_page`**, **`rename_page`**, **`remove_page`**. Removing the open
  page opens its neighbour first; the file's only page cannot be removed. All
  three are writes, so Read-only blocks them; `list_pages` and
  `set_current_page` still work while it is on.
- Figma's "Unable to establish connection to Figma … check your internet
  connection" now comes with what it usually means: data for a page not yet
  opened in this session, or a node id that does not exist.
- README: Read-only, the design checks, token sync, icons, updating the
  plugin, and troubleshooting rows for "blocked: read-only", "Plugin
  outdated", "Server outdated" and files you cannot edit.

### Changed

- Tool definitions are about 1,250 tokens smaller: the `file` parameter,
  repeated in 61 tools, has a shorter description.
- CI and release use `actions/checkout@v7`, `setup-go@v7`, `setup-node@v7`
  (Node 24); the release job never restores a package cache.

### Security

- **`import_image` fetches only public `https` addresses.** `http://` URLs,
  this machine, private and link-local networks (including cloud metadata at
  169.254.169.254) and carrier-grade NAT are refused where the connection is
  made, so a redirect or a DNS name pointing inward is refused too;
  redirects are limited to 5 and must stay on https; proxy settings from the
  environment are ignored. The AI chooses the URL, and text it reads (a spec,
  a design) could otherwise steer the server at local services.

### Tests

- Plugin tests now cover prototype links, variant layout, variable binding,
  `find_nodes` filters, component properties, the page tools, Read-only on
  them, and the new error hint; each was checked to fail when the code it
  covers is broken.
- Tool count: 62 → 65.

## [0.7.0] - 2026-11-21

### Upgrading from 0.6.x

Re-import the Figma plugin: protocol 3 → 4. An 0.6 plugin keeps working for
everything it knew; SVG import and the two `remove_*` tools need the new one,
and errors from the old one say so.

### Added

- **`import_tokens`** — creates or updates variables from CSS custom properties
  (`:root` = default mode, `[data-theme="x"]` or `.x` = mode x) or a
  design-tokens JSON tree. It reads what `export_tokens` writes, so the two
  keep code and Figma in sync: variables are matched by name or by the CSS
  name `export_tokens` gave them and updated in place, equal values are left
  alone (a second run changes nothing), and `var(--x)` / `{a.b}` become
  aliases, also to variables created in the same run. Colors become COLOR,
  numbers and `px` FLOAT, `true`/`false` BOOLEAN, anything else STRING.
  Missing modes are added; when the Figma plan allows fewer modes, those values
  are skipped and listed. `dry_run` reports the plan without writing. All
  writes go out as one `batch`.
- **`remove_variables`** and **`remove_variable_collection`**. Nodes bound to
  a removed variable are detached and keep the value they showed.
- **`import_image` takes SVG**: it becomes editable vector layers (Figma's
  image fills take only PNG, JPG and GIF), scaled proportionally to
  `width`/`height` with stroke widths kept in proportion, and `color`
  recolors every solid fill and stroke. The tool description points at icon
  libraries by URL with a pinned version, e.g.
  `https://unpkg.com/lucide-static@<version>/icons/<name>.svg`, after checking
  the file's own icon components first.

### Notes

- Tool count: 59 → 62.

## [0.6.0] - 2026-11-21

### Upgrading from 0.5.x

**Re-import the Figma plugin** from this release's `plugin.zip` (or
`npx figma-mcp-console install-plugin`), then run it again in every file.
The server and plugin protocol went from 1 to 3. An old plugin still connects
and the tools it knew keep working, but the 13 commands added since 0.5.4
answer `unknown command`; the server now adds a "re-import the plugin" hint
to those errors so the AI can tell you what to do.

The plugin now opens with **Read-only ticked**: untick it in the plugin
window before asking the AI to change a design.

### Added

- **Read-only switch** in the plugin window, on every time it opens: while
  ticked, every command that would change the file is refused and Figma shows
  a notice. `list_files` reports `read_only` per file.
- **`diff_design`** — what changed in a frame since a snapshot (a project file,
  or a checkpoint the user saves from the plugin window): layers added,
  removed, or changed in text, color, size, position, font, padding,
  visibility or component state.
- **`get_component_states`** — every component used in a frame, the states its
  variants define, the state each instance is in, and hand-made overrides.
- **Pages:** `list_pages`, `set_current_page`; `find_nodes` takes `page` and
  names the scope it searched.
- **Bulk editing:** `node_ids[]` on every mutating tool, with `{ok, ids}`
  results unless `verbose`; `rename_nodes`, `set_text_properties`.
- **Components:** `combine_as_variants` (lays variants out and sizes the set),
  `add_component_property`, `set_instance_properties`.
- **Prototyping:** `set_prototype_links`, `get_prototype_links`,
  `set_flow_starting_point`.
- `get_design_context`: `compact` (hex colors, defaults omitted), `out` to save
  to a file, `visible_only`, and the variable/style names behind values,
  annotations, variant values and mixed-style text runs.
- `find_nodes` filters on fill, font size and font style and returns chosen
  `fields`; `get_screenshot` reports the scale it applied and takes
  `max_dimension`; `get_selection` returns figma.com links.
- **`audit_design`** — checks a frame and returns only the findings, so the AI
  verifies its work with data instead of reading a screenshot:
  - `clipped` / `hidden_by_clip`: layers cut off or fully hidden by a clipping ancestor
  - `overflows_parent`: layers sticking out of a parent that does not clip
  - `text_overflows_box` / `text_truncated`: text spilling out of, or cut short in, a fixed-size box
  - `low_contrast`: text below WCAG AA (4.5:1, or 3:1 for large text), measured
    against the fill actually behind it (an ancestor, or a sibling drawn underneath)
  - `small_touch_target`: controls smaller than `min_touch` (opt-in, e.g. 44 for mobile)

  It is a read: it never changes the file and works while Read-only is ticked.
  Overflow under 1 px is ignored, and layout inside instances is not checked
  (it belongs to the main component; the result counts those layers under
  `layoutNotChecked`). Contrast and touch size are still checked there. Both
  rules came from a run on a real admin-UI file, where every layout finding was
  0.5 px of icon-vector noise inside instances. Text inside an instance whose
  variant marks it disabled (`State=Disabled`, `Style=Disable`, `Disabled=true`)
  is skipped for contrast, as WCAG exempts inactive controls, and counted under
  `contrastSkippedDisabled`.
  Text on an image, gradient or translucent fill cannot be measured and is
  counted under `contrastNotMeasured` rather than guessed.
- **`batch`** — runs several commands in one call. `"$N"` in an argument stands
  for the id of the node op N created, so a frame and its contents fit in one
  batch. Each op reports its own result or error; a failure does not stop or
  undo the others unless `stop_on_error` is set. `get_screenshot`,
  `download_assets` and `import_image` are refused inside a batch. A batch
  counts as a write and is blocked whole while Read-only is ticked.
- **`export_tokens`** — exports the file's variables as CSS custom properties
  (default mode under `:root`, other modes under `[data-theme="<mode>"]`) or as
  a W3C design-tokens JSON tree, with aliases kept as references. `out` saves
  the result into the project. Numbers are exported without units.
- `create_frame`: `x` and `y` are now optional. A top-level frame created
  without them is placed 100 px to the right of the existing content instead
  of at 0,0 on top of it.
- `design_workflow` prompt: uses `batch`, free-space placement, and
  `audit_design` before the screenshot step.
- The plugin reports its protocol when it connects. Errors from a plugin older
  than the server end with a hint to re-import it, instead of a bare
  `unknown command`.
- Releases publish to npm from CI (`release.yml`, npm Trusted Publishing over
  OIDC) when a `vX.Y.Z` tag on `main` is pushed; the GitHub Release notes are
  taken from this file.

### Changed

- Commands run one at a time per Figma file; the wait for a turn does not count
  against a call's timeout, and timeout and disconnect errors say how to recover.
- `/ws` rejects connections from web pages (by `Origin`), and `/peer` rejects
  any request carrying an `Origin`, so a browser tab cannot drive Figma.
- **`create_frame` no longer fills nested frames white.** A frame created
  inside another frame, component or group now has no fill unless `fill_color`
  is given; top-level frames (on a page or section) are still white. Layout
  wrappers were turning into white slabs over the design. To get the old
  behaviour pass `fill_color: "#FFFFFF"`.
- `create_rectangle` / `create_ellipse`: `x` and `y` default to 0 when omitted
  (they were required).

### Notes

- Tool count: 44 in 0.5.4 → 59.
- Considered and left out: merging the `create_*` / `set_*` tools into fewer,
  larger ones (breaks every existing prompt), arbitrary script execution
  (`run_script` was tried on `dev` and removed before release), and a token on
  `/peer` (same-user processes could read it anyway; browsers are refused by
  the Origin check).
