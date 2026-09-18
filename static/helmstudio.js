/* ── helmstudio: its runtime, its components ──────────────────────────────
 *
 * h3 studio draws its own takes rail, its own viewer and its own timeline,
 * and keeps drawing them. This is the one thing it cannot draw: the gallery
 * every studio shares, which is helmstudio's and holds what h3 studio
 * recorded beside what the others did.
 *
 * Everything here comes through the /helm/ proxy the server mounts, so the
 * page holds no token and nothing of helmstudio's is copied into this
 * repository. Standalone there is no /helm/: the imports fail, the Gallery
 * button stays hidden, and the rest of the page is untouched.
 *
 * The component is given no URL and builds none. It reads a client, which is
 * window.helm here — helm-ui's own rule, and why one page sets it once.
 */

const HELM_SDK = "/helm/sdk/v1";

/**
 * Point the Render log tab at the render that is running.
 *
 * app.js calls this on every queue update. Standalone it is this no-op and
 * stays one: there is no job log to follow, so the tab never appears and the
 * page behaves as it always did. connectHelmstudio replaces it.
 */
let showHelmRenderLog = () => {};

/** A dialog wearing helm-css, with the studio's own chrome around a component. */
function helmDialog(className, head, body) {
  const d = el("dialog", { class: `helmstudio-dialog ${className}` }, head, body);
  document.body.append(d);
  return d;
}

/**
 * Put a component inside a container that is already in the document, and
 * return it.
 *
 * Not el(): a custom element that document.createElement made does not
 * always upgrade — <helm-timeline> and <helm-player> come back here as
 * HTMLUnknownElement and never render, while <helm-gallery> and
 * <helm-terminal> happen to be fine. The parser upgrades all four, so the
 * element is written as markup and the container is in the document when it
 * is, which is when a custom element is upgraded.
 *
 * The markup is a fixed tag with fixed attributes and carries no data, so
 * this is not the innerHTML CONTRIBUTING warns about.
 */
function mountComponent(container, markup) {
  container.insertAdjacentHTML("beforeend", markup);
  return container.lastElementChild;
}

/**
 * The gallery: h3 studio's own takes, as helmstudio recorded them.
 *
 * One <helm-gallery> for the page, not one per opening: each holds an event
 * stream open so a take that lands appears without a reload, and a browser
 * gives a page only so many connections to one host.
 */
function galleryDialog() {
  const title = el("h2", { class: "helmstudio-title", text: "Gallery" });
  const status = el("span", { class: "helmstudio-status", role: "status" });
  const close = el("button", { class: "pathbutton", type: "button", text: "Close" });
  const head = el("div", { class: "helmstudio-head" },
    title, status, el("span", { class: "helmstudio-spacer" }), close);
  const d = helmDialog("helmstudio-gallery", head);
  const gallery = mountComponent(d, '<helm-gallery scope="self" kind="video"></helm-gallery>');

  let choose = null;
  const finish = (item) => {
    if (!choose) return;
    const resolve = choose;
    choose = null;
    gallery.removeAttribute("picker");
    title.textContent = "Gallery";
    status.textContent = "";
    resolve(item);
  };

  close.addEventListener("click", () => d.close());
  // The component says what happened and decides nothing: browsing emits
  // select, picking emits pick. What either means is this studio's business.
  gallery.addEventListener("select", (event) => {
    const item = event.detail && event.detail.item;
    if (!choose) status.textContent = item ? `${item.id} selected` : "";
  });
  gallery.addEventListener("pick", (event) => {
    finish(event.detail && event.detail.item);
    d.close();
  });
  // Closing a picker answers nothing rather than hanging the caller.
  d.addEventListener("close", () => finish(null));

  return {
    browse() {
      d.showModal();
    },
    /** Open as a picker; resolves to the chosen item, or null if dismissed. */
    pick(purpose) {
      finish(null);
      title.textContent = purpose;
      status.textContent = "Pick a take, then Use this.";
      gallery.setAttribute("picker", "");
      d.showModal();
      return new Promise((resolve) => {
        choose = resolve;
      });
    },
  };
}

/** The frame rates a sequence can have; a new one takes the nearest to its first take's. */
const SEQUENCE_RATES = [23.976, 24, 25, 29.97, 30, 48, 50, 59.94, 60];

/**
 * The timeline: helmstudio's sequence document, edited in helm-timeline.
 *
 * h3 studio's own Create Timeline concatenates files and is what a studio
 * running on its own still gets. This is the other thing — a sequence
 * helmstudio keeps, that can hold clips from any studio, trimmed and
 * dissolved and exported as a job it can cancel — so under helmstudio the
 * button opens this instead.
 */
