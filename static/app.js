/* h3 studio client */

const $ = (id) => document.getElementById(id);
const LEGAL = Array.from({ length: 22 }, (_, n) => 5 + 17 * n);
const H3_FPS = 24;
const MAX_PIXELS = 768 * 1344;

// Auto-reload functionality
(function() {
  let lastModified = 0;
  let reloadInterval = null;

  function checkReload() {
    fetch('/static/style.css')
      .then(res => {
        if (!res.ok) return null;
        const lastModified = res.headers.get('Last-Modified');
        if (lastModified) {
          const newModified = new Date(lastModified).getTime();
          if (newModified > lastModified) {
            console.log('Stylesheet changed - reloading...');
            location.reload();
          }
        }
        return res.text();
      })
      .catch(err => console.log('Reload check failed:', err));
  }

  // Check every 30 seconds
  reloadInterval = setInterval(checkReload, 30000);

  // Reload on tab visibility change
  document.addEventListener('visibilitychange', () => {
    if (!document.hidden) {
      checkReload();
    }
  });

  // Reload on page visibility
  document.addEventListener('pagehide', () => {
    if (reloadInterval) {
      clearInterval(reloadInterval);
    }
  });
})();

const state = {
  mode: "ref",
  refs: [],            // {name, kind}
  first: null,
  last: null,
  inputs: [],
  outputs: [],
  selected: null,
  job: null,
  cfg: null,
  tick: null,
  saveTimer: null,
  promptDoc: [{ type: "text", value: "" }],
  mention: null,
};

const SIZES = [
  ["1:1  (Square)", 1],
  ["2:3 (Portrait photo)", 2 / 3],
  ["3:2 (Landscape photo)", 3 / 2],
  ["3:4 (Portrait standard)", 3 / 4],
  ["4:3 (Standard)", 4 / 3],
  ["9:16 (Portrait)", 9 / 16],
  ["16:9 (Widescreen)", 16 / 9],
  ["21:9 (Ultrawide)", 21 / 9],
];

const QUALITY = [
  ["Draft", { steps: 4, layers: 50, reuse: 1, token: false }],
  ["Fast", { steps: 20, layers: 45, reuse: 2, token: true }],
  ["Default", { steps: 20, layers: 45, reuse: 2, token: false }],
  ["Reference", { steps: 50, layers: 50, reuse: 1, token: false }],
];

function slotLabel(refs, index) {
  const n = refs.slice(0, index + 1).filter((item) => item.kind === refs[index].kind).length;
  const ref = refs[index];
  return `${ref.kind === "image" ? "Picture" : ref.kind === "video" ? "Video" : "Audio"} ${n}`;
}

function resolvePrompt(doc, refs) {
  return doc.map((node) => {
    if (node.type === "text") return node.value;
    const i = refs.findIndex((ref) => ref.id === node.refId);
    if (i === -1) throw new Error(`Reference no longer attached: ${node.refId}`);
    return `<${slotLabel(refs, i)}>`;
  }).join("");
}

function promptText(doc = state.promptDoc) {
  return doc.map((node) => node.type === "text" ? node.value : `@${node.refId}`).join("");
}

function renderPromptEditor() {
  const editor = $("prompt");
  editor.innerHTML = "";
  state.promptDoc.forEach((node) => {
    if (node.type === "text") editor.append(document.createTextNode(node.value));
    else {
      const ref = state.refs.find((item) => item.id === node.refId);
      const chip = document.createElement("span");
      chip.className = `mention-chip${ref ? "" : " invalid"}`;
      chip.contentEditable = "false";
      chip.dataset.refId = node.refId;
      chip.textContent = ref ? `@${ref.name} · ${slotLabel(state.refs, state.refs.indexOf(ref))}` : "⚠ removed";
      editor.append(chip);
    }
  });
}

function readPromptEditor() {
  const doc = [];
  $("prompt").childNodes.forEach((node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      if (node.nodeValue) doc.push({ type: "text", value: node.nodeValue });
    } else if (node.nodeType === Node.ELEMENT_NODE && node.dataset.refId) {
      doc.push({ type: "ref", refId: node.dataset.refId });
    } else if (node.textContent) {
      doc.push({ type: "text", value: node.textContent });
    }
  });
  state.promptDoc = doc.length ? doc : [{ type: "text", value: "" }];
}

function promptCandidates(query) {
  const attached = new Set(state.refs.map((ref) => ref.name));
  return [
    ...state.refs.map((ref) => ({ ...ref, attached: true })),
    ...state.inputs.filter((file) => !attached.has(file.name)).map((file) => ({ ...file, attached: false })),
  ].filter((ref) => ref.name.toLowerCase().includes(query.toLowerCase()));
}

function closeMentionMenu() {
  $("mentionMenu").hidden = true;
  state.mention = null;
}

function insertMention(ref) {
  if (!ref.attached) addRef(ref);
  const attached = state.refs.find((item) => item.name === ref.name);
  if (!attached) return;
  const mention = state.mention;
  const textNode = mention?.node;
  if (!textNode || !$("prompt").contains(textNode)) return;
  const range = document.createRange();
  range.setStart(textNode, mention.start);
  range.setEnd(textNode, mention.end);
  range.deleteContents();
  const chip = document.createElement("span");
  chip.className = "mention-chip";
  chip.contentEditable = "false";
  chip.dataset.refId = attached.id;
  chip.textContent = `@${attached.name} · ${slotLabel(state.refs, state.refs.indexOf(attached))}`;
  const space = document.createTextNode(" ");
  range.insertNode(chip);
  chip.parentNode.insertBefore(space, chip.nextSibling);
  const caret = document.createRange();
  caret.setStart(space, 1);
  caret.collapse(true);
  const selection = getSelection();
  selection.removeAllRanges();
  selection.addRange(caret);
  $("prompt").focus();
  state.promptDoc = [];
  readPromptEditor();
  closeMentionMenu();
  sync();
}

function insertRefAtCaret(ref) {
  const range = getSelection()?.rangeCount ? getSelection().getRangeAt(0) : null;
  if (!range || !$("prompt").contains(range.commonAncestorContainer)) {
    $("prompt").focus();
    return;
  }
  const chip = document.createElement("span");
  chip.className = "mention-chip";
  chip.contentEditable = "false";
  chip.dataset.refId = ref.id;
  chip.textContent = `@${ref.name} · ${slotLabel(state.refs, state.refs.indexOf(ref))}`;
  range.deleteContents();
  range.insertNode(chip);
  const space = document.createTextNode(" ");
  chip.parentNode.insertBefore(space, chip.nextSibling);
  range.setStart(space, 1); range.collapse(true);
  getSelection().removeAllRanges(); getSelection().addRange(range);
  $("prompt").focus();
  readPromptEditor(); sync();
}

