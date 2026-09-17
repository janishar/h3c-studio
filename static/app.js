/* h3 studio client — form, render bar, progress, terminal, sessions, model. */

"use strict";

const QUALITY = [
  ["Draft", { steps: 4, layers: 50, reuse: 1, token: false }],
  ["Fast", { steps: 20, layers: 45, reuse: 2, token: true }],
  ["Default", { steps: 20, layers: 45, reuse: 2, token: false }],
  ["Reference", { steps: 50, layers: 50, reuse: 1, token: false }],
];
const INTERNAL_SCALES = [1, 0.75, 0.625];
const THEME_KEY = "h3studio-theme";
const NOTIFY_KEY = "h3studio-notify";

/* ── theme ──────────────────────────────────────────────────────── */

function applyTheme(mode) {
  [...$("themeSwitch").children].forEach((b) => b.classList.toggle("on", b.dataset.theme === mode));
  // Under helmstudio the launcher's theme wins; the switch is hidden while it does.
  if (document.documentElement.hasAttribute("data-helm-theme-follows")) return;
  if (mode === "light" || mode === "dark") document.documentElement.dataset.theme = mode;
  else delete document.documentElement.dataset.theme;
}

/* ── canvas ─────────────────────────────────────────────────────── */

function buildCanvasControls() {
  $("aspectSelect").replaceChildren(
    ...H3_ASPECTS.map(([key, label]) => el("option", { value: key, text: label })),
    el("option", { value: "input", text: "Match input" }),
    el("option", { value: "custom", text: "Custom" }));
  $("qualityPresets").replaceChildren(...QUALITY.map(([label, q]) => el("button", {
    type: "button", text: label,
    onclick: () => {
      $("steps").value = q.steps; $("layers").value = q.layers;
      $("reuse").value = q.reuse; $("tokenReduction").checked = q.token;
      sync();
    },
  })));
}

/** The image whose aspect "Match input" follows: first anchor, else first image reference. */
function aspectSourceInput() {
  const name = state.mode === "anchor" ? (state.first || state.last)
    : state.mode === "ref" ? state.refs.find((ref) => ref.kind === "image")?.name : null;
  const input = name && state.inputs.find((item) => item.name === name);
  return input?.probe?.width ? input : null;
}

function currentRatio() {
  const { aspect, customRatio } = state.canvas;
  if (aspect === "input") {
    const source = aspectSourceInput();
    if (source) return source.probe.width / source.probe.height;
  }
  return aspectRatioFor(aspect) || customRatio || (+$("width").value / +$("height").value) || 1;
}

function fitCanvas() {
  const result = solveCanvas(currentRatio(), +$("megapixels").value);
  if (!result) return;
  $("width").value = result.width;
  $("height").value = result.height;
}

function onAspectChange() {
  const key = $("aspectSelect").value;
  if (key === "input" && !aspectSourceInput()) {
    toast("Add an anchor or image reference first — Match input follows its aspect ratio.");
    $("aspectSelect").value = state.canvas.aspect;
    return;
  }
  if (key === "custom") state.canvas.customRatio = +$("width").value / +$("height").value;
  state.canvas.aspect = key;
  if (key !== "custom") fitCanvas();
  sync();
}

function onDimensionsTyped() {
  const w = snapDimension(+$("width").value || 32);
  const h = snapDimension(+$("height").value || 32);
  $("width").value = w;
  $("height").value = h;
  state.canvas.aspect = matchAspect(w, h, 0.005) || "custom";
  state.canvas.customRatio = w / h;
  $("megapixels").value = clampMegapixels((w * h) / 1e6);
  sync();
}

function nearestInternal(scale) {
  return String(INTERNAL_SCALES.reduce((best, s) => (Math.abs(s - scale) < Math.abs(best - scale) ? s : best), 1));
}

function renderCanvasReadout(p) {
  const px = p.width * p.height;
  $("aspectSelect").value = state.canvas.aspect;
  $("megapixelsValue").textContent = `${(+$("megapixels").value).toFixed(2)} MP`;
  $("resolvedDimensions").textContent = `${p.width} × ${p.height}`;
  $("actualMegapixels").textContent = `${(px / 1e6).toFixed(2)} MP`;
  $("actualRatio").textContent = (p.width / p.height).toFixed(3);
  const [lw, lh] = latentSize(p.width, p.height);
  $("latentSize").textContent = `${lw} × ${lh}`;
  $("sizeWarn").hidden = px <= state.maxPixels;
  $("sizeWarn").textContent = `${px.toLocaleString()} pixels exceeds the ${state.maxPixels.toLocaleString()} ceiling.`;
  const source = state.mode === "anchor" ? aspectSourceInput() : null;
  const mismatch = source ? aspectMismatch(source.probe.width, source.probe.height, p.width, p.height) : 0;
  $("aspectWarn").hidden = mismatch <= Math.log(1.03);
  if (!$("aspectWarn").hidden) {
    $("aspectWarn").textContent = `${source.name} is ${source.probe.width}×${source.probe.height} ` +
      `(${(source.probe.width / source.probe.height).toFixed(2)}) but the canvas is ${(p.width / p.height).toFixed(2)} — ` +
      "the anchor will be stretched. Choose Match input.";
  }
  const s = +$("steps").value, l = +$("layers").value, r = +$("reuse").value;
  [...$("qualityPresets").children].forEach((b, i) => {
    const q = QUALITY[i][1];
    b.classList.toggle("on", q.steps === s && q.layers === l && q.reuse === r);
  });
}

/* ── params ─────────────────────────────────────────────────────── */

function currentFrames() { return state.legalFrames[+$("frames").value] || state.legalFrames[1]; }

function params() {
  const w = +$("width").value, h = +$("height").value;
  const scale = +$("internal").value;
  const p = {
    session_name: state.session,
    label: $("label").value.trim(),
    mode: state.mode,
    prompt_doc: state.promptDoc,
    width: w, height: h,
    frames: currentFrames(),
    steps: +$("steps").value,
    layers: +$("layers").value,
    reuse: +$("reuse").value,
    seed: +$("seed").value,
    token_reduction: $("tokenReduction").checked,
    int8_row_fc2: $("int8RowFc2").checked,
    ssd_streaming: $("ssdStreaming").checked,
    run_mode: state.runMode,
    preview: $("previewToggle").checked,
    previewAllFrames: $("previewModeAll").checked,
    refs: state.mode === "ref" ? state.refs.map((ref) => ({ ...ref })) : [],
    env: {},
    extra_args: splitArgs($("extraArgs").value),
  };
  try {
    p.prompt = resolvePrompt(state.promptDoc, p.refs);
  } catch (error) {
    p.prompt = "";
    p.prompt_error = error.message;
  }
  const internal = internalRenderSize(w, h, scale);
  if (internal) { p.render_width = internal.width; p.render_height = internal.height; }
  if (state.mode === "anchor") {
    if (state.first) p.first_frame = state.first;
    if (state.last) p.last_frame = state.last;
  }
  if ($("zeroCopy").checked) p.env.H3_ZERO_COPY_WEIGHTS = "0";
  if ($("prefetchDepth").value) p.env.H3_QWEN_PREFETCH_DEPTH = $("prefetchDepth").value;
  if ($("prefetchWorkers").value) p.env.H3_QWEN_PREFETCH = $("prefetchWorkers").value;
  return p;
}

