/* h3 studio — timeline list and the combine-videos editor. */

"use strict";

const TL = { seq: [], browse: null, dragFrom: null };

async function loadTimeline() {
  if (!state.session) return;
  state.timeline = await api(`/api/timeline?session=${encodeURIComponent(state.session)}`);
  renderTimelineList();
}

function timelineMeta(item) {
  const bits = [];
  const clips = item.meta?.clips?.length;
  if (clips) bits.push(`${clips} clip${clips === 1 ? "" : "s"}`);
  if (item.probe?.width) bits.push(`${item.probe.width}×${item.probe.height}`);
  if (item.probe?.duration) bits.push(`${item.probe.duration.toFixed(1)}s`);
  return bits.join(" · ") || "combined";
}

function renderTimelineList() {
  $("timelineCount").textContent = state.timeline.length || "";
  if (!state.timeline.length) {
    $("timelineList").replaceChildren(el("li", { class: "list-empty", text: "No combined videos yet. Use Create Timeline above to build one." }));
    return;
  }
  $("timelineList").replaceChildren(...state.timeline.map((item) => el("li", {
    class: item.name === state.selectedTimeline ? "on" : "",
    onclick: () => selectTimeline(item.name),
  },
    el("div", { class: "row" },
      el("div", { class: "thumb" }, el("img", { src: item.thumb, alt: "", loading: "lazy" })),
      el("div", { class: "info" },
        el("div", { class: "nm", text: item.name, title: item.name }),
        el("div", { class: "meta", text: timelineMeta(item) }))),
    el("div", { class: "ops" },
      takeButton("Use video", "Copy into inputs and add as a video reference", () => useVideoRef(item, "timeline")),
      takeButton("Last frame", "Extract the last frame into inputs", () => useFrame(item, "last", "timeline")),
      takeButton("Delete", "Delete this combined video", () => deleteMedia(item.name, "timeline"))))));
}

function selectTimeline(name) {
  const item = state.timeline.find((t) => t.name === name);
  if (!item) return;
  state.selectedTimeline = name;
  state.selected = null;
  showVideo(item.url, `${item.name} · ${timelineMeta(item)}`);
  renderTakes();
  renderTimelineList();
}

function openTimelineModal() {
  TL.seq = [];
  renderSequence();
  $("timelineOutputName").value = "";
  $("timelineRenderStatus").textContent = "";
  if (state.timeline[0]) showReview(state.timeline[0]);
  else clearReview();
  $("timelineModal").hidden = false;
  browseTo("");
}

function closeTimelineModal() {
  $("timelineModal").hidden = true;
  $("timelineReviewVideo").pause();
}

function showReview(item) {
  const video = $("timelineReviewVideo");
  video.src = item.url;
  video.load();
  video.classList.add("on");
  $("timelineReviewEmpty").hidden = true;
}

function clearReview() {
  const video = $("timelineReviewVideo");
  video.pause();
  video.removeAttribute("src");
  video.load();
  video.classList.remove("on");
  $("timelineReviewEmpty").hidden = false;
}

async function browseTo(path) {
  try {
    TL.browse = await api(`/api/timeline/browse?session=${encodeURIComponent(state.session)}&path=${encodeURIComponent(path || "")}`);
  } catch (err) {
    toast(err.message, { kind: "error" });
    return;
  }
  renderBreadcrumb();
  renderBrowser();
}

function renderBreadcrumb() {
  const crumbs = [el("button", { type: "button", text: "sessions", onclick: () => browseTo(".") })];
  const parts = TL.browse.path && TL.browse.path !== "." ? TL.browse.path.split("/").filter(Boolean) : [];
  let acc = "";
  for (const part of parts) {
    acc = acc ? `${acc}/${part}` : part;
    const target = acc;
    crumbs.push(el("span", { class: "sep", text: "/" }), el("button", { type: "button", text: part, onclick: () => browseTo(target) }));
  }
  $("timelineBreadcrumb").replaceChildren(...crumbs);
}