function insertPromptText(text, savedRange = null) {
  if (!text) return;
  const selection = getSelection();
  const range = savedRange || (selection?.rangeCount ? selection.getRangeAt(0) : null);
  if (!range) return;
  if (!$("prompt").contains(range.commonAncestorContainer)) return;
  if (!savedRange) selection.removeAllRanges();
  range.deleteContents();
  const node = document.createTextNode(text);
  range.insertNode(node);
  range.setStartAfter(node);
  range.collapse(true);
  selection.removeAllRanges();
  selection.addRange(range);
  readPromptEditor();
  sync();
}

/* ── setup ─────────────────────────────────────────────────────── */

function buildChips() {
  $("sizePresets").innerHTML = "";
  const custom = document.createElement("option");
  custom.value = "custom";
  custom.textContent = "Custom";
  $("sizePresets").append(custom);
  SIZES.forEach(([label, ratio]) => {
    const option = document.createElement("option");
    option.value = ratio;
    option.textContent = label;
    $("sizePresets").append(option);
  });
  $("sizePresets").onchange = () => {
    const selected = SIZES.find(([, ratio]) => String(ratio) === $("sizePresets").value);
    if (selected) {
      const ratio = selected[1];
      $("sizePresets").dataset.ratio = ratio;
      $("sizePresets").dataset.native = "";
      $("sizePresets").dataset.preset = "true";
      setDimensionsForRatio(ratio, +$("megapixels").value);
    }
    sync();
  };
  $("megapixels").oninput = () => {
    const ratio = currentAspectRatio();
    setDimensionsForRatio(ratio, +$("megapixels").value);
    sync();
  };
  document.querySelectorAll("[data-native]").forEach((button) => {
    button.onclick = () => {
      const landscape = button.dataset.native === "landscape";
      $("width").value = landscape ? 1344 : 768;
      $("height").value = landscape ? 768 : 1344;
      $("sizePresets").value = "custom";
      $("sizePresets").dataset.ratio = landscape ? 1344 / 768 : 768 / 1344;
      $("sizePresets").dataset.native = "true";
      $("sizePresets").dataset.preset = "";
      sync();
    };
  });
  $("qualityPresets").innerHTML = "";
  QUALITY.forEach(([label, q]) => {
    const b = document.createElement("button");
    b.textContent = label;
    b.onclick = () => {
      $("steps").value = q.steps; $("layers").value = q.layers;
      $("reuse").value = q.reuse; $("tokenReduction").checked = q.token;
      sync();
    };
    $("qualityPresets").append(b);
  });
}

function currentAspectRatio() {
  const w = +$("width").value, h = +$("height").value;
  return +$("sizePresets").dataset.ratio || (h ? w / h : 1);
}

function solveCanvas(ratio, megapixels, { aspectWeight = 12 } = {}) {
  const target = Math.min(megapixels * 1e6, MAX_PIXELS);
  const base = Math.round(Math.sqrt(target * ratio) / 32);
  let best = null;
  for (let k = base - 8; k <= base + 8; k++) {
    const width = k * 32;
    if (width < 32) continue;
    const height = Math.max(32, Math.round(width / ratio / 32) * 32);
    const pixels = width * height;
    if (pixels > MAX_PIXELS) continue;
    const score = aspectWeight * Math.abs((width / height) / ratio - 1)
      + Math.abs(pixels - target) / target;
    if (!best || score < best.score) {
      best = {
        width, height, pixels, score,
        actualRatio: width / height,
        shortEdge: Math.min(width, height),
      };
    }
  }
  return best;
}

function setDimensionsForRatio(ratio, megapixels) {
  const result = solveCanvas(ratio, megapixels);
  if (!result) throw new Error("No legal H3 canvas size matches this aspect ratio.");
  $("width").value = result.width;
  $("height").value = result.height;
}

function markChips() {
  const w = +$("width").value, h = +$("height").value;
  const native = (w === 1344 && h === 768) || (w === 768 && h === 1344);
  if (native) $("sizePresets").dataset.native = "true";
  const preset = SIZES.find(([, ratio]) => Math.abs(ratio - w / h) < 1e-9);
  if (!$("sizePresets").dataset.preset && !native) {
    $("sizePresets").value = preset ? String(preset[1]) : "custom";
    if (w && h) $("sizePresets").dataset.ratio = w / h;
  }
  if (native) $("sizePresets").value = "custom";
  $("megapixels").disabled = !!$("sizePresets").dataset.native;
  const s = +$("steps").value, l = +$("layers").value, r = +$("reuse").value;
  [...$("qualityPresets").children].forEach((b, i) => {
    const q = QUALITY[i][1];
    b.classList.toggle("on", q.steps === s && q.layers === l && q.reuse === r);
  });
}

/* ── derived state ─────────────────────────────────────────────── */

function frames() { return LEGAL[+$("frames").value]; }

function params() {
  const scale = +$("internal").value;
  const w = +$("width").value, h = +$("height").value;
  const ratio = h ? w / h : 1;
  const p = {
    label: $("label").value.trim(),
    session_name: state.cfg?.session || "session-1",
    prompt_doc: state.promptDoc,
    width: w, height: h,
    frames: frames(),
    steps: +$("steps").value,
    layers: +$("layers").value,
    reuse: +$("reuse").value,
    seed: +$("seed").value,
    token_reduction: $("tokenReduction").checked,
    int8_row_fc2: $("int8RowFc2").checked,
    ssd_streaming: $("ssdStreaming").checked,
    run_mode: $("runMode").value,
    refs: state.mode === "ref" ? state.refs.map((ref) => ({ ...ref })) : [],
    env: {},
  };
  try {
    p.prompt = resolvePrompt(state.promptDoc, p.refs);
  } catch (error) {
    p.prompt = "";
    p.prompt_error = error.message;
  }
  if (scale < 1) {
    const internal = solveCanvas(ratio, (w * h / 1000000) * scale * scale);
    p.render_width = internal.width;
    p.render_height = internal.height;
  }
  if (state.mode !== "ref") {
    if (state.first) p.first_frame = state.first;
    if (state.last) p.last_frame = state.last;
  }
  if ($("zeroCopy").checked) p.env.H3_ZERO_COPY_WEIGHTS = "0";
  if ($("prefetchDepth").value) p.env.H3_QWEN_PREFETCH_DEPTH = $("prefetchDepth").value;
  if ($("prefetchWorkers").value) p.env.H3_QWEN_PREFETCH = $("prefetchWorkers").value;
  return p;
}

