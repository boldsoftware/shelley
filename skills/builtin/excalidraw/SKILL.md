---
name: excalidraw
description: Use when the user asks for a diagram, sketch, flowchart, architecture diagram, sequence diagram, mind map, or other visual explanation. Renders hand-drawn-style diagrams from Excalidraw JSON via the output_html tool.
---

Use this skill whenever the user wants something *drawn* or *diagrammed* (architecture, flow, sequence, mind map, sketch — anything visual). It renders an Excalidraw canvas inside the chat (view-only — user can pan/zoom but not edit; iteration happens by you editing the JSON) with **Download .excalidraw**, **Download PNG**, **Copy SVG**, **Copy PNG**, and **Copy JSON** buttons.

## Workflow

1. Write the renderer page to `/tmp/excalidraw.html` (template below).
2. Write the diagram to `/tmp/diagram.json` as a JSON array of elements (skeleton format — see below).
3. Call `output_html` with `path=/tmp/excalidraw.html`, `files={"elements.json": "/tmp/diagram.json"}`, and `libraries=["excalidraw"]`.
4. On user feedback ("move the DB right", "add a cache layer") edit the JSON and call `output_html` again.
5. If the user clicks **Copy JSON** in the canvas and pastes scene JSON back, parse it and use it as the new source of truth.

The canvas is view-only because the sandbox blocks edits-out: any change the user made would be silently lost on the next render. Iteration happens by you editing the JSON, or by the user copy/pasting JSON or SVG (with embedded scene) back.

The `libraries` parameter is essential: it tells the host page to stream the excalidraw runtime into the iframe out-of-band, so the (~2.5 MB) bundle does not get stored in the conversation. Without it, `window.__LIBS__` will never resolve.

## Renderer template (`excalidraw.html`)

```html
<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<style>
  html,body { margin:0; padding:0; height:100vh; font-family:sans-serif; }
  #toolbar { display:flex; gap:8px; padding:6px 10px; background:var(--color-background-secondary, #f5f5f5); border-bottom:1px solid var(--color-border-primary, #ddd); align-items:center; font-size:13px; }
  #toolbar button { font:inherit; padding:4px 10px; cursor:pointer; }
  #status { color:#888; margin-left:6px; }
  #editor { width:100%; height:calc(100vh - 40px); }
  #err { color:#b00; white-space:pre-wrap; padding:8px; font:12px monospace; }
</style>
</head>
<body>
<div id="toolbar">
  <button id="download">Download .excalidraw</button>
  <button id="download-png">Download PNG</button>
  <button id="copy-svg">Copy SVG</button>
  <button id="copy-png">Copy PNG</button>
  <button id="copy-json">Copy JSON</button>
  <span id="status"></span>
</div>
<div id="editor"></div>
<div id="err"></div>
<script type="module">
  try {
    const { render } = (await window.__LIBS__).excalidraw;
    const raw = window.__FILES__["elements.json"] || "[]";
    render({ elements: typeof raw === "string" ? JSON.parse(raw) : raw });
  } catch (e) {
    document.getElementById("err").textContent = "Excalidraw error: " + (e.stack || e.message);
  }
</script>
</body>
</html>
```

The template is intentionally minimal — toolbar markup plus one `render()` call. The button ids (`download`, `download-png`, `copy-svg`, `copy-png`, `copy-json`) and the editor mount id (`editor`) are wired up by the excalidraw library. Don't rename them. Any button you omit from the toolbar HTML is simply not bound — e.g. drop `copy-png` if you don't want it.

**Copy SVG** embeds the full scene JSON in the SVG (via `exportEmbedScene`), so pasting that SVG into excalidraw.com restores the diagram exactly. (This skill only ingests the JSON elements array, not SVG — if a user pastes SVG back, extract the embedded `<!-- payload-start -->...` JSON and use that as the new elements source.)

## Element format

Elements go through `convertToExcalidrawElements`, which accepts a "skeleton" form simpler than raw Excalidraw JSON. Required per element: `type`, `x`, `y`, `width`, `height`. Give each element a unique `id` if anything references it (we pass `regenerateIds: false` so ids survive).

Sensible defaults are applied: `strokeColor="#1e1e1e"`, `backgroundColor="transparent"`, `fillStyle="solid"`, `strokeWidth=2`, `roughness=1`, `opacity=100`. Canvas is white.

### Shapes

