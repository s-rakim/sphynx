// Sphynx admin UI. Plain JavaScript, no build step. All user data is put in
// the page with textContent (via h()), never innerHTML.
"use strict";

// ---------- small DOM helpers ----------

function h(tag, props, ...children) {
  const el = document.createElement(tag);
  if (props) {
    for (const [k, v] of Object.entries(props)) {
      if (v === undefined || v === null || v === false) continue;
      if (k === "class") el.className = v;
      else if (k === "text") el.textContent = v;
      else if (k === "style") el.style.cssText = v;
      else if (k.startsWith("on") && typeof v === "function") el.addEventListener(k.slice(2), v);
      else if (k === "value") el.value = v;
      else if (k === "checked") el.checked = !!v;
      else if (k === "disabled") el.disabled = !!v;
      else if (k === "selected") el.selected = !!v;
      else el.setAttribute(k, v === true ? "" : v);
    }
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c instanceof Node ? c : document.createTextNode(String(c)));
  }
  return el;
}

const ICONS = {
  overview: "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z",
  devices: "M3 5h18v11H3zM8 20h8M12 16v4",
  add: "M12 5v14M5 12h14",
  users: "M16 19v-1a4 4 0 0 0-4-4H6a4 4 0 0 0-4 4v1M9 10a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM22 19v-1a4 4 0 0 0-3-3.87M16 4.13a3 3 0 0 1 0 5.75",
  access: "M12 3l8 3v6c0 5-3.5 8-8 9-4.5-1-8-4-8-9V6z M9 12l2 2 4-4",
  dns: "M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM3 12h18M12 3c2.5 2.5 3.5 5.5 3.5 9s-1 6.5-3.5 9c-2.5-2.5-3.5-5.5-3.5-9s1-6.5 3.5-9z",
  routes: "M6 3v12M6 15a3 3 0 1 0 0 6 3 3 0 0 0 0-6zM18 9a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM18 9c0 6-12 3-12 6",
  wol: "M12 3v8M7.5 6.5a7 7 0 1 0 9 0",
  link: "M10 14a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1M14 10a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1",
  share: "M12 15V3M8 7l4-4 4 4M5 13v6h14v-6",
  chat: "M21 11.5a8.5 8.5 0 0 1-12.6 7.44L3 20.5l1.6-5.1A8.5 8.5 0 1 1 21 11.5z",
  keys: "M15 7a4 4 0 1 1-3.87 5H3v3h3v2h3v-2h2.13A4 4 0 0 1 15 7z",
  settings: "M4 6h10M18 6h2M4 12h4M12 12h8M4 18h12M20 18h0M14 4v4M8 10v4M16 16v4",
  logs: "M5 5h14M5 10h14M5 15h10M5 20h7",
  copy: "M9 9h11v11H9zM5 15H4V4h11v1",
  menu: "M4 6h16M4 12h16M4 18h16",
  close: "M6 6l12 12M18 6L6 18",
  refresh: "M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7",
  radio: "M12 13v8M8 21h8M8.5 9.5a5 5 0 0 1 7 0M5.6 6.6a9 9 0 0 1 12.8 0M12 13a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3z",
};

function icon(name) {
  const ns = "http://www.w3.org/2000/svg";
  const svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  svg.setAttribute("fill", "none");
  svg.setAttribute("stroke", "currentColor");
  svg.setAttribute("stroke-width", "1.8");
  svg.setAttribute("stroke-linecap", "round");
  svg.setAttribute("stroke-linejoin", "round");
  svg.setAttribute("aria-hidden", "true");
  const p = document.createElementNS(ns, "path");
  p.setAttribute("d", ICONS[name] || "");
  svg.append(p);
  return svg;
}

// ---------- API ----------

class ApiError extends Error {
  constructor(msg, status) { super(msg); this.status = status; }
}

async function api(method, path, body) {
  const opts = { method, headers: { "X-Sphynx": "1" }, credentials: "same-origin" };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch (e) {
    throw new ApiError("Can't reach the Sphynx server. Is the container running?", 0);
  }
  let data = null;
  const text = await res.text();
  if (text) {
    try { data = JSON.parse(text); } catch { data = { error: text }; }
  }
  if (!res.ok) {
    if (res.status === 401 && path !== "/api/login" && path !== "/api/password") {
      state.session = null;
      boot();
    }
    throw new ApiError((data && data.error) || res.statusText, res.status);
  }
  return data;
}

// Runs fn, showing errors as a toast. Disables the button while busy.
async function run(btn, fn, okMsg) {
  if (btn) btn.disabled = true;
  try {
    const r = await fn();
    if (okMsg) toast(okMsg);
    return r;
  } catch (e) {
    toast(e.message, true);
    throw e;
  } finally {
    if (btn) btn.disabled = false;
  }
}

function toast(msg, bad) {
  const t = h("div", { class: "toast" + (bad ? " bad" : ""), text: msg });
  document.getElementById("toasts").append(t);
  setTimeout(() => t.remove(), bad ? 7000 : 3500);
}

// ---------- formatting ----------

function isZeroTime(t) { return !t || t.startsWith("0001-") || new Date(t).getFullYear() < 1971; }

function timeAgo(t) {
  if (isZeroTime(t)) return "never";
  const s = (Date.now() - new Date(t).getTime()) / 1000;
  const future = s < 0;
  const a = Math.abs(s);
  let v;
  if (a < 45) v = "a few seconds";
  else if (a < 90) v = "a minute";
  else if (a < 3600) v = Math.round(a / 60) + " minutes";
  else if (a < 5400) v = "an hour";
  else if (a < 86400) v = Math.round(a / 3600) + " hours";
  else if (a < 2 * 86400) v = "a day";
  else if (a < 45 * 86400) v = Math.round(a / 86400) + " days";
  else if (a < 365 * 86400) v = Math.round(a / (30 * 86400)) + " months";
  else v = Math.round(a / (365 * 86400)) + " years";
  return future ? "in " + v : v + " ago";
}

function fmtDate(t) {
  if (isZeroTime(t)) return "—";
  return new Date(t).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

function fmtBytes(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i ? n.toFixed(1) : n) + " " + u[i];
}

function devName(d) { return d.givenName || d.name || "device " + d.id; }
function ipv4(d) { return (d.ipAddresses || []).find((ip) => !ip.includes(":")) || (d.ipAddresses || [])[0] || ""; }
function userName(u) { return u ? (u.displayName && u.displayName !== u.name ? `${u.displayName} (${u.name})` : u.name) : "—"; }

// ---------- building blocks ----------

function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    navigator.clipboard.writeText(text).then(() => toast("Copied"), () => fallbackCopy(text));
  } else fallbackCopy(text);
}

function fallbackCopy(text) {
  const ta = h("textarea", { style: "position:fixed;opacity:0" });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  try { document.execCommand("copy"); toast("Copied"); } catch { toast("Copy failed, select the text instead", true); }
  ta.remove();
}

function copyButton(text, label) {
  return h("button", { class: "small ghost", title: "Copy", onclick: (e) => { e.stopPropagation(); copyText(text); } }, icon("copy"), label || null);
}

function codebox(text) {
  return h("div", { class: "codebox" }, text, h("button", { class: "copy ghost", title: "Copy", onclick: () => copyText(text) }, icon("copy")));
}

function field(label, input, help) {
  const id = input.id || "f" + Math.random().toString(36).slice(2, 9);
  input.id = id;
  return h("div", { class: "field" }, h("label", { for: id, text: label }), input, help ? h("div", { class: "help" }, help) : null);
}

function textInput(value, attrs) { return h("input", Object.assign({ type: "text", value: value ?? "" }, attrs || {})); }

function select(options, value, attrs) {
  const s = h("select", attrs || {});
  for (const o of options) {
    const [v, label] = Array.isArray(o) ? o : [o, o];
    s.append(h("option", { value: v, text: label, selected: String(v) === String(value) }));
  }
  return s;
}

function switchInput(checked, onchange) {
  const input = h("input", { type: "checkbox", checked });
  if (onchange) input.addEventListener("change", () => onchange(input.checked, input));
  return { el: h("label", { class: "switch" }, input, h("span")), input };
}

function toggleRow(title, desc, checked, onchange) {
  const sw = switchInput(checked, onchange);
  const row = h("div", { class: "toggle-row" }, h("div", null, h("div", { class: "t", text: title }), desc ? h("div", { class: "d" }, desc) : null), sw.el);
  row.input = sw.input;
  return row;
}

function checkbox(title, desc, checked) {
  const input = h("input", { type: "checkbox", checked });
  const el = h("label", { class: "check" }, input, h("div", null, h("div", { class: "t", text: title }), desc ? h("div", { class: "d" }, desc) : null));
  el.input = input;
  return el;
}

function badge(text, kind) { return h("span", { class: "badge" + (kind ? " " + kind : ""), text }); }

function statusBadge(online, lastSeen) {
  return online
    ? h("span", { class: "badge ok" }, h("span", { class: "dot on" }), "Online")
    : h("span", { class: "badge", title: lastSeen ? "Last seen " + fmtDate(lastSeen) : "" }, h("span", { class: "dot" }), lastSeen && !isZeroTime(lastSeen) ? "Seen " + timeAgo(lastSeen) : "Offline");
}

function linkBadge(d) {
  if (!d.online) return null;
  switch (d.link) {
    case "direct": return h("span", { class: "badge ok", title: "Direct WireGuard path to Sphynx home: " + (d.linkAddr || "") }, "Direct");
    case "peer-relay": return h("span", { class: "badge accent", title: "Using a UDP peer relay: " + (d.linkAddr || "") }, "Peer relay");
    case "relay": return h("span", { class: "badge warn", title: "Relayed over HTTPS via DERP region " + (d.relayRegion || "?") }, "Relayed" + (d.relayRegion ? " · " + d.relayRegion : ""));
    default: return null;
  }
}

function notice(kind, ...children) { return h("div", { class: "notice " + kind }, ...children); }

function card(title, sub, ...children) {
  return h("section", { class: "card" }, title ? h("h2", { text: title }) : null, sub ? h("p", { class: "sub" }, sub) : null, ...children);
}

function pageHead(title, sub, ...actions) {
  return h("div", { class: "page-head" },
    h("div", null, h("h1", { text: title }), sub ? h("p", null, sub) : null),
    actions.length ? h("div", { class: "actions" }, ...actions) : null);
}

function empty(title, text, ...actions) {
  return h("div", { class: "empty" }, h("h3", { text: title }), text ? h("p", null, text) : null, ...actions);
}

// modal shows a dialog and resolves when it closes.
function modal({ title, body, buttons = [], wide = false, onOpen }) {
  return new Promise((resolve) => {
    const dlg = h("dialog", { class: wide ? "wide" : "" });
    const close = (v) => { dlg.close(); dlg.remove(); resolve(v); };
    const foot = h("div", { class: "dfoot" });
    for (const b of buttons) {
      foot.append(h("button", {
        class: b.class || "",
        onclick: async (e) => {
          if (!b.action) return close(b.value);
          const btn = e.currentTarget;
          btn.disabled = true;
          try {
            const r = await b.action();
            if (r !== false) close(r === undefined ? b.value : r);
          } catch (err) {
            toast(err.message, true);
          } finally {
            btn.disabled = false;
          }
        },
      }, b.text));
    }
    append(dlg, [
      h("div", { class: "dhead" }, h("h2", { text: title }), h("button", { class: "ghost icon", title: "Close", onclick: () => close(undefined) }, icon("close"))),
      h("div", { class: "dbody" }, body),
      buttons.length ? foot : null,
    ]);
    dlg.addEventListener("cancel", (e) => { e.preventDefault(); close(undefined); });
    document.body.append(dlg);
    dlg.showModal();
    if (onOpen) onOpen(dlg);
  });
}

function confirmAsk(title, message, confirmText = "Confirm", danger = false) {
  return modal({
    title,
    body: h("p", null, message),
    buttons: [
      { text: "Cancel", value: false },
      { text: confirmText, class: danger ? "danger solid" : "primary", value: true },
    ],
  });
}

function promptAsk(title, label, value, help) {
  const input = textInput(value);
  return modal({
    title,
    body: field(label, input, help),
    buttons: [{ text: "Cancel", value: null }, { text: "Save", class: "primary", action: () => input.value }],
    onOpen: () => { input.focus(); input.select(); },
  });
}

// listEditor edits a list of strings (one per line).
function listEditor(values, placeholder, rows = 3) {
  const ta = h("textarea", { rows, placeholder, style: "font-family:var(--mono)" });
  ta.value = (values || []).join("\n");
  ta.getList = () => ta.value.split(/[\n,]+/).map((s) => s.trim()).filter(Boolean);
  return ta;
}

// ---------- app state, layout and routing ----------

const state = { session: null, timer: null, users: null };

const NAV = [
  ["overview", "Overview", "overview"],
  ["devices", "Devices", "devices"],
  ["add", "Add device", "add"],
  ["users", "Users", "users"],
  null,
  ["access", "Access controls", "access"],
  ["routes", "Exit nodes & routes", "routes"],
  ["dns", "DNS", "dns"],
  ["keys", "Keys", "keys"],
  null,
  ["wol", "Wake-on-LAN", "wol"],
  ["chat", "Chat", "chat"],
  ["link", "Sphynx Link", "link"],
  ["radio", "Radio", "radio"],
  ["shares", "Public shares", "share"],
  null,
  ["settings", "Settings", "settings"],
  ["logs", "Logs", "logs"],
];

const PAGES = {};