function renderBrowser() {
  const data = TL.browse;
  if (!data) return;
  const items = [];
  if (data.parent !== null && data.parent !== undefined) {
    items.push(el("button", { type: "button", class: "browse-dir", onclick: () => browseTo(data.parent || ".") },
      el("div", { class: "icon", text: "⬅" }), el("div", { class: "nm", text: ".." })));
  }
  for (const dir of data.dirs || []) {
    items.push(el("button", { type: "button", class: "browse-dir", onclick: () => browseTo(dir.path) },
      el("div", { class: "icon", text: "📁" }), el("div", { class: "nm", text: dir.name })));
  }
  for (const file of data.files || []) {
    const count = TL.seq.filter((clip) => clip.path === file.path).length;
    items.push(el("button", {
      type: "button", class: `browse-file${count ? " selected" : ""}`, title: file.name,
      onclick: () => { TL.seq.push({ path: file.path, name: file.name, duration: file.duration, thumb: file.thumb }); renderSequence(); renderBrowser(); },
    },
      el("div", { class: "thumb" }, el("img", { src: file.thumb, alt: "", loading: "lazy" })),
      el("div", { class: "nm", text: file.name }),
      el("div", { class: "meta", text: [file.duration ? `${file.duration.toFixed(2)}s` : "", file.width ? `${file.width}×${file.height}` : ""].filter(Boolean).join(" · ") }),
      count ? el("div", { class: "pickcount", text: String(count) }) : null));
  }
  if (!(data.dirs || []).length && !(data.files || []).length) {
    items.push(el("div", { class: "browser-empty", text: "No videos in this directory." }));
  }
  $("timelineBrowserList").replaceChildren(...items);
}

function renderSequence() {
  const total = TL.seq.reduce((sum, clip) => sum + (clip.duration || 0), 0);
  $("timelineQueueTotal").textContent = TL.seq.length ? `${TL.seq.length} · ${total.toFixed(1)}s` : "";
  $("timelinePlaceholder").hidden = TL.seq.length > 0;
  const track = $("timelineTrack");
  track.replaceChildren(...TL.seq.map((clip, index) => {
    const item = el("div", { class: "timeline-item", draggable: true },
      el("div", { class: "drag-handle", text: "⠿", "aria-hidden": "true" }),
      el("div", { class: "seq", text: String(index + 1) }),
      el("img", { src: clip.thumb, alt: "" }),
      el("div", { class: "info" },
        el("div", { class: "nm", text: clip.name, title: clip.path }),
        el("div", { class: "meta", text: clip.duration ? `${clip.duration.toFixed(2)}s` : "" })),
      el("button", { class: "remove", type: "button", text: "✕", title: "Remove",
        onclick: () => { TL.seq.splice(index, 1); renderSequence(); renderBrowser(); } }));
    item.addEventListener("dragstart", (e) => { TL.dragFrom = index; item.classList.add("dragging"); e.dataTransfer.effectAllowed = "move"; e.dataTransfer.setData("text/plain", String(index)); });
    item.addEventListener("dragend", () => { item.classList.remove("dragging"); TL.dragFrom = null; track.querySelectorAll(".drag-over").forEach((n) => n.classList.remove("drag-over")); });
    item.addEventListener("dragover", (e) => { if (TL.dragFrom === null) return; e.preventDefault(); item.classList.add("drag-over"); });
    item.addEventListener("dragleave", () => item.classList.remove("drag-over"));
    item.addEventListener("drop", (e) => {
      e.preventDefault();
      item.classList.remove("drag-over");
      if (TL.dragFrom === null || TL.dragFrom === index) return;
      const [moved] = TL.seq.splice(TL.dragFrom, 1);
      TL.seq.splice(index, 0, moved);
      renderSequence();
    });
    return item;
  }), el("div", { class: "timeline-add-slot", text: TL.seq.length ? "+ pick another clip on the left" : "+ pick a clip on the left to start" }));
  $("renderTimeline").disabled = TL.seq.length === 0;
}

async function combineTimeline() {
  if (!TL.seq.length) return;
  $("renderTimeline").disabled = true;
  $("timelineRenderStatus").textContent = `Combining ${TL.seq.length} clip${TL.seq.length > 1 ? "s" : ""}…`;
  try {
    const data = await api("/api/timeline/render", {
      session: state.session, name: $("timelineOutputName").value.trim(), clips: TL.seq.map((clip) => clip.path),
    });
    $("timelineRenderStatus").textContent = `Saved ${data.name}`;
    state.timeline = data.timeline || [];
    renderTimelineList();
    const item = state.timeline.find((t) => t.name === data.name);
    if (item) { showReview(item); $("timelineReviewVideo").play().catch(() => {}); }
    TL.seq = [];
    renderSequence();
    renderBrowser();
  } catch (err) {
    $("timelineRenderStatus").textContent = "Failed — see the message.";
    toast(err.message, { kind: "error" });
  } finally {
    $("renderTimeline").disabled = TL.seq.length === 0;
  }
}

function bindTimeline() {
  $("timelineButton").onclick = openTimelineModal;
  $("closeTimeline").onclick = closeTimelineModal;
  $("clearTimeline").onclick = () => { TL.seq = []; renderSequence(); renderBrowser(); };
  $("renderTimeline").onclick = combineTimeline;
}
