/* h3 studio — viewer, takes list, preview scrubber, seed grid and A/B wipe. */

"use strict";

/* ── viewer ─────────────────────────────────────────────────────── */

function closeCompare() {
  const box = $("compare");
  box.querySelectorAll("video").forEach((v) => v.pause());
  box.replaceChildren();
  box.hidden = true;
  $("viewer").classList.remove("comparing");
}

/** Close the grid or wipe and go back to the selected take (or the empty viewer). */
function exitCompare() {
  closeCompare();
  if (state.selected) selectTake(state.selected, false);
  else showVideo(null);
}

function hidePreview() {
  resetPreviewQueue();
  $("previewFrame").hidden = true;
  $("previewImg").removeAttribute("src");
  $("previewScrub").hidden = true;
  state.scrub = null;
}

function setCaption(text) {
  $("viewerCaption").hidden = !text;
  $("viewerCaption").textContent = text || "";
}

/** Play a video in the viewer (or clear it when url is null). */
function showVideo(url, caption = "", autoplay = true) {
  closeCompare();
  hidePreview();
  const player = $("player");
  player.pause();
  if (!url) {
    player.removeAttribute("src");
    player.load();
    player.classList.remove("on");
    $("viewerEmpty").hidden = false;
    setCaption("");
    return;
  }
  player.src = url;
  player.classList.add("on");
  $("viewerEmpty").hidden = true;
  setCaption(caption);
  if (autoplay) player.play().catch(() => {});
}

function showPreviewImage(url, badge) {
  closeCompare();
  const player = $("player");
  player.pause();
  player.classList.remove("on");
  $("viewerEmpty").hidden = true;
  $("previewImg").src = url;
  $("previewBadge").textContent = badge;
  $("previewFrame").hidden = false;
}

function previewBadgeText(info) {
  // h3 reports frame numbers 1-based ("frame 22/22").
  const frame = info.frameTotal > 1 ? ` · frame ${info.frameIndex}/${info.frameTotal}` : "";
  const dims = info.width && info.height ? ` · ${info.width}×${info.height}` : "";
  return `Preview ${info.step}/${info.total}${frame}${dims}`;
}

const PREVIEW_MIN_INTERVAL_MS = 300;

// Frames from a multi-frame preview chunk arrive back-to-back. Queue them so
// each stays on screen a moment, dropping frames from an older step as soon
// as a newer step's frame arrives so the preview never lags far behind.
function queuePreviewFrame(info) {
  state.livePreview = info;
  if (!state.followPreview) { renderRunningFromQueue(); return; }
  if (info.step !== state.previewQueueStep) {
    state.previewQueue = [];
    state.previewQueueStep = info.step;
  }
  state.previewQueue.push(info);
  if (!state.previewTimer) {
    state.previewTimer = setInterval(drainPreviewQueue, 100);
    drainPreviewQueue();
  }
}

function drainPreviewQueue() {
  if (!state.previewQueue.length || !state.followPreview) return;
  const now = Date.now();
  if (now - state.previewLastShownAt < PREVIEW_MIN_INTERVAL_MS) return;
  const next = state.previewQueue.shift();
  state.previewLastShownAt = now;
  $("previewScrub").hidden = true;
  state.scrub = null;
  showPreviewImage(next.url, previewBadgeText(next));
  setCaption(`live preview · ${next.count} frame${next.count === 1 ? "" : "s"} so far`);
}

function resetPreviewQueue() {
  state.previewQueue = [];
  state.previewQueueStep = null;
  state.previewLastShownAt = 0;
  clearInterval(state.previewTimer);
  state.previewTimer = null;
}

/* ── preview scrubber ───────────────────────────────────────────── */

function openScrubber(take) {
  const list = take.meta?.previews || [];
  if (!list.length) return;
  state.followPreview = false;
  state.scrub = { take, list };
  const slider = $("previewSlider");
  slider.max = String(list.length - 1);
  slider.value = String(list.length - 1);
  renderScrub();
}