function localErrors(p) {
  const e = [];
  if (!p.session_name) e.push("Enter a session name.");
  if (p.width % 32 || p.height % 32) e.push("Width and height must be multiples of 32.");
  if (p.width * p.height > MAX_PIXELS)
    e.push(`${p.width}×${p.height} is ${(p.width * p.height).toLocaleString()} pixels; the ceiling is ${MAX_PIXELS.toLocaleString()}.`);
  if (p.prompt_error) e.push(p.prompt_error);
  if (!p.prompt) e.push("Write a prompt.");
  if (state.mode === "ref" && !state.refs.length) e.push("Ref2VA needs at least one reference.");
  const refs = p.refs || [];
  const images = refs.filter((r) => r.kind === "image");
  const videos = refs.filter((r) => r.kind === "video");
  const audio = refs.filter((r) => r.kind === "audio");
  const duration = refs.reduce((sum, r) => sum + (Number(r.duration) || 0), 0);
  if (audio.length && !images.length && !videos.length)
    e.push("A standalone audio reference must accompany an image or video.");
  if (images.length > 9) e.push("At most 9 image references.");
  if (videos.length > 3) e.push("At most 3 video references.");
  if (audio.length > 3) e.push("At most 3 audio references.");
  if (duration > 15) e.push(`Combined reference duration is ${duration.toFixed(1)}s; the limit is 15s.`);
  if (refs.some((ref) => ref.kind === "video" && ref.mode === "replace" && !ref.pairedAudio))
    e.push("Choose replacement audio for every video in replace mode.");
  if (state.mode === "anchor" && !state.first && !state.last)
    e.push("Set a first or last frame, or switch to Reference mode.");
  return e;
}

function commandPreview(p) {
  const q = (s) => (/[\s"']/.test(s) ? `'${s}'` : s);
  const env = Object.entries(p.env).map(([k, v]) => `${k}=${v}`).join(" ");
  const a = ["./h3", "--profile", "-d", q(state.cfg?.model || "MODEL")];
  a.push("-p", `'${p.prompt.replace(/\n/g, " ").slice(0, 60)}…'`);
  (p.refs || []).forEach((r) => {
    if (r.kind === "image") a.push("--ref-image", q(`input/${r.name}`));
    else if (r.kind === "audio") a.push("--ref-audio", q(`input/${r.name}`));
    else if (r.mode === "silent") a.push("--ref-silent-video", q(`input/${r.name}`));
    else if (r.mode === "replace" && r.pairedAudio) {
      a.push("--ref-video-audio", q(`input/${r.name}`), q(`input/${r.pairedAudio}`));
    } else a.push("--ref-video", q(`input/${r.name}`));
  });
  if (p.first_frame) a.push("--first-frame", q(`input/${p.first_frame}`));
  if (p.last_frame) a.push("--last-frame", q(`input/${p.last_frame}`));
  a.push("--width", p.width, "--height", p.height);
  if (p.render_width) a.push("--render-width", p.render_width, "--render-height", p.render_height);
  a.push("--frames", p.frames, "--steps", p.steps, "--layers", p.layers, "--reuse", p.reuse);
  if (p.token_reduction) a.push("--token-reduction");
  if (p.ssd_streaming) a.push("--ssd-streaming");
  else if (p.int8_row_fc2) a.push("--use-int8-row-fc2");
  a.push("--seed", p.seed, "-o", "outputs/take.mp4");
  return (env ? env + " \\\n  " : "") + a.join(" ");
}

function sync() {
  const p = params();
  clearTimeout(state.saveTimer);
  state.saveTimer = setTimeout(() => {
    fetch("/api/session/save", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(p),
    }).catch((err) => appendLog("!! Could not save session: " + err.message));
  }, 300);
  const n = frames();
  $("frameCount").textContent = n;
  $("frameSecs").textContent = (n / H3_FPS).toFixed(2) + " s";
  $("promptCount").textContent = p.prompt.length + " chars";
  markChips();

  const px = p.width * p.height;
  $("megapixelsValue").textContent = `${(+$("megapixels").value).toFixed(2)} MP`;
  $("resolvedDimensions").textContent = `${p.width} × ${p.height}`;
  $("actualMegapixels").textContent = `${(px / 1000000).toFixed(2)} MP`;
  $("actualRatio").textContent = (p.width / p.height).toFixed(3);
  const shortEdge = Math.min(p.width, p.height);
  $("shortEdge").textContent = `short edge ${shortEdge} · ${shortEdge === 768 ? "native" : "below native"}`;
  $("sizeWarn").hidden = px <= MAX_PIXELS;
  $("sizeWarn").textContent = `${px.toLocaleString()} pixels exceeds the ${MAX_PIXELS.toLocaleString()} ceiling.`;

  const errs = localErrors(p);
  $("errors").hidden = !errs.length;
  $("errors").textContent = errs.join("\n");
  $("render").disabled = !!errs.length || state.job?.state === "running";
  $("cmdPreview").textContent = commandPreview(p);
}

/* ── references ────────────────────────────────────────────────── */

function renderRefs() {
  const ul = $("refList");
  ul.innerHTML = "";
  state.refs.forEach((r, i) => {
    const li = document.createElement("li");
    li.draggable = true;
    li.dataset.kind = r.kind;
    li.dataset.i = i;
    const index = state.refs.slice(0, i + 1).filter((ref) => ref.kind === r.kind).length;
    const label = r.kind === "image" ? `Picture ${index}` : r.kind === "video" ? `Video ${index}` : `Audio ${index}`;
    li.innerHTML = `<button class="n" type="button">${label}</button>`;
    if (r.kind === "image") {
      const img = document.createElement("img");
      img.src = `/media/input/${encodeURIComponent(r.name)}`;
      li.append(img);
    }
    const nm = document.createElement("span");
    nm.className = "nm"; nm.textContent = r.name;
    const x = document.createElement("button");
    x.textContent = "×"; x.title = "Remove";
    x.onclick = (e) => { e.stopPropagation(); state.refs.splice(i, 1); renderRefs(); renderPromptEditor(); sync(); };
    li.querySelector(".n").onclick = (e) => {
      e.stopPropagation();
      insertRefAtCaret(r);
    };
    if (r.duration != null) {
      const duration = document.createElement("span");
      duration.className = "duration";
      duration.textContent = `${Number(r.duration).toFixed(1)}s`;
      li.append(duration);
    }
    if (r.kind === "video") {
      const mode = document.createElement("select");
      mode.className = "refmode";
      [["keep", "keep audio"], ["silent", "silent"], ["replace", "replace audio"]]
        .forEach(([value, text]) => {
          const option = document.createElement("option");
          option.value = value; option.textContent = text; option.selected = (r.mode || "keep") === value;
          mode.append(option);
        });
      mode.onchange = (e) => { r.mode = e.target.value; sync(); };
      li.append(mode);
      if (r.mode === "replace") {
        const audio = document.createElement("select");
        audio.className = "refmode";
        audio.innerHTML = '<option value="">audio file…</option>';
        state.inputs.filter((file) => file.kind === "audio").forEach((file) => {
          const option = document.createElement("option");
          option.value = file.name; option.textContent = file.name; option.selected = r.pairedAudio === file.name;
          audio.append(option);
        });
        audio.onchange = (e) => { r.pairedAudio = e.target.value || null; sync(); };
        li.append(audio);
      }
    }
    li.append(nm, x);

    li.ondragstart = (e) => { li.classList.add("dragging"); e.dataTransfer.setData("text/plain", i); };
    li.ondragend = () => li.classList.remove("dragging");
    li.ondragover = (e) => e.preventDefault();
    li.ondrop = (e) => {
      e.preventDefault();
      const from = +e.dataTransfer.getData("text/plain");
      const [moved] = state.refs.splice(from, 1);
      state.refs.splice(i, 0, moved);
      renderRefs(); sync();
    };
    ul.append(li);
  });
  const counts = { image: 0, video: 0, audio: 0 };
  let duration = 0;
  state.refs.forEach((ref) => { counts[ref.kind]++; duration += Number(ref.duration) || 0; });
  $("refHint").hidden = false;
  $("refHint").innerHTML =
    `Pictures ${counts.image}/9 · Videos ${counts.video}/3 · Audio ${counts.audio}/3 · ` +
    `duration ${duration.toFixed(1)}/15.0s · Click a label to insert it.`;
}

function renderLibrary() {
  const wrap = $("library");
  wrap.innerHTML = "";
  state.inputs.forEach((f) => {
    const fig = document.createElement("figure");
    fig.title = f.name;
    if (f.kind === "image") {
      const img = document.createElement("img");
      img.src = `/media/input/${encodeURIComponent(f.name)}`;
      img.alt = f.name;
      fig.append(img);
    } else {
      const d = document.createElement("div");
      d.className = "nonimg"; d.textContent = f.kind;
      fig.append(d);
    }
    const delBtn = document.createElement("button");
    delBtn.className = "delete-file";
    delBtn.type = "button";
    delBtn.title = "Delete input";
    delBtn.textContent = "×";
    delBtn.onclick = (e) => { e.stopPropagation(); deleteFile(f.name, "input"); };
    fig.append(delBtn);
    fig.onclick = () => addRef(f);
    wrap.append(fig);
  });
}

async function deleteFile(name, kind) {
  const message = kind === "output"
    ? `Permanently delete this take and its video file?\n${name}\n\nThis cannot be undone.`
    : `Delete this ${kind} file?\n${name}`;
  if (!confirm(message)) return;
  const res = await fetch("/api/delete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ name, kind }),
  });
  const data = await res.json();
  if (data.error) { appendLog("!! " + data.error); return; }
  state.inputs = data.inputs || [];
  state.outputs = data.outputs || [];
  state.refs = state.refs.filter((ref) => !(kind === "input" && ref.name === name));
  if (state.first === name) state.first = null;
  if (state.last === name) state.last = null;
  if (kind === "output" && state.selected === name) {
    state.selected = null;
    $("player").removeAttribute("src");
    $("player").classList.remove("on");
    $("viewerEmpty").hidden = false;
  }
  renderLibrary(); renderRefs(); renderPromptEditor(); renderAnchors(); renderTakes(); sync();
}