function route() {
  const parts = (location.hash.replace(/^#\/?/, "") || "overview").split("/");
  return { name: parts[0], arg: parts.slice(1).join("/") };
}

function go(path) { location.hash = "#/" + path; }

let layout = null;

function buildLayout() {
  const nav = h("nav", { class: "nav" });
  for (const item of NAV) {
    if (!item) { nav.append(h("div", { class: "sep" })); continue; }
    const [name, label, ic] = item;
    nav.append(h("a", { href: "#/" + name, "data-name": name, onclick: () => closeMenu() }, icon(ic), label));
  }
  const sidebar = h("aside", { class: "sidebar" },
    h("div", { class: "brand" }, h("img", { src: "icon.svg", alt: "" }), "Sphynx"),
    nav,
    h("div", { class: "foot" },
      h("div", { text: "Sphynx " + (state.session?.version || "") }),
      h("button", { class: "small", onclick: async () => { await api("POST", "/api/logout"); state.session = null; boot(); } }, "Sign out")));
  const main = h("main", { class: "main" });
  const scrim = h("div", { class: "scrim hidden", onclick: () => closeMenu() });
  const topbar = h("div", { class: "topbar" },
    h("button", { class: "ghost icon", title: "Menu", onclick: () => { sidebar.classList.add("open"); scrim.classList.remove("hidden"); } }, icon("menu")),
    h("img", { src: "icon.svg", alt: "" }), "Sphynx");
  function closeMenu() { sidebar.classList.remove("open"); scrim.classList.add("hidden"); }
  const shell = h("div", { class: "shell" }, topbar, sidebar, scrim, main);
  return { shell, main, nav };
}

async function render() {
  if (!layout) return;
  clearInterval(state.timer);
  state.timer = null;
  const { name, arg } = route();
  for (const a of layout.nav.querySelectorAll("a")) {
    const n = a.getAttribute("data-name");
    a.classList.toggle("active", n === name || (name === "device" && n === "devices"));
  }
  const page = PAGES[name] || PAGES.overview;
  const container = h("div", null, h("div", { class: "loading" }, h("span", { class: "spinner" })));
  layout.main.replaceChildren(container);
  window.scrollTo(0, 0);
  try {
    await page(container, arg);
  } catch (e) {
    container.replaceChildren(notice("bad", h("strong", null, "Couldn't load this page. "), e.message, " ", h("button", { class: "small", onclick: () => render() }, "Try again")));
  }
}

// every re-runs fn periodically while the page stays open.
function every(ms, fn) {
  clearInterval(state.timer);
  state.timer = setInterval(() => { if (!document.hidden) fn().catch(() => {}); }, ms);
}

window.addEventListener("hashchange", render);

async function boot() {
  layout = null;
  clearInterval(state.timer);
  const root = document.getElementById("app");
  try {
    state.session = await api("GET", "/api/session");
  } catch (e) {
    root.replaceChildren(h("div", { class: "auth" }, h("div", { class: "card" }, notice("bad", e.message), h("button", { onclick: boot }, "Retry"))));
    return;
  }
  const s = state.session;
  if (!s.passwordSet) return root.replaceChildren(screenCreatePassword());
  if (!s.authenticated) return root.replaceChildren(screenLogin());
  if (!s.setupDone) return root.replaceChildren(screenSetup());
  layout = buildLayout();
  root.replaceChildren(layout.shell);
  render();
}

// ---------- sign-in and first run ----------

function authCard(lead, ...children) {
  return h("div", { class: "auth" }, h("div", { class: "card" },
    h("div", { class: "brand" }, h("img", { src: "icon.svg", alt: "" }), "Sphynx"),
    h("p", { class: "lead" }, lead), ...children));
}

function screenCreatePassword() {
  const pw = h("input", { type: "password", autocomplete: "new-password" });
  const pw2 = h("input", { type: "password", autocomplete: "new-password" });
  const btn = h("button", { class: "primary", type: "submit", style: "width:100%;justify-content:center" }, "Create password");
  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      if (pw.value !== pw2.value) return toast("The passwords don't match", true);
      await run(btn, () => api("POST", "/api/setup/password", { password: pw.value }));
      boot();
    },
  }, field("Admin password", pw, "At least 8 characters. You'll use it to sign in to this page."), field("Repeat password", pw2), btn);
  setTimeout(() => pw.focus(), 0);
  return authCard("Welcome! Sphynx is your own private network, like Tailscale, running on this computer. First, protect this admin page with a password.", form);
}

function screenLogin() {
  const pw = h("input", { type: "password", autocomplete: "current-password" });
  const btn = h("button", { class: "primary", type: "submit", style: "width:100%;justify-content:center" }, "Sign in");
  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      await run(btn, () => api("POST", "/api/login", { password: pw.value }));
      boot();
    },
  }, field("Password", pw), btn);
  setTimeout(() => pw.focus(), 0);
  return authCard("Sign in to manage your network.", form);
}

function suggestServerURL() {
  const host = location.hostname;
  return `http://${host}:8080`;
}

function screenSetup() {
  const url = textInput(suggestServerURL(), { type: "url" });
  const name = textInput("", { placeholder: "e.g. malcolm", autocapitalize: "off" });
  const email = textInput("", { type: "email", placeholder: "optional" });
  const local = ["localhost", "127.0.0.1", "::1"].includes(location.hostname);
  const btn = h("button", { class: "primary", type: "submit", style: "width:100%;justify-content:center" }, "Finish setup");
  const waiting = h("div");
  const form = h("form", {
    onsubmit: async (e) => {
      e.preventDefault();
      await run(btn, () => api("POST", "/api/setup/finish", { serverUrl: url.value.trim(), userName: name.value.trim(), email: email.value.trim() }));
      state.session.setupDone = true;
      location.hash = "#/add";
      boot();
    },
  },
    h("h3", { text: "1. How will your devices reach this server?" }),
    local ? notice("warn", h("strong", null, "Devices can't use “localhost”. "), "Use this PC's address on your home network instead (for example http://192.168.1.20:8080). On Windows run ", h("code", null, "ipconfig"), " and look for “IPv4 Address”.") : null,
    field("Server address", url, h("span", null, "For use outside your home, forward TCP port 8080 on your router to this PC, or set up HTTPS with your own domain later in Settings. Phones usually need an https:// address.")),
    h("h3", { text: "2. Who are you?", style: "margin-top:6px" }),
    field("Your user name", name, "Your devices will belong to this user. Lowercase letters, numbers and dashes."),
    field("Email", email),
    waiting,
    btn);
  // Headscale may still be starting on the very first run.
  (async () => {
    for (let i = 0; i < 120; i++) {
      const s = await api("GET", "/api/session").catch(() => null);
      if (s && s.ready) { waiting.replaceChildren(); btn.disabled = false; return; }
      btn.disabled = true;
      waiting.replaceChildren(notice("info", h("span", { class: "spinner" }), " Starting the control server…"));
      await new Promise((r) => setTimeout(r, 1000));
    }
  })();
  const scr = authCard("Two quick questions and you're ready to add devices.", form);
  scr.classList.add("wide");
  return scr;
}

// ---------- shared data ----------

async function loadUsers() {
  state.users = await api("GET", "/api/users");
  return state.users;
}

function userSelect(users, value, allowNone) {
  const opts = users.map((u) => [u.id, userName(u)]);
  if (allowNone) opts.unshift(["", allowNone]);
  return select(opts, value);
}

// ---------- Overview ----------

PAGES.overview = async (el) => {
  const o = await api("GET", "/api/overview");
  const parts = [pageHead("Overview", "Your private network at a glance.", h("button", { class: "primary", onclick: () => go("add") }, icon("add"), "Add device"))];
  if (!o.ready) parts.push(notice("warn", h("strong", null, "The control server is starting. "), o.readyError || "", " This page refreshes by itself."));
  if (o.headscale && o.headscale.crashes > 0 && !o.headscale.running) parts.push(notice("bad", h("strong", null, "Headscale keeps stopping. "), "Check ", h("a", { href: "#/logs/headscale" }, "the logs"), " for the reason."));
  if (o.tlsError) parts.push(notice("bad", h("strong", null, "HTTPS problem: "), o.tlsError));

  if (o.ready) {
    parts.push(h("div", { class: "grid four" },
      statCard("Devices online", `${o.online} / ${o.devices}`, h("a", { href: "#/devices" }, "See all devices")),
      statCard("Users", o.users, h("a", { href: "#/users" }, "Manage users")),
      statCard("Exit nodes", o.exitNodes, h("a", { href: "#/routes" }, "Exit nodes & routes")),
      statCard("Waiting for approval", o.pendingRoutes, o.pendingRoutes ? h("a", { href: "#/routes" }, "Review routes") : h("span", { class: "faint" }, "Nothing to review"))));
  }

  const home = o.homeDevice;
  const homeBody = home
    ? h("div", { class: "kv" },
        h("div", null, "Status"), h("div", null, statusBadge(home.online, home.lastSeen)),
        h("div", null, "Address"), h("div", null, ipv4(home) || "—", ipv4(home) ? copyButton(ipv4(home)) : null),
        h("div", null, "Exit node"), h("div", null, o.home.exitNode ? (o.homeExitReady ? badge("Ready", "ok") : badge("Starting", "warn")) : badge("Off")),
        h("div", null, "Home networks"), h("div", null, (o.home.lanRoutes || []).length ? o.home.lanRoutes.join(", ") : h("span", { class: "faint" }, "none shared")),
        h("div", null, "Peer relay"), h("div", null, o.home.relayPort ? "UDP port " + o.home.relayPort : h("span", { class: "faint" }, "off")))
    : h("p", { class: "muted" }, o.ready ? "Sphynx home is joining the network…" : "Waiting for the control server.");
  parts.push(h("div", { class: "grid two" },
    card("Sphynx home", h("span", null, "The device Sphynx runs on your network from this PC. Pick ", h("strong", null, "sphynx-home"), " as exit node on a remote device to use your home internet."),
      homeBody, h("div", { class: "mt" }, h("a", { href: "#/settings" }, "Change in Settings"))),
    card("Connect a device", "Devices use the official Tailscale app, pointed at this server.",
      h("div", { class: "small muted" }, "Server address"), codebox(o.serverUrl),
      h("button", { class: "primary", onclick: () => go("add") }, "Step-by-step instructions"))));

  const r = o.resilience || {};
  const on = [
    r.cacheNetmaps && "reconnects without the server",
    r.clientReachability && "keeps working tunnels when the server link drops",
    r.probeUdpLifetime && "keeps direct paths alive on cellular",
    r.autoExitNode && "automatic exit node",
    r.peerMtu && "packet size discovery",
  ].filter(Boolean);
  parts.push(h("div", { class: "grid two" },
    card("Weak-signal mode", on.length ? "On for every device: " + on.join(", ") + "." : "Off. Turn it on in Settings if you travel with patchy connections.",
      h("a", { href: "#/settings" }, "Weak-signal settings")),
    card("Server", null, h("div", { class: "kv" },
      h("div", null, "Control server"), h("div", null, o.headscale?.running ? badge("Running", "ok") : badge("Stopped", "bad"), o.headscale?.startedAt && !isZeroTime(o.headscale.startedAt) ? h("span", { class: "faint small" }, " since " + timeAgo(o.headscale.startedAt).replace(" ago", "")) : null),
      h("div", null, "HTTPS"), h("div", null, o.tlsMode === "none" ? "off (plain HTTP)" : o.tlsMode === "letsencrypt" ? "Let's Encrypt" : "own certificate"),
      h("div", null, "MagicDNS"), h("div", null, o.magicDns ? "on, names end in ." + o.baseDomain : "off"),
      h("div", null, "Version"), h("div", null, "Sphynx " + o.version)))));
  el.replaceChildren(...parts);
  if (!o.ready) every(3000, async () => render());
};

function statCard(label, value, hint) {
  return h("div", { class: "card stat" }, h("div", { class: "label", text: label }), h("div", { class: "value", text: String(value) }), h("div", { class: "hint" }, hint));
}

// ---------- Devices ----------

PAGES.devices = async (el) => {
  let devices = await api("GET", "/api/nodes");
  let q = "", filter = "all", showHelpers = false;
  const tbody = h("tbody");
  const countEl = h("span", { class: "muted small" });

  function draw() {
    const rows = devices.filter((d) => {
      if (d.helper && !showHelpers) return false;
      if (filter === "online" && !d.online) return false;
      if (filter === "offline" && d.online) return false;
      if (!q) return true;
      const hay = [devName(d), d.name, d.user?.name, ...(d.ipAddresses || []), ...(d.tags || []), d.os, d.dnsName].join(" ").toLowerCase();
      return hay.includes(q);
    });
    countEl.textContent = `${rows.length} shown`;
    tbody.replaceChildren(...rows.map(deviceRow));
    if (!rows.length) tbody.append(h("tr", null, h("td", { colspan: 5 }, empty(devices.length ? "No matching devices" : "No devices yet", devices.length ? null : "Add your first device to get started.", devices.length ? null : h("button", { class: "primary", onclick: () => go("add") }, "Add device")))));
  }

  const search = textInput("", { placeholder: "Search by name, owner, IP or tag", oninput: (e) => { q = e.target.value.toLowerCase().trim(); draw(); } });
  const filterSel = select([["all", "All devices"], ["online", "Online"], ["offline", "Offline"]], filter, { onchange: (e) => { filter = e.target.value; draw(); } });
  const helpersBox = h("label", { class: "check", style: "margin:0" }, h("input", { type: "checkbox", onchange: (e) => { showHelpers = e.target.checked; draw(); } }), h("span", { class: "small" }, "Show Sphynx's own devices"));

  el.replaceChildren(
    pageHead("Devices", "Every computer, phone and server in your network.", h("button", { class: "primary", onclick: () => go("add") }, icon("add"), "Add device")),
    h("section", { class: "card" },
      h("div", { class: "row", style: "margin-bottom:10px;align-items:center" }, h("div", { style: "flex:2;min-width:220px" }, search), h("div", { style: "flex:1;min-width:140px" }, filterSel), helpersBox, countEl),
      h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
        h("thead", null, h("tr", null, h("th", { text: "Device" }), h("th", { text: "Owner" }), h("th", { text: "Address" }), h("th", { text: "Status" }), h("th", { text: "Features" }))),
        tbody))));
  draw();
  every(10000, async () => { devices = await api("GET", "/api/nodes"); draw(); });
};

function deviceRow(d) {
  const feats = [];
  if (d.home) feats.push(badge("Sphynx home", "accent"));
  else if (d.helper) feats.push(badge("Sphynx helper"));
  if (d.exitNodeApproved) feats.push(badge("Exit node", "accent"));
  else if (d.exitNodeAdvertised) feats.push(badge("Exit node: needs approval", "warn"));
  if (d.subnetsApproved.length) feats.push(badge("Subnets", "accent"));
  if (d.subnetsAdvertised.some((r) => !d.subnetsApproved.includes(r))) feats.push(badge("Subnet: needs approval", "warn"));
  for (const t of d.tags || []) feats.push(badge(t));
  if (d.strictFirewall) feats.push(badge("443 only"));
  if (!isZeroTime(d.expiry) && new Date(d.expiry) < new Date()) feats.push(badge("Key expired", "bad"));
  const ip = ipv4(d);
  return h("tr", { class: "clickable", onclick: () => go("device/" + d.id) },
    h("td", { "data-label": "Device" }, h("div", { class: "primary-text", text: devName(d) }), h("div", { class: "secondary-text" }, [d.os, d.osVersion].filter(Boolean).join(" · ") || d.dnsName || "")),
    h("td", { "data-label": "Owner" }, (d.tags || []).length ? h("span", { class: "muted" }, "tagged") : userName(d.user)),
    h("td", { "data-label": "Address", class: "nowrap" }, h("span", { class: "mono" }, ip), ip ? copyButton(ip) : null),
    h("td", { "data-label": "Status" }, statusBadge(d.online, d.lastSeen), linkBadge(d)),
    h("td", { "data-label": "Features" }, feats.length ? feats : h("span", { class: "faint" }, "—")));
}