function renderScrub() {
  if (!state.scrub) return;
  const index = Number($("previewSlider").value);
  const rel = state.scrub.list[index];
  const name = rel.split("/").pop();
  const m = name.match(/^s(\d+)of(\d+)(?:-f(\d+)of(\d+))?\.png$/);
  const info = m ? { step: +m[1], total: +m[2], frameIndex: m[3] ? +m[3] : 0, frameTotal: m[4] ? +m[4] : 0 } : { step: 0, total: 0 };
  showPreviewImage(mediaURL(rel), previewBadgeText(info));
  $("previewScrub").hidden = false;
  $("previewScrubLabel").textContent = `${index + 1}/${state.scrub.list.length}`;
  setCaption(`${state.scrub.take.name} · saved previews`);
}

/* ── takes list ─────────────────────────────────────────────────── */

async function loadTakes(selectName = null) {
  if (!state.session) return;
  state.takes = await api(`/api/takes?session=${encodeURIComponent(state.session)}`);
  renderTakes();
  // Never auto-select over a seed grid or A/B wipe that is open or opening.
  if (selectName && $("compare").hidden && !state.batchOpening) selectTake(selectName, false);
}

function takeMeta(take) {
  const p = take.meta?.params || {};
  const probe = take.probe || {};
  const bits = [];
  const width = probe.width || p.width, height = probe.height || p.height;
  if (width && height) bits.push(`${width}×${height}`);
  const frames = probe.frames || p.frames;
  if (frames) bits.push(`${frames}f`);
  else if (probe.duration) bits.push(`${probe.duration.toFixed(1)}s`);
  if (p.steps) bits.push(`${p.steps} steps`);
  if (p.seed !== undefined && p.seed !== null) bits.push(`seed ${p.seed}`);
  if (p.run_mode === "interactive") bits.push("interactive");
  if (take.meta?.duration_s) bits.push(fmtSecs(take.meta.duration_s));
  return bits.join(" · ");
}

function takeCaption(take) {
  const prompt = take.meta?.params?.prompt;
  return [take.name, takeMeta(take), prompt ? `“${prompt.length > 160 ? prompt.slice(0, 160) + "…" : prompt}”` : ""]
    .filter(Boolean).join("  ·  ");
}

function takeButton(label, title, fn, cls = "ghost") {
  return el("button", {
    class: cls, type: "button", text: label, title,
    onclick: async (event) => {
      event.stopPropagation();
      closePopmenus();
      try { await fn(); } catch (err) { toast(err.message, { kind: "error" }); }
    },
  });
}

function closePopmenus(except = null) {
  document.querySelectorAll(".popmenu").forEach((menu) => { if (menu !== except) menu.hidden = true; });
  document.querySelectorAll("[aria-expanded='true']").forEach((btn) => btn.setAttribute("aria-expanded", "false"));
}

