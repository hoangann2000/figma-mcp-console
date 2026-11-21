// Figma MCP Console — plugin main thread.
// Receives {id, command, params} from the Go bridge (relayed through ui.html),
// executes it against the Figma Plugin API, and replies {id, result|error}.

figma.showUI(__html__, { width: 280, height: 104 });

// Tell the UI which file this plugin window runs in; the UI forwards it to
// the bridge on every (re)connect so calls can be routed per file when
// several files run the plugin at once. figma.fileKey needs
// enablePrivatePluginApi in the manifest, so the file name stays the fallback.
// readOnly rides along so AI sees it in list_files before it plans an edit.
const register = () =>
  figma.ui.postMessage({ register: { key: figma.fileKey || undefined, name: figma.root.name, read_only: readOnly } });

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
  let p = pages.find((x) => x.id === want) || pages.find((x) => x.name.toLowerCase() === lower);
  if (!p) {
    const partial = pages.filter((x) => x.name.toLowerCase().indexOf(lower) !== -1);
    if (partial.length > 1) {
      throw new Error("page " + ref + " is ambiguous: " + partial.map((x) => x.name).join(", "));
    }
    p = partial[0];
  }
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

// The file key needed to build share links. Development plugins only get
// figma.fileKey because the manifest sets enablePrivatePluginApi.
const fileKey = figma.fileKey || "";

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
// visibleOnly drops hidden layers (and their subtrees): QC scripts would
// otherwise report them as missing on the web.
function serialize(n, depth, compact, visibleOnly) {
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
  // Sizes auto layout computes (fill, hug) and layers pinned outside the flow,
  // so diff_design reports only what the designer set. FIXED is the default, left out.
  try {
    if (n.layoutSizingHorizontal && n.layoutSizingHorizontal !== "FIXED") s.layoutSizingHorizontal = n.layoutSizingHorizontal;
    if (n.layoutSizingVertical && n.layoutSizingVertical !== "FIXED") s.layoutSizingVertical = n.layoutSizingVertical;
    if (n.layoutPositioning === "ABSOLUTE") s.layoutPositioning = "ABSOLUTE";
  } catch (e) {}
  // Designer notes and variant values; each getter can throw on node types or
  // plans that lack it, which must not sink the whole tree.
  try {
    if ("annotations" in n && n.annotations.length) {
      s.annotations = n.annotations.map((a) => ({
        label: a.labelMarkdown || a.label || "",
        properties: (a.properties || []).map((p) => p.type),
      }));
    }
  } catch (e) {}
  try {
    if ((n.type === "INSTANCE" || n.type === "COMPONENT") && n.variantProperties) {
      s.variant = n.variantProperties;
    }
  } catch (e) {}
  if (n.type === "COMPONENT_SET") {
    const opts = variantOptions(n);
    if (opts) s.variantOptions = opts;
  }
  // Dashed vs solid borders, and the tokens/styles behind the raw values: ids
  // here, swapped for names by resolveRefs once the (sync) walk is done.
  if ("dashPattern" in n && n.dashPattern.length) s.dashPattern = n.dashPattern;
  try {
    if (n.boundVariables && Object.keys(n.boundVariables).length) s.boundVariables = Object.assign({}, n.boundVariables); // a copy: resolveRefs rewrites it
  } catch (e) {}
  for (const k of STYLE_KEYS) {
    if (k in n && typeof n[k] === "string" && n[k]) s[k] = n[k];
  }
  // Text mixing fonts, sizes or colors (one bold word, a red asterisk) reads
  // as "mixed" above; list the runs so nothing hides behind it.
  if (n.type === "TEXT" && [n.fontName, n.fontSize, n.fills, n.textDecoration].includes(figma.mixed)) {
    try {
      s.segments = n.getStyledTextSegments(["fontName", "fontSize", "fills", "textDecoration"]).map((g) => {
        const seg = { text: g.characters, font: g.fontName.family + " " + g.fontName.style, size: g.fontSize, fill: g.fills.map(paintBrief).join(", ") };
        if (g.textDecoration !== "NONE") seg.decoration = g.textDecoration;
        return seg;
      });
    } catch (e) {}
  }
  if ("children" in n) {
    const kids = visibleOnly ? n.children.filter((c) => c.visible !== false) : n.children;
    if (!compact || kids.length) {
      s.childCount = kids.length;
      if (depth > 0) s.children = kids.map((c) => serialize(c, depth - 1, compact, visibleOnly));
    }
  }
  return s;
}

