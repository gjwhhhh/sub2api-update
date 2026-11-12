"use strict";
const PELICAN_VARIANTS = /*PELICAN_VARIANTS*/[];
const plSelected = new Set();
const plExpanded = new Set();
const plSavePrefs = () => store(PREF_STORE + ".pelican", { model: $("pl-model").value, effort: $("pl-effort").value, variant: $("pl-variant").value });
const plVariant = (id) => PELICAN_VARIANTS.find((v) => v.id === id);
const plVariantMeta = (r) => metric("image", "版本", plVariant(r.variant)?.label || r.variant);
function updatePelicanPrompt() {
  $("pl-prompt").textContent = plVariant($("pl-variant").value).prompt;
}

const plKind = (r) => (r.skipped ? "skip" : r.error ? "err" : "ok");
const PL_VERDICT = { ok: "已生成", err: "未生成", skip: "已跳过" };
const plVerdict = (r) => statusPill(plKind(r), PL_VERDICT[plKind(r)]);

// Lists carry summaries, so each page loads once per record: undefined while loading, an Error when it failed.
const pelicanPages = new Map();
const pelicanKey = (credentialID, recordID) => JSON.stringify([credentialID, recordID]);
function pelicanPage(credentialID, recordID) {
  const key = pelicanKey(credentialID, recordID);
  if (!pelicanPages.has(key)) {
    pelicanPages.set(key, undefined);
    const settle = (page) => {
      if (!pelicanPages.has(key)) return;
      pelicanPages.set(key, page);
      renderPelican();
    };
    api(`${BASE}/records/pelican/${Number(credentialID)}/${encodeURIComponent(recordID)}`)
      .then((r) => settle(r.pelican.html), (err) => err instanceof AuthError ? showLogin(err.message) : settle(err));
  }
  return pelicanPages.get(key);
}

// Model pages run sandboxed: their scripts work, but this page's origin and storage stay out of reach, and
// the CSP they inherit from it blocks external assets.
const pelicanFrame = (html) => `<iframe sandbox="allow-scripts" srcdoc="${esc(html)}" title="鹈鹕画面" loading="lazy" tabindex="-1"></iframe>`;
// Previews show a page as it looks a second after it is first drawn, then hold its animations and stop its
// timers, so a long list costs no CPU; only the dialog plays the page. Frames in a hidden tab can load without
// being drawn, and Chrome restarts CSS animations whenever a frame is drawn again, hence the wait for a frame
// and the hold on every animation start.
const PREVIEW_FREEZE = `<script>addEventListener("load", () => requestAnimationFrame(() => setTimeout(() => {
  const hold = () => document.getAnimations().forEach((animation) => { animation.pause(); animation.currentTime = 1000; });
  hold();
  addEventListener("animationstart", hold);
  document.querySelectorAll("svg").forEach((svg) => svg.pauseAnimations());
  for (let id = setTimeout(() => {}); id > 0; id--) { clearTimeout(id); clearInterval(id); }
  window.requestAnimationFrame = window.setTimeout = window.setInterval = () => 0;
}, 1000)));<\/script>`;
function pelicanPreview(credentialID, r) {
  if (r.error) return `<div class="preview" data-tip="${esc(r.error)}">${icon(STATUS_ICONS[plKind(r)])}</div>`;
  const page = pelicanPage(credentialID, r.id);
  if (page instanceof Error) return `<div class="preview" data-tip="${esc("画面加载失败：" + page.message)}">${icon("circle-alert")}</div>`;
  return `<div class="preview">${page === undefined ? icon("loader-circle", "spin") : pelicanFrame(page + PREVIEW_FREEZE)}
    <button class="preview-open" type="button" data-pl-detail="${esc(r.id)}" data-pl-credential="${esc(credentialID)}" aria-label="${esc(`放大查看 ${fmtTime(r.time)} 的画面`)}"></button></div>`;
}

function pelicanHistory(a, r) {
  return historyCard(r, r.error ? `${plVerdict(r)}<span class="${r.skipped ? "meta" : "detail-warning"}">${esc(r.error)}</span>` : pelicanPreview(a.id, r), plVariantMeta(r));
}

function renderPelicanRow(a) {
  const history = a.pelicans || [], last = history.at(-1);
  const button = a.pelican_running
    ? `<button class="btn ghost" type="button" data-tip="正在等待模型生成画面" disabled>${icon("loader-circle", "spin")}测试中</button>`
    : collectionButton("pl", a, null, "测试");
  return credentialRow({
    type: "pl", credential: a, selected: plSelected.has(a.id), open: plExpanded.has(a.id), selectable: runnable("pl-", a), button,
    result: latestResult(last, (r) => plVerdict(r) + plVariantMeta(r)), tested: !!last, meta: last ? pelicanPreview(a.id, last) : `<span class="none">—</span>`,
    history: () => [...history].reverse().map((r) => pelicanHistory(a, r)).join(""),
  });
}

function renderPelican() {
  renderSelection("pl-", plSelected, "测试");
  $("pl-clear").disabled = pending || !credentials.some((a) => a.pelicans?.length);
  renderList("pl-", renderPelicanRow);
}

function runPelican(body) {
  if (!$("pl-toolbar").reportValidity()) return;
  plSavePrefs();
  return update("/start", { method: "POST", body: { type: "pelican", ...body, model: $("pl-model").value, effort: $("pl-effort").value, variant: $("pl-variant").value } }, "开始测试失败");
}

function showPelicanDetail(r) {
  $("pl-detail-body").innerHTML = `<div class="result-dialog-meta record-meta">${recordMeta(r, plVariantMeta(r))}</div>
    <div class="preview pelican-stage">${pelicanFrame(r.html)}</div>`;
  fitPelicanStage();
}
function fitPelicanStage() {
  const stage = $("pl-detail-body").querySelector(".pelican-stage");
  stage?.style.setProperty("--preview-scale", stage.clientWidth / 800);
}

function initializePelican() {
  fillSelect("pl-effort", DEFAULT_EFFORTS, stored(PREF_STORE + ".pelican")?.effort, DEFAULT_EFFORT);
  fillSelect("pl-variant", PELICAN_VARIANTS.map((v) => [v.id, v.label]), stored(PREF_STORE + ".pelican")?.variant, "original");
  updatePelicanPrompt();
  const changeVariant = () => {
    store(PREF_STORE + ".pelican", { ...stored(PREF_STORE + ".pelican"), variant: $("pl-variant").value });
    updatePelicanPrompt();
  };
  for (const event of ["input", "change"]) $("pl-variant").addEventListener(event, changeVariant);
  for (const id of ["pl-model", "pl-effort"]) $(id).addEventListener("input", plSavePrefs);
  for (const id of ["pl-model", "pl-effort"]) $(id).addEventListener("change", plSavePrefs);
  // Closing the dialog also stops its animation.
  $("pl-detail").addEventListener("close", () => { $("pl-detail-body").innerHTML = ""; });
  addEventListener("resize", fitPelicanStage);
  bindCollectionActions({ type: "pl", scope: "pelican", expanded: plExpanded, renderRows: renderPelican, run: runPelican, showDetail: showPelicanDetail });
}