function renderTakes() {
  const starredOnly = $("starFilter").checked;
  const takes = starredOnly ? state.takes.filter((t) => t.meta?.starred) : state.takes;
  $("takeCount").textContent = state.takes.length || "";
  if (!takes.length) {
    $("takes").replaceChildren(el("li", { class: "list-empty",
      text: starredOnly ? "No starred takes in this session." : "No takes yet in this session." }));
    renderCompareBar();
    return;
  }
  $("takes").replaceChildren(...takes.map((take) => {
    const meta = take.meta || {};
    const previews = meta.previews || [];
    const starred = !!meta.starred;
    const comparing = state.compare.includes(take.name);
    const menu = el("div", { class: "popmenu", role: "menu", hidden: true },
      takeButton("First frame → input", "Extract the first frame into inputs and use it", () => useFrame(take, "first"), ""),
      probeHasAudio(take) ? takeButton("Use audio", "Extract the audio track into inputs as a reference", () => useAudio(take), "") : null,
      previews.length ? takeButton(`Previews (${previews.length})`, "Scrub through the previews saved during this render", async () => openScrubber(take), "") : null,
      takeButton(comparing ? "Remove from compare" : "Add to compare", "Pick takes for the grid or A/B wipe", async () => toggleCompare(take.name), ""),
      el("a", { class: "menu-link", href: `${take.url}?download=1`, text: "Download", onclick: (e) => e.stopPropagation() }),
      takeButton("Delete…", "Delete this take", () => deleteMedia(take.name, "output"), "danger-item"));
    const menuBtn = el("button", {
      class: "ghost", type: "button", text: "⋮", title: "More actions", "aria-haspopup": "menu",
      onclick: (event) => {
        event.stopPropagation();
        const open = menu.hidden;
        closePopmenus(menu);
        menu.hidden = !open;
        menuBtn.setAttribute("aria-expanded", String(open));
      },
    });
    return el("li", {
      class: [take.name === state.selected ? "on" : "", comparing ? "comparing" : ""].join(" "),
      onclick: () => selectTake(take.name),
    },
      el("div", { class: "row" },
        el("div", { class: "thumb" }, el("img", { src: take.thumb, alt: "", loading: "lazy" })),
        el("div", { class: "info" },
          el("div", { class: "nm", text: take.name, title: meta.params?.prompt || take.name }),
          el("div", { class: "meta", text: takeMeta(take) })),
        el("button", {
          class: `star${starred ? " on" : ""}`, type: "button", title: starred ? "Unstar" : "Star",
          "aria-pressed": String(starred), text: starred ? "★" : "☆",
          onclick: (event) => { event.stopPropagation(); setStar(take, !starred); },
        })),
      el("div", { class: "ops" },
        takeButton("Chain →", "Use the last frame as the first-frame anchor of the next shot", () => chain(take)),
        takeButton("Reuse", "Restore this take's settings into the form", async () => restore(meta.params)),
        takeButton("Use ref", "Copy this take into inputs and add it as a video reference", () => useVideoRef(take)),
        takeButton("Use frame", "Extract the last frame and add it as a reference or anchor", () => useFrame(take, "last")),
        el("div", { class: "menuwrap" }, menuBtn, menu)));
  }));
  renderCompareBar();
}

function probeHasAudio(take) { return !!take.probe?.has_audio; }

function selectTake(name, fromUser = true) {
  const take = state.takes.find((t) => t.name === name);
  if (!take) return;
  if (fromUser && state.runningId) state.followPreview = false;
  state.selected = name;
  state.selectedTimeline = null;
  showVideo(take.url, takeCaption(take));
  document.querySelectorAll("#takes > li").forEach((li) => li.classList.remove("on"));
  renderTakes();
  renderTimelineList();
  renderRunningFromQueue();
}

async function setStar(take, starred) {
  try {
    await api("/api/take/star", { session: state.session, name: take.name, starred });
    take.meta = { ...(take.meta || {}), starred };
    renderTakes();
  } catch (err) {
    toast(err.message, { kind: "error" });
  }
}

async function deleteMedia(name, kind) {
  const label = kind === "output" ? "take and its video" : kind === "timeline" ? "combined video" : "input file";
  if (!confirm(`Permanently delete this ${label}?\n${name}\n\nThis cannot be undone.`)) return;
  await api("/api/delete", { session: state.session, name, kind });
  if (kind === "output") {
    state.compare = state.compare.filter((n) => n !== name);
    if (state.selected === name) { state.selected = null; showVideo(null); }
    await loadTakes();
  } else if (kind === "timeline") {
    if (state.selectedTimeline === name) { state.selectedTimeline = null; showVideo(null); }
    await loadTimeline();
  } else {
    state.refs = state.refs.filter((ref) => ref.name !== name && ref.pairedAudio !== name);
    if (state.first === name) state.first = null;
    if (state.last === name) state.last = null;
    await loadInputs();
    renderRefs(); renderPromptEditor(); renderAnchors(); sync();
  }
}

/* ── continuing from a take ─────────────────────────────────────── */

async function chain(take) {
  const data = await api("/api/frame", { session: state.session, name: take.name, kind: "output", position: "last" });
  state.inputs = data.inputs;
  renderLibrary();
  setMode("anchor");
  state.first = data.name;
  state.last = null;
  renderAnchors();
  sync();
  toast(`${data.name} is now the first frame.`, { kind: "ok" });
}