const STYLE_KEYS = ["fillStyleId", "strokeStyleId", "textStyleId", "effectStyleId"];

// Replace variable and style ids in a serialized tree with their names
// ("color/primary/500", "Heading/H1"), so a reader can match them to tokens.
async function resolveRefs(root) {
  const cache = new Map();
  const name = async (id, style) => {
    if (!cache.has(id)) {
      let x = null;
      try {
        x = style ? await figma.getStyleByIdAsync(id) : await figma.variables.getVariableByIdAsync(id);
      } catch (e) {}
      cache.set(id, x ? x.name : id);
    }
    return cache.get(id);
  };
  const alias = async (v) =>
    Array.isArray(v) ? Promise.all(v.map(alias)) : v && v.type === "VARIABLE_ALIAS" ? name(v.id) : v;
  const walk = async (s) => {
    if (s.boundVariables) {
      for (const k of Object.keys(s.boundVariables)) s.boundVariables[k] = await alias(s.boundVariables[k]);
    }
    for (const k of STYLE_KEYS) {
      if (s[k]) {
        s[k.slice(0, -2)] = await name(s[k], true); // fillStyleId -> fillStyle
        delete s[k];
      }
    }
    if (s.children) for (const c of s.children) await walk(c);
  };
  await walk(root);
  return root;
}

