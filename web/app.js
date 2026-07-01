/* ============================================================
   Pine — embedded web UI (vanilla ES2020+, no build step)
   Sections:
     1. helpers (dom, time, icons, toasts, api, modals)
     2. global state + repo selector
     3. router
     4. pages: dashboard, repos, playbooks, playbook detail,
        roles, role detail, inventory, topology, hygiene, impact,
        jobs, job detail, plan
     5. run-playbook modal, keyboard shortcuts, boot
   ============================================================ */
"use strict";

/* ============================================================
   1. Helpers
   ============================================================ */

const $ = (sel, root = document) => root.querySelector(sel);
const $$ = (sel, root = document) => [...root.querySelectorAll(sel)];

/** Create a DOM element. el('div', {class:'x', onclick:fn}, child1, 'text', [more]) */
function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === "class") node.className = v;
      else if (k === "html") node.innerHTML = v; // trusted fragments only
      else if (k.startsWith("on") && typeof v === "function") node.addEventListener(k.slice(2), v);
      else if (k === "style" && typeof v === "object") Object.assign(node.style, v);
      else if (v === true) node.setAttribute(k, "");
      else node.setAttribute(k, String(v));
    }
  }
  appendChildren(node, children);
  return node;
}

function appendChildren(node, children) {
  for (const c of children) {
    if (c === null || c === undefined || c === false) continue;
    if (Array.isArray(c)) appendChildren(node, c);
    else if (c instanceof Node) node.appendChild(c);
    else node.appendChild(document.createTextNode(String(c)));
  }
}

function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (m) =>
    ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[m]));
}

/* ---- time formatting ---- */

function relTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const diff = Date.now() - t;
  if (diff < 0) return "just now";
  const s = Math.floor(diff / 1000);
  if (s < 5) return "just now";
  if (s < 60) return `${s}s ago`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 30) return `${d}d ago`;
  return new Date(iso).toLocaleDateString();
}

/** Forward-looking counterpart of relTime: "in 5m" for a future timestamp. */
function untilTime(iso) {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const diff = t - Date.now();
  if (diff <= 0) return "due now";
  const s = Math.ceil(diff / 1000);
  if (s < 60) return `in ${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `in ${m}m`;
  const h = Math.round(m / 60);
  if (h < 24) return `in ${h}h`;
  return `in ${Math.round(h / 24)}d`;
}

function fmtDuration(ms) {
  if (ms === null || ms === undefined || ms <= 0) return "—";
  if (ms < 1000) return `${ms}ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)}s`;
  const m = Math.floor(s / 60);
  const rs = Math.round(s % 60);
  if (m < 60) return `${m}m ${String(rs).padStart(2, "0")}s`;
  const h = Math.floor(m / 60);
  return `${h}h ${m % 60}m`;
}

/* ---- inline icons (trusted SVG strings) ---- */

const ICONS = {
  play: '<svg viewBox="0 0 24 24" fill="currentColor"><path d="M7 4.5l12 7.5-12 7.5z"/></svg>',
  sync: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M21 12a9 9 0 1 1-2.6-6.3"/><path d="M21 3v6h-6"/></svg>',
  trash: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 6h18M8 6V4a1 1 0 0 1 1-1h6a1 1 0 0 1 1 1v2m3 0v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6"/></svg>',
  folder: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>',
  loop: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M17 2l4 4-4 4"/><path d="M3 11V9a4 4 0 0 1 4-4h14M7 22l-4-4 4-4"/><path d="M21 13v2a4 4 0 0 1-4 4H3"/></svg>',
  branch: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><line x1="6" y1="3" x2="6" y2="15"/><circle cx="18" cy="6" r="3"/><circle cx="6" cy="18" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>',
  question: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><circle cx="12" cy="12" r="9"/><path d="M9.1 9a3 3 0 0 1 5.8 1c0 2-3 2.5-3 4"/><circle cx="12" cy="17.2" r="0.5" fill="currentColor"/></svg>',
  bell: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 8a6 6 0 1 0-12 0c0 7-3 9-3 9h18s-3-2-3-9"/><path d="M13.7 21a2 2 0 0 1-3.4 0"/></svg>',
  group: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linejoin="round"><path d="M3 7a2 2 0 0 1 2-2h4l2 3h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/></svg>',
  host: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4" stroke-linecap="round"/></svg>',
  globe: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/></svg>',
  download: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4M7 10l5 5 5-5M12 15V3"/></svg>',
  stop: '<svg viewBox="0 0 24 24" fill="currentColor"><rect x="6" y="6" width="12" height="12" rx="2"/></svg>',
  plus: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>',
  minus: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round"><path d="M5 12h14"/></svg>',
  search: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></svg>',
  role: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/></svg>',
  tree: '<svg viewBox="0 0 24 24"><path d="M12 1 L16.5 8.5 H7.5 Z" fill="#4ade80"/><path d="M12 5.5 L18 14.5 H6 Z" fill="#34c46a"/><path d="M12 10.5 L20 20.5 H4 Z" fill="#22a356"/><rect x="10.9" y="20" width="2.2" height="3.2" rx="0.6" fill="#8a5a3b"/></svg>',
  code: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M8 6l-6 6 6 6M16 6l6 6-6 6M13 4l-2 16"/></svg>',
  clipboard: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="8" y="2" width="8" height="4" rx="1"/><path d="M16 4h2a2 2 0 0 1 2 2v14a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V6a2 2 0 0 1 2-2h2"/><path d="M9 13l2 2 4-4"/></svg>',
  sparkle: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l1.9 5.1L19 10l-5.1 1.9L12 17l-1.9-5.1L5 10l5.1-1.9z"/><path d="M19 14.5l.9 2.1 2.1.9-2.1.9-.9 2.1-.9-2.1-2.1-.9 2.1-.9z"/></svg>',
  radar: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><circle cx="12" cy="12" r="1.6" fill="currentColor" stroke="none"/><path d="M15.5 8.5a5 5 0 0 1 0 7M8.5 15.5a5 5 0 0 1 0-7"/><path d="M18.4 5.6a9 9 0 0 1 0 12.8M5.6 18.4a9 9 0 0 1 0-12.8"/></svg>',
  diff: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M16 3l4 4-4 4M20 7H7M8 13l-4 4 4 4M4 17h13"/></svg>',
  drift: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="2 12 6 12 9 4.5 14.5 19.5 17.5 12 22 12"/></svg>',
  schedule: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21.3 14.6A9 9 0 1 1 20 7.7"/><path d="M21 3v5h-5"/><path d="M12 7.5V12l3 1.8"/></svg>',
  pipeline: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="2" y="9" width="6" height="6" rx="1.5"/><rect x="16" y="3" width="6" height="6" rx="1.5"/><rect x="16" y="15" width="6" height="6" rx="1.5"/><path d="M8 12h3M11 6v12M11 6h5M11 18h5"/></svg>',
  shield: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-3.6 8-10V5.5L12 2 4 5.5V12c0 6.4 8 10 8 10z"/><path d="M9 11.5l2 2 4-4"/></svg>',
  check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6L9 17l-5-5"/></svg>',
  timelapse: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M3 12a9 9 0 1 0 9-9"/><path d="M3 4v5h5"/><path d="M12 8v4l2.5 1.5"/></svg>',
};

function icon(name) {
  const span = el("span", { class: "icon-wrap", style: { display: "inline-flex" } });
  span.innerHTML = ICONS[name] || "";
  const svg = span.firstChild;
  if (svg) { svg.setAttribute("width", "14"); svg.setAttribute("height", "14"); }
  return span;
}

/* ---- toasts ---- */

function toast(msg, kind = "success", title) {
  const box = $("#toasts");
  const t = el("div", { class: `toast ${kind}` },
    el("span", { class: "toast-title" }, title || (kind === "error" ? "Error" : "Done")),
    el("span", { class: "toast-msg" }, msg));
  box.appendChild(t);
  const remove = () => { t.classList.add("leaving"); setTimeout(() => t.remove(), 220); };
  t.addEventListener("click", remove);
  setTimeout(remove, kind === "error" ? 6500 : 3500);
}

/* ---- api ---- */

async function api(path, opts = {}) {
  let res;
  try {
    res = await fetch("/api" + path, {
      headers: opts.body ? { "Content-Type": "application/json" } : undefined,
      ...opts,
    });
  } catch (e) {
    throw new Error("Network error — is the Pine server running?");
  }
  if (res.status === 204) return null;
  const text = await res.text();
  let data = null;
  if (text) { try { data = JSON.parse(text); } catch { /* non-JSON */ } }
  if (!res.ok) {
    throw new Error((data && data.error) || `Request failed (HTTP ${res.status})`);
  }
  return data;
}

/* ---- modals ---- */

let activeModal = null;

function openModal({ title, body, footer, width }) {
  closeModal();
  const modal = el("div", { class: "modal", role: "dialog", "aria-modal": "true" });
  if (width) modal.style.maxWidth = width;
  modal.appendChild(el("div", { class: "modal-head" },
    el("h2", null, title),
    el("button", { class: "x", "aria-label": "Close", onclick: closeModal }, "✕")));
  const bodyEl = el("div", { class: "modal-body" });
  appendChildren(bodyEl, [body]);
  modal.appendChild(bodyEl);
  if (footer) {
    const footEl = el("div", { class: "modal-foot" });
    appendChildren(footEl, Array.isArray(footer) ? footer : [footer]);
    modal.appendChild(footEl);
  }
  const overlay = el("div", { class: "modal-overlay", onclick: (e) => { if (e.target === overlay) closeModal(); } }, modal);
  $("#modal-root").appendChild(overlay);
  activeModal = overlay;
  const firstInput = modal.querySelector("input, select, button.btn-primary");
  if (firstInput) firstInput.focus();
  return { overlay, modal, body: bodyEl };
}

function closeModal() {
  if (activeModal) { activeModal.remove(); activeModal = null; }
}

function confirmModal(title, message, confirmLabel = "Delete") {
  return new Promise((resolve) => {
    const ok = el("button", { class: "btn btn-danger", onclick: () => { closeModal(); resolve(true); } }, confirmLabel);
    const cancel = el("button", { class: "btn", onclick: () => { closeModal(); resolve(false); } }, "Cancel");
    const { overlay } = openModal({ title, body: el("p", { class: "muted", style: { margin: 0 } }, message), footer: [cancel, ok] });
    overlay.addEventListener("click", (e) => { if (e.target === overlay) resolve(false); });
  });
}

/* ---- raw file preview ---- */

/** Build the raw-file API URL for a repo-relative path. */
function rawFileURL(repoId, path) {
  return `/api/repos/${repoId}/file?path=${path.split("/").map(encodeURIComponent).join("/")}`;
}

async function fetchRawFile(repoId, path) {
  const res = await fetch(rawFileURL(repoId, path));
  if (!res.ok) throw new Error(`HTTP ${res.status}`);
  return res.text();
}

/**
 * Candidate repo-relative locations for an include/import `file`, resolved
 * against `basePath` (the dir of the playbook or role-tasks file). Ansible
 * searches the file dir plus sibling vars/ and tasks/, so we try those in the
 * dir and its parent; the preview modal opens the first that exists.
 */
function includeCandidates(basePath, file) {
  // already-rooted reference (e.g. "roles/x/vars/y.yml") — use as-is first
  if (file.includes("/") && !file.startsWith("./") && !file.startsWith("../")) {
    // still fall through to base-relative candidates below
  }
  const clean = file.replace(/^\.\//, "");
  const parts = (basePath || "").split("/").filter(Boolean);
  const dir = parts.join("/");
  const parent = parts.slice(0, -1).join("/");
  const join = (...p) => p.filter((x) => x !== "" && x != null).join("/");
  const set = new Set([
    join(dir, clean),
    join(dir, "vars", clean),
    join(dir, "tasks", clean),
    join(parent, clean),
    join(parent, "vars", clean),
    join(parent, "tasks", clean),
    clean,
  ]);
  return [...set].filter(Boolean);
}

/**
 * Candidate repo-relative locations for a `src:` file referenced by a
 * template/copy/file-style module. Ansible looks in templates/ (template) or
 * files/ (copy etc.) relative to the role or playbook, so we try those next to
 * the task's dir and its parent; the preview opens the first that exists.
 */
function srcFileCandidates(basePath, src, module) {
  const clean = src.replace(/^\.\//, "").replace(/^\{\{\s*role_path\s*\}\}\//, "");
  const parts = (basePath || "").split("/").filter(Boolean);
  const dir = parts.join("/");
  const parent = parts.slice(0, -1).join("/");
  const join = (...p) => p.filter((x) => x !== "" && x != null).join("/");
  const sub = /template/.test(module || "") ? "templates" : "files";
  const set = new Set([
    join(dir, sub, clean), join(dir, clean),
    join(parent, sub, clean), join(parent, clean),
    join(sub, clean), clean,
  ]);
  return [...set].filter(Boolean);
}

/**
 * Preview the real source file behind a parsed view. `candidates` is a path or
 * a list of paths tried in order — the first that exists is shown. This lets a
 * caller pass alternates (main.yml/main.yaml, a hosts dir's likely files).
 */
/* ---- lightweight syntax highlighting for the code preview (no deps) ---- */

// wrap {{ … }} / {% … %} jinja in an already-HTML-escaped string.
function hlJinja(escaped) {
  return escaped.replace(/(\{\{[\s\S]*?\}\}|\{%[\s\S]*?%\})/g, '<span class="tok-jinja">$1</span>');
}

// highlight a YAML scalar value (raw, unescaped); preserves surrounding spaces.
function hlYamlValue(raw) {
  if (raw === "") return "";
  const body = raw.trimStart();
  const lead = raw.slice(0, raw.length - body.length);
  const t = body.trimEnd();
  const trail = body.slice(t.length);
  let cls = null;
  if (/^(['"]).*\1$/.test(t) || /^['"]/.test(t)) cls = "tok-str";
  else if (/^(true|false|yes|no|on|off|null|~|none)$/i.test(t)) cls = "tok-bool";
  else if (/^-?\d[\d_]*(\.\d+)?$/.test(t)) cls = "tok-num";
  let inner = hlJinja(esc(t));
  if (cls) inner = `<span class="${cls}">${inner}</span>`;
  return esc(lead) + inner + esc(trail);
}

function highlightYamlLine(line) {
  const indent = line.match(/^(\s*)/)[1];
  let rest = line.slice(indent.length);
  if (rest === "") return "";
  if (rest.startsWith("#")) return esc(indent) + `<span class="tok-comment">${esc(rest)}</span>`;
  if (rest === "---" || rest === "..." || rest.startsWith("--- ")) return esc(indent) + `<span class="tok-doc">${esc(rest)}</span>`;
  let out = esc(indent);
  const dash = rest.match(/^(-\s+)/);
  if (dash) { out += `<span class="tok-punct">${esc(dash[1])}</span>`; rest = rest.slice(dash[1].length); }
  else if (rest === "-") return out + `<span class="tok-punct">-</span>`;
  // split off an inline " # comment" (naive: ignores # inside quotes)
  let comment = "";
  const ci = rest.search(/\s#/);
  if (ci >= 0) { comment = rest.slice(ci); rest = rest.slice(0, ci); }
  const kv = rest.match(/^([^\s:#][^:]*?)(:)(\s.*|$)/);
  if (kv) out += `<span class="tok-key">${esc(kv[1])}</span><span class="tok-punct">:</span>` + hlYamlValue(kv[3]);
  else out += hlYamlValue(rest);
  if (comment) out += `<span class="tok-comment">${esc(comment)}</span>`;
  return out;
}

function highlightIniLine(line) {
  const t = line.trim();
  if (t === "") return "";
  if (t.startsWith("#") || t.startsWith(";")) return `<span class="tok-comment">${esc(line)}</span>`;
  if (/^\[.*\]$/.test(t)) return `<span class="tok-key">${esc(line)}</span>`;
  // host line with key=value vars; keep the host token plain, colour the vars
  return hlJinja(esc(line)).replace(/([\w.\-]+)(=)(&quot;[^&]*&quot;|&#39;[^&]*&#39;|\S+)/g,
    (m, k, eq, v) => `<span class="tok-attr">${k}</span><span class="tok-punct">${eq}</span><span class="tok-str">${v}</span>`);
}

function detectCodeLang(path, text) {
  const p = (path || "").toLowerCase();
  if (p.endsWith(".yml") || p.endsWith(".yaml")) return "yaml";
  if (p.endsWith(".ini") || p.endsWith(".cfg")) return "ini";
  const first = text.split("\n").map((l) => l.trim()).find((l) => l && !l.startsWith("#") && !l.startsWith(";"));
  if (first && (/^\[.+\]$/.test(first) || /^\S+\s*=\s*\S/.test(first))) return "ini";
  return "yaml"; // previews are overwhelmingly YAML
}

// highlightCode returns HTML (escaped + token spans) for the preview pane.
function highlightCode(text, path) {
  const lang = detectCodeLang(path, text);
  const fn = lang === "yaml" ? highlightYamlLine : lang === "ini" ? highlightIniLine : null;
  if (!fn) return esc(text);
  return text.split("\n").map(fn).join("\n");
}

async function openRawFileModal(repoId, candidates, title) {
  const paths = Array.isArray(candidates) ? candidates : [candidates];
  const { body: bodyEl } = openModal({
    title: title || "Source",
    body: el("div", { class: "muted small" }, "Loading…"),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Close")],
    width: "880px",
  });

  let text = null, usedPath = null, lastErr = null;
  for (const p of paths) {
    try { text = await fetchRawFile(repoId, p); usedPath = p; break; }
    catch (e) { lastErr = e; }
  }

  bodyEl.innerHTML = "";
  if (text === null) {
    bodyEl.appendChild(el("div", { class: "empty" },
      el("h3", null, "File not available"),
      el("p", null, (lastErr && lastErr.message) || "Could not read the source file.")));
    return;
  }

  const copyBtn = el("button", { class: "btn btn-sm" }, "Copy");
  copyBtn.onclick = async () => {
    try { await navigator.clipboard.writeText(text); toast("Copied to clipboard"); }
    catch { toast("Copy failed", "error"); }
  };
  bodyEl.appendChild(el("div", { class: "code-head" },
    el("span", { class: "mono small muted" }, usedPath),
    el("div", { class: "grow" }),
    copyBtn,
    el("a", { class: "btn btn-sm", href: rawFileURL(repoId, usedPath), target: "_blank", rel: "noopener" }, icon("download"), "Open")));
  bodyEl.appendChild(el("pre", { class: "code-view" }, el("code", { html: highlightCode(text, usedPath) })));
}

/* ---- skeletons ---- */

function skeletonRows(n = 3, height = 72) {
  return el("div", { class: "grid", style: { gap: "12px" } },
    Array.from({ length: n }, () => el("div", { class: "skeleton", style: { height: height + "px" } })));
}

/* ---- status pills ---- */

function statusPill(status) {
  const s = status || "unknown";
  return el("span", { class: `pill st-${s}` }, s);
}

function repoStatusBadge(repo) {
  if (repo.status === "syncing") {
    return el("span", { class: "pill st-syncing" }, el("span", { class: "spinner", style: { marginRight: "2px" } }), "syncing");
  }
  return statusPill(repo.status);
}

/* ============================================================
   2. Global state + repo selector
   ============================================================ */

const State = {
  repos: [],
  reposLoaded: false,
  repoId: localStorage.getItem("pine.repo") || "",
  scanCache: new Map(),   // repoId -> Promise<scan>
  lineageCache: new Map(),// "repoId␁inventory␁host" -> Promise<lineage>
  cleanups: [],           // run on navigation
  invSelection: new Map(),// repoId -> inventory name (inventory + topology pages)
};

function onCleanup(fn) { State.cleanups.push(fn); }
function runCleanups() {
  for (const fn of State.cleanups.splice(0)) { try { fn(); } catch { /* noop */ } }
}

function currentRepo() {
  return State.repos.find((r) => r.id === State.repoId) || null;
}

async function loadRepos(force = false) {
  if (State.reposLoaded && !force) return State.repos;
  State.repos = (await api("/repos")) || [];
  State.reposLoaded = true;
  // reconcile selection
  if (!State.repos.some((r) => r.id === State.repoId)) {
    State.repoId = State.repos.length ? State.repos[0].id : "";
    persistRepoSelection();
  }
  renderRepoSelector();
  return State.repos;
}

function persistRepoSelection() {
  if (State.repoId) localStorage.setItem("pine.repo", State.repoId);
  else localStorage.removeItem("pine.repo");
}

function renderRepoSelector() {
  const sel = $("#repo-select");
  sel.innerHTML = "";
  if (!State.repos.length) {
    sel.appendChild(el("option", { value: "" }, "No repositories"));
    sel.disabled = true;
    return;
  }
  sel.disabled = false;
  for (const r of State.repos) {
    sel.appendChild(el("option", { value: r.id, selected: r.id === State.repoId || null }, r.name));
  }
  sel.value = State.repoId;
}

function setRepo(id) {
  if (id === State.repoId) return;
  State.repoId = id;
  persistRepoSelection();
  renderRepoSelector();
  // repo-scoped pages re-render on selection change
  const seg = currentRoute()[0];
  if (["playbooks", "roles", "inventory", "topology", "hygiene", "impact", "drift", "services", "playbook", "role"].includes(seg)) {
    if (seg === "playbook" || seg === "role") location.hash = "#/" + (seg === "playbook" ? "playbooks" : "roles");
    else route();
  }
}

function getScan(repoId, force = false) {
  if (force) State.scanCache.delete(repoId);
  if (!State.scanCache.has(repoId)) {
    const p = api(`/repos/${repoId}/scan`).catch((e) => {
      State.scanCache.delete(repoId);
      throw e;
    });
    State.scanCache.set(repoId, p);
  }
  return State.scanCache.get(repoId);
}

/** Guard for repo-scoped pages: returns repo or renders an empty state. */
async function requireRepo(view) {
  await loadRepos();
  const repo = currentRepo();
  if (repo) return repo;
  view.appendChild(el("div", { class: "hero" },
    el("div", { html: ICONS.tree, class: "hero-ic", style: { width: "56px", margin: "0 auto 14px" } }),
    el("h2", null, "No repository selected"),
    el("p", null, "Pine reads playbooks, roles and inventories straight from your Ansible repositories. Connect one to get started."),
    el("button", { class: "btn btn-primary", onclick: () => { location.hash = "#/repos"; setTimeout(openAddRepoModal, 50); } },
      icon("plus"), "Connect repository")));
  return null;
}

/* ============================================================
   3. Router
   ============================================================ */

const PAGE_TITLES = {
  dashboard: "Dashboard", repos: "Repositories", playbooks: "Playbooks",
  playbook: "Playbook", roles: "Roles", role: "Role", inventory: "Inventory",
  topology: "Topology", hygiene: "Hygiene", impact: "Impact",
  drift: "Drift", services: "Services", schedules: "Schedules", pipelines: "Pipelines",
  jobs: "Jobs", job: "Job", plan: "Plan",
};

function currentRoute() {
  const hash = location.hash.replace(/^#\/?/, "");
  return hash.split("/").map((s) => s); // segments stay encoded; pages decode as needed
}

async function route() {
  runCleanups();
  const segs = currentRoute();
  let name = segs[0] || "dashboard";
  const handlers = {
    dashboard: pageDashboard,
    repos: pageRepos,
    playbooks: pagePlaybooks,
    playbook: pagePlaybookDetail,
    roles: pageRoles,
    role: pageRoleDetail,
    inventory: pageInventory,
    topology: pageTopology,
    hygiene: pageHygiene,
    impact: pageImpact,
    drift: pageDrift,
    services: pageServices,
    schedules: pageSchedules,
    pipelines: pagePipelines,
    jobs: pageJobs,
    job: pageJobDetail,
    plan: pagePlan,
  };
  if (!handlers[name]) { location.hash = "#/dashboard"; return; }

  // nav highlight
  const navKey = { playbook: "playbooks", role: "roles", job: "jobs", plan: "playbooks" }[name] || name;
  $$(".nav-item").forEach((n) => n.classList.toggle("active", n.dataset.nav === navKey));
  $("#topbar-title").textContent = PAGE_TITLES[name] || "Pine";

  const view = $("#view");
  view.innerHTML = "";
  const page = el("div", { class: "page" + (name === "topology" || name === "drift" || name === "services" ? " wide" : "") });
  view.appendChild(page);
  try {
    await handlers[name](page, segs.slice(1));
  } catch (e) {
    console.error(e);
    page.innerHTML = "";
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "Something went wrong"),
      el("p", null, e.message || String(e)),
      el("button", { class: "btn", onclick: route }, "Retry")));
    toast(e.message || String(e), "error");
  }
}

/* ============================================================
   4a. Dashboard
   ============================================================ */

async function pageDashboard(page) {
  page.appendChild(skeletonRows(2, 90));
  const [stats] = await Promise.all([api("/stats"), loadRepos()]);
  page.innerHTML = "";

  if (!stats.repos) {
    page.appendChild(el("div", { class: "hero" },
      el("div", { html: ICONS.tree.replace("<svg", '<svg class="tree"') }),
      el("h2", null, "Connect your first repository"),
      el("p", null, "Point Pine at a Git URL or a local path containing your Ansible project. Pine scans playbooks, roles and inventories, visualizes task flows and host topology, and runs jobs with live logs."),
      el("button", { class: "btn btn-primary", onclick: () => { location.hash = "#/repos"; setTimeout(openAddRepoModal, 50); } },
        icon("plus"), "Add repository")));
    return;
  }

  // stat cards
  const cards = [
    ["Repositories", stats.repos, "", "#/repos"],
    ["Playbooks", stats.playbooks, "accent", "#/playbooks"],
    ["Roles", stats.roles, "accent", "#/roles"],
    ["Hosts", stats.hosts, "secondary", "#/inventory"],
    ["Jobs running", stats.running_jobs, stats.running_jobs ? "warning" : "", "#/jobs"],
  ];
  page.appendChild(el("div", { class: "grid cols-4", style: { gridTemplateColumns: "repeat(auto-fit, minmax(170px, 1fr))" } },
    cards.map(([lbl, num, cls, href]) =>
      el("div", { class: "card stat-card clickable", onclick: () => (location.hash = href) },
        el("div", { class: `num ${cls}` }, String(num ?? 0)),
        el("div", { class: "lbl" }, lbl)))));

  // quick actions
  page.appendChild(el("div", { class: "row mt" },
    el("button", { class: "btn btn-primary", onclick: () => openRunModal() }, icon("play"), "Run playbook"),
    el("button", { class: "btn", onclick: () => { location.hash = "#/repos"; setTimeout(openAddRepoModal, 50); } }, icon("plus"), "Add repository"),
    el("button", { class: "btn", onclick: () => (location.hash = "#/topology") }, "View topology")));

  // recent jobs
  page.appendChild(el("div", { class: "section-title" }, "Recent jobs"));
  const recent = stats.recent_jobs || [];
  if (!recent.length) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "No jobs yet"),
      el("p", null, "Run a playbook to see execution history here."),
      el("button", { class: "btn btn-primary", onclick: () => openRunModal() }, icon("play"), "Run playbook")));
  } else {
    page.appendChild(jobsTable(recent));
  }
}

function jobsTable(jobs) {
  return el("div", { class: "table-wrap" },
    el("table", { class: "data" },
      el("thead", null, el("tr", null,
        ["Status", "Playbook", "Repository", "Inventory", "Flags", "Duration", "Started"].map((h) => el("th", null, h)))),
      el("tbody", null, jobs.map((j) =>
        el("tr", { class: "clickable", onclick: () => (location.hash = `#/job/${j.id}`) },
          el("td", null, statusPill(j.status)),
          el("td", null, el("span", { class: "mono", style: { color: "var(--text)" } }, j.playbook)),
          el("td", null, j.repo_name || "—"),
          el("td", { class: "mono" }, j.inventory || "—"),
          el("td", null, el("span", { class: "row", style: { gap: "5px" } },
            j.check ? el("span", { class: "flag-badge check" }, "check") : null,
            j.simulated ? el("span", { class: "flag-badge sim" }, "sim") : null)),
          el("td", { class: "mono" }, fmtDuration(j.duration_ms)),
          el("td", { class: "muted", title: j.started || j.created }, relTime(j.started || j.created)))))));
}

/* ============================================================
   4b. Repositories
   ============================================================ */

async function pageRepos(page) {
  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Repositories"),
    el("span", { class: "sub" }, "Git or local Ansible projects"),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: openAddRepoModal }, icon("plus"), "Add repository")));

  const listBox = el("div", { class: "grid cols-3" });
  page.appendChild(listBox);
  listBox.appendChild(skeletonRows(2, 180));

  let pollTimer = null;
  onCleanup(() => clearInterval(pollTimer));

  const refresh = async () => {
    await loadRepos(true);
    drawRepoCards(listBox, refresh);
    const anySyncing = State.repos.some((r) => r.status === "syncing" || r.status === "new");
    clearInterval(pollTimer);
    if (anySyncing) pollTimer = setInterval(async () => {
      try {
        const before = State.repos.map((r) => r.id + r.status).join();
        await loadRepos(true);
        const after = State.repos.map((r) => r.id + r.status).join();
        if (before !== after) {
          drawRepoCards(listBox, refresh);
          for (const r of State.repos) State.scanCache.delete(r.id);
        }
        if (!State.repos.some((r) => r.status === "syncing" || r.status === "new")) clearInterval(pollTimer);
      } catch { /* keep polling */ }
    }, 2000);
  };
  await refresh();
}

function drawRepoCards(listBox, refresh) {
  listBox.innerHTML = "";
  if (!State.repos.length) {
    listBox.style.display = "block";
    listBox.appendChild(el("div", { class: "empty" },
      el("h3", null, "No repositories connected"),
      el("p", null, "Add a Git URL (https or ssh) or a local filesystem path. Pine clones/pulls and scans it for playbooks, roles and inventories."),
      el("button", { class: "btn btn-primary", onclick: openAddRepoModal }, icon("plus"), "Add repository")));
    return;
  }
  listBox.style.display = "grid";
  for (const repo of State.repos) {
    const src = repo.url || repo.path || "—";
    const sum = repo.summary || {};
    const card = el("div", { class: "card repo-card" },
      el("div", { class: "top" },
        el("span", { class: "name" }, repo.name),
        repo.branch ? el("span", { class: "chip" }, icon("branch"), repo.branch) : null,
        el("div", { class: "grow", style: { flex: 1 } }),
        repoStatusBadge(repo)),
      el("div", { class: "src" }, src),
      repo.status === "error" && repo.error ? el("div", { class: "err" }, repo.error) : null,
      (repo.scan_paths || []).length ? el("div", { class: "row small muted", style: { flexWrap: "wrap", gap: "4px" } },
        el("span", null, "scan:"),
        ...repo.scan_paths.map((p) => el("span", { class: "chip" }, p)),
        el("a", { href: "#", onclick: (e) => { e.preventDefault(); openScanPathsModal(repo, refresh); } }, "edit")) : null,
      repo.status === "ready" && !(sum.playbooks > 0) ? el("div", { class: "err", style: { borderColor: "#3d3420", background: "rgba(251,191,36,.07)", color: "var(--warn, #fbbf24)" } },
        "No playbooks found in this repository. ",
        el("a", { href: "#", style: { color: "inherit", textDecoration: "underline" }, onclick: (e) => { e.preventDefault(); openScanPathsModal(repo, refresh); } },
          "Tell Pine where to look")) : null,
      el("div", { class: "counts" },
        [["playbooks", sum.playbooks], ["roles", sum.roles], ["inventories", sum.inventories], ["hosts", sum.hosts], ["groups", sum.groups]]
          .map(([k, v]) => el("div", { class: "c" }, el("b", null, String(v ?? 0)), el("span", null, k)))),
      el("div", { class: "row small muted" },
        repo.last_synced ? `Last synced ${relTime(repo.last_synced)}` : "Never synced"),
      el("div", { class: "actions" },
        el("button", {
          class: "btn btn-sm", disabled: repo.status === "syncing" || null,
          onclick: async (e) => {
            e.currentTarget.disabled = true;
            try {
              await api(`/repos/${repo.id}/sync`, { method: "POST" });
              State.scanCache.delete(repo.id);
              worktreeCache.delete(repo.id);
              toast(`Syncing ${repo.name}…`, "success", "Sync started");
              refresh();
            } catch (err) { toast(err.message, "error"); refresh(); }
          },
        }, icon("sync"), "Sync"),
        el("button", {
          class: "btn btn-sm",
          onclick: () => { setRepo(repo.id); location.hash = "#/playbooks"; },
        }, icon("folder"), "Browse"),
        el("div", { style: { flex: 1 } }),
        el("button", {
          class: "btn btn-sm btn-danger",
          onclick: async () => {
            const ok = await confirmModal("Delete repository", `Remove “${repo.name}” from Pine? The source repository itself is not touched. Job history referencing it remains.`);
            if (!ok) return;
            try {
              await api(`/repos/${repo.id}`, { method: "DELETE" });
              State.scanCache.delete(repo.id);
              toast(`Removed ${repo.name}`, "success");
              refresh();
            } catch (err) { toast(err.message, "error"); }
          },
        }, icon("trash"), "Delete")));
    listBox.appendChild(card);
    appendRepoWorktrees(card, repo, refresh); // async; fills in once loaded
  }
}

