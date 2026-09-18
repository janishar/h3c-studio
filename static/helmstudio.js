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

/** A dialog wearing helm-css, with the studio's own chrome around a component. */
function helmDialog(className, head, body) {
  const d = el("dialog", { class: `helmstudio-dialog ${className}` }, head, body);
  document.body.append(d);
  return d;
}

/**
 * The gallery: h3 studio's own takes, as helmstudio recorded them.
 *
 * One <helm-gallery> for the page, not one per opening: each holds an event
 * stream open so a take that lands appears without a reload, and a browser
 * gives a page only so many connections to one host.
 */
function galleryDialog() {
  const gallery = el("helm-gallery", { scope: "self", kind: "video" });
  const title = el("h2", { class: "helmstudio-title", text: "Gallery" });
  const status = el("span", { class: "helmstudio-status", role: "status" });
  const close = el("button", { class: "pathbutton", type: "button", text: "Close" });
  const head = el("div", { class: "helmstudio-head" },
    title, status, el("span", { class: "helmstudio-spacer" }), close);
  const d = helmDialog("helmstudio-gallery", head, gallery);

  close.addEventListener("click", () => d.close());
  // What a selected item is for is the studio's business, not the
  // component's: it says one was picked and h3 studio decides. Browsing is
  // all this does today; the timeline's picker is the next thing to use it.
  gallery.addEventListener("select", (event) => {
    const item = event.detail && event.detail.item;
    status.textContent = item ? `${item.id} selected` : "";
  });

  return { browse: () => d.showModal() };
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
  return true;
}