- **Rectangle / Ellipse / Diamond**: `{ "type": "rectangle", "id": "r1", "x": 100, "y": 100, "width": 200, "height": 80 }`.
  - `"roundness": { "type": 3 }` for rounded corners.
  - `"backgroundColor": "#a5d8ff", "fillStyle": "solid"` for filled.
  - **Add a label** with `"label": { "text": "Hello", "fontSize": 20 }` — auto-centered, no separate text element needed. Works on rectangle, ellipse, diamond, arrow.
- **Standalone text**: `{ "type": "text", "x": 150, "y": 138, "text": "Title", "fontSize": 24 }`. `x` is the left edge. To center at `cx`: width ≈ `text.length * fontSize * 0.5`, so `x = cx - text.length * fontSize * 0.25`.
- **Arrow / Line**: `{ "type": "arrow", "id": "a1", "x": 300, "y": 150, "width": 200, "height": 0, "points": [[0,0],[200,0]], "endArrowhead": "arrow" }`. `points` are `[dx,dy]` offsets from `x,y`. `endArrowhead` ∈ `null | "arrow" | "bar" | "dot" | "triangle"`. Use `"line"` for an unarrowed connector.

### Arrow bindings (preferred shorthand)

Attach an arrow to shapes by referencing their `id`s. `convertToExcalidrawElements` synthesizes the full bindings (focus, gap) and updates the shapes' `boundElements`:

```json
{ "type": "arrow", "id": "a1", "x": 100, "y": 50, "width": 200, "height": 0,
  "points": [[0,0],[200,0]], "endArrowhead": "arrow",
  "start": { "id": "r1" }, "end": { "id": "r2" } }
```

Referenced ids must exist or the binding is silently dropped and the arrow floats.

## Color palette

Primary strokes: `#4a9eed` blue, `#f59e0b` amber, `#22c55e` green, `#ef4444` red, `#8b5cf6` purple, `#ec4899` pink, `#06b6d4` cyan, `#84cc16` lime.

Pastel fills (use with `fillStyle: "solid"`): `#a5d8ff` (input/source), `#b2f2bb` (success/output), `#ffd8a8` (warning/external), `#d0bfff` (processing), `#ffc9c9` (error), `#fff3bf` (notes/decisions), `#c3fae8` (storage), `#eebefa` (analytics).

Background zones (use `opacity: 30`): `#dbe4ff` UI layer, `#e5dbff` logic layer, `#d3f9d8` data layer.

## Sizing

Diagrams display at ~700px wide. Design the bounding box to be roughly 4:3 so the canvas isn't squished.

- Common scene extents: 400×300 (close-up), 600×450 (section), 800×600 (default), 1200×900 (overview — keep fonts ≥18pt).
- Font minimums: 16 body/labels, 20 titles, 14 annotations only. Below 14 is unreadable at display scale.
- Element minimums: 120×60 for labeled boxes. Leave ≥20–30 px gaps.
- Array order is z-order: earlier elements draw first (behind). Background zones → shapes → arrows → top-level text / decorative icons.

## Worked example

Two connected labeled boxes:

```json
[
  { "type": "rectangle", "id": "b1", "x": 100, "y": 100, "width": 200, "height": 100,
    "roundness": { "type": 3 }, "backgroundColor": "#a5d8ff", "fillStyle": "solid",
    "label": { "text": "Start", "fontSize": 20 } },
  { "type": "rectangle", "id": "b2", "x": 450, "y": 100, "width": 200, "height": 100,
    "roundness": { "type": 3 }, "backgroundColor": "#b2f2bb", "fillStyle": "solid",
    "label": { "text": "End", "fontSize": 20 } },
  { "type": "arrow", "id": "a1", "x": 300, "y": 150, "width": 150, "height": 0,
    "points": [[0,0],[150,0]], "endArrowhead": "arrow",
    "start": { "id": "b1" }, "end": { "id": "b2" } }
]
```

## Common pitfalls

- **Overlapping elements**: check coordinates so labels, boxes, and zones don't stack.
- **Title centering**: `x` is the left edge. Use the formula above; don't trust `textAlign` for positioning (it only affects multi-line wrap).
- **Long arrow labels** overflow short arrows — shorten the label or widen the arrow.
- **Aspect ratio**: a very tall or very wide bounding box squishes into the iframe. Rearrange or pad to roughly 4:3.
- **Broken bindings**: arrow `start`/`end` ids must match an existing shape's `id`.
