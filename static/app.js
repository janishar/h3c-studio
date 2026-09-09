/* h3 studio client */

const $ = (id) => document.getElementById(id);
const LEGAL = Array.from({ length: 22 }, (_, n) => 5 + 17 * n);
const H3_FPS = 24;
const MAX_PIXELS = 768 * 1344;

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
};

const SIZES = [
  ["512 square", 512, 512],
  ["768 square", 768, 768],
  ["1344 × 768", 1344, 768],
  ["768 × 1344", 768, 1344],
  ["1024 × 768", 1024, 768],
  ["448 × 800", 448, 800],
  ["256 preview", 256, 256],
];

const QUALITY = [
  ["Draft", { steps: 4, layers: 50, reuse: 1, token: false }],
  ["Fast", { steps: 20, layers: 45, reuse: 2, token: true }],
  ["Default", { steps: 20, layers: 45, reuse: 2, token: false }],
  ["Reference", { steps: 50, layers: 50, reuse: 1, token: false }],
];

/* ── setup ─────────────────────────────────────────────────────── */

function buildChips() {
  $("sizePresets").innerHTML = "";
  SIZES.forEach(([label, w, h]) => {
    const b = document.createElement("button");
    b.textContent = label;
    b.onclick = () => { $("width").value = w; $("height").value = h; sync(); };
    $("sizePresets").append(b);
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

function markChips() {
  const w = +$("width").value, h = +$("height").value;
  [...$("sizePresets").children].forEach((b, i) =>
    b.classList.toggle("on", SIZES[i][1] === w && SIZES[i][2] === h));
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
  const p = {
    label: $("label").value.trim(),
    prompt: $("prompt").value.trim(),
    width: w, height: h,
    frames: frames(),
    steps: +$("steps").value,
    layers: +$("layers").value,
    reuse: +$("reuse").value,
    seed: +$("seed").value,
    token_reduction: $("tokenReduction").checked,
    int8_row_fc2: $("int8RowFc2").checked,
    ssd_streaming: $("ssdStreaming").checked,
    ref_images: [], ref_videos: [], ref_audio: [],
    env: {},
  };
  if (scale < 1) {
    p.render_width = Math.round(w * scale / 32) * 32;
    p.render_height = Math.round(h * scale / 32) * 32;
  }
  if (state.mode === "ref") {
    state.refs.forEach((r) => {
      if (r.kind === "image") p.ref_images.push(r.name);
      else if (r.kind === "video") p.ref_videos.push({ name: r.name, silent: false });
      else p.ref_audio.push(r.name);
    });
  } else {
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
  if (p.width % 32 || p.height % 32) e.push("Width and height must be multiples of 32.");
  if (p.width * p.height > MAX_PIXELS)
    e.push(`${p.width}×${p.height} is ${(p.width * p.height).toLocaleString()} pixels; the ceiling is ${MAX_PIXELS.toLocaleString()}.`);
  if (!p.prompt) e.push("Write a prompt.");
  if (state.mode === "ref" && !state.refs.length) e.push("Ref2VA needs at least one reference.");
  if (p.ref_audio?.length && !p.ref_images.length && !p.ref_videos.length)
    e.push("A standalone audio reference must accompany an image or video.");
  if (state.mode === "anchor" && !state.first && !state.last)
    e.push("Set a first or last frame, or switch to Reference mode.");
  return e;
}

function commandPreview(p) {
  const q = (s) => (/[\s"']/.test(s) ? `'${s}'` : s);
  const env = Object.entries(p.env).map(([k, v]) => `${k}=${v}`).join(" ");
  const a = ["./h3", "--profile", "-d", q(state.cfg?.model || "MODEL")];
  a.push("-p", `'${p.prompt.replace(/\n/g, " ").slice(0, 60)}…'`);
  p.ref_images.forEach((n) => a.push("--ref-image", q(`input/${n}`)));
  p.ref_videos.forEach((v) => a.push("--ref-video", q(`input/${v.name}`)));
  p.ref_audio.forEach((n) => a.push("--ref-audio", q(`input/${n}`)));
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
  const n = frames();
  $("frameCount").textContent = n;
  $("frameSecs").textContent = (n / H3_FPS).toFixed(2) + " s";
  $("promptCount").textContent = p.prompt.length + " chars";
  markChips();

  const px = p.width * p.height;
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
    li.innerHTML = `<span class="n"></span>`;
    if (r.kind === "image") {
      const img = document.createElement("img");
      img.src = `/media/input/${encodeURIComponent(r.name)}`;
      li.append(img);
    }
    const nm = document.createElement("span");
    nm.className = "nm"; nm.textContent = r.name;
    const x = document.createElement("button");
    x.textContent = "×"; x.title = "Remove";
    x.onclick = (e) => { e.stopPropagation(); state.refs.splice(i, 1); renderRefs(); sync(); };
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
  $("refHint").hidden = false;
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
    fig.onclick = () => addRef(f);
    wrap.append(fig);
  });
}

function addRef(f) {
  if (state.mode === "anchor") {
    if (f.kind !== "image") return;
    if (!state.first) state.first = f.name;
    else state.last = f.name;
    renderAnchors();
  } else {
    if (state.refs.length >= 12) return;
    state.refs.push({ name: f.name, kind: f.kind });
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
      addRef({ name: data.name, kind });
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
    li.innerHTML = `<div class="nm">${o.name}</div>
      <div class="meta">${p.width || "?"}×${p.height || "?"} · ${p.frames || "?"}f · ${p.steps || "?"} steps · seed ${p.seed ?? "?"}${secs ? " · " + secs + "s" : ""}</div>`;
    const ops = document.createElement("div");
    ops.className = "ops";
    ops.append(
      mkBtn("Reuse settings", () => restore(o)),
      mkBtn("Chain →", () => chain(o.name)),
      mkBtn("Delete", () => del(o.name)),
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
  v.src = `/media/output/${encodeURIComponent(name)}`;
  v.classList.add("on");
  $("viewerEmpty").hidden = true;
  v.play().catch(() => {});
  renderTakes();
}

function restore(o) {
  const p = o.meta?.params;
  if (!p) return;
  $("prompt").value = p.prompt || "";
  $("width").value = p.width; $("height").value = p.height;
  $("steps").value = p.steps; $("layers").value = p.layers;
  $("reuse").value = p.reuse || 1; $("seed").value = p.seed;
  $("tokenReduction").checked = !!p.token_reduction;
  $("frames").value = Math.max(0, LEGAL.indexOf(p.frames));
  state.refs = (p.ref_images || []).map((n) => ({ name: n, kind: "image" }));
  (p.ref_videos || []).forEach((v) => state.refs.push({ name: v.name, kind: "video" }));
  (p.ref_audio || []).forEach((n) => state.refs.push({ name: n, kind: "audio" }));
  state.first = p.first_frame || null; state.last = p.last_frame || null;
  setMode(state.first || state.last ? "anchor" : "ref");
  renderRefs(); renderAnchors(); sync();
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

async function del(name) {
  const res = await fetch("/api/delete", { method: "POST", body: JSON.stringify({ name }) });
  const data = await res.json();
  state.outputs = data.outputs;
  if (state.selected === name) {
    state.selected = null;
    $("player").classList.remove("on");
    $("viewerEmpty").hidden = false;
  }
  renderTakes();
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
  const busy = j && (j.state === "running");
  $("running").hidden = !busy;
  $("lamp").classList.toggle("busy", !!busy);
  $("lampText").textContent = busy ? (j.phase || "rendering") : "idle";
  $("render").disabled = !!busy || !!localErrors(params()).length;

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
      const run = payload.queue.find((j) => j.state === "running");
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

  ["width", "height", "steps", "layers", "reuse", "seed", "frames", "prompt",
   "label", "internal", "tokenReduction", "int8RowFc2", "ssdStreaming",
   "zeroCopy", "prefetchDepth", "prefetchWorkers"]
    .forEach((id) => $(id).addEventListener("input", sync));

  $("modeSwitch").onclick = (e) => {
    if (e.target.dataset.mode) { setMode(e.target.dataset.mode); sync(); }
  };
  $("dice").onclick = () => { $("seed").value = Math.floor(Math.random() * 2 ** 31); sync(); };

  $("scaffold").onclick = () => {
    $("prompt").value =
      "Scene: \nAction: \nCamera: \nLook: \nAudio: ";
    $("prompt").focus(); sync();
  };

  $("prompt").addEventListener("paste", (e) => {
    const text = e.clipboardData?.getData("text/plain");
    if (text == null) return;
    e.preventDefault();
    const input = e.currentTarget;
    const start = input.selectionStart;
    const end = input.selectionEnd;
    input.setRangeText(text, start, end, "end");
    input.dispatchEvent(new Event("input", { bubbles: true }));
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
  $("cancel").onclick = () =>
    fetch("/api/cancel", { method: "POST", body: JSON.stringify({ id: state.job?.id }) });

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

  document.addEventListener("keydown", (e) => {
    if ((e.metaKey || e.ctrlKey) && e.key === "Enter" && !$("render").disabled) {
      $("render").click();
    }
  });

  fetch("/api/config").then((r) => r.json()).then((c) => {
    state.cfg = c;
    $("modelPath").textContent = c.model;
    $("inputPath").textContent = c.inputs;
    $("outputPath").textContent = c.outputs;
    sync();
  });

  $("pathButtons").onclick = (e) => {
    const button = e.target.closest("[data-path]");
    if (!button) return;
    $("pathPanel").hidden = false;
    [...$("pathButtons").children].forEach((b) => b.classList.toggle("on", b === button));
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

  connect();
  sync();
}

init();