/** Everything the form holds, including conditioning of the modes not in use. */
function snapshot() {
  return {
    ...params(),
    ui: {
      refs: state.refs, first: state.first, last: state.last,
      aspect: state.canvas.aspect, customRatio: state.canvas.customRatio,
      megapixels: +$("megapixels").value, internal: +$("internal").value,
      extraArgs: $("extraArgs").value,
    },
  };
}

function restore(p) {
  if (!p || typeof p !== "object") return;
  state.restoring = true;
  const ui = p.ui || {};
  state.promptDoc = Array.isArray(p.prompt_doc) && p.prompt_doc.length ? p.prompt_doc : [{ type: "text", value: p.prompt || "" }];
  $("label").value = p.label || "";
  const w = p.width || 512, h = p.height || 512;
  $("width").value = w;
  $("height").value = h;
  state.canvas.aspect = ui.aspect || matchAspect(w, h, 0.005) || "custom";
  state.canvas.customRatio = ui.customRatio || w / h;
  $("megapixels").value = clampMegapixels(ui.megapixels ?? (w * h) / 1e6);
  $("internal").value = ui.internal ? nearestInternal(ui.internal) : p.render_width ? nearestInternal(p.render_width / w) : "1";
  $("steps").value = p.steps || 4;
  $("layers").value = p.layers || 50;
  $("reuse").value = p.reuse || 1;
  $("seed").value = p.seed ?? 42;
  const frameIndex = state.legalFrames.indexOf(p.frames);
  $("frames").value = frameIndex >= 0 ? frameIndex : Math.max(0, state.legalFrames.findIndex((f) => f >= (p.frames || 22)));
  $("tokenReduction").checked = !!p.token_reduction;
  $("int8RowFc2").checked = !!p.int8_row_fc2;
  $("ssdStreaming").checked = !!p.ssd_streaming;
  $("previewToggle").checked = p.preview !== false;
  $(p.previewAllFrames ? "previewModeAll" : "previewModeSingle").checked = true;
  $("zeroCopy").checked = p.env ? p.env.H3_ZERO_COPY_WEIGHTS === "0" : true;
  $("prefetchDepth").value = p.env?.H3_QWEN_PREFETCH_DEPTH || "";
  $("prefetchWorkers").value = p.env?.H3_QWEN_PREFETCH || "";
  $("extraArgs").value = ui.extraArgs ?? (p.extra_args || []).join(" ");
  const refs = Array.isArray(ui.refs) ? ui.refs : Array.isArray(p.refs) ? p.refs : [];
  state.refs = refs.map((ref, i) => ({
    id: ref.id || `${Date.now()}-${i}`, name: ref.name, kind: ref.kind,
    mode: ref.kind === "video" ? (ref.mode || "keep") : undefined,
    pairedAudio: ref.pairedAudio || null, duration: ref.duration ?? null,
  }));
  state.first = ui.first !== undefined ? ui.first : p.first_frame || null;
  state.last = ui.last !== undefined ? ui.last : p.last_frame || null;
  const mode = p.mode === "text" || p.mode === "anchor" || p.mode === "ref" ? p.mode
    : state.refs.length ? "ref" : state.first || state.last ? "anchor" : "text";
  setMode(mode);
  setRunMode(p.run_mode === "interactive" ? "interactive" : "oneshot");
  renderRefs();
  renderAnchors();
  renderPromptEditor();
  syncPreviewModeEnabled();
  state.restoring = false;
  sync();
}

function localErrors(p) {
  const e = [];
  if (p.width % 32 || p.height % 32) e.push("Width and height must be multiples of 32.");
  if (p.width * p.height > state.maxPixels) e.push(`${p.width}×${p.height} exceeds the ${state.maxPixels.toLocaleString()}-pixel ceiling.`);
  if (p.prompt_error) e.push(p.prompt_error);
  else if (!p.prompt.trim()) e.push("Write a prompt.");
  if (state.mode === "ref" && !state.refs.length) e.push("Reference mode needs at least one reference.");
  if (state.mode === "anchor" && !state.first && !state.last) e.push("Set a first or last frame, or switch to Prompt mode.");
  if (state.refs.some((ref) => state.mode === "ref" && ref.kind === "video" && ref.mode === "replace" && !ref.pairedAudio)) {
    e.push("Choose replacement audio for every video in replace mode.");
  }
  return e;
}

/* ── sync ───────────────────────────────────────────────────────── */

const saveSettings = debounce((session, settings) => {
  api("/api/session/save", { session, settings }).catch((err) => toast(`Could not save the session: ${err.message}`, { kind: "error" }));
}, 400);

let commandSeq = 0;
const refreshCommand = debounce(async (p) => {
  const seq = ++commandSeq;
  try {
    const data = await api("/api/command", p);
    if (seq !== commandSeq) return;
    state.serverErrors = data.errors || [];
    $("cmdPreview").textContent = data.launch ? `# h3.c launch\n${data.launch}\n\n# per render\n${data.display}` : data.display;
    const est = data.estimate || {};
    $("estimate").textContent = est.seconds
      ? `≈ ${fmtSecs(est.seconds)}${est.exact ? "" : " (scaled)"} · ${est.samples} take${est.samples === 1 ? "" : "s"}`
      : "";
    $("estimate").title = est.seconds ? "Estimated from this session's finished takes" : "";
  } catch (err) {
    if (seq !== commandSeq) return;
    state.serverErrors = err.data?.errors || [err.message];
  }
  renderErrors(p);
}, 250);

function renderErrors(p = params()) {
  const errors = [...new Set([...localErrors(p), ...(state.serverErrors || [])])];
  $("errors").hidden = !errors.length;
  $("errors").textContent = errors.join("\n");
  $("render").disabled = errors.length > 0;
  $("queueBtn").disabled = errors.length > 0;
}

function sync() {
  if (state.restoring || !state.session) return;
  const p = params();
  const n = p.frames;
  $("frameCount").textContent = n;
  $("frameSecs").textContent = `${(n / H3_FPS).toFixed(2)} s`;
  $("promptCount").textContent = `${p.prompt.length} chars`;
  renderCanvasReadout(p);
  state.serverErrors = [];
  renderErrors(p);
  saveSettings(state.session, snapshot());
  refreshCommand(p);
}