// shortSha trims a commit hash for compact display.
function shortSha(sha) {
  return sha && sha.length > 8 ? sha.slice(0, 8) : (sha || "—");
}

// worktreeCache memoizes GET /repos/{id}/worktrees per repo so the repos
// page's status polling doesn't refetch worktrees on every redraw.
const worktreeCache = new Map();

// appendRepoWorktrees lazily loads a repo's git worktrees and, when there is
// more than the main checkout, renders a switchable list inside its card so
// you can jump to a branch's working tree without leaving Repositories.
async function appendRepoWorktrees(card, repo, refresh) {
  if (repo.status !== "ready") return;
  let wt = worktreeCache.get(repo.id);
  try {
    if (!wt) {
      wt = await api(`/repos/${repo.id}/worktrees`);
      worktreeCache.set(repo.id, wt);
    }
  } catch { return; } // worktrees are a bonus — never break a card over them
  if (!wt.is_git) return;
  const trees = wt.worktrees || [];
  if (trees.length < 2) return;          // nothing to switch between
  if (!card.isConnected) return;         // card got replaced mid-load

  const list = el("div", { class: "wt-list" });
  for (const w of trees) {
    const ref = w.bare
      ? el("span", { class: "chip" }, "bare")
      : w.branch
        ? el("span", { class: "chip", style: { color: "var(--secondary)" } }, icon("branch"), w.branch)
        : el("span", { class: "chip" }, "detached");

    const existing = State.repos.find((r) => r.path && r.path === w.path);
    const active = w.main ? repo.id === State.repoId : existing && existing.id === State.repoId;
    let action;
    if (active) action = el("span", { class: "chip green" }, icon("check"), "active");
    else if (w.main) action = el("button", {
      class: "btn btn-sm", title: `Switch to ${repo.name}`,
      onclick: () => { setRepo(repo.id); toast(`Switched to ${repo.name}`, "success", "Worktree"); location.hash = "#/playbooks"; },
    }, "Switch");
    else action = el("button", {
      class: "btn btn-sm btn-secondary", title: existing ? `Switch to ${existing.name}` : "Open this worktree as a repository",
      onclick: (e) => switchToWorktree(repo, w, e.currentTarget, refresh),
    }, icon("folder"), existing ? "Switch" : "Open");

    list.appendChild(el("div", { class: "wt-row" },
      ref,
      el("span", { class: "mono wt-sha" }, shortSha(w.head)),
      el("span", { class: "mono wt-path", title: w.path }, w.main ? "main checkout" : w.path),
      w.locked ? el("span", { class: "chip warn", title: w.lock_reason || "" }, "locked") : null,
      w.prunable ? el("span", { class: "chip warn", title: w.prunable_reason || "" }, "prunable") : null,
      el("div", { class: "grow" }),
      action));
  }

  const section = el("div", { class: "wt-section" },
    el("div", { class: "wt-head" }, el("span", { class: "wt-ic", html: ICONS.branch }), `${trees.length} worktrees`),
    list);
  const actions = card.querySelector(".actions");
  if (actions) card.insertBefore(section, actions);
  else card.appendChild(section);
}

// switchToWorktree makes a worktree the active repo: reuse a repo already
// registered at that path, otherwise register the worktree path as a new
// path-based repo (inheriting the parent's scan paths) and select it.
async function switchToWorktree(parent, w, btn, refresh) {
  const existing = State.repos.find((r) => r.path && r.path === w.path);
  const label = w.branch || shortSha(w.head);
  if (existing) {
    setRepo(existing.id);
    toast(`Switched to ${existing.name}`, "success", "Worktree");
    location.hash = "#/playbooks";
    return;
  }
  if (btn) btn.disabled = true;
  try {
    const created = await api("/repos", {
      method: "POST",
      body: JSON.stringify({ name: `${parent.name} @ ${label}`, path: w.path, scan_paths: parent.scan_paths || [] }),
    });
    await loadRepos(true);           // so the repo selector knows the new repo
    setRepo(created.id);
    toast(`Opened worktree ${label} as a repository`, "success", "Switched to worktree");
    location.hash = "#/playbooks";
  } catch (e) {
    if (btn) btn.disabled = false;
    toast(e.message, "error");
  }
}

// parseScanPaths splits a comma/newline separated input into clean entries.
function parseScanPaths(raw) {
  return raw.split(/[\n,]/).map((s) => s.trim()).filter(Boolean);
}

// openScanPathsModal lets the user tell Pine where playbooks live when the
// default discovery finds nothing (or finds too much).
function openScanPathsModal(repo, onDone) {
  const pathsIn = el("textarea", {
    rows: "3",
    placeholder: "playbooks/\nplaybooks/*/apps\ndeploy/site.yml",
  });
  pathsIn.value = (repo.scan_paths || []).join("\n");

  const saveBtn = el("button", { class: "btn btn-primary" }, icon("sync"), "Save & re-scan");
  saveBtn.onclick = async () => {
    saveBtn.disabled = true;
    try {
      await api(`/repos/${repo.id}`, {
        method: "PATCH",
        body: JSON.stringify({ scan_paths: parseScanPaths(pathsIn.value) }),
      });
      State.scanCache.delete(repo.id);
      closeModal();
      toast(`Re-scanning ${repo.name}…`, "success", "Scan paths updated");
      if (onDone) onDone();
    } catch (e) {
      saveBtn.disabled = false;
      toast(e.message, "error");
    }
  };

  openModal({
    title: `Playbook locations — ${repo.name}`,
    body: el("div", null,
      el("p", { class: "muted", style: { marginTop: 0 } },
        "By default Pine scans the whole repository for playbook-shaped YAML files (skipping roles, inventories and vars). If that finds nothing — or too much — list the directories, files or glob patterns to scan, one per line, relative to the repo root."),
      el("div", { class: "field" }, el("label", null, "Scan paths"), pathsIn,
        el("span", { class: "hint" }, "Examples: playbooks/ · playbooks/*/billing · deploy/site.yml. Leave empty to restore automatic discovery."))),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), saveBtn],
  });
  pathsIn.focus();
}

function openAddRepoModal() {
  let mode = "git";
  const nameIn = el("input", { type: "text", placeholder: "demo-infra", autocomplete: "off" });
  const urlIn = el("input", { type: "text", placeholder: "https://github.com/acme/infra.git", autocomplete: "off" });
  const branchIn = el("input", { type: "text", placeholder: "main", autocomplete: "off" });
  const pathIn = el("input", { type: "text", placeholder: "/srv/ansible/infra", autocomplete: "off" });
  const scanIn = el("input", { type: "text", placeholder: "playbooks/, deploy/*.yml (optional)", autocomplete: "off" });

  const gitFields = el("div", null,
    el("div", { class: "field" }, el("label", null, "Git URL"), urlIn,
      el("span", { class: "hint" }, "https:// or git@ remote. Pine clones it into its workspace.")),
    el("div", { class: "field" }, el("label", null, "Branch"), branchIn,
      el("span", { class: "hint" }, "Defaults to main.")));
  const pathFields = el("div", { style: { display: "none" } },
    el("div", { class: "field" }, el("label", null, "Local path"), pathIn,
      el("span", { class: "hint" }, "Absolute path on the Pine server, e.g. /srv/ansible/infra.")));

  const tabGit = el("span", { class: "tab active" }, "Git URL");
  const tabPath = el("span", { class: "tab" }, "Local path");
  const setMode = (m) => {
    mode = m;
    tabGit.classList.toggle("active", m === "git");
    tabPath.classList.toggle("active", m === "path");
    gitFields.style.display = m === "git" ? "" : "none";
    pathFields.style.display = m === "path" ? "" : "none";
  };
  tabGit.onclick = () => setMode("git");
  tabPath.onclick = () => setMode("path");

  const submitBtn = el("button", { class: "btn btn-primary" }, icon("plus"), "Add repository");
  submitBtn.onclick = async () => {
    const name = nameIn.value.trim();
    if (!name) { toast("Repository name is required", "error"); nameIn.focus(); return; }
    let body;
    if (mode === "git") {
      const url = urlIn.value.trim();
      if (!url) { toast("Git URL is required", "error"); urlIn.focus(); return; }
      body = { name, url, branch: branchIn.value.trim() || "main" };
    } else {
      const path = pathIn.value.trim();
      if (!path) { toast("Local path is required", "error"); pathIn.focus(); return; }
      body = { name, path };
    }
    const scanPaths = parseScanPaths(scanIn.value);
    if (scanPaths.length) body.scan_paths = scanPaths;
    submitBtn.disabled = true;
    try {
      const repo = await api("/repos", { method: "POST", body: JSON.stringify(body) });
      closeModal();
      toast(`${repo.name} added — syncing now`, "success", "Repository added");
      State.repoId = State.repoId || repo.id;
      persistRepoSelection();
      await loadRepos(true);
      if (currentRoute()[0] === "repos") route();
      else location.hash = "#/repos";
    } catch (e) {
      submitBtn.disabled = false;
      toast(e.message, "error");
    }
  };

  const body = el("div", null,
    el("div", { class: "seg-tabs", style: { marginBottom: "16px" } }, tabGit, tabPath),
    el("div", { class: "field" }, el("label", null, "Name"), nameIn,
      el("span", { class: "hint" }, "A short identifier shown across Pine.")),
    gitFields, pathFields,
    el("div", { class: "field" }, el("label", null, "Playbook paths", el("span", { class: "muted" }, " — optional")), scanIn,
      el("span", { class: "hint" }, "Comma-separated dirs, files or globs if your playbooks live in non-standard places. By default Pine scans the whole repo.")));
  body.addEventListener("keydown", (e) => { if (e.key === "Enter" && e.target.tagName === "INPUT") submitBtn.click(); });

  openModal({
    title: "Add repository",
    body,
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), submitBtn],
  });
  nameIn.focus();
}

/* ============================================================
   4c. Playbooks (list)
   ============================================================ */

function encodePath(p) { return p.split("/").map(encodeURIComponent).join("/"); }
function decodeSegs(segs) { return segs.map(decodeURIComponent).join("/"); }

/** Project bucket for a playbook: the directory it lives in ("" = repo root). */
function playbookProject(path) {
  const i = path.lastIndexOf("/");
  return i === -1 ? "" : path.slice(0, i);
}

async function pagePlaybooks(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  const head = el("div", { class: "page-head" },
    el("h1", null, "Playbooks"),
    el("span", { class: "sub" }, `in ${repo.name}`),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: () => openRunModal({ repoId: repo.id }) }, icon("play"), "Run playbook"));
  page.appendChild(head);

  const box = el("div");
  page.appendChild(box);
  box.appendChild(skeletonRows(2, 60));

  const scan = await getScan(repo.id);
  box.innerHTML = "";
  const playbooks = scan.playbooks || [];
  if (!playbooks.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No playbooks found"),
      el("p", null, repo.scan_paths && repo.scan_paths.length
        ? `Nothing matched the configured scan paths (${repo.scan_paths.join(", ")}). Adjust them or clear them to let Pine scan the whole repository.`
        : "Pine scanned the whole repository for playbook-shaped YAML files but found none. If your playbooks live somewhere unusual, point Pine at them."),
      el("div", { class: "row", style: { justifyContent: "center", gap: "8px" } },
        el("button", { class: "btn btn-primary", onclick: () => openScanPathsModal(repo, () => route()) }, "Set playbook locations"),
        el("button", { class: "btn", onclick: () => (location.hash = "#/repos") }, "Go to repositories"))));
    return;
  }

  // Build a searchable view-model per playbook once; render reuses it on filter.
  const items = playbooks.map((pb) => {
    const plays = pb.plays || [];
    const hosts = [...new Set(plays.map((p) => p.hosts).filter(Boolean))];
    const tags = [...new Set(plays.flatMap((p) => [
      ...(p.tags || []),
      ...((p.tasks || []).flatMap((t) => t.tags || [])),
    ]))];
    const project = playbookProject(pb.path);
    const haystack = [pb.name || "", pb.path, project, ...hosts, ...tags].join(" ").toLowerCase();
    return { pb, plays, hosts, tags, project, haystack };
  });
  const projectCount = new Set(items.map((it) => it.project)).size;

  const filter = el("input", {
    type: "search", class: "pb-filter",
    placeholder: "Filter by name, path, host or tag…",
  });
  const summary = el("span", { class: "muted small" });
  const toggleAllBtn = el("button", { class: "btn btn-sm", onclick: () => toggleAll() });
  const toolbar = el("div", { class: "pb-toolbar" },
    el("span", { class: "pb-search-ic", html: ICONS.search }),
    filter,
    el("div", { class: "grow" }),
    summary,
    toggleAllBtn);
  box.appendChild(toolbar);

  const list = el("div", { class: "pb-list" });
  box.appendChild(list);

  // Collapsed projects persist across re-renders within this page visit.
  const collapsed = new Set();
  // Project keys shown by the current filter — kept fresh by render() so the
  // collapse-all button only ever toggles what's actually on screen.
  let visibleProjects = [];

  const toggleAll = () => {
    const allCollapsed = visibleProjects.length > 0 && visibleProjects.every((p) => collapsed.has(p));
    if (allCollapsed) visibleProjects.forEach((p) => collapsed.delete(p));
    else visibleProjects.forEach((p) => collapsed.add(p));
    render();
  };
  const applyFilter = (term) => { filter.value = term; render(); filter.focus(); };

  const chip = (text, cls, title) => el("span", {
    class: "chip pb-chip-link" + (cls ? " " + cls : ""), title: title || `Filter by ${text}`,
    onclick: (e) => { e.stopPropagation(); applyFilter(text); },
  }, text);

  const playbookRow = (it) => {
    const { pb, plays, hosts, tags } = it;
    const href = `#/playbook/${repo.id}/${encodePath(pb.path)}`;
    const base = pb.path.includes("/") ? pb.path.slice(pb.path.lastIndexOf("/") + 1) : pb.path;
    return el("div", { class: "pb-row clickable", onclick: () => (location.hash = href) },
      el("div", { class: "pb-row-main" },
        el("div", { class: "pb-row-name" }, pb.name || base),
        el("div", { class: "pb-row-path mono" }, pb.path)),
      el("div", { class: "pb-row-chips" },
        el("span", { class: "chip" }, `${plays.length} play${plays.length === 1 ? "" : "s"}`),
        hosts.slice(0, 4).map((h) => chip(h, "pb-host", `Filter by host ${h}`)),
        hosts.length > 4 ? el("span", { class: "chip" }, `+${hosts.length - 4}`) : null,
        tags.slice(0, 6).map((t) => chip(t, "tag", `Filter by tag ${t}`)),
        tags.length > 6 ? el("span", { class: "chip tag" }, `+${tags.length - 6}`) : null),
      el("div", { class: "pb-row-acts" },
        el("button", {
          class: "btn btn-sm btn-ghost", title: "Preview raw YAML",
          onclick: (e) => { e.stopPropagation(); openRawFileModal(repo.id, pb.path, pb.path); },
        }, icon("code")),
        el("button", {
          class: "btn btn-sm btn-secondary", title: "Plan — estimate what this playbook would do, without running it",
          onclick: (e) => { e.stopPropagation(); openRunModal({ repoId: repo.id, playbook: pb.path, plan: true }); },
        }, icon("clipboard"), "Plan"),
        el("button", {
          class: "btn btn-sm btn-primary",
          onclick: (e) => { e.stopPropagation(); openRunModal({ repoId: repo.id, playbook: pb.path }); },
        }, icon("play"), "Run")));
  };

  const render = () => {
    const terms = filter.value.trim().toLowerCase().split(/\s+/).filter(Boolean);
    const matched = items.filter((it) => terms.every((t) => it.haystack.includes(t)));

    summary.textContent = terms.length
      ? `${matched.length} of ${items.length} playbook${items.length === 1 ? "" : "s"}`
      : `${items.length} playbook${items.length === 1 ? "" : "s"} · ${projectCount} project${projectCount === 1 ? "" : "s"}`;

    list.innerHTML = "";
    if (!matched.length) {
      list.appendChild(el("div", { class: "empty" },
        el("h3", null, "No playbooks match"),
        el("p", null, `Nothing matches “${filter.value.trim()}”.`),
        el("button", { class: "btn", onclick: () => applyFilter("") }, "Clear filter")));
      return;
    }

    // group → sorted, repo root first then alphabetical by directory
    const groups = new Map();
    for (const it of matched) {
      if (!groups.has(it.project)) groups.set(it.project, []);
      groups.get(it.project).push(it);
    }
    const ordered = [...groups.entries()].sort((a, b) => {
      if (a[0] === "") return -1;
      if (b[0] === "") return 1;
      return a[0].localeCompare(b[0]);
    });

    visibleProjects = ordered.map(([project]) => project);
    const allCollapsed = visibleProjects.length > 0 && visibleProjects.every((p) => collapsed.has(p));
    toggleAllBtn.innerHTML = "";
    toggleAllBtn.append(icon(allCollapsed ? "plus" : "minus"), allCollapsed ? "Expand all" : "Collapse all");
    toggleAllBtn.disabled = visibleProjects.length === 0;

    for (const [project, group] of ordered) {
      group.sort((a, b) => (a.pb.name || a.pb.path).localeCompare(b.pb.name || b.pb.path));
      const isCollapsed = collapsed.has(project);
      const label = project || "repository root";
      const head = el("div", { class: "pb-group-head", onclick: () => {
        if (collapsed.has(project)) collapsed.delete(project); else collapsed.add(project);
        render();
      } },
        el("span", { class: "pb-caret" + (isCollapsed ? " collapsed" : "") }, "▾"),
        el("span", { class: "pb-folder-ic", html: ICONS.folder }),
        el("span", { class: "pb-group-name" + (project ? " mono" : "") }, label),
        el("span", { class: "chip pb-group-count" }, String(group.length)));
      const rows = el("div", { class: "pb-rows" });
      if (!isCollapsed) group.forEach((it) => rows.appendChild(playbookRow(it)));
      list.appendChild(el("div", { class: "pb-group" }, head, rows));
    }
  };

  filter.addEventListener("input", render);
  render();
}

/* ============================================================
   4d. Playbook detail — task flow visualization
   ============================================================ */

const MODULE_CATEGORIES = [
  ["pkg", /(^|\.)(apt|apt_key|apt_repository|yum|dnf|pip|package|gem|npm|apk|pacman|homebrew|snap|zypper|yum_repository|easy_install)$/],
  ["svc", /(^|\.)(service|systemd|systemd_service|sysvinit|supervisorctl|cron|launchd|runit|service_facts)$/],
  ["file", /(^|\.)(copy|template|file|lineinfile|blockinfile|fetch|unarchive|archive|synchronize|stat|find|replace|assemble|tempfile|slurp|get_url|git|mount)$/],
  ["docker", /(docker|podman|k8s|kubernetes|helm|ecs|ec2|aws|s3|azure|gcp|gce|openstack|terraform)/],
  ["cmd", /(^|\.)(command|shell|script|raw|expect|win_command|win_shell)$/],
  ["flow", /(^|\.)(block|meta|include_tasks|import_tasks|include_role|import_role|include_vars|import_playbook|set_fact|debug|assert|fail|wait_for|wait_for_connection|pause|add_host|group_by)$/],
];

function moduleCategory(mod) {
  const m = (mod || "").toLowerCase();
  for (const [cat, re] of MODULE_CATEGORIES) if (re.test(m)) return cat;
  return "other";
}

function shortModule(mod) {
  if (!mod) return "task";
  const parts = mod.split(".");
  return parts.length > 2 ? parts.slice(-1)[0] : mod;
}

/* ---- variable resolution in the task-flow (uses /repos/{id}/resolve) ---- */

const VAR_PATH_RE = /^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*$/;
const LEAD_IDENT_RE = /[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z0-9_]+)*/;

function lookupVarPath(vars, path) {
  let cur = vars;
  for (const part of path.split(".")) {
    if (cur && typeof cur === "object" && !Array.isArray(cur) && part in cur) cur = cur[part];
    else return undefined;
  }
  return cur;
}

function isScalarVal(v) { return v === null || typeof v !== "object"; }

function fmtVarValue(v) {
  if (v === null || v === undefined) return "";
  if (Array.isArray(v)) return `[${v.length} item${v.length === 1 ? "" : "s"}]`;
  if (typeof v === "object") return "{…}";
  return String(v);
}

// Split a string into literal + {{ … }} tokens, resolving simple var paths
// against `vars`. Filters/expressions stay unresolved (honest: shown raw).
function templateTokens(str, vars) {
  const tokens = [];
  let rest = str;
  for (;;) {
    const start = rest.indexOf("{{");
    if (start < 0) { if (rest) tokens.push({ text: rest }); break; }
    if (start > 0) tokens.push({ text: rest.slice(0, start) });
    const end = rest.indexOf("}}", start);
    if (end < 0) { tokens.push({ text: rest.slice(start) }); break; }
    const inner = rest.slice(start + 2, end).trim();
    const raw = rest.slice(start, end + 2);
    const m = inner.match(LEAD_IDENT_RE);
    const name = m ? m[0] : inner;
    let value, known = false;
    if (VAR_PATH_RE.test(inner) && vars) {
      const v = lookupVarPath(vars, inner);
      if (v !== undefined && isScalarVal(v)) { value = v; known = true; }
    }
    tokens.push({ varToken: true, raw, inner, name, value, known });
    rest = rest.slice(end + 2);
  }
  return tokens;
}

// itemLabel renders one loop item for display: scalars as-is, dicts by their
// most identifying key.
function itemLabel(it) {
  if (it === null || typeof it !== "object") return String(it);
  return String(it.name ?? it.key ?? it.path ?? it.role ?? it.dest ?? "{…}");
}

// resolveItemToken turns an `item` / `item.field` reference into the set of
// concrete values it takes across the loop's items.
function resolveItemToken(inner, items) {
  const sub = inner.split(".").slice(1);
  const out = [];
  for (const it of items) {
    if (!sub.length) { out.push(itemLabel(it)); continue; }
    let v = it;
    for (const p of sub) { if (v && typeof v === "object" && p in v) v = v[p]; else { v = undefined; break; } }
    if (v !== undefined && isScalarVal(v)) out.push(String(v));
  }
  return out;
}

// interpolateStr substitutes simple {{ var }} references from `vars` (scalars),
// recursively, leaving anything unresolvable (facts, expressions) intact.
function interpolateStr(str, vars, depth) {
  if (!str || typeof str !== "string" || !str.includes("{{") || (depth || 0) > 10) return str;
  const out = str.replace(/\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}/g, (m, name) => {
    const v = lookupVarPath(vars, name);
    return v !== undefined && isScalarVal(v) ? String(v) : m;
  });
  return out === str ? out : interpolateStr(out, vars, (depth || 0) + 1);
}

// loopItemsFor returns a task's concrete loop items (literal items captured at
// scan time, or a loop variable resolved against the current vars), with any
// {{ var }} inside the items expanded too.
function loopItemsFor(task, ctx) {
  let items = null;
  if (Array.isArray(task.loop_values) && task.loop_values.length) {
    items = task.loop_values;
  } else {
    const expr = (task.loop_expr || "").trim();
    const m = expr.match(/^\{\{\s*([A-Za-z_][A-Za-z0-9_.]*)\s*\}\}$/);
    const name = m ? m[1] : (/^[A-Za-z_][A-Za-z0-9_.]*$/.test(expr) ? expr : null);
    if (!name || !ctx || !ctx.vars) return null;
    const v = lookupVarPath(ctx.vars, name);
    items = Array.isArray(v) ? v : null;
  }
  if (items && ctx && ctx.vars) {
    items = items.map((it) => (typeof it === "string" ? interpolateStr(it, ctx.vars, 0) : it));
  }
  return items;
}

// renderTemplated returns a node for `str` with {{ vars }} shown as their
// resolved value (raw on hover) or, when unresolved, the raw {{ … }} — every
// variable is a chip that opens its lineage. Inside a loop, {{ item }} shows
// the possible items. Falls back to plain text with no resolution context.
function renderTemplated(str, ctx, cls) {
  if (str == null) return document.createTextNode("");
  const canResolve = ctx && (ctx.vars || (ctx.loopItems && ctx.loopItems.length));
  if (!str.includes("{{") || !canResolve) return document.createTextNode(str);
  const span = el("span", cls ? { class: cls } : null);
  for (const tok of templateTokens(str, ctx.vars || {})) {
    if (tok.text != null) { span.appendChild(document.createTextNode(tok.text)); continue; }
    // {{ item }} / {{ item.field }} inside a loop → a compact placeholder chip
    // (keeps paths readable); the concrete values are listed in the loop line.
    if (ctx.loopItems && ctx.loopItems.length && /^item(\..+)?$/.test(tok.inner)) {
      const vals = [...new Set(resolveItemToken(tok.inner, ctx.loopItems))];
      span.appendChild(el("span", {
        class: "var-tok var-loop",
        title: vals.length
          ? `${tok.raw} — loops over ${ctx.loopItems.length} item${ctx.loopItems.length === 1 ? "" : "s"}:\n${vals.join("\n")}`
          : tok.raw,
      }, tok.inner));
      continue;
    }
    if (tok.known) {
      span.appendChild(el("span", {
        class: "var-tok var-known",
        title: `${tok.raw} = ${fmtVarValue(tok.value)}\n(click for lineage)`,
        onclick: (e) => { e.stopPropagation(); openVarPopover(e.currentTarget, tok.name, ctx, "known"); },
      }, fmtVarValue(tok.value)));
      continue;
    }
    const klass = classifyVar(tok.name, ctx);
    span.appendChild(el("span", {
      class: "var-tok var-" + klass,
      title: `${tok.raw} — ${VAR_STATE_TITLE[klass]}\n(click for lineage)`,
      onclick: (e) => { e.stopPropagation(); openVarPopover(e.currentTarget, tok.name, ctx, klass); },
    }, tok.raw));
  }
  return span;
}

// renderLoopItems lists the concrete items a looped task iterates over — one
// per line, each highlighted like a variable value (so a remaining {{ fact }}
// shows as a runtime token, resolved parts as plain text).
function renderLoopItems(items, ctx) {
  const box = el("div", { class: "t-loop-items" },
    el("div", { class: "tli-head" },
      el("span", { class: "tli-label", html: ICONS.loop }),
      el("span", { class: "tli-count" }, `loops over ${items.length} item${items.length === 1 ? "" : "s"}`)));
  const list = el("div", { class: "tli-list" });
  const seen = new Set();
  for (const it of items) {
    const label = typeof it === "string" ? it : itemLabel(it);
    if (seen.has(label)) continue;
    seen.add(label);
    if (seen.size > 12) break;
    list.appendChild(el("div", { class: "tli-line" },
      el("span", { class: "tli-bullet" }, "•"),
      typeof it === "string" && it.includes("{{")
        ? renderTemplated(it, ctx)
        : document.createTextNode(label)));
  }
  if (items.length > seen.size) list.appendChild(el("div", { class: "tli-more muted small" }, `+${items.length - seen.size} more`));
  box.appendChild(list);
  return box;
}

/* ---- per-play variables panel (docked beside the task flow) ---- */

// the variables a single play's tasks reference (base identifiers).
function playReferencedNames(play) {
  const set = new Set();
  collectTaskRefs(play.pre_tasks, set);
  collectTaskRefs(play.tasks, set);
  collectTaskRefs(play.post_tasks, set);
  collectTaskRefs(play.handlers, set);
  return set;
}

// one play's resolved vars (value + lineage) plus its referenced-but-unresolved
// vars, each tagged runtime / elsewhere / undefined — same states as the flow.
function aggregatePlayVars(playVars, knownSet, play) {
  const map = new Map();
  const vars = (playVars && playVars.vars) || {};
  const lineage = (playVars && playVars.lineage) || {};
  for (const k of Object.keys(vars)) {
    map.set(k, { name: k, value: vars[k], chain: lineage[k] || [], state: "known" });
  }
  for (const name of playReferencedNames(play)) {
    const base = name.split(".")[0];
    if (map.has(name) || map.has(base)) continue;
    const state = isRuntimeVar(name) ? "runtime" : (knownSet.has(name) || knownSet.has(base) ? "elsewhere" : "undefined");
    map.set(name, { name, value: undefined, chain: [], state });
  }
  return [...map.values()].sort((a, b) => a.name.localeCompare(b.name));
}

// the colour key, matching the variable-token colours used in the task flow.
const VAR_LEGEND = [
  ["known", "resolved"], ["runtime", "runtime"], ["elsewhere", "elsewhere"],
  ["undefined", "nowhere"], ["loop", "loop item"],
];
function varColorLegend() {
  return el("div", { class: "var-legend" },
    VAR_LEGEND.map(([s, l]) => el("span", { class: "vl-item" }, el("span", { class: "vl-dot var-" + s }), l)));
}

// one variable row: resolved ones expand to their lineage; others show a state chip.
function renderVarRow(v) {
  const row = el("div", { class: "vp-item vp-" + (v.state || "known") });
  if (v.state === "known") {
    const effScope = v.chain.length ? v.chain[v.chain.length - 1].scope : "";
    const chainBox = el("div", { class: "vp-item-chain" });
    v.chain.forEach((e, i) => chainBox.appendChild(el("div", { class: "var-pop-row" + (i === v.chain.length - 1 ? " eff" : "") },
      el("span", { class: "vp-scope" }, VAR_SCOPE_LABELS[e.scope] || e.scope),
      el("span", { class: "vp-name mono", title: e.name }, e.name),
      el("span", { class: "vp-val mono" }, fmtVarValue(e.value)))));
    row.append(
      el("div", { class: "vp-item-head", onclick: () => row.classList.toggle("open") },
        el("span", { class: "vp-item-name mono" }, v.name),
        el("span", { class: "vp-item-val mono", title: fmtVarValue(v.value) }, fmtVarValue(v.value)),
        effScope ? el("span", { class: "vp-item-src" }, VAR_SCOPE_LABELS[effScope] || effScope) : null),
      chainBox);
  } else {
    row.append(el("div", { class: "vp-item-head vp-noexpand", title: VAR_STATE_TITLE[v.state] },
      el("span", { class: "vp-item-name mono" }, v.name),
      el("span", { class: "chip var-state-chip var-" + v.state }, v.state)));
  }
  return row;
}