async function useFrame(item, position, kind = "output") {
  const data = await api("/api/frame", { session: state.session, name: item.name, kind, position });
  state.inputs = data.inputs;
  renderLibrary();
  const added = addRef({ name: data.name, kind: "image", probe: data.probe });
  if (added) toast(`Added ${data.name}${state.mode === "anchor" ? " as an anchor" : " as a reference"}.`, { kind: "ok" });
}

async function useVideoRef(item, kind = "output") {
  if (state.mode !== "ref") setMode("ref");
  if (state.refs.filter((ref) => ref.kind === "video").length >= 3) throw new Error("At most 3 video references.");
  const data = await api("/api/use-video", { session: state.session, name: item.name, kind });
  state.inputs = data.inputs;
  renderLibrary();
  if (addRef({ name: data.name, kind: "video", duration: data.duration, probe: data.probe })) {
    toast(`Added ${data.name} as a video reference.`, { kind: "ok" });
  }
}

async function useAudio(take) {
  if (state.mode !== "ref") setMode("ref");
  const data = await api("/api/audio", { session: state.session, name: take.name, kind: "output" });
  state.inputs = data.inputs;
  renderLibrary();
  if (addRef({ name: data.name, kind: "audio", duration: data.duration, probe: data.probe })) {
    toast(`Added ${data.name} as an audio reference.`, { kind: "ok" });
  }
}

/* ── compare: seed grid and A/B wipe ────────────────────────────── */

function toggleCompare(name) {
  state.compare = state.compare.includes(name) ? state.compare.filter((n) => n !== name) : [...state.compare, name].slice(-4);
  renderTakes();
}

function renderCompareBar() {
  const n = state.compare.length;
  $("compareBar").hidden = n === 0;
  $("compareCount").textContent = `${n} picked`;
  $("compareGrid").disabled = n < 2;
  $("compareWipe").disabled = n !== 2;
}

/** Keep several videos in lockstep with the first one. */
function syncVideos(videos) {
  const [lead, ...rest] = videos;
  let syncing = false;
  const follow = (fn) => { if (syncing) return; syncing = true; rest.forEach(fn); syncing = false; };
  lead.addEventListener("play", () => follow((v) => v.play().catch(() => {})));
  lead.addEventListener("pause", () => follow((v) => v.pause()));
  lead.addEventListener("seeked", () => follow((v) => { v.currentTime = lead.currentTime; }));
  lead.addEventListener("ratechange", () => follow((v) => { v.playbackRate = lead.playbackRate; }));
  lead.addEventListener("timeupdate", () => follow((v) => {
    if (Math.abs(v.currentTime - lead.currentTime) > 0.12) v.currentTime = lead.currentTime;
  }));
  lead.addEventListener("ended", () => follow((v) => v.pause()));
  return lead;
}

function openCompareGrid(names = state.compare) {
  const takes = names.map((name) => state.takes.find((t) => t.name === name)).filter(Boolean);
  if (takes.length < 2) return;
  hidePreview();
  $("player").pause();
  $("player").classList.remove("on");
  $("viewerEmpty").hidden = true;
  const videos = takes.map((take) => el("video", { src: take.url, muted: true, playsInline: true, loop: true, preload: "auto" }));
  const cells = takes.map((take, i) => {
    const starred = !!take.meta?.starred;
    return el("figure", { class: "compare-cell" }, videos[i],
      el("figcaption", {},
        el("span", { class: "nm", text: `seed ${take.meta?.params?.seed ?? "?"}`, title: take.name }),
        el("button", {
          class: `ghost sm keep${starred ? " on" : ""}`, type: "button", text: starred ? "★ Kept" : "☆ Keep",
          onclick: async (event) => {
            await setStar(take, !take.meta?.starred);
            event.target.textContent = take.meta?.starred ? "★ Kept" : "☆ Keep";
            event.target.classList.toggle("on", !!take.meta?.starred);
          },
        }),
        el("button", { class: "ghost sm", type: "button", text: "Open", onclick: () => selectTake(take.name) })));
  });
  const lead = syncVideos(videos);
  const controls = el("div", { class: "compare-controls" },
    el("button", { class: "ghost sm", type: "button", text: "Play / pause", onclick: () => (lead.paused ? lead.play().catch(() => {}) : lead.pause()) }),
    el("button", { class: "ghost sm", type: "button", text: "Restart", onclick: () => { lead.currentTime = 0; lead.play().catch(() => {}); } }),
    el("span", { class: "hint", text: "Muted and looped in sync · Keep stars a take" }),
    el("button", { class: "ghost sm", type: "button", text: "Close", onclick: exitCompare }));
  const box = $("compare");
  box.replaceChildren(el("div", { class: `compare-grid n${takes.length}` }, cells), controls);
  box.hidden = false;
  $("viewer").classList.add("comparing");
  setCaption(`Comparing ${takes.length} takes`);
  videos.forEach((v) => v.addEventListener("loadeddata", () => { if (videos.every((x) => x.readyState >= 2)) lead.play().catch(() => {}); }, { once: true }));
}