PAGES.device = async (el, id) => {
  const d = await api("GET", "/api/nodes/" + encodeURIComponent(id));
  const reload = () => render();
  const parts = [pageHead(devName(d), d.dnsName || null,
    h("button", { onclick: () => go("devices") }, "Back to devices"),
    !d.helper ? h("button", {
      onclick: async () => {
        const name = await promptAsk("Rename device", "New name", devName(d), "This also changes its MagicDNS name.");
        if (name === null || name === undefined) return;
        await run(null, () => api("POST", `/api/nodes/${d.id}/rename`, { name }), "Renamed");
        reload();
      },
    }, "Rename") : null)];

  if (d.helper) parts.push(notice("info", d.home ? "This is Sphynx home, the device Sphynx itself runs on your network. Configure it in Settings." : "This is a helper device Sphynx uses to send Wake-on-LAN requests for this user. It's managed automatically."));

  const expired = !isZeroTime(d.expiry) && new Date(d.expiry) < new Date();
  parts.push(h("div", { class: "grid two" },
    card("Details", null, h("div", { class: "kv" },
      h("div", null, "Status"), h("div", null, statusBadge(d.online, d.lastSeen)),
      h("div", null, "Owner"), h("div", null, (d.tags || []).length ? "Tagged device (" + d.tags.join(", ") + ")" : userName(d.user)),
      h("div", null, "Tailnet addresses"), h("div", null, ...(d.ipAddresses || []).map((ip) => h("div", { class: "mono" }, ip, copyButton(ip)))),
      d.dnsName ? [h("div", null, "MagicDNS name"), h("div", { class: "mono" }, d.dnsName, copyButton(d.dnsName))] : null,
      h("div", null, "System"), h("div", null, [d.os, d.osVersion, d.deviceModel].filter(Boolean).join(" · ") || "—"),
      h("div", null, "Tailscale version"), h("div", null, d.clientVersion || "—"),
      h("div", null, "Added"), h("div", null, fmtDate(d.createdAt)),
      h("div", null, "Last seen"), h("div", null, d.online ? "now" : fmtDate(d.lastSeen)),
      h("div", null, "Login expires"), h("div", null, isZeroTime(d.expiry) ? "never" : (expired ? badge("expired " + timeAgo(d.expiry), "bad") : timeAgo(d.expiry))))),
    card("Connection to Sphynx home", "How this device's traffic reaches your home PC right now.", h("div", { class: "kv" },
      h("div", null, "Path"), h("div", null, linkBadge(d) || h("span", { class: "faint" }, d.online ? "idle (no recent traffic)" : "offline")),
      d.linkAddr ? [h("div", null, "Endpoint"), h("div", { class: "mono" }, d.linkAddr)] : null,
      d.relayRegion ? [h("div", null, "Home relay region"), h("div", null, d.relayRegion)] : null,
      d.lastHandshake ? [h("div", null, "Last handshake"), h("div", null, timeAgo(d.lastHandshake))] : null,
      h("div", null, "Traffic"), h("div", null, `↓ ${fmtBytes(d.rxBytes)}  ↑ ${fmtBytes(d.txBytes)}`),
      d.udpWorks === false ? [h("div", null, "UDP"), h("div", null, badge("blocked on this network", "warn"))] : null))));

  if (!d.helper) {
    // Exit node and subnet routes
    const routeBody = [];
    if (d.exitNodeAdvertised) {
      routeBody.push(toggleRow("Use as exit node", "Let other devices send their internet traffic through this device.", d.exitNodeApproved, async (on, input) => {
        try { await api("POST", `/api/nodes/${d.id}/routes`, { subnets: d.subnetsApproved, exitNode: on }); toast(on ? "Exit node approved" : "Exit node turned off"); reload(); } catch (e) { input.checked = !on; toast(e.message, true); }
      }));
    } else {
      routeBody.push(h("p", { class: "muted small" }, "To offer this device as an exit node, run ", h("code", null, "tailscale set --advertise-exit-node"), " on it (or pick “Run exit node” in the Tailscale app), then approve it here."));
    }
    if (d.subnetsAdvertised.length) {
      routeBody.push(h("h3", { text: "Subnet routes", style: "margin:10px 0 4px" }));
      for (const r of d.subnetsAdvertised) {
        routeBody.push(toggleRow(r, "Make this network reachable from your other devices.", d.subnetsApproved.includes(r), async (on, input) => {
          const subnets = on ? [...d.subnetsApproved, r] : d.subnetsApproved.filter((x) => x !== r);
          try { await api("POST", `/api/nodes/${d.id}/routes`, { subnets, exitNode: d.exitNodeApproved }); toast("Saved"); reload(); } catch (e) { input.checked = !on; toast(e.message, true); }
        }));
      }
    } else {
      routeBody.push(h("p", { class: "muted small" }, "No subnet routes advertised. To share this device's local network, run ", h("code", null, "tailscale set --advertise-routes=192.168.1.0/24"), "."));
    }

    const tagsInput = textInput((d.tags || []).join(", "), { placeholder: "tag:server, tag:nas" });
    const tagsBtn = h("button", {
      onclick: async (e) => {
        const tags = tagsInput.value.split(",").map((s) => s.trim()).filter(Boolean);
        if (tags.length && !(d.tags || []).length) {
          const ok = await confirmAsk("Tag this device?", "Tagged devices stop belonging to a person: rules for their owner no longer apply, and the device can't be used to relay Wake-on-LAN. The tags must be listed in tagOwners in your access policy.", "Apply tags");
          if (!ok) return;
        }
        await run(e.currentTarget, () => api("POST", `/api/nodes/${d.id}/tags`, { tags }), "Tags saved");
        reload();
      },
    }, "Save tags");

    parts.push(h("div", { class: "grid two" },
      card("Exit node & subnets", null, ...routeBody),
      card("Tags & network options", null,
        field("Tags", tagsInput, "Tags give a device a role in access rules, like tag:server."), tagsBtn,
        h("div", { class: "mt" }),
        toggleRow("Strict firewall mode", "Only use HTTPS on port 443, relaying everything. For hotel, office or national firewalls that block or flag anything else. Slower, so leave it off unless needed.", d.strictFirewall, async (on, input) => {
          try { await api("POST", `/api/nodes/${d.id}/options`, { strictFirewall: on }); toast(on ? "Strict firewall mode on" : "Strict firewall mode off"); } catch (e) { input.checked = !on; toast(e.message, true); }
        }))));

    const wolCard = card("Wake-on-LAN", null);
    if ((d.wolMacs || []).length) {
      wolCard.append(h("p", { class: "small muted" }, "This device reported its network card address: ", h("span", { class: "mono" }, d.wolMacs.join(", ")), "."),
        h("button", { onclick: () => go("wol/new?" + new URLSearchParams({ name: devName(d), mac: d.wolMacs[0], node: d.id })) }, "Set up waking this device"));
    } else {
      wolCard.append(h("p", { class: "small muted" }, "Want to switch this computer on remotely? Add it on the Wake-on-LAN page with its network card's MAC address."),
        h("button", { onclick: () => go("wol/new?" + new URLSearchParams({ name: devName(d), node: d.id })) }, "Set up Wake-on-LAN"));
    }

    parts.push(h("div", { class: "grid two" },
      card("Login", "Logins expire based on the key expiry setting. Expiring forces the device to sign in again.",
        h("div", { class: "inline" },
          h("button", { onclick: async (e) => { await run(e.currentTarget, () => api("POST", `/api/nodes/${d.id}/expire`, { disableExpiry: true }), "This device's login will never expire"); reload(); } }, "Never expire"),
          h("button", { class: "danger", onclick: async (e) => {
            if (!(await confirmAsk("Expire login now?", `${devName(d)} will be disconnected until someone signs in on it again.`, "Expire now", true))) return;
            await run(e.currentTarget, () => api("POST", `/api/nodes/${d.id}/expire`, { disableExpiry: false }), "Login expired"); reload();
          } }, "Expire now"))),
      wolCard));

    parts.push(card("Remove device", "The device is disconnected and must be added again to rejoin.",
      h("button", { class: "danger solid", onclick: async (e) => {
        if (!(await confirmAsk("Remove " + devName(d) + "?", "It will lose access to your network immediately.", "Remove device", true))) return;
        await run(e.currentTarget, () => api("DELETE", "/api/nodes/" + d.id), "Device removed");
        go("devices");
      } }, "Remove device")));
  }
  el.replaceChildren(...parts);
};

// ---------- Add device ----------

const OSES = [["windows", "Windows"], ["macos", "macOS"], ["ios", "iPhone / iPad"], ["android", "Android"], ["linux", "Linux"], ["docker", "Docker / server"], ["tv", "Apple TV"]];

function osInstructions(os, server, key) {
  const steps = [];
  const li = (...c) => steps.push(h("li", null, ...c));
  const cmd = (line) => codebox(line);
  switch (os) {
    case "windows":
      li("Install Tailscale from ", h("a", { href: "https://tailscale.com/download/windows", target: "_blank", rel: "noopener" }, "tailscale.com/download/windows"), ". Don't sign in in the app.");
      li("Open PowerShell or Command Prompt and run:", cmd(key ? `tailscale login --login-server ${server} --auth-key ${key}` : `tailscale login --login-server ${server}`));
      li(key ? "That's it. The device appears on the Devices page within seconds." : "A browser page opens. Enter your Sphynx admin password there and click Approve device.");
      li("Optional: in the tray icon's Preferences, turn on “Run unattended” so it stays connected when you're signed out of Windows.");
      break;
    case "macos":
      li("Install Tailscale from the ", h("a", { href: "https://tailscale.com/download/mac", target: "_blank", rel: "noopener" }, "Mac App Store or tailscale.com"), ".");
      li("Hold the Option (⌥) key and click the Tailscale icon in the menu bar. Open ", h("strong", null, "Debug → Custom Login Server → Add Account…"), " and enter:", cmd(server));
      if (key) li("Or, from Terminal (standalone version):", cmd(`tailscale login --login-server ${server} --auth-key ${key}`));
      else li("A browser page opens. Enter your Sphynx admin password there and click Approve device.");
      break;
    case "ios":
      li("Install Tailscale from the ", h("a", { href: "https://apps.apple.com/app/tailscale/id1470499037", target: "_blank", rel: "noopener" }, "App Store"), ".");
      li("Open it, tap the account icon at the top right, then ", h("strong", null, "Log in…"), ".");
      li("Tap the ⋯ menu at the top right, choose ", h("strong", null, "Use custom coordination server"), " and enter:", cmd(server));
      li("A Sphynx page opens. Enter your Sphynx admin password and tap ", h("strong", null, "Approve device"), ".");
      if (server.startsWith("http://")) li(h("span", { class: "muted" }, "iPhones and iPads may refuse a plain http:// server. If it doesn't connect, set up HTTPS in Settings."));
      break;
    case "android":
      li("Install Tailscale from ", h("a", { href: "https://play.google.com/store/apps/details?id=com.tailscale.ipn", target: "_blank", rel: "noopener" }, "Google Play"), " or F-Droid.");
      li("Open it, tap the settings icon at the top right, then ", h("strong", null, "Accounts"), ".");
      li("Tap ⋮ at the top right and choose ", h("strong", null, "Use an alternate server"), ". Enter:", cmd(server));
      if (key) li("If a login page opens, close it. Go back to ", h("strong", null, "Accounts → ⋮ → Use an auth key"), " and paste:", cmd(key));
      else li("A Sphynx page opens. Enter your Sphynx admin password and tap ", h("strong", null, "Approve device"), ".");
      break;
    case "linux":
      li("Install Tailscale:", cmd("curl -fsSL https://tailscale.com/install.sh | sh"));
      li("Connect it to Sphynx:", cmd(key ? `sudo tailscale up --login-server ${server} --auth-key ${key}` : `sudo tailscale up --login-server ${server}`));
      if (!key) li("It prints a link. Open it in any browser and approve with your Sphynx admin password, or paste it below under “Approve a sign-in”.");
      li("Optional: share this machine's internet or LAN:", cmd("sudo tailscale set --advertise-exit-node --advertise-routes=192.168.1.0/24"));
      break;
    case "docker":
      li("Run the official Tailscale container:", cmd(`docker run -d --name tailscale --restart unless-stopped \\\n  --cap-add NET_ADMIN --cap-add NET_RAW --device /dev/net/tun \\\n  -v tailscale-state:/var/lib/tailscale --network host \\\n  -e TS_STATE_DIR=/var/lib/tailscale \\\n  -e TS_AUTHKEY=${key || "<create a key above>"} \\\n  -e TS_EXTRA_ARGS=--login-server=${server} \\\n  tailscale/tailscale`));
      break;
    case "tv":
      li("Install Tailscale from the App Store on the Apple TV. Don't sign in yet.");
      li("Open it, choose ", h("strong", null, "Install VPN configuration"), " and allow it.");
      li("Open the Apple TV's Settings → Apps → Tailscale → ", h("strong", null, "Alternate coordination server URL"), " and enter:", cmd(server));
      li("Return to Tailscale and scan the QR code with your phone. On the page that opens, enter your Sphynx admin password and tap Approve device.");
      break;
  }
  return h("ol", { class: "steps" }, steps);
}