// The variables panel fills the space left of the task flow; the drag handle
// adjusts the FLOW (task) width instead — drag right for more task room, left to
// give the panel more. Stored in --flow-w on :root, persisted across renders.
const FLOW_W_MIN = 360, FLOW_W_MAX = 1200;
function applySavedFlowWidth() {
  const w = parseInt(localStorage.getItem("pine.flowWidth") || "", 10);
  if (w >= FLOW_W_MIN && w <= FLOW_W_MAX) {
    document.documentElement.style.setProperty("--flow-w", w + "px");
  }
}
// attachPvResize wires the handle on the panel's left edge: it resizes the task
// flow (and thus how much is left for the panel).
function attachPvResize(handle) {
  handle.addEventListener("mousedown", (e) => {
    e.preventDefault();
    const body = handle.closest(".play-layout") && handle.closest(".play-layout").querySelector(".play-body");
    const startX = e.clientX;
    const startW = body ? body.getBoundingClientRect().width : 700;
    document.body.classList.add("pv-resizing");
    let pending = false;
    const redrawArrows = () => {
      pending = false;
      document.querySelectorAll(".play-section").forEach(drawNotifyArrows);
    };
    const onMove = (ev) => {
      const w = Math.max(FLOW_W_MIN, Math.min(FLOW_W_MAX, startW + (ev.clientX - startX)));
      document.documentElement.style.setProperty("--flow-w", w + "px");
      // the body width changed → the notify arrows must be redrawn (throttled)
      if (!pending) { pending = true; requestAnimationFrame(redrawArrows); }
    };
    const onUp = () => {
      document.removeEventListener("mousemove", onMove);
      document.removeEventListener("mouseup", onUp);
      document.body.classList.remove("pv-resizing");
      requestAnimationFrame(redrawArrows);
      const cur = parseInt(getComputedStyle(document.documentElement).getPropertyValue("--flow-w"), 10);
      if (cur) { try { localStorage.setItem("pine.flowWidth", String(cur)); } catch { /* ignore */ } }
    };
    document.addEventListener("mousemove", onMove);
    document.addEventListener("mouseup", onUp);
  });
}

// buildPlayVarsPanel renders the variables panel that sits beside a play's tasks.
function buildPlayVarsPanel(play, playVars, resolveState) {
  const known = new Set((resolveState && resolveState.known) || []);
  const vars = aggregatePlayVars(playVars, known, play);
  markUsedVars(vars, play); // sets v.used = referenced by this play (transitively)
  const usedCount = vars.filter((v) => v.used).length;
  const scope = resolveState && resolveState.mode === "host" ? `host: ${resolveState.host}` : "constants (no host)";
  const nUndef = vars.filter((v) => v.state === "undefined").length;

  // default to "Used here" — the variables this playbook actually uses
  let onlyUsed = usedCount > 0;
  const filter = el("input", { type: "search", class: "vars-pane-filter", placeholder: "Filter variables…" });
  const list = el("div", { class: "vars-pane-list" });
  const allBtn = el("button", { class: "vp-seg-btn" + (onlyUsed ? "" : " active"), onclick: () => setUsed(false) }, `All ${vars.length}`);
  const usedBtn = el("button", { class: "vp-seg-btn" + (onlyUsed ? " active" : ""), title: "only variables this playbook references", onclick: () => setUsed(true) }, `Used here ${usedCount}`);
  const seg = el("div", { class: "vp-seg" }, allBtn, usedBtn);
  const setUsed = (v) => { onlyUsed = v; allBtn.classList.toggle("active", !v); usedBtn.classList.toggle("active", v); draw(); };

  const handle = el("div", { class: "pv-resize", title: "Drag to resize" });
  attachPvResize(handle);
  const panel = el("aside", { class: "play-vars" },
    handle,
    el("div", { class: "vars-pane-head" },
      el("div", null, el("b", null, "Variables"),
        el("div", { class: "muted small" }, scope),
        nUndef ? el("div", { class: "vars-pane-undef" }, `${nUndef} defined nowhere`) : null)),
    varColorLegend(), seg, filter, list);
  const draw = () => {
    const q = filter.value.trim().toLowerCase();
    const shown = vars.filter((v) => (!onlyUsed || v.used) && (!q || v.name.toLowerCase().includes(q)));
    list.innerHTML = "";
    if (!shown.length) {
      list.appendChild(el("div", { class: "muted small", style: { padding: "10px" } },
        vars.length ? "No variables match." : "This play references no variables."));
      return;
    }
    shown.forEach((v) => list.appendChild(renderVarRow(v)));
  };
  filter.addEventListener("input", draw);
  draw();
  return panel;
}

// markUsedVars flags the variables a play actually references — directly in its
// tasks, plus the ones those reference through their (authored) values — so the
// panel can filter to "used here" only.
function markUsedVars(vars, play) {
  const byBase = new Map();
  for (const v of vars) byBase.set(v.name.split(".")[0], v);
  const used = new Set();
  const queue = [...playReferencedNames(play)];
  while (queue.length) {
    const base = queue.pop().split(".")[0];
    if (used.has(base)) continue;
    used.add(base);
    const v = byBase.get(base);
    const authored = v && v.chain && v.chain.length ? v.chain[v.chain.length - 1].value : undefined;
    if (typeof authored === "string") extractRefNames(authored).forEach((r) => queue.push(r));
  }
  for (const v of vars) v.used = used.has(v.name.split(".")[0]);
}

const VAR_SCOPE_LABELS = {
  role_default: "role default", role_vars: "role vars", group: "group vars",
  host: "host vars", vars_file: "vars file", play_vars: "play vars",
  vars_prompt: "vars_prompt",
};

// Magic/runtime variables Ansible provides at run time — Pine can't resolve
// them statically, but they are NOT "undefined" either.
const RUNTIME_VARS = new Set([
  "inventory_hostname", "inventory_hostname_short", "inventory_dir", "inventory_file",
  "hostvars", "groups", "group_names", "play_hosts", "ansible_play_hosts",
  "ansible_play_batch", "ansible_play_hosts_all", "playbook_dir", "role_name", "role_path",
  "role_names", "ansible_role_names", "omit", "ansible_check_mode", "ansible_version",
  "ansible_facts", "item", "ansible_loop", "ansible_loop_var", "ansible_index_var",
]);

function isRuntimeVar(name) {
  const base = name.split(".")[0];
  return RUNTIME_VARS.has(base) || base.startsWith("ansible_") || base === "item";
}

// classifyVar buckets an unresolved variable: a runtime/magic var, one defined
// elsewhere in the project (just not in this scope), or one defined NOWHERE.
function classifyVar(name, ctx) {
  if (isRuntimeVar(name)) return "runtime";
  const base = name.split(".")[0];
  if (ctx.known && (ctx.known.has(name) || ctx.known.has(base))) return "elsewhere";
  return "undefined";
}

const VAR_STATE_TITLE = {
  runtime: "runtime variable (provided by Ansible at run time)",
  elsewhere: "defined in the project, but not in this scope",
  undefined: "not defined anywhere Pine can see",
};

// extractRefNames pulls the base variable names referenced by {{ … }} blocks in
// a string (the leading identifier path of each), skipping loop `item`.
function extractRefNames(str) {
  const out = [];
  if (!str || !str.includes("{{")) return out;
  let rest = str;
  for (;;) {
    const s = rest.indexOf("{{");
    if (s < 0) break;
    const e = rest.indexOf("}}", s);
    if (e < 0) break;
    const m = rest.slice(s + 2, e).trim().match(LEAD_IDENT_RE);
    if (m) { const base = m[0].split(".")[0]; if (base !== "item") out.push(base); }
    rest = rest.slice(e + 2);
  }
  return out;
}

// collectTaskRefs walks tasks (and their blocks/inlined imports) gathering every
// variable referenced in names, args and loop expressions.
function collectTaskRefs(tasks, set) {
  for (const t of tasks || []) {
    extractRefNames(t.name).forEach((n) => set.add(n));
    extractRefNames(t.args).forEach((n) => set.add(n));
    extractRefNames(t.loop_expr).forEach((n) => set.add(n));
    (t.loop_values || []).forEach((it) => { if (typeof it === "string") extractRefNames(it).forEach((n) => set.add(n)); });
    collectTaskRefs(t.block, set);
    collectTaskRefs(t.rescue, set);
    collectTaskRefs(t.always, set);
    collectTaskRefs(t.imported, set);
  }
}

let activeVarPopover = null;
function closeVarPopover() {
  if (!activeVarPopover) return;
  activeVarPopover.remove();
  activeVarPopover = null;
  document.removeEventListener("mousedown", onVarPopoverDocClick, true);
  document.removeEventListener("keydown", onVarPopoverKey, true);
}
function onVarPopoverDocClick(e) { if (activeVarPopover && !activeVarPopover.contains(e.target)) closeVarPopover(); }
function onVarPopoverKey(e) { if (e.key === "Escape") closeVarPopover(); }

// openVarPopover shows where a variable's value comes from — the precedence
// chain from the resolve endpoint, with the effective (winning) layer marked.
// klass ("known"|"runtime"|"elsewhere"|"undefined") tailors the empty state.
function openVarPopover(anchor, name, ctx, klass) {
  closeVarPopover();
  const chain = (ctx.lineage && ctx.lineage[name]) || [];
  const k = klass || (chain.length ? "known" : "undefined");
  const body = el("div", { class: "var-pop" },
    el("div", { class: "var-pop-head" },
      el("span", { class: "mono vp-key" }, name),
      el("span", { class: "chip var-state-chip var-" + k, title: "variable state" },
        k === "known" ? (ctx.mode === "host" ? `host: ${ctx.host}` : "constants")
          : k === "runtime" ? "runtime / magic"
          : k === "elsewhere" ? "defined elsewhere"
          : "defined nowhere")));
  if (!chain.length) {
    let msg;
    if (k === "runtime") {
      msg = "A runtime/magic variable — facts, inventory_hostname, groups, hostvars, item… are provided by Ansible while it runs, so Pine can't resolve them statically.";
    } else if (k === "elsewhere") {
      msg = "Defined somewhere in this project, but not in the current scope — most likely a host or group var. Pick a host above to resolve it.";
    } else {
      msg = "Defined nowhere Pine can see — not in any role defaults/vars, group_vars, host_vars, vars_files or play vars (no host var either). It's set at runtime (set_fact / include_vars / register), passed via --extra-vars or the environment, or it's a typo.";
    }
    body.appendChild(el("div", { class: "var-pop-empty var-" + k }, msg));
  } else {
    const list = el("div", { class: "var-pop-chain" });
    chain.forEach((e, i) => list.appendChild(el("div", { class: "var-pop-row" + (i === chain.length - 1 ? " eff" : "") },
      el("span", { class: "vp-scope" }, VAR_SCOPE_LABELS[e.scope] || e.scope),
      el("span", { class: "vp-name mono", title: e.name }, e.name),
      el("span", { class: "vp-val mono" }, fmtVarValue(e.value)))));
    body.appendChild(list);
  }
  const pop = el("div", { class: "var-popover" }, body);
  document.body.appendChild(pop);
  const r = anchor.getBoundingClientRect();
  const left = Math.max(8, Math.min(window.scrollX + r.left, window.scrollX + window.innerWidth - pop.offsetWidth - 12));
  pop.style.top = `${window.scrollY + r.bottom + 6}px`;
  pop.style.left = `${left}px`;
  activeVarPopover = pop;
  setTimeout(() => {
    document.addEventListener("mousedown", onVarPopoverDocClick, true);
    document.addEventListener("keydown", onVarPopoverKey, true);
  }, 0);
}

let taskUid = 0;

// Render a task's args (with templated-var highlighting). For modules that read
// a local repo file/template, add a clickable "open" affordance — resolving a
// {{ templated }} src against the current vars so it points at the real file.
function renderArgsNode(task, tctx, opts) {
  const args = task.args;
  const node = el("div", { class: "t-args mono", title: args }, renderTemplated(args, tctx));
  const localSrc = /(^|\.)(template|copy|unarchive|assemble|script)$/.test(task.module || "");
  // capture the whole src value (may be `{{ var }}` with spaces) up to the next "key:" pair
  const m = /\bsrc:\s*(.+?)(?=,\s*[A-Za-z_][\w]*:\s|$)/.exec(args);
  if (opts.repoId && localSrc && m) {
    const resolved = interpolateStr(m[1].trim(), (tctx && tctx.vars) || {}, 0);
    if (resolved && !resolved.includes("{{")) {
      node.appendChild(el("a", {
        class: "file-link", title: "Open " + resolved,
        onclick: (e) => { e.stopPropagation(); openRawFileModal(opts.repoId, srcFileCandidates(opts.basePath, resolved, task.module), resolved); },
      }, icon("code"), el("span", null, "open")));
    }
  }
  return node;
}

function renderTaskNode(task, opts = {}) {
  // blocks render as bordered groups
  const hasBlock = (task.block && task.block.length) || (task.rescue && task.rescue.length) || (task.always && task.always.length);
  if (hasBlock) {
    const group = el("div", { class: "block-group" });
    const label = el("div", { class: "block-label" }, "block");
    if (task.name) label.appendChild(el("span", { style: { color: "var(--text)", textTransform: "none", letterSpacing: 0, fontSize: "12px", fontWeight: 550 } }, task.name));
    if (task.when) label.appendChild(whenChip(task.when));
    (task.tags || []).forEach((t) => label.appendChild(el("span", { class: "chip tag" }, t)));
    group.appendChild(label);
    const sub = (label2, items, cls) => {
      if (!items || !items.length) return;
      const box = el("div", { class: `block-sub ${cls || ""}` });
      if (label2) box.appendChild(el("div", { class: "block-label", style: { marginTop: "4px" } }, label2));
      box.appendChild(taskColumn(items, opts));
      group.appendChild(box);
    };
    sub(null, task.block);
    if (task.rescue && task.rescue.length) {
      const r = el("div", { class: "block-group rescue-group", style: { marginTop: "10px" } },
        el("div", { class: "block-label" }, "rescue"), taskColumn(task.rescue, opts));
      group.appendChild(r);
    }
    if (task.always && task.always.length) {
      const a = el("div", { class: "block-group always-group", style: { marginTop: "10px" } },
        el("div", { class: "block-label" }, "always"), taskColumn(task.always, opts));
      group.appendChild(a);
    }
    return group;
  }

  // import_tasks resolved at scan time: pull the referenced file's tasks in,
  // rendered as a labelled group so the flow follows the import inline.
  if (task.imported && task.imported.length) {
    const group = el("div", { class: "block-group import-group" });
    const label = el("div", { class: "block-label" },
      el("span", { class: "module mod-flow", title: task.module || "" }, shortModule(task.module)));
    const file = task.include_path;
    if (file && !file.includes("{{") && opts.repoId) {
      label.appendChild(el("a", {
        class: "import-file mono t-args-link", title: "Open " + file,
        onclick: (e) => { e.stopPropagation(); openRawFileModal(opts.repoId, includeCandidates(opts.basePath, file), file); },
      }, icon("code"), el("span", null, file)));
    } else if (file) {
      label.appendChild(el("span", { class: "import-file mono" }, file));
    }
    if (task.name && task.name !== task.module) {
      label.appendChild(el("span", { class: "import-title" }, task.name));
    }
    if (task.when) label.appendChild(whenChip(task.when));
    (task.tags || []).forEach((t) => label.appendChild(el("span", { class: "chip tag" }, t)));
    label.appendChild(el("span", { class: "chip import-count" }, `${task.imported.length} task${task.imported.length === 1 ? "" : "s"}`));
    group.appendChild(label);
    group.appendChild(taskColumn(task.imported, opts));
    return group;
  }

  const cat = moduleCategory(task.module);
  const node = el("div", { class: "task-node" + (opts.handler ? " handler-node" : "") });
  node.dataset.uid = String(++taskUid);
  if (opts.handler && task.name) node.dataset.handlerName = task.name;
  if (task.notify && task.notify.length) node.dataset.notify = task.notify.join("\u0001");

  node.appendChild(el("span", { class: `module mod-${cat}`, title: task.module || "" }, shortModule(task.module)));

  // Inside a loop, resolve {{ item }} to the concrete items for this task.
  const loopItems = task.loop ? loopItemsFor(task, opts) : null;
  const tctx = (loopItems && loopItems.length) ? { ...opts, loopItems } : opts;

  const main = el("div", { class: "t-main" });
  const nameNode = task.name && task.name.includes("{{")
    ? renderTemplated(task.name, tctx)
    : document.createTextNode(task.name || "(unnamed task)");
  main.appendChild(el("div", { class: "t-name" + (task.name ? "" : " unnamed") }, nameNode));
  if (task.args) {
    // A static include/import path is clickable: it opens the referenced file.
    const linkable = task.include_path && !task.include_path.includes("{{") && opts.repoId;
    if (linkable) {
      main.appendChild(el("a", {
        class: "t-args mono t-args-link", title: "Open " + task.include_path,
        onclick: (e) => {
          e.stopPropagation();
          openRawFileModal(opts.repoId, includeCandidates(opts.basePath, task.include_path), task.include_path);
        },
      }, icon("code"), el("span", null, task.args)));
    } else {
      main.appendChild(renderArgsNode(task, tctx, opts));
    }
  }
  if (loopItems && loopItems.length) main.appendChild(renderLoopItems(loopItems, tctx));
  const chips = el("div", { class: "t-chips" });
  (task.tags || []).forEach((t) => chips.appendChild(el("span", { class: "chip tag" }, t)));
  if (task.when) chips.appendChild(whenChip(task.when));
  (task.notify || []).forEach((n) => chips.appendChild(el("span", {
    class: "chip notify-chip notify-link", title: `notifies handler “${n}” — click to scroll to it`,
    onclick: (e) => { e.stopPropagation(); scrollToHandler(e.currentTarget, n); },
  }, icon("bell"), n)));
  if (chips.childNodes.length) main.appendChild(chips);
  node.appendChild(main);

  const icons = el("div", { class: "t-icons" });
  if (task.loop) icons.appendChild(el("span", { class: "ic loop-ic", title: "Loops over items", html: ICONS.loop }));
  if (icons.childNodes.length) node.appendChild(icons);
  return node;
}

function whenChip(expr) {
  return el("span", { class: "chip when-chip", title: `when: ${expr}` }, "when");
}

// scrollToHandler jumps from a task's notify chip to the matching handler node
// within the same play and flashes it.
function scrollToHandler(anchor, name) {
  const section = anchor.closest(".play-section");
  if (!section) return;
  const target = [...section.querySelectorAll("[data-handler-name]")].find((h) => h.dataset.handlerName === name);
  if (!target) return;
  target.scrollIntoView({ behavior: "smooth", block: "center" });
  target.classList.add("flash-handler");
  setTimeout(() => target.classList.remove("flash-handler"), 1300);
}

function taskColumn(tasks, opts = {}) {
  const col = el("div", { class: "flow-col" });
  tasks.forEach((t, i) => {
    if (i > 0) col.appendChild(el("div", { class: "flow-connector" }));
    col.appendChild(renderTaskNode(t, opts));
  });
  return col;
}

async function pagePlaybookDetail(page, segs) {
  await loadRepos();
  const repoId = segs[0];
  const path = decodeSegs(segs.slice(1));
  const repo = State.repos.find((r) => r.id === repoId);
  if (!repo) { page.appendChild(el("div", { class: "empty" }, el("h3", null, "Repository not found"))); return; }

  page.appendChild(skeletonRows(3, 110));
  const scan = await getScan(repoId);
  const pb = (scan.playbooks || []).find((p) => p.path === path);
  page.innerHTML = "";
  if (!pb) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "Playbook not found"),
      el("p", null, `“${path}” is not in the latest scan of ${repo.name}. It may have been removed or renamed.`),
      el("button", { class: "btn", onclick: () => (location.hash = "#/playbooks") }, "Back to playbooks")));
    return;
  }

  $("#topbar-title").textContent = pb.name || pb.path;
  applySavedFlowWidth();
  const headControls = el("div", { class: "resolve-controls" });
  page.appendChild(el("div", { class: "page-head" },
    el("a", { href: "#/playbooks", class: "btn btn-ghost btn-sm" }, "← Playbooks"),
    el("div", null,
      el("h1", null, pb.name || pb.path),
      el("div", { class: "muted mono small" }, `${repo.name} / ${pb.path}`)),
    el("div", { class: "grow" }),
    headControls,
    el("button", { class: "btn", onclick: () => openRawFileModal(repoId, pb.path, pb.path) },
      icon("code"), "View YAML"),
    el("button", {
      class: "btn btn-secondary", title: "Plan — estimate what this playbook would do, without running it",
      onclick: () => openRunModal({ repoId, playbook: pb.path, plan: true }),
    }, icon("clipboard"), "Plan"),
    el("button", { class: "btn btn-primary", onclick: () => openRunModal({ repoId, playbook: pb.path }) },
      icon("play"), "Run playbook")));

  const sections = [];
  const playsBox = el("div");
  page.appendChild(playsBox);
  if (!(pb.plays || []).length) {
    playsBox.appendChild(el("div", { class: "empty" }, el("h3", null, "Empty playbook"), el("p", null, "No plays were parsed from this file.")));
  }

  // Resolve {{ vars }} so the flow shows real values. Default: host-agnostic
  // ("constants"); a host can be picked to resolve host-specific vars too.
  let resolveState = null;
  const redraw = () => sections.forEach(drawNotifyArrows);
  const renderPlays = () => {
    closeVarPopover();
    sections.length = 0;
    playsBox.innerHTML = "";
    (pb.plays || []).forEach((play, idx) => {
      const section = renderPlaySection(play, idx, repoId, pb.path, resolveState);
      sections.push(section);
      playsBox.appendChild(section);
    });
    requestAnimationFrame(() => requestAnimationFrame(redraw));
  };
  renderPlays();
  onCleanup(closeVarPopover);

  // Variables now live in a panel docked beside each play's tasks; this button
  // just toggles their visibility across all plays.
  let varsHidden = false;
  const toggleVarsPanels = () => { varsHidden = !varsHidden; playsBox.classList.toggle("vars-hidden", varsHidden); };

  const loadResolve = async (inventory, host) => {
    try {
      const q = new URLSearchParams({ playbook: pb.path });
      if (inventory) q.set("inventory", inventory);
      if (host) q.set("host", host);
      resolveState = await api(`/repos/${repoId}/resolve?${q.toString()}`);
    } catch { resolveState = null; }
    buildHeadControls();
    renderPlays();
  };

  // total distinct variables across plays, for the toggle button's count.
  const aggregateVars = () => {
    const known = resolveState && resolveState.known ? new Set(resolveState.known) : new Set();
    const map = new Map();
    for (const pv of (resolveState && resolveState.plays) || []) {
      for (const k of Object.keys(pv.vars || {})) {
        if (!map.has(k)) map.set(k, { name: k, value: pv.vars[k], chain: (pv.lineage || {})[k] || [], state: "known" });
      }
    }
    for (const play of pb.plays || []) {
      for (const name of playReferencedNames(play)) {
        const base = name.split(".")[0];
        if (map.has(name) || map.has(base)) continue;
        const state = isRuntimeVar(name) ? "runtime" : (known.has(name) || known.has(base) ? "elsewhere" : "undefined");
        map.set(name, { name, value: undefined, chain: [], state });
      }
    }
    return [...map.values()].sort((a, b) => a.name.localeCompare(b.name));
  };

  function buildHeadControls() {
    const items = [];
    if (resolveState && (resolveState.inventories || []).length) {
      const SEP = ""; // delimiter for inventory + host option values
      const sel = el("select", { class: "resolve-as", title: "Resolve variables as…" });
      sel.appendChild(el("option", { value: "" }, "Constants (no host)"));
      const mkOpt = (inv, h) => {
        const name = typeof h === "string" ? h : h.name;
        const varies = typeof h === "object" && h.varies;
        // ● marks hosts whose resolution differs from the constants (host/group vars)
        return el("option", {
          value: `${inv.name}${SEP}${name}`,
          class: varies ? "opt-varies" : null,
          title: varies ? "has host/group-specific variables" : "no host-specific variables",
        }, varies ? `● ${name}` : name);
      };
      for (const inv of resolveState.inventories) {
        if (!inv.hosts || !inv.hosts.length) continue;
        const label = inv.name || "inventory";
        // hosts the playbook actually targets come first, in their own group
        const targeted = inv.hosts.filter((h) => typeof h === "object" && h.targeted);
        const others = inv.hosts.filter((h) => !(typeof h === "object" && h.targeted));
        if (targeted.length) {
          const og = el("optgroup", { label: `${label} · targeted by this playbook` });
          targeted.forEach((h) => og.appendChild(mkOpt(inv, h)));
          sel.appendChild(og);
        }
        if (others.length) {
          const og = el("optgroup", { label: targeted.length ? `${label} · other hosts` : label });
          others.forEach((h) => og.appendChild(mkOpt(inv, h)));
          sel.appendChild(og);
        }
      }
      sel.value = resolveState.mode === "host" ? `${resolveState.inventory}${SEP}${resolveState.host}` : "";
      sel.onchange = () => { const [inv, host] = sel.value.split(SEP); loadResolve(host ? inv : "", host || ""); };
      items.push(el("div", { class: "resolve-picker", title: "Resolve variables as…" },
        icon("host"), el("span", { class: "muted small" }, "resolve as"), sel));
    }
    if (resolveState) {
      const n = aggregateVars().length;
      items.push(el("button", {
        class: "btn btn-sm", title: "Show/hide the variables panels beside each play",
        onclick: toggleVarsPanels,
      }, icon("code"), `Variables ${n}`));
    }
    headControls.replaceChildren(...items);
  }

  loadResolve("", "");

  window.addEventListener("resize", redraw);
  onCleanup(() => window.removeEventListener("resize", redraw));
}

function renderPlaySection(play, idx, repoId, basePath, resolveState) {
  // basePath = playbook dir, used to resolve clickable include/import files.
  const dir = basePath && basePath.includes("/") ? basePath.slice(0, basePath.lastIndexOf("/")) : "";
  const pv = resolveState && resolveState.plays ? resolveState.plays[idx] : null;
  const ctx = {
    repoId, basePath: dir,
    vars: pv ? pv.vars : null,
    lineage: pv ? pv.lineage : null,
    mode: resolveState ? resolveState.mode : "machine",
    host: resolveState ? resolveState.host : "",
    known: resolveState && resolveState.known ? new Set(resolveState.known) : null,
  };
  const prompts = play.vars_prompt || [];
  const head = el("div", { class: "play-head" },
    el("span", { class: "play-name" }, play.name || `Play ${idx + 1}`),
    el("span", { class: "hosts-label" }, icon("host"), "hosts:"),
    el("span", { class: "hosts-pat", title: "Host pattern this play targets" }, play.hosts || "all"),
    play.become ? el("span", { class: "chip warn", title: "Privilege escalation enabled" }, "become") : null,
    play.serial ? el("span", { class: "chip", title: "Rolling batch size" }, `serial: ${play.serial}`) : null,
    play.strategy ? el("span", { class: "chip" }, `strategy: ${play.strategy}`) : null,
    (play.tags || []).map((t) => el("span", { class: "chip tag" }, t)),
    (play.vars_files || []).length
      ? el("span", { class: "chip", title: "vars_files: " + play.vars_files.join(", ") }, `${play.vars_files.length} vars file${play.vars_files.length === 1 ? "" : "s"}`)
      : null,
    prompts.map((p) => el("span", {
      class: "chip prompt-chip",
      title: `${p.prompt || "prompt"}${p.default ? `\ndefault: ${p.default}` : ""}${p.private ? "\n(private)" : ""}`,
    }, icon("question"), `prompts: ${p.name}`)));

  const body = el("div", { class: "play-body" });
  const svg = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  svg.setAttribute("class", "flow-svg");
  body.appendChild(svg);

  const phase = (label, node, cls) => {
    if (!node) return;
    body.appendChild(el("div", { class: "flow-phase" },
      el("div", { class: `flow-phase-label ${cls || ""}` }, label), node));
  };

  if ((play.pre_tasks || []).length) phase("pre_tasks", taskColumn(play.pre_tasks, ctx));
  if ((play.roles || []).length) {
    phase("roles", el("div", { class: "roles-strip" },
      play.roles.map((r) => el("a", { class: "role-chip-lg", href: `#/role/${repoId}/${encodeURIComponent(r)}` }, icon("role"), r))));
  }
  if ((play.tasks || []).length) phase("tasks", taskColumn(play.tasks, ctx));
  if ((play.post_tasks || []).length) phase("post_tasks", taskColumn(play.post_tasks, ctx));
  if ((play.handlers || []).length) phase("handlers", taskColumn(play.handlers, { ...ctx, handler: true }), "handlers-lbl");

  if (body.children.length === 1) {
    body.appendChild(el("div", { class: "muted small" }, "Nothing to show for this play."));
  }
  // reserve right-side room for notify→handler arrows when the play has handlers
  if (body.querySelector("[data-handler-name]") && body.querySelector("[data-notify]")) {
    body.classList.add("has-arrows");
  }
  // dock the play's variables panel beside the task flow
  const playVars = resolveState && resolveState.plays ? resolveState.plays[idx] : null;
  const panel = resolveState && !play.import ? buildPlayVarsPanel(play, playVars, resolveState) : null;
  return el("div", { class: "play-section" }, head, el("div", { class: "play-layout" }, body, panel));
}

/** Curved SVG lines from tasks with notify → matching handler nodes within a play section. */
function drawNotifyArrows(section) {
  const body = section.querySelector(".play-body");
  const svg = section.querySelector(".flow-svg");
  if (!body || !svg) return;
  svg.innerHTML = "";
  const ns = "http://www.w3.org/2000/svg";
  const bodyRect = body.getBoundingClientRect();
  svg.setAttribute("viewBox", `0 0 ${bodyRect.width} ${bodyRect.height}`);
  svg.setAttribute("width", bodyRect.width);
  svg.setAttribute("height", bodyRect.height);

  // arrowhead marker
  const defs = document.createElementNS(ns, "defs");
  const marker = document.createElementNS(ns, "marker");
  marker.setAttribute("id", "arrow-" + (section.dataset.mid || (section.dataset.mid = String(++taskUid))));
  marker.setAttribute("viewBox", "0 0 8 8");
  marker.setAttribute("refX", "7"); marker.setAttribute("refY", "4");
  marker.setAttribute("markerWidth", "7"); marker.setAttribute("markerHeight", "7");
  marker.setAttribute("orient", "auto-start-reverse");
  const tip = document.createElementNS(ns, "path");
  tip.setAttribute("d", "M0,0 L8,4 L0,8 z");
  tip.setAttribute("fill", "#fbbf24");
  marker.appendChild(tip);
  defs.appendChild(marker);
  svg.appendChild(defs);

  const handlers = new Map();
  for (const h of body.querySelectorAll("[data-handler-name]")) handlers.set(h.dataset.handlerName, h);
  if (!handlers.size) return;

  let lane = 0;
  for (const src of body.querySelectorAll("[data-notify]")) {
    for (const name of src.dataset.notify.split("\u0001")) {
      const dst = handlers.get(name);
      if (!dst) continue;
      const a = src.getBoundingClientRect();
      const b = dst.getBoundingClientRect();
      const x1 = a.right - bodyRect.left;
      const y1 = a.top + a.height / 2 - bodyRect.top;
      const x2 = b.right - bodyRect.left;
      const y2 = b.top + b.height / 2 - bodyRect.top;
      const bend = Math.min(bodyRect.width - Math.max(x1, x2) - 12, 70 + (lane % 5) * 26);
      lane++;
      const cx = Math.max(x1, x2) + Math.max(24, bend);
      const path = document.createElementNS(ns, "path");
      path.setAttribute("d", `M ${x1} ${y1} C ${cx} ${y1}, ${cx} ${y2}, ${x2 + 6} ${y2}`);
      path.setAttribute("fill", "none");
      path.setAttribute("stroke", "rgba(251, 191, 36, 0.55)");
      path.setAttribute("stroke-width", "1.5");
      path.setAttribute("stroke-dasharray", "5 4");
      path.setAttribute("marker-end", `url(#${marker.getAttribute("id")})`);
      svg.appendChild(path);
    }
  }
}

/* ============================================================
   4e. Roles
   ============================================================ */

async function pageRoles(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Roles"),
    el("span", { class: "sub" }, `in ${repo.name}`)));

  const box = el("div", { class: "grid cols-3" });
  page.appendChild(box);
  box.appendChild(skeletonRows(2, 130));
  const scan = await getScan(repo.id);
  box.innerHTML = "";

  const roles = scan.roles || [];
  if (!roles.length) {
    box.style.display = "block";
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No roles found"),
      el("p", null, "Pine scans the roles/ directory for Ansible roles (tasks, defaults, handlers, meta).")));
    return;
  }

  for (const role of roles) {
    const card = el("div", { class: "card role-card clickable", onclick: () => (location.hash = `#/role/${repo.id}/${encodeURIComponent(role.name)}`) },
      el("div", { class: "name" }, icon("role"), role.name),
      el("div", { class: "desc" }, role.description || el("span", { class: "muted", style: { fontStyle: "italic" } }, "No description (meta/main.yml)")),
      el("div", { class: "stats" },
        el("span", null, el("b", null, String(role.tasks_count ?? (role.tasks || []).length)), " tasks"),
        el("span", null, el("b", null, String((role.handlers || []).length)), " handlers"),
        el("span", null, el("b", null, String((role.templates || []).length)), " templates"),
        el("span", null, el("b", null, String((role.files || []).length)), " files")),
      (role.dependencies || []).length
        ? el("div", { class: "deps" },
            el("span", { class: "muted small", style: { marginRight: "2px" } }, "deps:"),
            role.dependencies.map((d) => el("span", {
              class: "chip link",
              onclick: (e) => { e.stopPropagation(); location.hash = `#/role/${repo.id}/${encodeURIComponent(d)}`; },
            }, d)))
        : null);
    box.appendChild(card);
  }
}

