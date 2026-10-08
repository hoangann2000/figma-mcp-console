// Figma MCP Console — plugin main thread.
// Receives {id, command, params} from the Go bridge (relayed through ui.html),
// executes it against the Figma Plugin API, and replies {id, result|error}.

figma.showUI(__html__, { width: 280, height: 104 });

// Tell the UI which file this plugin window runs in; the UI forwards it to
// the bridge on every (re)connect so calls can be routed per file when
// several files run the plugin at once. figma.fileKey is undefined for
// non-published dev plugins, so the file name is the primary identifier.
figma.ui.postMessage({
  register: { key: figma.fileKey || undefined, name: figma.root.name },
});

// ---------- helpers ----------

function hexToRGB(hex) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex);
  if (!m) throw new Error("invalid hex color: " + hex + " (expected #RRGGBB)");
  const v = parseInt(m[1], 16);
  return { r: ((v >> 16) & 255) / 255, g: ((v >> 8) & 255) / 255, b: (v & 255) / 255 };
}

async function node(id) {
  const n = await figma.getNodeByIdAsync(id);
  if (!n) throw new Error("node not found: " + id);
  return n;
}

// Pages are addressed by id or name. With documentAccess "dynamic-page" a
// page's children are unreachable until it is loaded, so load it here.
async function resolvePage(ref) {
  const want = String(ref);
  const lower = want.toLowerCase();
  const pages = figma.root.children;
  const p =
    pages.find((x) => x.id === want) ||
    pages.find((x) => x.name.toLowerCase() === lower) ||
    pages.find((x) => x.name.toLowerCase().indexOf(lower) !== -1);
  if (!p) throw new Error("page not found: " + ref + " (see list_pages)");
  await p.loadAsync();
  return p;
}

// Figma stacks newly combined variants on top of each other and never grows
// the set to fit them, so the extras are invisible and clipped. Lay them out.
function arrangeVariants(set) {
  if (set.layoutMode && set.layoutMode !== "NONE") return;
  const gap = 32;
  let y = 0;
  let w = 0;
  for (const v of set.children) {
    v.x = 0;
    v.y = y;
    y += v.height + gap;
    w = Math.max(w, v.width);
  }
  if (y > 0) set.resize(Math.max(w, 1), Math.max(y - gap, 1));
}

// Mutating commands accept one node_id or many node_ids so a bulk edit costs
// one call instead of one per node.
async function targets(params) {
  const ids = params.node_ids && params.node_ids.length
    ? params.node_ids
    : params.node_id ? [params.node_id] : [];
  if (!ids.length) throw new Error("node_id or node_ids is required");
  const out = [];
  for (const id of ids) out.push(await node(id));
  return out;
}

// Result of a mutating command. Echoing a full summary per node drowns the
// caller's context on a 50-node batch, so default to ids and let the caller
// ask for the rest with verbose.
function done(params, nodes, extra) {
  const list = Array.isArray(nodes) ? nodes : [nodes];
  const out = params.verbose
    ? { nodes: list.map(summarize) }
    : { ok: true, ids: list.map((n) => n.id) };
  return Object.assign(out, extra || {});
}

// Mixed values (figma.mixed) are Symbols, which postMessage cannot clone.
function val(x) {
  return x === figma.mixed ? "mixed" : x;
}

function rgbToHex(c) {
  const h = (v) => ("0" + Math.round(v * 255).toString(16)).slice(-2);
  return "#" + h(c.r) + h(c.g) + h(c.b);
}

// A full PaintObject is ~20 lines of JSON (17-digit float channels, complete
// gradientTransform); compact callers only ever need the color.
function paintBrief(p) {
  if (p.visible === false) return "hidden";
  if (p.type !== "SOLID") return p.type;
  const hex = rgbToHex(p.color);
  return p.opacity === undefined || p.opacity === 1 ? hex : hex + "@" + p.opacity;
}

// First visible solid fill as hex, for find_nodes' fill filter.
function solidFillHex(n) {
  if (!("fills" in n) || !Array.isArray(n.fills)) return null;
  for (const p of n.fills) {
    if (p.type === "SOLID" && p.visible !== false) return rgbToHex(p.color);
  }
  return null;
}

function summarize(n) {
  const s = { id: n.id, name: n.name, type: n.type };
  if (n.parent) s.parentId = n.parent.id;
  if ("x" in n) { s.x = n.x; s.y = n.y; }
  if ("width" in n) { s.width = n.width; s.height = n.height; }
  if (n.type === "TEXT") s.text = n.characters;
  return s;
}

// The file key needed to build share links. Published plugins can read it from
// figma.fileKey, but development plugins (imported from a manifest) cannot, so
// we fall back to a key the user pastes once — their file URL — persisted in
// the file via pluginData so it survives reopening.
const FILE_KEY_DATA = "figma-mcp-file-key";
let fileKey = figma.fileKey || "";
try {
  if (!fileKey) fileKey = figma.root.getPluginData(FILE_KEY_DATA) || "";
} catch (e) {}

function setFileKey(key) {
  fileKey = key || "";
  try {
    figma.root.setPluginData(FILE_KEY_DATA, fileKey);
  } catch (e) {}
}

// nodeLink builds a shareable figma.com URL that jumps straight to a node (the
// same link as Figma's "Copy link to selection"). Returns null when no file
// key is known yet, so callers simply omit the link.
function nodeLink(id) {
  if (!fileKey) return null;
  return (
    "https://www.figma.com/design/" +
    fileKey +
    "/" +
    encodeURIComponent(figma.root.name) +
    "?node-id=" +
    id.replace(/:/g, "-")
  );
}