function openWipe(names = state.compare) {
  const takes = names.map((name) => state.takes.find((t) => t.name === name)).filter(Boolean);
  if (takes.length !== 2) return;
  hidePreview();
  $("player").pause();
  $("player").classList.remove("on");
  $("viewerEmpty").hidden = true;
  const [a, b] = takes;
  const videoA = el("video", { src: a.url, playsInline: true, loop: true, preload: "auto", class: "wipe-a" });
  const videoB = el("video", { src: b.url, playsInline: true, muted: true, loop: true, preload: "auto", class: "wipe-b" });
  const divider = el("div", { class: "wipe-divider" });
  const stage = el("div", { class: "wipe-stage" }, videoA, videoB, divider,
    el("span", { class: "wipe-label left", text: `A · ${a.name}` }),
    el("span", { class: "wipe-label right", text: `B · ${b.name}` }));
  const setWipe = (pct) => {
    videoB.style.clipPath = `inset(0 0 0 ${pct}%)`;
    divider.style.left = `${pct}%`;
  };
  const slider = el("input", { type: "range", min: "0", max: "100", value: "50", "aria-label": "Wipe position",
    oninput: (e) => setWipe(+e.target.value) });
  stage.addEventListener("pointermove", (e) => {
    if (!(e.buttons & 1)) return;
    const rect = stage.getBoundingClientRect();
    const pct = Math.min(100, Math.max(0, ((e.clientX - rect.left) / rect.width) * 100));
    slider.value = String(pct);
    setWipe(pct);
  });
  setWipe(50);
  const lead = syncVideos([videoA, videoB]);
  const box = $("compare");
  box.replaceChildren(stage, el("div", { class: "compare-controls" },
    el("button", { class: "ghost sm", type: "button", text: "Play / pause", onclick: () => (lead.paused ? lead.play().catch(() => {}) : lead.pause()) }),
    slider,
    el("button", { class: "ghost sm", type: "button", text: "Close", onclick: exitCompare })));
  box.hidden = false;
  $("viewer").classList.add("comparing");
  setCaption("A/B wipe · drag across the video or use the slider");
}

/** Track a multi-seed submit and open the grid once every take is done. */
function trackCompareBatch(jobIds) {
  state.compareBatch = { ids: new Set(jobIds), outputs: [], pending: jobIds.length };
}

function noteBatchJob(job) {
  const batch = state.compareBatch;
  if (!batch || !batch.ids.has(job.id)) return;
  if (!["done", "failed", "cancelled"].includes(job.state)) return;
  batch.ids.delete(job.id);
  if (job.state === "done" && job.output) batch.outputs.push(job.output);
  if (batch.ids.size) return;
  state.compareBatch = null;
  if (batch.outputs.length >= 2) {
    state.batchOpening = true;
    loadTakes().then(() => {
      state.compare = batch.outputs.slice(0, 4);
      renderTakes();
      openCompareGrid(state.compare);
    }).finally(() => { state.batchOpening = false; });
  }
}