async function pageRoleDetail(page, segs) {
  await loadRepos();
  const repoId = segs[0];
  const name = decodeURIComponent(segs[1] || "");
  const repo = State.repos.find((r) => r.id === repoId);
  if (!repo) { page.appendChild(el("div", { class: "empty" }, el("h3", null, "Repository not found"))); return; }

  page.appendChild(skeletonRows(2, 110));
  const scan = await getScan(repoId);
  const role = (scan.roles || []).find((r) => r.name === name);
  page.innerHTML = "";
  if (!role) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "Role not found"),
      el("p", null, `“${name}” is not in the latest scan of ${repo.name}.`),
      el("button", { class: "btn", onclick: () => (location.hash = "#/roles") }, "Back to roles")));
    return;
  }

  $("#topbar-title").textContent = role.name;
  page.appendChild(el("div", { class: "page-head" },
    el("a", { href: "#/roles", class: "btn btn-ghost btn-sm" }, "← Roles"),
    el("div", null,
      el("h1", null, role.name),
      el("div", { class: "muted small" }, role.description || el("span", { class: "mono" }, role.path))),
    el("div", { class: "grow" }),
    el("button", {
      class: "btn", onclick: () => openRawFileModal(repoId,
        [`${role.path}/tasks/main.yml`, `${role.path}/tasks/main.yaml`],
        `${role.name}/tasks/main.yml`),
    }, icon("code"), "View YAML"),
    el("span", { class: "chip" }, `${role.tasks_count ?? (role.tasks || []).length} tasks`)));

  const tabsDef = [
    ["tasks", `Tasks (${(role.tasks || []).length})`],
    ["defaults", "Defaults"],
    ["handlers", `Handlers (${(role.handlers || []).length})`],
    ["meta", "Meta"],
  ];
  const tabBar = el("div", { class: "tabs" });
  const content = el("div");
  page.appendChild(tabBar);
  page.appendChild(content);

  const show = (key) => {
    $$(".tab", tabBar).forEach((t) => t.classList.toggle("active", t.dataset.k === key));
    content.innerHTML = "";
    const roleCtx = { repoId, basePath: role.path };
    if (key === "tasks") {
      content.appendChild((role.tasks || []).length
        ? el("div", { class: "panel", style: { padding: "18px 22px" } }, taskColumn(role.tasks, roleCtx))
        : el("div", { class: "empty" }, el("h3", null, "No tasks"), el("p", null, "tasks/main.yml is empty or missing.")));
    } else if (key === "defaults") {
      const hasDefaults = role.defaults && Object.keys(role.defaults).length;
      const hasVars = role.vars && Object.keys(role.vars).length;
      if (!hasDefaults && !hasVars) {
        content.appendChild(el("div", { class: "empty" }, el("h3", null, "No defaults"), el("p", null, "defaults/main.yml defines no variables.")));
      } else {
        if (hasDefaults) {
          content.appendChild(el("div", { class: "section-title" }, "defaults/main.yml"));
          content.appendChild(el("div", { class: "panel", style: { padding: "14px 18px" } }, kvTree(role.defaults)));
        }
        if (hasVars) {
          content.appendChild(el("div", { class: "section-title" }, "vars/main.yml"));
          content.appendChild(el("div", { class: "panel", style: { padding: "14px 18px" } }, kvTree(role.vars)));
        }
      }
    } else if (key === "handlers") {
      content.appendChild((role.handlers || []).length
        ? el("div", { class: "panel", style: { padding: "18px 22px" } }, taskColumn(role.handlers, { ...roleCtx, handler: true }))
        : el("div", { class: "empty" }, el("h3", null, "No handlers"), el("p", null, "handlers/main.yml is empty or missing.")));
    } else if (key === "meta") {
      content.appendChild(roleMetaView(role, repoId));
    }
  };
  for (const [k, label] of tabsDef) {
    tabBar.appendChild(el("span", { class: "tab", "data-k": k, onclick: () => show(k) }, label));
  }
  show("tasks");
}

/** YAML-ish pretty-printed key:value tree for arbitrary JSON objects. */
function kvTree(obj) {
  const root = el("div", { class: "kv-tree" });
  const walk = (val, indent, keyLabel) => {
    const pad = "  ".repeat(indent);
    const keyHtml = keyLabel !== undefined
      ? `<span class="kv-key">${esc(keyLabel)}</span>: `
      : "";
    if (val === null || val === undefined) {
      root.appendChild(el("div", { class: "kv-row", html: pad + keyHtml + '<span class="kv-null">null</span>' }));
    } else if (Array.isArray(val)) {
      root.appendChild(el("div", { class: "kv-row", html: pad + keyHtml }));
      for (const item of val) {
        if (item !== null && typeof item === "object") {
          root.appendChild(el("div", { class: "kv-row", html: "  ".repeat(indent + 1) + '<span class="kv-dash">-</span>' }));
          for (const [k2, v2] of Object.entries(item)) walk(v2, indent + 2, k2);
        } else {
          root.appendChild(el("div", { class: "kv-row", html: "  ".repeat(indent + 1) + '<span class="kv-dash">- </span>' + scalarHtml(item) }));
        }
      }
    } else if (typeof val === "object") {
      if (keyLabel !== undefined) root.appendChild(el("div", { class: "kv-row", html: pad + keyHtml }));
      for (const [k2, v2] of Object.entries(val)) walk(v2, keyLabel !== undefined ? indent + 1 : indent, k2);
    } else {
      root.appendChild(el("div", { class: "kv-row", html: pad + keyHtml + scalarHtml(val) }));
    }
  };
  walk(obj, 0, undefined);
  return root;
}

function scalarHtml(v) {
  if (typeof v === "number") return `<span class="kv-num">${esc(v)}</span>`;
  if (typeof v === "boolean") return `<span class="kv-bool">${v}</span>`;
  return `<span class="kv-str">${esc(JSON.stringify(String(v)))}</span>`;
}

/** Meta tab: description + simple SVG dependency graph (deps → role). */
function roleMetaView(role, repoId) {
  const box = el("div");
  const deps = role.dependencies || [];
  box.appendChild(el("div", { class: "panel", style: { padding: "16px 18px", marginBottom: "16px" } },
    el("div", { class: "row" },
      el("span", { class: "muted small" }, "Path"),
      el("span", { class: "mono" }, role.path || "—")),
    el("div", { class: "row mt" },
      el("span", { class: "muted small" }, "Description"),
      el("span", null, role.description || "—")),
    (role.templates || []).length ? el("div", { class: "row mt" },
      el("span", { class: "muted small" }, "Templates"),
      role.templates.map((t) => el("span", { class: "chip" }, t))) : null,
    (role.files || []).length ? el("div", { class: "row mt" },
      el("span", { class: "muted small" }, "Files"),
      role.files.map((f) => el("span", { class: "chip" }, f))) : null));

  box.appendChild(el("div", { class: "section-title" }, "Dependencies"));
  if (!deps.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No dependencies"),
      el("p", null, "This role declares no dependencies in meta/main.yml.")));
    return box;
  }

  // simple SVG: dep nodes (left column) → arrows → this role (right)
  const ns = "http://www.w3.org/2000/svg";
  const rowH = 54, w = 560, h = Math.max(deps.length * rowH + 20, 120);
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", `0 0 ${w} ${h}`);
  svg.setAttribute("width", "100%");
  svg.style.maxWidth = w + "px";

  const mkNode = (x, y, label, accent, href) => {
    const g = document.createElementNS(ns, "g");
    if (href) { g.style.cursor = "pointer"; g.addEventListener("click", () => (location.hash = href)); }
    const bw = Math.max(label.length * 7.5 + 34, 90);
    const rect = document.createElementNS(ns, "rect");
    rect.setAttribute("x", x); rect.setAttribute("y", y - 17);
    rect.setAttribute("width", bw); rect.setAttribute("height", 34);
    rect.setAttribute("rx", 9);
    rect.setAttribute("fill", accent ? "rgba(74,222,128,0.12)" : "#161d19");
    rect.setAttribute("stroke", accent ? "#4ade80" : "#2c3e34");
    const text = document.createElementNS(ns, "text");
    text.setAttribute("x", x + bw / 2); text.setAttribute("y", y + 4.5);
    text.setAttribute("text-anchor", "middle");
    text.setAttribute("fill", accent ? "#4ade80" : "#e7efe9");
    text.setAttribute("font-size", "13");
    text.setAttribute("font-weight", "600");
    text.setAttribute("font-family", "var(--font)");
    text.textContent = label;
    g.appendChild(rect); g.appendChild(text);
    svg.appendChild(g);
    return { x, y, w: bw };
  };

  const targetY = h / 2;
  const target = mkNode(w - 180, targetY, role.name, true, null);
  deps.forEach((d, i) => {
    const y = 28 + i * rowH;
    const n = mkNode(20, y, d, false, `#/role/${repoId}/${encodeURIComponent(d)}`);
    const path = document.createElementNS(ns, "path");
    const x1 = n.x + n.w, x2 = target.x;
    path.setAttribute("d", `M ${x1} ${y} C ${x1 + 70} ${y}, ${x2 - 70} ${targetY}, ${x2 - 4} ${targetY}`);
    path.setAttribute("fill", "none");
    path.setAttribute("stroke", "#3a5246");
    path.setAttribute("stroke-width", "1.6");
    svg.insertBefore(path, svg.firstChild);
    const dot = document.createElementNS(ns, "circle");
    dot.setAttribute("cx", x2 - 4); dot.setAttribute("cy", targetY); dot.setAttribute("r", 3);
    dot.setAttribute("fill", "#4ade80");
    svg.appendChild(dot);
  });

  box.appendChild(el("div", { class: "panel dep-graph-box" }, svg));
  return box;
}

/* ============================================================
   4f. Inventory
   ============================================================ */

async function pageInventory(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  page.appendChild(skeletonRows(2, 120));
  const [scan, facts, svcRep] = await Promise.all([
    getScan(repo.id),
    api(`/repos/${repo.id}/facts`).catch(() => null), // facts are optional decoration
    api(`/repos/${repo.id}/services`).catch(() => null), // service status, optional
  ]);
  page.innerHTML = "";

  const factHosts = (facts && facts.hosts) || {};
  const factCount = facts ? (facts.count ?? Object.keys(factHosts).length) : 0;
  // host -> [{name,state,...}] from the services report, for status pills
  const svcCells = (svcRep && svcRep.cells) || {};
  const servicesForHost = (host) => {
    const out = [];
    for (const svc of (svcRep && svcRep.services) || []) {
      const cell = (svcCells[svc] || {})[host];
      if (cell) out.push({ name: svc, ...cell });
    }
    return out;
  };

  const inventories = scan.inventories || [];
  if (!inventories.length) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "No inventories found"),
      el("p", null, `Pine scans inventories/, inventory/ and hosts files in ${repo.name}. Add an INI or YAML inventory to the repo and re-sync.`)));
    return;
  }

  let invName = State.invSelection.get(repo.id);
  if (!inventories.some((i) => i.name === invName)) invName = inventories[0].name;
  const inv = inventories.find((i) => i.name === invName);

  const invSel = el("select", { onchange: (e) => { State.invSelection.set(repo.id, e.target.value); route(); } },
    inventories.map((i) => el("option", { value: i.name, selected: i.name === invName || null }, i.name)));

  const searchIn = el("input", { type: "search", placeholder: "Filter hosts…", style: { width: "220px" } });

  const refreshFactsBtn = el("button", {
    class: "btn btn-sm",
    title: "Launch a [gather facts] job against this inventory",
  }, icon("sync"), "Refresh facts");
  refreshFactsBtn.onclick = async () => {
    refreshFactsBtn.disabled = true;
    try {
      const job = await api(`/repos/${repo.id}/facts/refresh`, {
        method: "POST",
        body: JSON.stringify({ inventory: invName }),
      });
      toast(el("span", null, "Gathering facts… ", el("a", { href: `#/job/${job.id}` }, "job started")),
        "success", "Facts");
    } catch (e) {
      toast(e.message, "error", "Facts refresh failed");
    } finally {
      refreshFactsBtn.disabled = false;
    }
  };

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Inventory"),
    inventories.length > 1 ? invSel : el("span", { class: "chip green" }, invName),
    el("span", { class: "chip" }, inv.format || "?"),
    el("span", { class: "sub mono" }, inv.path || ""),
    el("div", { class: "grow" }),
    el("span", {
      class: "chip" + (factCount ? " green" : ""),
      title: facts
        ? "Hosts with gathered ansible facts stored by Pine"
        : "Facts could not be loaded",
    }, factCount ? `${factCount} host${factCount === 1 ? "" : "s"} with facts` : "no facts"),
    refreshFactsBtn,
    inv.path ? el("button", {
      class: "btn btn-sm", onclick: () => openRawFileModal(repo.id, invSourceCandidates(inv), inv.path),
    }, icon("code"), "View source") : null,
    searchIn));

  const left = el("div", { class: "panel inv-tree" });
  const right = el("div", { class: "panel", style: { padding: "18px 20px", minHeight: "300px" } });
  page.appendChild(el("div", { class: "inv-layout" }, left, right));

  const groups = inv.groups || [];
  const hosts = inv.hosts || [];
  const groupByName = new Map(groups.map((g) => [g.name, g]));
  const hostByName = new Map(hosts.map((h) => [h.name, h]));
  const isChild = new Set(groups.flatMap((g) => g.children || []));
  const roots = groups.filter((g) => !isChild.has(g.name));

  // `all` is Ansible's universal parent. Nest every other top-level group under
  // it instead of rendering them as flat siblings that each repeat the same
  // hosts. Synthesize a virtual `all` when the scanner dropped it (it only
  // keeps `all` when it carries group_vars).
  let allGroup = groupByName.get("all");
  const topGroups = roots.filter((g) => g.name !== "all");
  if (!allGroup && topGroups.length > 1) {
    allGroup = { name: "all", hosts: hosts.map((h) => h.name), vars: {}, virtual: true };
    groupByName.set("all", allGroup);
  }
  const declaredTop = topGroups.filter((g) => !g.constructed);
  const facetTop = topGroups.filter((g) => g.constructed);

  // expr by which each constructed group derives its membership (groups: map),
  // plus the keys that keyed_groups derive value-named groups from.
  const derivations = new Map();
  const keyedKeys = [];
  for (const r of inv.constructed_rules || []) {
    for (const [name, expr] of Object.entries(r.groups || {})) derivations.set(name, expr);
    for (const kg of r.keyed_groups || []) if (kg.key && !keyedKeys.includes(kg.key)) keyedKeys.push(kg.key);
  }

  /** count hosts including descendants */
  const groupHostCount = (g, seen = new Set()) => {
    if (seen.has(g.name)) return 0;
    seen.add(g.name);
    let n = (g.hosts || []).length;
    for (const c of g.children || []) {
      const cg = groupByName.get(c);
      if (cg) n += groupHostCount(cg, seen);
    }
    return n;
  };

  let selected = null; // {type:'group'|'host', name}

  const showGroup = (g) => {
    selected = { type: "group", name: g.name };
    markSelection();
    const isAll = g.name === "all";
    right.innerHTML = "";
    right.appendChild(el("h3", { style: { margin: "0 0 2px", display: "flex", alignItems: "center", gap: "8px" } },
      icon(isAll ? "globe" : "group"), g.name,
      el("span", { class: "chip" }, `${groupHostCount(g)} hosts`),
      g.constructed ? el("span", { class: "chip chip-constructed", title: "Generated by the ansible.builtin.constructed inventory plugin" }, "constructed") : null));
    if (isAll) {
      right.appendChild(el("div", { class: "muted small", style: { margin: "2px 0 4px" } },
        "Every host in the inventory. The groups nested under it are views over this set — a host can belong to several at once."));
    } else if (derivations.has(g.name)) {
      right.appendChild(el("div", { class: "inv-derived", title: "Membership derived by ansible.builtin.constructed" },
        el("span", { class: "muted small" }, "members where "),
        el("code", null, derivations.get(g.name))));
    } else if (g.constructed && keyedKeys.length) {
      right.appendChild(el("div", { class: "inv-derived", title: "Generated by keyed_groups in ansible.builtin.constructed" },
        el("span", { class: "muted small" }, "keyed on "),
        keyedKeys.map((k) => el("code", null, k))));
    }
    if ((g.children || []).length) {
      right.appendChild(el("div", { class: "row mt" },
        el("span", { class: "muted small" }, "Children:"),
        g.children.map((c) => el("span", {
          class: "chip link",
          onclick: () => { const cg = groupByName.get(c); if (cg) showGroup(cg); },
        }, c))));
    }
    const vars = g.vars || {};
    right.appendChild(el("div", { class: "section-title" }, "Group vars"));
    right.appendChild(Object.keys(vars).length ? varsTable(vars)
      : el("div", { class: "muted small" }, "No group variables."));
    right.appendChild(el("div", { class: "section-title" }, "Hosts"));
    const direct = (g.hosts || []).map((hn) => hostByName.get(hn) || { name: hn, groups: [], vars: {} });
    right.appendChild(direct.length
      ? el("div", null, direct.map((h) => hostRow(h)))
      : el("div", { class: "muted small" }, "No hosts directly in this group."));
  };

  const showHost = (h) => {
    selected = { type: "host", name: h.name };
    markSelection();
    right.innerHTML = "";
    right.appendChild(el("div", { class: "crumb", style: { marginBottom: "8px" } },
      (h.groups || []).flatMap((g, i) => [
        i ? el("span", { class: "sep" }, "/") : null,
        el("span", { class: "chip link", onclick: () => { const gg = groupByName.get(g); if (gg) showGroup(gg); } }, g),
      ])));
    const hf = factHosts[h.name];
    right.appendChild(el("h3", { style: { margin: "0 0 12px", display: "flex", alignItems: "center", gap: "8px", flexWrap: "wrap" } },
      icon("host"), h.name,
      h.vars && h.vars.ansible_host ? el("span", { class: "chip", style: { color: "var(--secondary)" } }, String(h.vars.ansible_host)) : null,
      hf ? el("span", {
        class: "chip green",
        title: `${hf.keys ?? 0} fact key${(hf.keys ?? 0) === 1 ? "" : "s"} · gathered ${relTime(hf.gathered_at)}`,
      }, `facts · ${relTime(hf.gathered_at)}`) : null));

    // service status pills (declared services + their running/stopped state)
    const svcs = servicesForHost(h.name);
    if (svcs.length) {
      right.appendChild(el("div", { class: "section-title" }, "Services"));
      right.appendChild(el("div", { class: "svc-pills" },
        svcs.map((s) => el("span", {
          class: `svc-pill svc-${s.state || "unknown"}`,
          title: `${s.unit || s.name} — ${s.state || "unknown"}${s.status ? " · " + s.status : ""}`,
        }, el("span", { class: "svc-pill-dot" }), s.name))));
    }

    const vars = h.vars || {};
    const varFilter = el("input", { type: "search", placeholder: "Filter vars…", class: "vars-filter" });
    const lineageNote = el("span", { class: "muted small", style: { display: "none" } }, "lineage unavailable");
    right.appendChild(el("div", { class: "row", style: { margin: "0 0 8px", gap: "10px" } },
      el("div", { class: "section-title", style: { margin: 0 } }, "Host vars"),
      lineageNote,
      el("div", { class: "grow" }),
      varFilter));
    const varsBox = el("div");
    right.appendChild(varsBox);

    let lineage; // undefined = loading, null = unavailable, object = loaded
    const renderVars = () => {
      varsBox.innerHTML = "";
      if (lineage && (lineage.vars || []).length) {
        varsBox.appendChild(lineageVarsView(lineage.vars, varFilter.value));
      } else if (lineage) {
        varsBox.appendChild(el("div", { class: "muted small" }, "No variables for this host."));
      } else if (Object.keys(vars).length) {
        varsBox.appendChild(varsTable(vars, varFilter.value)); // plain fallback
      } else {
        varsBox.appendChild(el("div", { class: "muted small" }, "No host variables."));
      }
    };
    varFilter.addEventListener("input", renderVars);

    varsBox.appendChild(el("div", { class: "skeleton", style: { height: "64px" } }));
    const stillHere = () => selected && selected.type === "host" && selected.name === h.name;
    getLineage(repo.id, invName, h.name).then((lin) => {
      if (!stillHere()) return;
      lineage = lin && Array.isArray(lin.vars) ? lin : { vars: [] };
      renderVars();
    }).catch(() => {
      // graceful fallback: keep the plain vars table
      if (!stillHere()) return;
      lineage = null;
      lineageNote.style.display = "";
      renderVars();
    });
  };

  const hostRow = (h) => el("div", { class: "host-row", onclick: () => showHost(h) },
    el("span", { class: "dot" }),
    el("span", { class: "mono" }, h.name),
    el("span", { class: "hgroups" }, (h.groups || []).slice(0, 3).map((g) => el("span", { class: "chip" }, g))));

  // The tree shows groups only — hosts live in the right pane. This keeps a
  // facet-heavy inventory (where every host repeats across azure / tc_agent /
  // tcagent_azure …) to a handful of rows instead of a hundred host leaves.
  const buildTree = () => {
    left.innerHTML = "";
    const ul = el("ul");
    const sep = (label) => el("li", { class: "tree-sep" }, label);
    const groupRow = (g) => el("div", {
      class: "tree-row" + (g.name === "all" ? " tree-root" : ""),
      "data-group": g.name,
      onclick: () => showGroup(g),
    }, icon(g.name === "all" ? "globe" : "group"), el("span", null, g.name),
      g.constructed ? el("span", { class: "chip chip-constructed", title: "Generated by the ansible.builtin.constructed inventory plugin" }, "constructed") : null,
      el("span", { class: "cnt" }, String(groupHostCount(g))));
    const visit = (g, parentUl, depth, seen) => {
      if (seen.has(g.name) || depth > 12) return;
      seen.add(g.name);
      const li = el("li");
      li.appendChild(groupRow(g));
      // explicit declared children; `all` additionally parents every top group.
      let children = (g.children || []).map((c) => groupByName.get(c)).filter(Boolean);
      const sub = el("ul");
      for (const c of children) visit(c, sub, depth + 1, seen);
      if (g.name === "all") {
        if (declaredTop.length) for (const c of declaredTop) visit(c, sub, depth + 1, seen);
        if (facetTop.length) {
          sub.appendChild(sep("constructed facets"));
          for (const c of facetTop) visit(c, sub, depth + 1, seen);
        }
      }
      if (sub.childNodes.length) li.appendChild(sub);
      parentUl.appendChild(li);
    };
    const seen = new Set();
    if (allGroup) visit(allGroup, ul, 0, seen);
    else for (const g of roots) visit(g, ul, 0, seen);
    // groups unreachable from the root (cycles / no `all`) — append flat
    for (const g of groups) if (!seen.has(g.name)) visit(g, ul, 0, new Set([...seen].filter((x) => x !== g.name)));
    left.appendChild(ul);
    markSelection();
  };

  const markSelection = () => {
    $$(".tree-row", left).forEach((r) => {
      const match = selected &&
        ((selected.type === "group" && r.dataset.group === selected.name) ||
         (selected.type === "host" && r.dataset.host === selected.name));
      r.classList.toggle("selected", !!match);
    });
  };

  // search → flat host results in right pane
  searchIn.addEventListener("input", () => {
    const q = searchIn.value.trim().toLowerCase();
    if (!q) { if (allGroup) showGroup(allGroup); else if (roots.length) showGroup(roots[0]); return; }
    const matches = hosts.filter((h) =>
      h.name.toLowerCase().includes(q) ||
      (h.vars && h.vars.ansible_host && String(h.vars.ansible_host).includes(q)));
    selected = null; markSelection();
    right.innerHTML = "";
    right.appendChild(el("h3", { style: { margin: "0 0 10px" } }, `Hosts matching “${q}”`,
      el("span", { class: "chip", style: { marginLeft: "8px" } }, String(matches.length))));
    right.appendChild(matches.length
      ? el("div", null, matches.map((h) => hostRow(h)))
      : el("div", { class: "muted small" }, "No hosts match. Try a hostname or IP fragment."));
  });

  buildTree();
  if (allGroup) showGroup(allGroup);
  else if (roots.length) showGroup(roots[0]);
  else if (hosts.length) showHost(hosts[0]);
  else right.appendChild(el("div", { class: "empty" }, el("h3", null, "Empty inventory"), el("p", null, "No groups or hosts were parsed.")));
}

/** Candidate source files for an inventory whose path may be a dir or a file. */
function invSourceCandidates(inv) {
  const p = inv.path || "";
  const names = ["hosts", "hosts.ini", "hosts.yml", "hosts.yaml", "inventory.ini", "inventory.yml", "00-hosts.ini"];
  return [p, ...names.map((n) => (p ? `${p}/${n}` : n))];
}

/** Compact JSON-ish rendering of a variable value. */
function fmtVarValue(v) {
  if (v === null || v === undefined) return "null";
  return typeof v === "object" ? JSON.stringify(v, null, 1).replace(/\n\s*/g, " ") : String(v);
}

function varsTable(vars, query) {
  const q = (query || "").trim().toLowerCase();
  let entries = Object.entries(vars);
  if (q) entries = entries.filter(([k]) => k.toLowerCase().includes(q));
  if (!entries.length) {
    return el("div", { class: "muted small" }, q ? "No variables match." : "No variables.");
  }
  return el("table", { class: "vars-table" },
    el("tbody", null, entries.map(([k, v]) =>
      el("tr", null,
        el("td", { class: "k" }, k),
        el("td", { class: "v" }, fmtVarValue(v))))));
}

/* ---- variable lineage (where does each host var come from?) ---- */

/** Cached GET /api/repos/{id}/lineage for one inventory host. */
function getLineage(repoId, inventory, host) {
  const key = `${repoId}${inventory}${host}`;
  if (!State.lineageCache.has(key)) {
    const p = api(`/repos/${repoId}/lineage?inventory=${encodeURIComponent(inventory)}&host=${encodeURIComponent(host)}`)
      .catch((e) => { State.lineageCache.delete(key); throw e; });
    State.lineageCache.set(key, p);
  }
  return State.lineageCache.get(key);
}

function lineageScopeBadge(entry) {
  if (entry.scope === "role_default") {
    return el("span", { class: "scope-badge sc-default", title: `role default from “${entry.name}”` }, `default · ${entry.name}`);
  }
  if (entry.scope === "group") {
    return el("span", { class: "scope-badge sc-group", title: `group vars of “${entry.name}”` }, `group · ${entry.name}`);
  }
  return el("span", { class: "scope-badge sc-host", title: `host vars of “${entry.name}”` }, "host");
}

/** Lineage-aware vars list: each row expands to its precedence chain. */
function lineageVarsView(vars, query) {
  const q = (query || "").trim().toLowerCase();
  const list = q ? vars.filter((v) => v.key.toLowerCase().includes(q)) : vars;
  if (!list.length) {
    return el("div", { class: "muted small" }, q ? "No variables match." : "No variables.");
  }
  const box = el("div", { class: "lineage-table" });
  for (const v of list) {
    const chain = v.chain || [];
    const expandable = chain.length > 1;
    const row = el("div", { class: "lineage-row" });
    const head = el("div", { class: "lin-head" + (expandable ? " expandable" : "") },
      el("span", { class: "lin-caret" + (expandable ? "" : " none") }, "▸"),
      el("span", { class: "lin-key mono" }, v.key),
      el("span", { class: "lin-val mono" }, fmtVarValue(v.value)),
      expandable ? el("span", { class: "chip lin-levels", title: "value is layered — click to see the precedence chain" },
        `${chain.length} levels`) : null);
    row.appendChild(head);
    if (expandable) {
      let detail = null;
      head.addEventListener("click", () => {
        if (detail) { detail.remove(); detail = null; row.classList.remove("open"); return; }
        row.classList.add("open");
        detail = el("div", { class: "lin-chain" },
          chain.map((c, i) => {
            const last = i === chain.length - 1;
            return el("div", { class: "lin-chain-row" + (last ? " final" : " overridden") },
              lineageScopeBadge(c),
              el("span", { class: "lin-chain-val mono" }, fmtVarValue(c.value)),
              last
                ? el("span", { class: "chip green" }, "effective")
                : el("span", { class: "lin-arrow", title: "overridden by the next, higher-precedence level" }, "overridden ↓"));
          }));
        row.appendChild(detail);
      });
    }
    box.appendChild(row);
  }
  return box;
}

/* ============================================================
   4g. Topology — force-directed graph (canvas)
   ============================================================ */

