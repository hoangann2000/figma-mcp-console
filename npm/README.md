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

Run inside the project you work in:

```sh
claude mcp add figma-console -- npx -y figma-mcp-console@latest
```

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

> 💡 **Tip:** register the server in your **project** config (not the global one) so exported files land inside that project.

### Step 2 — Install the Figma plugin

1. Download **[plugin.zip](https://github.com/hoangann2000/figma-mcp-console/releases/latest)** and unzip it
2. In **Figma Desktop**: **Plugins → Development → Import plugin from manifest…** → select `manifest.json` *(one-time step)*
3. Run it: **Plugins → Development → Figma MCP Console** — and **keep the plugin window open** while you work. 🟢 Green dot = connected

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
| ❌ "Figma plugin is not connected" | Open the plugin in Figma Desktop (**Plugins → Development → Figma MCP Console**) and keep its window open |
| 🕐 Plugin says "Waiting for MCP server on port 2000" | Normal before your AI client connects — the server starts automatically with it. If it never connects, restart your AI client |
| 🚫 The MCP server doesn't appear in your AI tool | Check Node.js 18+ is installed (`node -v`); the first run also needs internet access to download the server |
| 📁 Exported files end up in the wrong folder | Register the server in the project-level config, not the global one (see the tip in Step 1) |
| 🔤 Font errors when creating text | The Figma file uses a font not installed on your machine — the error message names it |

## 📄 License

[MIT](https://github.com/hoangann2000/figma-mcp-console/blob/main/LICENSE) — © [Hoang Le Thien An](https://github.com/hoangann2000)

> This is an independent community project, not affiliated with or endorsed by Figma, Inc. "Figma" is a trademark of Figma, Inc.