PAGES.add = async (el) => {
  const [ov, users] = await Promise.all([api("GET", "/api/overview"), loadUsers()]);
  const server = ov.serverUrl;
  let os = /Android/i.test(navigator.userAgent) ? "android" : /iPhone|iPad/i.test(navigator.userAgent) ? "ios" : /Mac/i.test(navigator.platform) ? "macos" : /Win/i.test(navigator.platform) ? "windows" : "linux";
  let key = null;
  let mode = "key";

  const osPick = h("div", { class: "os-pick" });
  const instr = h("div");
  const keyArea = h("div");
  const modeTabs = h("div", { class: "tabs" });

  const ownerSel = userSelect(users, users.find((u) => u.owner)?.id || users[0]?.id);
  const expirySel = select([["1", "1 hour"], ["24", "1 day"], ["168", "7 days"], ["2160", "90 days"], ["8760", "1 year"]], "24");
  const reusable = checkbox("Reusable", "Use the same key for several devices.", false);
  const ephemeral = checkbox("Ephemeral", "Remove the device automatically when it goes offline (for containers and test machines).", false);
  const tags = textInput("", { placeholder: "optional, e.g. tag:server" });

  function draw() {
    osPick.replaceChildren(...OSES.map(([v, label]) => h("button", { class: os === v ? "active" : "", onclick: () => { os = v; draw(); } }, label)));
    modeTabs.replaceChildren(
      h("button", { class: mode === "key" ? "active" : "", onclick: () => { mode = "key"; draw(); } }, "With a key (fastest)"),
      h("button", { class: mode === "web" ? "active" : "", onclick: () => { mode = "web"; draw(); } }, "Sign in on the device"));
    keyArea.classList.toggle("hidden", mode !== "key");
    instr.replaceChildren(osInstructions(os, server, mode === "key" ? (key ? key.key : null) : null));
    if (mode === "key" && !key && os !== "ios" && os !== "tv") instr.prepend(notice("info", "Create a key first, then follow these steps. The key is filled in for you."));
    if (mode === "key" && (os === "ios" || os === "tv")) instr.prepend(notice("info", "This device signs in with your admin password instead of a key. Just follow the steps."));
  }

  const createBtn = h("button", {
    class: "primary",
    onclick: async (e) => {
      const t = tags.value.split(",").map((s) => s.trim()).filter(Boolean);
      key = await run(e.currentTarget, () => api("POST", "/api/keys", {
        user: t.length ? "" : ownerSel.value, reusable: reusable.input.checked, ephemeral: ephemeral.input.checked, hours: Number(expirySel.value), tags: t,
      }));
      keyOut.replaceChildren(notice("ok", h("strong", null, "Key created. "), "It's filled into the steps below and is valid " + timeAgo(key.expiration) + "."), codebox(key.key));
      draw();
    },
  }, "Create key");
  const keyOut = h("div");
  keyArea.append(
    h("div", { class: "row" }, field("Device belongs to", ownerSel), field("Key valid for", expirySel)),
    reusable, ephemeral,
    field("Tags", tags, "Tagged devices belong to the network rather than a person, like servers. Leave empty for personal devices."),
    createBtn, keyOut);

  // Approve a sign-in link
  const linkInput = textInput("", { placeholder: server + "/register/…" });
  const approveUser = userSelect(users, users.find((u) => u.owner)?.name, null);
  for (const o of approveUser.options) o.value = users.find((u) => u.id === o.value)?.name || o.value;
  approveUser.value = users.find((u) => u.owner)?.name || users[0]?.name || "";
  const approveBtn = h("button", {
    class: "primary",
    onclick: async (e) => {
      const n = await run(e.currentTarget, () => api("POST", "/api/register", { link: linkInput.value, user: approveUser.value }));
      linkInput.value = "";
      toast(`${devName(n)} is now connected`);
      go("device/" + n.id);
    },
  }, "Approve device");

  el.replaceChildren(
    pageHead("Add a device", "Devices run the official Tailscale app. You point it at your Sphynx server instead of Tailscale's."),
    card("1. Pick the device type", null, osPick, modeTabs, keyArea),
    card("2. Follow these steps on the device", null, instr),
    card("Approve a sign-in", "When a device shows a sign-in link instead of connecting, paste that link here.",
      h("div", { class: "row" }, h("div", { class: "field", style: "flex:3;min-width:240px" }, h("label", { text: "Link shown by the device" }), linkInput), field("Belongs to", approveUser)), approveBtn));
  draw();
};

// ---------- Users ----------

PAGES.users = async (el) => {
  const users = await loadUsers();
  const name = textInput("", { placeholder: "e.g. alice", autocapitalize: "off" });
  const display = textInput("", { placeholder: "optional" });
  const email = textInput("", { type: "email", placeholder: "optional" });
  const addBtn = h("button", { class: "primary", onclick: async (e) => {
    await run(e.currentTarget, () => api("POST", "/api/users", { name: name.value, displayName: display.value, email: email.value }), "User added");
    render();
  } }, "Add user");

  const rows = users.map((u) => h("tr", null,
    h("td", { "data-label": "User" }, h("div", { class: "primary-text" }, u.displayName || u.name, u.owner ? badge("owns Sphynx home", "accent") : null), h("div", { class: "secondary-text" }, [u.name, u.email].filter(Boolean).join(" · "))),
    h("td", { "data-label": "Devices" }, String(u.devices)),
    h("td", { "data-label": "Sign-in" }, u.provider ? "Single sign-on" : "Sphynx"),
    h("td", { "data-label": "Added" }, fmtDate(u.createdAt)),
    h("td", { class: "actions" },
      h("button", { class: "small", onclick: async () => {
        const n = await promptAsk("Rename user", "New user name", u.name);
        if (!n) return;
        await run(null, () => api("POST", `/api/users/${u.id}/rename`, { name: n }), "Renamed");
        render();
      } }, "Rename"),
      " ",
      h("button", { class: "small danger", onclick: async () => {
        if (!(await confirmAsk("Delete " + u.name + "?", "Their devices must be removed first.", "Delete user", true))) return;
        await run(null, () => api("DELETE", "/api/users/" + u.id), "User deleted");
        render();
      } }, "Delete"))));

  el.replaceChildren(
    pageHead("Users", "People who own devices in your network. There's no limit on users or devices."),
    card(null, null, h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
      h("thead", null, h("tr", null, h("th", { text: "User" }), h("th", { text: "Devices" }), h("th", { text: "Sign-in" }), h("th", { text: "Added" }), h("th"))),
      h("tbody", null, rows)))),
    card("Add a user", "Give family members or friends their own user, so access rules can tell their devices apart.",
      h("div", { class: "row" }, field("User name", name), field("Display name", display), field("Email", email)), addBtn));
};

// ---------- Access controls ----------

const POLICY_PRESETS = [
  ["Everything can reach everything (default)", null],
  ["Each person only reaches their own devices", `{
	// People reach their own devices; tagged servers are reachable by everyone.
	"grants": [
		{"src": ["autogroup:member"], "dst": ["autogroup:self"], "ip": ["*"]},
		{"src": ["autogroup:member"], "dst": ["autogroup:tagged"], "ip": ["*"]},
		// Everyone may use exit nodes.
		{"src": ["autogroup:member"], "dst": ["autogroup:internet"], "ip": ["*"]},
	],
	"ssh": [
		{"action": "accept", "src": ["autogroup:member"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot", "root"]},
	],
	"nodeAttrs": [
		{"target": ["*"], "attr": ["drive:share", "drive:access"]},
	],
}
`],
  ["Family: admins reach everything, others only their own devices", `{
	"groups": {
		// Put your own user name here.
		"group:admins": ["you@"],
	},
	"grants": [
		{"src": ["group:admins"], "dst": ["*"], "ip": ["*"]},
		{"src": ["autogroup:member"], "dst": ["autogroup:self"], "ip": ["*"]},
		{"src": ["autogroup:member"], "dst": ["autogroup:internet"], "ip": ["*"]},
	],
	"ssh": [
		{"action": "accept", "src": ["group:admins"], "dst": ["autogroup:self"], "users": ["autogroup:nonroot", "root"]},
	],
}
`],
];

PAGES.access = async (el) => {
  const p = await api("GET", "/api/policy");
  const ta = h("textarea", { class: "code", spellcheck: "false" });
  ta.value = p.policy;
  ta.addEventListener("keydown", (e) => {
    if (e.key === "Tab" && !e.shiftKey) {
      e.preventDefault();
      const s = ta.selectionStart;
      ta.setRangeText("\t", s, ta.selectionEnd, "end");
    }
  });
  const result = h("div");
  const check = h("button", { onclick: async (e) => {
    try {
      e.currentTarget.disabled = true;
      await api("POST", "/api/policy/check", { policy: ta.value });
      result.replaceChildren(notice("ok", "The policy is valid."));
    } catch (err) {
      result.replaceChildren(notice("bad", h("strong", null, "Problem: "), err.message));
    } finally { e.currentTarget.disabled = false; }
  } }, "Check");
  const save = h("button", { class: "primary", onclick: async (e) => {
    try {
      e.currentTarget.disabled = true;
      await api("PUT", "/api/policy", { policy: ta.value });
      result.replaceChildren(notice("ok", "Saved. Devices pick up the new rules within seconds."));
    } catch (err) {
      result.replaceChildren(notice("bad", h("strong", null, "Not saved: "), err.message));
    } finally { e.currentTarget.disabled = false; }
  } }, "Save");
  const presetSel = select(POLICY_PRESETS.map(([label], i) => [String(i), label]), "", {});
  presetSel.prepend(h("option", { value: "", text: "Start from a template…", selected: true }));
  presetSel.addEventListener("change", async () => {
    const i = presetSel.value;
    if (i === "") return;
    if (await confirmAsk("Replace the editor contents?", "Your current text will be replaced (nothing is saved until you click Save).", "Replace")) {
      ta.value = POLICY_PRESETS[i][1] || p.default;
    }
    presetSel.value = "";
  });

  const managed = (p.managed || []).map((m) => h("li", null, h("div", null, h("div", { text: m.description }), h("div", { class: "mono faint small", text: `${m.section}: ` + JSON.stringify(m.json) }))));

  el.replaceChildren(
    pageHead("Access controls", h("span", null, "Who can reach what. Sphynx uses Tailscale's policy language, so everything in ", h("a", { href: "https://tailscale.com/docs/reference/syntax/policy-file", target: "_blank", rel: "noopener" }, "Tailscale's policy docs"), " works here: grants, ACLs, groups, tags, SSH rules, auto-approvers, node attributes and tests.")),
    h("section", { class: "card" },
      h("div", { class: "row", style: "margin-bottom:10px;align-items:center" }, presetSel, h("span", { class: "spacer" }), check, save),
      result, ta,
      p.updatedAt ? h("p", { class: "faint small mt" }, "Last saved " + fmtDate(p.updatedAt)) : null),
    card("Rules Sphynx adds for you", "These come from your Settings (weak-signal mode, Sphynx home relay) and device options. They're added on top of your policy automatically.",
      managed.length ? h("ul", { class: "list-plain" }, managed) : h("p", { class: "muted" }, "None right now.")),
    card("Quick reference", null, h("div", { class: "small" },
      h("p", null, h("strong", null, "Let a group use exit nodes: "), h("code", null, `{"src": ["group:family"], "dst": ["autogroup:internet"], "ip": ["*"]}`)),
      h("p", null, h("strong", null, "Reach a home network: "), h("code", null, `{"src": ["autogroup:member"], "dst": ["192.168.1.0/24"], "ip": ["*"]}`)),
      h("p", null, h("strong", null, "Approve exit nodes automatically: "), h("code", null, `"autoApprovers": {"exitNode": ["you@"]}`)),
      h("p", null, h("strong", null, "Tag servers: "), h("code", null, `"tagOwners": {"tag:server": ["you@"]}`)),
      h("p", { class: "mb0" }, h("strong", null, "People: "), "write user names with a trailing @, like ", h("code", null, "alice@"), "."))));
};

// ---------- Exit nodes & routes ----------

PAGES.routes = async (el) => {
  const devices = await api("GET", "/api/nodes");
  const exits = devices.filter((d) => d.exitNodeAdvertised);
  const subnets = devices.filter((d) => d.subnetsAdvertised.length);

  const exitRows = exits.map((d) => h("li", null,
    h("div", null, h("div", { class: "primary-text" }, devName(d), d.home ? badge("Sphynx home", "accent") : null), h("div", { class: "secondary-text" }, statusBadge(d.online, d.lastSeen), userName(d.user))),
    switchInput(d.exitNodeApproved, async (on, input) => {
      try { await api("POST", `/api/nodes/${d.id}/routes`, { subnets: d.subnetsApproved, exitNode: on }); toast(on ? "Approved" : "Turned off"); } catch (e) { input.checked = !on; toast(e.message, true); }
    }).el));

  const subnetRows = [];
  for (const d of subnets) {
    for (const r of d.subnetsAdvertised) {
      subnetRows.push(h("li", null,
        h("div", null, h("div", { class: "primary-text mono", text: r }), h("div", { class: "secondary-text" }, "via " + devName(d), " ", statusBadge(d.online, d.lastSeen))),
        switchInput(d.subnetsApproved.includes(r), async (on, input) => {
          const fresh = await api("GET", "/api/nodes/" + d.id);
          const list = on ? [...new Set([...fresh.subnetsApproved, r])] : fresh.subnetsApproved.filter((x) => x !== r);
          try { await api("POST", `/api/nodes/${d.id}/routes`, { subnets: list, exitNode: fresh.exitNodeApproved }); toast("Saved"); } catch (e) { input.checked = !on; toast(e.message, true); }
        }).el));
    }
  }

  el.replaceChildren(
    pageHead("Exit nodes & routes", "Send internet traffic through another device, or reach whole networks behind one."),
    h("div", { class: "grid two" },
      card("Exit nodes", "Approved exit nodes can carry other devices' internet traffic.",
        exitRows.length ? h("ul", { class: "list-plain" }, exitRows) : h("p", { class: "muted" }, "No device offers to be an exit node yet.")),
      card("Use an exit node", "On the device that should use it:",
        h("ul", { class: "small", style: "padding-left:18px;margin:0" },
          h("li", null, h("strong", null, "Windows/macOS: "), "Tailscale icon → Exit node → pick ", h("strong", null, "sphynx-home"), " (or “Recommended” for automatic)."),
          h("li", null, h("strong", null, "iPhone/Android: "), "open Tailscale → Exit node → pick one."),
          h("li", null, h("strong", null, "Linux: ")),
        ),
        codebox("tailscale set --exit-node=sphynx-home --exit-node-allow-lan-access"),
        h("p", { class: "small muted" }, "Automatic choice with failover (weak-signal mode):"),
        codebox("tailscale set --exit-node=auto:any"))),
    card("Subnet routes", "Networks reachable through a device (for example your home LAN through Sphynx home).",
      subnetRows.length ? h("ul", { class: "list-plain" }, subnetRows) : h("p", { class: "muted" }, "No device shares a network. Add your home network to Sphynx home in Settings, or run ", h("code", null, "tailscale set --advertise-routes=192.168.1.0/24"), " on a device.")));
};