function addRef(f) {
  if (state.mode === "anchor") {
    if (f.kind !== "image") return;
    if (!state.first) state.first = f.name;
    else state.last = f.name;
    renderAnchors();
  } else {
    if (state.refs.length >= 12) return;
    if (state.refs.some((ref) => ref.name === f.name)) return;
    const count = state.refs.filter((ref) => ref.kind === f.kind).length;
    const limit = f.kind === "image" ? 9 : 3;
    if (count >= limit) return;
    const duration = Number(f.duration) || 0;
    if ((f.kind === "video" || f.kind === "audio") && duration && (duration < 2 || duration > 15)) {
      appendLog(`!! ${f.name} must be between 2 and 15 seconds.`);
      return;
    }
    const usedDuration = state.refs.reduce((sum, ref) => sum + (Number(ref.duration) || 0), 0);
    if (usedDuration + duration > 15) {
      appendLog("!! Combined video and audio duration cannot exceed 15 seconds.");
      return;
    }
    state.refs.push({
      id: crypto.randomUUID?.() || `${Date.now()}-${Math.random()}`,
      name: f.name, kind: f.kind, mode: f.kind === "video" ? "keep" : undefined,
      pairedAudio: null, duration: f.duration ?? null,
    });
    renderRefs();
  }
  sync();
}

function renderAnchors() {
  $("anchorFirst").querySelector("em").textContent = state.first || "none";
  $("anchorFirst").classList.toggle("set", !!state.first);
  $("anchorLast").querySelector("em").textContent = state.last || "none";
  $("anchorLast").classList.toggle("set", !!state.last);
}

/* ── uploads ───────────────────────────────────────────────────── */

async function upload(files) {
  for (const f of files) {
    const buf = await f.arrayBuffer();
    const res = await fetch("/api/upload", {
      method: "POST",
      headers: { "X-Filename": encodeURIComponent(f.name) },
      body: buf,
    });
    const data = await res.json();
    if (data.inputs) { state.inputs = data.inputs; renderLibrary(); }
    if (data.name) {
      const kind = /\.(png|jpe?g|webp)$/i.test(data.name) ? "image"
        : /\.(mp4|mov)$/i.test(data.name) ? "video" : "audio";
      addRef({ ...data, kind });
    }
  }
}

/* ── takes ─────────────────────────────────────────────────────── */

function renderTakes() {
  const ul = $("takes");
  ul.innerHTML = "";
  $("takeCount").textContent = state.outputs.length;
  state.outputs.forEach((o) => {
    const li = document.createElement("li");
    li.classList.toggle("on", state.selected === o.name);
    const p = o.meta?.params || {};
    const secs = o.meta?.duration_s;

    const row = document.createElement("div");
    row.className = "row";
    const thumb = document.createElement("div");
    thumb.className = "thumb";
    const video = document.createElement("video");
    video.src = `/media/output/${encodeURIComponent(o.name)}`;
    video.muted = true;
    video.preload = "metadata";
    video.playsInline = true;
    video.setAttribute("aria-hidden", "true");
    thumb.append(video);
    const info = document.createElement("div");
    info.className = "info";
    info.innerHTML = `<div class="nm">${o.name}</div>
      <div class="meta">${p.width || "?"}×${p.height || "?"} · ${p.frames || "?"}f · ${p.steps || "?"} steps · seed ${p.seed ?? "?"}${secs ? " · " + secs + "s" : ""}</div>`;
    row.append(thumb, info);
    li.append(row);

    const ops = document.createElement("div");
    ops.className = "ops";
    ops.append(
      mkBtn("Reuse settings", () => restore(o)),
      mkBtn("Chain →", () => chain(o.name)),
      mkBtn("Delete", () => deleteFile(o.name, "output")),
    );
    li.append(ops);
    li.onclick = (e) => { if (e.target.tagName !== "BUTTON") select(o.name); };
    ul.append(li);
  });
}