function timelineDialog(picker) {
  const title = el("h2", { class: "helmstudio-title", text: "Timeline" });
  const which = el("select", { class: "helmstudio-select", hidden: true });
  const status = el("span", { class: "helmstudio-status", role: "status" });
  const fresh = el("button", { class: "pathbutton", type: "button", text: "New sequence" });
  const close = el("button", { class: "pathbutton", type: "button", text: "Close" });
  const empty = el("p", { class: "helmstudio-empty", text: "No sequence yet. New sequence starts one from a take." });
  const head = el("div", { class: "helmstudio-head" },
    title, which, status, el("span", { class: "helmstudio-spacer" }), fresh, close);
  const body = el("div", { class: "helmstudio-body" }, empty);
  const d = helmDialog("helmstudio-timeline", head, body);
  const tl = mountComponent(body, "<helm-timeline editable hidden></helm-timeline>");
  const say = (text) => { status.textContent = text; };

  // A clip is a bare asset id to the component; this is the only place that
  // knows a take made it, so this is the place that labels it.
  const labels = new Map();
  async function learnLabels() {
    let cursor = null;
    do {
      const page = await window.helm.gallery.query({ scope: "self", kind: "video", limit: 200, cursor });
      for (const item of page.items || []) {
        const seed = item.params && item.params.seed != null ? ` · seed ${item.params.seed}` : "";
        labels.set(item.asset_id, `${item.title || "take"}${seed}`);
      }
      cursor = page.next_cursor || null;
    } while (cursor);
  }
  tl.labelFor = (clip) => labels.get(clip.asset_id) || "";

  async function load(selectId) {
    const page = await window.helm.timeline.list({ limit: 50 });
    const sequences = page.items || [];
    empty.hidden = sequences.length > 0;
    tl.hidden = sequences.length === 0;
    which.hidden = sequences.length === 0;
    which.replaceChildren(...sequences.map((s) => new Option(`${s.name} · r${s.revision}`, s.id)));
    const chosen = sequences.find((s) => s.id === selectId) || sequences[0];
    if (!chosen) return;
    which.value = chosen.id;
    await learnLabels().catch(() => {});
    if (tl.getAttribute("timeline") !== chosen.id) tl.setAttribute("timeline", chosen.id);
  }

  which.addEventListener("change", () => tl.setAttribute("timeline", which.value));
  tl.addEventListener("changed", (event) => {
    const t = event.detail.timeline;
    const option = [...which.options].find((o) => o.value === t.id);
    if (option) option.textContent = `${t.name} · r${t.revision}`;
  });
  tl.addEventListener("exported", () => say("Exported — the sequence is in the gallery."));

  // What goes on a sequence is this page's to choose: the editor asks, and
  // the gallery answers.
  tl.addEventListener("add-request", async () => {
    const item = await picker.pick("Add a take to the sequence");
    if (item) await tl.append(item.asset_id);
  });

  fresh.addEventListener("click", async () => {
    const item = await picker.pick("Start a sequence from a take");
    if (!item) return;
    const asset = item.asset || {};
    if (!asset.width || !asset.height) {
      say("That take has no dimensions to build a sequence at.");
      return;
    }
    const want = asset.fps || 24;
    const fps = SEQUENCE_RATES.reduce((best, rate) => (Math.abs(rate - want) < Math.abs(best - want) ? rate : best));
    try {
      const made = await window.helm.timeline.create({
        name: `h3 sequence ${new Date().toLocaleString()}`,
        target: { width: asset.width, height: asset.height, fps },
        clips: [{ asset_id: item.asset_id }],
      });
      say("");
      await load(made.id);
    } catch (err) {
      say(`The sequence could not be made: ${err.message}`);
    }
  });
  close.addEventListener("click", () => d.close());

  return {
    async open() {
      d.showModal();
      say("");
      try {
        await load(which.value);
      } catch (err) {
        empty.hidden = false;
        empty.textContent = `The sequences could not be read: ${err.message}`;
      }
    },
  };
}

/**
 * Connect to helmstudio, if this studio is running under it.
 *
 * Failure is the standalone case and not an error: `helm dev` and the app
 * serve /helm/, a bare `h3studio` does not, and the page must be the same
 * page either way. So this reports whether it connected and says nothing.
 */
async function connectHelmstudio() {
  let connect;
  try {
    ({ connect } = await import(`${HELM_SDK}/helm-runtime.js`));
    await import(`${HELM_SDK}/helm-ui.js`);
  } catch {
    return false; // standalone: no proxy, no components, no Gallery button
  }
  // helm-ui components read window.helm when they are given no client of
  // their own, which is how one page serves every component it mounts.
  window.helm = connect();

  const gallery = galleryDialog();
  const button = $("galleryButton");
  if (button) {
    button.hidden = false;
    button.addEventListener("click", () => gallery.browse());
  }

  mountRenderLog();

  // Create Timeline opens helmstudio's sequence rather than h3 studio's own
  // combine-videos editor, which stays exactly as it is for a studio running
  // on its own. timeline.js sets this handler; under helmstudio there is a
  // better one, so it is taken over rather than added beside.
  const timeline = timelineDialog(gallery);
  const timelineButton = $("timelineButton");
  if (timelineButton) timelineButton.onclick = () => timeline.open();
  return true;
}

/**
 * The render log: helm-terminal streaming the job helmstudio keeps.
 *
 * It sits in its own tab beside Output rather than replacing it. h3 studio's
 * terminal is one pane for three things — the render's output, shell commands
 * and interactive h3 — and helm-terminal streams exactly one job, so
 * replacing it would trade two of those away. What it adds is what h3
 * studio's cannot do: rows drawn only where they are visible, ANSI colour,
 * and reconnection from the last line it saw, so a dropped stream resumes and
 * says what it missed instead of losing it in silence.
 */
function mountRenderLog() {
  const pane = $("renderLogPane");
  const tab = $("renderLogTab");
  if (!pane || !tab) return;

  const term = mountComponent(pane, "<helm-terminal follow></helm-terminal>");
  term.client = window.helm;
  tab.hidden = false;

  showHelmRenderLog = (job) => {
    const id = job && job.helm_job;
    // No job means nothing to stream: an older render from before this ran,
    // or a studio started without helmstudio. Clearing is what tells the
    // component to stop rather than keep showing a finished render's log.
    if (!id) {
      term.removeAttribute("job");
      return;
    }
    if (term.getAttribute("job") !== id) term.setAttribute("job", id);
  };
}
