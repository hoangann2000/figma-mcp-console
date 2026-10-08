# Contributing

Thanks for helping out. This project is a Go MCP server plus a Figma plugin that talk over a local WebSocket bridge — if you can run Figma Desktop and `go test`, you can work on it.

For *why* the non-obvious parts look the way they do, see [docs/design-notes.md](docs/design-notes.md).

## Prerequisites

- **Go** 1.26+ (`go version`)
- **Node.js** 18+ — only for the npm wrapper and the release build
- **Figma Desktop** — the web app cannot run development plugins

## Run your changes

```sh
# 1. Build the server
go build -o /tmp/figma-mcp ./cmd/figma-mcp

# 2. Point your MCP client at that binary instead of npx.
#    Claude CLI, from the directory you want it registered in:
claude mcp remove figma-console
claude mcp add figma-console -- /tmp/figma-mcp
```

Then in Figma Desktop: **Plugins → Development → Import plugin from manifest…** → pick `plugin/manifest.json` **from your checkout**, and run **Plugins → Development → Figma MCP Console**. Keep the window open; a green dot means it reached the bridge.

Exported assets are written relative to the server's working directory, which is wherever your client launched it.

### Reloading after an edit

- **Plugin files**: close the plugin window and run it again. No build step, and no hot reload — Figma only reads the files when the window opens.
- **Go server**: `go build`, then restart your MCP client so it respawns the server.

### Four things that will confuse you once

- **Figma loads the plugin from the folder you imported**, shown under its name in **Plugins → Development**. Import the release zip or `install-plugin` and you are editing a copy that Figma never reads — the symptom is `unknown command: <your new tool>`, which looks like a missing feature.
- **One process owns port 2000.** Another editor or an older session can be holding the bridge with a stale build, so your rebuilt server is idle and the plugin reports a protocol mismatch. Find it with `lsof -nP -iTCP:2000 -sTCP:LISTEN`; kill it and the next server takes over in about a second.
- **The status line names the side that is behind** — "Server outdated" vs "Plugin outdated". Keep it truthful by bumping `bridge.ProtocolVersion` and `PROTOCOL` in `plugin/ui.html` together whenever you add or change commands.
- **stdin is the MCP transport.** Running the server with no stdin (background job, `nohup`) makes it read EOF and exit at once, looking like a crash. To drive it by hand: `tail -f /dev/null | ./figma-mcp`. Keep one such server alive while testing, or the plugin flips between `Connected` and `Searching for MCP server…` as short-lived servers come and go.

## Architecture in one paragraph

`cmd/figma-mcp` serves MCP over stdio. `internal/bridge` runs a shared WebSocket bridge on port 2000: the first session to bind it is the **owner** and serves every plugin window (`/ws`) and every other session (`/peer`); the rest become peers and forward through it. `internal/tools` registers one MCP tool per plugin command. `plugin/code.js` executes those commands against the Figma Plugin API; `plugin/ui.html` is the WebSocket client, since the plugin main thread has no network access. `CLAUDE.md` has more detail.

**stdout is the MCP transport.** Anything you print there corrupts the protocol — log to stderr.

## Adding a tool

A tool exists in four places. Miss one and it fails at runtime, not at compile time:

1. **`internal/tools/tools.go`** — an args struct embedding `fileArg` (and `nodeTarget` if it mutates existing nodes), plus a `registerBridged[...]` call.
2. **`plugin/code.js`** — a handler in the `handlers` object **with exactly the same name**. That is how dispatch works.
3. **`cmd/figma-mcp/main_test.go`** — bump the expected tool count. It is a deliberate tripwire against adding or dropping a tool by accident.
4. **`internal/bridge.ProtocolVersion`** *and* the `PROTOCOL` constant in `plugin/ui.html` — only when you change the message shape or break an existing command. A hand-installed plugin cannot auto-update, so the mismatch warning is what tells users to re-import.

Conventions worth matching:

- Mutating tools take `node_id` **or** `node_ids[]` via `nodeTarget`, and return `done(params, nodes)` — ids by default, full summaries only when `verbose` is set. A batch of 50 echoed summaries buries the caller's context.
- Anything needing the filesystem or the network belongs in the Go server, not the plugin: the sandbox has neither. `download_assets`, `import_image` and `get_screenshot` show the pattern.
- All disk paths go through `resolveProjectPath`, which rejects anything escaping the working directory.
- `jsonschema:"..."` tags are what the model reads to decide how to call your tool. Say what the parameter does and what a valid value looks like.

## Tests

```sh
go test ./...              # all
go test -race ./...        # what CI runs
go test -run TestEndToEnd ./cmd/figma-mcp
```

`TestEndToEnd` boots the real binary and speaks MCP to it; it does not need Figma, since the point is the not-connected path. `TestEveryBridgedToolHasAPluginHandler` catches a tool registered without its handler.

There is no test harness for `plugin/code.js` — CI never runs it. At minimum `node --check plugin/code.js`, and exercise anything non-trivial in a real Figma file before opening the PR. Say in the PR what you tested by hand.

## Commits and PRs

- [Conventional Commits](https://www.conventionalcommits.org/): `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`, with `!` or a `BREAKING CHANGE:` footer for breaks.
- One topic per PR. Say what changed, why, how you verified it, and how to roll it back.
- CI runs `go vet ./...` and `go test -race ./...` on every PR, and can be run on demand from the Actions tab.

## Releasing (maintainers)

```sh
vim npm/package.json          # version is the single source of truth
./npm/build.sh                # 6 binaries + plugin copy -> npm/bin/
cd npm && npm publish
git tag v0.6.0 && git push origin v0.6.0
```

The tag is what triggers `release.yml`, which packages `plugin/` into `plugin.zip` and creates the GitHub Release. Binaries ship through npm only — no workflow publishes them. Publish and tag together: a release whose plugin speaks a newer protocol than the published server makes every user see the outdated warning.