// compact drops defaults (visible, full opacity, empty fills) and renders
// paints as hex: depth 1 on a 27-child frame is ~15k tokens otherwise.
function serialize(n, depth, compact) {
  const s = summarize(n);
  if (compact) {
    for (const k of ["x", "y", "width", "height"]) {
      if (typeof s[k] === "number") s[k] = Math.round(s[k] * 100) / 100;
    }
  }
  if ("visible" in n && (!compact || !n.visible)) s.visible = n.visible;
  if ("opacity" in n && (!compact || n.opacity !== 1)) s.opacity = n.opacity;
  if ("fills" in n && Array.isArray(n.fills) && (!compact || n.fills.length)) {
    s.fills = compact ? n.fills.map(paintBrief) : n.fills;
  }
  if ("strokes" in n && n.strokes.length) {
    s.strokes = compact ? n.strokes.map(paintBrief) : n.strokes;
    if (!compact || val(n.strokeWeight) !== 1) s.strokeWeight = val(n.strokeWeight);
  }
  if ("effects" in n && n.effects.length) {
    s.effects = compact ? n.effects.map((e) => e.type) : n.effects;
  }
  if ("cornerRadius" in n && n.cornerRadius !== 0) s.cornerRadius = val(n.cornerRadius);
  if (n.type === "TEXT") {
    s.fontSize = val(n.fontSize);
    s.fontName = val(n.fontName);
  }
  if ("layoutMode" in n && n.layoutMode !== "NONE") {
    s.layoutMode = n.layoutMode;
    s.itemSpacing = n.itemSpacing;
    s.padding = {
      top: n.paddingTop, right: n.paddingRight,
      bottom: n.paddingBottom, left: n.paddingLeft,
    };
    s.primaryAxisAlignItems = n.primaryAxisAlignItems;
    s.counterAxisAlignItems = n.counterAxisAlignItems;
  }
  if ("children" in n && (!compact || n.children.length)) {
    s.childCount = n.children.length;
    if (depth > 0) s.children = n.children.map((c) => serialize(c, depth - 1, compact));
  }
  return s;
}

async function parentOf(params) {
  if (params.parent_id) {
    const p = await node(params.parent_id);
    if (!("appendChild" in p)) throw new Error("node " + params.parent_id + " cannot have children");
    return p;
  }
  return figma.currentPage;
}

// Builds a GradientPaint; the transform rotates the gradient around the
// node's center by `angle` degrees (0 = left-to-right).
function buildGradient(g) {
  const stops = (g.stops || []).map((s) => {
    const c = hexToRGB(s.color);
    return {
      position: s.position,
      color: { r: c.r, g: c.g, b: c.b, a: s.opacity === undefined || s.opacity === null ? 1 : s.opacity },
    };
  });
  if (stops.length < 2) throw new Error("gradient needs at least 2 stops");
  const rad = ((g.angle || 0) * Math.PI) / 180;
  const cos = Math.cos(rad);
  const sin = Math.sin(rad);
  return {
    type: (g.type || "LINEAR").toUpperCase() === "RADIAL" ? "GRADIENT_RADIAL" : "GRADIENT_LINEAR",
    gradientTransform: [
      [cos, sin, 0.5 - 0.5 * cos - 0.5 * sin],
      [-sin, cos, 0.5 + 0.5 * sin - 0.5 * cos],
    ],
    gradientStops: stops,
  };
}

// COLOR variables take {r,g,b,a}; accept hex strings for convenience.
function variableValue(v, value) {
  if (v.resolvedType === "COLOR" && typeof value === "string") {
    const c = hexToRGB(value);
    return { r: c.r, g: c.g, b: c.b, a: 1 };
  }
  return value;
}

function buildEffects(list) {
  return (list || []).map((e) => {
    const type = e.type;
    if (type === "LAYER_BLUR" || type === "BACKGROUND_BLUR") {
      return { type, radius: e.radius || 0, visible: true };
    }
    if (type !== "DROP_SHADOW" && type !== "INNER_SHADOW") {
      throw new Error("unsupported effect type: " + type);
    }
    const color = hexToRGB(e.color || "#000000");
    color.a = e.color_opacity === undefined ? 0.25 : e.color_opacity;
    return {
      type,
      color,
      offset: { x: e.offset_x || 0, y: e.offset_y || 0 },
      radius: e.radius || 0,
      spread: e.spread || 0,
      visible: true,
      blendMode: "NORMAL",
    };
  });
}

function applyShapeParams(n, params) {
  n.x = params.x;
  n.y = params.y;
  if (params.width > 0 && params.height > 0) n.resize(params.width, params.height);
  if (params.name) n.name = params.name;
  if (params.fill_color && params.fill_color !== "none") {
    n.fills = [{ type: "SOLID", color: hexToRGB(params.fill_color) }];
  }
}

// Tracking the nodes a script creates needs a wrapper around figma, but the
// host object's properties are non-configurable and non-writable, so a Proxy
// get trap that returns anything else (even a bound copy) breaks the Proxy
// invariant and every call throws "proxy: inconsistent get". Scripts get the
// real figma until there is a wrapper that survives that.

// Frames from the script read "at <anonymous> (<input>:6:22)". new Function
// prepends two header lines and we add the async wrapper, so the caller's
// line 1 is file line 4. Verified against the sandbox; a different engine
// would only cost us the hint, since no match means no line is reported.
function scriptLine(e) {
  const m = /<input>:(\d+):\d+/.exec((e && e.stack) || "");
  if (!m) return 0;
  const n = parseInt(m[1], 10) - 3;
  return n > 0 ? n : 0;
}

function scriptFailure(e) {
  let msg = String((e && e.message) || e);
  // Plan limits read like API errors; say what they actually mean.
  if (/Limited to \d+ mode/i.test(msg)) {
    msg += " (Figma plan limit on modes per collection — split the tokens across separate collections)";
  }
  const line = scriptLine(e);
  if (line) msg += " [script line " + line + "]";
  return msg;
}

// A run_script that times out on the server keeps running here: Figma gives
// no way to abort it. Keyed by token, a retry joins the original run instead
// of doing its work a second time.
const scriptRuns = new Map();
const SCRIPT_MEMO = 20;

function rememberRun(token, promise) {
  scriptRuns.set(token, promise);
  while (scriptRuns.size > SCRIPT_MEMO) scriptRuns.delete(scriptRuns.keys().next().value);
}

// Optional extras find_nodes can attach per node, so callers can filter on
// style without a get_design_context round-trip per hit.
const FIELD_GETTERS = {
  fills: (n) => ("fills" in n && Array.isArray(n.fills) ? n.fills.map(paintBrief) : undefined),
  strokes: (n) => ("strokes" in n && Array.isArray(n.strokes) && n.strokes.length ? n.strokes.map(paintBrief) : undefined),
  fontSize: (n) => (n.type === "TEXT" ? val(n.fontSize) : undefined),
  fontName: (n) => (n.type === "TEXT" ? val(n.fontName) : undefined),
  cornerRadius: (n) => ("cornerRadius" in n ? val(n.cornerRadius) : undefined),
  effects: (n) => ("effects" in n && n.effects.length ? n.effects.map((e) => e.type) : undefined),
  opacity: (n) => ("opacity" in n ? n.opacity : undefined),
  characters: (n) => (n.type === "TEXT" ? n.characters : undefined),
};