async function pageTopology(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  page.appendChild(skeletonRows(1, 60));
  const scan = await getScan(repo.id);
  page.innerHTML = "";

  const inventories = scan.inventories || [];
  if (!inventories.length) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "No inventories to visualize"),
      el("p", null, `Add an inventory to ${repo.name} to see the host/group topology graph.`)));
    return;
  }

  let invName = State.invSelection.get(repo.id);
  if (!inventories.some((i) => i.name === invName)) invName = inventories[0].name;
  const inv = inventories.find((i) => i.name === invName);

  const invSel = el("select", { onchange: (e) => { State.invSelection.set(repo.id, e.target.value); route(); } },
    inventories.map((i) => el("option", { value: i.name, selected: i.name === invName || null }, i.name)));

  const tlBtn = el("button", {
    class: "btn btn-sm",
    title: "Replay how this inventory's topology evolved through git history",
  }, icon("timelapse"), "Time-lapse");

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Topology"),
    invSel,
    el("span", { class: "sub" }, "each bubble is a group · dots are hosts · wheel to zoom · drag to pan · double-click to fit"),
    el("div", { class: "grow" }),
    tlBtn));

  // --- what-if variables panel: preview the inventory with extra vars applied ---
  const varsEd = createVarsEditor();
  varsEd.setRepo(repo.id);
  const previewBtn = el("button", { class: "btn btn-primary btn-sm" }, "Preview");
  const resetBtn = el("button", { class: "btn btn-sm", disabled: true }, "Reset");
  const unknownNote = el("span", { class: "chip warn", style: { display: "none", cursor: "help" } });
  let whatifOpen = false;
  const whatifCaret = el("span", { class: "collapse-caret" }, "▸");
  const whatifBody = el("div", { class: "whatif-body", style: { display: "none" } },
    varsEd.root,
    el("div", { class: "row" }, previewBtn, resetBtn, unknownNote));
  page.appendChild(el("div", { class: "panel whatif-panel" },
    el("div", {
      class: "whatif-head",
      onclick: () => {
        whatifOpen = !whatifOpen;
        whatifCaret.classList.toggle("open", whatifOpen);
        whatifBody.style.display = whatifOpen ? "" : "none";
      },
    }, whatifCaret, el("span", null, "What-if variables"),
      el("span", { class: "muted small" }, "preview how variables would reshape constructed groups")),
    whatifBody));

  const wrap = el("div", { class: "topo-wrap" });
  const canvasBox = el("div", { class: "topo-canvas-box" });
  const legend = el("div", { class: "topo-legend" },
    el("span", { class: "li" }, el("span", { class: "sw", style: { background: "rgba(74,222,128,0.18)", border: "1.5px solid var(--accent)" } }), "group"),
    el("span", { class: "li" }, el("span", { class: "sw", style: { background: "rgba(34,211,238,0.1)", border: "1.5px dashed var(--secondary)" } }), "constructed group"),
    el("span", { class: "li" }, el("span", { class: "sw", style: { width: "8px", height: "8px", borderRadius: "50%", background: "var(--secondary)" } }), "host"),
    el("span", { class: "li" }, "bubble size = host count"));
  canvasBox.appendChild(legend);
  const sidePanel = el("div", { class: "topo-panel", style: { display: "none" } });
  canvasBox.appendChild(sidePanel);
  wrap.appendChild(canvasBox);
  page.appendChild(wrap);

  let topo;
  try {
    topo = await api(`/repos/${repo.id}/topology?inventory=${encodeURIComponent(invName)}`);
  } catch (e) {
    wrap.innerHTML = "";
    wrap.appendChild(el("div", { class: "empty" }, el("h3", null, "Could not load topology"), el("p", null, e.message)));
    return;
  }
  if (!((topo && topo.nodes) || []).length) {
    wrap.innerHTML = "";
    wrap.appendChild(el("div", { class: "empty" }, el("h3", null, "Empty topology"), el("p", null, "This inventory has no groups or hosts.")));
    return;
  }

  // (re-)render the cluster view; a fresh canvas per render keeps listeners clean
  let disposeView = null;
  const renderGraph = (topoData, invData) => {
    if (disposeView) { disposeView(); disposeView = null; }
    const old = canvasBox.querySelector("canvas");
    if (old) old.remove();
    const canvas = el("canvas");
    canvasBox.insertBefore(canvas, canvasBox.firstChild);
    sidePanel.style.display = "none";
    const nodes = (((topoData || {}).nodes) || []).map((n) => ({ ...n }));
    const hostVars = new Map((((invData || {}).hosts) || []).map((h) => [h.name, h]));
    disposeView = startClusterView({ canvas, canvasBox, nodes, hostVars, sidePanel });
  };
  renderGraph(topo, inv);

  // --- time-lapse: replay the inventory topology commit by commit ---
  let tl = null; // { frames, idx, playing, timer, bar }
  const exitTimelapse = () => {
    if (!tl) return;
    clearTimeout(tl.timer);
    tl.bar.remove();
    tl = null;
    tlBtn.disabled = false;
    renderGraph(topo, inv); // restore the live topology
  };
  onCleanup(() => { if (tl) clearTimeout(tl.timer); });

  const startTimelapse = (frames) => {
    const caption = el("span", { class: "tl-caption mono" });
    const counter = el("span", { class: "tl-count muted small mono" });
    const slider = el("input", {
      type: "range", min: "0", max: String(frames.length - 1), value: "0",
      class: "tl-slider", title: "Scrub through commits",
    });
    const prevBtn = el("button", { class: "btn btn-sm", title: "Previous commit" }, "⏮");
    const playBtn = el("button", { class: "btn btn-sm", title: "Play / pause (800ms per frame)" }, "⏯");
    const nextBtn = el("button", { class: "btn btn-sm", title: "Next commit" }, "⏭");
    const exitBtn = el("button", { class: "btn btn-sm", title: "Back to the live topology" }, "✕ Exit");
    const bar = el("div", { class: "timelapse-bar" }, prevBtn, playBtn, nextBtn, slider, counter, caption, exitBtn);
    canvasBox.appendChild(bar);
    tl = { frames, idx: 0, playing: false, timer: 0, bar };

    const show = (i) => {
      if (!tl) return;
      tl.idx = Math.max(0, Math.min(frames.length - 1, i));
      const f = frames[tl.idx];
      slider.value = String(tl.idx);
      counter.textContent = `${tl.idx + 1}/${frames.length}`;
      caption.textContent = `${f.commit} · ${f.message || "(no message)"} · ${relTime(f.date)} · ${f.hosts ?? 0} hosts / ${f.groups ?? 0} groups`;
      caption.title = caption.textContent;
      renderGraph(f.topology || {}, inv); // replaces only the canvas; the player bar stays
    };
    const stop = () => {
      if (!tl) return;
      tl.playing = false;
      clearTimeout(tl.timer);
      playBtn.classList.remove("active");
    };
    const tick = () => {
      if (!tl || !tl.playing) return;
      if (tl.idx >= frames.length - 1) { stop(); return; }
      show(tl.idx + 1);
      tl.timer = setTimeout(tick, 800);
    };
    const togglePlay = () => {
      if (!tl) return;
      if (tl.playing) { stop(); return; }
      tl.playing = true;
      playBtn.classList.add("active");
      if (tl.idx >= frames.length - 1) show(0); // restart from the oldest frame
      tl.timer = setTimeout(tick, 800);
    };
    playBtn.onclick = togglePlay;
    prevBtn.onclick = () => { stop(); show(tl.idx - 1); };
    nextBtn.onclick = () => { stop(); show(tl.idx + 1); };
    slider.oninput = () => { stop(); show(Number(slider.value)); };
    exitBtn.onclick = exitTimelapse;
    show(0);
    togglePlay();
  };

  tlBtn.onclick = async () => {
    tlBtn.disabled = true;
    let res;
    try {
      res = await api(`/repos/${repo.id}/timelapse?inventory=${encodeURIComponent(invName)}&limit=30`);
    } catch (e) {
      // typically 400 — not a git repository; keep the button disabled with the reason
      tlBtn.title = e.message;
      toast(e.message, "error", "Time-lapse unavailable");
      return;
    }
    const frames = (res && res.frames) || [];
    if (!frames.length) {
      tlBtn.disabled = false;
      toast("No history frames found for this inventory.", "error", "Time-lapse");
      return;
    }
    startTimelapse(frames);
  };

  previewBtn.onclick = async () => {
    previewBtn.disabled = true;
    if (tl) exitTimelapse(); // what-if preview always applies to the live topology
    try {
      varsEd.persist();
      const res = await api(`/repos/${repo.id}/inventory-preview`, {
        method: "POST",
        body: JSON.stringify({
          inventory: invName,
          vars: varsEd.getVars(),
          host_vars: {},
          fact_profile: varsEd.getProfile(),
        }),
      });
      renderGraph(res.topology || {}, res.inventory || inv);
      resetBtn.disabled = false;
      let count = 0;
      const lines = [];
      for (const [g, hostsMap] of Object.entries(res.unknown_groups || {})) {
        for (const [h, miss] of Object.entries(hostsMap || {})) {
          count++;
          lines.push(`${h} ∈ ${g}? — missing ${(miss || []).join(", ") || "vars"}`);
        }
      }
      unknownNote.style.display = count ? "" : "none";
      unknownNote.textContent = `${count} membership${count === 1 ? "" : "s"} unknown`;
      unknownNote.title = lines.join("\n");
    } catch (e) {
      toast(e.message, "error", "Preview failed");
    } finally {
      previewBtn.disabled = false;
    }
  };
  resetBtn.onclick = () => {
    renderGraph(topo, inv);
    resetBtn.disabled = true;
    unknownNote.style.display = "none";
  };
}

function startClusterView({ canvas, canvasBox, nodes, hostVars, sidePanel }) {
  const ctx = canvas.getContext("2d");
  const dpr = window.devicePixelRatio || 1;
  let W = 0, H = 0;
  const disposers = []; // window-level listeners; removed on dispose/navigation

  // --- build clusters: bucket each host into its primary (most-specific) group ---
  const groupByLabel = new Map(nodes.filter((n) => n.type === "group").map((g) => [g.label, g]));
  const buckets = new Map();
  for (const h of nodes.filter((n) => n.type === "host")) {
    const key = h.group || "(ungrouped)";
    if (!buckets.has(key)) buckets.set(key, []);
    buckets.get(key).push(h);
  }
  const clusters = [...buckets.entries()].map(([name, hosts]) => ({
    name, hosts, group: groupByLabel.get(name) || null,
  }));
  clusters.sort((a, b) => b.hosts.length - a.hosts.length || a.name.localeCompare(b.name));

  // --- inner layout: host dots on a centered grid; bubble radius encloses them ---
  const DOT = 4.5, CELL = DOT * 2 + 5.5;
  for (const c of clusters) {
    const n = c.hosts.length;
    const cols = Math.max(1, Math.ceil(Math.sqrt(n)));
    const rows = Math.ceil(n / cols);
    c.hosts.forEach((h, i) => {
      h.dx = ((i % cols) - (cols - 1) / 2) * CELL;
      h.dy = (Math.floor(i / cols) - (rows - 1) / 2) * CELL;
    });
    c.r = Math.max(Math.hypot((cols - 1) * CELL, (rows - 1) * CELL) / 2 + DOT + 14, 30);
  }

  // --- bubble layout: deterministic row-flow, largest first, never overlapping ---
  const LABEL = 24, PAD = 32;
  const totalArea = clusters.reduce((s, c) => s + Math.PI * c.r * c.r, 0);
  const targetW = Math.max(Math.sqrt(totalArea) * 2.6, 360);
  let cx = 0, cy = 0, rowH = 0;
  for (const c of clusters) {
    const w = c.r * 2 + PAD;
    if (cx > 0 && cx + w > targetW) { cx = 0; cy += rowH; rowH = 0; }
    c.cx = cx + c.r + PAD / 2;
    c.cy = cy + c.r + PAD / 2 + LABEL;
    cx += w;
    rowH = Math.max(rowH, c.r * 2 + PAD + LABEL);
  }
  for (const c of clusters) for (const h of c.hosts) { h.x = c.cx + h.dx; h.y = c.cy + h.dy; }

  // view transform
  let scale = 1, tx = 0, ty = 0;
  let hovered = null, hoveredCluster = null;
  let panning = false, userInteracted = false;
  let lastMouse = { x: 0, y: 0 };
  let raf = 0, stopped = false;

  function fitView() {
    let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
    for (const c of clusters) {
      minX = Math.min(minX, c.cx - c.r);
      minY = Math.min(minY, c.cy - c.r - LABEL);
      maxX = Math.max(maxX, c.cx + c.r);
      maxY = Math.max(maxY, c.cy + c.r);
    }
    if (!Number.isFinite(minX) || W === 0 || H === 0) return;
    const gw = Math.max(maxX - minX, 1), gh = Math.max(maxY - minY, 1);
    const pad = 70;
    scale = Math.max(Math.min((W - pad) / gw, (H - pad) / gh, 1.6), 0.05);
    tx = W / 2 - ((minX + maxX) / 2) * scale;
    ty = H / 2 - ((minY + maxY) / 2) * scale;
  }

  const resize = () => {
    W = canvasBox.clientWidth; H = canvasBox.clientHeight;
    canvas.width = W * dpr; canvas.height = H * dpr;
    canvas.style.width = W + "px"; canvas.style.height = H + "px";
    if (!userInteracted) fitView();
    requestDraw();
  };

  const toWorld = (px, py) => ({ x: (px - tx) / scale, y: (py - ty) / scale });

  function draw() {
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, W, H);
    ctx.translate(tx, ty);
    ctx.scale(scale, scale);

    for (const c of clusters) {
      const hot = c === hoveredCluster || (hovered && hovered.group === c.name);
      const constructed = !!(c.group && c.group.constructed);
      ctx.beginPath();
      ctx.arc(c.cx, c.cy, c.r, 0, Math.PI * 2);
      ctx.fillStyle = constructed
        ? (hot ? "rgba(34,211,238,0.13)" : "rgba(34,211,238,0.05)")
        : (hot ? "rgba(74,222,128,0.14)" : "rgba(74,222,128,0.06)");
      ctx.fill();
      ctx.strokeStyle = constructed
        ? (hot ? "#22d3ee" : "rgba(34,211,238,0.55)")
        : (hot ? "#4ade80" : "rgba(74,222,128,0.38)");
      ctx.lineWidth = (hot ? 2 : 1.3) / scale;
      if (constructed) ctx.setLineDash([6 / scale, 4 / scale]);
      ctx.stroke();
      ctx.setLineDash([]);
      for (const h of c.hosts) {
        ctx.beginPath();
        ctx.arc(h.x, h.y, h === hovered ? DOT + 1.5 : DOT, 0, Math.PI * 2);
        ctx.fillStyle = h === hovered ? "#e7efe9" : "#22d3ee";
        ctx.fill();
      }
      ctx.textAlign = "center";
      ctx.fillStyle = constructed
        ? (hot ? "#9be8f4" : "#6cb8c6")
        : (hot ? "#bdf3cf" : "#8fcfa6");
      ctx.font = `600 ${12.5 / scale}px ui-monospace, Menlo, monospace`;
      ctx.fillText(`${c.name}  ${c.hosts.length}`, c.cx, c.cy - c.r - 7 / scale);
    }

    if (hovered) {
      ctx.textAlign = "left";
      ctx.font = `600 ${11 / scale}px ui-monospace, Menlo, monospace`;
      const label = hovered.label;
      const tw = ctx.measureText(label).width;
      const px = hovered.x + DOT + 5 / scale, py = hovered.y - DOT - 5 / scale;
      ctx.fillStyle = "rgba(8,12,10,0.92)";
      ctx.fillRect(px - 3 / scale, py - 12 / scale, tw + 8 / scale, 17 / scale);
      ctx.fillStyle = "#e7efe9";
      ctx.fillText(label, px + 1 / scale, py);
    }
  }

  function requestDraw() {
    if (raf || stopped) return;
    raf = requestAnimationFrame(() => { raf = 0; draw(); });
  }

  const dispose = () => {
    stopped = true;
    if (raf) cancelAnimationFrame(raf);
    for (const fn of disposers.splice(0)) { try { fn(); } catch { /* noop */ } }
  };
  resize();
  window.addEventListener("resize", resize);
  disposers.push(() => window.removeEventListener("resize", resize));
  onCleanup(dispose);
  canvas.style.cursor = "grab";

  function hostAt(px, py) {
    const p = toWorld(px, py);
    let best = null, bestD = Infinity;
    for (const c of clusters) {
      if (Math.hypot(c.cx - p.x, c.cy - p.y) > c.r + 8) continue;
      for (const h of c.hosts) {
        const d = Math.hypot(h.x - p.x, h.y - p.y);
        if (d < Math.max(DOT + 3, 7 / scale) && d < bestD) { best = h; bestD = d; }
      }
    }
    return best;
  }
  function clusterAt(px, py) {
    const p = toWorld(px, py);
    let best = null, bestD = Infinity;
    for (const c of clusters) {
      const d = Math.hypot(c.cx - p.x, c.cy - p.y);
      if (d < c.r && d < bestD) { best = c; bestD = d; }
    }
    return best;
  }

  canvas.addEventListener("mousedown", (e) => {
    userInteracted = true;
    const rect = canvas.getBoundingClientRect();
    lastMouse = { x: e.clientX - rect.left, y: e.clientY - rect.top };
    panning = true;
    canvas.classList.add("dragging");
  });
  window.addEventListener("mousemove", onMove);
  disposers.push(() => window.removeEventListener("mousemove", onMove));
  function onMove(e) {
    const rect = canvas.getBoundingClientRect();
    const px = e.clientX - rect.left, py = e.clientY - rect.top;
    if (panning) {
      tx += px - lastMouse.x; ty += py - lastMouse.y;
      lastMouse = { x: px, y: py };
      requestDraw();
      return;
    }
    const h = hostAt(px, py);
    const c = h ? null : clusterAt(px, py);
    if (h !== hovered || c !== hoveredCluster) {
      hovered = h; hoveredCluster = c;
      canvas.style.cursor = (h || c) ? "pointer" : "grab";
      requestDraw();
    }
  }
  window.addEventListener("mouseup", onUp);
  disposers.push(() => window.removeEventListener("mouseup", onUp));
  function onUp() { panning = false; canvas.classList.remove("dragging"); }

  canvas.addEventListener("click", (e) => {
    const rect = canvas.getBoundingClientRect();
    const px = e.clientX - rect.left, py = e.clientY - rect.top;
    const h = hostAt(px, py);
    if (h) { showPanel({ type: "host", node: h }); return; }
    const c = clusterAt(px, py);
    if (c) { showPanel({ type: "group", cluster: c }); return; }
    sidePanel.style.display = "none";
  });

  canvas.addEventListener("dblclick", (e) => {
    const rect = canvas.getBoundingClientRect();
    if (hostAt(e.clientX - rect.left, e.clientY - rect.top)) return;
    userInteracted = false; fitView(); requestDraw();
  });

  canvas.addEventListener("wheel", (e) => {
    e.preventDefault();
    userInteracted = true;
    const rect = canvas.getBoundingClientRect();
    const px = e.clientX - rect.left, py = e.clientY - rect.top;
    const factor = e.deltaY < 0 ? 1.12 : 1 / 1.12;
    const ns = Math.min(Math.max(scale * factor, 0.05), 6);
    tx = px - ((px - tx) / scale) * ns;
    ty = py - ((py - ty) / scale) * ns;
    scale = ns;
    requestDraw();
  }, { passive: false });

  function showPanel(sel) {
    sidePanel.style.display = "";
    sidePanel.innerHTML = "";
    const closeBtn = el("button", { class: "x", style: { background: "none", border: "none", color: "var(--muted)", cursor: "pointer" }, onclick: () => (sidePanel.style.display = "none") }, "✕");
    if (sel.type === "host") {
      const h = sel.node;
      const hv = hostVars.get(h.label);
      const groups = hv && hv.groups ? hv.groups : [];
      sidePanel.appendChild(el("h3", null,
        el("span", { class: "sw", style: { width: "10px", height: "10px", borderRadius: "50%", display: "inline-block", background: "var(--secondary)" } }),
        h.label, el("span", { style: { flex: 1 } }), closeBtn));
      sidePanel.appendChild(el("div", { class: "muted small", style: { marginBottom: "10px" } },
        `host · member of ${groups.length} group${groups.length === 1 ? "" : "s"}`));
      if (groups.length) {
        sidePanel.appendChild(el("div", { class: "row", style: { marginBottom: "10px", gap: "5px" } },
          groups.map((g) => el("span", { class: "chip" }, g))));
      }
      const vars = hv ? hv.vars || {} : {};
      sidePanel.appendChild(el("div", { class: "section-title", style: { margin: "10px 0 8px" } }, "Vars"));
      sidePanel.appendChild(Object.keys(vars).length ? varsTable(vars) : el("div", { class: "muted small" }, "No host vars."));
    } else {
      const c = sel.cluster;
      sidePanel.appendChild(el("h3", null,
        el("span", { class: "sw", style: { width: "10px", height: "10px", borderRadius: "3px", display: "inline-block", background: "var(--accent)" } }),
        c.name, el("span", { style: { flex: 1 } }), closeBtn));
      sidePanel.appendChild(el("div", { class: "muted small", style: { marginBottom: "10px" } },
        `group · ${c.hosts.length} host${c.hosts.length === 1 ? "" : "s"} here`));
      sidePanel.appendChild(el("div", { class: "section-title", style: { margin: "10px 0 8px" } }, "Hosts"));
      sidePanel.appendChild(el("div", { class: "row", style: { gap: "5px" } },
        c.hosts.slice(0, 60).map((h) => el("span", {
          class: "chip", style: { color: "var(--secondary)", cursor: "pointer" },
          onclick: () => showPanel({ type: "host", node: h }),
        }, h.label))));
      if (c.hosts.length > 60) {
        sidePanel.appendChild(el("div", { class: "muted small", style: { marginTop: "6px" } }, `+${c.hosts.length - 60} more`));
      }
    }
  }

  return dispose;
}

/* ============================================================
   4h. Hygiene — repo health report
   ============================================================ */

/** SVG score ring (0-100) tinted by tone: good ≥90, warn ≥70, bad below. */
function hygieneScoreRing(score, tone) {
  const ns = "http://www.w3.org/2000/svg";
  const r = 30, c = 2 * Math.PI * r;
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 76 76");
  svg.setAttribute("class", "score-ring " + tone);
  const bg = document.createElementNS(ns, "circle");
  bg.setAttribute("cx", "38"); bg.setAttribute("cy", "38"); bg.setAttribute("r", String(r));
  bg.setAttribute("class", "ring-bg");
  const fg = document.createElementNS(ns, "circle");
  fg.setAttribute("cx", "38"); fg.setAttribute("cy", "38"); fg.setAttribute("r", String(r));
  fg.setAttribute("class", "ring-fg");
  fg.setAttribute("stroke-dasharray", `${(c * score / 100).toFixed(1)} ${c.toFixed(1)}`);
  fg.setAttribute("transform", "rotate(-90 38 38)");
  const txt = document.createElementNS(ns, "text");
  txt.setAttribute("x", "38"); txt.setAttribute("y", "44");
  txt.setAttribute("text-anchor", "middle");
  txt.setAttribute("class", "ring-num");
  txt.textContent = String(score);
  svg.appendChild(bg); svg.appendChild(fg); svg.appendChild(txt);
  return svg;
}

function hygieneRow(ic, name, reason, ...extras) {
  return el("div", { class: "hy-row" },
    ic,
    el("span", { class: "mono hy-name" }, name),
    reason ? el("span", { class: "muted small hy-reason" }, reason) : null,
    el("div", { class: "grow" }),
    extras);
}

async function pageHygiene(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Hygiene"),
    el("span", { class: "sub" }, `repository health for ${repo.name}`)));

  const box = el("div");
  page.appendChild(box);
  box.appendChild(skeletonRows(3, 90));

  let hy;
  try {
    hy = await api(`/repos/${repo.id}/hygiene`);
  } catch (e) {
    box.innerHTML = "";
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "Could not compute hygiene"),
      el("p", null, e.message),
      el("button", { class: "btn", onclick: route }, "Retry")));
    toast(e.message, "error", "Hygiene failed");
    return;
  }
  box.innerHTML = "";

  const score = Math.max(0, Math.min(100, Math.round(hy.score ?? 0)));
  const tone = score >= 90 ? "good" : score >= 70 ? "warn" : "bad";
  const vaults = hy.vault_files ?? 0;

  box.appendChild(el("div", { class: "panel hygiene-head" },
    hygieneScoreRing(score, tone),
    el("div", null,
      el("div", { class: "hh-title" }, "Repository hygiene score"),
      el("div", { class: "muted small", style: { maxWidth: "520px" } },
        "Static analysis of unused roles, never-notified handlers, dead variables, untargeted hosts and plaintext secrets.")),
    el("div", { class: "grow" }),
    el("span", { class: "chip" + (vaults > 0 ? " green" : "") },
      `${vaults} vault-encrypted file${vaults === 1 ? "" : "s"}`)));

  const sections = [
    {
      key: "unused_roles", title: "Unused roles", ok: "No unused roles", icon: "role",
      row: (f) => hygieneRow(icon("role"), f.name, f.reason),
    },
    {
      key: "unnotified_handlers", title: "Unnotified handlers", ok: "No unnotified handlers", icon: "bell",
      row: (f) => hygieneRow(icon("bell"), f.name, f.reason,
        f.role ? el("span", { class: "chip" }, `role · ${f.role}`) : null),
    },
    {
      key: "unused_vars", title: "Unused variables", ok: "No unused variables", icon: "code",
      row: (f) => hygieneRow(icon("code"), f.key, null,
        f.defined_in ? el("span", { class: "chip mono" }, f.defined_in) : null),
    },
    {
      key: "untargeted_hosts", title: "Untargeted hosts", ok: "No untargeted hosts", icon: "host",
      row: (f) => hygieneRow(icon("host"), f.name, f.reason,
        f.inventory ? el("span", { class: "chip", style: { color: "var(--secondary)" } }, f.inventory) : null),
    },
    {
      key: "secret_findings", title: "Plaintext secrets", ok: "No plaintext secrets", icon: "search",
      row: (f) => hygieneRow(icon("search"), f.key, f.reason,
        f.hint ? el("span", { class: "hy-hint" }, icon("question"), f.hint) : null,
        f.file ? el("span", { class: "chip mono" }, f.file) : null,
        el("span", { class: `pill sev-${f.severity === "high" ? "high" : "med"}` }, f.severity || "medium")),
    },
  ];

  const total = sections.reduce((n, s) => n + (hy[s.key] || []).length, 0);
  if (!total) {
    box.appendChild(el("div", { class: "hero" },
      el("div", { html: ICONS.sparkle.replace("<svg", '<svg class="tree" style="color:var(--accent)"') }),
      el("h2", null, "Your repo is tidy"),
      el("p", null, "Every role is referenced, every handler gets notified, no dead variables, no untargeted hosts, no plaintext secrets. Keep it that way.")));
    return;
  }

  for (const s of sections) {
    const findings = hy[s.key] || [];
    if (!findings.length) {
      box.appendChild(el("div", { class: "hy-ok" }, el("span", { class: "hy-check" }, "✓"), s.ok));
      continue;
    }
    box.appendChild(el("div", { class: "card hy-section" },
      el("div", { class: "hy-sec-head" },
        icon(s.icon),
        el("span", { class: "hy-sec-title" }, s.title),
        el("span", { class: "chip warn" }, String(findings.length))),
      findings.map(s.row)));
  }
}

/* ============================================================
   4i. Impact — blast radius of a change (Files → Roles →
       Playbooks → Hosts ripple flow)
   ============================================================ */

async function pageImpact(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  const baseIn = el("input", { type: "text", placeholder: "HEAD~1", spellcheck: "false", style: { width: "150px" } });
  const headIn = el("input", { type: "text", placeholder: "HEAD", spellcheck: "false", style: { width: "150px" } });
  const analyzeBtn = el("button", { class: "btn btn-primary btn-sm" }, icon("radar"), "Analyze");
  const presetWorktree = el("button", { class: "btn btn-sm", title: "Uncommitted changes vs HEAD" }, "Uncommitted");
  const presetLast = el("button", { class: "btn btn-sm", title: "HEAD~1 → HEAD" }, "Last commit");

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Impact"),
    el("span", { class: "sub" }, `blast radius of changes in ${repo.name}`)));

  page.appendChild(el("div", { class: "panel impact-bar" },
    el("span", { class: "muted small" }, "base"), baseIn,
    el("span", { class: "muted small" }, "head"), headIn,
    analyzeBtn,
    el("span", { class: "muted small", style: { marginLeft: "10px" } }, "presets:"),
    presetWorktree, presetLast));

  const resultBox = el("div");
  page.appendChild(resultBox);

  let redraw = null;
  const onResize = () => { if (redraw) redraw(); };
  window.addEventListener("resize", onResize);
  onCleanup(() => window.removeEventListener("resize", onResize));

  const analyze = async () => {
    redraw = null;
    resultBox.innerHTML = "";
    resultBox.appendChild(skeletonRows(2, 120));
    analyzeBtn.disabled = true;
    const params = new URLSearchParams();
    if (baseIn.value.trim()) params.set("base", baseIn.value.trim());
    if (headIn.value.trim()) params.set("head", headIn.value.trim());
    const qs = params.toString();
    try {
      const imp = await api(`/repos/${repo.id}/impact${qs ? "?" + qs : ""}`);
      resultBox.innerHTML = "";
      redraw = renderImpact(resultBox, repo, imp);
    } catch (e) {
      resultBox.innerHTML = "";
      resultBox.appendChild(el("div", { class: "empty" },
        el("h3", null, "Impact analysis failed"),
        el("p", null, e.message)));
      toast(e.message, "error", "Impact failed");
    } finally {
      analyzeBtn.disabled = false;
    }
  };
  analyzeBtn.onclick = analyze;
  presetWorktree.onclick = () => { baseIn.value = ""; headIn.value = ""; analyze(); };
  presetLast.onclick = () => { baseIn.value = "HEAD~1"; headIn.value = "HEAD"; analyze(); };
  for (const input of [baseIn, headIn]) {
    input.addEventListener("keydown", (e) => { if (e.key === "Enter") analyze(); });
  }

  analyze(); // default: worktree mode (uncommitted vs HEAD)
}

/**
 * Render the impact result: summary strip + 4-column ripple flow with
 * bezier links (same technique as the playbook notify arrows).
 * Returns the link-redraw function (called again on window resize).
 */
function renderImpact(box, repo, imp) {
  const files = imp.changed_files || [];
  const entries = imp.entries || [];
  const hosts = imp.hosts || [];
  const sum = imp.summary || {};

  if (!files.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No changes detected"),
      el("p", null, `Nothing differs between ${imp.base || "base"} and ${imp.head || "head"}.`)));
    return null;
  }

  // ---- summary strip ----
  const handlers = sum.handlers || [];
  box.appendChild(el("div", { class: "panel impact-summary" },
    el("span", { class: "chip mono" }, `${imp.base || "?"} → ${imp.head || "?"}`),
    el("span", { class: "sum-chip" }, el("b", null, String(sum.files ?? files.length)), el("span", null, "files")),
    el("span", { class: "imp-arrow" }, "→"),
    el("span", { class: "sum-chip" }, el("b", { style: { color: "var(--purple)" } }, String(sum.roles ?? 0)), el("span", null, "roles")),
    el("span", { class: "imp-arrow" }, "→"),
    el("span", { class: "sum-chip" }, el("b", { style: { color: "var(--secondary)" } }, String(sum.playbooks ?? 0)), el("span", null, "playbooks")),
    el("span", { class: "imp-arrow" }, "→"),
    el("span", { class: "sum-chip" }, el("b", { style: { color: "var(--accent)" } }, String(sum.hosts_total ?? hosts.length)), el("span", null, "hosts")),
    handlers.map((h) => el("span", { class: "chip warn", title: "a change to a notifying task or template would fire this handler" },
      icon("bell"), `would trigger: ${h}`))));

  // ---- graph model: nodes per column + directed edges ----
  const nodes = new Map(); // id -> {id, col, label, ...extra}
  const edges = [];
  const edgeSeen = new Set();
  const addNode = (id, col, label, extra) => {
    if (!nodes.has(id)) nodes.set(id, { id, col, label, ...(extra || {}) });
    return nodes.get(id);
  };
  const addEdge = (from, to) => {
    const k = from + "" + to;
    if (edgeSeen.has(k)) return;
    edgeSeen.add(k);
    edges.push({ from, to });
  };

  for (const en of entries) {
    const fid = "f:" + en.file;
    addNode(fid, 0, en.file, { kind: en.kind, handlers: en.handlers || [] });
    for (const r of en.roles || []) {
      addNode("r:" + r, 1, r);
      addEdge(fid, "r:" + r);
    }
    for (const pb of en.playbooks || []) {
      const pid = "p:" + pb.path;
      addNode(pid, 2, pb.path, { via: new Set() });
      if (pb.via) nodes.get(pid).via.add(pb.via);
      const m = /^role (.+)$/.exec(pb.via || "");
      if (m) {
        addNode("r:" + m[1], 1, m[1]);
        addEdge(fid, "r:" + m[1]);
        addEdge("r:" + m[1], pid);
      } else {
        addEdge(fid, pid);
      }
    }
  }
  const byInv = new Map();
  for (const h of hosts) {
    const inv = h.inventory || "(no inventory)";
    if (!byInv.has(inv)) byInv.set(inv, []);
    byInv.get(inv).push(h);
  }
  for (const [inv, list] of byInv) {
    const iid = "i:" + inv;
    addNode(iid, 3, inv, { hosts: list });
    for (const h of list) {
      for (const via of h.via || []) {
        if (nodes.has("p:" + via)) addEdge("p:" + via, iid);
      }
    }
  }

  // ---- adjacency for hover highlighting (full up/downstream chain) ----
  const out = new Map(), inn = new Map();
  for (const e of edges) {
    if (!out.has(e.from)) out.set(e.from, []);
    out.get(e.from).push(e.to);
    if (!inn.has(e.to)) inn.set(e.to, []);
    inn.get(e.to).push(e.from);
  }
  const chainOf = (id) => {
    const set = new Set([id]);
    const walk = (adj) => {
      const stack = [id];
      while (stack.length) {
        const cur = stack.pop();
        for (const nxt of adj.get(cur) || []) {
          if (!set.has(nxt)) { set.add(nxt); stack.push(nxt); }
        }
      }
    };
    walk(out); walk(inn);
    return set;
  };

  // ---- DOM: 4 columns + svg link layer ----
  const flow = el("div", { class: "impact-flow" });
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("class", "impact-svg");
  flow.appendChild(svg);
  const cardEls = new Map(); // node id -> element

  function drawLinks() {
    if (!flow.isConnected) return;
    svg.innerHTML = "";
    const rect = flow.getBoundingClientRect();
    svg.setAttribute("viewBox", `0 0 ${rect.width} ${rect.height}`);
    svg.setAttribute("width", rect.width);
    svg.setAttribute("height", rect.height);
    for (const e of edges) {
      const a = cardEls.get(e.from), b = cardEls.get(e.to);
      if (!a || !b) continue;
      const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect();
      const x1 = ra.right - rect.left, y1 = ra.top + ra.height / 2 - rect.top;
      const x2 = rb.left - rect.left, y2 = rb.top + rb.height / 2 - rect.top;
      const mx = (x1 + x2) / 2;
      const path = document.createElementNS(ns, "path");
      path.setAttribute("d", `M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`);
      path.setAttribute("class", "impact-link");
      path.dataset.from = e.from;
      path.dataset.to = e.to;
      svg.appendChild(path);
    }
  }

  const setHover = (id) => {
    flow.classList.toggle("hovering", !!id);
    const chain = id ? chainOf(id) : null;
    for (const [nid, card] of cardEls) card.classList.toggle("hl", !!(chain && chain.has(nid)));
    for (const p of svg.querySelectorAll(".impact-link")) {
      p.classList.toggle("hl", !!(chain && chain.has(p.dataset.from) && chain.has(p.dataset.to)));
    }
  };

  const cardFor = (n) => {
    let card;
    if (n.col === 0) {
      card = el("div", { class: "impact-card" },
        el("div", { class: "mono imp-file" }, n.label),
        el("div", { class: "row", style: { gap: "5px", marginTop: "6px", flexWrap: "wrap" } },
          n.kind ? el("span", { class: "chip imp-kind" }, n.kind) : null,
          (n.handlers || []).map((h) => el("span", { class: "chip warn", title: "handler this change would trigger" }, h))));
    } else if (n.col === 1) {
      card = el("div", {
        class: "impact-card clickable",
        onclick: () => (location.hash = `#/role/${repo.id}/${encodeURIComponent(n.label)}`),
      }, el("div", { class: "imp-name" }, icon("role"), n.label));
    } else if (n.col === 2) {
      const via = [...(n.via || [])];
      card = el("div", { class: "impact-card" },
        el("div", { class: "imp-name mono" }, n.label),
        via.length ? el("div", { class: "muted small", style: { marginTop: "2px" } }, "via " + via.join(", ")) : null,
        el("div", { class: "row", style: { gap: "6px", marginTop: "8px" } },
          el("button", {
            class: "btn btn-sm btn-secondary", title: "Plan this playbook",
            onclick: (e) => { e.stopPropagation(); openRunModal({ repoId: repo.id, playbook: n.label, plan: true }); },
          }, icon("clipboard"), "Plan"),
          el("button", {
            class: "btn btn-sm btn-primary", title: "Run this playbook",
            onclick: (e) => { e.stopPropagation(); openRunModal({ repoId: repo.id, playbook: n.label }); },
          }, icon("play"), "Run")));
    } else {
      const count = (sum.hosts_by_inventory || {})[n.label] ?? (n.hosts || []).length;
      const caret = el("span", { class: "lin-caret" }, "▸");
      const list = el("div", { class: "imp-host-list", style: { display: "none" } },
        (n.hosts || []).map((h) => el("div", { class: "imp-host mono", title: "via " + ((h.via || []).join(", ") || "—") }, h.name)));
      let open = false;
      card = el("div", { class: "impact-card" },
        el("div", {
          class: "imp-name imp-inv-head",
          onclick: () => {
            open = !open;
            caret.classList.toggle("open", open);
            list.style.display = open ? "" : "none";
            requestAnimationFrame(drawLinks); // heights changed
          },
        }, caret, icon("host"), el("span", null, `${n.label} · ${count} host${count === 1 ? "" : "s"}`)),
        list);
    }
    card.addEventListener("mouseenter", () => setHover(n.id));
    card.addEventListener("mouseleave", () => setHover(null));
    cardEls.set(n.id, card);
    return card;
  };

  const colNodes = [[], [], [], []];
  for (const n of nodes.values()) colNodes[n.col].push(n);
  for (const col of [1, 2, 3]) colNodes[col].sort((a, b) => a.label.localeCompare(b.label));

  ["Files", "Roles", "Playbooks", "Hosts"].forEach((title, i) => {
    const colEl = el("div", { class: "impact-col" }, el("div", { class: "impact-col-title" }, title));
    if (!colNodes[i].length) colEl.appendChild(el("div", { class: "col-empty" }, "none"));
    for (const n of colNodes[i]) colEl.appendChild(cardFor(n));
    flow.appendChild(colEl);
  });
  box.appendChild(flow);

  requestAnimationFrame(() => requestAnimationFrame(drawLinks));
  return drawLinks;
}