// ---------- DNS ----------

const NS_PRESETS = [
  ["Cloudflare", ["1.1.1.1", "1.0.0.1", "2606:4700:4700::1111", "2606:4700:4700::1001"]],
  ["Google", ["8.8.8.8", "8.8.4.4", "2001:4860:4860::8888", "2001:4860:4860::8844"]],
  ["Quad9 (blocks malware)", ["9.9.9.9", "149.112.112.112", "2620:fe::fe", "2620:fe::9"]],
  ["Cloudflare family (blocks adult content)", ["1.1.1.3", "1.0.0.3"]],
];

PAGES.dns = async (el) => {
  const data = await api("GET", "/api/dns");
  const dns = data.dns;
  const magic = toggleRow("MagicDNS", "Reach devices by name, like nas." + dns.baseDomain + " or just nas.", dns.magicDns);
  const base = textInput(dns.baseDomain);
  const override = toggleRow("Use these DNS servers on every device", "Devices use the servers below instead of whatever network they're on. Good for privacy and ad blocking.", dns.overrideLocalDns);
  const ns = listEditor(dns.nameservers, "1.1.1.1\nhttps://dns.nextdns.io/abc123", 4);
  const search = listEditor(dns.searchDomains, "example.com", 2);
  const presetBtns = NS_PRESETS.map(([label, list]) => h("button", { class: "small", onclick: () => { ns.value = list.join("\n"); } }, label));

  const splitRows = h("div");
  function splitRow(domain, servers) {
    const d = textInput(domain, { placeholder: "corp.example.com" });
    const s = textInput((servers || []).join(", "), { placeholder: "10.0.0.53" });
    const row = h("div", { class: "row" }, h("div", { class: "field" }, d), h("div", { class: "field" }, s), h("div", { class: "field", style: "flex:none;min-width:0" }, h("button", { class: "small danger", onclick: () => row.remove() }, "Remove")));
    row.get = () => [d.value.trim(), s.value.split(",").map((x) => x.trim()).filter(Boolean)];
    return row;
  }
  for (const [k, v] of Object.entries(dns.splitDns || {})) splitRows.append(splitRow(k, v));

  const recRows = h("tbody");
  function recRow(r) {
    const n = textInput(r.name, { placeholder: "nas." + dns.baseDomain });
    const t = select(["A", "AAAA"], r.type || "A");
    const v = textInput(r.value, { placeholder: "100.64.0.5" });
    const tr = h("tr", null, h("td", { "data-label": "Name" }, n), h("td", { "data-label": "Type", style: "width:110px" }, t), h("td", { "data-label": "Address" }, v), h("td", { class: "actions" }, h("button", { class: "small danger", onclick: () => tr.remove() }, "Remove")));
    tr.get = () => ({ name: n.value.trim(), type: t.value, value: v.value.trim() });
    return tr;
  }
  for (const r of data.records) recRows.append(recRow(r));

  const save = h("button", { class: "primary", onclick: async (e) => {
    const split = {};
    for (const row of splitRows.children) { const [d, s] = row.get(); if (d) split[d] = s; }
    const records = [...recRows.children].map((tr) => tr.get()).filter((r) => r.name || r.value);
    const r = await run(e.currentTarget, () => api("PUT", "/api/dns", {
      dns: { magicDns: magic.input.checked, baseDomain: base.value.trim(), overrideLocalDns: override.input.checked, nameservers: ns.getList(), splitDns: split, searchDomains: search.getList() },
      records,
    }));
    toast(r.restarted ? "Saved. The control server restarted to apply it." : "Saved");
  } }, "Save DNS settings");

  el.replaceChildren(
    pageHead("DNS", "Names for your devices and which DNS servers they use.", save),
    card("MagicDNS", null, magic, field("Network domain", base, "Device names end with this. Something you don't use elsewhere, like sphynx.internal.")),
    card("DNS servers", null, override, field("Global nameservers", ns, "One per line. IP addresses or DNS-over-HTTPS URLs (NextDNS, AdGuard, Pi-hole with DoH…)."), h("div", { class: "inline" }, h("span", { class: "small muted" }, "Presets:"), presetBtns)),
    card("Split DNS", "Send lookups for some domains to specific servers, e.g. your home router for .lan names.",
      splitRows, h("button", { class: "small", onclick: () => splitRows.append(splitRow("", [])) }, "Add domain")),
    card("Search domains", "Short names are tried with these domains appended.", search),
    card("Custom records", "Extra names that resolve on every device, applied instantly.",
      h("div", { class: "table-wrap" }, h("table", { class: "responsive" }, h("thead", null, h("tr", null, h("th", { text: "Name" }), h("th", { text: "Type" }), h("th", { text: "Address" }), h("th"))), recRows)),
      h("button", { class: "small mt", onclick: () => recRows.append(recRow({})) }, "Add record")),
    h("div", { class: "right" }, save.cloneNode(true)));
  // The cloned bottom button needs its own handler.
  el.querySelector(".right button").addEventListener("click", () => save.click());
};

// ---------- Keys ----------

PAGES.keys = async (el) => {
  const [keys, apikeys, users] = await Promise.all([api("GET", "/api/keys"), api("GET", "/api/apikeys"), loadUsers()]);
  const now = new Date();
  const keyRows = keys.map((k) => {
    const expired = new Date(k.expiration) < now;
    const status = expired ? badge("Expired") : k.used && !k.reusable ? badge("Used") : badge("Active", "ok");
    return h("tr", null,
      h("td", { "data-label": "Key" }, h("span", { class: "mono" }, k.key.slice(0, 12) + "…"), !expired ? copyButton(k.key) : null),
      h("td", { "data-label": "For" }, (k.aclTags || []).length ? k.aclTags.join(", ") : userName(k.user)),
      h("td", { "data-label": "Type" }, [k.reusable ? "reusable" : "single use", k.ephemeral ? "ephemeral" : null].filter(Boolean).join(", ")),
      h("td", { "data-label": "Status" }, status),
      h("td", { "data-label": "Expires" }, timeAgo(k.expiration)),
      h("td", { class: "actions" },
        !expired ? h("button", { class: "small", onclick: async () => { await run(null, () => api("POST", `/api/keys/${k.id}/expire`), "Key expired"); render(); } }, "Expire") : null, " ",
        h("button", { class: "small danger", onclick: async () => { await run(null, () => api("DELETE", `/api/keys/${k.id}`), "Key deleted"); render(); } }, "Delete")));
  });
  const apiRows = apikeys.map((k) => h("tr", null,
    h("td", { "data-label": "Key" }, h("span", { class: "mono" }, k.prefix + "…"), k.sphynx ? badge("used by Sphynx", "accent") : null),
    h("td", { "data-label": "Created" }, fmtDate(k.createdAt)),
    h("td", { "data-label": "Last used" }, k.lastSeen ? timeAgo(k.lastSeen) : "never"),
    h("td", { "data-label": "Expires" }, k.expiration ? timeAgo(k.expiration) : "—"),
    h("td", { class: "actions" }, k.sphynx ? null : [
      h("button", { class: "small", onclick: async () => { await run(null, () => api("POST", `/api/apikeys/${k.prefix}/expire`), "Expired"); render(); } }, "Expire"), " ",
      h("button", { class: "small danger", onclick: async () => { await run(null, () => api("DELETE", `/api/apikeys/${k.prefix}`), "Deleted"); render(); } }, "Delete")])));

  const days = select([["30", "30 days"], ["90", "90 days"], ["365", "1 year"]], "90");
  el.replaceChildren(
    pageHead("Keys", "Auth keys add devices without signing in. API keys let scripts and other tools manage Sphynx's Headscale.", h("button", { class: "primary", onclick: () => go("add") }, "Create an auth key")),
    card("Auth keys", null, keys.length ? h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
      h("thead", null, h("tr", null, h("th", { text: "Key" }), h("th", { text: "For" }), h("th", { text: "Type" }), h("th", { text: "Status" }), h("th", { text: "Expires" }), h("th"))),
      h("tbody", null, keyRows))) : h("p", { class: "muted" }, "No auth keys. Create one on the Add device page.")),
    card("API keys", "For the headscale CLI, scripts or other Headscale tools. Keep them secret.",
      h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
        h("thead", null, h("tr", null, h("th", { text: "Key" }), h("th", { text: "Created" }), h("th", { text: "Last used" }), h("th", { text: "Expires" }), h("th"))),
        h("tbody", null, apiRows))),
      h("div", { class: "row mt", style: "align-items:center" }, h("div", { style: "width:160px" }, days),
        h("button", { onclick: async (e) => {
          const r = await run(e.currentTarget, () => api("POST", "/api/apikeys", { days: Number(days.value) }));
          await modal({ title: "API key created", body: h("div", null, h("p", null, "Copy it now; it won't be shown again."), codebox(r.key)), buttons: [{ text: "Done", class: "primary", value: true }] });
          render();
        } }, "Create API key"))));
};

// ---------- Wake-on-LAN ----------

PAGES.wol = async (el, arg) => {
  const [targets, devices] = await Promise.all([api("GET", "/api/wol"), api("GET", "/api/nodes")]);
  const byId = Object.fromEntries(devices.map((d) => [d.id, d]));
  const relays = devices.filter((d) => !d.helper && !(d.tags || []).length);

  async function editTarget(t) {
    t = t || { method: relays.length ? "relay" : "server" };
    const name = textInput(t.name, { placeholder: "Gaming PC" });
    const mac = textInput(t.mac, { placeholder: "3c:7c:3f:12:34:56" });
    const method = select([["relay", "An awake device on the same network sends it (recommended)"], ["server", "This Sphynx server sends it (only if it's on the same network)"]], t.method);
    const relay = select(relays.map((d) => [d.id, devName(d) + (d.online ? "" : " (offline)")]), t.relayNodeId || relays.find((d) => d.online && d.os !== "iOS")?.id);
    const linked = select([["", "— none —"], ...devices.filter((d) => !d.helper).map((d) => [d.id, devName(d)])], t.nodeId || "");
    const relayField = field("Device that sends the wake signal", relay, "Pick something that's always on and on the same home network, like a Raspberry Pi, NAS or another PC running Tailscale. It must belong to a person (not be tagged).");
    const sync = () => relayField.classList.toggle("hidden", method.value !== "relay");
    method.addEventListener("change", sync);
    sync();
    const body = h("div", null,
      field("Name", name),
      field("MAC address of its network card", mac, h("span", null, "Windows: run ", h("code", null, "getmac /v"), ". macOS: ", h("code", null, "ifconfig en0 | grep ether"), ". Linux: ", h("code", null, "ip link"), ". Use the wired (Ethernet) adapter.")),
      field("How to send the wake signal", method), relayField,
      field("The computer's own Sphynx device (optional)", linked, "Lets Sphynx show when the computer comes online."));
    const saved = await modal({
      title: t.id ? "Edit " + t.name : "Add a computer to wake",
      body,
      buttons: [{ text: "Cancel", value: null }, { text: "Save", class: "primary", action: () => api("POST", "/api/wol", { id: t.id || "", name: name.value, mac: mac.value, method: method.value, relayNodeId: method.value === "relay" ? relay.value : "", nodeId: linked.value }) }],
    });
    if (saved) { toast("Saved"); go("wol"); render(); }
  }

  const rows = targets.map((t) => {
    const target = t.nodeId && byId[t.nodeId];
    const via = t.method === "server" ? "Sphynx server" : byId[t.relayNodeId] ? devName(byId[t.relayNodeId]) : "missing device";
    const result = h("div", { class: "secondary-text" }, t.lastWake && !isZeroTime(t.lastWake) ? `Last wake ${timeAgo(t.lastWake)}: ${t.lastResult}` : "Never woken");
    return h("tr", null,
      h("td", { "data-label": "Computer" }, h("div", { class: "primary-text" }, t.name), result),
      h("td", { "data-label": "MAC", class: "mono" }, t.mac),
      h("td", { "data-label": "Sent by" }, via, t.method === "relay" && byId[t.relayNodeId] ? h("div", null, statusBadge(byId[t.relayNodeId].online, byId[t.relayNodeId].lastSeen)) : null),
      h("td", { "data-label": "Status" }, target ? statusBadge(target.online, target.lastSeen) : h("span", { class: "faint" }, "—")),
      h("td", { class: "actions" },
        h("button", { class: "primary small", onclick: async (e) => {
          const r = await run(e.currentTarget, () => api("POST", `/api/wol/${t.id}/wake`));
          toast("Wake signal sent: " + r.result);
          render();
        } }, icon("wol"), "Wake"), " ",
        h("button", { class: "small", onclick: () => editTarget(t) }, "Edit"), " ",
        h("button", { class: "small danger", onclick: async () => {
          if (!(await confirmAsk("Remove " + t.name + "?", "You can add it again any time.", "Remove", true))) return;
          await run(null, () => api("DELETE", "/api/wol/" + t.id), "Removed"); render();
        } }, "Remove")));
  });

  el.replaceChildren(
    pageHead("Wake-on-LAN", "Switch on a sleeping or powered-off computer from anywhere. Sphynx asks an awake device on the same network to broadcast the wake signal, so nothing extra needs installing.",
      h("button", { class: "primary", onclick: () => editTarget() }, icon("add"), "Add computer")),
    targets.length ? card(null, null, h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
      h("thead", null, h("tr", null, h("th", { text: "Computer" }), h("th", { text: "MAC" }), h("th", { text: "Sent by" }), h("th", { text: "Status" }), h("th"))),
      h("tbody", null, rows))))
      : card(null, null, empty("No computers yet", "Add the computer you want to switch on remotely.", h("button", { class: "primary", onclick: () => editTarget() }, "Add computer"))),
    card("Make sure the computer can be woken", null, h("ol", { class: "steps" },
      h("li", null, "Turn on ", h("strong", null, "Wake on LAN"), " (sometimes “Power on by PCI-E” or “Resume by LAN”) in the computer's BIOS/UEFI settings."),
      h("li", null, "Windows: Device Manager → your Ethernet adapter → Properties → Power Management: allow it to wake the computer, and in Advanced enable “Wake on Magic Packet”. Turning off “Fast startup” helps when the PC is shut down."),
      h("li", null, "Use a wired connection. Most computers can't be woken over Wi-Fi."),
      h("li", null, "The device that sends the signal must be on the same home network and online in Sphynx."))));

  if (arg && arg.startsWith("new")) {
    const params = new URLSearchParams(arg.split("?")[1] || "");
    editTarget({ name: params.get("name") || "", mac: params.get("mac") || "", nodeId: params.get("node") || "", method: relays.length ? "relay" : "server" });
  }
};

