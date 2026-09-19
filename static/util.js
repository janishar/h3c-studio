/* h3 studio — shared state and DOM/API helpers (loaded first). */

"use strict";

const $ = (id) => document.getElementById(id);
const H3_FPS = 24;
const STAGES = [["load", "Load"], ["encode", "Encode"], ["denoise", "Denoise"], ["decode", "Decode"], ["mp4", "MP4"]];

const state = {
  cfg: null,
  session: null,
  sessions: [],
  legalFrames: Array.from({ length: 22 }, (_, n) => 5 + 17 * n),
  maxPixels: 768 * 1344,
  mode: "ref",           // "text" | "anchor" | "ref"
  runMode: "oneshot",    // "oneshot" | "interactive"
  refs: [],              // {id, name, kind, mode, pairedAudio, duration}
  first: null,
  last: null,
  inputs: [],
  takes: [],
  timeline: [],
  selected: null,
  selectedTimeline: null,
  selectedSequence: null,  // the helmstudio sequence the viewer is playing
  queue: [],
  runningId: null,
  interactive: { loaded: false },
  promptDoc: [{ type: "text", value: "" }],
  mention: null,
  canvas: { aspect: "16:9", customRatio: 16 / 9 },
  followPreview: true,
  livePreview: null,
  scrub: null,
  previewQueue: [],
  previewQueueStep: null,
  previewLastShownAt: 0,
  previewTimer: null,
  compare: [],           // take names picked for grid / A/B wipe
  compareBatch: null,    // {ids: Set, outputs: []} for "Queue 3 seeds"
  batchOpening: false,   // the seed grid is about to open; don't auto-select a take
  serverErrors: [],
  restoring: false,
};

/** Create an element. attrs: class, text, dataset, on* handlers, properties, attributes. */
function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else if (key === "dataset") Object.assign(node.dataset, value);
    else if (key === "style" && typeof value === "object") Object.assign(node.style, value);
    else if (key.startsWith("on") && typeof value === "function") node.addEventListener(key.slice(2), value);
    else if (typeof value !== "string" && key in node) node[key] = value;
    else node.setAttribute(key, value === true ? "" : String(value));
  }
  for (const child of children.flat()) {
    if (child === null || child === undefined || child === false) continue;
    node.append(child instanceof Node ? child : document.createTextNode(String(child)));
  }
  return node;
}

/** GET when body is undefined, otherwise POST JSON. Throws Error(message) with .data. */
async function api(path, body) {
  const opts = body === undefined ? {} : {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
  };
  const res = await fetch(path, opts);
  const data = await res.json().catch(() => ({}));
  if (!res.ok || data.error) {
    const message = data.error || (Array.isArray(data.errors) ? data.errors.join("\n") : "") || `HTTP ${res.status}`;
    const error = new Error(message);
    error.data = data;
    throw error;
  }
  return data;
}

/** Debounced call; .flush() runs a pending call immediately with its arguments. */
function debounce(fn, ms) {
  let timer = null;
  let pending = null;
  const wrapped = (...args) => {
    pending = args;
    clearTimeout(timer);
    timer = setTimeout(() => { const call = pending; pending = null; fn(...call); }, ms);
  };
  wrapped.flush = () => {
    clearTimeout(timer);
    if (pending) { const call = pending; pending = null; return fn(...call); }
    return undefined;
  };
  return wrapped;
}

function fmtSecs(seconds) {
  const s = Math.max(0, Math.round(seconds || 0));
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${String(s % 60).padStart(2, "0")}s`;
  return `${Math.floor(m / 60)}h ${String(m % 60).padStart(2, "0")}m`;
}

function fmtBytes(bytes) {
  if (!bytes) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  const i = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  return `${(bytes / 1024 ** i).toFixed(i >= 3 ? 1 : 0)} ${units[i]}`;
}

function randomSeed() { return Math.floor(Math.random() * 2 ** 31); }

function uid() { return crypto.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`; }

/** URL of a file inside the current session: mediaURL("inputs/x.png"). */
function mediaURL(rel, session = state.session) {
  return `/media/${encodeURIComponent(session)}/${rel.split("/").map(encodeURIComponent).join("/")}`;
}

/** Split an argument string shell-style (single/double quotes, backslash). */
function splitArgs(text) {
  const out = [];
  let current = "", quote = null, has = false;
  for (let i = 0; i < (text || "").length; i++) {
    const c = text[i];
    if (quote) {
      if (c === quote) quote = null;
      else if (c === "\\" && quote === '"' && i + 1 < text.length) current += text[++i];
      else current += c;
    } else if (c === "'" || c === '"') { quote = c; has = true; }
    else if (c === "\\" && i + 1 < text.length) { current += text[++i]; has = true; }
    else if (/\s/.test(c)) { if (has || current) out.push(current); current = ""; has = false; }
    else { current += c; has = true; }
  }
  if (has || current) out.push(current);
  return out;
}

/** Transient notification. kind: "info" | "error" | "ok". */
function toast(message, { kind = "info", hint = "", timeout = 7000 } = {}) {
  const box = el("div", { class: `toast ${kind}`, role: kind === "error" ? "alert" : "status" },
    el("div", { class: "toast-msg", text: message }),
    hint ? el("div", { class: "toast-hint", text: hint }) : null,
    el("button", { class: "toast-close", type: "button", title: "Dismiss", text: "×", onclick: () => box.remove() }));
  $("toasts").append(box);
  if (timeout) setTimeout(() => box.remove(), kind === "error" ? timeout * 2 : timeout);
  return box;
}

/** True when a keyboard event comes from something the user is typing in. */
function isTyping(target) {
  if (!target) return false;
  const tag = target.tagName;
  return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || target.isContentEditable;
}

function safeStorage(fn, fallback) {
  try { return fn(); } catch (e) { return fallback; }
}
