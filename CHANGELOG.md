# Changelog

All notable changes to this project are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
`npm/package.json`.

## [Unreleased]

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
