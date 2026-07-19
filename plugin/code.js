// Figma MCP Console — plugin main thread.
// Receives {id, command, params} from the Go bridge (relayed through ui.html),
// executes it against the Figma Plugin API, and replies {id, result|error}.

figma.showUI(__html__, { width: 300, height: 128 });

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

// Mixed values (figma.mixed) are Symbols, which postMessage cannot clone.
function val(x) {
  return x === figma.mixed ? "mixed" : x;
}

function summarize(n) {
  const s = { id: n.id, name: n.name, type: n.type };
  if (n.parent) s.parentId = n.parent.id;
  if ("x" in n) { s.x = n.x; s.y = n.y; }
  if ("width" in n) { s.width = n.width; s.height = n.height; }
  if (n.type === "TEXT") s.text = n.characters;
  return s;
}

function serialize(n, depth) {
  const s = summarize(n);
  if ("visible" in n) s.visible = n.visible;
  if ("opacity" in n) s.opacity = n.opacity;
  if ("fills" in n && Array.isArray(n.fills)) s.fills = n.fills;
  if ("strokes" in n && n.strokes.length) {
    s.strokes = n.strokes;
    s.strokeWeight = val(n.strokeWeight);
  }
  if ("effects" in n && n.effects.length) s.effects = n.effects;
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
  if ("children" in n) {
    s.childCount = n.children.length;
    if (depth > 0) s.children = n.children.map((c) => serialize(c, depth - 1));
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
  if (params.fill_color) n.fills = [{ type: "SOLID", color: hexToRGB(params.fill_color) }];
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

  async get_selection() {
    return figma.currentPage.selection.map(summarize);
  },

  async get_design_context(params) {
    const depth = params.depth === undefined ? 2 : params.depth;
    return serialize(await node(params.node_id), depth);
  },

  async create_frame(params) {
    const f = figma.createFrame();
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
    const t = await node(params.node_id);
    if (t.type !== "TEXT") throw new Error("node " + params.node_id + " is " + t.type + ", not TEXT");
    if (t.characters.length > 0) {
      const fonts = t.getRangeAllFontNames(0, t.characters.length);
      await Promise.all(fonts.map(figma.loadFontAsync));
    } else {
      await figma.loadFontAsync(t.fontName);
    }
    t.characters = params.text;
    return summarize(t);
  },

  async set_fills(params) {
    const n = await node(params.node_id);
    if (!("fills" in n)) throw new Error("node " + params.node_id + " has no fills");
    let paint;
    if (params.gradient) {
      paint = buildGradient(params.gradient);
    } else if (params.color) {
      paint = { type: "SOLID", color: hexToRGB(params.color) };
    } else {
      throw new Error("set_fills needs color or gradient");
    }
    if (params.opacity !== undefined && params.opacity !== null) paint.opacity = params.opacity;
    n.fills = [paint];
    return summarize(n);
  },

  async move_nodes(params) {
    const out = [];
    for (const item of params.items) {
      const n = await node(item.node_id);
      if (!("x" in n)) throw new Error("node " + item.node_id + " cannot be moved");
      n.x = item.x;
      n.y = item.y;
      out.push(summarize(n));
    }
    return out;
  },

  async resize_nodes(params) {
    const out = [];
    for (const item of params.items) {
      const n = await node(item.node_id);
      if (!("resize" in n)) throw new Error("node " + item.node_id + " cannot be resized");
      n.resize(item.width, item.height);
      out.push(summarize(n));
    }
    return out;
  },

  async remove_nodes(params) {
    const deleted = [];
    for (const id of params.node_ids) {
      const n = await node(id);
      deleted.push(summarize(n));
      n.remove();
    }
    return { deleted };
  },

  async set_auto_layout(params) {
    const n = await node(params.node_id);
    if (!("layoutMode" in n)) throw new Error("node " + params.node_id + " does not support auto-layout");
    n.layoutMode = params.layout_mode;
    if (params.layout_mode === "NONE") return summarize(n);
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
    return summarize(n);
  },

  async set_corner_radius(params) {
    const n = await node(params.node_id);
    if (params.radius !== undefined) {
      if (!("cornerRadius" in n)) throw new Error("node " + params.node_id + " has no corner radius");
      n.cornerRadius = params.radius;
    }
    if (!("topLeftRadius" in n)) {
      if (params.radius === undefined) throw new Error("node " + params.node_id + " has no per-corner radius");
      return summarize(n);
    }
    if (params.top_left !== undefined) n.topLeftRadius = params.top_left;
    if (params.top_right !== undefined) n.topRightRadius = params.top_right;
    if (params.bottom_right !== undefined) n.bottomRightRadius = params.bottom_right;
    if (params.bottom_left !== undefined) n.bottomLeftRadius = params.bottom_left;
    return summarize(n);
  },

  async set_strokes(params) {
    const n = await node(params.node_id);
    if (!("strokes" in n)) throw new Error("node " + params.node_id + " has no strokes");
    if (params.color) {
      const paint = { type: "SOLID", color: hexToRGB(params.color) };
      if (params.opacity !== undefined && params.opacity !== null) paint.opacity = params.opacity;
      n.strokes = [paint];
    } else {
      n.strokes = [];
    }
    if (params.weight !== undefined && "strokeWeight" in n) n.strokeWeight = params.weight;
    if (params.align && "strokeAlign" in n) n.strokeAlign = params.align;
    return summarize(n);
  },

  async set_effects(params) {
    const n = await node(params.node_id);
    if (!("effects" in n)) throw new Error("node " + params.node_id + " has no effects");
    n.effects = buildEffects(params.effects);
    return summarize(n);
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
    const scope = params.node_id ? await node(params.node_id) : figma.currentPage;
    if (!("findAll" in scope)) throw new Error("node " + scope.id + " has no children to search");
    const name = params.name ? params.name.toLowerCase() : null;
    const text = params.text ? params.text.toLowerCase() : null;
    const types = params.types && params.types.length ? params.types : null;
    const matches = scope.findAll((n) => {
      if (types && types.indexOf(n.type) === -1) return false;
      if (name && n.name.toLowerCase().indexOf(name) === -1) return false;
      if (text && (n.type !== "TEXT" || n.characters.toLowerCase().indexOf(text) === -1)) return false;
      return true;
    });
    const max = params.max_results || 50;
    return {
      total: matches.length,
      truncated: matches.length > max,
      nodes: matches.slice(0, max).map(summarize),
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
    const out = [];
    let index = params.index;
    for (const id of params.node_ids) {
      const n = await node(id);
      if (index === undefined || index === null) {
        parent.appendChild(n);
      } else {
        parent.insertChild(index++, n);
      }
      out.push(summarize(n));
    }
    return out;
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
    const n = await node(params.node_id);
    const style = await figma.getStyleByIdAsync(params.style_id);
    if (!style) throw new Error("style not found: " + params.style_id);
    if (style.type === "PAINT") {
      if (params.target === "stroke") {
        await n.setStrokeStyleIdAsync(style.id);
      } else {
        await n.setFillStyleIdAsync(style.id);
      }
    } else if (style.type === "TEXT") {
      if (n.type !== "TEXT") throw new Error("node " + params.node_id + " is " + n.type + ", not TEXT");
      await figma.loadFontAsync(style.fontName);
      await n.setTextStyleIdAsync(style.id);
    } else if (style.type === "EFFECT") {
      await n.setEffectStyleIdAsync(style.id);
    } else if (style.type === "GRID") {
      await n.setGridStyleIdAsync(style.id);
    } else {
      throw new Error("unsupported style type: " + style.type);
    }
    return summarize(n);
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
    const n = await node(params.node_id);
    const v = await figma.variables.getVariableByIdAsync(params.variable_id);
    if (!v) throw new Error("variable not found: " + params.variable_id);
    const field = params.field;
    if (field === "fills" || field === "strokes") {
      if (!(field in n)) throw new Error("node " + params.node_id + " has no " + field);
      const paints = n[field].length ? n[field] : [{ type: "SOLID", color: { r: 0, g: 0, b: 0 } }];
      n[field] = [figma.variables.setBoundVariableForPaint(paints[0], "color", v)].concat(paints.slice(1));
    } else {
      n.setBoundVariable(field, v);
    }
    return summarize(n);
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
    let scale = params.scale || 1;
    scale = Math.max(0.5, Math.min(scale, 4));
    // Cap the output size: huge pages at high scale produce multi-MB base64
    // payloads that are slow to ship and waste the AI's context.
    const maxDim = Math.max("width" in target ? target.width : 0, "height" in target ? target.height : 0);
    if (maxDim * scale > 2000) scale = 2000 / maxDim;
    const bytes = await target.exportAsync({ format: "PNG", constraint: { type: "SCALE", value: scale } });
    return {
      data: figma.base64Encode(bytes),
      width: ("width" in target ? target.width : 0) * scale,
      height: ("height" in target ? target.height : 0) * scale,
    };
  },
};

// ---------- dispatch ----------

figma.ui.onmessage = async (msg) => {
  if (!msg || !msg.id || !msg.command) return;
  const h = handlers[msg.command];
  try {
    if (!h) throw new Error("unknown command: " + msg.command);
    const result = await h(msg.params || {});
    figma.ui.postMessage({ id: msg.id, result: result === undefined ? null : result });
  } catch (e) {
    figma.ui.postMessage({ id: msg.id, error: String((e && e.message) || e) });
  }
};
