/* h3 studio — prompt editor with @-mentions of references and inputs. */

"use strict";

function slotLabel(refs, index) {
  const ref = refs[index];
  const n = refs.slice(0, index + 1).filter((item) => item.kind === ref.kind).length;
  return `${ref.kind === "image" ? "Picture" : ref.kind === "video" ? "Video" : "Audio"} ${n}`;
}

/** Prompt text h3 receives: ref chips become <Picture N>, library chips stay @name. */
function resolvePrompt(doc, refs) {
  return doc.map((node) => {
    if (node.type === "text") return node.value;
    if (node.type === "lib") return `@${node.name}`;
    const i = refs.findIndex((ref) => ref.id === node.refId);
    if (i === -1) throw new Error("A mentioned reference is no longer attached — remove its ⚠ chip from the prompt.");
    return `<${slotLabel(refs, i)}>`;
  }).join("");
}

/** Plain-text view of a prompt document (for history and copying). */
function promptText(doc = state.promptDoc) {
  return doc.map((node) => {
    if (node.type === "text") return node.value;
    if (node.type === "lib") return `@${node.name}`;
    const ref = state.refs.find((item) => item.id === node.refId);
    return ref ? `@${ref.name}` : "";
  }).join("");
}

function mentionChip(node) {
  if (node.type === "lib") {
    return el("span", { class: "mention-chip", contentEditable: "false", dataset: { libName: node.name }, text: `@${node.name}` });
  }
  const i = state.refs.findIndex((item) => item.id === node.refId);
  const ref = state.refs[i];
  return el("span", {
    class: `mention-chip${ref ? "" : " invalid"}`, contentEditable: "false", dataset: { refId: node.refId },
    text: ref ? `@${ref.name} · ${slotLabel(state.refs, i)}` : "⚠ removed",
  });
}

function renderPromptEditor() {
  const editor = $("prompt");
  editor.replaceChildren(...state.promptDoc.map((node) =>
    node.type === "text" ? document.createTextNode(node.value) : mentionChip(node)));
}

function readPromptEditor() {
  const doc = [];
  $("prompt").childNodes.forEach((node) => {
    if (node.nodeType === Node.TEXT_NODE) {
      if (node.nodeValue) doc.push({ type: "text", value: node.nodeValue });
    } else if (node.nodeType === Node.ELEMENT_NODE && node.dataset.refId) {
      doc.push({ type: "ref", refId: node.dataset.refId });
    } else if (node.nodeType === Node.ELEMENT_NODE && node.dataset.libName) {
      doc.push({ type: "lib", name: node.dataset.libName });
    } else if (node.textContent) {
      doc.push({ type: "text", value: node.textContent });
    }
  });
  state.promptDoc = doc.length ? doc : [{ type: "text", value: "" }];
}

function promptCandidates(query) {
  const attached = new Set(state.refs.map((ref) => ref.name));
  const inRefMode = state.mode === "ref";
  return [
    ...(inRefMode ? state.refs.map((ref) => ({ ...ref, attached: true })) : []),
    ...state.inputs.filter((file) => !inRefMode || !attached.has(file.name)).map((file) => ({ ...file, attached: false })),
  ].filter((ref) => ref.name.toLowerCase().includes(query.toLowerCase()));
}

function closeMentionMenu() {
  $("mentionMenu").hidden = true;
  state.mention = null;
}

function placeCaretAfter(node) {
  const space = document.createTextNode(" ");
  node.parentNode.insertBefore(space, node.nextSibling);
  const caret = document.createRange();
  caret.setStart(space, 1);
  caret.collapse(true);
  const selection = getSelection();
  selection.removeAllRanges();
  selection.addRange(caret);
  $("prompt").focus();
}

function insertMention(ref) {
  const mention = state.mention;
  const textNode = mention?.node;
  if (!textNode || !$("prompt").contains(textNode)) return;
  const range = document.createRange();
  range.setStart(textNode, mention.start);
  range.setEnd(textNode, mention.end);
  range.deleteContents();

  // Only Reference mode has slots to attach to; elsewhere a mention is a name chip.
  let chip;
  if (state.mode !== "ref") {
    chip = mentionChip({ type: "lib", name: ref.name });
  } else {
    if (!ref.attached) addRef(ref);
    const attached = state.refs.find((item) => item.name === ref.name);
    if (!attached) { closeMentionMenu(); return; }
    chip = mentionChip({ type: "ref", refId: attached.id });
  }
  range.insertNode(chip);
  placeCaretAfter(chip);
  readPromptEditor();
  closeMentionMenu();
  sync();
}

function insertRefAtCaret(ref) {
  const selection = getSelection();
  const range = selection?.rangeCount ? selection.getRangeAt(0) : null;
  if (!range || !$("prompt").contains(range.commonAncestorContainer)) {
    $("prompt").focus();
    return;
  }
  const chip = mentionChip({ type: "ref", refId: ref.id });
  range.deleteContents();
  range.insertNode(chip);
  placeCaretAfter(chip);
  readPromptEditor();
  sync();
}