/* ============================================================
   4j-1. Drift — playbook × host heatmap from --check runs
   ============================================================ */

async function pageDrift(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Drift"),
    el("span", { class: "sub" }, `where reality diverges from ${repo.name}`),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: () => openDriftCheckModal(repo) },
      icon("play"), "Run drift check")));

  const box = el("div");
  page.appendChild(box);
  box.appendChild(skeletonRows(3, 80));

  let drift;
  try {
    drift = await api(`/repos/${repo.id}/drift`);
  } catch (e) {
    box.innerHTML = "";
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "Could not load drift"),
      el("p", null, e.message),
      el("button", { class: "btn", onclick: route }, "Retry")));
    toast(e.message, "error", "Drift failed");
    return;
  }
  box.innerHTML = "";

  const playbooks = (drift && drift.playbooks) || [];
  const hosts = (drift && drift.hosts) || [];
  const sum = (drift && drift.summary) || {};

  if (!playbooks.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No drift data yet"),
      el("p", null, "No drift data yet — run a drift check (--check jobs) to see how reality diverges from your repo."),
      el("button", { class: "btn btn-primary", onclick: () => openDriftCheckModal(repo) },
        icon("play"), "Run drift check")));
    return;
  }

  // summary strip
  const drifted = sum.hosts_with_drift ?? 0;
  box.appendChild(el("div", { class: "panel drift-summary" },
    el("span", { class: "sum-chip" },
      el("b", null, String(sum.checked_playbooks ?? playbooks.length)), el("span", null, "playbooks checked")),
    el("span", { class: "sum-chip " + (drifted > 0 ? "failed" : "ok") },
      el("b", null, String(drifted)), el("span", null, drifted === 1 ? "host with drift" : "hosts with drift")),
    el("span", { class: "sum-chip changed" },
      el("b", null, String(sum.total_changed ?? 0)), el("span", null, "changed tasks")),
    el("div", { class: "grow" }),
    el("span", { class: "muted small", title: sum.last_checked || "" }, `last checked ${relTime(sum.last_checked)}`)));

  // heatmap cell: colored by changed count, red border on failures
  const cellFor = (pb, host) => {
    const cell = (pb.hosts || {})[host];
    if (!cell) {
      return el("td", { class: "drift-cell" },
        el("span", { class: "drift-na", title: `${host} was not part of the last --check run of ${pb.playbook}` }, "·"));
    }
    const changed = cell.changed || 0;
    const failed = cell.failed || 0;
    const sev = changed >= 3 ? "high" : changed > 0 ? "mid" : "none";
    const tip = [
      `${pb.playbook} × ${host} — ${changed} changed · ${failed} failed`,
      ...(cell.tasks || []).map((t) => "· " + t),
    ].join("\n");
    return el("td", { class: "drift-cell" },
      el("button", {
        class: `drift-dot sev-${sev}` + (failed > 0 ? " has-failed" : ""),
        title: tip,
        onclick: () => openDriftCellModal(pb, host, cell),
      }, changed > 0 ? String(changed) : ""));
  };

  box.appendChild(el("div", { class: "table-wrap drift-heatmap" },
    el("table", { class: "data" },
      el("thead", null, el("tr", null,
        el("th", null, "Playbook"),
        hosts.map((h) => el("th", { class: "drift-host-th mono" }, h)))),
      el("tbody", null, playbooks.map((pb) => el("tr", null,
        el("td", null,
          el("div", { class: "mono", style: { color: "var(--text)" } }, pb.playbook),
          pb.job_id
            ? el("a", { class: "muted small", href: `#/job/${pb.job_id}`, title: pb.finished || "" }, `checked ${relTime(pb.finished)}`)
            : el("div", { class: "muted small", title: pb.finished || "" }, `checked ${relTime(pb.finished)}`)),
        hosts.map((h) => cellFor(pb, h))))))));

  box.appendChild(el("div", { class: "drift-legend" },
    el("span", { class: "li" }, el("span", { class: "drift-dot sev-none" }), "in sync"),
    el("span", { class: "li" }, el("span", { class: "drift-dot sev-mid" }, "2"), "1–2 changed"),
    el("span", { class: "li" }, el("span", { class: "drift-dot sev-high" }, "3"), "3+ changed"),
    el("span", { class: "li" }, el("span", { class: "drift-dot sev-none has-failed" }), "failures"),
    el("span", { class: "li" }, el("span", { class: "drift-na" }, "·"), "not checked")));
}

/** Cell click → modal with the drifted task names + link to the source job. */
function openDriftCellModal(pb, host, cell) {
  const tasks = cell.tasks || [];
  openModal({
    title: `${pb.playbook} × ${host}`,
    body: el("div", null,
      el("div", { class: "row", style: { marginBottom: "12px" } },
        el("span", { class: "sum-chip changed" }, el("b", null, String(cell.changed || 0)), el("span", null, "would change")),
        el("span", { class: "sum-chip failed" }, el("b", null, String(cell.failed || 0)), el("span", null, "failed")),
        el("span", { class: "muted small", title: pb.finished || "" }, `checked ${relTime(pb.finished)}`)),
      tasks.length
        ? el("div", { class: "drift-task-list" },
            tasks.map((t) => el("div", { class: "drift-task-item mono" }, t)))
        : el("p", { class: "muted", style: { margin: 0 } },
            (cell.failed || 0) > 0
              ? "No drifted tasks, but the check run reported failures on this host."
              : "No drifted tasks — this host matches the repo.")),
    footer: [
      el("button", { class: "btn", onclick: closeModal }, "Close"),
      pb.job_id ? el("a", { class: "btn btn-secondary", href: `#/job/${pb.job_id}`, onclick: closeModal },
        icon("code"), "View source job") : null,
    ],
    width: "540px",
  });
}

/** Playbook multi-select → POST /drift/check (empty selection = all). */
function openDriftCheckModal(repo) {
  const startBtn = el("button", { class: "btn btn-primary", disabled: true }, icon("play"), "Start drift check");
  const { body } = openModal({
    title: "Run drift check",
    body: skeletonRows(2, 44),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), startBtn],
    width: "540px",
  });

  const boxes = [];
  getScan(repo.id).then((scan) => {
    const pbs = scan.playbooks || [];
    body.innerHTML = "";
    if (!pbs.length) {
      body.appendChild(el("div", { class: "empty" },
        el("h3", null, "No playbooks"),
        el("p", null, "This repository has no playbooks to check.")));
      return;
    }
    body.appendChild(el("p", { class: "muted small", style: { marginTop: 0 } },
      "Pine launches one --check job per selected playbook and computes drift from the results. Nothing is changed on the hosts."));
    const list = el("div", { class: "drift-pb-list" });
    for (const pb of pbs) {
      const cb = el("input", { type: "checkbox", checked: true, value: pb.path });
      boxes.push(cb);
      list.appendChild(el("label", { class: "check-row drift-pb-row" }, cb, el("span", { class: "mono" }, pb.path)));
    }
    body.appendChild(list);
    body.appendChild(el("div", { class: "row small", style: { marginTop: "8px", gap: "12px" } },
      el("a", { href: "#", onclick: (e) => { e.preventDefault(); boxes.forEach((b) => (b.checked = true)); } }, "select all"),
      el("a", { href: "#", onclick: (e) => { e.preventDefault(); boxes.forEach((b) => (b.checked = false)); } }, "select none")));
    startBtn.disabled = false;
  }).catch((e) => {
    body.innerHTML = "";
    body.appendChild(el("div", { class: "empty" },
      el("h3", null, "Scan failed"), el("p", null, e.message)));
    toast(e.message, "error");
  });

  startBtn.onclick = async () => {
    const selected = boxes.filter((b) => b.checked).map((b) => b.value);
    if (!selected.length) { toast("Select at least one playbook", "error"); return; }
    startBtn.disabled = true;
    try {
      const all = selected.length === boxes.length;
      const jobs = (await api(`/repos/${repo.id}/drift/check`, {
        method: "POST",
        body: JSON.stringify({ playbooks: all ? [] : selected }),
      })) || [];
      closeModal();
      toast(el("span", null,
        `${jobs.length} drift-check job${jobs.length === 1 ? "" : "s"} started — `,
        el("a", { href: "#/jobs" }, "view jobs")), "success", "Drift check");
    } catch (e) {
      startBtn.disabled = false;
      toast(e.message, "error");
    }
  };
}

/* ============================================================
   4j-1b. Services — status of declared services across hosts
   ============================================================ */

async function pageServices(page) {
  const repo = await requireRepo(page);
  if (!repo) return;

  const refreshBtn = el("button", { class: "btn btn-primary" }, icon("sync"), "Refresh status");
  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Services"),
    el("span", { class: "sub" }, `service status across ${repo.name}`),
    el("div", { class: "grow" }),
    refreshBtn));

  const box = el("div");
  page.appendChild(box);
  box.appendChild(skeletonRows(3, 80));

  const refresh = async () => {
    refreshBtn.disabled = true;
    try {
      const job = await api(`/repos/${repo.id}/services/refresh`, { method: "POST", body: JSON.stringify({}) });
      toast(el("span", null, "Checking service status… ", el("a", { href: `#/job/${job.id}` }, "job started")),
        "success", "Services");
      setTimeout(route, 1400); // reload once the (fast) job has stored results
    } catch (e) {
      toast(e.message, "error", "Service check failed");
      refreshBtn.disabled = false;
    }
  };
  refreshBtn.onclick = refresh;

  let rep;
  try {
    rep = await api(`/repos/${repo.id}/services`);
  } catch (e) {
    box.innerHTML = "";
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "Could not load services"),
      el("p", null, e.message),
      el("button", { class: "btn", onclick: route }, "Retry")));
    return;
  }
  box.innerHTML = "";

  const services = (rep && rep.services) || [];
  const hosts = (rep && rep.hosts) || [];
  const cells = (rep && rep.cells) || {};
  const sum = (rep && rep.summary) || {};

  if (!services.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No services declared"),
      el("p", null, "Declare services on hosts with a services: inventory var (e.g. services: [teamcity-agent, docker]), then run a check — Pine queries their real systemd state, no agents required."),
      el("button", { class: "btn btn-primary", onclick: refresh }, icon("sync"), "Refresh status")));
    return;
  }

  // summary strip
  const down = sum.hosts_down ?? 0;
  box.appendChild(el("div", { class: "panel drift-summary" },
    el("span", { class: "sum-chip" },
      el("b", null, String(sum.watched ?? services.length)), el("span", null, "services watched")),
    el("span", { class: "sum-chip " + (down > 0 ? "failed" : "ok") },
      el("b", null, String(down)), el("span", null, down === 1 ? "host with a service down" : "hosts with a service down")),
    el("span", { class: "sum-chip changed" },
      el("b", null, String(sum.running ?? 0)), el("span", null, "running")),
    rep.inventory ? el("span", { class: "chip", title: "Inventory reported on" }, rep.inventory) : null,
    rep.simulated ? el("span", { class: "chip", title: "Estimated — synthesized without ansible" }, "estimated") : null,
    el("div", { class: "grow" }),
    el("span", { class: "muted small", title: sum.last_checked || "" },
      sum.last_checked ? `checked ${relTime(sum.last_checked)}` : "never checked")));

  // heatmap cell: green running / red stopped / grey unknown / · not declared
  const cellFor = (svc, host) => {
    const cell = (cells[svc] || {})[host];
    if (!cell) {
      return el("td", { class: "drift-cell" },
        el("span", { class: "drift-na", title: `${host} does not declare ${svc}` }, "·"));
    }
    const state = cell.state || "unknown";
    const tip = `${cell.unit || svc} on ${host} — ${state}` + (cell.status ? ` · ${cell.status}` : "");
    return el("td", { class: "drift-cell" },
      el("button", {
        class: `svc-dot svc-${state}`,
        title: tip,
        onclick: () => openServiceCellModal(rep, svc, host, cell),
      }));
  };

  box.appendChild(el("div", { class: "table-wrap drift-heatmap" },
    el("table", { class: "data" },
      el("thead", null, el("tr", null,
        el("th", null, "Service"),
        hosts.map((h) => el("th", { class: "drift-host-th mono" }, h)))),
      el("tbody", null, services.map((svc) => el("tr", null,
        el("td", null, el("div", { class: "mono", style: { color: "var(--text)" } }, svc)),
        hosts.map((h) => cellFor(svc, h))))))));

  box.appendChild(el("div", { class: "drift-legend" },
    el("span", { class: "li" }, el("span", { class: "svc-dot svc-running" }), "running"),
    el("span", { class: "li" }, el("span", { class: "svc-dot svc-stopped" }), "stopped"),
    el("span", { class: "li" }, el("span", { class: "svc-dot svc-unknown" }), "unknown"),
    el("span", { class: "li" }, el("span", { class: "drift-na" }, "·"), "not declared")));
}

/** Cell click → modal with the unit/state + link to the source job. */
function openServiceCellModal(rep, svc, host, cell) {
  const state = cell.state || "unknown";
  const note = state === "running" ? "This service is running on the host."
    : state === "stopped" ? "Declared but not running on the host — investigate."
    : "Not gathered yet — run a service check to learn its state.";
  openModal({
    title: `${cell.unit || svc} × ${host}`,
    body: el("div", null,
      el("div", { class: "row", style: { marginBottom: "12px", gap: "10px" } },
        el("span", { class: `svc-chip svc-${state}` }, state),
        cell.status ? el("span", { class: "chip" }, cell.status) : null,
        el("span", { class: "muted small", title: rep.summary.last_checked || "" },
          rep.summary.last_checked ? `checked ${relTime(rep.summary.last_checked)}` : "")),
      el("p", { class: "muted", style: { margin: 0 } }, note),
      rep.simulated ? el("p", { class: "muted small" },
        "Estimated — ansible was not available, so this is synthesized from the inventory. Run against real hosts for true status.") : null),
    footer: [
      el("button", { class: "btn", onclick: closeModal }, "Close"),
      rep.job_id ? el("a", { class: "btn btn-secondary", href: `#/job/${rep.job_id}`, onclick: closeModal },
        icon("code"), "View source job") : null,
    ],
    width: "480px",
  });
}

/* ============================================================
   4j-2. Schedules — recurring, optionally plan-gated runs
   ============================================================ */

async function pageSchedules(page) {
  const box = el("div");
  let schedules = null;

  const sig = (list) => (list || []).map((s) =>
    [s.id, s.status, s.enabled, s.interval, s.gate, s.next_run_at, s.last_run_id, s.approved_at, s.blocked_reason].join("")).join("\n");

  const draw = () => {
    box.innerHTML = "";
    if (!schedules.length) {
      box.appendChild(el("div", { class: "empty" },
        el("h3", null, "No schedules yet"),
        el("p", null, "Run a playbook every 15 minutes, hour or day. With plan-gating on, Pine refuses to run when the current plan no longer matches the one you approved."),
        el("button", { class: "btn btn-primary", onclick: () => openScheduleModal(null, () => refresh()) },
          icon("plus"), "New schedule")));
      return;
    }
    const grid = el("div", { class: "grid cols-3" });
    for (const s of schedules) grid.appendChild(scheduleCard(s, () => refresh()));
    box.appendChild(grid);
  };

  const refresh = async (silent = false) => {
    try {
      const next = (await api("/schedules")) || [];
      const changed = schedules === null || sig(next) !== sig(schedules);
      schedules = next;
      if (changed) draw();
    } catch (e) {
      if (!silent) {
        box.innerHTML = "";
        box.appendChild(el("div", { class: "empty" },
          el("h3", null, "Could not load schedules"),
          el("p", null, e.message),
          el("button", { class: "btn", onclick: route }, "Retry")));
        toast(e.message, "error", "Schedules failed");
      }
    }
  };

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Schedules"),
    el("span", { class: "sub" }, "recurring playbook runs, optionally gated on an approved plan"),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: () => openScheduleModal(null, () => refresh()) },
      icon("plus"), "New schedule")));
  page.appendChild(box);
  box.appendChild(skeletonRows(3, 110));

  await refresh();
  const timer = setInterval(() => refresh(true), 10000);
  onCleanup(() => clearInterval(timer));
}

function scheduleCard(s, refresh) {
  const blocked = s.status === "blocked";

  const toggle = el("input", { type: "checkbox", checked: s.enabled || null });
  toggle.onchange = async () => {
    toggle.disabled = true;
    try {
      await api(`/schedules/${s.id}`, { method: "PATCH", body: JSON.stringify({ enabled: toggle.checked }) });
      toast(toggle.checked ? `${s.playbook} enabled` : `${s.playbook} paused`, "success");
      refresh();
    } catch (e) {
      toast(e.message, "error");
      toggle.checked = !toggle.checked;
      toggle.disabled = false;
    }
  };

  const runNowBtn = el("button", { class: "btn btn-sm", title: "Launch this schedule's job immediately" },
    icon("play"), "Run now");
  runNowBtn.onclick = async () => {
    runNowBtn.disabled = true;
    try {
      const job = await api(`/schedules/${s.id}/run-now`, { method: "POST" });
      toast(el("span", null, `${s.playbook} queued — `, el("a", { href: `#/job/${job.id}` }, "view job")),
        "success", "Run started");
      refresh();
    } catch (e) { toast(e.message, "error"); }
    runNowBtn.disabled = false;
  };

  return el("div", { class: "card sched-card" },
    el("div", { class: "row", style: { gap: "8px" } },
      el("span", { class: "name mono" }, s.playbook),
      el("div", { class: "grow" }),
      el("span", { class: `pill st-${s.status}`, title: blocked ? (s.blocked_reason || "") : null },
        blocked ? "BLOCKED" : s.status)),
    el("div", { class: "muted small" },
      s.repo_name || s.repo_id,
      s.inventory ? el("span", null, " · ", el("span", { class: "mono" }, s.inventory)) : null),
    el("div", { class: "row", style: { gap: "6px", flexWrap: "wrap" } },
      el("span", { class: "chip" }, icon("schedule"), `every ${s.interval}`),
      s.gate ? el("span", {
        class: "chip green",
        title: "Plan-gated: refuses to run when the current plan differs from the approved one",
      }, icon("shield"), "plan-gated") : null,
      s.check ? el("span", { class: "flag-badge check" }, "check") : null,
      s.limit ? el("span", { class: "chip mono" }, `limit ${s.limit}`) : null,
      s.tags ? el("span", { class: "chip mono" }, `tags ${s.tags}`) : null),
    blocked && s.blocked_reason
      ? el("div", { class: "sched-blocked-reason" }, icon("shield"), s.blocked_reason)
      : null,
    el("div", { class: "row small muted", style: { gap: "14px" } },
      el("span", { title: s.next_run_at || "" },
        !s.enabled ? "paused" : blocked ? "waiting for approval" : `next run ${untilTime(s.next_run_at)}`),
      s.last_run_id
        ? el("a", { href: `#/job/${s.last_run_id}`, title: s.last_run_at || "" }, `last run ${relTime(s.last_run_at)}`)
        : el("span", null, "never ran")),
    el("div", { class: "actions" },
      el("label", { class: "switch", title: s.enabled ? "Disable this schedule" : "Enable this schedule" },
        toggle, el("span", { class: "slider" })),
      runNowBtn,
      blocked ? el("button", {
        class: "btn btn-sm btn-warn",
        onclick: () => openApproveScheduleModal(s, refresh),
      }, icon("shield"), "Approve plan") : null,
      el("div", { class: "grow" }),
      el("button", { class: "btn btn-sm", onclick: () => openScheduleModal(s, refresh) }, "Edit"),
      el("button", {
        class: "btn btn-sm btn-danger", title: "Delete schedule",
        onclick: async () => {
          const ok = await confirmModal("Delete schedule",
            `Stop running “${s.playbook}” every ${s.interval}? Job history is kept.`);
          if (!ok) return;
          try {
            await api(`/schedules/${s.id}`, { method: "DELETE" });
            toast("Schedule deleted", "success");
            refresh();
          } catch (e) { toast(e.message, "error"); }
        },
      }, icon("trash"))));
}

/** Blocked schedule → review/approve the current plan fingerprint. */
function openApproveScheduleModal(s, refresh) {
  const approveBtn = el("button", { class: "btn btn-primary" }, icon("check"), "Approve & resume");
  approveBtn.onclick = async () => {
    approveBtn.disabled = true;
    try {
      await api(`/schedules/${s.id}/approve`, { method: "POST" });
      closeModal();
      toast(`${s.playbook} — current plan approved, scheduled runs resume`, "success", "Plan approved");
      refresh();
    } catch (e) {
      approveBtn.disabled = false;
      toast(e.message, "error");
    }
  };
  const viewPlanBtn = el("button", {
    class: "btn btn-secondary",
    title: "Open the current plan for this schedule's exact parameters",
  }, icon("clipboard"), "View plan");
  viewPlanBtn.onclick = () => runPlan({
    repo_id: s.repo_id, playbook: s.playbook, inventory: s.inventory || "",
    limit: s.limit || "", tags: s.tags || "", check: !!s.check,
    vars: {}, host_vars: {}, fact_profile: "",
  }, viewPlanBtn);

  openModal({
    title: "Approve changed plan",
    body: el("div", null,
      el("p", { class: "muted", style: { marginTop: 0 } },
        "The plan changed since the last approval. Review and approve to let scheduled runs resume."),
      s.blocked_reason ? el("div", { class: "sched-blocked-reason" }, icon("shield"), s.blocked_reason) : null,
      el("div", { class: "row small muted", style: { marginTop: "12px", gap: "12px" } },
        el("span", { class: "mono" }, s.playbook),
        s.inventory ? el("span", { class: "mono" }, s.inventory) : null,
        el("span", null, s.approved_at ? `last approved ${relTime(s.approved_at)}` : "never approved"))),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), viewPlanBtn, approveBtn],
  });
}

const SCHEDULE_INTERVALS = ["15m", "1h", "6h", "24h"];

/** Create (existing = null) or edit a schedule. */
async function openScheduleModal(existing, onDone) {
  try { await loadRepos(); } catch (e) { toast(e.message, "error"); return; }
  if (!State.repos.length) {
    toast("Connect a repository first", "error", "No repositories");
    location.hash = "#/repos";
    return;
  }

  const repoSel = el("select");
  const pbSel = el("select");
  const invSel = el("select");
  const limitIn = el("input", { type: "text", placeholder: "e.g. web01,db* (optional)" });
  const tagsIn = el("input", { type: "text", placeholder: "e.g. config,deploy (optional)" });
  const checkBox = el("input", { type: "checkbox" });
  const gateBox = el("input", { type: "checkbox" });
  const intervalSel = el("select", null,
    SCHEDULE_INTERVALS.map((v) => el("option", { value: v }, `every ${v}`)),
    el("option", { value: "custom" }, "custom…"));
  const customIn = el("input", { type: "text", placeholder: "e.g. 45m, 2h, 7d", style: { display: "none", marginTop: "6px" } });
  intervalSel.addEventListener("change", () => {
    customIn.style.display = intervalSel.value === "custom" ? "" : "none";
    if (intervalSel.value === "custom") customIn.focus();
  });

  for (const r of State.repos) repoSel.appendChild(el("option", { value: r.id }, r.name));
  repoSel.value = (existing && existing.repo_id) || State.repoId || State.repos[0].id;
  if (!repoSel.value) repoSel.value = State.repos[0].id;
  limitIn.value = (existing && existing.limit) || "";
  tagsIn.value = (existing && existing.tags) || "";
  checkBox.checked = !!(existing && existing.check);
  gateBox.checked = existing ? !!existing.gate : true; // gate defaults ON
  if (existing && existing.interval) {
    if (SCHEDULE_INTERVALS.includes(existing.interval)) intervalSel.value = existing.interval;
    else { intervalSel.value = "custom"; customIn.value = existing.interval; customIn.style.display = ""; }
  }

  const saveBtn = el("button", { class: "btn btn-primary" }, existing ? "Save changes" : "Create schedule");

  const fillScanOptions = async () => {
    pbSel.innerHTML = ""; invSel.innerHTML = "";
    pbSel.appendChild(el("option", { value: "" }, "Loading…"));
    invSel.appendChild(el("option", { value: "" }, "Loading…"));
    pbSel.disabled = invSel.disabled = saveBtn.disabled = true;
    try {
      const scan = await getScan(repoSel.value);
      pbSel.innerHTML = ""; invSel.innerHTML = "";
      const pbs = scan.playbooks || [];
      const invs = scan.inventories || [];
      if (!pbs.length) pbSel.appendChild(el("option", { value: "" }, "No playbooks found"));
      for (const p of pbs) pbSel.appendChild(el("option", { value: p.path }, `${p.name || p.path}  (${p.path})`));
      if (!invs.length) invSel.appendChild(el("option", { value: "" }, "No inventories found"));
      for (const i of invs) invSel.appendChild(el("option", { value: i.path || i.name }, i.name));
      if (existing && existing.playbook && pbs.some((p) => p.path === existing.playbook)) pbSel.value = existing.playbook;
      if (existing && existing.inventory) {
        const opt = [...invSel.options].find((o) => o.value === existing.inventory);
        if (opt) invSel.value = existing.inventory;
      }
      pbSel.disabled = !pbs.length;
      invSel.disabled = !invs.length;
      saveBtn.disabled = !pbs.length;
    } catch (e) {
      pbSel.innerHTML = ""; invSel.innerHTML = "";
      pbSel.appendChild(el("option", { value: "" }, "Scan failed"));
      invSel.appendChild(el("option", { value: "" }, "Scan failed"));
      toast(e.message, "error");
    }
  };
  repoSel.addEventListener("change", fillScanOptions);

  saveBtn.onclick = async () => {
    if (!pbSel.value) { toast("Pick a playbook", "error"); return; }
    const interval = intervalSel.value === "custom" ? customIn.value.trim() : intervalSel.value;
    if (!interval) { toast("Interval is required", "error"); customIn.focus(); return; }
    const body = {
      repo_id: repoSel.value,
      playbook: pbSel.value,
      inventory: invSel.value || "",
      limit: limitIn.value.trim(),
      tags: tagsIn.value.trim(),
      check: checkBox.checked,
      interval,
      gate: gateBox.checked,
      enabled: existing ? !!existing.enabled : true,
    };
    saveBtn.disabled = true;
    try {
      if (existing) await api(`/schedules/${existing.id}`, { method: "PATCH", body: JSON.stringify(body) });
      else await api("/schedules", { method: "POST", body: JSON.stringify(body) });
      closeModal();
      toast(existing ? "Schedule updated" : `${body.playbook} runs every ${interval}`,
        "success", existing ? "Saved" : "Schedule created");
      if (onDone) onDone();
    } catch (e) {
      saveBtn.disabled = false;
      toast(e.message, "error");
    }
  };

  openModal({
    title: existing ? `Edit schedule — ${existing.playbook}` : "New schedule",
    body: el("div", null,
      el("div", { class: "field" }, el("label", null, "Repository"), repoSel),
      el("div", { class: "field" }, el("label", null, "Playbook"), pbSel),
      el("div", { class: "field" }, el("label", null, "Inventory"), invSel),
      el("div", { style: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: "12px" } },
        el("div", { class: "field" }, el("label", null, "Limit"), limitIn),
        el("div", { class: "field" }, el("label", null, "Tags"), tagsIn)),
      el("div", { class: "field" }, el("label", null, "Interval"), intervalSel, customIn,
        el("span", { class: "hint" }, "How often the playbook runs.")),
      el("label", { class: "check-row", style: { marginBottom: "10px" } }, checkBox,
        el("span", null, "Check mode ", el("span", { class: "muted" }, "(dry run, no changes applied)"))),
      el("label", { class: "check-row" }, gateBox,
        el("span", null, "Plan-gate ", el("span", { class: "muted" },
          "— refuse to run when the current plan differs from the approved one")))),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), saveBtn],
  });
  fillScanOptions();
}

/* ============================================================
   4j-3. Pipelines — multi-step runs with approval gates
   ============================================================ */

const PIPELINE_TERMINAL = new Set(["success", "failed", "canceled"]);

