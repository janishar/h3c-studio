/* h3 studio — timeline list and the combine-videos editor. */

"use strict";

const TL = { seq: [], browse: null, dragFrom: null };

// The sequence the viewer is playing: its clips, which one is on screen, and
// the listeners driving it. Empty when the viewer is showing anything else.
const SEQ = { item: null, clips: [], at: 0, on: null };

async function loadTimeline() {
  if (!state.session) return;
  state.timeline = await api(`/api/timeline?session=${encodeURIComponent(state.session)}`);
  renderTimelineList();
}

/** A sequence helmstudio keeps, rather than a video this session rendered. */
function isSequence(item) {
  return item.meta?.source === "helmstudio";
}

/** The clips of a sequence the viewer can play: each needs a source to play. */
function sequenceClips(item) {
  return (item.meta?.clips || []).filter((clip) => clip.url);
}

function sequenceCaption(item, index, total) {
  return `${item.name}${total > 1 ? ` · clip ${index + 1}/${total}` : ""} · in helmstudio`;
}

// A sequence is an edit helmstudio keeps, not a file on this disk, so there is
// nothing to hand the viewer but the clips it names. Clicking one plays them
// in order in the same player a take uses — each clip straight from its asset
// through this studio's /helm/ proxy, cut at the in and out points the
// sequence holds — so the edit can be watched as it stands, without waiting on
// an export. Sound is each clip's own; a sequence whose audio was cut on its
// own tracks has to be exported to be heard as it was laid out.
function playSequence(item) {
  const clips = sequenceClips(item);
  if (!clips.length) return;
  const player = $("player");
  // This clears whatever the viewer held, including another sequence, so SEQ
  // is only filled in afterwards.
  showVideo(clips[0].url, sequenceCaption(item, 0, clips.length));
  SEQ.item = item;
  SEQ.clips = clips;
  SEQ.at = 0;
  SEQ.on = {
    loadedmetadata: () => seekIntoClip(player),
    timeupdate: () => {
      const clip = SEQ.clips[SEQ.at];
      // A clip ends at its out point, not at the end of the asset it was cut
      // from; ended covers the clip that runs to the end.
      if (clip.out != null && player.currentTime >= clip.out) nextClip();
    },
    ended: () => nextClip(),
  };
  for (const [event, handler] of Object.entries(SEQ.on)) player.addEventListener(event, handler);
  // Watching a sequence is a choice, as picking a take is: a render running
  // behind it keeps its previews out of the viewer.
  if (state.runningId) state.followPreview = false;
  state.selected = null;
  state.selectedTimeline = null;
  state.selectedSequence = item.meta?.timeline_id || item.name;
  renderTakes();
  renderTimelineList();
}

// Start the clip on screen where the sequence cuts into it. It runs on every
// loadedmetadata, since a source has no seekable range before then.
function seekIntoClip(player) {
  const into = SEQ.clips[SEQ.at]?.in;
  if (into && player.currentTime < into) player.currentTime = into;
}

/** Cut to the next clip, or stop on the last one as a take does when it ends. */
function nextClip() {
  const player = $("player");
  if (SEQ.at + 1 >= SEQ.clips.length) {
    player.pause();
    return;
  }
  SEQ.at += 1;
  player.src = SEQ.clips[SEQ.at].url;
  player.load();
  setCaption(sequenceCaption(SEQ.item, SEQ.at, SEQ.clips.length));
  player.play().catch(() => {});
}

// Stop playing a sequence. showVideo calls this before it shows anything
// else, which is every other way the viewer changes — so a take, a preview or
// an emptied viewer all end the sequence rather than fighting it for the
// player.
function stopSequence() {
  if (!SEQ.clips.length) return;
  const player = $("player");
  for (const [event, handler] of Object.entries(SEQ.on || {})) player.removeEventListener(event, handler);
  SEQ.item = null;
  SEQ.clips = [];
  SEQ.at = 0;
  SEQ.on = null;
  state.selectedSequence = null;
}

function timelineMeta(item) {
  const bits = [];
  const clips = item.meta?.clips?.length;
  if (clips) bits.push(`${clips} clip${clips === 1 ? "" : "s"}`);
  if (item.probe?.width) bits.push(`${item.probe.width}×${item.probe.height}`);
  const seconds = item.probe?.duration ?? (isSequence(item) ? item.meta?.duration_s : null);
  if (seconds) bits.push(`${seconds.toFixed(1)}s`);
  if (isSequence(item)) bits.push("in helmstudio");
  return bits.join(" · ") || (isSequence(item) ? "in helmstudio" : "combined");
}

function renderTimelineList() {
  $("timelineCount").textContent = state.timeline.length || "";
  if (!state.timeline.length) {
    $("timelineList").replaceChildren(el("li", { class: "list-empty", text: "No combined videos yet. Use Create Timeline above to build one." }));
    return;
  }
  $("timelineList").replaceChildren(...state.timeline.map((item) => {
    // A sequence has no file here: it is an edit helmstudio keeps, and
    // exporting one is its own job. So clicking one plays its clips rather
    // than a file, it is not selectable into the combine editor, and it is
    // offered none of the three actions below, every one of which reads or
    // deletes a file in this session.
    const sequence = isSequence(item);
    const playable = sequence && sequenceClips(item).length > 0;
    const on = sequence
      ? state.selectedSequence && state.selectedSequence === (item.meta?.timeline_id || item.name)
      : item.name === state.selectedTimeline;
    return el("li", {
      class: on ? "on" : "",
      onclick: sequence ? (playable ? () => playSequence(item) : null) : () => selectTimeline(item.name),
    },
      el("div", { class: "row" },
        el("div", { class: "thumb" }, item.thumb ? el("img", { src: item.thumb, alt: "", loading: "lazy" }) : null),
        el("div", { class: "info" },
          el("div", { class: "nm", text: item.name, title: item.name }),
          el("div", { class: "meta", text: timelineMeta(item) }))),
      sequence
        ? null
        : el("div", { class: "ops" },
          takeButton("Use video", "Copy into inputs and add as a video reference", () => useVideoRef(item, "timeline")),
          takeButton("Last frame", "Extract the last frame into inputs", () => useFrame(item, "last", "timeline")),
          takeButton("Delete", "Delete this combined video", () => deleteMedia(item.name, "timeline"))));
  }));
}

function selectTimeline(name) {
  // By name and by kind: a sequence may carry the same name as a combined
  // video, and it is the file this plays.
  const item = state.timeline.find((t) => !isSequence(t) && t.name === name);
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
  // The review pane plays a file, so it opens on the newest combined video —
  // a sequence has no file of its own to give it.
  const combined = state.timeline.find((item) => !isSequence(item));
  if (combined) showReview(combined);
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