function mkBtn(label, fn) {
  const b = document.createElement("button");
  b.className = "ghost"; b.textContent = label;
  b.onclick = (e) => { e.stopPropagation(); fn(); };
  return b;
}

function select(name) {
  state.selected = name;
  const v = $("player");
  v.pause();
  v.muted = false;
  v.volume = 1;
  v.src = `/media/output/${encodeURIComponent(name)}`;
  v.load();
  v.classList.add("on");
  $("viewerEmpty").hidden = true;
  v.play().catch((error) => appendLog("!! Playback did not start automatically: " + error.message));
  renderTakes();
}

function restore(o) {
  const p = o.meta?.params;
  if (!p) return;
  state.promptDoc = Array.isArray(p.prompt_doc) ? p.prompt_doc : [{ type: "text", value: p.prompt || "" }];
  $("width").value = p.width; $("height").value = p.height;
  $("steps").value = p.steps; $("layers").value = p.layers;
  $("reuse").value = p.reuse || 1; $("seed").value = p.seed;
  $("tokenReduction").checked = !!p.token_reduction;
  $("frames").value = Math.max(0, LEGAL.indexOf(p.frames));
  state.refs = (p.refs || [
    ...(p.ref_images || []).map((name) => ({ name, kind: "image" })),
    ...(p.ref_videos || []).map((clip) => ({ name: clip.name, kind: "video", mode: clip.silent ? "silent" : "keep" })),
    ...(p.ref_audio || []).map((name) => ({ name, kind: "audio" })),
  ]).map((ref, i) => ({
    id: ref.id || `${Date.now()}-${i}`, pairedAudio: ref.pairedAudio || null,
    duration: ref.duration ?? null, ...ref,
  }));
  state.first = p.first_frame || null; state.last = p.last_frame || null;
  setMode(state.first || state.last ? "anchor" : "ref");
  renderRefs(); renderPromptEditor(); renderAnchors(); sync();
}

async function chain(name) {
  const res = await fetch("/api/chain", {
    method: "POST", body: JSON.stringify({ name }),
  });
  const data = await res.json();
  if (data.error) { alert(data.error); return; }
  state.inputs = data.inputs;
  setMode("anchor");
  state.first = data.name;
  state.last = null;
  renderLibrary(); renderAnchors(); sync();
}

/* ── rendering ─────────────────────────────────────────────────── */

async function submit(p) {
  appendLog("$ " + commandPreview(p));
  const res = await fetch("/api/render", { method: "POST", body: JSON.stringify(p) });
  const data = await res.json();
  if (data.errors) { $("errors").hidden = false; $("errors").textContent = data.errors.join("\n"); }
}

function showJob(j) {
  state.job = j;
  const busy = j && (j.state === "running" || j.state === "cancelling");
  $("running").hidden = !busy;
  $("lamp").classList.toggle("busy", !!busy);
  $("lampText").textContent = busy ? (j.phase || "rendering") : "idle";
  $("render").disabled = !!busy || !!localErrors(params()).length;
  $("cancel").disabled = !!(busy && j.state === "cancelling");
  $("cancel").textContent = j?.state === "cancelling" ? "Stopping…" : "Stop";

  if (!j) return;
  $("phaseName").textContent = j.phase || "starting";
  if (j.progress) {
    const [n, t] = j.progress;
    $("phaseBar").style.width = (100 * n / t) + "%";
    $("phasePct").textContent = `${n}/${t}`;
  }
  if (busy && !state.tick) {
    state.tick = setInterval(() => {
      if (!state.job?.started) return;
      const s = Math.floor(Date.now() / 1000 - state.job.started);
      $("elapsed").textContent = `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, "0")}s elapsed`;
    }, 1000);
  }
  if (!busy && state.tick) { clearInterval(state.tick); state.tick = null; }

  const tbody = $("profile").querySelector("tbody");
  tbody.innerHTML = "";
  (j.profile || []).forEach((r) => {
    const tr = document.createElement("tr");
    tr.innerHTML = `<td>${r.component}</td><td>${r.stage}</td><td>${r.wall.toFixed(2)}s</td>`;
    tbody.append(tr);
  });
}

function renderQueue(q) {
  $("queueWrap").hidden = !q.length;
  const ul = $("queue");
  ul.innerHTML = "";
  q.forEach((j) => {
    const li = document.createElement("li");
    li.textContent = `${j.label || "take"} · seed ${j.params.seed} · ${j.state}`;
    li.append(mkBtn("Remove", async () => {
      await fetch("/api/cancel", { method: "POST", body: JSON.stringify({ id: j.id }) });
    }));
    ul.append(li);
  });
}

function appendLog(line) {
  const el = $("terminalOutput");
  el.textContent += line + "\n";
  if (el.textContent.length > 60000) el.textContent = el.textContent.slice(-40000);
  el.scrollTop = el.scrollHeight;
}

/* ── events ────────────────────────────────────────────────────── */

function connect() {
  const es = new EventSource("/api/events");
  es.onmessage = (e) => {
    const { kind, payload } = JSON.parse(e.data);
    if (kind === "hello") {
      state.inputs = payload.inputs; state.outputs = payload.outputs;
      renderLibrary(); renderTakes(); renderQueue(payload.queue);
      $("terminalOutput").textContent = "";
      if (payload.terminal_log) $("terminalOutput").textContent = payload.terminal_log;
      else [...(payload.history || []).reverse(), ...(payload.queue || [])].forEach((job) => {
        (job.log || []).forEach((line) => appendLog(line));
      });
      $("terminalOutput").scrollTop = $("terminalOutput").scrollHeight;
      const run = payload.queue.find((j) => j.state === "running" || j.state === "cancelling");
      if (run) showJob(run);
    } else if (kind === "job") {
      showJob(payload);
      if (payload.state === "failed" && payload.error) appendLog("!! " + payload.error);
    } else if (kind === "line") {
      appendLog(payload.line);
    } else if (kind === "terminal") {
      if (payload.line) appendLog(payload.line);
      if (payload.error) appendLog("!! " + payload.error);
      $("terminalCommand").disabled = !!payload.running;
      $("terminalForm").querySelector("button").disabled = !!payload.running;
    } else if (kind === "queue") {
      renderQueue(payload);
      if (!payload.some((j) => j.state === "running")) showJob(null);
    } else if (kind === "outputs") {
      state.outputs = payload; renderTakes();
      if (payload[0] && !state.selected) select(payload[0].name);
    } else if (kind === "inputs") {
      state.inputs = payload; renderLibrary();
    } else if (kind === "reload") {
      console.log('[Hot Reload] Reloading...');
      location.reload();
    }
  };
  es.onerror = () => setTimeout(() => { es.close(); connect(); }, 3000);
}

