// Tests for static/canvas.js — run with `node --test static/`.
"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const c = require("./canvas.js");

test("solved sizes are on the 32px grid and under the pixel ceiling", () => {
  for (const [, , ratio] of c.H3_ASPECTS) {
    for (let mp = c.CANVAS_MIN_MP; mp <= c.CANVAS_MAX_MP; mp += 0.07) {
      const s = c.solveCanvas(ratio, mp);
      assert.ok(s, `no size for ratio ${ratio} at ${mp} MP`);
      assert.equal(s.width % c.H3_GRID, 0);
      assert.equal(s.height % c.H3_GRID, 0);
      assert.ok(s.width * s.height <= c.H3_MAX_PIXELS);
    }
  }
});

test("a ratio and its inverse give mirrored sizes", () => {
  for (const mp of [0.26, 0.5, 0.9]) {
    const wide = c.solveCanvas(16 / 9, mp);
    const tall = c.solveCanvas(9 / 16, mp);
    assert.deepEqual([wide.width, wide.height], [tall.height, tall.width]);
  }
});

test("native canvases are reachable", () => {
  const s = c.solveCanvas(1344 / 768, 1.03);
  assert.deepEqual([s.width, s.height], [1344, 768]);
  assert.deepEqual([c.solveCanvas(1, 0.262144).width, c.solveCanvas(1, 0.262144).height], [512, 512]);
});

test("size grows with megapixels", () => {
  let last = 0;
  for (let mp = 0.1; mp <= 1.03; mp += 0.05) {
    const s = c.solveCanvas(16 / 9, mp);
    assert.ok(s.pixels >= last, `pixels dropped at ${mp} MP`);
    last = s.pixels;
  }
});

test("internal render size scales area and keeps the grid", () => {
  assert.equal(c.internalRenderSize(768, 1344, 1), null);
  const s = c.internalRenderSize(768, 1344, 0.75);
  assert.equal(s.width % 32, 0);
  assert.ok(Math.abs(s.pixels / (768 * 1344) - 0.5625) < 0.08);
});

test("aspect helpers", () => {
  assert.equal(c.matchAspect(1920, 1080), "16:9");
  assert.equal(c.matchAspect(1000, 1000), "1:1");
  assert.equal(c.matchAspect(1000, 170), null);
  assert.equal(c.aspectRatioFor("4:3"), 4 / 3);
  assert.equal(c.aspectMismatch(1920, 1080, 1344, 768) < Math.log(1.03), true);
  assert.equal(c.aspectMismatch(1080, 1920, 512, 512) > Math.log(1.03), true);
  assert.deepEqual(c.latentSize(480, 864), [30, 54]);
  assert.equal(c.clampMegapixels(5), c.CANVAS_MAX_MP);
  assert.equal(c.snapDimension(500), 512);
});
