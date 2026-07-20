# Figma MCP Console

[![npm](https://img.shields.io/npm/v/@hoangann/figma-mcp-console?color=cb3837&logo=npm)](https://www.npmjs.com/package/@hoangann/figma-mcp-console)
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
claude mcp add figma-console -- npx -y @hoangann/figma-mcp-console@latest
```

Or add to the project's `.mcp.json`:

```json
{
  "mcpServers": {
    "figma-console": {
      "command": "npx",
      "args": ["-y", "@hoangann/figma-mcp-console@latest"]
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
      "args": ["-y", "@hoangann/figma-mcp-console@latest"]
    }
  }
}
```

Restart Claude Desktop. On Windows use `"command": "cmd"`, `"args": ["/c", "npx", "-y", "@hoangann/figma-mcp-console@latest"]`.

</details>

<details>
<summary><b>Codex CLI</b></summary>

```sh
codex mcp add figma-console -- npx -y @hoangann/figma-mcp-console@latest
```

Or in `~/.codex/config.toml`:

```toml
[mcp_servers.figma-console]
command = "npx"
args = ["-y", "@hoangann/figma-mcp-console@latest"]
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
      "args": ["-y", "@hoangann/figma-mcp-console@latest"],
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
      "args": ["-y", "@hoangann/figma-mcp-console@latest"]
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
      "args": ["-y", "@hoangann/figma-mcp-console@latest"]
    }
  }
}
```

Click **Refresh** after saving.

</details>

<details>
<summary><b>Other MCP clients (opencode, Windsurf, Cline, Gemini CLI, Zed, …)</b></summary>

Standard MCP stdio transport — any client works. Set the command to `npx` with args `["-y", "@hoangann/figma-mcp-console@latest"]`.

</details>

> 💡 **Tip:** register the server in your **project** config (not the global one) so exported files land inside that project.

### Step 2 — Install the Figma plugin

1. Download **[plugin.zip](https://github.com/hoangann2000/figma-mcp-console/releases/latest)** and unzip it
2. In **Figma Desktop**: **Plugins → Development → Import plugin from manifest…** → select `manifest.json` *(one-time step)*
3. Run it: **Plugins → Development → Figma MCP Console** — and **keep the plugin window open** while you work. 🟢 Green dot = connected

### Step 3 — Try it

#### 🖥️ Implement a Figma design in code

In Figma, right-click the page's frame → **Copy link to selection**, then paste this prompt:

> **📋 Copy-paste prompt:**

```text
Implement this Figma page in our project: <paste link>

- Recreate the layout, spacing, typography and colors exactly as designed
- Export icons from the design as SVG into public/icons and images as PNG
  into public/images, and use those files in the code — do not create your own
- The design is the desktop layout (1440px); make the page responsive
  and adapt it sensibly for tablet and mobile
- When done, screenshot the Figma frame, compare it with the implemented
  page, and fix any differences before finishing
```

The link contains the frame's node id, so the AI knows exactly which page to read. *(No link? Just select the frame in Figma and say "implement the frame I selected".)*

#### 🎨 Draw a Figma design from a requirement

> **📋 Copy-paste prompt:**

```text
Design a landing page (desktop, 1440px wide) in Figma for this requirement:
"SaaS product page: hero with headline and CTA button, three feature cards,
a pricing section with three plans, and a footer."

- light theme, primary color #2563EB, font Inter
- use auto-layout for every section, 80px vertical spacing between sections
- when done, take a screenshot, compare it against this brief, and fix
  anything that's off
```

> 🎯 **The screenshot line matters in both workflows** — it makes the AI look at the actual result and iterate instead of guessing. The more concrete the brief, the closer the first pass lands.

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

> **📋 Copy-paste rules** (adjust paths to your project — this one fits Next.js):

```markdown
## Figma asset rules
- Icons: always SVG, into public/icons/. Never export icons as PNG.
- Images: PNG into public/images/, exported at the size they are actually
  displayed. Use 2x only for small fixed-size images that must stay sharp
  on retina screens; never blanket-export everything at 2x.
- File names: kebab-case, English, descriptive (arrow-left.svg, hero-banner.png).
  Ignore the Figma layer names if they are messy.
- Design tokens: read them with get_variable_defs and map to our theme config.
- After building a screen, take a screenshot and fix issues before finishing.
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
