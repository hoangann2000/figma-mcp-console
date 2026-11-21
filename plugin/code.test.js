// Runs plugin/code.js against a stubbed `figma` to check the read handlers
// (overrides, token names, text segments) and the read-only switch.
// Usage: node plugin/code.test.js
const fs = require("fs"), assert = require("assert");
const src = fs.readFileSync(require("path").join(__dirname, "code.js"), "utf8");
const posted = [], store = {}, notes = [];
const mixed = Symbol("mixed");
const box = (x, y, w, h) => ({ x, y, width: w, height: h });
// tiny fake tree: Screen (clips) > [Card > [Title text overflowing, Icon sticking out], Footer partly outside Screen], Price truncated
const title = { id: "1:3", name: "Title", type: "TEXT", visible: true, fontSize: 16, textAutoResize: "NONE", textTruncation: "DISABLED",
  absoluteBoundingBox: box(10, 10, 100, 20), absoluteRenderBounds: box(10, 10, 100, 60) };
const icon = { id: "1:4", name: "Icon", type: "VECTOR", visible: true, absoluteBoundingBox: box(195, 10, 20, 20) };
const card = { id: "1:2", name: "Card", type: "FRAME", visible: true, clipsContent: false, absoluteBoundingBox: box(0, 0, 200, 100), children: [title, icon] };
const footer = { id: "1:5", name: "Footer", type: "FRAME", visible: true, clipsContent: false, absoluteBoundingBox: box(0, 380, 360, 60), children: [] };
const gone = { id: "1:7", name: "Gone", type: "RECTANGLE", visible: true, absoluteBoundingBox: box(500, 0, 10, 10) };
const hidden = { id: "1:8", name: "Hidden", type: "RECTANGLE", visible: false, absoluteBoundingBox: box(900, 0, 10, 10) };
const price = { id: "1:6", name: "Price", type: "TEXT", visible: true, fontSize: 14, textTruncation: "ENDING", maxLines: 1,
  absoluteBoundingBox: box(0, 200, 80, 20), absoluteRenderBounds: box(0, 200, 80, 20),
  fontName: mixed, characters: "Giá 100k",
  getStyledTextSegments: () => [
    { characters: "Giá ", fontName: { family: "Inter", style: "Regular" }, fontSize: 14, fills: [{ type: "SOLID", color: { r: 0, g: 0, b: 0 } }], textDecoration: "NONE" },
    { characters: "100k", fontName: { family: "Inter", style: "Bold" }, fontSize: 18, fills: [{ type: "SOLID", color: { r: 1, g: 0, b: 0 } }], textDecoration: "STRIKETHROUGH" }] };
const screen = { id: "1:1", name: "Screen", type: "FRAME", visible: true, clipsContent: true, absoluteBoundingBox: box(0, 0, 360, 400),
  children: [card, footer, price, gone, hidden], boundVariables: { fills: [{ type: "VARIABLE_ALIAS", id: "V1" }] }, fillStyleId: "S1",
  findAllWithCriteria: () => [price] };
