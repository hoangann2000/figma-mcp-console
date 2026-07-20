# Figma MCP Console

[![npm](https://img.shields.io/npm/v/figma-mcp-console?color=cb3837&logo=npm)](https://www.npmjs.com/package/figma-mcp-console)
[![MCP](https://img.shields.io/badge/MCP-Compatible-blue)](https://modelcontextprotocol.io/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://github.com/hoangann2000/figma-mcp-console/blob/main/LICENSE)

> **Let your AI assistant design in Figma — no API token, no rate limits.**
> Works with Claude CLI · Claude Desktop · Codex · VS Code · Cursor · Antigravity · any MCP client.

## ✨ What can it do?

- 🖥️ **Figma → Code** — paste a link to a Figma frame, and the AI implements that exact screen in your codebase, exporting the design's icons and images into your project instead of inventing its own
- 🎨 **Requirement → Figma** — paste a requirement, and the AI draws the screen in Figma, then screenshots its own work and fixes what's off
- 🔁 **Token sync** — pull design tokens into your theme config, or build a design system in Figma from your code
- ✏️ **Bulk editing** — rename layers, replace text across a page, reorganize frames

> 💡 **Why no token?** Most Figma MCP servers call the REST API — access token required, and free plans get only a handful of calls. This one drives a plugin **inside Figma Desktop** instead: the same unlimited API every Figma plugin uses. Everything stays on your machine.

---

## ⚡ Quick start

**Prerequisites**

- [ ] [Node.js](https://nodejs.org) 18+ — check with `node --version`
- [ ] **Figma Desktop** app (the web app can't run development plugins)

### Step 1 — Add the server to your AI tool

No install needed — `npx` downloads and runs everything automatically.

<details>
<summary><b>Claude CLI</b></summary>

For one project — run inside that project (registers it there only):

```sh
claude mcp add figma-console -- npx -y figma-mcp-console@latest
```

For all your projects at once — add it globally instead:

```sh
claude mcp add --scope user figma-console -- npx -y figma-mcp-console@latest
```

Both work the same at runtime: each Claude session starts its own server instance in that project's directory (so exported assets land in the right project), and all instances share one local bridge to Figma automatically.

Or add to the project's `.mcp.json`:

```json
{
  "mcpServers": {
    "figma-console": {
      "command": "npx",
      "args": ["-y", "figma-mcp-console@latest"]
    }
  }
}
```

</details>

<details>
<summary><b>Claude Desktop</b></summary>

**Settings → Developer → Edit Config** (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "figma-console": {
      "command": "npx",
      "args": ["-y", "figma-mcp-console@latest"]
    }
  }
}
```

Restart Claude Desktop. On Windows use `"command": "cmd"`, `"args": ["/c", "npx", "-y", "figma-mcp-console@latest"]`.

</details>

<details>
<summary><b>Codex CLI</b></summary>

```sh
codex mcp add figma-console -- npx -y figma-mcp-console@latest
```

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.figma-console]
command = "npx"
args = ["-y", "figma-mcp-console@latest"]
```

</details>

<details>
<summary><b>VS Code (GitHub Copilot)</b></summary>

Create `.vscode/mcp.json` (or **Command Palette → "MCP: Add Server"**):

```json
{
  "servers": {
    "figma-console": {
      "type": "stdio",
      "command": "npx",
      "args": ["-y", "figma-mcp-console@latest"],
      "cwd": "${workspaceFolder}"
    }
  }
}
```

Use it from Copilot Chat in **Agent** mode.

</details>

<details>
<summary><b>Cursor</b></summary>

Create `.cursor/mcp.json` (or **Settings → MCP → Add new MCP server**):

```json
{
  "mcpServers": {
    "figma-console": {
      "command": "npx",
      "args": ["-y", "figma-mcp-console@latest"]
    }
  }
}
```

</details>

<details>
<summary><b>Antigravity</b></summary>

Agent panel → **MCP Servers → Manage MCP servers → View raw config** (`mcp_config.json`):

```json
{
  "mcpServers": {
    "figma-console": {
      "command": "npx",
      "args": ["-y", "figma-mcp-console@latest"]
    }
  }
}
```

Click **Refresh** after saving.

</details>

<details>
<summary><b>Other MCP clients (opencode, Windsurf, Cline, Gemini CLI, Zed, …)</b></summary>

Standard MCP stdio transport — any client works. Set the command to `npx` with args `["-y", "figma-mcp-console@latest"]`.

</details>

> 💡 **Tip:** exported assets are written relative to the directory the server runs in. CLI tools (Claude CLI, Codex, Cursor, …) start it in your project directory, so global registration is fine. In clients without a working directory (e.g. Claude Desktop), pass explicit export paths or prefer a project-level config where available.

### Step 2 — Install the Figma plugin

1. Run **`npx figma-mcp-console install-plugin`** — it writes the plugin files locally and prints the path to `manifest.json`
2. In **Figma Desktop**: **Plugins → Development → Import plugin from manifest…** → select that `manifest.json` *(one-time step)*
3. Run it: **Plugins → Development → Figma MCP Console** — and **keep the plugin window open** while you work. 🟢 Green dot = connected
4. Working with several Figma files at once? Run the plugin in **each** file — every window connects to the same local bridge, and the AI picks the right file per call

### Step 3 — Try it

**Figma → code** — right-click a frame → **Copy link to selection**, then:

```text
Implement this Figma page in our project: <paste link>. Match layout, spacing,
type and colors; export its icons/images into the project; screenshot the frame
and compare when done.
```

**Requirement → Figma:**

```text
Design a SaaS landing page in Figma (desktop, 1440px): hero + CTA, three feature
cards, pricing with three plans, footer. Light theme, use auto-layout, then
screenshot and fix anything off.
```

> 🎯 Asking the AI to **screenshot and compare** at the end makes it check its own work instead of guessing.

---

## 🗂 Multiple files, multiple projects

All AI sessions and all Figma files share **one local bridge** on port 2000 — the first MCP server to start owns it, later ones join it automatically, and if the owner exits another session takes over within a second. Nothing to configure.

- **Several Figma files**: run the plugin in each file (its window shows *Connected · \<file name\>*). With more than one file connected, every tool accepts a `file` parameter (file name, case-insensitive), and `list_files` shows what's connected — so you can say things like *"copy the primary color from **Design System** into the CTA button in **Landing Page**"* in a single session. With one file connected, nothing changes.
- **Several code projects**: each AI session (one per project) reaches every connected file through the shared bridge. State which file you mean in the prompt and each session works with the right one.

## 🧠 Teach your AI what to export, and where

Exports follow the file extension the AI picks (`.svg` for vector, `.png`/`.jpg` for bitmap, with an optional 0.5–4x scale). It decides based on what you ask — so put your preferences in the project rules file once, and stop repeating yourself:

| AI tool | Rules file |
|---|---|
| Claude CLI / Claude Desktop | `CLAUDE.md` |
| Codex / opencode | `AGENTS.md` |
| Cursor | `.cursorrules` |
| VS Code Copilot | `.github/copilot-instructions.md` |
| Windsurf | `.windsurfrules` |

> **📋 Copy-paste rules** (adjust paths — this fits Next.js):

```markdown
## Figma asset rules
- Icons: SVG into public/icons/. Images: PNG into public/images/.
- File names: kebab-case English (arrow-left.svg, hero-banner.png).
- Read design tokens with get_variable_defs; map them to our theme config.
- After building a screen, screenshot and fix issues before finishing.
```

## 🔧 Troubleshooting

| Problem | Fix |
|---|---|
| ❌ "no Figma plugin is connected" | Open the plugin in Figma Desktop (**Plugins → Development → Figma MCP Console**) and keep its window open — in every file you want the AI to touch |
| 🕐 Plugin says "Searching for MCP server…" | Normal before your AI client connects — the server starts automatically with it. If it never connects, click the status line to retry, or restart your AI client |
| 🗂 "N Figma files are connected; pass the file parameter" | More than one plugin window is open — tell the AI which file you mean (or close the extra plugin windows) |
| 🚫 The MCP server doesn't appear in your AI tool | Check Node.js 18+ is installed (`node -v`) |
| 📁 Exported files end up in the wrong folder | Register the server in the project-level config, not the global one (see the tip in Step 1) |
| 🔤 Font errors when creating text | The Figma file uses a font not installed on your machine — the error message names it |

## 📄 License

[MIT](https://github.com/hoangann2000/figma-mcp-console/blob/main/LICENSE) — © [Hoang Le Thien An](https://github.com/hoangann2000)

> This is an independent community project, not affiliated with or endorsed by Figma, Inc. "Figma" is a trademark of Figma, Inc.