function syncPreviewModeEnabled() {
  $("previewModeGroup").classList.toggle("disabled", !$("previewToggle").checked);
}

/* ── references ─────────────────────────────────────────────────── */

async function loadInputs() {
  if (!state.session) return;
  state.inputs = await api(`/api/inputs?session=${encodeURIComponent(state.session)}`);
  renderLibrary();
  renderRefs();
}

function renderRefs() {
  const counts = { image: 0, video: 0, audio: 0 };
  const durations = { video: 0, audio: 0 };
  state.refs.forEach((ref) => {
    counts[ref.kind]++;
    if (ref.kind in durations) durations[ref.kind] += Number(ref.duration) || 0;
  });
  $("refList").replaceChildren(...state.refs.map((ref, i) => {
    const audioFiles = state.inputs.filter((file) => file.kind === "audio");
    const li = el("li", { draggable: true, dataset: { kind: ref.kind } },
      el("button", { class: "n", type: "button", text: slotLabel(state.refs, i), title: "Insert this label at the prompt caret",
        onclick: (e) => { e.stopPropagation(); insertRefAtCaret(ref); } }),
      ref.kind === "image" ? el("img", { src: mediaURL(`inputs/${ref.name}`), alt: "" }) : null,
      ref.duration != null ? el("span", { class: "duration", text: `${Number(ref.duration).toFixed(1)}s` }) : null,
      ref.kind === "video" ? el("select", { class: "refmode", "aria-label": "Video audio handling",
        onchange: (e) => { ref.mode = e.target.value; renderRefs(); sync(); } },
        [["keep", "keep audio"], ["silent", "silent"], ["replace", "replace audio"]].map(([value, text]) =>
          el("option", { value, text, selected: (ref.mode || "keep") === value }))) : null,
      ref.kind === "video" && ref.mode === "replace" ? el("select", { class: "refmode", "aria-label": "Replacement audio",
        onchange: (e) => { ref.pairedAudio = e.target.value || null; sync(); } },
        el("option", { value: "", text: "audio file…" }),
        audioFiles.map((file) => el("option", { value: file.name, text: file.name, selected: ref.pairedAudio === file.name }))) : null,
      el("span", { class: "nm", text: ref.name, title: ref.name }),
      el("button", { type: "button", text: "×", title: "Remove reference",
        onclick: (e) => { e.stopPropagation(); state.refs.splice(i, 1); renderRefs(); renderPromptEditor(); sync(); } }));
    li.addEventListener("dragstart", (e) => { li.classList.add("dragging"); e.dataTransfer.setData("text/plain", String(i)); });
    li.addEventListener("dragend", () => li.classList.remove("dragging"));
    li.addEventListener("dragover", (e) => e.preventDefault());
    li.addEventListener("drop", (e) => {
      e.preventDefault();
      const from = +e.dataTransfer.getData("text/plain");
      if (Number.isNaN(from) || from === i) return;
      const [moved] = state.refs.splice(from, 1);
      state.refs.splice(i, 0, moved);
      renderRefs(); renderPromptEditor(); sync();
    });
    return li;
  }));
  $("refHint").textContent = `Pictures ${counts.image}/9 · Videos ${counts.video}/3 (${durations.video.toFixed(1)}/15.0s) · ` +
    `Audio ${counts.audio}/3 (${durations.audio.toFixed(1)}/15.0s) · Order sets <Picture N>; drag to reorder, click a label to insert it.`;
}

function renderLibrary() {
  $("library").replaceChildren(...state.inputs.map((file) => {
    const probe = file.probe || {};
    const details = [file.name, probe.width ? `${probe.width}×${probe.height}` : "", file.duration ? `${file.duration.toFixed(1)}s` : ""].filter(Boolean).join(" · ");
    const used = state.refs.some((ref) => ref.name === file.name) || state.first === file.name || state.last === file.name;
    return el("figure", { class: used ? "used" : "", title: `${details}\nClick to use`, onclick: () => addRef(file) },
      file.kind === "image" ? el("img", { src: file.url, alt: file.name, loading: "lazy" })
        : file.kind === "video" ? el("img", { src: file.thumb, alt: file.name, loading: "lazy" })
          : el("div", { class: "nonimg", text: `♪ ${file.name}` }),
      file.kind !== "image" ? el("button", { class: "play-input", type: "button", title: `Play ${file.name}`, text: "▶",
        onclick: (e) => { e.stopPropagation(); state.selected = null; showVideo(file.url, details); renderTakes(); } }) : null,
      file.duration ? el("span", { class: "badge", text: `${file.duration.toFixed(1)}s` }) : null,
      el("button", { class: "delete-file", type: "button", title: "Delete input", text: "×",
        onclick: (e) => { e.stopPropagation(); deleteMedia(file.name, "input").catch((err) => toast(err.message, { kind: "error" })); } }));
  }));
}

/** Put an input into the current mode's next slot. Returns true when added. */
function addRef(file) {
  if (state.mode === "text") {
    toast("Prompt mode has no reference slots — switch to Anchors or References, or mention the file with @.");
    return false;
  }
  if (state.mode === "anchor") {
    if (file.kind !== "image") { toast("Anchors must be images.", { kind: "error" }); return false; }
    if (!state.first) state.first = file.name;
    else state.last = file.name;
    renderAnchors(); renderLibrary(); sync();
    return true;
  }
  if (state.refs.some((ref) => ref.name === file.name)) { toast(`${file.name} is already a reference.`); return false; }
  const count = state.refs.filter((ref) => ref.kind === file.kind).length;
  const limit = file.kind === "image" ? 9 : 3;
  if (count >= limit) { toast(`At most ${limit} ${file.kind} references.`, { kind: "error" }); return false; }
  const duration = Number(file.duration) || 0;
  if ((file.kind === "video" || file.kind === "audio") && duration && (duration < 2 || duration > 15)) {
    if (duration > 15) offerTrim(file);
    else toast(`${file.name} is ${duration.toFixed(1)}s; Ref2VA needs at least 2s.`, { kind: "error" });
    return false;
  }
  if (file.kind === "video" || file.kind === "audio") {
    const used = state.refs.filter((ref) => ref.kind === file.kind).reduce((sum, ref) => sum + (Number(ref.duration) || 0), 0);
    if (used + duration > 15) { toast(`Combined ${file.kind} reference duration can't exceed 15 seconds.`, { kind: "error" }); return false; }
  }
  state.refs.push({
    id: uid(), name: file.name, kind: file.kind, mode: file.kind === "video" ? "keep" : undefined,
    pairedAudio: null, duration: file.duration ?? null,
  });
  renderRefs(); renderLibrary(); sync();
  return true;
}