// The body of run_script, split out so a token can hold onto its promise.
async function runScriptBody(params) {
  const helpers = {
    node, summarize, serialize, hexToRGB, rgbToHex, paintBrief, solidFillHex,
    // The Plugin API has no createAutoLayout; scripts written against other
    // Figma tooling reach for it and fail on "not a function".
    createAutoLayout(direction, props) {
      const f = figma.createFrame();
      f.layoutMode = String(direction || "VERTICAL").toUpperCase() === "HORIZONTAL" ? "HORIZONTAL" : "VERTICAL";
      f.primaryAxisSizingMode = "AUTO";
      f.counterAxisSizingMode = "AUTO";
      f.fills = [];
      if (props) for (const k in props) f[k] = props[k];
      return f;
    },
  };

  let fn;
  try {
    fn = new Function("figma", "helpers", '"use strict"; return (async () => {\n' + params.code + "\n})();");
  } catch (e) {
    throw new Error(
      "the Figma plugin sandbox rejected dynamic code (" + String((e && e.message) || e) +
      "); use the atomic tools instead",
    );
  }

  let result;
  try {
    result = await fn(figma, helpers);
  } catch (e) {
    throw new Error(scriptFailure(e));
  }
  if (result === undefined) return { ok: true };
  // The reply is structured-cloned to the UI; a live node would kill the
  // connection, so force plain data here and fail with a clear message.
  try {
    return JSON.parse(JSON.stringify(result));
  } catch (e) {
    throw new Error("script must return plain data (ids, numbers, strings, arrays), not Figma objects");
  }
}

// ---------- prototyping ----------

const TRIGGERS = ["ON_CLICK", "ON_HOVER", "ON_PRESS", "ON_DRAG"];
const NAVIGATIONS = ["NAVIGATE", "SWAP", "OVERLAY", "BACK", "CLOSE"];
// Directional transitions carry a direction; the rest only easing/duration.
const DIRECTIONAL_TRANSITIONS = ["MOVE_IN", "MOVE_OUT", "PUSH", "SLIDE_IN", "SLIDE_OUT"];
const SIMPLE_TRANSITIONS = ["DISSOLVE", "SMART_ANIMATE", "SCROLL_ANIMATE"];

function buildTransition(item) {
  const type = (item.transition || "").toUpperCase();
  if (!type || type === "NONE" || type === "INSTANT") return null;
  const directional = DIRECTIONAL_TRANSITIONS.indexOf(type) !== -1;
  if (!directional && SIMPLE_TRANSITIONS.indexOf(type) === -1) {
    throw new Error(
      "unsupported transition: " + type + " (use " +
      SIMPLE_TRANSITIONS.concat(DIRECTIONAL_TRANSITIONS).join(", ") + ")",
    );
  }
  const t = {
    type,
    easing: { type: (item.easing || "EASE_OUT").toUpperCase() },
    duration: item.duration === undefined || item.duration === null ? 0.3 : item.duration,
  };
  if (directional) {
    t.direction = (item.direction || "LEFT").toUpperCase();
    t.matchLayers = false;
  }
  return t;
}

async function buildReaction(item) {
  const trigger = (item.trigger || "ON_CLICK").toUpperCase();
  if (TRIGGERS.indexOf(trigger) === -1) {
    throw new Error("unsupported trigger: " + trigger + " (use " + TRIGGERS.join(", ") + ")");
  }
  const navigation = (item.navigation || "NAVIGATE").toUpperCase();
  if (NAVIGATIONS.indexOf(navigation) === -1) {
    throw new Error("unsupported navigation: " + navigation + " (use " + NAVIGATIONS.join(", ") + ")");
  }
  const needsDestination = navigation !== "BACK" && navigation !== "CLOSE";
  if (needsDestination && !item.destination_id) {
    throw new Error("destination_id is required for " + navigation + "; only BACK and CLOSE take none");
  }
  // BACK and CLOSE are action types of their own, not navigations of a NODE
  // action; sending them as a navigation fails validation.
  if (!needsDestination) {
    return { trigger: { type: trigger }, actions: [{ type: navigation }] };
  }
  // Resolve the destination here so a stale id fails by name, not as an
  // opaque setReactionsAsync rejection.
  await node(item.destination_id);
  return {
    trigger: { type: trigger },
    actions: [{
      type: "NODE",
      destinationId: item.destination_id,
      navigation,
      transition: buildTransition(item),
      preserveScrollPosition: false,
    }],
  };
}

// The page owning a node; flowStartingPoints lives on the page, which is not
// necessarily the open one.
function pageOf(n) {
  while (n && n.type !== "PAGE") n = n.parent;
  if (!n) throw new Error("node is not on a page");
  return n;
}

// ---------- command handlers (names match MCP tool names) ----------