/* ── mode ──────────────────────────────────────────────────────── */

function setMode(m) {
  state.mode = m;
  [...$("modeSwitch").children].forEach((b) => b.classList.toggle("on", b.dataset.mode === m));
  $("refMode").hidden = m !== "ref";
  $("anchorMode").hidden = m === "ref";
}

/* ── wiring ────────────────────────────────────────────────────── */

function init() {
  buildChips();
  setMode("ref");
  renderAnchors();

  ["width", "height", "steps", "layers", "reuse", "seed", "frames",
   "label", "internal", "tokenReduction", "int8RowFc2", "ssdStreaming",
   "zeroCopy", "prefetchDepth", "prefetchWorkers", "runMode"]
    .forEach((id) => $(id).addEventListener("input", (event) => {
      if (id === "width" || id === "height") {
        $("sizePresets").dataset.native = "";
        $("sizePresets").dataset.preset = "";
      }
      sync();
    }));
  $("prompt").addEventListener("input", () => {
    readPromptEditor();
    const selection = getSelection();
    const node = selection?.anchorNode;
    if (!node || node.nodeType !== Node.TEXT_NODE || !$("prompt").contains(node)) {
       closeMentionMenu(); sync(); return;
    }
    const before = node.nodeValue.slice(0, selection.anchorOffset);
    const match = before.match(/(^|\s)@([^\s@]*)$/);
    if (!match) { closeMentionMenu(); sync(); return; }
    const query = match[2];
    const options = promptCandidates(query);
    state.mention = {
       node,
       start: before.length - match[0].length + match[1].length,
       end: before.length,
       options,
       selected: 0,
    };
    const rect = selection.getRangeAt(0).getBoundingClientRect();
    const menu = $("mentionMenu");
    menu.style.left = `${rect.left}px`; menu.style.top = `${rect.bottom + 4}px`;
    menu.innerHTML = "";
    let lastGroup = null;
    options.forEach((ref, i) => {
       const group = ref.attached ? "Attached" : "Input library";
       if (group !== lastGroup) {
         const heading = document.createElement("div");
         heading.className = "mention-group";
         heading.textContent = group;
         menu.append(heading);
         lastGroup = group;
       }
       const button = document.createElement("button");
       button.type = "button"; button.className = `mention-option${i ? "" : " on"}`;
       const preview = document.createElement("span");
       preview.className = `mention-preview ${ref.kind}`;
       if (ref.kind === "image") {
         const image = document.createElement("img");
         image.src = `/media/input/${encodeURIComponent(ref.name)}`;
         image.alt = "";
         preview.append(image);
       } else {
         preview.textContent = ref.kind === "video" ? "▶" : "♪";
       }
       const details = document.createElement("span");
       details.innerHTML = `${ref.name}<small>${ref.attached ? "attached" : "attach from library"}</small>`;
       button.append(preview, details);
       button.dataset.optionIndex = i;
       button.onmousedown = (event) => { event.preventDefault(); insertMention(ref); };
       menu.append(button);
    });
    menu.hidden = !options.length;
    sync();
  });
  $("prompt").addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "z") {
      event.preventDefault();
      document.execCommand(event.shiftKey ? "redo" : "undo");
      readPromptEditor();
      sync();
      return;
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "c") {
      closeMentionMenu();
      return;
    }
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "v") {
      event.preventDefault();
      closeMentionMenu();
      const savedRange = getSelection()?.rangeCount ? getSelection().getRangeAt(0).cloneRange() : null;
      navigator.clipboard?.readText().then((text) => insertPromptText(text, savedRange)).catch(() => {});
      return;
    }
    if (event.key === "Backspace") {
      const selection = getSelection();
      if (selection?.isCollapsed && selection.anchorNode?.nodeType === Node.TEXT_NODE &&
          selection.anchorOffset === 0 && selection.anchorNode.previousSibling?.dataset?.refId) {
        event.preventDefault();
        selection.anchorNode.previousSibling.remove();
        readPromptEditor(); renderPromptEditor(); sync();
        return;
      }
    }
    if (!state.mention || $("mentionMenu").hidden) return;
    const options = state.mention.options;
    if (event.key === "Escape") { event.preventDefault(); closeMentionMenu(); return; }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
       event.preventDefault();
       state.mention.selected = (state.mention.selected + (event.key === "ArrowDown" ? 1 : options.length - 1)) % options.length;
       [...$("mentionMenu").querySelectorAll(".mention-option")].forEach((el) =>
         el.classList.toggle("on", +el.dataset.optionIndex === state.mention.selected));
    } else if (event.key === "Enter" || event.key === "Tab") {
       event.preventDefault(); insertMention(options[state.mention.selected]);
    }
  });

  $("modeSwitch").onclick = (e) => {
    if (e.target.dataset.mode) { setMode(e.target.dataset.mode); sync(); }
  };
  $("dice").onclick = () => { $("seed").value = Math.floor(Math.random() * 2 ** 31); sync(); };

  async function switchSession(name) {
    if (!name) return;
    if (state.cfg?.session && name !== state.cfg.session &&
        !confirm(`Switch to session "${name}"?\nThe web UI will reload with its saved settings and blank current selections.`)) {
      $("sessionSelect").value = state.cfg.session;
      return;
    }
    // Clear terminal output when switching sessions
    $("terminalOutput").textContent = "";
    const active = await fetch("/api/session/activate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name }),
    });
    const activeData = await active.json();
    if (activeData.error) {
      appendLog("!! " + activeData.error);
      $("sessionSelect").value = state.cfg.session;
      return;
    }
    if (name !== state.cfg.session) {
      window.location.reload();
      return;
    }
    const res = await fetch(`/api/session/${encodeURIComponent(name)}`);
    if (!res.ok) {
      state.inputs = [];
      state.outputs = [];
      renderLibrary();
      renderTakes();
      // Clear terminal output when session doesn't exist (new session)
      $("terminalOutput").textContent = "";
      return;
    }
    const p = await res.json();
    state.promptDoc = Array.isArray(p.prompt_doc) ? p.prompt_doc : [{ type: "text", value: p.prompt || "" }];
    $("label").value = p.label || "";
    $("width").value = p.width || 512;
    $("height").value = p.height || 512;
    $("steps").value = p.steps || 4;
    $("layers").value = p.layers || 50;
    $("reuse").value = p.reuse || 1;
    $("seed").value = p.seed || 42;
    $("frames").value = LEGAL.indexOf(p.frames);
    $("internal").value = p.render_width ? String(p.render_width / p.width) : "1";
    $("tokenReduction").checked = !!p.token_reduction;
    $("int8RowFc2").checked = !!p.int8_row_fc2;
    $("ssdStreaming").checked = !!p.ssd_streaming;
    $("runMode").value = p.run_mode || "oneshot";
    $("prefetchDepth").value = p.env?.H3_QWEN_PREFETCH_DEPTH || "";
    $("prefetchWorkers").value = p.env?.H3_QWEN_PREFETCH || "";
    state.refs = (p.refs || [
      ...(p.ref_images || []).map((name) => ({ name, kind: "image" })),
      ...(p.ref_videos || []).map((clip) => ({ name: clip.name, kind: "video", mode: clip.silent ? "silent" : "keep" })),
      ...(p.ref_audio || []).map((name) => ({ name, kind: "audio" })),
    ]).map((ref, i) => ({
      id: ref.id || `${Date.now()}-${i}`, mode: ref.kind === "video" ? (ref.mode || "keep") : undefined,
      pairedAudio: ref.pairedAudio || null, duration: ref.duration ?? null, ...ref,
    }));
    state.first = p.first_frame || null;
    state.last = p.last_frame || null;
    renderRefs();
    renderPromptEditor();
    renderAnchors();
    fetch("/api/inputs").then((r) => r.json()).then((items) => {
      state.inputs = items;
      renderLibrary();
    });
    fetch("/api/outputs").then((r) => r.json()).then((items) => {
      state.outputs = items;
      renderTakes();
    });
    sync();
  }

  $("sessionSelect").onchange = (e) => {
    if (e.target.value) switchSession(e.target.value);
  };
  const closeSessionModal = () => {
    $("sessionModal").hidden = true;
    $("sessionError").hidden = true;
  };
  $("newSession").onclick = () => {
    $("newSessionName").value = "";
    $("sessionModal").hidden = false;
    $("newSessionName").focus();
  };
  $("cancelSession").onclick = closeSessionModal;
  $("createSession").onclick = async () => {
    const name = $("newSessionName").value.trim();
    if (!name) {
      $("sessionError").textContent = "Enter a session name.";
      $("sessionError").hidden = false;
      $("newSessionName").focus();
      return;
    }
    const active = await fetch("/api/session/activate", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: name.trim() }),
    });
    const data = await active.json();
    if (data.error) {
      $("sessionError").textContent = data.error;
      $("sessionError").hidden = false;
      return;
    }
    // Clear terminal output when creating a new session
    $("terminalOutput").textContent = "";
    window.location.reload();
  };
  $("newSessionName").onkeydown = (e) => {
    if (e.key === "Enter") $("createSession").click();
    if (e.key === "Escape") closeSessionModal();
  };

  $("scaffold").onclick = () => {
    const tokens = {};
    state.refs.forEach((ref, i) => {
      const n = state.refs.slice(0, i + 1).filter((item) => item.kind === ref.kind).length;
      tokens[ref.kind] = tokens[ref.kind] || [];
      tokens[ref.kind].push(`<${ref.kind === "image" ? "Picture" : ref.kind[0].toUpperCase() + ref.kind.slice(1)} ${n}>`);
    });
    const subject = Object.values(tokens).flat()[0] || "<subject>";
    const audio = tokens.audio?.[0] || "the ambience";
    state.promptDoc = [{ type: "text", value:
      `Scene: ${subject} stands in ...\nAction: ...\nCamera: ...\nLook: ...\nAudio: match the ambience of ${audio}` }];
    renderPromptEditor(); $("prompt").focus(); sync();
  };

  $("prompt").addEventListener("paste", (e) => {
    const text = e.clipboardData?.getData("text/plain");
    if (text == null) {
      e.preventDefault();
      const savedRange = getSelection()?.rangeCount ? getSelection().getRangeAt(0).cloneRange() : null;
      navigator.clipboard?.readText().then((value) => insertPromptText(value, savedRange)).catch(() => {});
      return;
    }
    e.preventDefault();
    insertPromptText(text);
  });

  $("file").onchange = (e) => upload(e.target.files);
  const drop = $("drop");
  ["dragenter", "dragover"].forEach((ev) =>
    drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.add("over"); }));
  ["dragleave", "drop"].forEach((ev) =>
    drop.addEventListener(ev, (e) => { e.preventDefault(); drop.classList.remove("over"); }));
  drop.addEventListener("drop", (e) => upload(e.dataTransfer.files));

  $("anchorFirst").onclick = () => { state.first = null; renderAnchors(); sync(); };
  $("anchorLast").onclick = () => { state.last = null; renderAnchors(); sync(); };

  $("render").onclick = () => submit(params());
  $("queueBtn").onclick = () => {
    const base = params();
    for (let i = 0; i < 3; i++) {
      submit({ ...base, seed: Math.floor(Math.random() * 2 ** 31),
               label: (base.label || "take") + "-" + (i + 1) });
    }
  };
  $("cancel").onclick = async () => {
    const button = $("cancel");
    button.disabled = true;
    button.textContent = "Stopping…";
    try {
      const res = await fetch("/api/cancel", {
        method: "POST",
        body: JSON.stringify({ id: state.job?.id }),
      });
      const data = await res.json();
      if (!data.ok) {
        button.disabled = false;
        button.textContent = "Stop";
        appendLog("!! Could not stop the active render.");
      }
    } catch (error) {
      button.disabled = false;
      button.textContent = "Stop";
      appendLog("!! Stop failed: " + error.message);
    }
  };

  document.querySelector(".tabs").onclick = (e) => {
    if (!e.target.dataset.tab) return;
    [...e.currentTarget.children].forEach((b) => b.classList.toggle("on", b === e.target));
    $("terminalOutput").hidden = e.target.dataset.tab !== "log";
    $("profile").hidden = e.target.dataset.tab !== "profile";
  };

  $("terminalForm").onsubmit = async (e) => {
    e.preventDefault();
    const input = $("terminalCommand");
    const command = input.value.trim();
    if (!command) return;
    appendLog("$ " + command);
    input.value = "";
    const res = await fetch("/api/terminal", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ command }),
    });
    const data = await res.json();
    if (data.error) appendLog("!! " + data.error);
  };

  $("loadH3").onclick = async () => {
    const res = await fetch("/api/interactive/load", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(params()),
    });
    const data = await res.json();
    if (data.error) appendLog("!! " + data.error);
    else {
      appendLog("$ h3 -d " + state.cfg.model);
      $("sendH3").disabled = false;
      $("runMode").value = "interactive";
      sync();
    }
  };

  $("interactiveForm").onsubmit = async (e) => {
    e.preventDefault();
    const input = $("interactiveInput");
    const line = input.value.trim();
    if (!line) return;
    appendLog("h3> " + line);
    input.value = "";
    const res = await fetch("/api/interactive/input", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ line }),
    });
    const data = await res.json();
    if (data.error) appendLog("!! " + data.error);
  };

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && !$("render").disabled) {
      $("render").click();
    }
  });

  fetch("/api/config").then((r) => r.json()).then((c) => {
    state.cfg = c;
    $("sessionSelect").value = c.session || "session-1";
    $("loadH3").hidden = false;
    $("sendH3").hidden = false;
    $("interactiveForm").hidden = false;
    $("modelPath").textContent = c.model;
    $("h3Path").textContent = c.h3;
    switchSession(c.session || "session-1");
  });
  fetch("/api/sessions").then((r) => r.json()).then((sessions) => {
    const select = $("sessionSelect");
    select.innerHTML = '<option value="">Select session</option>';
    sessions.forEach((s) => {
      const option = document.createElement("option");
      option.value = s.name;
      option.textContent = s.name;
      select.append(option);
    });
    select.value = state.cfg?.session || "";
  });

  $("modelButton").onclick = () => {
    $("pathPanel").hidden = false;
    $("modelPathInput").value = state.cfg.model;
    $("h3PathInput").value = state.cfg.h3;
  };
  $("changeH3").onclick = () => {
    $("pathPanel").hidden = true;
    $("h3Error").hidden = true;
    $("h3PathInput").value = state.cfg.h3;
    $("h3Modal").hidden = false;
    $("h3PathInput").focus();
  };
  $("cancelH3").onclick = () => { $("h3Modal").hidden = true; };
  $("saveH3Path").onclick = async () => {
    const h3 = $("h3PathInput").value.trim();
    const res = await fetch("/api/h3", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ h3 }),
    });
    const data = await res.json();
    if (data.error) {
      $("h3Error").textContent = data.error;
      $("h3Error").hidden = false;
      return;
    }
    state.cfg.h3 = data.h3;
    $("h3Path").textContent = data.h3;
    $("h3Modal").hidden = true;
  };
  $("h3PathInput").onkeydown = (e) => {
    if (e.key === "Enter") $("saveH3Path").click();
    if (e.key === "Escape") $("cancelH3").click();
  };
  $("changeModel").onclick = () => {
    $("pathPanel").hidden = true;
    $("modelError").hidden = true;
    $("modelPathInput").value = state.cfg.model;
    $("modelModal").hidden = false;
    $("modelPathInput").focus();
  };
  $("cancelModel").onclick = () => { $("modelModal").hidden = true; };
  $("saveModel").onclick = async () => {
    const model = $("modelPathInput").value.trim();
    const res = await fetch("/api/model", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ model }),
    });
    const data = await res.json();
    if (data.error) {
      $("modelError").textContent = data.error;
      $("modelError").hidden = false;
      return;
    }
    state.cfg.model = data.model;
    $("modelPath").textContent = data.model;
    $("modelModal").hidden = true;
  };
  $("modelPathInput").onkeydown = (e) => {
    if (e.key === "Enter") $("saveModel").click();
    if (e.key === "Escape") $("cancelModel").click();
  };

  document.addEventListener("click", (e) => {
    if (e.target.closest("#pathButtons") || e.target.closest("#pathPanel")) return;
    $("pathPanel").hidden = true;
    [...$("pathButtons").children].forEach((b) => b.classList.remove("on"));
  });

  document.addEventListener("click", (e) => {
    const command = document.querySelector("details.cmd");
    if (command?.open && !e.target.closest("details.cmd")) command.open = false;
  });

  // Terminal resize functionality
  const resizeHandle = $("resizeHandle");
  const resizeBtn = $("resizeBtn");
  const consoleContainer = $("consoleContainer");
  const stageContainer = document.querySelector(".stage");
  let isResizing = false;
  let startY = 0;
  let startHeight = 0;

  console.log("Resize handle found:", !!resizeHandle);
  console.log("Resize button found:", !!resizeBtn);
  console.log("Console container found:", !!consoleContainer);

  // Button drag to resize terminal
  resizeBtn.addEventListener("mousedown", (e) => {
    console.log("Resize button drag started");
    isResizing = true;
    startY = e.clientY;
    startHeight = consoleContainer.offsetHeight;
    document.body.style.cursor = "ns-resize";
    document.body.style.userSelect = "none";
    resizeBtn.classList.add("resizing");
    e.preventDefault();
  });

  // Button click to toggle terminal size
  resizeBtn.addEventListener("click", (e) => {
    if (isResizing) {
      e.preventDefault();
      return;
    }
    const currentHeight = consoleContainer.offsetHeight;
    const newHeight = currentHeight > 250 ? 150 : 300;
    consoleContainer.style.height = `${newHeight}px`;
  });

  resizeHandle.addEventListener("mousedown", (e) => {
    console.log("Resize started");
    isResizing = true;
    startY = e.clientY;
    startHeight = consoleContainer.offsetHeight;
    document.body.style.cursor = "ns-resize";
    document.body.style.userSelect = "none";
    resizeHandle.classList.add("resizing");
    e.preventDefault();
  });

  document.addEventListener("mousemove", (e) => {
    if (!isResizing) return;
    console.log("Resizing:", e.clientY, "delta:", startY - e.clientY);

    const deltaY = startY - e.clientY;
    const newHeight = Math.max(150, Math.min(startHeight + deltaY, 500));
    consoleContainer.style.height = `${newHeight}px`;
  });

  document.addEventListener("mouseup", () => {
    if (isResizing) {
      console.log("Resize ended");
      isResizing = false;
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
      resizeHandle.classList.remove("resizing");
    }
  });

  // Touch support for mobile
  resizeHandle.addEventListener("touchstart", (e) => {
    console.log("Touch resize started");
    isResizing = true;
    startY = e.touches[0].clientY;
    startHeight = consoleContainer.offsetHeight;
    resizeHandle.classList.add("resizing");
    e.preventDefault();
  });

  document.addEventListener("touchmove", (e) => {
    if (!isResizing) return;
    console.log("Touch resizing:", e.touches[0].clientY, "delta:", startY - e.touches[0].clientY);

    const deltaY = startY - e.touches[0].clientY;
    const newHeight = Math.max(150, Math.min(startHeight + deltaY, 500));
    consoleContainer.style.height = `${newHeight}px`;
  });

  document.addEventListener("touchend", () => {
    if (isResizing) {
      console.log("Touch resize ended");
      isResizing = false;
      resizeHandle.classList.remove("resizing");
    }
  });

  connect();
  sync();
}

init();
