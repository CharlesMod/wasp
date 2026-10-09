const $ = (id) => document.getElementById(id);
const BOOT = JSON.parse($("boot").textContent);
const TOKEN = new URLSearchParams(location.search).get("t") || "";
const PAGES = BOOT.pages;                    // the product's, welcome first and done last
const RUN = { id: "run", title: "Installing", lede: "Each step is checked as it lands." };
const ORDER = PAGES.slice(0, -1).concat([RUN], PAGES.slice(-1));
const mods = {};                             // page id -> { render, collect }
let at = 0, seen = 0, running = false, failed = false, answers = BOOT.answers || {};

async function api(path, body) {
  const opt = { headers: { "X-Wasp-Token": TOKEN } };
  if (body !== undefined) { opt.method = "POST"; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  if (!r.ok && r.status !== 400 && r.status !== 409) throw new Error(path + ": " + r.status + " " + (await r.text()));
  return r.json();
}
const esc = (s) => String(s).replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" }[c]));

function mod(id) {
  if (!(id in mods)) {
    const sect = document.querySelector('.page[data-page="' + id + '"]');
    mods[id] = PAGE_SCRIPTS[id] ? PAGE_SCRIPTS[id](sect) : {};
  }
  return mods[id];
}

function show() {
  const p = ORDER[at];
  document.querySelectorAll(".page").forEach((s) => { s.hidden = s.dataset.page !== p.id; });
  $("title").textContent = p.title;
  $("lede").textContent = p.lede || "";
  $("steps").innerHTML = ORDER.map((q, i) =>
    `<li class="${i === at ? "on" : i < at ? "past" : ""}">${esc(q.title)}</li>`).join("");
  const last = at === ORDER.length - 1, isRun = p.id === "run";
  $("back").disabled = at === 0 || isRun || last;
  $("next").textContent = last ? "Close" : failed && isRun ? "Retry" : ORDER[at + 1].id === "run" ? "Install" : "Next";
  $("next").disabled = isRun && !failed;
  $("cancel").textContent = failed && isRun ? "Close" : "Cancel";
  $("cancel").hidden = last;
  $("checkerr").hidden = true;
}

async function enter() {
  const p = ORDER[at];
  show();
  if (p.id === "run") return;
  let r = { data: null, answers };
  try { r = await api("/api/page/" + encodeURIComponent(p.id)); answers = r.answers || answers; }
  catch (e) { $("checkerr").hidden = false; $("checkerr").textContent = String(e.message || e); return; }
  const m = mod(p.id);
  if (m.render) m.render(r.data, answers);
}

async function next() {
  const p = ORDER[at];
  if (at === ORDER.length - 1) return finish();
  if (p.id === "run") { return retry(); }
  const m = mod(p.id);
  const collected = m.collect ? m.collect() : {};
  const r = await api("/api/check", { page: p.id, answers: collected || {} });
  if (!r.ok) { $("checkerr").hidden = false; $("checkerr").textContent = r.error || "That does not look right."; return; }
  Object.assign(answers, collected);
  at += 1;
  if (ORDER[at].id === "run") { show(); await start(); return; }
  await enter();
}

async function start() {
  seen = 0; failed = false;
  $("stages").innerHTML = ""; $("log").textContent = ""; $("failed").hidden = true;
  const r = await api("/api/start", {});
  if (r.error) { fail(r.error); return; }
  follow();
}

async function retry() {
  failed = false; $("failed").hidden = true; show();
  const r = await api("/api/retry", {});
  if (r.error) { fail(r.error); return; }
  follow();
}

// follow long-polls the run: each answer arrives the moment the run has
// something new (or after a keepalive with nothing), and is asked again.
async function follow() {
  running = true;
  for (;;) {
    let v;
    try { v = await api("/api/run?seen=" + seen); }
    catch (e) { running = false; fail("setup stopped answering: " + (e.message || e)); return; }
    apply(v);
    if (v.state !== "running") break;
  }
  running = false;
  end(lastState);
}

let lastState = "running";
function apply(v) {
  lastState = v.state;
  for (const e of v.events) {
    seen = Math.max(seen, e.seq);
    if (e.kind === "stage") {
      const prev = $("stages").querySelector("li.busy");
      if (prev) prev.classList.remove("busy");
      $("stages").insertAdjacentHTML("beforeend", `<li class="busy">${esc(e.text)}</li>`);
      $("barbox").hidden = true;
    } else if (e.kind === "say") {
      const log = $("log");
      log.textContent += e.text + "\n";
      log.scrollTop = log.scrollHeight;
    } else if (e.kind === "progress") {
      $("barbox").hidden = false;
      $("barbox").classList.toggle("indeterminate", !(e.total > 0));
      $("bar").style.width = e.total > 0 ? Math.min(100, (100 * e.done) / e.total) + "%" : "";
    }
  }
  seen = Math.max(seen, v.seq);
}

function end(state) {
  const busy = $("stages").querySelector("li.busy");
  $("barbox").hidden = true;
  if (state === "done") { if (busy) busy.classList.remove("busy"); at = ORDER.length - 1; enter(); return; }
  if (busy) { busy.classList.remove("busy"); busy.classList.add("bad"); }
  api("/api/run?seen=" + seen).then((v) => fail(v.error || "Setup stopped.", v.failed_stage));
}

function fail(why, stage) {
  failed = true;
  $("failed").hidden = false;
  $("failed").textContent = (stage ? stage + ": " : "") + why;
  show();
  $("next").disabled = false;
}

async function finish() {
  try { await api("/api/close", {}); } catch (e) { /* the server may already be gone */ }
  window.close();
  $("lede").textContent = "You can close this window.";
}

$("next").onclick = () => { next().catch((e) => { $("checkerr").hidden = false; $("checkerr").textContent = String(e.message || e); }); };
$("back").onclick = () => { if (at > 0) { at -= 1; enter(); } };
$("cancel").onclick = async () => {
  if (ORDER[at].id === "run" && running) { await api("/api/cancel", {}); return; }
  try { await api("/api/close", {}); } catch (e) { /* gone */ }
  window.close();
  $("lede").textContent = "Setup closed. You can close this window.";
};

(function boot() {
  $("pname").textContent = BOOT.name;
  $("ver").textContent = BOOT.version ? "version " + BOOT.version : "";
  document.title = BOOT.name + " Setup";
  if (BOOT.logo) $("mark").innerHTML = `<img alt="" src="${BOOT.logo}">`;
  enter();
})();
