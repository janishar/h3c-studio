// h3 studio canvas sizing — pure helpers, no DOM. Loaded before app.js and
// also require()-able from Node for tests (static/canvas.test.js).
//
// H3 renders on a 32-pixel grid, at most 768 × 1344 pixels, and its video VAE
// compresses 16× spatially.

"use strict";

const H3_ASPECTS = [
  ["16:9", "16:9 · Widescreen", 16 / 9],
  ["9:16", "9:16 · Vertical", 9 / 16],
  ["1:1", "1:1 · Square", 1],
  ["4:3", "4:3 · Standard", 4 / 3],
  ["3:4", "3:4 · Portrait", 3 / 4],
  ["3:2", "3:2 · Photo", 3 / 2],
  ["2:3", "2:3 · Portrait photo", 2 / 3],
  ["21:9", "21:9 · Ultrawide", 21 / 9],
];

const H3_GRID = 32;
const H3_LATENT_RATIO = 16;
const H3_MAX_PIXELS = 768 * 1344;
const CANVAS_MIN_MP = 0.1;
const CANVAS_MAX_MP = 1.03;

/** Nearest legal dimension on the grid (at least one cell). */
function snapDimension(value, grid = H3_GRID) {
  return Math.max(grid, Math.round(value / grid) * grid);
}

/**
 * Legal width/height closest to `ratio` at about `megapixels`.
 *
 * Scans widths and heights on the grid around the ideal (the other side
 * rounded to the grid), so a ratio and its inverse give mirrored sizes, and
 * scores weighted aspect error plus relative pixel-count error. Sizes above
 * maxPixels are skipped.
 */
function solveCanvas(ratio, megapixels, { grid = H3_GRID, maxPixels = H3_MAX_PIXELS, aspectWeight = 12 } = {}) {
  if (!(ratio > 0) || !(megapixels > 0)) return null;
  const target = Math.min(megapixels * 1e6, maxPixels);
  const idealW = Math.sqrt(target * ratio);
  const idealH = idealW / ratio;
  const candidates = [];
  for (let k = Math.round(idealW / grid) - 8; k <= Math.round(idealW / grid) + 8; k++) {
    if (k >= 1) candidates.push([k * grid, snapDimension((k * grid) / ratio, grid)]);
  }
  for (let k = Math.round(idealH / grid) - 8; k <= Math.round(idealH / grid) + 8; k++) {
    if (k >= 1) candidates.push([snapDimension(k * grid * ratio, grid), k * grid]);
  }
  let best = null;
  for (const [width, height] of candidates) {
    const pixels = width * height;
    if (pixels > maxPixels) continue;
    const score = aspectWeight * Math.abs(Math.log(width / height / ratio)) + Math.abs(pixels - target) / target;
    if (!best || score < best.score - 1e-12) best = { width, height, pixels, score };
  }
  return best;
}

/** Internal render size for a scale factor (1 = full size → null). */
function internalRenderSize(width, height, scale) {
  if (!(scale < 1) || !width || !height) return null;
  return solveCanvas(width / height, (width * height / 1e6) * scale * scale);
}

/** Key of the named aspect ratio within `tolerance` of width/height, or null. */
function matchAspect(width, height, tolerance = 0.03) {
  if (!width || !height) return null;
  const found = H3_ASPECTS.find(([, , r]) => Math.abs(width / height / r - 1) <= tolerance);
  return found ? found[0] : null;
}

function aspectRatioFor(key) {
  const found = H3_ASPECTS.find(([k]) => k === key);
  return found ? found[2] : null;
}

/** Relative aspect-ratio difference between an image and the canvas (0 = same). */
function aspectMismatch(imageWidth, imageHeight, width, height) {
  if (!imageWidth || !imageHeight || !width || !height) return 0;
  return Math.abs(Math.log((imageWidth / imageHeight) / (width / height)));
}

function latentSize(width, height) {
  return [Math.floor(width / H3_LATENT_RATIO), Math.floor(height / H3_LATENT_RATIO)];
}

function clampMegapixels(mp) {
  return Math.min(CANVAS_MAX_MP, Math.max(CANVAS_MIN_MP, Math.round(mp * 100) / 100));
}

if (typeof module !== "undefined") {
  module.exports = {
    H3_ASPECTS, H3_GRID, H3_LATENT_RATIO, H3_MAX_PIXELS, CANVAS_MIN_MP, CANVAS_MAX_MP,
    snapDimension, solveCanvas, internalRenderSize, matchAspect, aspectRatioFor, aspectMismatch,
    latentSize, clampMegapixels,
  };
}