for (const c of [card, footer, price, gone, hidden]) c.parent = screen;
for (const c of [title, icon]) c.parent = card;
const nodes = { "1:1": screen, "1:6": price };
global.__html__ = "";
global.figma = {
  mixed, fileKey: "K", root: { name: "F" }, currentPage: { selection: [], name: "Page" },
  showUI() {}, on() {}, ui: { postMessage: (m) => posted.push(m), resize() {} },
  notify: (m) => notes.push(m),
  clientStorage: { getAsync: async (k) => store[k], setAsync: async (k, v) => (store[k] = v) },
  getNodeByIdAsync: async (id) => nodes[id] || null,
  getStyleByIdAsync: async (id) => (id === "S1" ? { name: "Surface/White" } : null),
  variables: { getVariableByIdAsync: async (id) => (id === "V1" ? { name: "color/bg" } : null) },
};
eval(src + "\nglobalThis.__h = handlers; globalThis.__resolve = resolveRefs; globalThis.__ser = serialize;");
(async () => {
  const ctx = await __resolve(__ser(screen, 1, true, false));
  console.log(ctx.boundVariables, ctx.fillStyle, ctx.children.find((c) => c.name === "Price").segments);
  assert.deepStrictEqual(ctx.boundVariables, { fills: ["color/bg"] });
  assert.strictEqual(ctx.fillStyle, "Surface/White");
  assert.ok(!("fillStyleId" in ctx));
  assert.strictEqual(screen.boundVariables.fills[0].type, "VARIABLE_ALIAS", "source node must not be mutated");
  const seg = ctx.children.find((c) => c.name === "Price").segments;
  assert.strictEqual(seg[1].decoration, "STRIKETHROUGH");

  // read-only: a write is refused, a read passes, both logged
  // read-only is on before the user ever ticks it
  await figma.ui.onmessage({ id: "a", command: "remove_nodes", params: { node_ids: ["1:7"] } });
  await figma.ui.onmessage({ id: "b", command: "get_design_context", params: { node_id: "1:1" } });
  const res = posted.filter((m) => m.id), acts = posted.filter((m) => m.activity).map((m) => m.activity);
  assert.match(res.find((m) => m.id === "a").error, /blocked: read-only/);
  assert.strictEqual(notes.length, 1, "a blocked edit must tell the user");
  assert.match(notes[0], /remove_nodes/);
  assert.ok(res.find((m) => m.id === "b").result);
  assert.deepStrictEqual(acts.map((x) => [x.command, x.write, !!x.error]), [["remove_nodes", true, true], ["get_design_context", false, false]]);
  // unticked: a write goes through (same name, so later checks still see "Screen"), and nothing is stored
  const regs = () => posted.filter((m) => m.register).map((m) => m.register.read_only);
  assert.deepStrictEqual(regs(), [true], "the file registers as read-only from the start");
  await figma.ui.onmessage({ setReadOnly: false });
  assert.deepStrictEqual(regs(), [true, false], "unticking re-registers so list_files shows it");
  await figma.ui.onmessage({ id: "c", command: "rename_nodes", params: { items: [{ node_id: "1:1", name: "Screen" }] } });
  assert.ok(posted.find((m) => m.id === "c").result, posted.find((m) => m.id === "c").error);
  assert.deepStrictEqual(store, {});
  // overrides on an instance: which layer, which fields
  const set = { id: "9:1", name: "Button", type: "COMPONENT_SET", componentPropertyDefinitions: { State: { type: "VARIANT", variantOptions: ["Default", "Disabled"] } } };
  const main = { id: "9:2", name: "State=Disabled", parent: set, remote: false };
  const label = { id: "I5:1;9:3", name: "Label" };
  const inst = { id: "5:1", name: "Btn", type: "INSTANCE", visible: true, variantProperties: { State: "Disabled" },
    getMainComponentAsync: async () => main, overrides: [{ id: "5:1", overriddenFields: ["fills"] }, { id: "I5:1;9:3", overriddenFields: ["characters"] }] };
  nodes["9:9"] = { id: "9:9", name: "Form", type: "FRAME", findAllWithCriteria: () => [inst] };
  nodes["I5:1;9:3"] = label;
  const st = await __h.get_component_states({ node_id: "9:9" });
  console.log(JSON.stringify(st[0].overrides));
  assert.strictEqual(st[0].overridden, 1);
  assert.deepStrictEqual(st[0].overrides[0].layers, [{ layer: "Btn", fields: ["fills"] }, { layer: "Label", fields: ["characters"] }]);

  // audit_design: findings only, straight off the fixture tree above
  title.fills = [{ type: "SOLID", color: { r: 0.6, g: 0.6, b: 0.6 } }];
  title.characters = "Hello"; title.fontName = { family: "Inter", style: "Regular" };
  card.fills = [{ type: "SOLID", color: { r: 1, g: 1, b: 1 } }];
  icon.name = "Icon button"; icon.reactions = [{}]; // wired in the prototype, so it is a control
  const au = await __h.audit_design({ node_id: "1:1", min_touch: 44 });
  const found = au.issues.map((i) => i.name + ":" + i.issue).sort();
  console.log(found);
  assert.deepStrictEqual(found, [
    "Footer:clipped",              // sticks out of the clipping Screen
    "Gone:hidden_by_clip",         // entirely outside it
    "Icon button:overflows_parent",// out of Card, which does not clip
    "Icon button:small_touch_target",
    "Title:low_contrast",          // #999 on white is 2.85:1
    "Title:text_overflows_box",    // glyphs taller than the fixed box
  ]);
  assert.ok(!found.some((f) => f.startsWith("Hidden")), "invisible layers are not audited");
  assert.strictEqual(au.issues.find((i) => i.issue === "low_contrast").ratio, 2.85);
  assert.deepStrictEqual(au.issues.find((i) => i.name === "Footer").cutOff, { bottom: 40 });
  // inside an instance only contrast and touch are checked: its geometry belongs to the component
  const inner = { id: "I2:1;3:1", name: "Glyph", type: "VECTOR", visible: true, absoluteBoundingBox: box(150, 0, 40, 10) };
  const badge = { id: "2:1", name: "Badge", type: "INSTANCE", visible: true, clipsContent: true, absoluteBoundingBox: box(100, 300, 60, 20), children: [inner] };
  inner.parent = badge; badge.parent = screen;
  const nearly = { id: "2:2", name: "Nearly", type: "RECTANGLE", visible: true, absoluteBoundingBox: box(-0.8, 100, 10, 10), parent: screen };
  screen.children.push(badge, nearly);
  const au2 = await __h.audit_design({ node_id: "1:1" });
  assert.ok(!au2.issues.some((i) => i.name === "Glyph"), "no layout findings inside an instance");
  assert.ok(!au2.issues.some((i) => i.name === "Nearly"), "under a pixel is noise");
  assert.match(au2.layoutNotChecked, /^1 layers inside instances/);
  screen.children.splice(-2);

  // a disabled button's label is exempt from contrast, an enabled one is not
  const mkBtn = (id, state) => {
    const lbl = { id: id + "t", name: "Label", type: "TEXT", visible: true, characters: "Send", fontSize: 14,
      fontName: { family: "Inter", style: "Regular" }, fills: [{ type: "SOLID", color: { r: 0.6, g: 0.6, b: 0.6 } }],
      textAutoResize: "WIDTH_AND_HEIGHT", absoluteBoundingBox: box(10, 330, 40, 16) };
    const btn = { id, name: "Button", type: "INSTANCE", visible: true, variantProperties: { State: state },
      fills: [{ type: "SOLID", color: { r: 1, g: 1, b: 1 } }], absoluteBoundingBox: box(0, 320, 100, 40), children: [lbl], parent: screen };
    lbl.parent = btn;
    return btn;
  };
  screen.children.push(mkBtn("3:1", "Disable"), mkBtn("3:2", "Default"));
  const au3 = await __h.audit_design({ node_id: "1:1", checks: ["contrast"] });
  const lows = au3.issues.filter((i) => i.issue === "low_contrast").map((i) => i.id);
  assert.ok(lows.includes("3:2t") && !lows.includes("3:1t"), JSON.stringify(lows));
  assert.match(au3.contrastSkippedDisabled, /^1 text layers/);
  screen.children.splice(-2);

  // it is a read: allowed while read-only is on
  await figma.ui.onmessage({ setReadOnly: true });
  await figma.ui.onmessage({ id: "d", command: "audit_design", params: { node_id: "1:1" } });
  assert.ok(posted.find((m) => m.id === "d").result);
  // batch is a write: refused whole while read-only
  await figma.ui.onmessage({ id: "e", command: "batch", params: { ops: [{ command: "rename_nodes", params: { items: [] } }] } });
  assert.match(posted.find((m) => m.id === "e").error, /blocked: read-only/);
  await figma.ui.onmessage({ setReadOnly: false });

  // batch: later ops reach earlier results with "$n", a failure stays in its slot
  const made = [];
  const page = { type: "PAGE", children: [{ x: 0, y: 50, width: 300, height: 100 }], appendChild(n) { n.parent = this; this.children.push(n); } };
  figma.currentPage = Object.assign(page, { selection: [], name: "Page" });
  figma.createFrame = () => {
    const f = { id: "N" + (made.length + 1), type: "FRAME", name: "Frame", fills: ["white"], children: [],
      resize(w, h) { this.width = w; this.height = h; }, appendChild(n) { n.parent = this; this.children.push(n); } };
    made.push(f); nodes[f.id] = f;
    return f;
  };
  const b = await __h.batch({ ops: [
    { command: "create_frame", params: { name: "Screen", width: 360, height: 640 } },
    { command: "create_frame", params: { name: "Row", parent_id: "$0", width: 100, height: 40 } },
    { command: "rename_nodes", params: { items: [{ node_id: "nope", name: "x" }] } },
    { command: "get_screenshot", params: {} },
    { command: "rename_nodes", params: { items: [{ node_id: "$1", name: "Header" }] } },
  ] });
  console.log(JSON.stringify(b.results.map((r) => r.error || "ok")));
  assert.deepStrictEqual([b.ok, b.ran, b.failed], [false, 5, 2]);
  assert.match(b.results[2].error, /node not found: nope/);
  assert.match(b.results[3].error, /cannot run inside batch/);
  assert.strictEqual(made[1].parent, made[0], "$0 resolved to the first op's frame");
  assert.strictEqual(made[1].name, "Header");
  // safe defaults: a screen keeps Figma's white and lands beside existing work; a nested frame has no fill
  assert.deepStrictEqual([made[0].fills, made[0].x, made[0].y], [["white"], 400, 50]);
  assert.deepStrictEqual(made[1].fills, []);
  assert.deepStrictEqual((await __h.batch({ stop_on_error: true, ops: [{ command: "nope" }, { command: "list_pages" }] })).skipped, 1);

  // import_image with SVG: vector layers, recolored, scaled as a whole
  const glyph = { type: "VECTOR", fills: [], strokes: [{ type: "SOLID", color: { r: 0, g: 0, b: 0 }, opacity: 0.5 }] };
  let scaled = null;
  const svgFrame = { id: "S1", type: "FRAME", name: "svg", width: 24, height: 24, fills: ["white"],
    findAll: () => [glyph], rescale(k) { scaled = k; this.width *= k; this.height *= k; } };
  figma.createNodeFromSvg = (src) => { assert.match(src, /<svg/); return svgFrame; };
  const svgNode = await __h.import_image({ svg: "<svg/>", name: "arrow-left", color: "#2563eb", width: 16, x: 8, y: 4 });
  assert.deepStrictEqual([svgNode.name, svgFrame.fills, scaled, svgFrame.x, svgFrame.y], ["arrow-left", [], 16 / 24, 8, 4]);
  assert.strictEqual(glyph.strokes[0].opacity, 0.5, "recolor keeps opacity");
  assert.strictEqual(Math.round(glyph.strokes[0].color.b * 255), 0xeb);
  assert.strictEqual(svgFrame.parent, figma.currentPage);
  await assert.rejects(__h.import_image({ svg: "<svg/>", node_id: "1:1" }), /becomes layers, not a fill/);

  // remove_variables / remove_variable_collection
  const gone = [];
  figma.variables.getVariableByIdAsync = async (id) => (id === "V1" ? { id, name: "color/bg", remove: () => gone.push(id) } : null);
  figma.variables.getVariableCollectionByIdAsync = async (id) => (id === "C1" ? { id, name: "theme", variableIds: ["V1", "V2"], remove: () => gone.push(id) } : null);
  assert.deepStrictEqual(await __h.remove_variables({ variable_ids: ["V1"] }), { removed: [{ id: "V1", name: "color/bg" }] });
  await assert.rejects(__h.remove_variables({ variable_ids: ["V9"] }), /variable not found: V9/);
  assert.deepStrictEqual((await __h.remove_variable_collection({ collection_id: "C1" })).removed, { id: "C1", name: "theme", variables: 2 });
  assert.deepStrictEqual(gone, ["V1", "C1"]);

  {
    // set_prototype_links: items for one node become its single set of reactions
    const got = {};
    const btn = { id: "7:1", name: "Btn", type: "INSTANCE", setReactionsAsync: async (r) => (got.r = r) };
    nodes["7:1"] = btn; nodes["7:2"] = { id: "7:2", name: "Detail", type: "FRAME" };
    await __h.set_prototype_links({ items: [
      { node_id: "7:1", destination_id: "7:2", transition: "smart_animate" },
      { node_id: "7:1", trigger: "ON_HOVER", navigation: "BACK" },
    ] });
    assert.strictEqual(got.r.length, 2, "both links on one node land in one call");
    assert.deepStrictEqual(got.r[0].actions[0].destinationId, "7:2");
    assert.strictEqual(got.r[0].actions[0].transition.type, "SMART_ANIMATE");
    assert.ok(!("direction" in got.r[0].actions[0].transition), "smart animate takes no direction");
    assert.deepStrictEqual(got.r[1], { trigger: { type: "ON_HOVER" }, actions: [{ type: "BACK" }] });
    await assert.rejects(__h.set_prototype_links({ items: [{ node_id: "7:1" }] }), /destination_id is required for NAVIGATE/);
    await assert.rejects(__h.set_prototype_links({ items: [{ node_id: "7:1", destination_id: "7:2", trigger: "ON_WINK" }] }), /unsupported trigger/);
    await assert.rejects(__h.set_prototype_links({ items: [{ node_id: "7:1", destination_id: "7:2", transition: "FLIP" }] }), /unsupported transition/);

    // combine_as_variants: Figma stacks variants; the handler lays them out and sizes the set
    const v1 = { id: "8:1", type: "COMPONENT", width: 100, height: 40 }, v2 = { id: "8:2", type: "COMPONENT", width: 120, height: 60 };
    nodes["8:1"] = v1; nodes["8:2"] = v2; nodes["8:3"] = { id: "8:3", type: "FRAME" };
    let size;
    figma.combineAsVariants = (list, parent) => ({ id: "8:9", name: "Set", type: "COMPONENT_SET", layoutMode: "NONE", children: list, parent, resize: (w, h) => (size = [w, h]) });
    v1.parent = { id: "8:0", type: "FRAME" };
    const set = await __h.combine_as_variants({ node_ids: ["8:1", "8:2"], name: "Button" });
    assert.deepStrictEqual([v1.y, v2.y, size, set.name], [0, 72, [120, 132], "Button"]);
    await assert.rejects(__h.combine_as_variants({ node_ids: ["8:1", "8:3"] }), /is FRAME, not a COMPONENT/);

    // set_bound_variable: paints are bound through setBoundVariableForPaint, other fields directly
    const card2 = { id: "9:5", type: "FRAME", fills: [{ type: "SOLID", color: { r: 1, g: 1, b: 1 } }], setBoundVariable(f, v) { this.bound = [f, v.id]; } };
    nodes["9:5"] = card2;
    figma.variables.getVariableByIdAsync = async (id) => (id === "VAR1" ? { id, name: "color/bg" } : null);
    figma.variables.setBoundVariableForPaint = (paint, field, v) => Object.assign({}, paint, { boundVariables: { [field]: v.id } });
    await __h.set_bound_variable({ node_id: "9:5", field: "fills", variable_id: "VAR1" });
    assert.deepStrictEqual(card2.fills[0].boundVariables, { color: "VAR1" });
    await __h.set_bound_variable({ node_id: "9:5", field: "width", variable_id: "VAR1" });
    assert.deepStrictEqual(card2.bound, ["width", "VAR1"]);
    await assert.rejects(__h.set_bound_variable({ node_id: "9:5", field: "fills", variable_id: "NOPE" }), /variable not found/);

    // find_nodes: style filters, chosen fields, named scope
    const red = [{ type: "SOLID", color: { r: 1, g: 0, b: 0 } }];
    const pool = [
      { id: "f1", name: "Title", type: "TEXT", characters: "Hello", fontSize: 14, fontName: { family: "Inter", style: "Bold" }, fills: red },
      { id: "f2", name: "Box", type: "RECTANGLE", fills: red },
      { id: "f3", name: "Body", type: "TEXT", characters: "World", fontSize: 16, fontName: { family: "Inter", style: "Regular" }, fills: [] },
    ];
    nodes["9:6"] = { id: "9:6", name: "Screen", type: "FRAME", parent: { type: "PAGE", name: "Home" }, findAll: (fn) => pool.filter(fn) };
    const ids = async (q) => (await __h.find_nodes(Object.assign({ node_id: "9:6" }, q))).nodes.map((n) => n.id);
    assert.deepStrictEqual(await ids({ fill: "FF0000" }), ["f1", "f2"]);
    assert.deepStrictEqual(await ids({ fill: "#ff0000", types: ["TEXT"] }), ["f1"]);
    assert.deepStrictEqual(await ids({ font_size: 16 }), ["f3"]);
    assert.deepStrictEqual(await ids({ font_style: "bold" }), ["f1"]);
    assert.deepStrictEqual(await ids({ text: "WOR" }), ["f3"]);
    const fr = await __h.find_nodes({ node_id: "9:6", name: "title", fields: ["fills", "characters"] });
    assert.deepStrictEqual([fr.scope, fr.nodes[0].fills, fr.nodes[0].characters], ["Screen (page Home)", ["#ff0000"], "Hello"]);

    // add_component_property: creates the property and binds it to the child layer
    const lbl = { id: "9:8", componentPropertyReferences: { visible: "Show#1:1" } };
    const comp = { id: "9:7", type: "COMPONENT", addComponentProperty: (n, t, d) => { comp.added = [n, t, d]; return n + "#9:1"; } };
    nodes["9:7"] = comp; nodes["9:8"] = lbl;
    const prop = await __h.add_component_property({ component_id: "9:7", name: "Label", default_value: "OK", bind_node_id: "9:8" });
    assert.deepStrictEqual([prop.key, lbl.componentPropertyReferences], ["Label#9:1", { visible: "Show#1:1", characters: "Label#9:1" }]);
    await __h.add_component_property({ component_id: "9:7", name: "Icon", type: "boolean" });
    assert.deepStrictEqual(comp.added, ["Icon", "BOOLEAN", false], "a missing BOOLEAN default becomes false");
    await assert.rejects(__h.add_component_property({ component_id: "9:5", name: "X" }), /not a COMPONENT or COMPONENT_SET/);

    // pages: create at an index, rename, remove the open page (its neighbour opens), refuse the last one
    const mk = (id, name) => ({ id, name, type: "PAGE", children: [], loadAsync: async () => {},
      remove() { figma.root.children.splice(figma.root.children.indexOf(this), 1); } });
    const home = mk("0:1", "Home"), draft = mk("0:2", "Draft");
    figma.root.children = [home, draft];
    figma.root.insertChild = (i, pg) => { const a = figma.root.children; a.splice(a.indexOf(pg), 1); a.splice(i, 0, pg); };
    figma.createPage = () => { const pg = mk("0:9", "Page 3"); figma.root.children.push(pg); return pg; };
    figma.setCurrentPageAsync = async (pg) => { figma.currentPage = pg; };
    figma.currentPage = draft;
    assert.deepStrictEqual(await __h.create_page({ name: "MCP Test", index: 0 }), { id: "0:9", name: "MCP Test", index: 0 });
    assert.deepStrictEqual((await __h.rename_page({ page: "mcp", name: "Scratch" })).from, "MCP Test");
    const rm = await __h.remove_page({ page: "Draft" });
    assert.deepStrictEqual([rm.removed.name, rm.currentPage, figma.root.children.map((x) => x.name)], ["Draft", "Home", ["Scratch", "Home"]]);
    await __h.remove_page({ page: "Scratch" });
    await assert.rejects(__h.remove_page({ page: "Home" }), /only page/);

    // read-only: the page tools are writes, list_pages stays a read
    await figma.ui.onmessage({ setReadOnly: true });
    await figma.ui.onmessage({ id: "p1", command: "create_page", params: { name: "X" } });
    await figma.ui.onmessage({ id: "p2", command: "list_pages", params: {} });
    assert.match(posted.find((m) => m.id === "p1").error, /blocked: read-only/);
    assert.ok(posted.find((m) => m.id === "p2").result);

    // Figma's "Unable to establish connection" gets a hint that is not about the network
    const keep = figma.getNodeByIdAsync;
    figma.getNodeByIdAsync = async () => { throw new Error("Unable to establish connection to Figma after 10 seconds. Please check your internet connection."); };
    await figma.ui.onmessage({ id: "p3", command: "get_design_context", params: { node_id: "1:1" } });
    figma.getNodeByIdAsync = keep;
    assert.match(posted.find((m) => m.id === "p3").error, /could not load the data for this request; the network is usually fine/);
    await figma.ui.onmessage({ setReadOnly: false });
  }

  {
    // set_layout_child: FILL needs an auto-layout parent, HUG an auto-layout frame or text
    const row = { id: "L:0", name: "Row", type: "FRAME", layoutMode: "HORIZONTAL" };
    const plain = { id: "L:9", name: "Canvas", type: "FRAME", layoutMode: "NONE" };
    const kid = { id: "L:1", type: "RECTANGLE", parent: row, constraints: { horizontal: "MIN", vertical: "MIN" } };
    const loose = { id: "L:2", type: "RECTANGLE", parent: plain, constraints: { horizontal: "MIN", vertical: "MIN" } };
    const label = { id: "L:3", type: "TEXT", parent: row };
    Object.assign(nodes, { "L:1": kid, "L:2": loose, "L:3": label });
    await __h.set_layout_child({ node_id: "L:1", sizing_horizontal: "FILL", min_width: 120, max_width: 0, positioning: "ABSOLUTE" });
    assert.deepStrictEqual([kid.layoutSizingHorizontal, kid.minWidth, kid.maxWidth, kid.layoutPositioning], ["FILL", 120, null, "ABSOLUTE"]);
    await __h.set_layout_child({ node_id: "L:3", sizing_horizontal: "HUG" });
    await assert.rejects(__h.set_layout_child({ node_id: "L:2", sizing_horizontal: "FILL" }), /cannot FILL: its parent Canvas has no auto layout/);
    await assert.rejects(__h.set_layout_child({ node_id: "L:1", sizing_vertical: "HUG" }), /cannot HUG/);
    await assert.rejects(__h.set_layout_child({ node_id: "L:2", min_width: 10 }), /min\/max sizes apply/);
    await __h.set_layout_child({ node_id: "L:2", constraint_horizontal: "STRETCH" });
    assert.deepStrictEqual(loose.constraints, { horizontal: "STRETCH", vertical: "MIN" }, "the other axis is kept");

    // set_node_properties: false and 0 are values, not "not given"
    const box2 = { id: "L:4", type: "FRAME", visible: true, locked: false, opacity: 1, rotation: 0, clipsContent: true };
    nodes["L:4"] = box2;
    await __h.set_node_properties({ node_id: "L:4", visible: false, opacity: 0, clips_content: false, rotation: 90, locked: true });
    assert.deepStrictEqual([box2.visible, box2.opacity, box2.clipsContent, box2.rotation, box2.locked], [false, 0, false, 90, true]);
    await assert.rejects(__h.set_node_properties({ node_id: "L:4" }), /at least one of/);
    await assert.rejects(__h.set_node_properties({ node_id: "L:4", opacity: 40 }), /opacity is 0\.\.1/);
    await assert.rejects(__h.set_node_properties({ node_id: "L:1", clips_content: true }), /only frames and components clip/);

    // set_auto_layout: wrap and the row gap
    const grid = { id: "L:5", type: "FRAME", layoutMode: "NONE", layoutWrap: "NO_WRAP" };
    nodes["L:5"] = grid;
    await __h.set_auto_layout({ node_id: "L:5", layout_mode: "HORIZONTAL", layout_wrap: "WRAP", counter_axis_spacing: 16 });
    assert.deepStrictEqual([grid.layoutWrap, grid.counterAxisSpacing], ["WRAP", 16]);
    await assert.rejects(__h.set_auto_layout({ node_id: "L:5", layout_mode: "VERTICAL", layout_wrap: "WRAP" }), /needs layout_mode HORIZONTAL/);
  }

  {
    // update_style: only the fields of the style's own type; remove_styles
    const styles = {
      P: { id: "P", name: "color/primary", type: "PAINT", paints: [{ type: "SOLID", color: { r: 0, g: 0, b: 0 }, opacity: 0.5 }], remove() { this.gone = true; } },
      T: { id: "T", name: "text/body", type: "TEXT", fontName: { family: "Inter", style: "Regular" }, fontSize: 14 },
    };
    figma.getStyleByIdAsync = async (id) => styles[id] || null;
    const loaded = [];
    figma.loadFontAsync = async (f) => { loaded.push(f.family + " " + f.style); };
    await __h.update_style({ style_id: "P", color: "#ff0000", name: "color/brand", description: "" });
    assert.deepStrictEqual([styles.P.name, styles.P.paints[0].color.r, styles.P.paints[0].opacity, styles.P.description], ["color/brand", 1, 0.5, ""]);
    await __h.update_style({ style_id: "T", font_style: "Bold", font_size: 16 });
    assert.deepStrictEqual([styles.T.fontName, styles.T.fontSize, loaded], [{ family: "Inter", style: "Bold" }, 16, ["Inter Bold"]]);
    await assert.rejects(__h.update_style({ style_id: "T", color: "#fff" }), /color applies to PAINT styles; text\/body is a TEXT style/);
    assert.deepStrictEqual((await __h.remove_styles({ style_ids: ["P"] })).removed[0].name, "color/brand");
    assert.ok(styles.P.gone);
    await assert.rejects(__h.remove_styles({ style_ids: ["Z"] }), /style not found: Z/);

    // create_section: only on a page or section; free space when no position is given
    const pg = { id: "0:5", type: "PAGE", name: "P", selection: [], children: [{ x: 0, y: 20, width: 500, height: 100 }], appendChild(n) { n.parent = this; this.children.push(n); } };
    figma.currentPage = pg;
    figma.createSection = () => ({ id: "SEC", type: "SECTION", name: "Section", width: 100, height: 100, resizeWithoutConstraints(w, h) { this.width = w; this.height = h; } });
    const sec = await __h.create_section({ name: "Login", width: 2000, height: 1200 });
    assert.deepStrictEqual([sec.name, sec.x, sec.y, sec.width], ["Login", 600, 20, 2000]);
    nodes["L:0x"] = { id: "L:0x", type: "FRAME", appendChild() {} };
    await assert.rejects(__h.create_section({ parent_id: "L:0x" }), /goes on a page or inside another section/);

    // text decoration and case; component description
    const tx = { id: "TX", type: "TEXT", characters: "", fontName: { family: "Inter", style: "Regular" } };
    nodes.TX = tx;
    await __h.set_text_properties({ node_id: "TX", text_decoration: "UNDERLINE", text_case: "UPPER" });
    assert.deepStrictEqual([tx.textDecoration, tx.textCase], ["UNDERLINE", "UPPER"]);
    nodes.CMP = { id: "CMP", type: "COMPONENT" };
    await __h.set_node_properties({ node_id: "CMP", description: "Primary action" });
    assert.strictEqual(nodes.CMP.description, "Primary action");
    await assert.rejects(__h.set_node_properties({ node_id: "TX", description: "x" }), /only components and component sets/);
  }

  console.log("ALL OK");
})().catch((e) => { console.error(e); process.exit(1); });