async function offerTrim(file) {
  const duration = Number(file.duration) || 0;
  const maxStart = Math.max(0, duration - 2);
  const answer = prompt(`${file.name} is ${duration.toFixed(1)}s; Ref2VA references must be 2–15s.\nTrim starting at (seconds, 0–${maxStart.toFixed(1)}):`, "0");
  if (answer === null) return;
  const start = Math.min(Math.max(Number(answer) || 0, 0), maxStart);
  try {
    const data = await api("/api/trim", { session: state.session, name: file.name, start, length: Math.min(14.8, duration - start) });
    state.inputs = data.inputs;
    renderLibrary();
    addRef({ name: data.name, kind: data.kind || file.kind, duration: data.duration });
  } catch (err) {
    toast(err.message, { kind: "error" });
  }
}

function renderAnchors() {
  for (const [id, name] of [["anchorFirst", state.first], ["anchorLast", state.last]]) {
    $(id).querySelector("em").textContent = name || "none";
    $(id).classList.toggle("set", !!name);
  }
}

function setMode(mode) {
  state.mode = mode;
  [...$("modeSwitch").children].forEach((b) => {
    b.classList.toggle("on", b.dataset.mode === mode);
    b.setAttribute("aria-checked", String(b.dataset.mode === mode));
  });
  $("refMode").hidden = mode !== "ref";
  $("anchorMode").hidden = mode !== "anchor";
  $("textModeHint").hidden = mode !== "text";
  const noRef2va = state.cfg && !state.cfg.model_info?.has_ref2va;
  $("modeUnavailable").hidden = !(mode === "ref" && noRef2va);
  $("modeUnavailable").textContent = "References need the Ref2VA pipeline, which the model directory doesn't have. Open Model to check the setup.";
  closeMentionMenu();
  renderLibrary();
}