const handlers = {
  async get_metadata() {
    return {
      file: figma.root.name,
      currentPage: { id: figma.currentPage.id, name: figma.currentPage.name },
      children: figma.currentPage.children.map(summarize),
    };
  },

  async list_pages() {
    return {
      currentPage: figma.currentPage.name,
      pages: figma.root.children.map((p) => ({
        id: p.id,
        name: p.name,
        current: p.id === figma.currentPage.id,
      })),
    };
  },

  async set_current_page(params) {
    const p = await resolvePage(params.page);
    await figma.setCurrentPageAsync(p);
    return { id: p.id, name: p.name, childCount: p.children.length };
  },

  async get_selection() {
    return figma.currentPage.selection.map((n) => {
      const s = summarize(n);
      const link = nodeLink(n.id);
      if (link) s.link = link;
      return s;
    });
  },

  async get_design_context(params) {
    const depth = params.depth === undefined ? 2 : params.depth;
    return serialize(await node(params.node_id), depth, !!params.compact);
  },

  async create_frame(params) {
    const f = figma.createFrame();
    // Figma gives new frames an opaque white fill, which silently covers
    // whatever sits behind a nested frame. Transparent unless asked.
    if (!params.fill_color || params.fill_color === "none") f.fills = [];
    applyShapeParams(f, params);
    (await parentOf(params)).appendChild(f);
    return summarize(f);
  },

  async create_rectangle(params) {
    const r = figma.createRectangle();
    applyShapeParams(r, params);
    (await parentOf(params)).appendChild(r);
    return summarize(r);
  },

  async create_ellipse(params) {
    const e = figma.createEllipse();
    applyShapeParams(e, params);
    (await parentOf(params)).appendChild(e);
    return summarize(e);
  },

  async create_line(params) {
    const l = figma.createLine();
    l.x = params.x;
    l.y = params.y;
    l.resize(params.length > 0 ? params.length : 100, 0);
    l.strokes = [{ type: "SOLID", color: hexToRGB(params.stroke_color || "#000000") }];
    if (params.stroke_weight) l.strokeWeight = params.stroke_weight;
    if (params.rotation !== undefined && params.rotation !== null) l.rotation = params.rotation;
    if (params.name) l.name = params.name;
    (await parentOf(params)).appendChild(l);
    return summarize(l);
  },

  async import_image(params) {
    const image = figma.createImage(figma.base64Decode(params.data));
    const paint = { type: "IMAGE", imageHash: image.hash, scaleMode: params.scale_mode || "FILL" };
    let n;
    if (params.node_id) {
      n = await node(params.node_id);
      if (!("fills" in n)) throw new Error("node " + params.node_id + " has no fills");
      n.fills = [paint];
    } else {
      const size = await image.getSizeAsync();
      n = figma.createRectangle();
      n.x = params.x || 0;
      n.y = params.y || 0;
      n.resize(params.width > 0 ? params.width : size.width, params.height > 0 ? params.height : size.height);
      if (params.name) n.name = params.name;
      n.fills = [paint];
      (await parentOf(params)).appendChild(n);
    }
    return summarize(n);
  },

  async create_text(params) {
    const font = { family: params.font_family || "Inter", style: params.font_style || "Regular" };
    await figma.loadFontAsync(font);
    const t = figma.createText();
    t.fontName = font;
    t.characters = params.text;
    t.x = params.x;
    t.y = params.y;
    if (params.font_size) t.fontSize = params.font_size;
    if (params.fill_color) t.fills = [{ type: "SOLID", color: hexToRGB(params.fill_color) }];
    if (params.line_height) t.lineHeight = { value: params.line_height, unit: "PIXELS" };
    if (params.letter_spacing !== undefined && params.letter_spacing !== null) {
      t.letterSpacing = { value: params.letter_spacing, unit: "PIXELS" };
    }
    if (params.text_align) t.textAlignHorizontal = params.text_align;
    if (params.max_width > 0) {
      t.textAutoResize = "HEIGHT";
      t.resize(params.max_width, t.height);
    }
    if (params.name) t.name = params.name;
    (await parentOf(params)).appendChild(t);
    return summarize(t);
  },

  async set_characters(params) {
    const nodes = await targets(params);
    for (const t of nodes) {
      if (t.type !== "TEXT") throw new Error("node " + t.id + " is " + t.type + ", not TEXT");
      if (t.characters.length > 0) {
        const fonts = t.getRangeAllFontNames(0, t.characters.length);
        await Promise.all(fonts.map(figma.loadFontAsync));
      } else {
        await figma.loadFontAsync(t.fontName);
      }
      t.characters = params.text;
    }
    return done(params, nodes);
  },

  async set_fills(params) {
    const nodes = await targets(params);
    const clear = params.color === "none" || params.color === null;
    let paint = null;
    if (!clear) {
      if (params.gradient) {
        paint = buildGradient(params.gradient);
      } else if (params.color) {
        paint = { type: "SOLID", color: hexToRGB(params.color) };
      } else {
        throw new Error('set_fills needs color, gradient, or color: "none" to clear');
      }
      if (params.opacity !== undefined && params.opacity !== null) paint.opacity = params.opacity;
    }
    for (const n of nodes) {
      if (!("fills" in n)) throw new Error("node " + n.id + " has no fills");
      n.fills = clear ? [] : [paint];
    }
    return done(params, nodes);
  },

  async move_nodes(params) {
    const out = [];
    for (const item of params.items) {
      const n = await node(item.node_id);
      if (!("x" in n)) throw new Error("node " + item.node_id + " cannot be moved");
      n.x = item.x;
      n.y = item.y;
      out.push(n);
    }
    return done(params, out);
  },

  async resize_nodes(params) {
    const out = [];
    for (const item of params.items) {
      const n = await node(item.node_id);
      if (!("resize" in n)) throw new Error("node " + item.node_id + " cannot be resized");
      n.resize(item.width, item.height);
      out.push(n);
    }
    return done(params, out);
  },

  async remove_nodes(params) {
    const deleted = [];
    for (const id of params.node_ids) {
      const n = await node(id);
      deleted.push({ id: n.id, name: n.name, type: n.type });
      n.remove();
    }
    return { deleted };
  },

  async set_auto_layout(params) {
    const n = await node(params.node_id);
    if (!("layoutMode" in n)) throw new Error("node " + params.node_id + " does not support auto-layout");
    n.layoutMode = params.layout_mode;
    if (params.layout_mode === "NONE") return done(params, n);
    if (params.item_spacing !== undefined) n.itemSpacing = params.item_spacing;
    if (params.padding !== undefined) {
      n.paddingTop = n.paddingRight = n.paddingBottom = n.paddingLeft = params.padding;
    }
    if (params.padding_top !== undefined) n.paddingTop = params.padding_top;
    if (params.padding_right !== undefined) n.paddingRight = params.padding_right;
    if (params.padding_bottom !== undefined) n.paddingBottom = params.padding_bottom;
    if (params.padding_left !== undefined) n.paddingLeft = params.padding_left;
    if (params.primary_axis_align) n.primaryAxisAlignItems = params.primary_axis_align;
    if (params.counter_axis_align) n.counterAxisAlignItems = params.counter_axis_align;
    if (params.primary_axis_sizing) n.primaryAxisSizingMode = params.primary_axis_sizing;
    if (params.counter_axis_sizing) n.counterAxisSizingMode = params.counter_axis_sizing;
    return done(params, n);
  },

  async set_corner_radius(params) {
    const nodes = await targets(params);
    for (const n of nodes) {
      if (params.radius !== undefined) {
        if (!("cornerRadius" in n)) throw new Error("node " + n.id + " has no corner radius");
        n.cornerRadius = params.radius;
      }
      if (!("topLeftRadius" in n)) {
        if (params.radius === undefined) throw new Error("node " + n.id + " has no per-corner radius");
        continue;
      }
      if (params.top_left !== undefined) n.topLeftRadius = params.top_left;
      if (params.top_right !== undefined) n.topRightRadius = params.top_right;
      if (params.bottom_right !== undefined) n.bottomRightRadius = params.bottom_right;
      if (params.bottom_left !== undefined) n.bottomLeftRadius = params.bottom_left;
    }
    return done(params, nodes);
  },

  async set_strokes(params) {
    const nodes = await targets(params);
    for (const n of nodes) {
      if (!("strokes" in n)) throw new Error("node " + n.id + " has no strokes");
      if (params.color && params.color !== "none") {
        const paint = { type: "SOLID", color: hexToRGB(params.color) };
        if (params.opacity !== undefined && params.opacity !== null) paint.opacity = params.opacity;
        n.strokes = [paint];
      } else {
        n.strokes = [];
      }
      if (params.weight !== undefined && "strokeWeight" in n) n.strokeWeight = params.weight;
      if (params.align && "strokeAlign" in n) n.strokeAlign = params.align;
    }
    return done(params, nodes);
  },

  async set_effects(params) {
    const nodes = await targets(params);
    const effects = buildEffects(params.effects);
    for (const n of nodes) {
      if (!("effects" in n)) throw new Error("node " + n.id + " has no effects");
      n.effects = effects;
    }
    return done(params, nodes);
  },

  async rename_nodes(params) {
    const renamed = [];
    for (const item of params.items) {
      const n = await node(item.node_id);
      n.name = item.name;
      renamed.push(n);
    }
    return done(params, renamed);
  },

  // Restyle text in place. create_text sets the font once; variants (Bold vs
  // Regular of the same label) need this.
  async set_text_properties(params) {
    const nodes = await targets(params);
    for (const n of nodes) {
      if (n.type !== "TEXT") throw new Error("node " + n.id + " is " + n.type + ", not TEXT");
      // Every font already in the node must be loaded before any edit, and
      // the new one before it can be applied.
      const current = n.characters.length
        ? n.getRangeAllFontNames(0, n.characters.length)
        : [n.fontName];
      await Promise.all(current.filter((f) => f !== figma.mixed).map(figma.loadFontAsync));
      if (params.font_family || params.font_style) {
        const base = current[0] === figma.mixed ? { family: "Inter", style: "Regular" } : current[0];
        const font = {
          family: params.font_family || base.family,
          style: params.font_style || base.style,
        };
        await figma.loadFontAsync(font);
        n.fontName = font;
      }
      if (params.font_size) n.fontSize = params.font_size;
      if (params.line_height !== undefined && params.line_height !== null) {
        n.lineHeight = { value: params.line_height, unit: "PIXELS" };
      }
      if (params.letter_spacing !== undefined && params.letter_spacing !== null) {
        n.letterSpacing = { value: params.letter_spacing, unit: "PIXELS" };
      }
      if (params.text_align) n.textAlignHorizontal = params.text_align;
      if (params.fill_color) n.fills = [{ type: "SOLID", color: hexToRGB(params.fill_color) }];
    }
    return done(params, nodes);
  },

  async download_assets(params) {
    const out = [];
    // One bad item (stale node id, un-exportable type) must not sink the
    // whole batch — report it in place and keep exporting the rest.
    for (const item of params.items) {
      try {
        const target = await node(item.node_id);
        if (!("exportAsync" in target)) throw new Error("node " + item.node_id + " cannot be exported");
        const format = (item.format || "PNG").toUpperCase();
        let settings;
        if (format === "SVG") {
          settings = { format: "SVG" };
        } else if (format === "PNG" || format === "JPG") {
          let scale = item.scale || 1;
          scale = Math.max(0.5, Math.min(scale, 4));
          settings = { format, constraint: { type: "SCALE", value: scale } };
        } else {
          throw new Error("unsupported format: " + format + " (use PNG, JPG or SVG)");
        }
        const bytes = await target.exportAsync(settings);
        out.push({
          data: figma.base64Encode(bytes),
          format,
          name: target.name,
          width: "width" in target ? target.width : 0,
          height: "height" in target ? target.height : 0,
        });
      } catch (e) {
        out.push({ error: String((e && e.message) || e) });
      }
    }
    return out;
  },

  async set_selection(params) {
    const nodes = await Promise.all(params.node_ids.map(node));
    figma.currentPage.selection = nodes;
    figma.viewport.scrollAndZoomIntoView(nodes);
    return nodes.map(summarize);
  },

  async find_nodes(params) {
    let scope;
    if (params.node_id) scope = await node(params.node_id);
    else if (params.page) scope = await resolvePage(params.page);
    else scope = figma.currentPage;
    if (!("findAll" in scope)) throw new Error("node " + scope.id + " has no children to search");
    const name = params.name ? params.name.toLowerCase() : null;
    const text = params.text ? params.text.toLowerCase() : null;
    const types = params.types && params.types.length ? params.types : null;
    // Style filters let callers target "every white card" directly instead of
    // guessing a node's role from its size.
    const fill = params.fill ? ("#" + params.fill.replace(/^#/, "")).toLowerCase() : null;
    const fontSize = params.font_size;
    const fontStyle = params.font_style ? params.font_style.toLowerCase() : null;
    const matches = scope.findAll((n) => {
      if (types && types.indexOf(n.type) === -1) return false;
      if (name && n.name.toLowerCase().indexOf(name) === -1) return false;
      if (text && (n.type !== "TEXT" || n.characters.toLowerCase().indexOf(text) === -1)) return false;
      if (fill) {
        const hex = solidFillHex(n);
        if (!hex || hex.toLowerCase() !== fill) return false;
      }
      if (fontSize !== undefined && fontSize !== null) {
        if (n.type !== "TEXT" || val(n.fontSize) !== fontSize) return false;
      }
      if (fontStyle) {
        if (n.type !== "TEXT") return false;
        const f = val(n.fontName);
        if (f === "mixed" || f.style.toLowerCase() !== fontStyle) return false;
      }
      return true;
    });
    const max = params.max_results || 50;
    const fields = params.fields && params.fields.length ? params.fields : null;
    // Name the scope: with no node_id or page this searches whatever page the
    // user happens to have open, which silently changes under a long session.
    const page = (function (n) {
      while (n && n.type !== "PAGE") n = n.parent;
      return n ? n.name : undefined;
    })(scope);
    return {
      scope: scope.type === "PAGE" ? "page " + scope.name : scope.name + " (page " + page + ")",
      total: matches.length,
      truncated: matches.length > max,
      nodes: matches.slice(0, max).map((n) => {
        const s = summarize(n);
        for (const f of fields || []) {
          const v = FIELD_GETTERS[f] ? FIELD_GETTERS[f](n) : undefined;
          if (v !== undefined) s[f] = v;
        }
        return s;
      }),
    };
  },

  async group_nodes(params) {
    const nodes = await Promise.all(params.node_ids.map(node));
    const group = figma.group(nodes, nodes[0].parent);
    if (params.name) group.name = params.name;
    return summarize(group);
  },

  async ungroup_nodes(params) {
    const released = [];
    for (const id of params.node_ids) {
      const n = await node(id);
      if (n.type !== "GROUP" && n.type !== "FRAME") {
        throw new Error("node " + id + " is " + n.type + ", not a GROUP or FRAME");
      }
      released.push(...figma.ungroup(n).map(summarize));
    }
    return { released };
  },

  async append_children(params) {
    const parent = await node(params.parent_id);
    if (!("appendChild" in parent)) throw new Error("node " + params.parent_id + " cannot have children");
    const moved = [];
    const warnings = [];
    let index = params.index;
    for (const id of params.node_ids) {
      const n = await node(id);
      // Figma silently pulls a variant out of its set and renames it
      // "<Set>/<Variant>" — easy to miss until the variants stop working.
      if (n.type === "COMPONENT" && n.parent && n.parent.type === "COMPONENT_SET") {
        warnings.push(
          "node " + n.id + " (" + n.name + ") left component set " + n.parent.name +
          "; Figma renames detached variants to <Set>/<Variant>",
        );
      }
      if (index === undefined || index === null) {
        parent.appendChild(n);
      } else {
        parent.insertChild(index++, n);
      }
      moved.push(n);
    }
    return done(params, moved, warnings.length ? { warnings } : null);
  },

  async clone_node(params) {
    const n = await node(params.node_id);
    if (!("clone" in n)) throw new Error("node " + params.node_id + " cannot be cloned");
    const copy = n.clone();
    if (params.parent_id) {
      const p = await node(params.parent_id);
      if (!("appendChild" in p)) throw new Error("node " + params.parent_id + " cannot have children");
      p.appendChild(copy);
    }
    if (params.x !== undefined) copy.x = params.x;
    if (params.y !== undefined) copy.y = params.y;
    if (params.name) copy.name = params.name;
    return summarize(copy);
  },

  async list_available_fonts(params) {
    const fonts = await figma.listAvailableFontsAsync();
    const filter = params.family ? params.family.toLowerCase() : null;
    const byFamily = {};
    const order = [];
    for (const f of fonts) {
      const fam = f.fontName.family;
      if (filter && fam.toLowerCase().indexOf(filter) === -1) continue;
      if (!byFamily[fam]) {
        byFamily[fam] = [];
        order.push(fam);
      }
      byFamily[fam].push(f.fontName.style);
    }
    const max = params.max_families || 50;
    return {
      total: order.length,
      truncated: order.length > max,
      families: order.slice(0, max).map((fam) => ({ family: fam, styles: byFamily[fam] })),
    };
  },

  // ---------- components ----------

  async create_component(params) {
    const n = await node(params.node_id);
    if (n.type !== "FRAME") throw new Error("node " + params.node_id + " is " + n.type + ", not a FRAME");
    return summarize(figma.createComponentFromNode(n));
  },

  async get_local_components() {
    await figma.loadAllPagesAsync();
    const comps = figma.root.findAllWithCriteria({ types: ["COMPONENT", "COMPONENT_SET"] });
    return comps.map((c) => ({
      id: c.id,
      name: c.name,
      type: c.type,
      description: c.description || "",
      page: c.parent && c.parent.type === "PAGE" ? c.parent.name : undefined,
    }));
  },

  async create_instance(params) {
    const c = await node(params.component_id);
    if (c.type !== "COMPONENT") throw new Error("node " + params.component_id + " is " + c.type + ", not a COMPONENT");
    const inst = c.createInstance();
    if (params.x !== undefined) inst.x = params.x;
    if (params.y !== undefined) inst.y = params.y;
    (await parentOf(params)).appendChild(inst);
    return summarize(inst);
  },

  async combine_as_variants(params) {
    const nodes = await Promise.all(params.node_ids.map(node));
    for (const n of nodes) {
      if (n.type !== "COMPONENT") throw new Error("node " + n.id + " is " + n.type + ", not a COMPONENT");
    }
    const parent = params.parent_id ? await node(params.parent_id) : nodes[0].parent;
    const set = figma.combineAsVariants(nodes, parent);
    if (params.name) set.name = params.name;
    arrangeVariants(set);
    return summarize(set);
  },

  // Component properties live on the component (or set); binding the property
  // to a child layer is what makes it editable from the instance panel, so do
  // both in one call.
  async add_component_property(params) {
    const c = await node(params.component_id);
    if (c.type !== "COMPONENT" && c.type !== "COMPONENT_SET") {
      throw new Error("node " + c.id + " is " + c.type + ", not a COMPONENT or COMPONENT_SET");
    }
    const type = (params.type || "TEXT").toUpperCase();
    // Figma requires a default of the matching kind; pick the empty one
    // rather than failing a call that only omitted it.
    let def = params.default_value;
    if (def === undefined || def === null) def = type === "BOOLEAN" ? false : "";
    const key = c.addComponentProperty(params.name, type, def);
    if (params.bind_node_id) {
      const t = await node(params.bind_node_id);
      const field = type === "TEXT" ? "characters" : type === "BOOLEAN" ? "visible" : "mainComponent";
      const refs = {};
      for (const k in t.componentPropertyReferences || {}) refs[k] = t.componentPropertyReferences[k];
      refs[field] = key;
      t.componentPropertyReferences = refs;
    }
    return { key, name: params.name, type, boundTo: params.bind_node_id || null };
  },

  async set_instance_properties(params) {
    const inst = await node(params.instance_id);
    if (inst.type !== "INSTANCE") throw new Error("node " + params.instance_id + " is " + inst.type + ", not an INSTANCE");
    const known = Object.keys(inst.componentProperties || {});
    for (const k in params.properties) {
      if (known.indexOf(k) === -1) {
        throw new Error("unknown property " + k + "; this instance accepts: " + (known.join(", ") || "(none)"));
      }
    }
    inst.setProperties(params.properties);
    return { id: inst.id, properties: inst.componentProperties };
  },

  async swap_component(params) {
    const inst = await node(params.instance_id);
    if (inst.type !== "INSTANCE") throw new Error("node " + params.instance_id + " is " + inst.type + ", not an INSTANCE");
    const c = await node(params.component_id);
    if (c.type !== "COMPONENT") throw new Error("node " + params.component_id + " is " + c.type + ", not a COMPONENT");
    inst.swapComponent(c);
    return summarize(inst);
  },

  async detach_instance(params) {
    const inst = await node(params.instance_id);
    if (inst.type !== "INSTANCE") throw new Error("node " + params.instance_id + " is " + inst.type + ", not an INSTANCE");
    return summarize(inst.detachInstance());
  },

  // ---------- styles ----------

  async get_local_styles() {
    const [paint, text, effect, grid] = await Promise.all([
      figma.getLocalPaintStylesAsync(),
      figma.getLocalTextStylesAsync(),
      figma.getLocalEffectStylesAsync(),
      figma.getLocalGridStylesAsync(),
    ]);
    const brief = (s) => ({ id: s.id, name: s.name });
    return {
      paint: paint.map(brief),
      text: text.map(brief),
      effect: effect.map(brief),
      grid: grid.map(brief),
    };
  },

  async create_paint_style(params) {
    const s = figma.createPaintStyle();
    s.name = params.name;
    const paint = { type: "SOLID", color: hexToRGB(params.color) };
    if (params.opacity !== undefined && params.opacity !== null) paint.opacity = params.opacity;
    s.paints = [paint];
    return { id: s.id, name: s.name };
  },

  async create_text_style(params) {
    const font = { family: params.font_family || "Inter", style: params.font_style || "Regular" };
    await figma.loadFontAsync(font);
    const s = figma.createTextStyle();
    s.name = params.name;
    s.fontName = font;
    if (params.font_size) s.fontSize = params.font_size;
    return { id: s.id, name: s.name };
  },

  async create_effect_style(params) {
    const s = figma.createEffectStyle();
    s.name = params.name;
    s.effects = buildEffects(params.effects);
    return { id: s.id, name: s.name };
  },

  async apply_style(params) {
    const nodes = await targets(params);
    const style = await figma.getStyleByIdAsync(params.style_id);
    if (!style) throw new Error("style not found: " + params.style_id);
    if (style.type === "TEXT") await figma.loadFontAsync(style.fontName);
    for (const n of nodes) {
      if (style.type === "PAINT") {
        if (params.target === "stroke") {
          await n.setStrokeStyleIdAsync(style.id);
        } else {
          await n.setFillStyleIdAsync(style.id);
        }
      } else if (style.type === "TEXT") {
        if (n.type !== "TEXT") throw new Error("node " + n.id + " is " + n.type + ", not TEXT");
        await n.setTextStyleIdAsync(style.id);
      } else if (style.type === "EFFECT") {
        await n.setEffectStyleIdAsync(style.id);
      } else if (style.type === "GRID") {
        await n.setGridStyleIdAsync(style.id);
      } else {
        throw new Error("unsupported style type: " + style.type);
      }
    }
    return done(params, nodes);
  },

  // ---------- variables (design tokens) ----------

  async get_variable_defs() {
    const [collections, variables] = await Promise.all([
      figma.variables.getLocalVariableCollectionsAsync(),
      figma.variables.getLocalVariablesAsync(),
    ]);
    return {
      collections: collections.map((c) => ({
        id: c.id,
        name: c.name,
        defaultModeId: c.defaultModeId,
        modes: c.modes.map((m) => ({ modeId: m.modeId, name: m.name })),
      })),
      variables: variables.map((v) => ({
        id: v.id,
        name: v.name,
        resolvedType: v.resolvedType,
        collectionId: v.variableCollectionId,
        valuesByMode: v.valuesByMode,
      })),
    };
  },

  async create_variable_collection(params) {
    const c = figma.variables.createVariableCollection(params.name);
    if (params.mode_name) c.renameMode(c.defaultModeId, params.mode_name);
    return {
      id: c.id,
      name: c.name,
      defaultModeId: c.defaultModeId,
      modes: c.modes.map((m) => ({ modeId: m.modeId, name: m.name })),
    };
  },

  async add_variable_mode(params) {
    const c = await figma.variables.getVariableCollectionByIdAsync(params.collection_id);
    if (!c) throw new Error("variable collection not found: " + params.collection_id);
    const modeId = c.addMode(params.name);
    return { collectionId: c.id, modeId, name: params.name };
  },

  async create_variable(params) {
    const c = await figma.variables.getVariableCollectionByIdAsync(params.collection_id);
    if (!c) throw new Error("variable collection not found: " + params.collection_id);
    const v = figma.variables.createVariable(params.name, c, params.type);
    if (params.value !== undefined && params.value !== null) {
      v.setValueForMode(c.defaultModeId, variableValue(v, params.value));
    }
    return { id: v.id, name: v.name, resolvedType: v.resolvedType, collectionId: c.id };
  },

  async set_variable_value(params) {
    const v = await figma.variables.getVariableByIdAsync(params.variable_id);
    if (!v) throw new Error("variable not found: " + params.variable_id);
    const c = await figma.variables.getVariableCollectionByIdAsync(v.variableCollectionId);
    const modeId = params.mode_id || c.defaultModeId;
    v.setValueForMode(modeId, variableValue(v, params.value));
    return { id: v.id, name: v.name, modeId, valuesByMode: v.valuesByMode };
  },

  async set_bound_variable(params) {
    const nodes = await targets(params);
    const v = await figma.variables.getVariableByIdAsync(params.variable_id);
    if (!v) throw new Error("variable not found: " + params.variable_id);
    const field = params.field;
    for (const n of nodes) {
      if (field === "fills" || field === "strokes") {
        if (!(field in n)) throw new Error("node " + n.id + " has no " + field);
        const paints = n[field].length ? n[field] : [{ type: "SOLID", color: { r: 0, g: 0, b: 0 } }];
        n[field] = [figma.variables.setBoundVariableForPaint(paints[0], "color", v)].concat(paints.slice(1));
      } else {
        n.setBoundVariable(field, v);
      }
    }
    return done(params, nodes);
  },

  // ---------- prototyping ----------

  // setReactionsAsync replaces a node's reactions wholesale, so items for the
  // same node are grouped and written once instead of overwriting each other.
  async set_prototype_links(params) {
    const items = params.items || [];
    if (!items.length) throw new Error("items is required");
    const order = [];
    const groups = {};
    for (const item of items) {
      if (!item.node_id) throw new Error("node_id is required for every item");
      if (!groups[item.node_id]) {
        groups[item.node_id] = [];
        order.push(item.node_id);
      }
      groups[item.node_id].push(await buildReaction(item));
    }
    const out = [];
    for (const id of order) {
      const n = await node(id);
      if (!("setReactionsAsync" in n)) {
        throw new Error("node " + id + " is " + n.type + " and cannot carry prototype reactions");
      }
      await n.setReactionsAsync(groups[id]);
      out.push(n);
    }
    return done(params, out);
  },

  async get_prototype_links(params) {
    const scope = params.node_id ? await node(params.node_id) : figma.currentPage;
    const withReactions = [];
    const has = (n) => "reactions" in n && n.reactions && n.reactions.length;
    if (has(scope)) withReactions.push(scope);
    if ("findAll" in scope) withReactions.push(...scope.findAll(has));
    const names = {};
    const nameOf = async (id) => {
      if (!(id in names)) {
        const d = await figma.getNodeByIdAsync(id);
        names[id] = d ? d.name : null;
      }
      return names[id];
    };
    const links = [];
    for (const n of withReactions) {
      for (const r of n.reactions) {
        for (const a of r.actions || []) {
          const link = { id: n.id, name: n.name, trigger: r.trigger ? r.trigger.type : null, action: a.type };
          if (a.navigation) link.navigation = a.navigation;
          if (a.url) link.url = a.url;
          if (a.destinationId) {
            link.destinationId = a.destinationId;
            const dn = await nameOf(a.destinationId);
            if (dn) link.destinationName = dn;
          }
          if (a.transition) link.transition = a.transition.type;
          links.push(link);
        }
      }
    }
    return {
      scope: scope.type === "PAGE" ? "page " + scope.name : scope.name,
      total: links.length,
      links,
    };
  },

  // flowStartingPoints is a page-level array, so read-modify-write it.
  async set_flow_starting_point(params) {
    const n = await node(params.node_id);
    const page = pageOf(n);
    const kept = page.flowStartingPoints.filter((p) => p.nodeId !== n.id);
    const changed = params.remove
      ? kept.length !== page.flowStartingPoints.length
      : true;
    page.flowStartingPoints = params.remove
      ? kept
      : kept.concat([{ nodeId: n.id, name: params.name || n.name }]);
    return {
      ok: true,
      id: n.id,
      page: page.name,
      changed,
      flowStartingPoints: page.flowStartingPoints.map((p) => ({ nodeId: p.nodeId, name: p.name })),
    };
  },

  // Escape hatch for bulk work: one call instead of hundreds of atomic ones.
  // Figma's plugin sandbox may refuse dynamic code entirely; when it does, say
  // so plainly rather than failing with a bare ReferenceError.
  async run_script(params) {
    if (typeof params.code !== "string" || !params.code.trim()) throw new Error("code is required");
    const token = typeof params.token === "string" && params.token ? params.token : null;
    if (token && scriptRuns.has(token)) return scriptRuns.get(token);
    const run = runScriptBody(params);
    if (token) rememberRun(token, run);
    return run;
  },

  async get_screenshot(params) {
    let target;
    if (params.node_id) {
      target = await node(params.node_id);
    } else if (figma.currentPage.selection.length === 1) {
      target = figma.currentPage.selection[0];
    } else {
      target = figma.currentPage;
    }
    if (!("exportAsync" in target)) throw new Error("node " + target.id + " cannot be exported");
    // With dynamic-page access a node can be reachable before its page is
    // loaded, and exporting then yields a blank image with no error.
    let owner = target;
    while (owner && owner.type !== "PAGE") owner = owner.parent;
    if (owner && typeof owner.loadAsync === "function") await owner.loadAsync();
    const requested = Math.max(0.5, Math.min(params.scale || 1, 4));
    let scale = requested;
    // Cap the output size: huge pages at high scale produce multi-MB base64
    // payloads that are slow to ship and waste the AI's context.
    const cap = params.max_dimension > 0 ? Math.min(params.max_dimension, 4096) : 2000;
    const maxDim = Math.max("width" in target ? target.width : 0, "height" in target ? target.height : 0);
    const clamped = maxDim * scale > cap;
    if (clamped) scale = cap / maxDim;
    const bytes = await target.exportAsync({ format: "PNG", constraint: { type: "SCALE", value: scale } });
    return {
      data: figma.base64Encode(bytes),
      width: ("width" in target ? target.width : 0) * scale,
      height: ("height" in target ? target.height : 0) * scale,
      scale,
      clamped,
      sourceHeight: "height" in target ? target.height : 0,
    };
  },
};

// ---------- live selection panel ----------
// Mirror the current selection into the plugin window so the user can grab a
// frame's name and shareable link without going through the AI. Refreshes on
// every selection change and resizes the window to fit the list.
// selectionGen guards against stale posts: SVG export is async, so a newer
// selection change may land while an older one is still exporting.
let selectionGen = 0;

async function postSelection() {
  const gen = ++selectionGen;
  const sel = figma.currentPage.selection;
  const items = [];
  for (const n of sel) {
    const item = { name: n.name, type: n.type, link: nodeLink(n.id) };
    // Frames are copied as links; every other element (vectors, instances, …)
    // is copied as SVG, so pre-export it now to keep the UI copy synchronous
    // (clipboard writes must happen inside the click gesture). Skip huge
    // selections to avoid stalling on a big export the user won't copy.
    if (n.type !== "FRAME" && sel.length <= 50 && typeof n.exportAsync === "function") {
      try {
        item.svg = await n.exportAsync({ format: "SVG_STRING" });
      } catch (e) {
        item.svgError = String((e && e.message) || e);
      }
      if (gen !== selectionGen) return; // superseded by a newer selection
    }
    items.push(item);
  }
  const needsKey = !fileKey && items.some((it) => it.type === "FRAME");
  figma.ui.postMessage({ selection: items, hasKey: !!fileKey });
  // Room for: base card + filter bar (30) + optional Copy-all header (22) +
  // the paste-URL prompt when a frame needs a key (64) + up to 6 visible rows
  // (the list scrolls beyond that).
  const rows = Math.min(items.length, 6);
  const h = items.length
    ? 134 + (items.length > 1 ? 22 : 0) + (needsKey ? 64 : 0) + rows * 30
    : 104;
  figma.ui.resize(280, h);
}
figma.on("selectionchange", postSelection);
postSelection();

// ---------- dispatch ----------

figma.ui.onmessage = async (msg) => {
  if (!msg) return;
  // Control message from the UI: the user pasted the file URL to enable links.
  if (typeof msg.setFileKey === "string") {
    setFileKey(msg.setFileKey);
    postSelection();
    return;
  }
  if (!msg.id || !msg.command) return;
  const h = handlers[msg.command];
  try {
    if (!h) throw new Error("unknown command: " + msg.command);
    const result = await h(msg.params || {});
    figma.ui.postMessage({ id: msg.id, result: result === undefined ? null : result });
  } catch (e) {
    figma.ui.postMessage({ id: msg.id, error: String((e && e.message) || e) });
  }
};
