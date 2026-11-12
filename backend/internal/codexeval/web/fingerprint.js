"use strict";

const FP_CONFIG = /*FINGERPRINT_CONFIG*/{};
const fpSelected = new Set();
const fpExpanded = new Set();
const fpConcurrency = () => boundedInput("fp-concurrency", FP_CONFIG.requests, FP_CONFIG.requests);
const fpSavePrefs = () => store(PREF_STORE + ".fingerprint", { model: $("fp-model").value, concurrency: fpConcurrency() });
const fpPercent = (value) => value == null ? "—" : `${(value * 100).toFixed(1)}%`;

function fingerprintModelID(model) {
  const id = String(model || "").trim().toLowerCase().split("/").pop();
  return Object.hasOwn(FP_CONFIG.aliases, id) ? FP_CONFIG.aliases[id] : id;
}
function fpOutcome(r) {
  const attr = r.attribution;
  const states = { failed: ["bad", "circle-x", "测试失败"], cancelled: ["neutral", "circle-pause", "测试已停止"] };
  if (states[r.status] || !attr) {
    const [tone, symbol, titleHTML] = states[r.status] || ["idle", "fingerprint", "等待测试"];
    return resultOutcome({ tone, symbol, titleHTML });
  }
  if (attr.decision === "not_model") return resultOutcome({ tone: "warn", symbol: "circle-alert", titleHTML: "不像语言模型回答" });
  const matches = fingerprintModelID(r.model) === fingerprintModelID(attr.prediction);
  return resultOutcome({ tone: matches ? "ok" : "warn", symbol: matches ? "circle-check" : "shuffle",
    titleHTML: `<span class="fp-prediction"><span>${esc(attr.prediction_name)}</span><span class="mono">${fpPercent(attr.probability)}</span></span>`,
    detailHTML: `${matches ? "与测试模型一致" : "与测试模型不一致"}${r.status === "partial" ? " · 部分结果" : ""}` });
}
function showFingerprintDetail(r) {
  const attr = r.attribution;
  const samples = r.samples || [];
  const ranking = attr && attr.decision !== "not_model" && ["completed", "partial"].includes(r.status)
    ? `<section class="result-dialog-section"><h3>候选模型排行</h3><div class="fp-detail-scroll"><table class="fp-detail-table"><thead><tr><th>排名</th><th>模型</th><th>得分</th><th>归因概率</th></tr></thead><tbody>${(attr.results || []).map((c, i) => `<tr><td>${i + 1}</td><td class="mono">${esc(c.display_name)}</td><td class="mono">${Number(c.score).toFixed(4)}</td><td class="mono">${fpPercent(c.probability)}</td></tr>`).join("")}</tbody></table></div></section>` : "";
  const part = (label, text) => `<div class="fp-sample-part"><h4>${label}</h4><pre>${esc(text)}</pre></div>`;
  $("fp-detail-body").innerHTML = `<p class="result-dialog-meta"><span class="mono">${esc(r.model)}</span> · ${esc(fmtTime(r.time))} · ${fmtSec(r.duration_ms)} · 并发 ${esc(r.concurrency)}</p>
    ${fpOutcome(r)}${r.error ? `<p class="detail-warning">${esc(r.error)}</p>` : ""}${ranking}
    ${samples.length ? `<section class="result-dialog-section"><h3>挑战记录 · ${samples.reduce((n, s) => n + (s.attempts || 0), 0)} 次请求</h3>${samples.map((s, i) => `<details class="fp-sample"><summary>${icon("chevron-right", "chev")}挑战 ${i + 1}<span>${s.parsed_numbers} / ${s.expected_count} 个数字 · ${s.attempts || 0} 次尝试</span><span class="verdict ${s.accepted ? "ok" : "err"}">${s.accepted ? "有效" : "未计入"}</span></summary>${s.instructions ? part("开发者指令", s.instructions) : ""}${part("提示词", s.prompt)}${s.text ? part("回答", s.text) : ""}${s.error ? `<p class="detail-warning">${esc(s.error)}</p>` : ""}</details>`).join("")}</section>` : ""}`;
}
function renderFingerprintRow(a) {
  const history = a.fingerprints || [], last = history.at(-1), p = a.fingerprint_running;
  return credentialRow({ type: "fp", credential: a, selected: fpSelected.has(a.id), open: fpExpanded.has(a.id), selectable: runnable("fp-", a),
    button: collectionButton("fp", a, p, "测试"), result: p ? collectionOutcome(p, p.total, true) : fpOutcome(last || {}), tested: !!(last || p),
    meta: `<div class="fp-test-model mono">${esc(p?.model || last?.model || "—")}</div>${listTime(!p && last?.time)}`,
    history: () => [...history].reverse().map((r) => historyEntry("fp", a.id, r, fpOutcome(r))).join("") });
}
function renderFingerprints() {
  renderSelection("fp-", fpSelected, "测试");
  $("fp-clear").disabled = pending || !credentials.some((a) => a.fingerprints?.length);
  renderList("fp-", renderFingerprintRow);
}
function runFingerprint(body) {
  if (!$("fp-toolbar").reportValidity()) return;
  fpSavePrefs();
  return update("/start", { method: "POST", body: { type: "fingerprint", ...body, model: $("fp-model").value, concurrency: fpConcurrency() } }, "开始测试失败");
}
function initializeFingerprint() {
  const saved = stored(PREF_STORE + ".fingerprint") || {};
  $("fp-concurrency").max = FP_CONFIG.requests;
  $("fp-concurrency").value = saved.concurrency || FP_CONFIG.requests;
  $("fp-concurrency").value = fpConcurrency();
  for (const id of ["fp-model", "fp-concurrency"]) $(id).addEventListener("input", fpSavePrefs);
  $("fp-model").addEventListener("change", () => { fpSavePrefs(); renderFingerprints(); });
  $("fp-concurrency").addEventListener("change", () => { $("fp-concurrency").value = fpConcurrency(); fpSavePrefs(); });
  bindCollectionActions({ type: "fp", scope: "fingerprint", expanded: fpExpanded, renderRows: renderFingerprints, run: runFingerprint, showDetail: showFingerprintDetail });
}