async function uploadFiles(files) {
  for (const file of files) {
    try {
      const res = await fetch(`/api/upload?session=${encodeURIComponent(state.session)}`, {
        method: "POST", headers: { "X-Filename": encodeURIComponent(file.name) }, body: file,
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok || data.error) throw new Error(data.error || `upload failed (HTTP ${res.status})`);
      state.inputs = data.inputs;
      renderLibrary();
      if (state.mode !== "text") addRef({ name: data.name, kind: data.kind, duration: data.duration, probe: data.probe });
    } catch (err) {
      toast(`${file.name}: ${err.message}`, { kind: "error" });
    }
  }
}

/* ── render ─────────────────────────────────────────────────────── */

async function submit(count = 1) {
  if ($("render").disabled) return;
  saveSettings.flush();
  const base = params();
  const jobs = count === 1 ? [base] : Array.from({ length: count }, (_, i) => ({
    ...base, seed: randomSeed(), label: `${base.label || "take"}-${i + 1}`,
  }));
  try {
    const data = await api("/api/render", { jobs });
    state.followPreview = true;
    if (count > 1) trackCompareBatch(data.jobs.map((job) => job.id));
  } catch (err) {
    $("errors").hidden = false;
    $("errors").textContent = err.message;
  }
}

function setRunMode(mode) {
  state.runMode = mode;
  [...$("runModeSwitch").children].forEach((b) => {
    b.classList.toggle("on", b.dataset.run === mode);
    b.setAttribute("aria-checked", String(b.dataset.run === mode));
  });
  $("render").firstChild.textContent = mode === "interactive" ? "Send to h3.c " : "Render ";
  renderInteractive();
}

function renderInteractive() {
  const loaded = !!state.interactive?.loaded;
  $("interPill").hidden = state.runMode !== "interactive" && !loaded;
  $("interPill").classList.toggle("on", loaded);
  $("interText").textContent = loaded ? "h3.c loaded" : "h3.c not loaded · loads on first send";
  $("interToggle").textContent = loaded ? "Unload" : "Load h3.c";
  $("interactiveForm").hidden = !loaded;
}

async function toggleInteractive() {
  const button = $("interToggle");
  button.disabled = true;
  try {
    if (state.interactive?.loaded) {
      state.interactive = await api("/api/interactive/unload", {});
    } else {
      saveSettings.flush();
      state.interactive = await api("/api/interactive/load", params());
    }
  } catch (err) {
    toast(err.message, { kind: "error" });
  } finally {
    button.disabled = false;
    renderInteractive();
  }
}

/* ── queue and progress ─────────────────────────────────────────── */

let elapsedTimer = null;

function isActive(job) { return job.state === "running" || job.state === "cancelling"; }

function renderQueue(items) {
  state.queue = items;
  const running = items.find(isActive);
  const pending = items.filter((job) => job.state === "queued");
  if (running && state.runningId !== running.id) {
    state.runningId = running.id;
    state.followPreview = true;
    state.livePreview = null;
  }
  if (!running) state.runningId = null;
  $("lamp").classList.toggle("busy", !!running);
  $("lampText").textContent = running ? (pending.length ? `rendering · ${pending.length} queued` : "rendering") : pending.length ? `${pending.length} queued` : "idle";
  $("running").hidden = !running;
  clearInterval(elapsedTimer);
  if (running) {
    renderRunning(running);
    elapsedTimer = setInterval(renderRunningFromQueue, 1000);
  } else {
    document.title = "h3 studio";
  }
  $("queueWrap").hidden = !pending.length;
  $("queue").replaceChildren(...pending.map((job) => el("li", {},
    el("span", { text: `${job.label || "take"} · seed ${job.seed} · ${job.run_mode === "interactive" ? "interactive" : "one-shot"} · queued` }),
    el("button", { class: "ghost sm", type: "button", text: "Remove",
      onclick: () => api("/api/cancel", { id: job.id }).catch((err) => toast(err.message, { kind: "error" })) }))));
}

function renderRunningFromQueue() {
  const running = state.queue.find(isActive);
  if (running) renderRunning(running);
}

/** Rough overall progress for the tab title: stage bands, denoise weighted most. */
function overallPercent(job) {
  const bands = { load: [0, 8], encode: [8, 15], denoise: [15, 85], decode: [85, 95], mp4: [95, 100] };
  const [lo, hi] = bands[job.stage] || [0, 0];
  const [n, total] = job.progress || [0, 0];
  return Math.round(lo + (total ? (hi - lo) * Math.min(1, n / total) : 0));
}

function renderRunning(job) {
  const stageIndex = STAGES.findIndex(([key]) => key === job.stage);
  const [n, total] = job.progress || [0, 0];
  $("stepper").replaceChildren(...STAGES.map(([key, label], i) => el("li", {
    class: i < stageIndex ? "done" : i === stageIndex ? "active" : "",
  }, key === "denoise" && i === stageIndex && total ? `${label} ${n}/${total}` : label)));
  const cancelling = job.state === "cancelling";
  $("phaseName").textContent = `${job.label || "take"} — ${cancelling ? "stopping…" : job.phase || "starting"}`;
  $("phasePct").textContent = total ? `${n}/${total}` : "";
  $("phaseBar").style.width = total ? `${Math.min(100, (100 * n) / total)}%` : "0%";
  $("phaseBar").parentElement.classList.toggle("indeterminate", !total);
  const elapsed = job.started ? Date.now() / 1000 - job.started : 0;
  $("elapsed").textContent = job.started ? `${fmtSecs(elapsed)} elapsed` : "";
  let eta = "";
  if (job.stage === "denoise" && job.eta_s) eta = `≈ ${fmtSecs(job.eta_s)} left in denoise`;
  else if (job.estimate_s && job.estimate_s > elapsed) eta = `≈ ${fmtSecs(job.estimate_s - elapsed)} left (estimate)`;
  $("eta").textContent = eta;
  $("cancel").disabled = cancelling;
  $("cancel").textContent = cancelling ? "Stopping…" : "Stop";
  $("cancel").onclick = () => api("/api/cancel", { id: job.id }).catch((err) => toast(err.message, { kind: "error" }));
  const live = state.livePreview && state.livePreview.id === job.id ? state.livePreview : job.preview_latest;
  $("followPreviewBtn").hidden = !live || state.followPreview;
  $("followPreviewBtn").onclick = () => {
    state.followPreview = true;
    state.selected = null;
    renderTakes();
    queuePreviewFrame({ ...live, id: job.id });
    $("followPreviewBtn").hidden = true;
  };
  document.title = `(${overallPercent(job)}%) h3 studio`;
}

function onJobEvent(job) {
  const index = state.queue.findIndex((item) => item.id === job.id);
  if (isActive(job) || job.state === "queued") {
    if (index >= 0) state.queue[index] = { ...state.queue[index], ...job };
    renderQueue(state.queue);
  }
  noteBatchJob(job);
  if (job.profile?.length) renderProfileTable(job.profile);
  if (job.state === "failed") {
    toast(`${job.label || "take"} failed: ${job.error || "unknown error"}`, { kind: "error", hint: job.hint || "" });
    notify(`Render failed: ${job.label || "take"}`, job.error || "");
  } else if (job.state === "done") {
    notify(`Render finished: ${job.label || "take"}`, job.output || "");
  }
  if (["done", "failed", "cancelled"].includes(job.state) && job.session === state.session) {
    resetPreviewQueue();
    if (job.state !== "done" && state.followPreview && !state.selected) showVideo(null);
  }
}

function notify(title, body) {
  const enabled = safeStorage(() => localStorage.getItem(NOTIFY_KEY) === "1", false);
  if (!enabled || !("Notification" in window) || Notification.permission !== "granted" || !document.hidden) return;
  try { new Notification(title, { body }); } catch (e) { /* notifications unavailable */ }
}

function renderNotifyButton() {
  const on = safeStorage(() => localStorage.getItem(NOTIFY_KEY) === "1", false) && "Notification" in window && Notification.permission === "granted";
  $("notifyButton").classList.toggle("on", on);
  $("notifyButton").setAttribute("aria-pressed", String(on));
  $("notifyButton").title = on ? "Notifications on — click to turn off" : "Notify me when a render finishes";
}

async function toggleNotify() {
  if (!("Notification" in window)) { toast("This browser doesn't support notifications."); return; }
  const on = safeStorage(() => localStorage.getItem(NOTIFY_KEY) === "1", false);
  if (on) {
    safeStorage(() => localStorage.setItem(NOTIFY_KEY, "0"));
  } else {
    const permission = Notification.permission === "granted" ? "granted" : await Notification.requestPermission();
    if (permission !== "granted") { toast("Notifications are blocked for this page in the browser settings."); return; }
    safeStorage(() => localStorage.setItem(NOTIFY_KEY, "1"));
  }
  renderNotifyButton();
}

/* ── terminal ───────────────────────────────────────────────────── */

let lastLogReplaceable = false;

function appendLog(line, replace = false, kind = "") {
  const out = $("terminalOutput");
  if (replace && lastLogReplaceable && out.lastChild) {
    out.lastChild.textContent = `${line}\n`;
  } else {
    out.append(el("span", { class: kind || (line.startsWith("!! ") ? "err" : line.startsWith("$ ") || line.startsWith("h3> ") ? "cmdline" : ""), text: `${line}\n` }));
    while (out.childNodes.length > 4000) out.firstChild.remove();
  }
  lastLogReplaceable = true;
  if ($("followLog").checked) out.scrollTop = out.scrollHeight;
}

function setTerminalLog(text) {
  const out = $("terminalOutput");
  out.replaceChildren();
  lastLogReplaceable = false;
  // Logs written by older versions still contain Kitty image payload lines.
  (text || "").split("\n").filter((line) => line && !line.includes("\u001b_G")).forEach((line) => appendLog(line));
  lastLogReplaceable = false;
}

function renderProfileTable(rows) {
  $("profileTable").querySelector("tbody").replaceChildren(...rows.map((row) => el("tr", {},
    el("td", { text: row.component }), el("td", { text: row.stage }), el("td", { text: `${row.wall.toFixed(2)}s` }))));
}

/**
 * Wall time per component from --profile rows. A component's "total" row
 * already includes its sub-stages, so it wins over summing; a "load" stage is
 * split out so model loading and compute show separately.
 */
function profileByComponent(rows) {
  const groups = {};
  for (const row of rows) (groups[row.component] = groups[row.component] || []).push(row);
  const sums = {};
  for (const [component, items] of Object.entries(groups)) {
    const total = items.find((row) => row.stage === "total");
    const load = items.filter((row) => row.stage === "load").reduce((a, row) => a + row.wall, 0);
    const wall = total ? total.wall : items.reduce((a, row) => a + row.wall, 0);
    if (load > 0 && wall > load) {
      sums[`${component} load`] = load;
      sums[component] = wall - load;
    } else {
      sums[component] = wall;
    }
  }
  return sums;
}

/** Stacked bars of profile wall time per component for recent takes. */
function renderTimingChart() {
  const takes = state.takes.filter((t) => t.meta?.profile?.length).slice(0, 8);
  if (!takes.length) {
    $("timingChart").replaceChildren(el("p", { class: "hint", text: "Timing appears here after a take renders with --profile." }));
    return;
  }
  const totals = {};
  const perTake = takes.map((take) => {
    const sums = profileByComponent(take.meta.profile);
    for (const [key, value] of Object.entries(sums)) totals[key] = (totals[key] || 0) + value;
    return { take, sums, total: Object.values(sums).reduce((a, b) => a + b, 0) };
  });
  const top = Object.entries(totals).sort((a, b) => b[1] - a[1]).slice(0, 6).map(([key]) => key);
  const keys = [...top, "other"];
  const max = Math.max(...perTake.map((row) => row.total), 1);
  const segment = (take, sums) => keys.map((key, i) => {
    const value = key === "other" ? Object.entries(sums).filter(([k]) => !top.includes(k)).reduce((a, [, v]) => a + v, 0) : sums[key] || 0;
    return value > 0 ? el("i", { class: `c${i}`, style: { width: `${(100 * value) / max}%` }, title: `${key}: ${value.toFixed(1)}s` }) : null;
  });
  $("timingChart").replaceChildren(
    el("div", { class: "legend" }, keys.map((key, i) => el("span", {}, el("i", { class: `c${i}` }), key))),
    ...perTake.map(({ take, sums, total }) => el("div", { class: "timing-row", title: take.name },
      el("span", { class: "nm", text: take.name }),
      el("div", { class: "bars" }, segment(take, sums)),
      el("span", { class: "total", text: `${total.toFixed(1)}s` }))));
}

function bindTerminal() {
  $("consoleTabs").addEventListener("click", (e) => {
    const tab = e.target.closest("button[data-tab]")?.dataset.tab;
    if (!tab) return;
    [...$("consoleTabs").children].forEach((b) => b.classList.toggle("on", b.dataset.tab === tab));
    $("terminalOutput").hidden = tab !== "log";
    $("profile").hidden = tab !== "profile";
    if (tab === "profile") renderTimingChart();
  });
  $("clearLog").onclick = () => { $("terminalOutput").replaceChildren(); lastLogReplaceable = false; };
  $("terminalForm").onsubmit = async (e) => {
    e.preventDefault();
    const command = $("terminalCommand").value.trim();
    if (!command) return;
    $("terminalCommand").value = "";
    try { await api("/api/shell", { session: state.session, command }); } catch (err) { appendLog(`!! ${err.message}`); }
  };
  $("interactiveForm").onsubmit = async (e) => {
    e.preventDefault();
    const line = $("interactiveInput").value.trim();
    if (!line) return;
    $("interactiveInput").value = "";
    try { await api("/api/interactive/input", { line }); } catch (err) { appendLog(`!! ${err.message}`); }
  };

  const consoleBox = $("consoleContainer");
  let startY = 0, startHeight = 0, dragging = false;
  const move = (clientY) => { consoleBox.style.height = `${Math.max(150, Math.min(startHeight + (startY - clientY), 640))}px`; };
  $("resizeHandle").addEventListener("pointerdown", (e) => {
    dragging = true; startY = e.clientY; startHeight = consoleBox.offsetHeight;
    $("resizeHandle").setPointerCapture(e.pointerId);
    document.body.classList.add("resizing");
  });
  $("resizeHandle").addEventListener("pointermove", (e) => { if (dragging) move(e.clientY); });
  $("resizeHandle").addEventListener("pointerup", () => { dragging = false; document.body.classList.remove("resizing"); });
}

/* ── events ─────────────────────────────────────────────────────── */

let eventSource = null;

function connect() {
  eventSource?.close();
  const session = state.session;
  const es = new EventSource(`/api/events?session=${encodeURIComponent(session)}`);
  eventSource = es;
  es.onmessage = (event) => {
    const { kind, payload } = JSON.parse(event.data);
    const mine = !payload?.session || payload.session === state.session;
    switch (kind) {
      case "hello":
        setTerminalLog(payload.terminal_log);
        state.interactive = payload.interactive || { loaded: false };
        renderInteractive();
        renderQueue(payload.queue || []);
        break;
      case "queue":
        renderQueue(payload);
        break;
      case "job":
        onJobEvent(payload);
        break;
      case "progress": {
        const job = state.queue.find((item) => item.id === payload.id);
        if (job) { Object.assign(job, payload); renderRunning(job); }
        break;
      }
      case "log":
        if (mine) appendLog(payload.line, payload.replace);
        break;
      case "shell":
        if (mine) appendLog(payload.line);
        $("terminalCommand").disabled = !!payload.running;
        break;
      case "preview":
        if (mine) {
          const job = state.queue.find((item) => item.id === payload.id);
          if (job) { job.preview_latest = payload; job.preview_count = payload.count; }
          queuePreviewFrame(payload);
          renderRunningFromQueue();
        }
        break;
      case "takes":
        if (mine) loadTakes(state.followPreview || !state.selected ? payload.name : null).catch(() => {});
        break;
      case "inputs":
        if (mine) loadInputs().catch(() => {});
        break;
      case "timeline":
        if (mine) loadTimeline().catch(() => {});
        break;
      case "interactive":
        state.interactive = payload;
        renderInteractive();
        break;
      default:
    }
  };
  es.onerror = () => {
    $("lampText").textContent = "reconnecting…";
    if (es.readyState === EventSource.CLOSED) setTimeout(() => { if (eventSource === es) connect(); }, 3000);
  };
}

/* ── sessions ───────────────────────────────────────────────────── */

function renderSessionSelect() {
  $("sessionSelect").replaceChildren(...state.sessions.map((name) => el("option", { value: name, text: name, selected: name === state.session })));
}

async function activateSession(name) {
  saveSettings.flush();
  const data = await api("/api/session/activate", { session: name });
  state.session = data.name;
  state.sessions = data.sessions;
  renderSessionSelect();
  state.selected = null;
  state.selectedTimeline = null;
  state.compare = [];
  state.serverErrors = [];
  showVideo(null);
  await Promise.all([loadInputs(), loadTakes(), loadTimeline()]);
  restore({ ...data.settings });
  connect();
}

function openSessionModal(kind) {
  $("sessionModal").dataset.kind = kind;
  $("sessionModalTitle").textContent = kind === "duplicate" ? `Duplicate ${state.session}` : "New session";
  $("confirmSession").textContent = kind === "duplicate" ? "Duplicate" : "Create";
  $("sessionNameInput").value = kind === "duplicate" ? `${state.session}-copy` : "";
  $("sessionError").hidden = true;
  $("sessionModal").hidden = false;
  $("sessionNameInput").focus();
  $("sessionNameInput").select();
}

async function confirmSessionModal() {
  const name = $("sessionNameInput").value.trim();
  if (!name) { $("sessionError").textContent = "Enter a session name."; $("sessionError").hidden = false; return; }
  try {
    saveSettings.flush();
    if ($("sessionModal").dataset.kind === "duplicate") {
      const data = await api("/api/session/duplicate", { session: state.session, as: name });
      await activateSession(data.name);
    } else {
      if (state.sessions.includes(name)) throw new Error("A session with that name already exists — pick it from the list.");
      await activateSession(name);
    }
    $("sessionModal").hidden = true;
  } catch (err) {
    $("sessionError").textContent = err.message;
    $("sessionError").hidden = false;
  }
}

async function deleteSession() {
  if (!confirm(`Delete session "${state.session}"?\nThis permanently removes its inputs, takes, timeline and settings.`)) return;
  try {
    saveSettings.flush();
    const data = await api("/api/session/delete", { session: state.session });
    state.session = null;
    await activateSession(data.name);
  } catch (err) {
    toast(err.message, { kind: "error" });
  }
}

/* ── model dialog ───────────────────────────────────────────────── */

function renderModelDot(info) {
  const dot = $("modelDot");
  const bad = !info.exists || !info.has_fl2va || !info.h3_runs;
  const warn = !info.has_ref2va || info.broken_links.length || !info.ffmpeg || !info.ffprobe;
  dot.className = `dot ${bad ? "bad" : warn ? "warn" : "ok"}`;
  $("modelButton").title = bad ? "Setup problem — open to check" : warn ? "Setup warning — open to check" : "Model and tools look good";
}

function renderModelChecks(info) {
  const row = (status, label, detail) => el("li", { class: status },
    el("b", { text: status === "ok" ? "✓" : status === "warn" ? "!" : status === "bad" ? "✗" : "·" }),
    el("span", { text: label }), detail ? el("code", { text: detail }) : null);
  $("modelChecks").replaceChildren(
    row(info.exists ? "ok" : "bad", "Model directory", info.path),
    row(info.has_fl2va ? "ok" : "bad", "FL2VA pipeline — required for every render", info.has_fl2va ? "" : "FL2VA/transformer/config.json missing"),
    row(info.has_ref2va ? "ok" : "warn", "Ref2VA pipeline — needed for references", info.has_ref2va ? "" : "Ref2VA/transformer/model.safetensors.index.json missing"),
    row(info.broken_links.length ? "bad" : "ok", info.broken_links.length ? `${info.broken_links.length} broken symlink(s)` : "Symlinks resolve", info.broken_links.slice(0, 4).join(", ")),
    row("info", `Size on disk: ${fmtBytes(info.size_bytes)}`, ""),
    row(info.h3_runs ? "ok" : "bad", "h3 binary runs", info.h3_error ? `${info.h3} — ${info.h3_error}` : info.h3),
    row(info.ffmpeg ? "ok" : "bad", "ffmpeg", info.ffmpeg || "not found — brew install ffmpeg"),
    row(info.ffprobe ? "ok" : "bad", "ffprobe", info.ffprobe || "not found — brew install ffmpeg"));
  renderModelDot(info);
}

async function openModelModal(refresh = false) {
  $("modelError").hidden = true;
  $("modelModal").hidden = false;
  $("modelPathInput").value = state.cfg.model;
  $("h3PathInput").value = state.cfg.h3;
  $("h3PathField").hidden = !state.cfg.allow_shell;
  $("h3PathHint").hidden = !!state.cfg.allow_shell;
  $("modelChecks").replaceChildren(el("li", { class: "info", text: "Checking…" }));
  try {
    const info = await api(`/api/model${refresh ? "?refresh=1" : ""}`);
    state.cfg.model_info = info;
    renderModelChecks(info);
  } catch (err) {
    $("modelError").textContent = err.message;
    $("modelError").hidden = false;
  }
}

async function saveModelPaths() {
  $("modelError").hidden = true;
  try {
    let info = null;
    const model = $("modelPathInput").value.trim();
    if (model && model !== state.cfg.model) {
      info = await api("/api/model", { model });
      state.cfg.model = info.path;
    }
    const h3 = $("h3PathInput").value.trim();
    if (state.cfg.allow_shell && h3 && h3 !== state.cfg.h3) {
      info = await api("/api/h3", { h3 });
      state.cfg.h3 = info.h3;
    }
    if (info) {
      state.cfg.model_info = info;
      renderModelChecks(info);
      setMode(state.mode);
      sync();
      toast("Paths updated.", { kind: "ok" });
    }
  } catch (err) {
    $("modelError").textContent = err.message;
    $("modelError").hidden = false;
  }
}

/* ── shortcuts ──────────────────────────────────────────────────── */

function visibleTakeNames() {
  return [...$("takes").children].map((li) => li.querySelector(".nm")?.textContent).filter(Boolean);
}

function onGlobalKeydown(e) {
  const mod = e.metaKey || e.ctrlKey;
  if (mod && e.key === "Enter") {
    e.preventDefault();
    submit(e.shiftKey ? 3 : 1);
    return;
  }
  if (e.key === "Escape") {
    let closed = false;
    document.querySelectorAll(".modal").forEach((modal) => { if (!modal.hidden) { modal.hidden = true; closed = true; } });
    $("timelineReviewVideo").pause();
    if (!$("compare").hidden) { exitCompare(); closed = true; }
    closePopmenus();
    if (closed) e.preventDefault();
    return;
  }
  if (mod || e.altKey || isTyping(e.target) || e.target.tagName === "BUTTON" || e.target.tagName === "A") return;
  if ([...document.querySelectorAll(".modal")].some((modal) => !modal.hidden)) return;
  const player = $("player");
  const hasVideo = player.classList.contains("on") && player.currentSrc;
  if (e.key === " " && hasVideo) {
    e.preventDefault();
    if (player.paused) player.play().catch(() => {}); else player.pause();
  } else if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && hasVideo) {
    e.preventDefault();
    player.pause();
    player.currentTime = Math.max(0, player.currentTime + (e.key === "ArrowRight" ? 1 : -1) / H3_FPS);
  } else if (e.key === "j" || e.key === "k") {
    const names = visibleTakeNames();
    if (!names.length) return;
    const index = names.indexOf(state.selected);
    const next = index < 0 ? 0 : Math.min(names.length - 1, Math.max(0, index + (e.key === "j" ? 1 : -1)));
    selectTake(names[next]);
    $("takes").children[next]?.scrollIntoView({ block: "nearest" });
  }
}