// ---------- Sphynx Chat ----------

function chatOpenURL(C) {
  const local = ["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);
  if (local && C.localPort) return `http://localhost:${C.localPort}/`;
  return C.addresses[0];
}

PAGES.chat = async (el) => {
  const C = await api("GET", "/api/chat");
  const s = C.settings;
  const enabled = toggleRow("Turn on Sphynx Chat", "Every device on your Sphynx network can open it and chat with every other device.", s.enabled);
  const maxMb = h("input", { type: "number", min: 1, max: 16384, value: s.maxUploadMb });
  const secure = checkbox("Also serve the chat over HTTPS", "Browsers only allow calls, voice notes, location and notifications on HTTPS addresses.", s.secure);
  const secureHost = textInput(s.secureHost, { placeholder: C.secureHost || "chat.myname.duckdns.org" });

  const save = h("button", { class: "primary", onclick: async (e) => {
    await run(e.currentTarget, () => api("PUT", "/api/chat", {
      enabled: enabled.input.checked, maxUploadMb: Number(maxMb.value), secure: secure.input.checked, secureHost: secureHost.value.trim(),
    }), "Saved");
    render();
  } }, "Save");

  const open = s.enabled ? h("a", { class: "btn", href: chatOpenURL(C), target: "_blank", rel: "noopener" }, icon("chat"), "Open chat") : null;
  const parts = [pageHead("Sphynx Chat", "A WhatsApp-style messenger for your Sphynx network. Every device is its own contact with its own chat, and messages are end-to-end encrypted between browsers.", open, save)];
  if (!C.homeOnline) parts.push(notice("warn", "Sphynx home isn't connected yet, so devices can't open the chat. It connects by itself a minute after Sphynx starts."));

  const httpsStatus = !C.tlsOn
    ? notice("warn", "Turn on HTTPS under ", h("a", { href: "#/settings" }, "Settings"), " first (Let's Encrypt or your own certificate). Until then the chat works over http, without calls, voice notes and notifications.")
    : !s.secure ? h("p", { class: "muted" }, "Off.")
    : C.secureReady ? h("div", null, h("div", { class: "inline", style: "margin-bottom:8px" }, h("span", { class: "muted small" }, "Status:"), badge("Ready", "ok")), codebox("https://" + C.secureHost + "/"))
    : h("div", null,
      h("div", { class: "inline", style: "margin-bottom:8px" }, h("span", { class: "muted small" }, "Status:"), badge("Waiting for a certificate", "warn")),
      C.secureError ? notice("bad", h("strong", null, "Last try: "), C.secureError) : null,
      h("p", { class: "small" }, "Sphynx asks Let's Encrypt for a certificate for ", h("span", { class: "mono" }, C.secureHost || "chat.<your domain>"), ". For that, the name must point at the same public IP address as your server, and ports 80 and 443 must be forwarded to this PC. DuckDNS names do this by themselves: chat.myname.duckdns.org already points wherever myname.duckdns.org does."));

  parts.push(h("div", { class: "grid two" },
    card("Open the chat", null,
      h("div", { class: "small muted" }, "On a phone, tablet or computer: connect it to Sphynx with the Tailscale app, then open this in its browser:"),
      codebox(C.addresses[0]),
      C.addresses.length > 1 ? h("div", { class: "small muted" }, "Or: ", ...C.addresses.slice(1).flatMap((a, i) => [i ? ", " : null, h("span", { class: "mono", text: a })])) : null,
      C.localPort ? h("div", { class: "small muted mt" }, "On this PC (it chats as Sphynx home):") : null,
      C.localPort ? codebox(`http://localhost:${C.localPort}/`) : null,
      h("p", { class: "small muted" }, "Each device shows up as its own contact, named like it is on the Devices page. On phones, use the browser's \"Add to Home screen\" to get a chat app icon.")),
    card("Settings", null, enabled,
      field("Largest file people can send (MB)", maxMb, "Files and photos are kept on this PC until their messages are deleted."))));

  parts.push(card("HTTPS address", "Optional. Over http, chats, photos, files, groups and statuses all work; calls, voice notes, location and notifications need HTTPS.",
    secure, field("HTTPS address (optional)", secureHost, "Leave empty for " + (C.secureHost || "chat.<your server's name>") + "."), httpsStatus));
  el.replaceChildren(...parts);
};

// ---------- Sphynx Link ----------

PAGES.link = async (el) => {
  const L = await api("GET", "/api/link");
  const s = L.settings;
  const enabled = toggleRow("Turn on Sphynx Link", "Run a Tailcat server on Sphynx home.", s.enabled);
  const exit = checkbox("Use my home internet and network", "Link users get a local proxy that sends their traffic out through this PC.", s.exitNode);
  const inbox = checkbox("Inbox", "Accept files sent to home. Senders can't see or read anything in the inbox.", s.inbox);
  const admin = checkbox("This admin page", "Open the Sphynx admin page from anywhere (it still asks for your password).", s.admin);
  const derp = textInput(s.derpMapUrl, { placeholder: "default: Tailcat's free relays" });
  const allowRows = h("div");
  function allowRow(c) {
    const n = textInput(c.name, { placeholder: "Work laptop" });
    const k = textInput(c.key, { placeholder: "nodekey:…" });
    const row = h("div", { class: "row" }, h("div", { class: "field" }, n), h("div", { class: "field", style: "flex:3" }, k), h("div", { class: "field", style: "flex:none;min-width:0" }, h("button", { class: "small danger", onclick: () => row.remove() }, "Remove")));
    row.get = () => ({ name: n.value.trim(), key: k.value.trim() });
    return row;
  }
  for (const c of s.allow || []) allowRows.append(allowRow(c));

  const save = h("button", { class: "primary", onclick: async (e) => {
    await run(e.currentTarget, () => api("PUT", "/api/link", {
      enabled: enabled.input.checked, exitNode: exit.input.checked, inbox: inbox.input.checked, admin: admin.input.checked,
      allow: [...allowRows.children].map((r) => r.get()).filter((c) => c.key), derpMapUrl: derp.value.trim(),
    }), "Saved");
    setTimeout(() => render(), 2500);
  } }, "Save");

  const addr = L.address;
  const A = addr || "<address>";
  const status = !s.enabled ? badge("Off") : L.running && addr ? badge("Running", "ok") : L.running ? badge("Starting", "warn") : badge("Stopped", "bad");

  const parts = [pageHead("Sphynx Link", h("span", null, "A backup way home that needs no port forwarding and no VPN app. Built on ", h("a", { href: "https://github.com/tailscale/tailcat", target: "_blank", rel: "noopener" }, "Tailcat"), " by Tailscale: an encrypted WireGuard link set up with one secret address."), save)];
  if (!L.available) parts.push(notice("warn", "The tailcat program isn't included in this Sphynx build."));
  if (L.error) parts.push(notice("bad", h("strong", null, "Problem: "), L.error));

  parts.push(h("div", { class: "grid two" },
    card("Your link address", null,
      h("div", { class: "inline", style: "margin-bottom:8px" }, h("span", { class: "muted small" }, "Status:"), status),
      addr ? codebox(addr) : h("p", { class: "muted" }, s.enabled ? "Waiting for the address…" : "Turn on Sphynx Link to get an address."),
      notice("warn", h("strong", null, "Treat this like a password. "), (s.allow || []).length ? "Only the client keys listed below can connect." : "Anyone who has it can use the services you turn on."),
      addr ? h("button", { class: "danger", onclick: async (e) => {
        if (!(await confirmAsk("Get a new address?", "The old address stops working immediately for everyone.", "Replace address", true))) return;
        await run(e.currentTarget, () => api("POST", "/api/link/rotate"), "New address created");
        setTimeout(() => render(), 3000);
      } }, "Replace address") : null),
    card("Services", null, enabled, exit, inbox, admin)));

  parts.push(card("Use it from another computer", h("span", null, "Download the small ", h("code", null, "tailcat"), " program for Windows, macOS or Linux from ", h("a", { href: "https://github.com/tailscale/tailcat/releases", target: "_blank", rel: "noopener" }, "its releases page"), ". No install or admin rights needed. Then:"),
    h("div", { class: "small muted" }, "Use your home internet: this starts a proxy on port 1080. Set your browser's SOCKS5 proxy to 127.0.0.1:1080."),
    codebox(`tailcat socks --listen=127.0.0.1:1080 ${A}`),
    h("div", { class: "small muted" }, "Send a file home:"), codebox(`tailcat cp report.pdf ${A}:`),
    h("div", { class: "small muted" }, "Open this admin page (it opens in your browser):"), codebox(`tailcat forward --open-browser ${A} 0:${L.adminPort}`),
    h("div", { class: "small muted" }, "Check the connection (shows direct or relayed):"), codebox(`tailcat ping --until-direct ${A}`)));

  const files = L.inbox || [];
  parts.push(h("div", { class: "grid two" },
    card("Inbox", "Files sent with tailcat cp.",
      files.length ? h("ul", { class: "list-plain" }, files.map((f) => h("li", null,
        h("div", null, h("div", { class: "primary-text", text: f.name }), h("div", { class: "secondary-text" }, fmtBytes(f.size) + " · " + timeAgo(f.modTime))),
        h("div", { class: "nowrap" },
          h("a", { class: "btn small", href: "/api/link/inbox/" + encodeURIComponent(f.name) }, "Download"), " ",
          h("button", { class: "small danger", onclick: async () => { await run(null, () => api("DELETE", "/api/link/inbox/" + encodeURIComponent(f.name)), "Deleted"); render(); } }, "Delete")))))
        : h("p", { class: "muted" }, "Empty.")),
    card("Only allow certain computers (optional)", h("span", null, "On each computer run ", h("code", null, "tailcat genkey --client --key=client-default"), " and paste the nodekey it prints."),
      allowRows, h("button", { class: "small", onclick: () => allowRows.append(allowRow({})) }, "Add computer"),
      h("div", { class: "mt" }), field("Relay map URL (advanced)", derp, "Tailcat uses its free relays to set up connections; traffic stays end-to-end encrypted."))));
  el.replaceChildren(...parts);
};

// ---------- Radio ----------

const RADIO_LINKS = [["meshtastic", "Meshtastic (LoRa)"], ["aprs", "APRS (amateur radio)"]];
const radioLinkName = (l) => (l === "aprs" ? "APRS" : "Meshtastic");

PAGES.radio = async (el) => {
  const [R, devices] = await Promise.all([api("GET", "/api/radio"), api("GET", "/api/nodes").catch(() => [])]);
  const s = R.settings;

  const enabled = toggleRow("Turn on radio mode", "Sphynx listens on the radios below and answers your radio contacts.", s.enabled);
  const meshOn = checkbox("Use a Meshtastic radio", "LoRa radios for about US$30 to 100. No licence needed.", s.meshtastic.enabled);
  const meshAddr = textInput(s.meshtastic.address, { placeholder: "192.168.1.50 or /dev/ttyUSB0" });
  const aprsOn = checkbox("Use APRS", "Amateur packet radio. Needs an amateur radio licence.", s.aprs.enabled);
  const call = textInput(s.aprs.call, { placeholder: "5Z4ABC-10", autocapitalize: "characters", spellcheck: "false" });
  const pass = h("input", { type: "password", autocomplete: "new-password", inputmode: "numeric", placeholder: R.passcodeSet ? "Saved. Leave empty to keep it." : "From your APRS-IS account" });
  const clearPass = R.passcodeSet ? checkbox("Forget the saved passcode", "Without one, APRS-IS only lets Sphynx listen.", false) : null;
  const server = textInput(s.aprs.server, { placeholder: "rotate.aprs2.net:14580" });
  const kiss = textInput(s.aprs.kiss, { placeholder: "192.168.1.60:8001" });
  const path = textInput((s.aprs.path || []).join(", "), { placeholder: "WIDE1-1, WIDE2-1" });

  // Radio contacts, edited in place and saved with the page.
  const contactRows = h("div");
  const contactKeys = () => new Set([...contactRows.children].map((r) => r.get()).map((c) => c.link + ":" + c.address.toLowerCase()));
  function contactRow(c) {
    const name = textInput(c.name, { placeholder: "My handheld" });
    const link = select(RADIO_LINKS, c.link || "meshtastic");
    const addr = textInput(c.address, { class: "mono", spellcheck: "false" });
    const addrField = field("Node id", addr);
    const hint = () => {
      const aprs = link.value === "aprs";
      addr.placeholder = aprs ? "5Z4ABC-7" : "!a1b2c3d4";
      addrField.querySelector("label").textContent = aprs ? "Callsign" : "Node id";
    };
    link.addEventListener("change", hint);
    hint();
    const cmds = checkbox("Commands", "status, ping and wake", c.commands ?? true);
    const chat = checkbox("Chat", "A contact in Sphynx Chat", c.chat ?? true);
    const row = h("div", { class: "radio-contact" },
      h("div", { class: "row" }, field("Name", name), field("Radio", link), addrField),
      h("div", { class: "row middle" }, cmds, chat, h("div", { class: "spacer" }),
        c.id ? h("button", { class: "small", title: "Send it a test message", onclick: async (e) => {
          await run(e.currentTarget, () => api("POST", "/api/radio/test", { contactId: c.id }), "Sending a test message. The log shows how it went.");
          setTimeout(() => refresh().catch(() => {}), 2000);
        } }, "Test") : null,
        h("button", { class: "small danger", onclick: () => { row.remove(); drawContactsEmpty(); } }, "Remove")));
    row.get = () => ({ id: c.id || "", name: name.value.trim(), link: link.value, address: addr.value.trim(), commands: cmds.input.checked, chat: chat.input.checked });
    return row;
  }
  const contactsEmpty = h("p", { class: "muted" }, "No radio contacts yet. Only stations listed here can use Sphynx.");
  function drawContactsEmpty() { contactsEmpty.classList.toggle("hidden", contactRows.children.length > 0); }
  function addContact(c, scroll) {
    contactRows.append(contactRow(c));
    drawContactsEmpty();
    if (scroll) {
      contactRows.lastChild.scrollIntoView({ behavior: "smooth", block: "center" });
      toast("Added to your radio contacts. Save to keep it.");
    }
  }
  for (const c of s.contacts || []) addContact(c);
  drawContactsEmpty();

  // Where chat messages from radio contacts go.
  const people = devices.filter((d) => !d.helper || d.home);
  const deliver = people.map((d) => {
    const cb = checkbox(devName(d) + (d.home ? " (Sphynx home, this PC)" : ""), null, (s.deliverTo || []).includes(d.id));
    cb.nodeId = d.id;
    return cb;
  });

  const save = h("button", { class: "primary", onclick: async (e) => {
    await run(e.currentTarget, () => api("PUT", "/api/radio", {
      enabled: enabled.input.checked,
      meshtastic: { enabled: meshOn.input.checked, address: meshAddr.value.trim() },
      aprs: { enabled: aprsOn.input.checked, call: call.value.trim(), server: server.value.trim(), kiss: kiss.value.trim(), path: path.value.split(/[\s,]+/).filter(Boolean) },
      contacts: [...contactRows.children].map((r) => r.get()).filter((c) => c.name || c.address),
      deliverTo: deliver.filter((cb) => cb.input.checked).map((cb) => cb.nodeId),
      passcode: pass.value.trim(),
      clearPasscode: !!clearPass?.input.checked,
    }), "Saved");
    render();
  } }, "Save");

  // Parts that refresh while the page is open.
  const linksBox = h("div");
  const nodesBox = h("div");
  const heardBox = h("div");
  const logBox = h("div");
  function draw(R) {
    linksBox.replaceChildren(!s.enabled ? h("p", { class: "muted small" }, "Radio mode is off.")
      : !R.links.length ? h("p", { class: "muted small" }, "No radio turned on.")
      : h("ul", { class: "list-plain" }, R.links.map((l) => h("li", null,
        h("div", null, h("div", { class: "primary-text", text: l.name }), h("div", { class: "secondary-text" }, l.error || l.detail || "")),
        l.connected ? badge("Connected", "ok") : l.error ? badge("Not connected", "bad") : badge("Connecting", "warn")))));

    const keys = contactKeys();
    const nodes = R.nodes.filter((n) => !keys.has("meshtastic:" + n.id)).sort((a, b) => (b.lastHeard || "").localeCompare(a.lastHeard || "")).slice(0, 30);
    nodesBox.replaceChildren(nodes.length ? h("ul", { class: "list-plain" }, nodes.map((n) => h("li", null,
      h("div", null, h("div", { class: "primary-text" }, n.longName || n.shortName || n.id, " ", h("span", { class: "mono faint small", text: n.id })),
        h("div", { class: "secondary-text" }, [n.lastHeard ? "heard " + timeAgo(n.lastHeard) : "not heard yet", n.direct ? "directly" + (n.snr != null ? `, SNR ${n.snr.toFixed(1)} dB` : "") : null].filter(Boolean).join(", "))),
      h("button", { class: "small", onclick: () => addContact({ name: n.longName || n.shortName || n.id, link: "meshtastic", address: n.id }, true) }, "Add as contact"))))
      : h("p", { class: "muted small" }, R.links.some((l) => l.name.startsWith("Meshtastic") && l.connected) ? "None besides your contacts." : "Shown once the Meshtastic radio is connected."));

    const heard = R.heard.filter((m) => !keys.has(m.link + ":" + m.from.toLowerCase()));
    heardBox.replaceChildren(heard.length ? h("ul", { class: "list-plain" }, heard.map((m) => h("li", null,
      h("div", null, h("div", { class: "primary-text" }, m.name || m.from, " ", h("span", { class: "mono faint small", text: m.name ? m.from : "" }), " ", badge(radioLinkName(m.link))),
        h("div", { class: "secondary-text" }, `"${m.text}", ${timeAgo(m.at)}`)),
      h("button", { class: "small", onclick: () => addContact({ name: m.name || m.from, link: m.link, address: m.from }, true) }, "Add as contact"))))
      : h("p", { class: "muted small" }, "Nobody else has messaged Sphynx."));

    logBox.replaceChildren(R.log.length ? h("ul", { class: "list-plain" }, R.log.map((e) => h("li", null,
      h("div", null,
        h("div", { class: "primary-text" }, e.out ? "To " : "From ", e.station, " ", badge(radioLinkName(e.link)), " ", h("span", { class: "mono small", text: e.text })),
        e.result ? h("div", { class: "secondary-text" + (e.result.startsWith("not") ? " bad-text" : "") }, e.result) : null),
      h("div", { class: "faint small nowrap" }, timeAgo(e.at)))))
      : h("p", { class: "muted small" }, "No radio messages yet."));
  }
  async function refresh() { draw(await api("GET", "/api/radio")); }
  draw(R);

  async function showCode() {
    const c = await api("POST", "/api/radio/code");
    await modal({
      title: "Scan this with an authenticator app",
      body: h("div", null,
        h("p", null, "Use any authenticator app, like Google Authenticator, Microsoft Authenticator, Aegis or 2FAS. It then shows a new 6-digit code for Sphynx every 30 seconds."),
        h("div", { class: "qr-wrap" }, h("img", { class: "qr", src: c.qr, alt: "QR code for the authenticator app" })),
        h("p", { class: "small muted" }, "Can't scan it? Type this key into the app instead (time-based):"),
        codebox(c.secret),
        notice("warn", "Sphynx shows this only once. Anyone with it can make wake codes.")),
      buttons: [{ text: "Done", class: "primary", value: true }],
    });
    render();
  }

  const parts = [pageHead("Radio", "Reach Sphynx with short text messages over long-range radio where there is no internet at all. A radio at home passes them on: commands to check on things and wake computers, and chats with your devices in Sphynx Chat. Radio carries text only, not internet traffic.", save)];
  if (!R.chatOn) parts.push(notice("warn", "Sphynx Chat is off, so radio contacts can only use commands. Turn it on under ", h("a", { href: "#/chat" }, "Chat"), "."));

  parts.push(h("div", { class: "grid two" },
    card("Radio mode", null, enabled, h("div", { class: "mt" }), linksBox),
    card("What you need", null, h("ol", { class: "steps" },
      h("li", null, h("strong", null, "A radio at home"), " that this PC can reach: a Meshtastic radio on your Wi-Fi or plugged in by USB, or an APRS radio with a TNC such as Dire Wolf. APRS can also use the internet's APRS-IS network."),
      h("li", null, h("strong", null, "A radio with you"), ": a Meshtastic radio paired with the Meshtastic app on your phone, or an APRS radio that sends messages."),
      h("li", null, "Add your radio below as a contact, then send ", h("code", null, "help"), " to Sphynx from it.")))));

  parts.push(h("div", { class: "grid two" },
    card("Meshtastic (LoRa)", "Each radio reaches roughly 2 to 15 km, more from high ground, and further when other Meshtastic radios in between pass messages on.",
      meshOn,
      field("The radio at home", meshAddr, h("span", null, "Its IP address on your network (turn on Wi-Fi or Ethernet in the radio's Network settings), or a USB port like ", h("code", null, "/dev/ttyUSB0"), " when Sphynx runs on Linux. Give Sphynx a radio of its own: a phone connected to the same radio would take the messages first.")),
      h("p", { class: "small muted" }, "From your radio, send a direct message to the home radio in the Meshtastic app.")),
    card("APRS (amateur radio)", "APRS reaches far: through digipeaters, and internet gateways (iGates) that pass messages to APRS-IS, a message sent from hundreds or thousands of kilometres away can reach Sphynx.",
      aprsOn,
      notice("warn", h("strong", null, "APRS messages are public. "), "Anyone can read them, for example on aprs.fi, and callsigns can be faked, so wake commands always need a code. You need an amateur radio licence to send."),
      h("div", { class: "mt" }),
      h("div", { class: "row" }, field("Sphynx's callsign", call, "Yours, with an SSID of its own, like -10."), field("APRS-IS passcode", pass, "Lets Sphynx send through APRS-IS. Without it Sphynx only listens there.")),
      clearPass,
      field("APRS-IS server", server, "Leave empty to use only the radio at home."),
      h("div", { class: "row" }, field("KISS TNC at home (optional)", kiss, "host:port of Dire Wolf or a KISS TNC, for sending and hearing over your own radio."), field("Digipeater path", path)),
      h("p", { class: "small muted" }, "From your radio, send an APRS message to Sphynx's callsign."))));

  parts.push(card("Radio contacts", "The stations that may use Sphynx. Messages from anyone else are ignored and listed under Recently heard.",
    contactsEmpty, contactRows,
    h("div", { class: "inline mt" }, h("button", { class: "small", onclick: () => addContact({ link: "meshtastic", commands: true, chat: true }) }, icon("add"), "Add contact"), h("span", { class: "muted small" }, "Save the page to keep changes."))));

  parts.push(h("div", { class: "grid two" },
    card("Meshtastic radios in range", "Radios the home radio knows about.", nodesBox),
    card("Recently heard", "Stations that messaged Sphynx but aren't your contacts.", heardBox)));

  parts.push(h("div", { class: "grid two" },
    card("Wake computers over the radio", null,
      h("p", null, "Send ", h("code", null, "wake <computer> <code>"), ", with the 6-digit code your authenticator app shows for Sphynx. Each code works once; after 5 wrong codes, waking stops for an hour."),
      R.codeSet
        ? h("div", { class: "inline" }, badge("Wake codes on", "ok"),
          h("button", { class: "small", onclick: async (e) => {
            if (!(await confirmAsk("Replace the wake codes?", "Codes from the old entry in your authenticator app stop working.", "Replace"))) return;
            await run(e.currentTarget, showCode);
          } }, "Replace"),
          h("button", { class: "small danger", onclick: async (e) => {
            if (!(await confirmAsk("Turn off wake codes?", "Nobody can wake computers over the radio until you set them up again.", "Turn off", true))) return;
            await run(e.currentTarget, () => api("DELETE", "/api/radio/code"), "Wake codes off");
            render();
          } }, "Turn off"))
        : h("div", null, h("p", { class: "muted small" }, "Off: nobody can wake computers over the radio."), h("button", { class: "primary", onclick: (e) => run(e.currentTarget, showCode) }, "Set up wake codes")),
      h("p", { class: "small muted mt" }, R.wakeTargets.length ? "Computers: " + R.wakeTargets.join(", ") + "." : h("span", null, "Add the computers to wake on the ", h("a", { href: "#/wol" }, "Wake-on-LAN"), " page."))),
    card("Commands", "Send these from a radio contact with Commands ticked.", h("div", { class: "kv" },
      h("div", null, h("code", null, "status")), h("div", null, "Which devices are online, and this PC's internet."),
      h("div", null, h("code", null, "status <device>")), h("div", null, "One device: online or last seen, and its address."),
      h("div", null, h("code", null, "ping")), h("div", null, "Checks the link and tells you how well Sphynx hears you."),
      h("div", null, h("code", null, "wake <computer> <code>")), h("div", null, "Switches on a computer with Wake-on-LAN."),
      h("div", null, h("code", null, "help")), h("div", null, "The list of commands."),
      h("div", null, h("code", null, "@laptop on my way")), h("div", null, "With Chat ticked: a chat message for one device. Anything that isn't a command goes to Sphynx Chat.")))));

  parts.push(card("Chat messages from radio go to", "A radio contact's messages go to every device that has a chat with it. Until one does, they go to the devices ticked here, or to Sphynx home on this PC (open http://localhost:3001) when none are.",
    deliver.length ? h("div", { class: "radio-deliver" }, deliver) : h("p", { class: "muted small" }, "No devices yet.")));

  parts.push(card("Log", "The last radio messages, newest first.", logBox));
  el.replaceChildren(...parts);
  every(5000, refresh);
};

// ---------- Public shares ----------

PAGES.shares = async (el) => {
  const [data, devices] = await Promise.all([api("GET", "/api/shares"), api("GET", "/api/nodes")]);
  const byId = Object.fromEntries(devices.map((d) => [d.id, d]));
  const base = data.serverUrl.replace(/\/$/, "");
  const enabledRow = toggleRow("Allow public shares", "Publish selected services from your network on the internet, like Tailscale Funnel.", data.enabled, async (on, input) => {
    try { await api("PUT", "/api/shares/settings", { enabled: on }); toast(on ? "Public shares on" : "Public shares off"); } catch (e) { input.checked = !on; toast(e.message, true); }
  });

  async function editShare(s) {
    s = s || { enabled: true, port: 80 };
    const name = textInput(s.name, { placeholder: "photos" });
    const dev = select(devices.filter((d) => !d.helper).map((d) => [d.id, devName(d)]), s.nodeId);
    const port = h("input", { type: "number", min: 1, max: 65535, value: s.port });
    const https = checkbox("The service uses HTTPS itself", null, s.https);
    const en = checkbox("Enabled", null, s.enabled);
    const saved = await modal({
      title: s.id ? "Edit share" : "New public share",
      body: h("div", null, field("Name", name, h("span", null, "Public address: ", h("span", { class: "mono" }, base + "/s/<name>/"))), field("Device running the service", dev), field("Port", port), https, en),
      buttons: [{ text: "Cancel", value: null }, { text: "Save", class: "primary", action: () => api("POST", "/api/shares", { id: s.id || "", name: name.value, nodeId: dev.value, port: Number(port.value), https: https.input.checked, enabled: en.input.checked }) }],
    });
    if (saved) { toast("Saved"); render(); }
  }

  const rows = data.shares.map((s) => {
    const url = `${base}/s/${s.name}/`;
    return h("tr", null,
      h("td", { "data-label": "Public address" }, h("a", { href: url, target: "_blank", rel: "noopener", class: "mono", text: url }), copyButton(url)),
      h("td", { "data-label": "Goes to" }, (byId[s.nodeId] ? devName(byId[s.nodeId]) : "missing device") + " : " + s.port),
      h("td", { "data-label": "Status" }, s.enabled ? badge("On", "ok") : badge("Off")),
      h("td", { class: "actions" },
        h("button", { class: "small", onclick: () => editShare(s) }, "Edit"), " ",
        h("button", { class: "small danger", onclick: async () => { await run(null, () => api("DELETE", "/api/shares/" + s.id), "Removed"); render(); } }, "Remove")));
  });

  el.replaceChildren(
    pageHead("Public shares", "Sphynx's version of Tailscale Funnel: a web service on one of your devices, reachable by anyone at a public address on your Sphynx server.", h("button", { class: "primary", onclick: () => editShare() }, icon("add"), "New share")),
    card(null, null, enabledRow),
    notice("warn", h("strong", null, "Anyone on the internet can open these addresses. "), "Only share services that have their own login. Your server must be reachable from the internet (HTTPS recommended). Apps that expect to live at the top of a domain may not work under /s/<name>/."),
    data.shares.length ? card(null, null, h("div", { class: "table-wrap" }, h("table", { class: "responsive" },
      h("thead", null, h("tr", null, h("th", { text: "Public address" }), h("th", { text: "Goes to" }), h("th", { text: "Status" }), h("th"))),
      h("tbody", null, rows)))) : card(null, null, empty("No shares", "Create one to publish a service.")));
};

// ---------- Settings ----------

PAGES.settings = async (el) => {
  const [data, users] = await Promise.all([api("GET", "/api/settings"), loadUsers()]);
  const s = data.settings;

  const serverUrl = textInput(s.serverUrl, { type: "url" });
  const tlsMode = select([["none", "Off: plain HTTP (home network, or behind your own reverse proxy)"], ["letsencrypt", "Let's Encrypt: free certificate for your domain"], ["manual", "My own certificate files"]], s.tls.mode);
  const tlsHost = textInput(s.tls.hostname, { placeholder: "vpn.example.com" });
  const tlsEmail = textInput(s.tls.email, { type: "email", placeholder: "for expiry notices (optional)" });
  const tlsChallenge = select([["HTTP-01", "HTTP-01 (needs TCP port 80)"], ["TLS-ALPN-01", "TLS-ALPN-01 (only port 443)"]], s.tls.challengeType || "HTTP-01");
  const tlsCert = textInput(s.tls.certPath, { placeholder: "/data/certs/fullchain.pem" });
  const tlsKey = textInput(s.tls.keyPath, { placeholder: "/data/certs/privkey.pem" });
  const leBox = h("div", null, h("div", { class: "row" }, field("Domain name", tlsHost), field("Email", tlsEmail)), field("Verification", tlsChallenge));
  const manBox = h("div", null, h("div", { class: "row" }, field("Certificate (PEM)", tlsCert), field("Private key (PEM)", tlsKey)), h("p", { class: "small muted" }, "Paths inside the container. Put the files in the data volume. Renewed files are picked up automatically."));
  const syncTLS = () => { leBox.classList.toggle("hidden", tlsMode.value !== "letsencrypt"); manBox.classList.toggle("hidden", tlsMode.value !== "manual"); };
  tlsMode.addEventListener("change", () => {
    syncTLS();
    if (tlsMode.value === "letsencrypt" && tlsHost.value && !serverUrl.value.startsWith("https://")) serverUrl.value = "https://" + tlsHost.value;
  });
  tlsHost.addEventListener("input", () => { if (tlsMode.value === "letsencrypt") serverUrl.value = "https://" + tlsHost.value.trim(); });
  syncTLS();

  const derpPublic = toggleRow("Tailscale's public relays", "Free relays around the world for when two devices can't connect directly. They only ever see encrypted traffic.", s.derp.usePublic);
  const derpEmbedded = toggleRow("Built-in relay on this server", "Your own relay and STUN server (needs HTTPS and UDP port 3478 forwarded). Best for privacy and for the only-port-443 networks.", s.derp.embedded);
  const derpV4 = textInput(s.derp.ipv4, { placeholder: "your public IPv4 (optional)" });
  const derpV6 = textInput(s.derp.ipv6, { placeholder: "your public IPv6 (optional)" });

  const owner = select(users.map((u) => [u.name, userName(u)]), s.owner);
  const homeExit = toggleRow("Exit node", "Remote devices can use this PC's internet connection, as if they were plugged into your home network.", s.home.exitNode);
  const homeLAN = listEditor(s.home.lanRoutes, "192.168.1.0/24", 2);
  const relayPort = h("input", { type: "number", min: 0, max: 65535, value: s.home.relayPort || 0 });
  const relayEps = listEditor(s.home.relayEndpoints, "203.0.113.7:40000", 2);

  const R = s.resilience;
  const rCache = toggleRow("Reconnect without the server", "Devices remember the network map, so after a restart they reach your other devices even while this server is unreachable.", R.cacheNetmaps);
  const rReach = toggleRow("Trust working tunnels", "Devices judge for themselves whether a peer is reachable, so a shaky link to this server doesn't tear down tunnels that still work.", R.clientReachability);
  const rProbe = toggleRow("Keep direct paths alive on cellular", "Devices measure how long the mobile network keeps connections open and keep direct paths from silently dying.", R.probeUdpLifetime);
  const rAuto = toggleRow("Automatic exit node", "Allow “auto” exit node choice, which picks the best exit node and fails over by itself.", R.autoExitNode);
  const rMTU = toggleRow("Packet size discovery", "Find the largest packet each path allows. Helps on satellite and some mobile networks. Experimental in Tailscale.", R.peerMtu);

  const expiry = select([["0", "Never"], ["30d", "30 days"], ["90d", "90 days"], ["180d", "180 days"], ["365d", "1 year"]], s.nodes.expiry);
  if (![...expiry.options].some((o) => o.value === s.nodes.expiry)) expiry.append(h("option", { value: s.nodes.expiry, text: s.nodes.expiry, selected: true }));
  const ephemeral = textInput(s.nodes.ephemeralTimeout);
  const taildrop = toggleRow("Taildrop", "Send files between your devices.", s.features.taildrop);
  const autoUpdate = toggleRow("Automatic Tailscale updates", "Devices that haven't chosen update themselves.", s.features.autoUpdate);

  const O = s.oidc;
  const oEnabled = toggleRow("Sign in with Google, Microsoft, Authentik…", "Let people add devices by signing in with an OpenID Connect provider.", O.enabled);
  const oIssuer = textInput(O.issuer, { placeholder: "https://accounts.google.com" });
  const oClient = textInput(O.clientId);
  const oSecret = h("input", { type: "password", placeholder: data.oidcSecretSet ? "saved (leave empty to keep)" : "" });
  const oDomains = textInput((O.allowedDomains || []).join(", "), { placeholder: "example.com" });
  const oUsers = textInput((O.allowedUsers || []).join(", "), { placeholder: "alice@example.com" });
  const oGroups = textInput((O.allowedGroups || []).join(", "));
  const oPKCE = checkbox("Use PKCE", "Recommended when the provider supports it.", O.pkce);

  const v4 = textInput(s.prefixes.v4);
  const v6 = textInput(s.prefixes.v6);
  const alloc = select([["sequential", "In order"], ["random", "Random"]], s.prefixes.allocation);
  const logLevel = select(["info", "debug", "warn", "error"], s.logLevel);
  const bcast = listEditor(s.wol.broadcasts, "192.168.1.255", 2);
  const csv = (input) => input.value.split(",").map((x) => x.trim()).filter(Boolean);

  const saveBtn = h("button", { class: "primary", onclick: async (e) => {
    const body = Object.assign({}, s, {
      serverUrl: serverUrl.value.trim(),
      owner: owner.value,
      tls: { mode: tlsMode.value, hostname: tlsHost.value.trim(), email: tlsEmail.value.trim(), challengeType: tlsChallenge.value, certPath: tlsCert.value.trim(), keyPath: tlsKey.value.trim() },
      derp: { embedded: derpEmbedded.input.checked, usePublic: derpPublic.input.checked, ipv4: derpV4.value.trim(), ipv6: derpV6.value.trim() },
      home: { exitNode: homeExit.input.checked, lanRoutes: homeLAN.getList(), relayPort: Number(relayPort.value) || 0, relayEndpoints: relayEps.getList() },
      resilience: { cacheNetmaps: rCache.input.checked, clientReachability: rReach.input.checked, probeUdpLifetime: rProbe.input.checked, autoExitNode: rAuto.input.checked, peerMtu: rMTU.input.checked },
      nodes: { expiry: expiry.value, ephemeralTimeout: ephemeral.value.trim() },
      features: { taildrop: taildrop.input.checked, autoUpdate: autoUpdate.input.checked },
      oidc: { enabled: oEnabled.input.checked, issuer: oIssuer.value.trim(), clientId: oClient.value.trim(), clientSecret: oSecret.value, scopes: O.scopes, allowedDomains: csv(oDomains), allowedUsers: csv(oUsers), allowedGroups: csv(oGroups), pkce: oPKCE.input.checked },
      prefixes: { v4: v4.value.trim(), v6: v6.value.trim(), allocation: alloc.value },
      wol: { broadcasts: bcast.getList() },
      logLevel: logLevel.value,
    });
    const r = await run(e.currentTarget, () => api("PUT", "/api/settings", body));
    toast(r.restarted ? "Saved. The control server is restarting (a few seconds)." : "Saved");
  } }, "Save settings");

  const pwCur = h("input", { type: "password", autocomplete: "current-password" });
  const pwNew = h("input", { type: "password", autocomplete: "new-password" });

  const helpers = (data.helpers || []).map((x) => h("li", null,
    h("div", null, h("div", { class: "primary-text" }, x.hostname || "sphynx-" + x.user, x.home ? badge("home", "accent") : null), h("div", { class: "secondary-text" }, `user ${x.user} · ${x.state || "starting"}${x.ips ? " · " + x.ips.join(", ") : ""}`, x.error ? h("div", { class: "small", style: "color:var(--bad)" }, x.error) : null)),
    x.running ? badge("running", "ok") : badge("stopped", "bad")));

  el.replaceChildren(
    pageHead("Settings", "Changes to the server, HTTPS, relays or sign-in restart the control server for a few seconds. Devices reconnect by themselves.", saveBtn),
    card("Server address", "The address devices use to reach Sphynx. Changing it means existing devices must be pointed at the new address.",
      field("Server address", serverUrl, h("span", null, "Home only: http://<this PC's IP>:8080. From anywhere: forward the ports on your router to this PC and use HTTPS below. Ports: TCP 8080 (plain) or TCP 443 + 80 (HTTPS), UDP 3478 (relay), UDP ", String(data.ports.homeWireGuard), " (direct connections to Sphynx home)."))),
    card("HTTPS", "Encrypts the control connection's outer layer and is needed by phones, the built-in relay and public shares.", field("HTTPS mode", tlsMode), leBox, manBox),
    card("Relays", "Used only when two devices can't connect directly (strict NATs, firewalls, mobile networks).", derpPublic, derpEmbedded, h("div", { class: "row mt" }, field("Public IPv4", derpV4), field("Public IPv6", derpV6))),
    card("Sphynx home", "The device Sphynx itself runs on your network from inside this container.",
      field("Owner", owner, "Sphynx home belongs to this user. Pick yourself."),
      homeExit,
      field("Home networks to share", homeLAN, "Your router's network, e.g. 192.168.1.0/24, so remote devices can reach printers, NAS, cameras… One per line."),
      h("div", { class: "row" }, field("Peer relay UDP port", relayPort, "0 = off. When set, devices that can't connect directly relay through your home over fast UDP instead of HTTPS relays. Forward this UDP port on your router and in Docker."), field("Relay public address:port", relayEps, "Your public IP and the forwarded port, so devices can find the relay."))),
    card("Weak-signal mode", "For travel in remote areas with patchy cellular, satellite or hotel networks. Applied to every device as Tailscale settings. Make sure devices run a recent Tailscale version.",
      rCache, rReach, rProbe, rAuto, rMTU,
      h("p", { class: "small muted mt mb0" }, "Per-device “strict firewall mode” (HTTPS port 443 only) is on each device's page.")),
    card("Devices", null, h("div", { class: "row" }, field("Login expiry", expiry, "How often devices must sign in again."), field("Remove ephemeral devices after", ephemeral, "Offline time, like 30m.")), taildrop, autoUpdate),
    card("Single sign-on (optional)", null, oEnabled,
      h("div", { class: "row" }, field("Issuer URL", oIssuer), field("Client ID", oClient), field("Client secret", oSecret)),
      h("div", { class: "row" }, field("Allowed email domains", oDomains), field("Allowed emails", oUsers), field("Allowed groups", oGroups)),
      oPKCE, h("p", { class: "small muted" }, "Redirect URL to register with the provider: ", h("span", { class: "mono" }, serverUrl.value.replace(/\/$/, "") + "/oidc/callback"))),
    card("Advanced", null,
      h("div", { class: "row" }, field("IPv4 range", v4), field("IPv6 range", v6), field("Address assignment", alloc)),
      h("div", { class: "row" }, field("Log level", logLevel), field("Wake-on-LAN broadcast addresses (server method)", bcast))),
    h("div", { class: "right", style: "margin-bottom:16px" }, h("button", { class: "primary", onclick: () => saveBtn.click() }, "Save settings")),
    h("div", { class: "grid two" },
      card("Admin password", null, field("Current password", pwCur), field("New password", pwNew),
        h("button", { onclick: async (e) => { await run(e.currentTarget, () => api("POST", "/api/password", { current: pwCur.value, new: pwNew.value }), "Password changed"); pwCur.value = pwNew.value = ""; } }, "Change password")),
      card("Backup & maintenance", "The backup contains the database, keys and settings. Keep it safe: it can impersonate your server.",
        h("div", { class: "inline" },
          h("a", { class: "btn", href: "/api/backup" }, "Download backup"),
          h("button", { onclick: async (e) => { if (await confirmAsk("Restart the control server?", "Devices reconnect within seconds.", "Restart")) await run(e.currentTarget, () => api("POST", "/api/headscale/restart"), "Restarting"); } }, "Restart control server")),
        h("h3", { text: "Sphynx's own devices", style: "margin:16px 0 4px" }),
        helpers.length ? h("ul", { class: "list-plain" }, helpers) : h("p", { class: "muted small" }, "None yet."))));
};

// ---------- Logs ----------

PAGES.logs = async (el, arg) => {
  let source = arg || "sphynx";
  const box = h("div", { class: "logbox" });
  const sel = select([["sphynx", "Sphynx"], ["headscale", "Control server (Headscale)"], ["home", "Sphynx home (tailscaled)"], ["link", "Sphynx Link (tailcat)"]], source, {
    onchange: (e) => { source = e.target.value; history.replaceState(null, "", "#/logs/" + source); load(); },
  });
  async function load() {
    const lines = await api("GET", "/api/logs?lines=600&source=" + source);
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 30;
    box.textContent = lines.length ? lines.join("\n") : "(nothing yet)";
    if (atBottom) box.scrollTop = box.scrollHeight;
  }
  el.replaceChildren(pageHead("Logs", "Recent output, newest at the bottom. Useful when something doesn't connect.", h("div", { style: "min-width:240px" }, sel), h("button", { onclick: () => load() }, icon("refresh"), "Refresh")), card(null, null, box));
  await load();
  box.scrollTop = box.scrollHeight;
  every(4000, load);
};

boot();