function insertPromptText(text, savedRange = null) {
  if (!text) return;
  const selection = getSelection();
  const range = savedRange || (selection?.rangeCount ? selection.getRangeAt(0) : null);
  if (!range || !$("prompt").contains(range.commonAncestorContainer)) return;
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

function onPromptInput() {
  readPromptEditor();
  const selection = getSelection();
  const node = selection?.anchorNode;
  if (!node || node.nodeType !== Node.TEXT_NODE || !$("prompt").contains(node)) {
    closeMentionMenu(); sync(); return;
  }
  const before = node.nodeValue.slice(0, selection.anchorOffset);
  const match = before.match(/(^|\s)@([^\s@]*)$/);
  if (!match) { closeMentionMenu(); sync(); return; }
  const options = promptCandidates(match[2]);
  state.mention = {
    node,
    start: before.length - match[0].length + match[1].length,
    end: before.length,
    options,
    selected: 0,
  };
  const rect = selection.getRangeAt(0).getBoundingClientRect();
  const menu = $("mentionMenu");
  menu.style.left = `${rect.left}px`;
  menu.style.top = `${rect.bottom + 4}px`;
  const items = [];
  let lastGroup = null;
  options.forEach((ref, i) => {
    const group = state.mode === "ref" && ref.attached ? "Attached" : "Input library";
    if (group !== lastGroup) {
      items.push(el("div", { class: "mention-group", text: group }));
      lastGroup = group;
    }
    const preview = el("span", { class: `mention-preview ${ref.kind}` },
      ref.kind === "image" ? el("img", { src: mediaURL(`inputs/${ref.name}`), alt: "" })
        : ref.kind === "video" ? "▶" : "♪");
    const hint = state.mode !== "ref" ? "mention in prompt" : ref.attached ? "attached" : "attach from library";
    items.push(el("button", {
      type: "button", class: `mention-option${i ? "" : " on"}`, dataset: { optionIndex: i },
      onmousedown: (event) => { event.preventDefault(); insertMention(ref); },
    }, preview, el("span", {}, ref.name, el("small", { text: hint }))));
  });
  menu.replaceChildren(...items);
  menu.hidden = !options.length;
  sync();
}

function onPromptKeydown(event) {
  const mod = event.metaKey || event.ctrlKey;
  if (mod && event.key === "Enter") return; // global shortcut handles render
  if (mod && event.key.toLowerCase() === "z") {
    event.preventDefault();
    document.execCommand(event.shiftKey ? "redo" : "undo");
    readPromptEditor();
    sync();
    return;
  }
  if (event.key === "Backspace") {
    const selection = getSelection();
    const prev = selection?.anchorNode?.previousSibling;
    if (selection?.isCollapsed && selection.anchorNode?.nodeType === Node.TEXT_NODE &&
        selection.anchorOffset === 0 && (prev?.dataset?.refId || prev?.dataset?.libName)) {
      event.preventDefault();
      prev.remove();
      readPromptEditor(); renderPromptEditor(); sync();
      return;
    }
  }
  if (!state.mention || $("mentionMenu").hidden) return;
  const options = state.mention.options;
  if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); closeMentionMenu(); return; }
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    state.mention.selected = (state.mention.selected + (event.key === "ArrowDown" ? 1 : options.length - 1)) % options.length;
    [...$("mentionMenu").querySelectorAll(".mention-option")].forEach((node) =>
      node.classList.toggle("on", +node.dataset.optionIndex === state.mention.selected));
  } else if (event.key === "Enter" || event.key === "Tab") {
    event.preventDefault();
    insertMention(options[state.mention.selected]);
  }
}

function onPromptPaste(event) {
  event.preventDefault();
  closeMentionMenu();
  const text = event.clipboardData?.getData("text/plain");
  if (text != null) { insertPromptText(text); return; }
  const savedRange = getSelection()?.rangeCount ? getSelection().getRangeAt(0).cloneRange() : null;
  navigator.clipboard?.readText().then((value) => insertPromptText(value, savedRange)).catch(() => {});
}

function scaffoldPrompt() {
  const tokens = {};
  state.refs.forEach((ref, i) => {
    (tokens[ref.kind] = tokens[ref.kind] || []).push(`<${slotLabel(state.refs, i)}>`);
  });
  const subject = Object.values(tokens).flat()[0] || "<subject>";
  const audio = tokens.audio?.[0] || "the ambience";
  state.promptDoc = [{ type: "text", value:
    `Scene: ${subject} stands in ...\nAction: ...\nCamera: ...\nLook: ...\nAudio: match the ambience of ${audio}` }];
  renderPromptEditor();
  $("prompt").focus();
  sync();
}

/** Prompt history from the session's takes, newest first, unique. */
function promptHistory() {
  const seen = new Set();
  const items = [];
  for (const take of state.takes) {
    const p = take.meta?.params;
    if (!p || !p.prompt) continue;
    const key = p.prompt.trim();
    if (!key || seen.has(key)) continue;
    seen.add(key);
    items.push({ text: key, doc: Array.isArray(p.prompt_doc) ? p.prompt_doc : null, take: take.name });
    if (items.length === 20) break;
  }
  return items;
}

function renderHistoryMenu() {
  const items = promptHistory();
  const menu = $("historyMenu");
  if (!items.length) {
    menu.replaceChildren(el("div", { class: "popmenu-empty", text: "No prompts in this session's takes yet." }));
    return;
  }
  menu.replaceChildren(...items.map((item) => el("button", {
    type: "button", role: "menuitem", title: item.text,
    onclick: () => {
      // Keep chips only when every mentioned reference is still attached.
      const usable = item.doc && item.doc.every((node) => node.type !== "ref" || state.refs.some((ref) => ref.id === node.refId));
      state.promptDoc = usable ? item.doc : [{ type: "text", value: item.text }];
      renderPromptEditor();
      menu.hidden = true;
      sync();
    },
  }, el("span", { class: "history-text", text: item.text }), el("small", { text: item.take }))));
}
