"use strict";

const siwcSelected = new Set();
const siwcExpanded = new Set();
const siwcSavePrefs = () => store(PREF_STORE + ".siwc", { model: $("siwc-model").value });
const SIWC_STATUS = {
  available: ["ok", "SIWC 可用"],
  restricted: ["bad", "订阅共享受限"],
  limited: ["skip", "额度或频率受限"],
  auth_error: ["err", "认证异常"],
  cancelled: ["skip", "测试已停止"],
  failed: ["err", "检测未完成"],
  skipped: ["skip", "已跳过"],
};
const siwcVerdict = (r) => statusPill(...(SIWC_STATUS[r?.status] || SIWC_STATUS.failed));

function showSIWCDetail(r) {
  $("siwc-detail-body").innerHTML = `<p class="result-dialog-meta">${r.model ? `<span class="mono">${esc(r.model)}</span> · ` : ""}${esc(fmtTime(r.time))} · ${r.duration_ms != null ? fmtSec(r.duration_ms) : "—"}${r.http_status ? ` · HTTP ${esc(r.http_status)}` : ""}</p>
    <div class="latest-top">${siwcVerdict(r)}${r.code ? `<span class="meta mono">${esc(r.code)}</span>` : ""}</div>
    <div class="metrics">${metrics(r)}</div>
    ${r.error ? `<p class="detail-warning">${esc(r.error)}</p>` : ""}`;
}

function renderSIWCRow(a) {
  const records = a.siwc || [], last = records.at(-1), p = a.siwc_running;
  const result = p ? collectionOutcome(p, p.total || 1, true) : latestResult(last, siwcVerdict);
  return credentialRow({
    type: "siwc", credential: a, selected: siwcSelected.has(a.id), open: siwcExpanded.has(a.id), selectable: runnable("siwc-", a),
    button: collectionButton("siwc", a, p, "检测"),
    result, tested: !!(last || p),
    meta: `<div class="fp-test-model mono">${esc(p?.model || last?.model || "—")}</div>${listTime(!p && last?.time)}`,
    history: () => [...records].reverse().map((r) => historyEntry("siwc", a.id, r,
      `<div class="outcome"><div class="outcome-copy"><div class="outcome-title">${siwcVerdict(r)}${r.code ? ` <span class="meta mono">${esc(r.code)}</span>` : ""}</div>${r.error ? `<div class="detail-warning">${esc(r.error)}</div>` : ""}</div></div>`,
      r.http_status ? `<span class="meta mono">HTTP ${esc(r.http_status)}</span>` : "")).join(""),
  });
}

function renderSIWC() {
  renderSelection("siwc-", siwcSelected, "检测");
  $("siwc-clear").disabled = pending || !credentials.some((a) => a.siwc?.length);
  renderList("siwc-", renderSIWCRow);
}

function runSIWC(body) {
  if (!$("siwc-toolbar").reportValidity()) return;
  siwcSavePrefs();
  return update("/start", { method: "POST", body: { type: "siwc", ...body, model: $("siwc-model").value } }, "开始检测失败");
}

function initializeSIWC() {
  $("siwc-model").addEventListener("input", () => { siwcSavePrefs(); renderSIWC(); });
  $("siwc-model").addEventListener("change", () => { siwcSavePrefs(); renderSIWC(); });
  bindCollectionActions({ type: "siwc", scope: "siwc", expanded: siwcExpanded, renderRows: renderSIWC, run: runSIWC, showDetail: showSIWCDetail });
}