async function pagePipelines(page) {
  const pipeBox = el("div", { class: "grid cols-3" });
  const runsBox = el("div");
  let pipelines = null;
  let runs = null;
  let pollTimer = 0;
  onCleanup(() => clearTimeout(pollTimer));

  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Pipelines"),
    el("span", { class: "sub" }, "chain playbooks into multi-step runs with approval gates"),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: () => openPipelineModal(null, () => refreshPipelines()) },
      icon("plus"), "New pipeline")));
  page.appendChild(el("div", { class: "section-title" }, "Pipelines"));
  page.appendChild(pipeBox);
  page.appendChild(el("div", { class: "section-title" }, "Recent runs"));
  page.appendChild(runsBox);
  pipeBox.appendChild(skeletonRows(2, 150));
  runsBox.appendChild(skeletonRows(2, 64));

  const pipelineCard = (pl) => {
    const steps = pl.steps || [];
    const runBtn = el("button", { class: "btn btn-sm btn-primary" }, icon("play"), "Run");
    runBtn.onclick = async () => {
      runBtn.disabled = true;
      try {
        await api(`/pipelines/${pl.id}/run`, { method: "POST" });
        toast(`${pl.name} started`, "success", "Pipeline run");
        refreshRuns(true);
      } catch (e) { toast(e.message, "error"); }
      runBtn.disabled = false;
    };
    return el("div", { class: "card pipe-card" },
      el("div", { class: "row", style: { gap: "8px" } },
        el("span", { style: { fontWeight: 650, fontSize: "15px" } }, pl.name),
        el("div", { class: "grow" }),
        el("span", { class: "chip" }, `${steps.length} step${steps.length === 1 ? "" : "s"}`)),
      el("div", { class: "muted small" }, pl.repo_name || pl.repo_id),
      el("div", { class: "pipe-steps" }, steps.flatMap((st, i) => [
        i ? el("span", { class: "pipe-arrow" }, "→") : null,
        el("span", {
          class: "step-chip",
          title: `${st.playbook}${st.inventory ? " @ " + st.inventory : ""}`
            + `${st.limit ? " · limit " + st.limit : ""}${st.tags ? " · tags " + st.tags : ""}${st.check ? " · check" : ""}`
            + `${st.require_approval ? "\npauses for human approval before this step" : ""}`
            + `${st.continue_on_failure ? "\ncontinues even if this step fails" : ""}`,
        },
          st.require_approval ? el("span", { class: "step-gate" }, icon("shield")) : null,
          st.name || st.playbook),
      ])),
      el("div", { class: "actions" },
        runBtn,
        el("button", {
          class: "btn btn-sm", title: "Edit and rebuild this pipeline",
          onclick: () => openPipelineModal(pl, () => refreshPipelines()),
        }, "Edit"),
        el("div", { class: "grow" }),
        el("button", {
          class: "btn btn-sm btn-danger",
          onclick: async () => {
            const ok = await confirmModal("Delete pipeline", `Remove “${pl.name}”? Past pipeline runs are kept.`);
            if (!ok) return;
            try {
              await api(`/pipelines/${pl.id}`, { method: "DELETE" });
              toast(`Removed ${pl.name}`, "success");
              refreshPipelines();
            } catch (e) { toast(e.message, "error"); }
          },
        }, icon("trash"), "Delete")));
  };

  const drawPipelines = () => {
    pipeBox.innerHTML = "";
    if (!pipelines.length) {
      pipeBox.style.display = "block";
      pipeBox.appendChild(el("div", { class: "empty" },
        el("h3", null, "No pipelines yet"),
        el("p", null, "A pipeline chains playbook steps — canary on one host, pause for approval, then roll out everywhere."),
        el("button", { class: "btn btn-primary", onclick: () => openPipelineModal(null, () => refreshPipelines()) },
          icon("plus"), "New pipeline")));
      return;
    }
    pipeBox.style.display = "grid";
    for (const pl of pipelines) pipeBox.appendChild(pipelineCard(pl));
  };

  const stepChip = (st) => {
    const inner = [el("span", { class: "step-dot" }), st.name || "step"];
    const cls = `step-chip run-st-${st.status || "pending"}`;
    return st.job_id
      ? el("a", { class: cls, href: `#/job/${st.job_id}`, title: `${st.status} — view job` }, inner)
      : el("span", { class: cls, title: st.status || "pending" }, inner);
  };

  const runRow = (r) => {
    const waiting = r.status === "waiting_approval";
    const active = !PIPELINE_TERMINAL.has(r.status);
    const approveBtn = el("button", { class: "btn btn-sm btn-warn" }, icon("shield"), "Approve & continue");
    approveBtn.onclick = async () => {
      approveBtn.disabled = true;
      try {
        await api(`/pipeline-runs/${r.id}/approve`, { method: "POST" });
        toast(`${r.pipeline_name} — continuing`, "success", "Approved");
      } catch (e) { toast(e.message, "error"); }
      refreshRuns(true);
    };
    const cancelBtn = el("button", { class: "btn btn-sm btn-danger" }, icon("stop"), "Cancel");
    cancelBtn.onclick = async () => {
      cancelBtn.disabled = true;
      try {
        await api(`/pipeline-runs/${r.id}/cancel`, { method: "POST" });
        toast(`${r.pipeline_name} canceled`, "success");
      } catch (e) { toast(e.message, "error"); }
      refreshRuns(true);
    };
    return el("div", { class: "panel pipe-run" + (waiting ? " waiting" : "") },
      el("div", { class: "row", style: { gap: "10px" } },
        el("span", { class: `pill st-${r.status}` }, (r.status || "?").replace(/_/g, " ")),
        el("span", { style: { fontWeight: 600 } }, r.pipeline_name),
        el("span", { class: "muted small", title: r.created || "" },
          PIPELINE_TERMINAL.has(r.status) && r.finished
            ? `finished ${relTime(r.finished)}`
            : `started ${relTime(r.created)}`),
        el("div", { class: "grow" }),
        waiting ? approveBtn : null,
        active ? cancelBtn : null),
      el("div", { class: "pipe-steps" }, (r.steps || []).flatMap((st, i) => [
        i ? el("span", { class: "pipe-arrow" }, "→") : null,
        stepChip(st),
      ])));
  };

  const drawRuns = () => {
    runsBox.innerHTML = "";
    if (!runs.length) {
      runsBox.appendChild(el("div", { class: "empty" },
        el("h3", null, "No pipeline runs yet"),
        el("p", null, "Press Run on a pipeline — each run shows up here with live per-step status and links to its jobs.")));
      return;
    }
    for (const r of runs) runsBox.appendChild(runRow(r));
  };

  const refreshPipelines = async () => {
    try {
      pipelines = (await api("/pipelines")) || [];
      drawPipelines();
    } catch (e) {
      pipeBox.innerHTML = "";
      pipeBox.style.display = "block";
      pipeBox.appendChild(el("div", { class: "empty" },
        el("h3", null, "Could not load pipelines"), el("p", null, e.message)));
      toast(e.message, "error", "Pipelines failed");
    }
  };

  const runsSig = (list) => (list || []).map((r) =>
    r.id + r.status + (r.steps || []).map((st) => st.status + (st.job_id || "")).join(",")).join("|");

  // poll every 5s while any run is still moving
  const refreshRuns = async (silent = false) => {
    try {
      const next = (await api("/pipeline-runs")) || [];
      const changed = runs === null || runsSig(next) !== runsSig(runs);
      runs = next;
      if (changed) drawRuns();
    } catch (e) {
      if (!silent) {
        runsBox.innerHTML = "";
        runsBox.appendChild(el("div", { class: "empty" },
          el("h3", null, "Could not load runs"), el("p", null, e.message)));
      }
    }
    clearTimeout(pollTimer);
    if ((runs || []).some((r) => !PIPELINE_TERMINAL.has(r.status))) {
      pollTimer = setTimeout(() => refreshRuns(true), 5000);
    }
  };

  await Promise.all([refreshPipelines(), refreshRuns()]);
}

/** Pipeline builder: name + repo + dynamic, reorderable step list.
 *  Editing rebuilds (POST new, DELETE old) — the API has no pipeline PATCH. */
async function openPipelineModal(existing, onDone) {
  try { await loadRepos(); } catch (e) { toast(e.message, "error"); return; }
  if (!State.repos.length) {
    toast("Connect a repository first", "error", "No repositories");
    location.hash = "#/repos";
    return;
  }

  const nameIn = el("input", { type: "text", placeholder: "Deploy shop", autocomplete: "off" });
  nameIn.value = (existing && existing.name) || "";
  const repoSel = el("select");
  for (const r of State.repos) repoSel.appendChild(el("option", { value: r.id }, r.name));
  repoSel.value = (existing && existing.repo_id) || State.repoId || State.repos[0].id;
  if (!repoSel.value) repoSel.value = State.repos[0].id;

  let scanPbs = [];
  let scanInvs = [];
  const newStep = (data) => ({
    name: (data && data.name) || "",
    playbook: (data && data.playbook) || "",
    inventory: (data && data.inventory) || "",
    limit: (data && data.limit) || "",
    tags: (data && data.tags) || "",
    check: !!(data && data.check),
    require_approval: !!(data && data.require_approval),
    continue_on_failure: !!(data && data.continue_on_failure),
  });
  const steps = (existing && (existing.steps || []).length)
    ? existing.steps.map(newStep)
    : [newStep()];

  const stepsBox = el("div");
  const saveBtn = el("button", { class: "btn btn-primary" }, existing ? "Rebuild pipeline" : "Create pipeline");

  const stepEditor = (st, idx) => {
    const nameI = el("input", { type: "text", placeholder: "e.g. Canary on web01", style: { width: "100%" } });
    nameI.value = st.name;
    nameI.addEventListener("input", () => (st.name = nameI.value));

    const pbS = el("select");
    if (!scanPbs.length) pbS.appendChild(el("option", { value: "" }, "No playbooks found"));
    for (const p of scanPbs) pbS.appendChild(el("option", { value: p.path }, p.name ? `${p.name}  (${p.path})` : p.path));
    if (st.playbook && scanPbs.some((p) => p.path === st.playbook)) pbS.value = st.playbook;
    st.playbook = pbS.value;
    pbS.addEventListener("change", () => (st.playbook = pbS.value));

    const invS = el("select");
    if (!scanInvs.length) invS.appendChild(el("option", { value: "" }, "No inventories found"));
    for (const i of scanInvs) invS.appendChild(el("option", { value: i.path || i.name }, i.name));
    if (st.inventory) {
      const opt = [...invS.options].find((o) => o.value === st.inventory);
      if (opt) invS.value = st.inventory;
    }
    st.inventory = invS.value;
    invS.addEventListener("change", () => (st.inventory = invS.value));

    const limitI = el("input", { type: "text", placeholder: "limit (optional)", style: { width: "100%" } });
    limitI.value = st.limit;
    limitI.addEventListener("input", () => (st.limit = limitI.value));
    const tagsI = el("input", { type: "text", placeholder: "tags (optional)", style: { width: "100%" } });
    tagsI.value = st.tags;
    tagsI.addEventListener("input", () => (st.tags = tagsI.value));

    const mkToggle = (key, label, title) => {
      const cb = el("input", { type: "checkbox", checked: st[key] || null });
      cb.addEventListener("change", () => (st[key] = cb.checked));
      return el("label", { class: "check-row small", title }, cb, label);
    };

    return el("div", { class: "pipe-step-editor" },
      el("div", { class: "row", style: { gap: "8px", marginBottom: "8px", flexWrap: "nowrap" } },
        el("span", { class: "step-num" }, String(idx + 1)),
        el("div", { style: { flex: 1, minWidth: 0 } }, nameI),
        el("button", {
          class: "btn btn-sm btn-ghost", title: "Move up", disabled: idx === 0 || null,
          onclick: () => { steps.splice(idx, 1); steps.splice(idx - 1, 0, st); renderSteps(); },
        }, "↑"),
        el("button", {
          class: "btn btn-sm btn-ghost", title: "Move down", disabled: idx === steps.length - 1 || null,
          onclick: () => { steps.splice(idx, 1); steps.splice(idx + 1, 0, st); renderSteps(); },
        }, "↓"),
        el("button", {
          class: "btn btn-sm btn-danger", title: "Remove step",
          onclick: () => { steps.splice(idx, 1); renderSteps(); },
        }, icon("trash"))),
      el("div", { style: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px", marginBottom: "8px" } },
        el("div", { class: "field", style: { marginBottom: 0 } }, el("label", null, "Playbook"), pbS),
        el("div", { class: "field", style: { marginBottom: 0 } }, el("label", null, "Inventory"), invS)),
      el("div", { style: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px", marginBottom: "6px" } },
        limitI, tagsI),
      el("div", { class: "pipe-toggles" },
        mkToggle("check", "check mode", "Run this step with --check (dry run)"),
        mkToggle("require_approval", "require approval", "Pause for human approval before this step"),
        mkToggle("continue_on_failure", "continue on failure", "Keep going to the next step even if this one fails")));
  };

  const renderSteps = () => {
    stepsBox.innerHTML = "";
    if (!steps.length) {
      stepsBox.appendChild(el("div", { class: "muted small", style: { padding: "8px 2px" } },
        "No steps — add at least one."));
    }
    steps.forEach((st, idx) => stepsBox.appendChild(stepEditor(st, idx)));
  };

  const loadScan = async () => {
    stepsBox.innerHTML = "";
    stepsBox.appendChild(skeletonRows(Math.max(steps.length, 1), 130));
    try {
      const scan = await getScan(repoSel.value);
      scanPbs = scan.playbooks || [];
      scanInvs = scan.inventories || [];
    } catch (e) {
      scanPbs = [];
      scanInvs = [];
      toast(e.message, "error");
    }
    renderSteps();
  };
  repoSel.addEventListener("change", loadScan);

  saveBtn.onclick = async () => {
    const name = nameIn.value.trim();
    if (!name) { toast("Pipeline name is required", "error"); nameIn.focus(); return; }
    if (!steps.length) { toast("Add at least one step", "error"); return; }
    for (let i = 0; i < steps.length; i++) {
      if (!steps[i].playbook) { toast(`Step ${i + 1} needs a playbook`, "error"); return; }
    }
    const body = {
      name,
      repo_id: repoSel.value,
      steps: steps.map((st, i) => ({
        name: st.name.trim() || st.playbook || `Step ${i + 1}`,
        playbook: st.playbook,
        inventory: st.inventory || "",
        limit: st.limit.trim(),
        tags: st.tags.trim(),
        check: st.check,
        require_approval: st.require_approval,
        continue_on_failure: st.continue_on_failure,
      })),
    };
    saveBtn.disabled = true;
    try {
      await api("/pipelines", { method: "POST", body: JSON.stringify(body) });
      if (existing) {
        try { await api(`/pipelines/${existing.id}`, { method: "DELETE" }); }
        catch { /* rebuilt copy exists; the old one just lingers */ }
      }
      closeModal();
      toast(existing ? `${name} rebuilt` : `${name} created`, "success", "Pipeline saved");
      if (onDone) onDone();
    } catch (e) {
      saveBtn.disabled = false;
      toast(e.message, "error");
    }
  };

  openModal({
    title: existing ? `Edit pipeline — ${existing.name}` : "New pipeline",
    body: el("div", null,
      el("div", { style: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: "12px" } },
        el("div", { class: "field" }, el("label", null, "Name"), nameIn),
        el("div", { class: "field" }, el("label", null, "Repository"), repoSel)),
      el("div", { class: "field", style: { marginBottom: "8px" } }, el("label", null, "Steps")),
      stepsBox,
      el("button", {
        class: "btn btn-sm", style: { marginTop: "4px" },
        onclick: () => {
          steps.push(newStep({ inventory: steps.length ? steps[steps.length - 1].inventory : "" }));
          renderSteps();
        },
      }, icon("plus"), "Add step"),
      existing ? el("div", { class: "hint muted", style: { marginTop: "12px", fontSize: "11.5px" } },
        "Saving rebuilds the pipeline under a new id; past runs keep pointing at the old version.") : null),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), saveBtn],
    width: "720px",
  });
  loadScan();
}

/* ============================================================
   4j. Jobs (list)
   ============================================================ */

async function pageJobs(page) {
  page.appendChild(el("div", { class: "page-head" },
    el("h1", null, "Jobs"),
    el("span", { class: "sub" }, "playbook runs and history"),
    el("div", { class: "grow" }),
    el("button", { class: "btn btn-primary", onclick: () => openRunModal() }, icon("play"), "Run playbook")));

  const box = el("div");
  page.appendChild(box);
  box.appendChild(skeletonRows(4, 48));

  const draw = (jobs) => {
    box.innerHTML = "";
    if (!jobs.length) {
      box.appendChild(el("div", { class: "empty" },
        el("h3", null, "No jobs yet"),
        el("p", null, "Run a playbook against an inventory — Pine streams the output live and keeps the history here."),
        el("button", { class: "btn btn-primary", onclick: () => openRunModal() }, icon("play"), "Run playbook")));
      return;
    }
    box.appendChild(jobsTable(jobs));
  };

  let jobs = await api("/jobs") || [];
  draw(jobs);

  // light polling to keep statuses fresh
  const timer = setInterval(async () => {
    try {
      const next = await api("/jobs") || [];
      const sig = (a) => a.map((j) => j.id + j.status + (j.duration_ms || 0)).join();
      if (sig(next) !== sig(jobs)) { jobs = next; draw(jobs); }
    } catch { /* transient */ }
  }, 4000);
  onCleanup(() => clearInterval(timer));
}

/* ============================================================
   4k. Job detail — live log via SSE + run diff
   ============================================================ */

const TERMINAL_STATUSES = new Set(["success", "failed", "canceled"]);

function logLineClass(line) {
  if (line.startsWith("PLAY RECAP")) return "ll-recap";
  if (line.startsWith("PLAY ")) return "ll-play";
  if (line.startsWith("TASK [") || line.startsWith("HANDLER [") || line.startsWith("RUNNING HANDLER [")) return "ll-task";
  const t = line.trimStart();
  if (t.startsWith("ok:") || t.startsWith("ok ")) return "ll-ok";
  if (t.startsWith("changed:")) return "ll-changed";
  if (t.startsWith("failed:") || t.startsWith("fatal:") || t.startsWith("ERROR")) return "ll-failed";
  if (t.startsWith("unreachable:")) return "ll-failed";
  if (t.startsWith("skipping:") || t.startsWith("skipped:")) return "ll-skip";
  if (t.startsWith("[WARNING]") || t.startsWith("[DEPRECATION")) return "ll-warn";
  return "";
}

async function pageJobDetail(page, segs) {
  const jobId = segs[0];
  page.appendChild(skeletonRows(2, 80));
  let job = await api(`/jobs/${jobId}`);
  page.innerHTML = "";

  $("#topbar-title").textContent = `Job · ${job.playbook}`;

  const headBox = el("div", { class: "panel", style: { padding: "16px 20px", marginBottom: "4px" } });
  page.appendChild(headBox);

  const cancelBtn = el("button", {
    class: "btn btn-danger btn-sm",
    onclick: async () => {
      cancelBtn.disabled = true;
      try {
        job = await api(`/jobs/${jobId}/cancel`, { method: "POST" });
        toast("Cancel requested", "success");
        renderHead();
      } catch (e) { toast(e.message, "error"); cancelBtn.disabled = false; }
    },
  }, icon("stop"), "Cancel");

  const renderHead = () => {
    headBox.innerHTML = "";
    const running = job.status === "running" || job.status === "pending";
    headBox.appendChild(el("div", { class: "row", style: { marginBottom: "10px" } },
      el("a", { href: "#/jobs", class: "btn btn-ghost btn-sm" }, "← Jobs"),
      statusPill(job.status),
      el("span", { class: "mono", style: { fontWeight: 650, fontSize: "15px" } }, job.playbook),
      job.check ? el("span", { class: "flag-badge check" }, "check mode") : null,
      job.simulated ? el("span", { class: "flag-badge sim", title: "Simulated run (no real ansible-playbook execution)" }, "simulated") : null,
      el("span", { style: { flex: 1 } }),
      running ? cancelBtn : null,
      TERMINAL_STATUSES.has(job.status) ? el("button", {
        class: "btn btn-sm", title: "Diff this run against another run of the same playbook",
        onclick: () => openCompareModal(job, showDiff),
      }, icon("diff"), "Compare") : null,
      el("a", { class: "btn btn-sm", href: `/api/jobs/${jobId}/log`, download: `${jobId}.log` }, icon("download"), "Download log")));
    headBox.appendChild(el("div", { class: "row small muted", style: { marginBottom: "12px", gap: "16px" } },
      el("span", null, "repo ", el("b", { style: { color: "var(--text)" } }, job.repo_name || job.repo_id)),
      el("span", null, "inventory ", el("b", { class: "mono", style: { color: "var(--text)" } }, job.inventory || "—")),
      job.limit ? el("span", null, "limit ", el("b", { class: "mono", style: { color: "var(--text)" } }, job.limit)) : null,
      job.tags ? el("span", null, "tags ", el("b", { class: "mono", style: { color: "var(--text)" } }, job.tags)) : null,
      el("span", { title: job.started || job.created }, running ? `started ${relTime(job.started || job.created)}` : `ran ${relTime(job.started || job.created)}`),
      el("span", null, "duration ", el("b", { class: "mono", style: { color: "var(--text)" } }, fmtDuration(job.duration_ms)))));
    const s = job.summary || {};
    headBox.appendChild(el("div", { class: "summary-chips" },
      el("span", { class: "sum-chip ok" }, el("b", null, String(s.ok ?? 0)), el("span", null, "ok")),
      el("span", { class: "sum-chip changed" }, el("b", null, String(s.changed ?? 0)), el("span", null, "changed")),
      el("span", { class: "sum-chip failed" }, el("b", null, String(s.failed ?? 0)), el("span", null, "failed")),
      el("span", { class: "sum-chip skipped" }, el("b", null, String(s.skipped ?? 0)), el("span", null, "skipped")),
      el("span", { class: "sum-chip unreachable" }, el("b", null, String(s.unreachable ?? 0)), el("span", null, "unreachable"))));
  };
  renderHead();

  // log toolbar + terminal (wrapped so the diff view can swap in for it)
  let autoscroll = true;
  const autoBox = el("input", { type: "checkbox", checked: true, onchange: (e) => { autoscroll = e.target.checked; if (autoscroll) scrollLog(); } });
  const logSection = el("div");
  logSection.appendChild(el("div", { class: "log-toolbar" },
    el("span", { class: "section-title", style: { margin: 0 } }, "Output"),
    el("div", { class: "grow" }),
    el("label", { class: "check-row muted small" }, autoBox, "Autoscroll")));

  const logBox = el("div", { class: "job-log" });
  logSection.appendChild(logBox);
  page.appendChild(logSection);
  const diffSection = el("div", { style: { display: "none" } });
  page.appendChild(diffSection);
  const cursor = el("span", { class: "cursor" });

  function backToLog() {
    diffSection.style.display = "none";
    diffSection.innerHTML = "";
    logSection.style.display = "";
  }

  /** Fetch + render the run diff against another (older or newer) job. */
  async function showDiff(other) {
    logSection.style.display = "none";
    diffSection.style.display = "";
    diffSection.innerHTML = "";
    diffSection.appendChild(skeletonRows(3, 64));
    try {
      const diff = await api(`/jobs/${jobId}/diff?with=${encodeURIComponent(other.id)}`);
      diffSection.innerHTML = "";
      renderJobDiff(diffSection, diff, backToLog, () => openCompareModal(job, showDiff));
    } catch (e) {
      diffSection.innerHTML = "";
      diffSection.appendChild(el("div", { class: "empty" },
        el("h3", null, "Could not compute diff"),
        el("p", null, e.message),
        el("button", { class: "btn", onclick: backToLog }, "Back to log")));
      toast(e.message, "error", "Diff failed");
    }
  }

  const scrollLog = () => { if (autoscroll) logBox.scrollTop = logBox.scrollHeight; };
  const appendLine = (line) => {
    const div = el("div", { class: "ll " + logLineClass(line) }, line);
    if (cursor.parentNode) logBox.insertBefore(div, cursor);
    else logBox.appendChild(div);
    scrollLog();
  };

  if (TERMINAL_STATUSES.has(job.status)) {
    // finished: fetch full log once
    try {
      const res = await fetch(`/api/jobs/${jobId}/log`);
      const text = res.ok ? await res.text() : "";
      logBox.innerHTML = "";
      if (!text.trim()) {
        logBox.appendChild(el("div", { class: "muted" }, "(no output captured)"));
      } else {
        for (const line of text.replace(/\n$/, "").split("\n")) appendLine(line);
      }
    } catch {
      logBox.appendChild(el("div", { class: "ll ll-failed" }, "Failed to load log."));
    }
    logBox.scrollTop = 0;
  } else {
    // live: stream via SSE (server replays from the start of the run)
    logBox.appendChild(cursor);
    const es = new EventSource(`/api/jobs/${jobId}/events`);
    onCleanup(() => es.close());
    es.addEventListener("line", (e) => appendLine(e.data));
    es.addEventListener("status", (e) => {
      try {
        job = JSON.parse(e.data);
        renderHead();
        if (TERMINAL_STATUSES.has(job.status)) {
          es.close();
          cursor.remove();
          toast(`Job ${job.status}: ${job.playbook}`, job.status === "success" ? "success" : "error",
            job.status === "success" ? "Job finished" : "Job " + job.status);
        }
      } catch { /* malformed status frame */ }
    });
    es.onerror = () => {
      // EventSource auto-reconnects; if the job finished meanwhile, refresh state
      api(`/jobs/${jobId}`).then((j) => {
        job = j; renderHead();
        if (TERMINAL_STATUSES.has(j.status)) { es.close(); cursor.remove(); }
      }).catch(() => {});
    };
  }
}

/** Modal listing other terminal runs of the same playbook+repo to diff against. */
async function openCompareModal(job, onPick) {
  const { body } = openModal({
    title: "Compare with another run",
    body: skeletonRows(3, 44),
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel")],
    width: "560px",
  });
  let jobs;
  try {
    jobs = (await api("/jobs")) || [];
  } catch (e) {
    body.innerHTML = "";
    body.appendChild(el("div", { class: "empty" },
      el("h3", null, "Could not load jobs"),
      el("p", null, e.message)));
    toast(e.message, "error");
    return;
  }
  const cands = jobs
    .filter((j) => j.id !== job.id && j.repo_id === job.repo_id && j.playbook === job.playbook && TERMINAL_STATUSES.has(j.status))
    .sort((a, b) => new Date(b.started || b.created || 0) - new Date(a.started || a.created || 0));
  body.innerHTML = "";
  if (!cands.length) {
    body.appendChild(el("div", { class: "empty" },
      el("h3", null, "Nothing to compare"),
      el("p", null, `No other finished runs of ${job.playbook} in this repository yet.`)));
    return;
  }
  body.appendChild(el("p", { class: "muted small", style: { marginTop: 0 } },
    "Other finished runs of ", el("span", { class: "mono" }, job.playbook), " — pick one to diff against this run."));
  body.appendChild(el("div", { class: "cmp-list" },
    cands.map((c) => el("div", { class: "cmp-row", onclick: () => { closeModal(); onPick(c); } },
      statusPill(c.status),
      el("span", { class: "mono small muted" }, (c.id || "").slice(0, 8)),
      el("span", { class: "muted small mono" }, c.inventory || "—"),
      el("div", { class: "grow" }),
      el("span", { class: "mono small" }, fmtDuration(c.duration_ms)),
      el("span", { class: "muted small", title: c.started || c.created }, relTime(c.started || c.created))))));
}

function diffStatusWord(s) {
  const st = s || "missing";
  return el("span", { class: "ds ds-" + st }, st);
}

/** Render a GET /api/jobs/{id}/diff result: summary chips + per-task host transitions. */
function renderJobDiff(box, diff, onBack, onRepick) {
  const sum = diff.summary || {};
  const a = diff.a || {}, b = diff.b || {};

  box.appendChild(el("div", { class: "log-toolbar" },
    el("span", { class: "section-title", style: { margin: 0 } }, "Run diff"),
    el("span", { class: "muted small" },
      el("span", { class: "mono" }, (a.id || "").slice(0, 8)),
      ` (${relTime(a.started || a.created)}) → `,
      el("span", { class: "mono" }, (b.id || "").slice(0, 8)),
      ` (${relTime(b.started || b.created)})`),
    el("div", { class: "grow" }),
    onRepick ? el("button", { class: "btn btn-sm", onclick: onRepick }, icon("diff"), "Compare with…") : null,
    el("a", { href: "#", onclick: (e) => { e.preventDefault(); onBack(); } }, "Back to log")));

  box.appendChild(el("div", { class: "panel diff-summary" },
    el("span", { class: "sum-chip failed" }, el("b", null, String(sum.regressed ?? 0)), el("span", null, "regressed")),
    el("span", { class: "sum-chip ok" }, el("b", null, String(sum.improved ?? 0)), el("span", null, "improved")),
    el("span", { class: "sum-chip changed" }, el("b", null, String(sum.changed ?? 0)), el("span", null, "changed")),
    el("span", { class: "sum-chip" }, el("b", null, String(sum.new_tasks ?? 0)), el("span", null, "new")),
    el("span", { class: "sum-chip" }, el("b", null, String(sum.removed_tasks ?? 0)), el("span", null, "removed")),
    el("span", { class: "sum-chip skipped" }, el("b", null, String(sum.same ?? 0)), el("span", null, "same"))));

  const tasks = diff.tasks || [];
  if (!tasks.length) {
    box.appendChild(el("div", { class: "empty" },
      el("h3", null, "No differences"),
      el("p", null, `All ${sum.same ?? 0} task${(sum.same ?? 0) === 1 ? "" : "s"} behaved identically in both runs.`)));
    return;
  }

  const list = el("div", { class: "panel diff-list" });
  for (const t of tasks) {
    const changes = t.changes || [];
    const regressed = changes.some((c) => c.b === "failed");
    const improved = !regressed && changes.some((c) => c.a === "failed" && c.b === "ok");
    list.appendChild(el("div", { class: "diff-task" + (regressed ? " regressed" : improved ? " improved" : "") },
      el("div", { class: "diff-task-head" },
        el("span", { class: "diff-name mono" }, t.name),
        t.only_in === "b" ? el("span", { class: "flag-badge diff-new" }, "new in this run") : null,
        t.only_in === "a" ? el("span", { class: "flag-badge diff-removed" }, "removed") : null),
      changes.length ? el("div", { class: "diff-hosts" },
        changes.map((c) => el("span", { class: "diff-host mono" },
          el("span", { class: "muted" }, `${c.host}: `),
          diffStatusWord(c.a),
          el("span", { class: "diff-arrow" }, " → "),
          diffStatusWord(c.b)))) : null));
  }
  box.appendChild(list);
}

/* ============================================================
   4l. Plan mode — static "terraform plan" for playbooks
   ============================================================ */

/** Last plan request+result. Plans are not persisted server-side, so the
 *  #/plan route renders from this and redirects away when it's empty. */
let PlanState = null;

let factProfilesPromise = null;
/** Cached GET /api/fact-profiles. */
function getFactProfiles() {
  if (!factProfilesPromise) {
    factProfilesPromise = api("/fact-profiles").catch((e) => {
      factProfilesPromise = null;
      throw e;
    });
  }
  return factProfilesPromise;
}

/** Parse a single value: JSON when possible, bare [a, b] arrays, else string. */
function parseVarValue(s) {
  const v = String(s).trim();
  if (v === "") return "";
  try { return JSON.parse(v); } catch { /* not strict JSON */ }
  if (v.startsWith("[") && v.endsWith("]")) {
    const inner = v.slice(1, -1).trim();
    return inner === "" ? [] : inner.split(",").map((x) => parseVarValue(x));
  }
  return v;
}

/** Vars textarea syntax: a JSON object, or `key = value` / `key: value` lines. */
function parseVarsText(text) {
  const raw = (text || "").trim();
  if (!raw) return {};
  try {
    const obj = JSON.parse(raw);
    if (obj && typeof obj === "object" && !Array.isArray(obj)) return obj;
  } catch { /* fall back to line-based */ }
  const vars = {};
  for (const line of raw.split("\n")) {
    const t = line.trim();
    if (!t || t.startsWith("#") || t.startsWith("//")) continue;
    const m = t.match(/^([^\s:=]+)\s*[:=]\s*(.*)$/);
    if (m) vars[m[1]] = parseVarValue(m[2]);
  }
  return vars;
}