// {property: [values]} for a component set's variant properties, or null.
function variantOptions(set) {
  try {
    const out = {};
    for (const [k, d] of Object.entries(set.componentPropertyDefinitions)) {
      if (d.type === "VARIANT") out[k.replace(/#.*$/, "")] = d.variantOptions || [];
    }
    return Object.keys(out).length ? out : null;
  } catch (e) {
    return null;
  }
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
  n.x = params.x || 0;
  n.y = params.y || 0;
  if (params.width > 0 && params.height > 0) n.resize(params.width, params.height);
  if (params.name) n.name = params.name;
  if (params.fill_color && params.fill_color !== "none") {
    n.fills = [{ type: "SOLID", color: hexToRGB(params.fill_color) }];
  }
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


// Right of everything already on the page, top-aligned with it.
function placeInFreeSpace(n, page) {
  const others = page.children.filter((c) => c !== n && "width" in c);
  if (!others.length) return;
  n.x = Math.max.apply(null, others.map((c) => c.x + c.width)) + 100;
  n.y = Math.min.apply(null, others.map((c) => c.y));
}

// ---------- design audit ----------

const EPS = 1; // sub-pixel noise from auto layout, rotation and vector bounds: never visible

function intersect(a, b) {
  const x = Math.max(a.x, b.x), y = Math.max(a.y, b.y);
  const r = Math.min(a.x + a.width, b.x + b.width), bt = Math.min(a.y + a.height, b.y + b.height);
  return { x, y, width: Math.max(0, r - x), height: Math.max(0, bt - y) };
}

// How far box a sticks out of box b, per side; null when it does not.
function spill(a, b) {
  const o = {
    left: b.x - a.x, top: b.y - a.y,
    right: a.x + a.width - (b.x + b.width), bottom: a.y + a.height - (b.y + b.height),
  };
  const out = {};
  for (const k in o) if (o[k] > EPS) out[k] = Math.round(o[k] * 10) / 10;
  return Object.keys(out).length ? out : null;
}

// Topmost visible paint: an {r,g,b} color when it is an opaque solid, "unknown"
// for images, gradients and translucent fills (no single color to measure
// against), null when the node paints nothing.
function topFill(n) {
  if (!("fills" in n) || !Array.isArray(n.fills)) return null;
  for (let i = n.fills.length - 1; i >= 0; i--) {
    const p = n.fills[i];
    if (p.visible === false) continue;
    if (p.type !== "SOLID" || (p.opacity !== undefined && p.opacity < 1) || ("opacity" in n && n.opacity < 1)) return "unknown";
    return p.color;
  }
  return null;
}

// What a text layer is drawn on: the nearest thing behind it that paints,
// either a sibling lower in the stack that covers it (the hand-drawn
// "rectangle as background") or an ancestor's own fill.
function backgroundOf(t) {
  const box = t.absoluteBoundingBox;
  for (let n = t; n.parent && n.parent.type !== "DOCUMENT"; n = n.parent) {
    const sibs = n.parent.children || [];
    for (let i = sibs.indexOf(n) - 1; i >= 0; i--) {
      const s = sibs[i];
      if (s.visible === false || !s.absoluteBoundingBox || spill(box, s.absoluteBoundingBox)) continue;
      const f = topFill(s);
      if (f) return f;
    }
    const f = n.parent.type === "PAGE" ? null : topFill(n.parent);
    if (f) return f;
  }
  return "unknown";
}

// WCAG 2.x relative luminance and contrast ratio.
function luminance(c) {
  const f = (v) => (v <= 0.03928 ? v / 12.92 : Math.pow((v + 0.055) / 1.055, 2.4));
  return 0.2126 * f(c.r) + 0.7152 * f(c.g) + 0.0722 * f(c.b);
}

function contrastRatio(a, b) {
  const la = luminance(a), lb = luminance(b);
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

// WCAG exempts inactive controls from contrast: text inside an instance whose
// variant says Disabled (State=Disabled, Disabled=true, ...) is not measured.
function inDisabledControl(n) {
  for (let p = n.parent; p && p.type !== "PAGE"; p = p.parent) {
    if (p.type !== "INSTANCE") continue;
    let v = null;
    try {
      v = p.variantProperties;
    } catch (e) {}
    for (const k in v || {}) {
      if (/^(disabled?|inactive)$/i.test(v[k]) || (/disabled/i.test(k) && /^(true|yes|on)$/i.test(v[k]))) return true;
    }
  }
  return false;
}

// Layers a finger is expected to hit: anything wired in the prototype, or
// named like a control.
const CONTROL_NAME = /\b(button|btn|link|tab|chip|checkbox|radio|switch|toggle|icon[-_ ]?button)\b/i;

function isControl(n) {
  if ("reactions" in n && n.reactions && n.reactions.length) return true;
  return (n.type === "INSTANCE" || n.type === "FRAME" || n.type === "COMPONENT") && CONTROL_NAME.test(n.name);
}

// "$2" in a batch op's params stands for the id of the node op 2 produced,
// so a frame and its children can be created in one call.
function resolveBatchRefs(v, results) {
  if (Array.isArray(v)) return v.map((x) => resolveBatchRefs(x, results));
  if (v && typeof v === "object") {
    const out = {};
    for (const k in v) out[k] = resolveBatchRefs(v[k], results);
    return out;
  }
  const m = typeof v === "string" ? /^\$(\d+)$/.exec(v) : null;
  if (!m) return v;
  const r = results[+m[1]];
  if (!r) throw new Error(v + " refers to an op that has not run yet; refs only point backwards");
  const res = r.result || {};
  const id = res.id || (res.ids && res.ids[0]) || (res.nodes && res.nodes[0] && res.nodes[0].id);
  if (!id) throw new Error(v + " has no node id: op " + m[1] + (r.error ? " failed" : " did not return a node"));
  return id;
}

// These carry megabytes of base64 or need the server's filesystem/network, so
// they only work as their own tool call.
const NOT_BATCHABLE = ["batch", "download_assets", "import_image", "get_screenshot"];

// An SVG becomes editable vector layers (createImage takes only PNG, JPG and
// GIF), scaled as a whole so stroke widths keep their proportion, the way a
// designer scales an icon.
async function importSvg(params) {
  if (params.node_id) {
    throw new Error("an SVG becomes layers, not a fill: omit node_id, and pass parent_id to place it inside a node");
  }
  const n = figma.createNodeFromSvg(params.svg);
  n.fills = []; // the wrapper frame; the SVG's own shapes are its children
  if (params.name) n.name = params.name;
  if (params.color) {
    const c = hexToRGB(params.color);
    // Icons draw with fills or strokes (lucide uses strokes); recolor every
    // solid paint and keep its opacity.
    const recolor = (paints) => paints.map((p) => (p.type === "SOLID" ? Object.assign({}, p, { color: c }) : p));
    for (const d of n.findAll(() => true)) {
      if ("fills" in d && Array.isArray(d.fills) && d.fills.length) d.fills = recolor(d.fills);
      if ("strokes" in d && Array.isArray(d.strokes) && d.strokes.length) d.strokes = recolor(d.strokes);
    }
  }
  const w = params.width > 0 ? params.width / n.width : 0;
  const h = params.height > 0 ? params.height / n.height : 0;
  const scale = w && h ? Math.min(w, h) : w || h;
  if (scale && Math.abs(scale - 1) > 1e-6) n.rescale(scale);
  n.x = params.x || 0;
  n.y = params.y || 0;
  (await parentOf(params)).appendChild(n);
  return summarize(n);
}

// Figma's own wording for "could not load that data" blames the network,
// which sends people checking their connection. It shows up when a page that
// was never opened in this session is loaded, and for some ids that do not exist.
function explainError(message) {
  if (/Unable to establish connection to Figma/i.test(message)) {
    return message + " — Figma could not load the data for this request; the network is usually fine. " +
      "If it reads another page, open that page once in Figma (or reopen the file) and retry; " +
      "if it names a node id, check the id with find_nodes.";
  }
  return message;
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

  async create_page(params) {
    if (!params.name) throw new Error("name is required");
    const p = figma.createPage();
    p.name = params.name;
    if (params.index !== undefined && params.index !== null) figma.root.insertChild(params.index, p);
    return { id: p.id, name: p.name, index: figma.root.children.indexOf(p) };
  },

  async rename_page(params) {
    if (!params.name) throw new Error("name is required");
    const p = await resolvePage(params.page);
    const from = p.name;
    p.name = params.name;
    return { id: p.id, from, name: p.name };
  },

  // Figma cannot remove the open page or the last one: open a neighbour first.
  async remove_page(params) {
    const p = await resolvePage(params.page);
    const pages = figma.root.children;
    if (pages.length === 1) throw new Error("page " + p.name + " is the file's only page and cannot be removed");
    if (p.id === figma.currentPage.id) {
      const i = pages.indexOf(p);
      await figma.setCurrentPageAsync(pages[i === 0 ? 1 : i - 1]);
    }
    const out = { removed: { id: p.id, name: p.name, layers: p.children.length }, currentPage: figma.currentPage.name };
    p.remove();
    return out;
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
    return resolveRefs(serialize(await node(params.node_id), depth, !!params.compact, !!params.visible_only));
  },

  // Every component used under node_id, grouped by component (set), with all
  // the states its set defines and the state each instance is in, so testers
  // can write a case per state. Library components resolve too when Figma
  // has their set loaded.
  async get_component_states(params) {
    const root = await node(params.node_id);
    const instances = "findAllWithCriteria" in root ? root.findAllWithCriteria({ types: ["INSTANCE"] }) : [];
    if (root.type === "INSTANCE") instances.unshift(root);
    const groups = new Map();
    for (const inst of instances) {
      if (!inst.visible) continue;
      const main = await inst.getMainComponentAsync();
      if (!main) continue;
      const set = main.parent && main.parent.type === "COMPONENT_SET" ? main.parent : null;
      const owner = set || main;
      let g = groups.get(owner.id);
      if (!g) {
        g = { component: owner.name, id: owner.id, library: !!main.remote, count: 0, used: [] };
        if (set) {
          const opts = variantOptions(set);
          if (opts) g.states = opts;
          else g.statesError = "variant list not readable";
        }
        groups.set(owner.id, g);
      }
      g.count++;
      let state = null;
      try {
        state = inst.variantProperties;
      } catch (e) {}
      if (state) {
        g.usedStates = g.usedStates || {};
        for (const [k, v] of Object.entries(state)) {
          const vals = (g.usedStates[k] = g.usedStates[k] || []);
          if (!vals.includes(v)) vals.push(v);
        }
      }
      if (g.used.length < 10) {
        const u = { id: inst.id, name: inst.name };
        if (state) u.state = state;
        g.used.push(u);
      }
      // Hand edits on top of the component (a "Disabled" button recolored by
      // hand) are what testers miss; list which layer and which fields.
      let ovs = [];
      try {
        ovs = inst.overrides || [];
      } catch (e) {}
      if (ovs.length) {
        g.overridden = (g.overridden || 0) + 1;
        g.overrides = g.overrides || [];
        if (g.overrides.length < 10) {
          const layers = [];
          for (const o of ovs) {
            const n = o.id === inst.id ? inst : await figma.getNodeByIdAsync(o.id);
            layers.push({ layer: n ? n.name : o.id, fields: o.overriddenFields });
          }
          g.overrides.push({ id: inst.id, name: inst.name, layers });
        }
      }
    }
    return [...groups.values()].sort((a, b) => b.count - a.count);
  },

  async create_frame(params) {
    const f = figma.createFrame();
    const parent = await parentOf(params);
    const topLevel = parent.type === "PAGE" || parent.type === "SECTION";
    // A frame inside another is almost always a layout wrapper, and Figma's
    // white default turns every wrapper into a white slab over the design.
    // Top-level frames are screens and keep it.
    if (params.fill_color === "none" || (!params.fill_color && !topLevel)) f.fills = [];
    applyShapeParams(f, params);
    parent.appendChild(f);
    // A screen created without a position goes beside the existing work
    // instead of on top of it at 0,0.
    if (parent.type === "PAGE" && params.x == null && params.y == null) placeInFreeSpace(f, parent);
    return summarize(f);
  },

  // Findings only: what a screenshot shows but a model reading it tends to
  // miss. Reads the rendered geometry, never changes the document.
  async audit_design(params) {
    const root = await node(params.node_id);
    if (!root.absoluteBoundingBox) throw new Error("node " + root.id + " is " + root.type + " and has no box to audit; pass a frame, section or component");
    const want = params.checks && params.checks.length ? params.checks : ["layout", "contrast"];
    for (const c of want) {
      if (["layout", "contrast"].indexOf(c) === -1) throw new Error("unknown check: " + c + " (use layout, contrast)");
    }
    const layout = want.indexOf("layout") !== -1, contrast = want.indexOf("contrast") !== -1;
    const minTouch = params.min_touch > 0 ? params.min_touch : 0;
    const max = params.max_results || 100;
    const issues = [], summary = {};
    let checked = 0, contrastUnknown = 0, disabledSkipped = 0;
    const report = (n, issue, detail) => {
      summary[issue] = (summary[issue] || 0) + 1;
      if (issues.length < max) issues.push(Object.assign({ id: n.id, name: n.name, issue }, detail));
    };
    let insideInstances = 0;
    // clip: the visible window left by every clipping ancestor, or null.
    // inInstance: geometry inside an instance belongs to its component and
    // cannot be fixed on this screen (an icon's vector parts poking out of its
    // own frame), so only contrast and touch size are checked there.
    const walk = (n, clip, parentCut, inInstance) => {
      if (n.visible === false) return;
      const box = n.absoluteBoundingBox;
      if (!box) return;
      checked++;
      let cut = parentCut;
      if (inInstance) insideInstances++;
      if (layout && n !== root && !inInstance) {
        const seen = clip ? intersect(box, clip) : box;
        if (clip && !parentCut && (seen.width <= EPS || seen.height <= EPS)) {
          // Nothing of it shows, so nothing inside it is worth reporting.
          report(n, "hidden_by_clip", { clippedBy: clip.id });
          return;
        }
        const over = clip && !parentCut ? spill(box, clip) : null;
        if (over) {
          report(n, "clipped", { clippedBy: clip.id, cutOff: over });
          cut = true;
        } else if (n.parent && n.parent.absoluteBoundingBox && n.parent.type !== "GROUP" && !parentCut) {
          const out = spill(box, n.parent.absoluteBoundingBox);
          if (out) report(n, "overflows_parent", { parent: n.parent.id, by: out });
        }
      }
      if (n.type === "TEXT") {
        if (layout && !inInstance) {
          // A fixed-size text box does not grow with its content: the glyphs
          // either spill out of it or are cut with an ellipsis.
          const rb = n.absoluteRenderBounds;
          const fixed = n.textAutoResize === "NONE" || n.textAutoResize === "TRUNCATE";
          const out = fixed && rb ? spill(rb, box) : null;
          if (out && (out.right || out.bottom)) report(n, "text_overflows_box", { by: out });
          const size = typeof n.fontSize === "number" ? n.fontSize : 14;
          const lines = n.maxLines > 0 ? n.maxLines : Math.max(1, Math.floor(box.height / (size * 1.2)));
          // shortcut: half the font size per character is a lower bound on
          // width, so this only catches clear cases; measuring needs a clone,
          // and an audit must not touch the document.
          if ((n.textTruncation === "ENDING" || n.textAutoResize === "TRUNCATE") &&
              (n.characters || "").length * size * 0.5 > box.width * lines) {
            report(n, "text_truncated", { text: n.characters.slice(0, 60) });
          }
        }
        if (contrast && (n.characters || "").trim()) {
          const fg = topFill(n), bg = fg && fg !== "unknown" ? backgroundOf(n) : "unknown";
          if (!fg || inDisabledControl(n)) {
            // paints nothing, or a disabled control's label: exempt
            if (fg) disabledSkipped++;
          } else if (fg === "unknown" || bg === "unknown") {
            contrastUnknown++;
          } else {
            const size = typeof n.fontSize === "number" ? n.fontSize : 0;
            const bold = n.fontName && n.fontName !== figma.mixed && /bold|black|heavy/i.test(n.fontName.style || "");
            // WCAG AA: 3:1 for large text (24px, or 18.66px bold), else 4.5:1.
            const need = size >= 24 || (bold && size >= 18.66) ? 3 : 4.5;
            const ratio = contrastRatio(fg, bg);
            if (ratio < need) {
              report(n, "low_contrast", { ratio: Math.round(ratio * 100) / 100, required: need, text: rgbToHex(fg), background: rgbToHex(bg) });
            }
          }
        }
      }
      if (minTouch && isControl(n) && (box.width < minTouch - EPS || box.height < minTouch - EPS)) {
        report(n, "small_touch_target", { width: Math.round(box.width), height: Math.round(box.height), required: minTouch });
      }
      if (!("children" in n) || n.type === "BOOLEAN_OPERATION") return;
      let inner = clip;
      if (n.clipsContent) {
        inner = clip ? intersect(box, clip) : { x: box.x, y: box.y, width: box.width, height: box.height };
        inner.id = n.id;
      }
      for (const c of n.children) walk(c, inner, cut, inInstance || n.type === "INSTANCE");
    };
    walk(root, null, false, root.type === "INSTANCE");
    const total = Object.keys(summary).reduce((a, k) => a + summary[k], 0);
    const out = { scope: root.name, checks: want.concat(minTouch ? ["touch"] : []), nodesChecked: checked, total, summary, issues };
    if (total > issues.length) out.truncated = true;
    if (layout && insideInstances) out.layoutNotChecked = insideInstances + " layers inside instances; audit their main component to check those";
    if (disabledSkipped) out.contrastSkippedDisabled = disabledSkipped + " text layers in disabled controls (WCAG exempts them)";
    if (contrastUnknown) out.contrastNotMeasured = contrastUnknown + " text layers sit on an image, gradient or translucent fill; check those by screenshot";
    return out;
  },

  // Several commands in one round-trip. A failed op is reported in place and
  // the rest still run: what already landed in the document stays there.
  async batch(params) {
    const ops = params.ops || [];
    if (!ops.length) throw new Error("ops is required");
    const results = [];
    let failed = 0;
    for (let i = 0; i < ops.length; i++) {
      const op = ops[i] || {};
      try {
        if (NOT_BATCHABLE.indexOf(op.command) !== -1) throw new Error(op.command + " cannot run inside batch; call it as its own tool");
        const h = handlers[op.command];
        if (!h) throw new Error("unknown command: " + op.command + " (batch runs the same commands as the other tools, by tool name)");
        const r = await h(resolveBatchRefs(op.params || {}, results));
        results.push({ result: r === undefined ? null : r });
      } catch (e) {
        failed++;
        results.push({ error: String((e && e.message) || e) });
        if (params.stop_on_error) break;
      }
    }
    const out = { ok: failed === 0, ran: results.length, failed, results };
    if (results.length < ops.length) out.skipped = ops.length - results.length;
    return out;
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
    if (params.svg !== undefined) return importSvg(params);
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
    if (params.layout_wrap) {
      if (params.layout_wrap === "WRAP" && n.layoutMode !== "HORIZONTAL") throw new Error("layout_wrap WRAP needs layout_mode HORIZONTAL");
      n.layoutWrap = params.layout_wrap;
    }
    if (params.counter_axis_spacing !== undefined && params.counter_axis_spacing !== null) {
      if (n.layoutWrap !== "WRAP") throw new Error("counter_axis_spacing is the gap between wrapped rows: set layout_wrap WRAP first");
      n.counterAxisSpacing = params.counter_axis_spacing;
    }
    if (params.primary_axis_sizing) n.primaryAxisSizingMode = params.primary_axis_sizing;
    if (params.counter_axis_sizing) n.counterAxisSizingMode = params.counter_axis_sizing;
    return done(params, n);
  },

  // How a layer sizes and sits inside its parent. FILL and HUG only mean
  // something in auto layout, and Figma's own errors for that are opaque.
  async set_layout_child(params) {
    const nodes = await targets(params);
    const has = (k) => params[k] !== undefined && params[k] !== null && params[k] !== "";
    for (const n of nodes) {
      const inAuto = n.parent && "layoutMode" in n.parent && n.parent.layoutMode !== "NONE";
      const isAuto = "layoutMode" in n && n.layoutMode !== "NONE";
      for (const [k, prop] of [["sizing_horizontal", "layoutSizingHorizontal"], ["sizing_vertical", "layoutSizingVertical"]]) {
        if (!has(k)) continue;
        const v = params[k];
        if (v === "FILL" && !inAuto) throw new Error("node " + n.id + " cannot FILL: its parent " + (n.parent ? n.parent.name : "") + " has no auto layout (see set_auto_layout)");
        if (v === "HUG" && !isAuto && n.type !== "TEXT") throw new Error("node " + n.id + " cannot HUG: only auto-layout frames and text hug their content");
        n[prop] = v;
      }
      if (has("positioning")) {
        if (!inAuto) throw new Error("node " + n.id + ": positioning applies to children of an auto-layout frame");
        n.layoutPositioning = params.positioning;
      }
      for (const [k, prop] of [["min_width", "minWidth"], ["max_width", "maxWidth"], ["min_height", "minHeight"], ["max_height", "maxHeight"]]) {
        if (params[k] === undefined || params[k] === null) continue;
        if (!inAuto && !isAuto) throw new Error("node " + n.id + ": min/max sizes apply to auto-layout frames and their children");
        n[prop] = params[k] > 0 ? params[k] : null; // 0 clears the limit
      }
      if (has("constraint_horizontal") || has("constraint_vertical")) {
        if (!("constraints" in n)) throw new Error("node " + n.id + " is " + n.type + " and has no constraints");
        n.constraints = {
          horizontal: params.constraint_horizontal || n.constraints.horizontal,
          vertical: params.constraint_vertical || n.constraints.vertical,
        };
      }
    }
    return done(params, nodes);
  },

  async set_node_properties(params) {
    const nodes = await targets(params);
    const given = (k) => params[k] !== undefined && params[k] !== null;
    if (!["visible", "opacity", "rotation", "locked", "clips_content"].some(given)) {
      throw new Error("pass at least one of visible, opacity, rotation, locked, clips_content");
    }
    for (const n of nodes) {
      if (given("visible")) n.visible = params.visible;
      if (given("locked")) n.locked = params.locked;
      if (given("opacity")) {
        if (!("opacity" in n)) throw new Error("node " + n.id + " is " + n.type + " and has no opacity");
        if (params.opacity < 0 || params.opacity > 1) throw new Error("opacity is 0..1, got " + params.opacity);
        n.opacity = params.opacity;
      }
      if (given("rotation")) {
        if (!("rotation" in n)) throw new Error("node " + n.id + " is " + n.type + " and cannot rotate");
        n.rotation = params.rotation;
      }
      if (given("clips_content")) {
        if (!("clipsContent" in n)) throw new Error("node " + n.id + " is " + n.type + "; only frames and components clip their content");
        n.clipsContent = params.clips_content;
      }
    }
    return done(params, nodes);
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
          if (item.exact) settings.useAbsoluteBounds = true;
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
    if (!params.node_ids || !params.node_ids.length) throw new Error("node_ids is required");
    const nodes = await Promise.all(params.node_ids.map((id) => node(id)));
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
    const t = params.bind_node_id ? await node(params.bind_node_id) : null;
    const key = c.addComponentProperty(params.name, type, def);
    if (t) {
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

  // Removing a variable detaches it from every node bound to it; those nodes
  // keep the value they showed.
  async remove_variables(params) {
    const removed = [];
    for (const id of params.variable_ids || []) {
      const v = await figma.variables.getVariableByIdAsync(id);
      if (!v) throw new Error("variable not found: " + id + " (see get_variable_defs)");
      removed.push({ id: v.id, name: v.name });
      v.remove();
    }
    if (!removed.length) throw new Error("variable_ids is required");
    return { removed };
  },

  async remove_variable_collection(params) {
    const c = await figma.variables.getVariableCollectionByIdAsync(params.collection_id);
    if (!c) throw new Error("variable collection not found: " + params.collection_id + " (see get_variable_defs)");
    const out = { removed: { id: c.id, name: c.name, variables: c.variableIds.length } };
    c.remove();
    return out;
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
    const item = { id: n.id, name: n.name, type: n.type, link: nodeLink(n.id) };
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
  figma.ui.postMessage({ selection: items });
}
figma.on("selectionchange", postSelection);
postSelection();

// ---------- activity log clicks ----------

async function uiAction(msg) {
  if (msg.uiAction !== "select") throw new Error("unknown action: " + msg.uiAction);
  const n = await node(msg.nodeId);
  figma.currentPage.selection = [n];
  figma.viewport.scrollAndZoomIntoView([n]);
  return null;
}

// ---------- read-only switch ----------
// Commands that leave the document as it is (selection and the open page are
// view state). With the switch on, everything else is refused, so a tester can
// let AI read a file it must not change. On at every start and never
// remembered, so AI edits only while the user has this window unlocked.
const READ_COMMANDS = /^(get_|list_|find_|audit_|download_assets$|set_selection$|set_current_page$)/;
let readOnly = true;
register();
// Checkpoint buttons are for people who can edit this file. Dev Mode and
// view-only windows (should the plugin ever run there) do not get them.
figma.ui.postMessage({ canEdit: figma.editorType === "figma" && figma.mode !== "inspect" });

// ---------- dispatch ----------

figma.ui.onmessage = async (msg) => {
  if (!msg) return;
  // The window measures its content after every render.
  if (msg.resize) {
    figma.ui.resize(280, Math.max(104, Math.min(600, Math.round(msg.resize))));
    return;
  }
  if (msg.uiAction) {
    // A log row whose layer was deleted since: nothing to select.
    await uiAction(msg).catch(() => {});
    return;
  }
  if (msg.setReadOnly !== undefined) {
    readOnly = !!msg.setReadOnly;
    register();
    return;
  }
  if (!msg.id || !msg.command) return;
  const h = handlers[msg.command];
  const p = msg.params || {};
  const act = { command: msg.command, write: !READ_COMMANDS.test(msg.command), node: p.node_id || (p.node_ids && p.node_ids[0]) || null };
  try {
    if (!h) throw new Error("unknown command: " + msg.command);
    if (readOnly && act.write) {
      figma.notify("AI tried to edit this file (" + msg.command + ") and was blocked. Untick Read-only in Figma MCP Console to allow edits.", { timeout: 8000 });
      throw new Error(msg.command + " was blocked: read-only mode is on in this file's Figma MCP Console window. " +
        "Tell the user; they can turn it off there if the edit is intended.");
    }
    const result = await h(p);
    figma.ui.postMessage({ id: msg.id, result: result === undefined ? null : result });
  } catch (e) {
    act.error = explainError(String((e && e.message) || e));
    figma.ui.postMessage({ id: msg.id, error: act.error });
  }
  figma.ui.postMessage({ activity: act });
};