/* ── wiring ─────────────────────────────────────────────────────── */

function bindForm() {
  ["steps", "layers", "reuse", "seed", "frames", "label", "internal", "tokenReduction", "int8RowFc2", "ssdStreaming",
    "zeroCopy", "prefetchDepth", "prefetchWorkers", "previewModeSingle", "previewModeAll", "extraArgs"]
    .forEach((id) => $(id).addEventListener("input", sync));
  $("previewToggle").addEventListener("input", () => { syncPreviewModeEnabled(); sync(); });
  $("width").addEventListener("change", onDimensionsTyped);
  $("height").addEventListener("change", onDimensionsTyped);
  $("aspectSelect").addEventListener("change", onAspectChange);
  $("megapixels").addEventListener("input", () => {
    if (state.canvas.aspect === "custom") state.canvas.customRatio = state.canvas.customRatio || +$("width").value / +$("height").value;
    fitCanvas();
    sync();
  });
  $("dice").onclick = () => { $("seed").value = randomSeed(); sync(); };

  $("prompt").addEventListener("input", onPromptInput);
  $("prompt").addEventListener("keydown", onPromptKeydown);
  $("prompt").addEventListener("paste", onPromptPaste);
  $("scaffold").onclick = scaffoldPrompt;
  $("clearPrompt").onclick = () => {
    if (!promptText().trim() || !confirm("Clear the prompt?")) return;
    state.promptDoc = [{ type: "text", value: "" }];
    renderPromptEditor(); $("prompt").focus(); sync();
  };
  $("historyBtn").onclick = (e) => {
    e.stopPropagation();
    const menu = $("historyMenu");
    const open = menu.hidden;
    closePopmenus(menu);
    if (open) renderHistoryMenu();
    menu.hidden = !open;
  };

  $("modeSwitch").addEventListener("click", (e) => {
    const mode = e.target.closest("button[data-mode]")?.dataset.mode;
    if (mode) { setMode(mode); sync(); }
  });
  $("runModeSwitch").addEventListener("click", (e) => {
    const mode = e.target.closest("button[data-run]")?.dataset.run;
    if (mode) { setRunMode(mode); sync(); }
  });
  $("interToggle").onclick = toggleInteractive;
  $("anchorFirst").onclick = () => { state.first = null; renderAnchors(); renderLibrary(); sync(); };
  $("anchorLast").onclick = () => { state.last = null; renderAnchors(); renderLibrary(); sync(); };

  $("file").addEventListener("change", (e) => { uploadFiles([...e.target.files]); e.target.value = ""; });
  const drop = $("drop");
  ["dragenter", "dragover"].forEach((type) => drop.addEventListener(type, (e) => { e.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "drop"].forEach((type) => drop.addEventListener(type, (e) => { e.preventDefault(); drop.classList.remove("over"); }));
  drop.addEventListener("drop", (e) => uploadFiles([...e.dataTransfer.files]));

  $("render").onclick = () => submit(1);
  $("queueBtn").onclick = () => submit(3);
}

function bindChrome() {
  $("themeSwitch").addEventListener("click", (e) => {
    const mode = e.target.closest("button[data-theme]")?.dataset.theme;
    if (!mode) return;
    applyTheme(mode);
    safeStorage(() => localStorage.setItem(THEME_KEY, mode));
  });
  $("notifyButton").onclick = toggleNotify;
  $("sessionSelect").onchange = (e) => activateSession(e.target.value).catch((err) => toast(err.message, { kind: "error" }));
  $("sessionMenuBtn").onclick = (e) => {
    e.stopPropagation();
    const menu = $("sessionMenu");
    const open = menu.hidden;
    closePopmenus(menu);
    menu.hidden = !open;
    $("sessionMenuBtn").setAttribute("aria-expanded", String(open));
  };
  $("newSession").onclick = () => { closePopmenus(); openSessionModal("new"); };
  $("duplicateSession").onclick = () => { closePopmenus(); openSessionModal("duplicate"); };
  $("deleteSession").onclick = () => { closePopmenus(); deleteSession(); };
  $("cancelSession").onclick = () => { $("sessionModal").hidden = true; };
  $("confirmSession").onclick = confirmSessionModal;
  $("sessionNameInput").addEventListener("keydown", (e) => { if (e.key === "Enter") confirmSessionModal(); });

  $("modelButton").onclick = () => openModelModal(false);
  $("recheckModel").onclick = () => openModelModal(true);
  $("cancelModel").onclick = () => { $("modelModal").hidden = true; };
  $("saveModel").onclick = saveModelPaths;

  document.querySelector(".sidetabs").addEventListener("click", (e) => {
    const side = e.target.closest("button[data-side]")?.dataset.side;
    if (!side) return;
    [...document.querySelector(".sidetabs").children].forEach((b) => b.classList.toggle("on", b.dataset.side === side));
    $("takesPanel").hidden = side !== "takes";
    $("timelinePanel").hidden = side !== "timeline";
  });
  $("starFilter").addEventListener("change", renderTakes);
  $("compareGrid").onclick = () => openCompareGrid();
  $("compareWipe").onclick = () => openWipe();
  $("compareClear").onclick = () => { state.compare = []; if (!$("compare").hidden) exitCompare(); renderTakes(); };
  $("previewSlider").addEventListener("input", renderScrub);
  $("previewClose").onclick = () => {
    const take = state.scrub?.take;
    hidePreview();
    if (take) selectTake(take.name, false); else showVideo(null);
  };

  document.addEventListener("click", (e) => {
    if (!e.target.closest(".menuwrap")) closePopmenus();
    const command = document.querySelector("details.cmd");
    if (command?.open && !e.target.closest("details.cmd")) command.open = false;
  });
  document.addEventListener("keydown", onGlobalKeydown);
  window.addEventListener("beforeunload", () => saveSettings.flush());
}

async function init() {
  applyTheme(safeStorage(() => localStorage.getItem(THEME_KEY), null) || "system");
  renderNotifyButton();
  buildCanvasControls();
  bindForm();
  bindChrome();
  bindTerminal();
  bindTimeline();
  const cfg = await api("/api/config");
  state.cfg = cfg;
  state.legalFrames = cfg.legal_frames || state.legalFrames;
  state.maxPixels = cfg.max_pixels || state.maxPixels;
  state.sessions = cfg.sessions || [];
  state.interactive = cfg.interactive || { loaded: false };
  $("terminalForm").hidden = !cfg.allow_shell;
  renderModelDot(cfg.model_info);
  if (!cfg.ffmpeg || !cfg.ffprobe) {
    toast("ffmpeg/ffprobe not found — thumbnails, frame extraction and the timeline won't work.", { kind: "error", hint: "Install with `brew install ffmpeg`, or set H3_FFMPEG / H3_FFPROBE." });
  }
  if (!cfg.model_info.has_fl2va || !cfg.model_info.h3_runs) openModelModal(false);
  await activateSession(cfg.session);
}

init().catch((err) => { console.error(err); toast(`h3 studio failed to start: ${err.message}`, { kind: "error", timeout: 0 }); });