/**
 * Vars editor used by the plan modal and the topology what-if panel:
 * free-form vars textarea + fact-profile select. The last-used text and
 * profile persist per repo under "pine.planvars."+repoId.
 */
function createVarsEditor() {
  const ta = el("textarea", {
    rows: "5", spellcheck: "false",
    placeholder: 'app_version = 2.4.1\nservices: ["docker", "nginx"]\n…or paste a JSON object',
  });
  const profileSel = el("select");
  profileSel.appendChild(el("option", { value: "" }, "No fact profile"));

  let repoId = "";
  let desiredProfile = "";
  let profilesReady = false;

  getFactProfiles().then((profiles) => {
    for (const p of profiles || []) profileSel.appendChild(el("option", { value: p.id }, p.label));
    profilesReady = true;
    profileSel.value = desiredProfile;
    if (profileSel.value !== desiredProfile) profileSel.value = "";
  }).catch(() => {
    profileSel.appendChild(el("option", { value: "", disabled: true }, "(profiles unavailable)"));
  });

  const persist = () => {
    if (!repoId) return;
    try {
      localStorage.setItem("pine.planvars." + repoId,
        JSON.stringify({ text: ta.value, profile: profileSel.value }));
    } catch { /* storage blocked/full */ }
  };
  ta.addEventListener("change", persist);
  profileSel.addEventListener("change", () => { desiredProfile = profileSel.value; persist(); });

  const setRepo = (id) => {
    if ((id || "") === repoId) return;
    repoId = id || "";
    let saved = null;
    try { saved = JSON.parse(localStorage.getItem("pine.planvars." + repoId) || "null"); } catch { /* corrupt */ }
    ta.value = saved && typeof saved.text === "string" ? saved.text : "";
    desiredProfile = (saved && saved.profile) || "";
    if (profilesReady) {
      profileSel.value = desiredProfile;
      if (profileSel.value !== desiredProfile) profileSel.value = "";
    }
  };

  const root = el("div", { class: "vars-editor" },
    el("div", { class: "field" },
      el("label", null, "Extra variables"),
      ta,
      el("span", { class: "hint" }, "JSON object, or one key = value (or key: value) per line — values are parsed as JSON when possible.")),
    el("div", { class: "field", style: { marginBottom: "6px" } },
      el("label", null, "Fact profile"),
      profileSel,
      el("span", { class: "hint" }, "Simulated ansible_facts (OS family, distribution…) used to evaluate conditionals.")));

  return {
    root, setRepo, persist,
    getVars: () => parseVarsText(ta.value),
    getProfile: () => profileSel.value,
  };
}

/** POST /api/plans, remember the result and show it on #/plan. */
async function runPlan(request, btn) {
  if (btn) btn.disabled = true;
  try {
    const result = await api("/plans", { method: "POST", body: JSON.stringify(request) });
    PlanState = { request, result };
    closeModal();
    if (currentRoute()[0] === "plan") route();
    else location.hash = "#/plan";
    return true;
  } catch (e) {
    toast(e.message, "error", "Plan failed");
    return false;
  } finally {
    if (btn) btn.disabled = false;
  }
}

/** Horizontal stacked run/skip/unknown bar (proportional segments). */
function verdictBar(counts, mini = false) {
  const run = (counts && counts.run) || 0;
  const skip = (counts && counts.skip) || 0;
  const unknown = (counts && counts.unknown) || 0;
  const bar = el("div", {
    class: "verdict-bar" + (mini ? " mini" : ""),
    title: `run ${run} · skip ${skip} · unknown ${unknown}`,
  });
  if (run + skip + unknown === 0) return bar;
  const seg = (n, cls) => (n > 0 ? el("span", { class: "vb-seg " + cls, style: { flexGrow: String(n) } }) : null);
  appendChildren(bar, [seg(run, "vb-run"), seg(skip, "vb-skip"), seg(unknown, "vb-unknown")]);
  return bar;
}

function planVerdictPill(status) {
  const s = status || "unknown";
  return el("span", { class: `pill verdict-${s}` }, s);
}

async function pagePlan(page) {
  if (!PlanState) { location.hash = "#/playbooks"; return; }
  const { request, result } = PlanState;
  const s = result.summary || {};

  $("#topbar-title").textContent = `Plan · ${result.playbook}`;

  const replanBtn = el("button", { class: "btn", onclick: () => runPlan(request, replanBtn) },
    icon("sync"), "Re-plan");
  const applyBtn = el("button", {
    class: "btn btn-primary",
    onclick: () => openRunModal({
      repoId: result.repo_id, playbook: result.playbook, inventory: result.inventory,
      limit: request.limit || "", tags: request.tags || "", check: !!result.check,
      vars: request.vars || {}, vaultPassword: request.vault_password || "",
    }),
  }, icon("play"), "Apply (run)");

  page.appendChild(el("div", { class: "page-head" },
    el("a", { href: `#/playbook/${result.repo_id}/${encodePath(result.playbook)}`, class: "btn btn-ghost btn-sm" }, "← Playbook"),
    el("div", null,
      el("h1", null, result.playbook),
      el("div", { class: "muted small" },
        result.repo_name || result.repo_id,
        " · inventory ", el("span", { class: "mono" }, result.inventory || "—"),
        result.fact_profile ? el("span", null, " · facts ", el("span", { class: "mono" }, result.fact_profile)) : null)),
    el("span", {
      class: "flag-badge " + ((result.mode || "estimated") === "exact" ? "exact" : "estimated"),
      title: (result.mode || "estimated") === "exact"
        ? "computed by ansible --check against the real hosts"
        : "computed statically by Pine, without running ansible",
    }, result.mode || "estimated"),
    result.check ? el("span", { class: "flag-badge check" }, "check") : null,
    el("div", { class: "grow" }),
    replanBtn, applyBtn));

  // summary strip: totals + stacked verdict bar
  page.appendChild(el("div", { class: "panel plan-summary" },
    el("span", { class: "sum-chip" }, el("b", null, String(s.hosts ?? 0)), el("span", null, "hosts")),
    el("span", { class: "sum-chip" }, el("b", null, String(s.tasks ?? 0)), el("span", null, "tasks")),
    verdictBar(s),
    el("span", { class: "sum-chip ok" }, el("b", null, String(s.run ?? 0)), el("span", null, "run")),
    el("span", { class: "sum-chip skipped" }, el("b", null, String(s.skip ?? 0)), el("span", null, "skip")),
    el("span", { class: "sum-chip unknown" }, el("b", null, String(s.unknown ?? 0)), el("span", null, "unknown")),
    (result.estimated_duration_ms || 0) > 0 ? el("span", {
      class: "sum-chip", title: "estimated duration, from average task timings of previous runs",
    }, el("b", null, `≈ ${fmtDuration(result.estimated_duration_ms)}`), el("span", null, "est.")) : null));

  // vault panel: decrypt vault-encrypted vars with a password, then re-plan
  const vaultVars = s.vault_vars || [];
  if (vaultVars.length || s.vault_note) {
    const vpw = el("input", { type: "password", placeholder: "ansible-vault password", autocomplete: "off" });
    const vbtn = el("button", { class: "btn btn-primary" }, icon("shield"), "Decrypt & re-plan");
    vbtn.onclick = () => {
      if (!vpw.value) { toast("Enter the vault password", "error"); return; }
      runPlan({ ...request, vault_password: vpw.value }, vbtn);
    };
    vpw.addEventListener("keydown", (e) => { if (e.key === "Enter") vbtn.click(); });
    page.appendChild(el("div", { class: "panel missing-vars" },
      el("div", { class: "mv-head" },
        icon("shield"),
        el("span", { class: "mv-title" }, `${vaultVars.length} vault-encrypted variable${vaultVars.length === 1 ? "" : "s"}`),
        el("span", { class: "muted small" }, s.vault_note || "Enter the ansible-vault password to decrypt these for the plan — used once, never stored.")),
      vaultVars.length ? el("div", { class: "mv-row" }, el("span", { class: "mono mv-name" }, vaultVars.join(", "))) : null,
      el("div", { class: "mv-row" }, vpw, vbtn)));
  }

  // missing-variables panel: fill values inline → merge into vars → re-plan
  const missingInputs = new Map(); // var name -> input element
  const missing = s.missing_vars || [];
  if (missing.length) {
    const replanVarsBtn = el("button", { class: "btn btn-primary" }, icon("sync"), "Re-plan with these values");
    replanVarsBtn.onclick = () => {
      const vars = { ...(request.vars || {}) };
      let any = false;
      for (const [name, input] of missingInputs) {
        const v = input.value.trim();
        if (v === "") continue;
        vars[name] = parseVarValue(v);
        any = true;
      }
      if (!any) { toast("Provide at least one value first", "error"); return; }
      runPlan({ ...request, vars }, replanVarsBtn);
    };
    page.appendChild(el("div", { class: "panel missing-vars" },
      el("div", { class: "mv-head" },
        icon("question"),
        el("span", { class: "mv-title" }, `${missing.length} missing variable${missing.length === 1 ? "" : "s"}`),
        el("span", { class: "muted small" }, "Unresolved variables leave verdicts uncertain — fill in values and re-plan.")),
      missing.map((mv) => {
        const input = el("input", { type: "text", placeholder: "value — JSON or plain string" });
        input.addEventListener("keydown", (e) => { if (e.key === "Enter") replanVarsBtn.click(); });
        missingInputs.set(mv.name, input);
        return el("div", { class: "mv-row" },
          el("span", { class: "mono mv-name" }, mv.name),
          el("span", { class: "chip warn", title: "verdicts affected by this variable" }, `×${mv.count ?? 0}`),
          input);
      }),
      el("div", { class: "row", style: { justifyContent: "flex-end", marginTop: "10px" } }, replanVarsBtn)));
  }

  const focusMissingVar = (name) => {
    const input = missingInputs.get(name);
    if (!input) { toast(`“${name}” is not in the missing-variables panel`, "error"); return; }
    input.scrollIntoView({ behavior: "smooth", block: "center" });
    input.focus({ preventScroll: true });
    input.classList.add("flash");
    setTimeout(() => input.classList.remove("flash"), 1000);
  };

  const plays = result.plays || [];
  if (!plays.length) {
    page.appendChild(el("div", { class: "empty" },
      el("h3", null, "Nothing planned"),
      el("p", null, "The plan contains no plays. Check the playbook, inventory and limit, then re-plan.")));
    return;
  }
  const totalMs = result.estimated_duration_ms || 0;
  plays.forEach((play, idx) => page.appendChild(renderPlanPlay(play, idx, focusMissingVar, totalMs)));
}

function renderPlanPlay(play, idx, focusMissingVar, totalMs) {
  // pure import plays carry no tasks — render a thin pass-through row
  if (play.import) {
    return el("div", { class: "plan-import-row" },
      icon("code"),
      el("span", null, play.name || `Play ${idx + 1}`),
      el("span", null, "imports →"),
      el("span", { class: "mono", style: { color: "var(--secondary)" } }, play.import));
  }

  const matched = play.matched_hosts || [];
  const batches = play.batches || [];
  const head = el("div", { class: "play-head" },
    el("span", { class: "play-name" }, play.name || `Play ${idx + 1}`),
    el("span", { class: "hosts-label" }, icon("host"), "hosts:"),
    el("span", { class: "hosts-pat", title: "Host pattern this play targets" }, play.hosts || "all"),
    el("span", { class: "chip", title: matched.length ? matched.join(", ") : "No hosts matched" },
      `${matched.length} matched`),
    batches.length > 1 ? el("span", {
      class: "chip warn",
      title: "serial — rolling batches:\n" + batches.map((b, i) => `batch ${i + 1}: ${b.join(", ")}`).join("\n"),
    }, `serial · ${batches.length} batches`) : null);

  const body = el("div", { class: "plan-body" });
  const tasks = play.tasks || [];
  if (!tasks.length) {
    body.appendChild(el("div", { class: "muted small", style: { padding: "6px" } }, "No tasks in this play."));
  }
  for (const t of tasks) body.appendChild(renderPlanTaskRow(t, focusMissingVar, totalMs));

  const handlers = play.handlers || [];
  if (handlers.length) {
    body.appendChild(el("div", { class: "flow-phase-label handlers-lbl", style: { margin: "16px 6px 8px" } }, "handlers"));
    for (const h of handlers) {
      const hosts = h.hosts || [];
      body.appendChild(el("div", { class: "plan-handler" },
        el("span", { class: `module mod-${moduleCategory(h.module)}`, title: h.module || "" }, shortModule(h.module)),
        el("span", { class: "pt-name" }, h.name),
        h.uncertain ? el("span", {
          class: "chip warn", style: { cursor: "help" },
          title: "Triggered only by tasks whose changed-state can't be known statically",
        }, "uncertain") : null,
        (h.triggered_by || []).length
          ? el("span", { class: "muted small" }, "← ", h.triggered_by.join(", "))
          : null,
        el("div", { class: "grow" }),
        el("span", { class: "chip", title: hosts.join(", ") }, `${hosts.length} host${hosts.length === 1 ? "" : "s"}`)));
    }
  }
  return el("div", { class: "play-section" }, head, body);
}

function renderPlanTaskRow(task, focusMissingVar, totalMs) {
  const counts = task.counts || {};
  const avgMs = task.avg_ms || 0;
  const hotTask = avgMs > 0 && (totalMs || 0) > 0 && avgMs > totalMs * 0.3;
  const templated = task.raw_name && task.raw_name !== task.name;
  const row = el("div", { class: "plan-task" });
  let detail = null;
  const toggle = () => {
    if (detail) { detail.remove(); detail = null; row.classList.remove("open"); return; }
    row.classList.add("open");
    detail = el("div", { class: "plan-host-detail" }, planHostTable(task, focusMissingVar));
    row.appendChild(detail);
  };

  const chips = el("div", { class: "pt-chips" });
  (task.tags || []).forEach((t) => chips.appendChild(el("span", { class: "chip tag" }, t)));
  if (task.when) chips.appendChild(whenChip(task.when));
  (task.notify || []).forEach((n) => chips.appendChild(
    el("span", { class: "chip notify-chip", title: `notifies handler “${n}”` }, `→ ${n}`)));

  row.appendChild(el("div", { class: "plan-task-row", onclick: toggle },
    el("span", { class: "plan-section-lbl" }, task.section || "tasks"),
    el("div", { class: "pt-main" },
      el("div", { class: "pt-title" },
        task.role ? el("span", { class: "chip green", title: `from role “${task.role}”` }, icon("role"), task.role) : null,
        el("span", { class: `module mod-${moduleCategory(task.module)}`, title: task.module || "" }, shortModule(task.module)),
        el("span", {
          class: "pt-name" + (templated ? " templated" : ""),
          title: templated ? `raw: ${task.raw_name}` : null,
        }, task.name || "(unnamed task)"),
        task.loop_items ? el("span", {
          class: "chip loop-chip",
          title: task.loop_items === -1 ? "Loop of unknown size" : `Loop over ${task.loop_items} item${task.loop_items === 1 ? "" : "s"}`,
        }, task.loop_items === -1 ? "loop ?" : `×${task.loop_items}`) : null),
      task.args ? el("div", {
        class: "pt-args mono" + (task.raw_args ? " templated" : ""),
        title: task.raw_args ? `raw: ${task.raw_args}` : null,
      }, task.args) : null,
      chips.childNodes.length ? chips : null,
      task.check_note ? el("div", { class: "pt-checknote" }, task.check_note) : null),
    el("div", { class: "pt-verdict" },
      avgMs > 0 ? el("span", {
        class: "chip pt-dur" + (hotTask ? " warn" : ""),
        title: hotTask
          ? "average duration from previous runs — more than 30% of the estimated total"
          : "average duration from previous runs",
      }, `≈ ${fmtDuration(avgMs)}`) : null,
      verdictBar(counts, true),
      el("span", { class: "pt-counts mono" },
        el("b", { class: "c-run" }, String(counts.run || 0)), " · ",
        el("b", { class: "c-skip" }, String(counts.skip || 0)), " · ",
        el("b", { class: "c-unknown" }, String(counts.unknown || 0))))));
  return row;
}

/** Expanded per-host verdict table for a planned task. */
function planHostTable(task, focusMissingVar) {
  const entries = Object.entries(task.hosts || {});
  if (!entries.length) {
    return el("div", { class: "muted small", style: { padding: "2px 0 8px" } }, "No per-host verdicts for this task.");
  }
  entries.sort((a, b) => a[0].localeCompare(b[0]));
  return el("div", { class: "table-wrap" },
    el("table", { class: "data plan-hosts" },
      el("thead", null, el("tr", null, ["Host", "Verdict", "Why"].map((h) => el("th", null, h)))),
      el("tbody", null, entries.map(([host, v]) => {
        const missing = (v && v.missing) || [];
        return el("tr", null,
          el("td", { class: "mono" }, host),
          el("td", null, planVerdictPill(v && v.status)),
          el("td", null,
            v && v.reason ? el("span", { class: "muted mono small" }, v.reason) : null,
            missing.length ? el("span", { class: "row", style: { gap: "5px", display: "inline-flex", marginLeft: v && v.reason ? "8px" : "0" } },
              el("span", { class: "muted small" }, "missing:"),
              missing.map((m) => el("span", {
                class: "chip warn link", title: "Jump to this variable in the missing-variables panel",
                onclick: (e) => { e.stopPropagation(); focusMissingVar(m); },
              }, m))) : null,
            !(v && v.reason) && !missing.length ? el("span", { class: "muted small" }, "—") : null));
      }))));
}

/* ============================================================
   5. Run-playbook modal
   ============================================================ */

async function openRunModal(prefill = {}) {
  try { await loadRepos(); } catch (e) { toast(e.message, "error"); return; }
  if (!State.repos.length) {
    toast("Connect a repository first", "error", "No repositories");
    location.hash = "#/repos";
    setTimeout(openAddRepoModal, 60);
    return;
  }

  const repoSel = el("select");
  const pbSel = el("select");
  const invSel = el("select");
  const hostSel = el("select", { title: "Pick a single host to test against (sets --limit)" });
  const limitIn = el("input", { type: "text", placeholder: "or a pattern, e.g. web01,db*" });
  const tagsIn = el("input", { type: "text", placeholder: "e.g. config,deploy (optional)" });
  let scanInvs = [];
  // fill the host picker from the selected inventory. When a playbook is chosen
  // we use the resolve endpoint, which flags the hosts the playbook targets — so
  // they're grouped first, like the "resolve as" picker.
  const syncHostSel = () => {
    hostSel.value = [...hostSel.options].some((o) => o.value && o.value === limitIn.value.trim()) ? limitIn.value.trim() : "";
  };
  const fillHosts = async () => {
    const invVal = invSel.value, pb = pbSel.value;
    hostSel.innerHTML = ""; hostSel.appendChild(el("option", { value: "" }, "Loading hosts…")); hostSel.disabled = true;
    let hosts = [];
    if (pb && invVal) {
      try {
        const q = new URLSearchParams({ playbook: pb, inventory: invVal });
        const rr = await api(`/repos/${repoSel.value}/resolve?${q.toString()}`);
        const invObj = scanInvs.find((i) => (i.path || i.name) === invVal);
        const invName = invObj ? invObj.name : invVal;
        const inv = (rr.inventories || []).find((i) => i.name === invName) || (rr.inventories || [])[0];
        hosts = (inv && inv.hosts) || []; // {name, targeted, varies}, already targeted-first
        renderVault(rr.vault_vars || []);
      } catch { renderVault([]); }
    }
    if (!hosts.length) {
      const invObj = scanInvs.find((i) => (i.path || i.name) === invVal) || scanInvs.find((i) => i.name === invVal);
      hosts = ((invObj && invObj.hosts) || []).map((h) => ({ name: h.name, targeted: false }));
    }
    hostSel.innerHTML = "";
    hostSel.appendChild(el("option", { value: "" }, hosts.length ? "— all targeted hosts —" : "— no inventory hosts —"));
    const targeted = hosts.filter((h) => h.targeted), others = hosts.filter((h) => !h.targeted);
    const addGroup = (label, list) => {
      if (!list.length) return;
      const og = el("optgroup", { label });
      list.forEach((h) => og.appendChild(el("option", { value: h.name }, h.name)));
      hostSel.appendChild(og);
    };
    if (targeted.length) { addGroup("targeted by this playbook", targeted); addGroup("other hosts", others); }
    else others.forEach((h) => hostSel.appendChild(el("option", { value: h.name }, h.name)));
    hostSel.disabled = !hosts.length;
    syncHostSel();
  };
  hostSel.onchange = () => { if (hostSel.value) limitIn.value = hostSel.value; else if ([...hostSel.options].some((o) => o.value === limitIn.value.trim())) limitIn.value = ""; };
  limitIn.addEventListener("input", () => syncHostSel());
  const checkBox = el("input", { type: "checkbox" });
  // plan mode: estimated (static, instant) vs exact (ansible --check)
  let planMode = "estimated";
  const modeEstTab = el("span", { class: "tab active", title: "Computed statically by Pine — instant, nothing is executed" }, "Estimated (static)");
  const modeExactTab = el("span", { class: "tab", title: "Runs ansible-playbook --check against the real hosts" }, "Exact (--check via ansible)");
  const setPlanMode = (m) => {
    planMode = m;
    modeEstTab.classList.toggle("active", m === "estimated");
    modeExactTab.classList.toggle("active", m === "exact");
  };
  modeEstTab.onclick = () => setPlanMode("estimated");
  modeExactTab.onclick = () => setPlanMode("exact");
  const runBtn = el("button", { class: "btn btn-primary" }, icon("play"), "Launch");
  const planBtn = el("button", {
    class: "btn btn-secondary",
    title: "Estimate what this run would do — computed statically, nothing is executed",
  }, icon("clipboard"), "Plan");
  const varsEd = createVarsEditor();

  // vars_prompt inputs (asked at runtime) + an ansible-vault password field,
  // both feeding the plan so prompted/vaulted values resolve.
  let scanPlaybooks = [];
  const promptInputs = new Map();
  const promptsBox = el("div", { class: "field", style: { display: "none" } });
  const vaultPwIn = el("input", { type: "password", placeholder: "ansible-vault password", autocomplete: "off" });
  const vaultBox = el("div", { class: "field", style: { display: "none" } });
  const fillPrompts = () => {
    const pb = scanPlaybooks.find((p) => p.path === pbSel.value);
    const prompts = [], seen = new Set();
    for (const play of (pb && pb.plays) || []) {
      for (const pr of play.vars_prompt || []) {
        if (pr.name && !seen.has(pr.name)) { seen.add(pr.name); prompts.push(pr); }
      }
    }
    promptInputs.clear();
    promptsBox.innerHTML = "";
    if (!prompts.length) { promptsBox.style.display = "none"; return; }
    promptsBox.style.display = "";
    promptsBox.appendChild(el("label", null, "Prompted variables"));
    promptsBox.appendChild(el("span", { class: "hint" }, "This playbook asks for these at runtime (vars_prompt) — provide them to resolve the plan."));
    for (const pr of prompts) {
      const inp = el("input", { type: pr.private ? "password" : "text", value: pr.default || "", placeholder: pr.default ? `default: ${pr.default}` : "(required)" });
      promptInputs.set(pr.name, inp);
      promptsBox.appendChild(el("div", { class: "prompt-row" },
        el("label", { class: "mono small", title: pr.prompt || pr.name }, pr.name), inp));
    }
  };
  const collectPromptVars = () => {
    const out = {};
    for (const [name, inp] of promptInputs) { const v = inp.value.trim(); if (v) out[name] = v; }
    return out;
  };
  const renderVault = (vv) => {
    vaultBox.innerHTML = "";
    if (!vv || !vv.length) { vaultBox.style.display = "none"; return; }
    vaultBox.style.display = "";
    vaultBox.appendChild(el("label", null, "Vault password"));
    vaultBox.appendChild(el("span", { class: "hint" },
      `${vv.length} vault-encrypted variable${vv.length === 1 ? "" : "s"} in scope (${vv.join(", ")}). Enter the ansible-vault password to decrypt them for this plan — used once, never stored.`));
    vaultBox.appendChild(vaultPwIn);
  };

  for (const r of State.repos) repoSel.appendChild(el("option", { value: r.id }, r.name));
  repoSel.value = prefill.repoId || State.repoId || State.repos[0].id;
  varsEd.setRepo(repoSel.value);
  limitIn.value = prefill.limit || "";
  tagsIn.value = prefill.tags || "";
  checkBox.checked = !!prefill.check;
  vaultPwIn.value = prefill.vaultPassword || "";

  const fillScanOptions = async () => {
    pbSel.innerHTML = ""; invSel.innerHTML = "";
    pbSel.appendChild(el("option", { value: "" }, "Loading…"));
    invSel.appendChild(el("option", { value: "" }, "Loading…"));
    pbSel.disabled = invSel.disabled = runBtn.disabled = planBtn.disabled = true;
    try {
      const scan = await getScan(repoSel.value);
      pbSel.innerHTML = ""; invSel.innerHTML = "";
      const pbs = scan.playbooks || [];
      const invs = scan.inventories || [];
      scanInvs = invs;
      scanPlaybooks = pbs;
      if (!pbs.length) pbSel.appendChild(el("option", { value: "" }, "No playbooks found"));
      for (const p of pbs) pbSel.appendChild(el("option", { value: p.path }, `${p.name || p.path}  (${p.path})`));
      if (!invs.length) invSel.appendChild(el("option", { value: "" }, "No inventories found"));
      for (const i of invs) invSel.appendChild(el("option", { value: i.path || i.name }, i.name));
      if (prefill.playbook && pbs.some((p) => p.path === prefill.playbook)) pbSel.value = prefill.playbook;
      if (prefill.inventory) {
        const opt = [...invSel.options].find((o) => o.value === prefill.inventory);
        if (opt) invSel.value = prefill.inventory;
      }
      pbSel.disabled = !pbs.length;
      invSel.disabled = !invs.length;
      runBtn.disabled = planBtn.disabled = !pbs.length;
      fillPrompts();
      fillHosts();
    } catch (e) {
      pbSel.innerHTML = ""; invSel.innerHTML = "";
      pbSel.appendChild(el("option", { value: "" }, "Scan failed"));
      invSel.appendChild(el("option", { value: "" }, "Scan failed"));
      toast(e.message, "error");
    }
  };
  repoSel.addEventListener("change", () => { varsEd.setRepo(repoSel.value); fillScanOptions(); });
  invSel.addEventListener("change", fillHosts);
  pbSel.addEventListener("change", () => { fillPrompts(); fillHosts(); });

  runBtn.onclick = async () => {
    if (!pbSel.value) { toast("Pick a playbook to run", "error"); return; }
    varsEd.persist();
    runBtn.disabled = true;
    try {
      const job = await api("/jobs", {
        method: "POST",
        body: JSON.stringify({
          repo_id: repoSel.value,
          playbook: pbSel.value,
          inventory: invSel.value || "",
          limit: limitIn.value.trim(),
          tags: tagsIn.value.trim(),
          check: checkBox.checked,
          vars: { ...collectPromptVars(), ...varsEd.getVars() },
          ...(vaultPwIn.value ? { vault_password: vaultPwIn.value } : {}),
        }),
      });
      closeModal();
      toast(`${job.playbook} queued`, "success", "Job started");
      location.hash = `#/job/${job.id}`;
    } catch (e) {
      runBtn.disabled = false;
      toast(e.message, "error");
    }
  };

  planBtn.onclick = async () => {
    if (!pbSel.value) { toast("Pick a playbook to plan", "error"); return; }
    varsEd.persist();
    await runPlan({
      repo_id: repoSel.value,
      playbook: pbSel.value,
      inventory: invSel.value || "",
      limit: limitIn.value.trim(),
      tags: tagsIn.value.trim(),
      check: checkBox.checked,
      vars: { ...collectPromptVars(), ...varsEd.getVars() },
      host_vars: {},
      fact_profile: varsEd.getProfile(),
      ...(vaultPwIn.value ? { vault_password: vaultPwIn.value } : {}),
      ...(planMode === "exact" ? { mode: "exact" } : {}),
    }, planBtn);
  };

  // collapsible vars + fact-profile section (used by Plan; runs ignore it)
  let varsOpen = !!prefill.plan;
  const varsCaret = el("span", { class: "collapse-caret" + (varsOpen ? " open" : "") }, "▸");
  const varsBody = el("div", { style: { display: varsOpen ? "" : "none" } },
    varsEd.root,
    el("span", { class: "hint", style: { display: "block" } },
      "Passed to Plan (to resolve templates) and to Run (as -e extra vars), alongside any prompted variables."));
  const varsHead = el("div", {
    class: "collapse-head",
    onclick: () => {
      varsOpen = !varsOpen;
      varsCaret.classList.toggle("open", varsOpen);
      varsBody.style.display = varsOpen ? "" : "none";
    },
  }, varsCaret, "Variables & facts");

  const body = el("div", null,
    el("div", { class: "field" }, el("label", null, "Repository"), repoSel),
    el("div", { class: "field" }, el("label", null, "Playbook"), pbSel),
    el("div", { class: "field" }, el("label", null, "Inventory"), invSel,
      el("span", { class: "hint" }, "Inventory the play targets.")),
    el("div", { class: "field" }, el("label", null, "Limit to host"),
      el("div", { style: { display: "grid", gridTemplateColumns: "1fr 1fr", gap: "10px" } }, hostSel, limitIn),
      el("span", { class: "hint" }, "Pick a host to test against, or type a pattern (web01,db*). Empty = all targeted hosts.")),
    el("div", { class: "field" }, el("label", null, "Tags"), tagsIn),
    promptsBox,
    vaultBox,
    el("label", { class: "check-row" }, checkBox,
      el("span", null, "Check mode ", el("span", { class: "muted" }, "(dry run, no changes applied)"))),
    el("div", { class: "field", style: { margin: "14px 0 0" } },
      el("label", null, "Plan mode"),
      el("div", { class: "seg-tabs" }, modeEstTab, modeExactTab),
      el("span", { class: "hint" }, "How Plan computes verdicts — estimated is static and instant; exact runs ansible-playbook --check against the real hosts. Launch is unaffected.")),
    varsHead, varsBody);

  openModal({
    title: "Run playbook",
    body,
    footer: [el("button", { class: "btn", onclick: closeModal }, "Cancel"), planBtn, runBtn],
  });
  fillScanOptions();
}

/* ============================================================
   6. Keyboard shortcuts + boot
   ============================================================ */

let gPressed = false, gTimer = 0;

document.addEventListener("keydown", (e) => {
  if (e.key === "Escape") {
    if (activeModal) { closeModal(); return; }
    // on the plan view, Escape goes back to the playbook detail
    if (currentRoute()[0] === "plan" && PlanState) {
      location.hash = `#/playbook/${PlanState.result.repo_id}/${encodePath(PlanState.result.playbook)}`;
    }
    return;
  }
  const tag = (e.target.tagName || "").toLowerCase();
  if (tag === "input" || tag === "select" || tag === "textarea" || e.metaKey || e.ctrlKey || e.altKey) return;
  if (e.key === "g") {
    gPressed = true;
    clearTimeout(gTimer);
    gTimer = setTimeout(() => (gPressed = false), 900);
    return;
  }
  if (gPressed) {
    const map = { d: "dashboard", r: "repos", p: "playbooks", o: "roles", i: "inventory", t: "topology", h: "hygiene", m: "impact", w: "drift", v: "services", s: "schedules", l: "pipelines", j: "jobs" };
    if (map[e.key]) { location.hash = "#/" + map[e.key]; e.preventDefault(); }
    gPressed = false;
  } else if (e.key === "n") {
    openRunModal();
  }
});

$("#repo-select").addEventListener("change", (e) => setRepo(e.target.value));
$("#topbar-run").addEventListener("click", () => openRunModal());
window.addEventListener("hashchange", route);

(async function boot() {
  if (!location.hash) location.hash = "#/dashboard";
  try { await loadRepos(); } catch { /* page handlers surface errors */ }
  route();
})();
