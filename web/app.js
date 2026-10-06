"use strict";
/* Recover dashboard. Vanilla JS, no framework. All numbers on screen come from the API
   (/overview, /analyze, /live, /benchmark, /api/fraud/*); nothing is hard-coded. */

// ---------------------------------------------------------------- helpers
const $ = id => document.getElementById(id);
const esc = s => String(s == null ? "" : s).replace(/[&<>"']/g, c => ({"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#39;"}[c]));
const inr = p => "₹" + Math.round(p / 100).toLocaleString("en-IN");
const C = {ink:"#f4f1ea", blue:"#2f6bff", cyan:"#00e5ff", red:"#ff3b3b", lime:"#b6ff00", yel:"#ffd400", mut:"#aaaaaa", grid:"#333333", bg:"#000000", card:"#141414"};
const NS = "http://www.w3.org/2000/svg";
function S(tag, attrs, text){ const e = document.createElementNS(NS, tag); for (const k in (attrs||{})) e.setAttribute(k, attrs[k]); if (text != null) e.textContent = text; return e; }
async function getJSON(url, opt){ const r = await fetch(url, opt); if (!r.ok) throw new Error(url + " -> HTTP " + r.status); return r.json(); }
function chip(text, cls, title){ return '<span class="chip ' + cls + '"' + (title ? ' title="' + esc(title) + '"' : "") + '>' + esc(text) + '</span>'; }
const mono = {"font-family":"JetBrains Mono,ui-monospace,Menlo,monospace"};
function txt(parent, x, y, text, size, fill, anchor, weight){
  const t = S("text", Object.assign({x, y, "font-size": size||11, fill: fill||C.ink, "text-anchor": anchor||"start", "font-weight": weight||700}, mono), text);
  parent.appendChild(t); return t;
}
function clear(svg){ while (svg.firstChild) svg.removeChild(svg.firstChild); }

// ---------------------------------------------------------------- tabs
const TABS = ["overview","plan","live","fraud","benchmark","api"];
let currentTab = "overview";
function showTab(name){
  if (!TABS.includes(name)) name = "overview";
  currentTab = name;
  TABS.forEach(t => $("tab-" + t).classList.toggle("active", t === name));
  document.querySelectorAll(".tab-btn").forEach(b => {
    if (b.dataset.tab === name) b.setAttribute("aria-current", "page"); else b.removeAttribute("aria-current");
  });
  if (location.hash !== "#" + name) history.replaceState(null, "", "#" + name);
  if (name === "overview"){ if (lastOv) { drawSpark(lastOv.series); } }
  if (name === "fraud") loadFraudModel();
  if (name === "benchmark") loadGnnTable();
  if (name === "live") { drawLiveStrip(); }
}
document.querySelectorAll(".tab-btn").forEach(b => b.onclick = () => showTab(b.dataset.tab));
window.addEventListener("hashchange", () => showTab(location.hash.slice(1)));

// ---------------------------------------------------------------- graph model status
let GS = {enabled:false, online:false, status:"disabled", quarantine_at:0.8, review_at:0.5, demo_illicit_pct:25};
async function pollGraph(){
  try { GS = await getJSON("/api/graph/status"); } catch(e){ GS = {enabled:false, online:false, status:"disabled", quarantine_at:0.8, review_at:0.5}; }
  const c = $("graphChip");
  if (!GS.enabled){ c.className = "pill dis"; c.textContent = "graph model: off"; c.title = "FRAUD_URL is not set. Recover runs exactly as before."; }
  else if (GS.online){ c.className = "pill on"; c.textContent = "graph model: online"; c.title = "Quarantine at risk >= " + GS.quarantine_at + ", review at >= " + GS.review_at; }
  else { c.className = "pill off"; c.textContent = "graph model: offline"; c.title = "Fail-open: Recover keeps working without the graph signal."; }
  updateFmStatus();
}
function updateFmStatus(){
  const el = $("fmStatus"); if (!el) return;
  if (!GS.enabled) el.innerHTML = '<b>Graph model is off.</b> <code>FRAUD_URL</code> is not set, so only the card-testing signal runs and Recover works exactly as before. Start the fraud service (<code>make fraud-serve</code>) and restart with <code>FRAUD_URL=http://localhost:8100</code>.';
  else if (!GS.online) el.innerHTML = '<b>Graph model is offline.</b> The service at <code>FRAUD_URL</code> does not answer. Recover fails open: it keeps working and applies only the card-testing signal.';
  else el.innerHTML = '<b>Graph model is online.</b> Quarantine at risk &ge; <b>' + GS.quarantine_at + '</b>; flag for review at &ge; <b>' + GS.review_at + '</b> (flagged payments stay recoverable). Fail-open if the service goes down. Demo links point at illicit nodes <b>' + GS.demo_illicit_pct + '%</b> of the time so the signal is visible (real prevalence is lower: see the report card).';
}
setInterval(pollGraph, 5000); pollGraph();

// ---------------------------------------------------------------- overview: KPIs, outcome bar, sparkline
let lastOv = null, prevOv = null;
function drawSpark(series){
  const sc = $("spark"); if (!sc || !sc.clientWidth) return;
  const dpr = window.devicePixelRatio || 1, W = sc.clientWidth, H = sc.clientHeight || 150;
  sc.width = Math.round(W * dpr); sc.height = Math.round(H * dpr);
  const ctx = sc.getContext("2d"); ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.fillStyle = "#000"; ctx.fillRect(0, 0, W, H);
  ctx.font = "700 12px JetBrains Mono, monospace"; ctx.textBaseline = "alphabetic";
  const padL = 34, padR = 10, padT = 12, padB = 12;
  if (!series || series.length < 2){ ctx.fillStyle = C.mut; ctx.fillText("Waiting for activity. Simulate live traffic, or try the sample.", padL, H / 2); return; }
  const maxV = Math.max(1, ...series.map(p => Math.max(p.failures, p.threats, p.sent, p.graph || 0)));
  ctx.strokeStyle = C.grid; ctx.lineWidth = 1; ctx.fillStyle = C.mut;
  [0, .5, 1].forEach(f => { const y = H - padB - (H - padT - padB) * f; ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(W - padR, y); ctx.stroke(); ctx.fillText(Math.round(maxV * f), 4, y + 4); });
  const line = (key, color) => {
    ctx.strokeStyle = color; ctx.lineWidth = 3; ctx.lineJoin = "miter"; ctx.lineCap = "butt"; ctx.beginPath();
    series.forEach((p, i) => { const x = padL + (W - padL - padR) * (i / (series.length - 1)), y = H - padB - (H - padT - padB) * ((p[key] || 0) / maxV); i ? ctx.lineTo(x, y) : ctx.moveTo(x, y); });
    ctx.stroke();
  };
  line("failures", C.red); line("threats", C.yel); line("sent", C.lime); line("graph", C.cyan);
}
function renderOutcome(o){
  const F = o.failures, Q = o.quarantined, Sd = o.sent, other = Math.max(0, F - Sd - Q);
  const bar = $("outcomeBar"); bar.innerHTML = "";
  if (!F){ $("outcomeNote").textContent = "No failed payments yet."; return; }
  [[Sd, C.lime, "messaged"], [Q, C.red, "quarantined"], [other, C.yel, "not messaged"]].forEach(([n, col, lab]) => {
    if (!n) return;
    const d = document.createElement("div"); d.style.width = (100 * n / (Sd + Q + other)) + "%"; d.style.background = col;
    d.textContent = n > (F * 0.08) ? lab + " " + n : n; d.title = lab + ": " + n; bar.appendChild(d);
  });
  $("outcomeNote").textContent = F + " failed payments: " + Sd + " messaged, " + Q + " quarantined, " + other + " not messaged (hard declines, below the recovery threshold, or still pending).";
}
function renderOverview(o){
  $("oSeen").textContent = o.payments_seen; $("oRecovered").textContent = inr(o.recovered);
  $("oThreats").textContent = o.threats; $("oSent").textContent = o.sent;
  $("oGraph").textContent = (o.graph_review || 0) + (o.graph_quarantined || 0); $("oQuar").textContent = o.quarantined;
  drawSpark(o.series); renderOutcome(o); updatePipeline(o);
  prevOv = lastOv; lastOv = o;
}
async function pollOverview(){ try { renderOverview(await getJSON("/overview")); } catch(e){} }
setInterval(pollOverview, 2000);

// honest demo-traffic simulator (clearly labeled)
let simTimer = null;
function startSim(){ if (simTimer) return; $("simToggle").textContent = "Stop simulation"; $("simBadge").style.display = "inline-block"; simTimer = setInterval(() => { fetch("/simulate", {method:"POST"}).catch(() => {}); }, 1200); }
function stopSim(){ if (!simTimer) return; clearInterval(simTimer); simTimer = null; $("simToggle").textContent = "Simulate live traffic"; $("simBadge").style.display = "none"; }
$("simToggle").onclick = e => { e.preventDefault(); simTimer ? stopSim() : startSim(); };

// ---------------------------------------------------------------- live pipeline diagram
const PN = [ // x positions of the six stages (box width 130, y 40..130)
  {id:"observe", x:20,  label:"OBSERVE",     sub:"payments in",        col:C.cyan},
  {id:"fraud",   x:180, label:"FRAUD CHECK", sub:"burst + graph",      col:C.yel},
  {id:"diagnose",x:340, label:"DIAGNOSE",    sub:"decline code",       col:C.blue},
  {id:"score",   x:500, label:"SCORE",       sub:"P(recover)",         col:C.blue},
  {id:"draft",   x:660, label:"DRAFT",       sub:"LLM or template",    col:C.lime},
  {id:"send",    x:820, label:"SEND",        sub:"email / SMS",        col:C.lime}
];
const PW = 130, PY = 40, PH = 92;
let pipeBuilt = false, packets = [], rafOn = false;
function buildPipeline(){
  const svg = $("pipe"); clear(svg);
  // edges
  for (let i = 0; i < PN.length - 1; i++){
    const x1 = PN[i].x + PW, x2 = PN[i+1].x, y = PY + PH / 2;
    svg.appendChild(S("line", {x1, y1:y, x2, y2:y, stroke:C.ink, "stroke-width":3}));
    svg.appendChild(S("polygon", {points:`${x2},${y} ${x2-9},${y-7} ${x2-9},${y+7}`, fill:C.ink}));
  }
  // quarantine branch
  const fx = PN[1].x + PW / 2;
  svg.appendChild(S("line", {x1:fx, y1:PY+PH, x2:fx, y2:180, stroke:C.red, "stroke-width":3}));
  svg.appendChild(S("polygon", {points:`${fx},180 ${fx-7},170 ${fx+7},170`, fill:C.red}));
  // stage boxes
  PN.forEach(n => {
    svg.appendChild(S("rect", {x:n.x+5, y:PY+5, width:PW, height:PH, fill:n.col}));            // hard shadow
    svg.appendChild(S("rect", {x:n.x, y:PY, width:PW, height:PH, fill:C.card, stroke:C.ink, "stroke-width":3}));
    txt(svg, n.x + 10, PY + 20, n.label, 12, n.col);
    n.cnt = txt(svg, n.x + 10, PY + 56, "0", 26, C.ink);
    n.cnt.setAttribute("id", "pc-" + n.id);
    txt(svg, n.x + 10, PY + 80, n.sub, 10, C.mut, "start", 500);
  });
  // quarantine box
  const q = {x:PN[1].x, y:180, w:PW + 40};
  svg.appendChild(S("rect", {x:q.x+5, y:q.y+5, width:q.w, height:70, fill:C.red}));
  svg.appendChild(S("rect", {x:q.x, y:q.y, width:q.w, height:70, fill:C.card, stroke:C.red, "stroke-width":3}));
  txt(svg, q.x + 10, q.y + 20, "QUARANTINE", 12, C.red);
  txt(svg, q.x + 10, q.y + 48, "0", 26, C.ink).setAttribute("id", "pc-quar");
  txt(svg, q.x + 10, q.y + 64, "never messaged", 10, C.mut, "start", 500).setAttribute("id", "pc-quar-sub");
  // annotations for the two signals
  txt(svg, PN[1].x + PW + 8, PY + PH + 22, "signal 1: card-testing burst", 10, C.yel, "start", 500);
  txt(svg, PN[1].x + PW + 8, PY + PH + 38, "signal 2: graph model risk", 10, C.cyan, "start", 500);
  txt(svg, PN[1].x + PW + 8, PY + PH + 54, "either one quarantines", 10, C.mut, "start", 500);
  svg.appendChild(S("g", {id:"pk"}));
  pipeBuilt = true;
}
const cy = PY + PH / 2;
const ROUTES = {
  seen:  [[PN[0].x + PW/2, cy], [PN[1].x + PW/2, cy]],
  fail:  [[PN[1].x + PW/2, cy], [PN[2].x + PW/2, cy], [PN[3].x + PW/2, cy]],
  quar:  [[PN[1].x + PW/2, cy], [PN[1].x + PW/2, 215]],
  sent:  [[PN[3].x + PW/2, cy], [PN[4].x + PW/2, cy], [PN[5].x + PW/2, cy]]
};
const ROUTE_COL = {seen:C.cyan, fail:C.blue, quar:C.red, sent:C.lime};
function spawn(route, n){
  n = Math.min(n, 8);
  for (let i = 0; i < n; i++) packets.push({route, t0: performance.now() + i * 140, dur: 950 + Math.random() * 200});
  if (packets.length > 60) packets.splice(0, packets.length - 60);
  if (!rafOn){ rafOn = true; requestAnimationFrame(tickPackets); }
}
function tickPackets(now){
  const g = $("pk"); if (!g){ rafOn = false; return; }
  clear(g);
  packets = packets.filter(p => now - p.t0 < p.dur);
  packets.forEach(p => {
    if (now < p.t0) return;
    const f = (now - p.t0) / p.dur, pts = ROUTES[p.route], segs = pts.length - 1, s = Math.min(segs - 1, Math.floor(f * segs)), lf = f * segs - s;
    const x = pts[s][0] + (pts[s+1][0] - pts[s][0]) * lf, y = pts[s][1] + (pts[s+1][1] - pts[s][1]) * lf;
    g.appendChild(S("rect", {x:x-7, y:y-7, width:14, height:14, fill:ROUTE_COL[p.route], stroke:"#000", "stroke-width":2}));
  });
  if (packets.length) requestAnimationFrame(tickPackets); else rafOn = false;
}
function updatePipeline(o){
  if (!pipeBuilt) buildPipeline();
  const set = (id, v) => { const e = $(id); if (e) e.textContent = v; };
  const F = o.failures, Q = o.quarantined, ok = Math.max(0, F - Q);
  set("pc-observe", o.payments_seen); set("pc-fraud", F); set("pc-diagnose", ok); set("pc-score", ok);
  set("pc-draft", o.sent); set("pc-send", o.sent); set("pc-quar", Q);
  const graphQ = o.graph_quarantined || 0;
  set("pc-quar-sub", "burst " + o.threats + " clusters + graph " + graphQ);
  $("pipeNote").textContent = o.payments_seen ? o.payments_seen + " payments seen this session" : "waiting for traffic";
  if (prevOv && currentTab === "overview"){
    const d = (a, b) => Math.max(0, (a || 0) - (b || 0));
    spawn("seen", d(o.payments_seen, prevOv.payments_seen));
    spawn("fail", d(F - Q, prevOv.failures - prevOv.quarantined));
    spawn("quar", d(Q, prevOv.quarantined));
    spawn("sent", d(o.sent, prevOv.sent));
  }
}
buildPipeline();

// ---------------------------------------------------------------- shared: fraud-signal chips
function sigLabel(s, r){
  if (s.kind === "card_testing") return "BURST";
  const risk = r.graph_risk != null ? r.graph_risk.toFixed(2) : "";
  return "GRAPH " + risk + (s.level === "quarantine" ? " QUARANTINE" : " REVIEW");
}
function sigCell(r){
  const sigs = r.fraud_signals || [];
  let h = "";
  sigs.forEach(s => { h += chip(sigLabel(s, r), s.level === "quarantine" ? "red" : "yel", s.detail); });
  if (!sigs.length){
    if (r.graph_status === "ok") h += chip("clear " + (r.graph_risk != null ? r.graph_risk.toFixed(2) : ""), "lime", "graph model risk below the review threshold");
    else if (r.graph_status === "not_linked") h += chip("no graph link", "dim", "no graph_node_id on this payment");
    else if (r.graph_status === "offline") h += chip("graph offline", "dim", "fail-open: only card-testing applied");
    else h += chip("graph off", "dim", "FRAUD_URL not set");
  }
  if (r.graph_link === "demo") h += chip("demo link", "cyan", "linked to a random test-period node for demonstration");
  const first = sigs[0];
  if (first) h += "<small>" + esc(first.detail) + "</small>";
  return h;
}
function execRowFrom(r){
  const q = (r.fraud_signals || []).find(s => s.level === "quarantine");
  return {charge_id:r.charge_id, customer_name:r.customer_name, customer_email:r.customer_email, amount:r.amount, currency:r.currency,
          method:r.method, failure_code:r.failure_code, attack:!!r.attack, quarantined:!!r.quarantined, quarantine_reason:q ? q.detail : ""};
}
function demoRecipient(){ return $("demoRecipient").value.trim(); }

// ---------------------------------------------------------------- recovery plan
const drop = $("drop"), file = $("file");
drop.onclick = () => file.click();
file.onchange = () => { if (file.files[0]) readAndAnalyze(file.files[0]); };
["dragover","dragenter"].forEach(e => drop.addEventListener(e, ev => { ev.preventDefault(); drop.classList.add("over"); }));
["dragleave","drop"].forEach(e => drop.addEventListener(e, ev => { ev.preventDefault(); drop.classList.remove("over"); }));
drop.addEventListener("drop", ev => { if (ev.dataTransfer.files[0]) readAndAnalyze(ev.dataTransfer.files[0]); });
$("sampleLink").onclick = async e => { e.preventDefault(); analyze(await (await fetch("sample_failed_payments.csv")).text()); };
$("attackLink").onclick = async e => { e.preventDefault(); analyze(await (await fetch("sample_with_attack.csv")).text()); };
function readAndAnalyze(f){ const r = new FileReader(); r.onload = () => analyze(r.result); r.readAsText(f); }

let ROWS = [];
async function analyze(csv){
  try {
    const res = await fetch("/analyze", {method:"POST", headers:{"Content-Type":"text/csv"}, body:csv});
    if (!res.ok){ alert("Could not parse that CSV."); return; }
    const a = await res.json(), s = a.summary; ROWS = a.rows;
    $("uploadCard").classList.add("hidden"); $("results").classList.remove("hidden");
    $("kAtRisk").textContent = inr(s.at_risk_amount); $("kCount").textContent = s.count + " failed payments";
    $("kRecover").textContent = inr(s.recoverable_amount); $("kPct").textContent = s.recoverable_pct + "% expected win-back";
    $("kActionable").textContent = ROWS.filter(r => !r.quarantined && r.diagnosis.action !== "review").length;
    const burst = ROWS.filter(r => r.attack).length;
    $("kQuar").textContent = s.quarantined_total != null ? s.quarantined_total : burst;
    $("kQuarSub").textContent = burst + " card-testing + " + (s.graph_quarantined || 0) + " graph";
    // card-testing threats
    const tc = $("threatCard");
    if (a.threats && a.threats.length){
      tc.classList.remove("hidden");
      $("threatList").innerHTML = a.threats.map(t =>
        '<div class="gw"><div style="flex:1"><b>Card-testing attack</b> ' + chip(t.risk + " risk", "red") +
        '<div class="muted" style="margin-top:3px">' + t.count + ' failed auths ' + esc(t.window_start) + '–' + esc(t.window_end) + ' across ' + t.distinct_customers +
        ' cards (' + inr(t.total_amount) + ' total). ' + esc(t.explanation) + '</div></div></div>').join("");
    } else tc.classList.add("hidden");
    renderGraphSummary(s);
    drawBuckets(s.by_bucket);
    renderRows();
  } catch(e){ alert("Something went wrong analyzing the file."); }
}
function renderGraphSummary(s){
  const el = $("graphSummary"), st = s.graph_status;
  let h;
  if (st === "disabled") h = "Graph model is <b>off</b> (<code>FRAUD_URL</code> not set). Only the card-testing signal ran, and Recover behaves exactly as before.";
  else if (st === "offline") h = "Graph model is <b>offline</b>. Fail-open: only the card-testing signal ran, and nothing was held back because of the graph.";
  else if (st === "not_linked") h = "No row carries a <code>graph_node_id</code>, so no graph signal was applied. Add that column to link payments to graph nodes.";
  else h = "Rows scored by the graph model: <b>" + ROWS.filter(r => r.graph_status === "ok").length + "</b>. Quarantined by the graph: <b>" + (s.graph_quarantined || 0) +
    "</b>. Flagged for review (still recoverable): <b>" + (s.graph_review || 0) + "</b>. Links are explicit <code>graph_node_id</code> values; for the sample files they are a seeded demo link, not real customer-to-graph links.";
  el.innerHTML = h;
}
function drawBuckets(bb){
  const svg = $("bucketChart"); clear(svg);
  const names = {soft_decline:"soft decline", timeout:"timeout", data_error:"data error", hard_decline:"hard decline", unknown:"unknown"};
  const cols = {soft_decline:C.lime, timeout:C.lime, data_error:C.cyan, hard_decline:C.red, unknown:C.mut};
  const keys = Object.keys(bb || {}).sort((a, b) => bb[b] - bb[a]);
  if (!keys.length){ txt(svg, 20, 90, "no data", 12, C.mut); return; }
  const max = Math.max(...keys.map(k => bb[k])), rowH = Math.min(34, 150 / keys.length), left = 120;
  keys.forEach((k, i) => {
    const y = 14 + i * (rowH + 8), w = (700 - left - 60) * bb[k] / max;
    txt(svg, left - 10, y + rowH * 0.65, names[k] || k, 12, C.ink, "end");
    svg.appendChild(S("rect", {x:left, y, width:Math.max(w, 3), height:rowH, fill:cols[k] || C.mut, stroke:C.ink, "stroke-width":2}));
    txt(svg, left + w + 8, y + rowH * 0.65, bb[k], 13, C.ink);
  });
  svg.setAttribute("viewBox", "0 0 700 " + (28 + keys.length * (rowH + 8)));
}
function renderRows(){
  const tb = $("tbody"); tb.innerHTML = "";
  ROWS.forEach((r, i) => {
    const rec = r.quarantined ? "none" : r.diagnosis.recoverable;
    const recCell = r.quarantined ? chip("fraud", "red") : '<span class="tag t-' + esc(rec) + '">' + esc(rec) + '</span>';
    const actCell = r.quarantined ? '<span class="muted">quarantined</span>'
      : (r.diagnosis.action === "review" ? '<span class="muted">review</span>'
      : '<button class="btn-primary rowbtn" onclick="draft(' + i + ',this)">Draft</button>');
    const tr = document.createElement("tr"); if (r.quarantined) tr.className = "q";
    tr.innerHTML =
      '<td><b>' + esc(r.customer_name || "—") + '</b><div class="muted">' + esc(r.customer_email || "") + '</div></td>' +
      '<td class="money">' + inr(r.amount) + '</td>' +
      '<td>' + esc(r.failure_code) + '<div class="muted" style="max-width:230px">' + esc(r.diagnosis.reason) + '</div></td>' +
      '<td>' + recCell + '</td>' +
      '<td><span class="bar-mini"><i style="width:' + Math.round(r.p_recover * 100) + '%"></i></span> ' + Math.round(r.p_recover * 100) + '%</td>' +
      '<td class="money">' + inr(r.expected_recovered) + '</td>' +
      '<td class="sigs">' + sigCell(r) + '</td>' +
      '<td>' + actCell + '</td>';
    const dr = document.createElement("tr");
    dr.innerHTML = '<td colspan="8" style="padding-top:0;border:none"><div class="draftbox" id="draft' + i + '"></div></td>';
    tb.appendChild(tr); tb.appendChild(dr);
  });
}
async function draft(i, btn){
  const r = ROWS[i], box = $("draft" + i);
  btn.disabled = true; btn.textContent = "..."; box.style.display = "block"; box.textContent = "Drafting...";
  try {
    const res = await fetch("/draft", {method:"POST", headers:{"Content-Type":"application/json"},
      body: JSON.stringify({charge_id:r.charge_id, customer_name:r.customer_name, amount:r.amount, currency:r.currency, method:r.method, failure_code:r.failure_code})});
    const d = await res.json();
    const head = "DRAFTED MESSAGE " + (d.mode === "ai" ? "· AI-written" : "· template");
    const noteHtml = (d.note && d.mode !== "ai") ? '<div style="font-size:11px;color:var(--yel);margin-top:6px">Note: ' + esc(d.note) + '</div>' : "";
    box.innerHTML = '<div style="font-size:11px;color:var(--cyan);margin-bottom:4px;font-family:var(--mono)">' + head + '</div>' + esc(d.message || "—") + noteHtml;
  } catch(e){ box.textContent = "Draft failed."; }
  btn.disabled = false; btn.textContent = "Draft";
}
function reset(){ $("results").classList.add("hidden"); $("uploadCard").classList.remove("hidden"); $("file").value = ""; $("logCard").classList.add("hidden"); }
function toRows(arr){ return arr.map(execRowFrom); }
function actionRow(a){
  const m = {sent:"t-high", simulated:"t-medium", scheduled:"t-high", skipped:"t-none", failed:"t-low"};
  const ch = a.status === "skipped" ? "SKIP" : (a.channel || "").toUpperCase();
  const modeTag = a.mode === "ai" ? ' <span class="tag t-high">AI</span>' : a.mode === "template" ? ' <span class="tag t-none">template</span>' : "";
  return '<div class="gw"><span class="t">' + esc(ch) + '</span><div style="flex:1"><b>' + esc(a.customer || a.charge_id) +
    '</b> <span class="tag ' + (m[a.status] || "t-none") + '">' + esc(a.status) + '</span>' + modeTag +
    '<div class="muted" style="margin-top:2px">' + esc(a.detail || "") + '</div>' +
    (a.message ? '<div class="muted" style="margin-top:4px;font-style:italic">“' + esc(a.message) + '”</div>' : "") + '</div></div>';
}
async function execAndRender(rows, container){
  container.innerHTML = '<div class="hint">Agent working through the plan...</div>';
  try {
    const res = await fetch("/execute", {method:"POST", headers:{"Content-Type":"application/json"}, body: JSON.stringify({rows:rows, demo_recipient:demoRecipient()})});
    if (res.status === 429){ container.innerHTML = '<div class="hint">Rate limit. Wait a moment and try again.</div>'; return; }
    const d = await res.json(), s = d.summary;
    let html = '<div class="hint" style="margin-bottom:8px"><b>' + s.sent + '</b> sent · <b>' + s.simulated + '</b> simulated · <b>' + s.skipped + '</b> skipped · <b>' + inr(s.revenue_in_flight) + '</b> revenue in flight</div>';
    d.actions.forEach(a => html += actionRow(a));
    container.innerHTML = html;
    return d;
  } catch(e){ container.innerHTML = '<div class="hint">Run failed.</div>'; }
}
async function runPlan(){
  const btn = $("runBtn"); btn.disabled = true; btn.textContent = "Running...";
  $("logCard").classList.remove("hidden"); $("logSummary").textContent = "";
  await execAndRender(toRows(ROWS), $("log"));
  btn.disabled = false; btn.textContent = "Approve & run recovery";
}

// ---------------------------------------------------------------- live feed (+ the previously missing runLive)
let LIVE = [];
async function pollLive(){
  try {
    const d = await getJSON("/live"); LIVE = d.items || [];
    const list = $("liveList");
    if (!LIVE.length){
      $("liveHint").innerHTML = "Listening for Stripe <code>charge.failed</code> events... (connect the webhook: see README, or simulate traffic on the Overview tab)";
      list.innerHTML = ""; $("liveCount").textContent = ""; const b = $("liveRecover"); if (b) b.remove(); drawLiveStrip(); return;
    }
    $("liveHint").innerHTML = "<b>" + LIVE.length + "</b> live failure(s) received.";
    $("liveCount").textContent = LIVE.filter(i => i.quarantined).length + " quarantined";
    list.innerHTML = "";
    LIVE.slice(0, 8).forEach(it => {
      const row = document.createElement("div"); row.className = "gw";
      row.innerHTML = '<span class="t">' + esc(it.received) + '</span>' +
        '<span style="flex:1"><b>' + esc(it.customer_name || it.charge_id) + '</b> ' + inr(it.amount) + ' <span class="muted">· ' + esc(it.failure_code) + '</span>' +
        '<div class="sigs" style="margin-top:4px">' + sigCell(it) + '</div></span>' +
        '<span class="tag t-' + esc(it.diagnosis.recoverable) + '">' + Math.round(it.p_recover * 100) + '%</span>';
      list.appendChild(row);
    });
    if (!$("liveRecover")){
      const b = document.createElement("button"); b.id = "liveRecover"; b.className = "btn-primary"; b.style.marginTop = "10px";
      b.textContent = "Recover these live failures"; b.onclick = runLive; list.after(b);
    }
    drawLiveStrip();
  } catch(e){}
}
async function runLive(){ // was referenced but never defined on main: the button never appeared
  const b = $("liveRecover"); if (b){ b.disabled = true; b.textContent = "Running..."; }
  await execAndRender(LIVE.map(execRowFrom), $("liveActions"));
  if (b){ b.disabled = false; b.textContent = "Recover these live failures"; }
}
setInterval(pollLive, 2500); pollLive();

function drawLiveStrip(){
  const svg = $("liveStrip"); if (!svg) return; clear(svg);
  const W = 700, H = 200, L = 40, R = 12, T = 12, B = 24;
  const y = v => H - B - (H - T - B) * v;
  [0, .25, .5, .75, 1].forEach(v => { svg.appendChild(S("line", {x1:L, y1:y(v), x2:W-R, y2:y(v), stroke:C.grid, "stroke-width":1})); txt(svg, L - 6, y(v) + 4, v.toFixed(2), 10, C.mut, "end", 500); });
  [[GS.review_at, C.yel, "review"], [GS.quarantine_at, C.red, "quarantine"]].forEach(([v, col, lab]) => {
    svg.appendChild(S("line", {x1:L, y1:y(v), x2:W-R, y2:y(v), stroke:col, "stroke-width":2, "stroke-dasharray":"6 4"}));
    txt(svg, W - R - 2, y(v) - 4, lab + " " + v, 10, col, "end");
  });
  const pts = LIVE.filter(i => i.graph_risk != null).slice(0, 50).reverse();
  if (!pts.length){ txt(svg, W / 2, H / 2, "no graph-scored live events yet", 12, C.mut, "middle"); return; }
  const step = (W - L - R) / Math.max(pts.length, 12);
  pts.forEach((p, i) => {
    const v = p.graph_risk, col = v >= GS.quarantine_at ? C.red : v >= GS.review_at ? C.yel : C.lime;
    const x = L + 8 + i * step;
    const r = S("rect", {x:x-5, y:y(v)-5, width:10, height:10, fill:col, stroke:C.ink, "stroke-width":2});
    r.appendChild(S("title", {}, (p.customer_name || p.charge_id) + " | graph risk " + v.toFixed(3) + (p.graph_link === "demo" ? " | demo link" : "")));
    svg.appendChild(r);
  });
}

// ---------------------------------------------------------------- payment router sample runner (SmartRoute)
const NAMES = ["Aarav","Diya","Kabir","Meera","Rohan","Ananya","Vivaan","Isha","Arjun","Saanvi","Tara","Dev"];
const rnd = n => Math.floor(Math.random() * n);
async function refreshGateways(){
  try {
    const st = await getJSON("/api/route/stats"), box = $("gw"); box.innerHTML = "";
    st.psps.forEach(p => { box.insertAdjacentHTML("beforeend", chip(p.name + " | breaker " + p.breaker + " | success EWMA " + p.ewma_success, p.breaker === "open" ? "red" : p.breaker === "half_open" ? "yel" : "lime")); });
  } catch(e){}
}
async function chaos(down){
  try {
    const r = await fetch("/api/route/chaos?psp=all&down=" + down, {method:"POST"}); if (!r.ok) throw new Error("HTTP " + r.status);
    $("chaosState").textContent = down ? "All gateways are OFFLINE. Payments will fail and go to the Recover agent." : "Gateways restored. The router's breakers close again after a successful payment.";
    refreshGateways();
  } catch(e){ $("chaosState").textContent = "Could not reach the mock gateways (" + e.message + "). Is `make psps` running?"; }
}
function tcell(tr, text, cls){ const td = document.createElement("td"); td.textContent = text; if (cls) td.className = cls; tr.appendChild(td); return td; }
async function runSamples(){
  const msg = $("chaosState");
  if (!GS.enabled){ msg.textContent = "FRAUD_URL is not set, so the router has no fraud check to run. Start with FRAUD_URL=http://localhost:8100."; return; }
  if (!GS.online){ msg.textContent = "The graph model is offline, and the router sample needs it to pick test nodes. Start it with `make fraud-serve` (Recover itself keeps working without it)."; return; }
  const btn = $("bRun"); btn.disabled = true; msg.textContent = "";
  const tb = document.querySelector("#simTable tbody"); tb.replaceChildren(); $("simTable").classList.remove("hidden");
  const t = {ROUTED:0, REVIEW:0, BLOCKED:0, paid:0, failed:0, email:0, quarantined:0, other:0};
  const kinds = [0,1,2,3,4,5,6,7,8,9].map(i => i < 3 ? "illicit" : "licit").sort(() => Math.random() - .5);
  try {
    for (const kind of kinds){
      const s = await getJSON("/api/fraud/sample?kind=" + kind), name = NAMES[rnd(NAMES.length)];
      const body = {amount:(rnd(491) + 10) * 1000, fraud_node_id:s.node_id, customer_name:name, customer_email:name.toLowerCase() + "@example.com", currency:"INR"};
      const to = demoRecipient(); if (to) body.demo_recipient = to;
      const j = await (await fetch("/api/route", {method:"POST", headers:{"Content-Type":"application/json"}, body:JSON.stringify(body)})).json();
      const tr = document.createElement("tr");
      tcell(tr, s.node_id + " (" + kind + ")"); tcell(tr, "INR " + (body.amount / 100));
      tcell(tr, j.fraud_risk != null ? j.fraud_risk.toFixed(3) : "n/a");
      const d = tcell(tr, j.fraud_decision, "dec " + j.fraud_decision); d.style.fontWeight = "700";
      tcell(tr, j.status === "success" ? "success via " + j.psp : j.status === "blocked" ? "blocked, no gateway tried" : "FAILED after " + j.attempts + " tries (" + (j.tried || []).join(", ") + ")");
      const rc = tcell(tr, "-");
      if (j.recovery){
        const a = j.recovery; rc.textContent = a.type + " / " + a.channel + ": " + a.status;
        const sm = document.createElement("div"); sm.className = "muted"; sm.textContent = a.detail + (a.message ? "  “" + a.message + "”" : ""); rc.appendChild(sm);
        if (a.status === "skipped" && j.status === "blocked") t.quarantined++; else if (a.status === "sent" || a.status === "simulated") t.email++; else t.other++;
      }
      tb.appendChild(tr);
      t[j.fraud_decision]++; if (j.status === "success") t.paid++; else if (j.status !== "blocked") t.failed++;
      $("tally").textContent = "ROUTED " + t.ROUTED + " | REVIEW " + t.REVIEW + " | BLOCKED " + t.BLOCKED + " | paid " + t.paid + " | gateways failed " + t.failed + " | recovery messages " + t.email + " | quarantined " + t.quarantined + (t.other ? " | other " + t.other : "");
    }
  } catch(e){ msg.textContent = "Sample run stopped: " + e.message; }
  btn.disabled = false; refreshGateways();
}
$("bRun").onclick = runSamples; $("bKill").onclick = () => chaos(true); $("bRestore").onclick = () => chaos(false);
refreshGateways();

// ---------------------------------------------------------------- fraud model tab
let fmLoaded = false, METRICS = null;
function showErr(msg){ const e = $("err"); e.textContent = msg; e.classList.remove("hidden"); $("result").classList.add("hidden"); }
function bandOf(risk){ return risk >= GS.quarantine_at ? "QUARANTINE" : risk >= GS.review_at ? "REVIEW" : "CLEAR"; }
const BCOL = {QUARANTINE:C.red, REVIEW:C.yel, CLEAR:C.lime};
async function scoreNode(id){
  $("err").classList.add("hidden");
  if (!GS.enabled) return showErr("FRAUD_URL is not set, so the graph model is off. Start the fraud service and restart with FRAUD_URL=http://localhost:8100.");
  const t0 = performance.now(); let d;
  try { d = await getJSON("/api/fraud/score/" + id + "?hops=2"); }
  catch(e){ return showErr("Could not score node " + id + ": " + e.message + ". The graph model may be offline (Recover fails open)."); }
  const ms = performance.now() - t0, band = bandOf(d.risk);
  $("rNode").textContent = id;
  $("rLabel").textContent = (d.label_if_known || "unknown") + " (" + d.split + (d.in_sample ? ", in-sample" : "") + ")";
  $("risk").textContent = d.risk.toFixed(3);
  $("barFill").style.width = (d.risk * 100) + "%"; $("barFill").style.background = BCOL[band];
  $("tReview").style.left = (GS.review_at * 100) + "%"; $("tBlock").style.left = (GS.quarantine_at * 100) + "%";
  $("tLabels").textContent = "review " + GS.review_at + " | quarantine " + GS.quarantine_at;
  $("splitTag").textContent = d.split.toUpperCase();
  const dc = $("decision"); dc.textContent = band; dc.className = "badge " + band;
  $("rBase").textContent = d.baseline_risk.toFixed(3) + " (logistic regression, no graph)";
  $("rRtt").textContent = ms.toFixed(0) + " ms round trip from this browser";
  const known = d.label_if_known;
  $("rNote").textContent = known === "illicit" && band === "CLEAR" ? "Known illicit, but the model scored it low: a miss. Recall at the quarantine threshold is limited; see the charts below."
    : known === "licit" && band !== "CLEAR" ? "Known licit, but the model flagged it: a false alarm."
    : band === "REVIEW" ? "In the review band: flagged, still recoverable." : "";
  $("result").classList.remove("hidden");
  drawNeighbourhood(d);
}
function drawNeighbourhood(d){
  const g = $("graph"); clear(g);
  const cx = 200, cy2 = 160, pos = {}; pos[d.node_id] = [cx, cy2];
  const h1 = d.top_neighbors.filter(n => n.hop === 1), h2 = d.top_neighbors.filter(n => n.hop === 2);
  h1.forEach((n, i) => { const a = 2 * Math.PI * i / Math.max(h1.length, 1) - Math.PI / 2; n.a = a; pos[n.node_id] = [cx + 95 * Math.cos(a), cy2 + 95 * Math.sin(a)]; });
  const kids = {}; h2.forEach(n => (kids[n.via] = kids[n.via] || []).push(n));
  h2.forEach(n => { const p = h1.find(x => x.node_id === n.via), sib = kids[n.via], k = sib.indexOf(n), a = (p ? p.a : 0) + (k - (sib.length - 1) / 2) * 0.28; pos[n.node_id] = [cx + 148 * Math.cos(a), cy2 + 148 * Math.sin(a)]; });
  d.top_neighbors.forEach(n => { const a = pos[n.via], b = pos[n.node_id]; g.appendChild(S("line", {x1:a[0], y1:a[1], x2:b[0], y2:b[1], stroke:C.ink, "stroke-width":n.hop === 1 ? 3 : 2, "stroke-dasharray":n.hop === 1 ? "" : "5 4"})); });
  const all = [{node_id:d.node_id, risk:d.risk, label:d.label_if_known, hop:0}].concat(d.top_neighbors);
  all.forEach(n => {
    const [x, y] = pos[n.node_id], r = n.hop === 0 ? 22 : n.hop === 1 ? 16 : 12;
    const c = S("circle", {cx:x, cy:y, r, fill:BCOL[bandOf(n.risk)], stroke:C.ink, "stroke-width":3});
    c.appendChild(S("title", {}, "node " + n.node_id + " | risk " + n.risk.toFixed(3) + " | known label: " + (n.label || "unknown")));
    g.appendChild(c);
    const tx = txt(g, x, y + 4, n.risk.toFixed(2), n.hop === 2 ? 9 : 11, "#000", "middle", 700);
    txt(g, x, y + r + 12, n.label === "illicit" ? "ILL" : n.label === "licit" ? "LIC" : "?", 9, C.ink, "middle");
  });
  const lg = $("gLegend"); lg.innerHTML = "";
  [["CLEAR", "below review"], ["REVIEW", "review band"], ["QUARANTINE", "quarantine band"]].forEach(([k, t]) => lg.insertAdjacentHTML("beforeend", '<span><span class="sw" style="background:' + BCOL[k] + '"></span>' + t + '</span>'));
  lg.insertAdjacentHTML("beforeend", "<span>solid = 1 hop, dashed = 2 hops; ILL/LIC = known label, ? = unlabeled</span>");
  $("graphNote").textContent = "Node " + d.node_id + " has " + d.degree + " neighbours; showing its riskiest " + h1.length + " and, for the top 3 of those, up to 2 further hops each.";
}
async function pick(kind){
  try { const s = await getJSON("/api/fraud/sample?kind=" + kind); $("nodeId").value = s.node_id; await scoreNode(s.node_id); }
  catch(e){ showErr("Could not fetch a sample: " + e.message + ". Is the graph model running?"); }
}
$("bIll").onclick = () => pick("illicit"); $("bLic").onclick = () => pick("licit");
$("bGo").onclick = () => { const v = $("nodeId").value.trim(); if (!/^\d+$/.test(v)) return showErr("Enter a node id (a whole number)."); scoreNode(parseInt(v, 10)); };
$("nodeId").addEventListener("keydown", e => { if (e.key === "Enter") $("bGo").click(); });

async function loadMetrics(){
  if (METRICS) return METRICS;
  try { METRICS = await getJSON("/api/fraud/metrics"); } catch(e){ METRICS = null; }
  return METRICS;
}
async function loadFraudModel(){
  if (fmLoaded) return;
  const m = await loadMetrics();
  if (!m){ $("reportMeta").textContent = "Metrics unavailable: the graph model is not reachable."; return; }
  fmLoaded = true;
  fillReport(m); drawMetricChart(m); drawPR(m);
  try { drawHist(await getJSON("/api/fraud/distribution")); } catch(e){}
}
const f3 = x => x.toFixed(3);
const msf = s => f3(s.mean) + " ± " + f3(s.std);
function modelRows(m){
  const M = m.models;
  return [["Logistic regression (no graph)", "165", M.logreg_all165, "logreg_all165"], ["GCN, 2 layers", "165", M.gcn_all165, "gcn_all165"],
          ["Logistic regression (no graph)", "93 local", M.logreg_local93, "logreg_local93"], ["GCN, 2 layers", "93 local", M.gcn_local93, "gcn_local93"]];
}
function metricCells(mod){ return mod.summary ? [msf(mod.summary.roc_auc), msf(mod.summary.pr_auc), msf(mod.summary.f1_illicit)] : [f3(mod.test.roc_auc), f3(mod.test.pr_auc), f3(mod.test.f1_illicit)]; }
function fillTable(tb, m, withFeatures){
  tb.innerHTML = "";
  modelRows(m).forEach(([name, feat, mod, key]) => {
    const tr = document.createElement("tr"); if (key === "gcn_all165") tr.className = "hl";
    const cells = withFeatures ? [name, feat].concat(metricCells(mod)) : [name + " (" + feat + ")"].concat(metricCells(mod));
    cells.forEach(t => { const td = document.createElement("td"); td.textContent = t; tr.appendChild(td); });
    tb.appendChild(tr);
  });
}
function metaText(m){
  const d = m.dataset, c = m.config, lat = m.serving_latency;
  return "Test = time steps 35-49 (" + d.test_labeled + " labeled, " + d.test_illicit + " illicit). Train = steps 1-34 (" + d.train_labeled + " labeled). " + d.nodes + " nodes, " + d.edges_undirected +
    " edges. GCN = mean ± std over " + c.seeds.length + " seeds; logistic regression is deterministic. F1 at threshold " + c.f1_threshold + "." +
    (lat ? " Lookup latency on localhost: p50 " + lat.score_1hop.p50_ms.toFixed(2) + " ms, p95 " + lat.score_1hop.p95_ms.toFixed(2) + " ms (not Lambda)." : "");
}
function fillReport(m){ fillTable(document.querySelector("#report tbody"), m, true); $("reportMeta").textContent = metaText(m); }
async function loadGnnTable(){
  const m = await loadMetrics();
  if (!m){ $("gnnMeta").textContent = "Graph model metrics unavailable: the fraud service is not reachable."; return; }
  fillTable(document.querySelector("#gnnTable tbody"), m, false); $("gnnMeta").textContent = metaText(m);
}
function axes(svg, L, T, W, H, xl, yl, xt, yt){
  svg.appendChild(S("rect", {x:L, y:T, width:W - L, height:H - T, fill:"none", stroke:C.ink, "stroke-width":2}));
  xt.forEach(([v, lab]) => { const x = L + (W - L) * v; svg.appendChild(S("line", {x1:x, y1:T, x2:x, y2:H, stroke:C.grid})); txt(svg, x, H + 16, lab, 11, C.mut, "middle", 500); });
  yt.forEach(([v, lab]) => { const y = H - (H - T) * v; svg.appendChild(S("line", {x1:L, y1:y, x2:W, y2:y, stroke:C.grid})); txt(svg, L - 6, y + 4, lab, 11, C.mut, "end", 500); });
  txt(svg, (L + W) / 2, H + 32, xl, 11, C.ink, "middle");
  const t = txt(svg, 12, (T + H) / 2, yl, 11, C.ink, "middle"); t.setAttribute("transform", "rotate(-90 12 " + (T + H) / 2 + ")");
}
function drawHist(d){
  const svg = $("histChart"); clear(svg);
  const nL = d.licit.reduce((a, b) => a + b, 0), nI = d.illicit.reduce((a, b) => a + b, 0), bins = d.licit.length;
  const L = 52, T = 12, W = 510, H = 218, bw = (W - L) / bins;
  const sh = (c, n) => Math.sqrt(c / n);
  const maxS = Math.max(...d.licit.map(c => sh(c, nL)), ...d.illicit.map(c => sh(c, nI)));
  axes(svg, L, T, W, H, "graph risk (P illicit)", "share of class (sqrt scale)", [0, .25, .5, .75, 1].map(v => [v, v.toFixed(2)]), []);
  for (let i = 0; i < bins; i++){
    [[d.licit[i], nL, C.lime, 0], [d.illicit[i], nI, C.red, 1]].forEach(([c, n, col, k]) => {
      const h = (H - T) * sh(c, n) / maxS, x = L + i * bw + k * (bw / 2 - 1) + 1;
      const r = S("rect", {x, y:H - h, width:bw / 2 - 2, height:Math.max(h, c ? 2 : 0), fill:col, stroke:"#000", "stroke-width":1});
      r.appendChild(S("title", {}, (k ? "illicit" : "licit") + " risk " + (i / bins).toFixed(2) + "-" + ((i + 1) / bins).toFixed(2) + ": " + c + " transactions (" + (100 * c / n).toFixed(1) + "% of class)"));
      svg.appendChild(r);
    });
  }
  [[GS.review_at, C.yel, "review"], [GS.quarantine_at, C.red, "quarantine"]].forEach(([v, col, lab]) => {
    const x = L + (W - L) * v; svg.appendChild(S("line", {x1:x, y1:T, x2:x, y2:H, stroke:col, "stroke-width":3, "stroke-dasharray":"6 4"})); txt(svg, x + 4, T + 12, lab, 10, col);
  });
  txt(svg, L + 4, T + 12, "n: licit " + nL + ", illicit " + nI, 10, C.mut, "start", 500);
}
function drawPR(m){
  const svg = $("prChart"); clear(svg);
  const L = 52, T = 12, W = 510, H = 252;
  axes(svg, L, T, W, H, "recall (share of illicit caught)", "precision", [0, .25, .5, .75, 1].map(v => [v, v.toFixed(2)]), [0, .25, .5, .75, 1].map(v => [v, v.toFixed(2)]));
  const tabs = m.threshold_table_test, gk = Object.keys(tabs).find(k => k.startsWith("gcn")), lk = Object.keys(tabs).find(k => k.startsWith("logreg"));
  [[tabs[lk], C.yel, "logreg"], [tabs[gk], C.cyan, "gcn"]].forEach(([rows, col, name]) => {
    const px = r => L + (W - L) * r.recall_illicit, py = r => H - (H - T) * r.precision_illicit;
    svg.appendChild(S("polyline", {points:rows.map(r => px(r) + "," + py(r)).join(" "), fill:"none", stroke:col, "stroke-width":3}));
    rows.forEach(r => {
      const sq = S("rect", {x:px(r) - 5, y:py(r) - 5, width:10, height:10, fill:col, stroke:"#000", "stroke-width":2});
      sq.appendChild(S("title", {}, name + " threshold " + r.threshold + ": precision " + r.precision_illicit.toFixed(3) + ", recall " + r.recall_illicit.toFixed(3) + ", flagged " + r.flagged));
      svg.appendChild(sq);
      if (name === "gcn" && (Math.abs(r.threshold - GS.review_at) < 1e-9 || Math.abs(r.threshold - GS.quarantine_at) < 1e-9))
        txt(svg, px(r) + 8, py(r) - 8, (r.threshold >= GS.quarantine_at ? "quarantine " : "review ") + r.threshold, 10, C.cyan);
    });
  });
}
function drawMetricChart(m){
  const svg = $("metricChart"); clear(svg);
  const L = 52, T = 12, W = 510, H = 252, M = m.models;
  axes(svg, L, T, W, H, "", "score (0 to 1)", [], [0, .25, .5, .75, 1].map(v => [v, v.toFixed(2)]));
  const groups = [["ROC-AUC", "roc_auc"], ["PR-AUC", "pr_auc"], ["F1 illicit", "f1_illicit"]];
  const bars = [[M.gcn_all165, C.cyan], [M.logreg_all165, C.yel], [M.gcn_local93, C.blue], [M.logreg_local93, "#888888"]];
  const gw = (W - L) / groups.length, bw = (gw - 26) / bars.length;
  groups.forEach(([lab, key], gi) => {
    bars.forEach(([mod, col], bi) => {
      const v = mod.summary ? mod.summary[key].mean : mod.test[key], sd = mod.summary ? mod.summary[key].std : 0;
      const x = L + gi * gw + 13 + bi * bw, h = (H - T) * v;
      const r = S("rect", {x, y:H - h, width:bw - 4, height:h, fill:col, stroke:"#000", "stroke-width":2});
      r.appendChild(S("title", {}, lab + ": " + v.toFixed(3) + (sd ? " ± " + sd.toFixed(3) : ""))); svg.appendChild(r);
      if (sd){ const cx = x + (bw - 4) / 2, y1 = H - (H - T) * (v + sd), y2 = H - (H - T) * (v - sd); svg.appendChild(S("line", {x1:cx, y1, x2:cx, y2, stroke:C.ink, "stroke-width":2})); }
      txt(svg, x + (bw - 4) / 2, H - h - 6, v.toFixed(2), 10, C.ink, "middle");
    });
    txt(svg, L + gi * gw + gw / 2, H + 18, lab, 11, C.ink, "middle");
  });
}

txt($("graph"), 200, 160, "score a node to draw its neighbours", 12, C.mut, "middle");

// ---------------------------------------------------------------- benchmark (existing table unchanged) + chart
async function runBench(){
  const b = $("benchBtn"); b.disabled = true; b.textContent = "Running...";
  try {
    const d = await getJSON("/benchmark"), money = v => "₹" + Math.round(v / 100).toLocaleString("en-IN");
    let h = '<div class="hint" style="margin-bottom:8px">' + d.rows + ' payments · ' + money(d.recoverable_truth) + ' genuinely recoverable</div>';
    h += '<div class="scroll"><table><thead><tr><th>Strategy</th><th>Recovered</th><th>Rate</th><th>Messages</th><th>Fraud contacted</th><th>Precision</th></tr></thead><tbody>';
    d.strategies.forEach(s => {
      const hl = s.name.indexOf("Recover") === 0 ? ' class="hl"' : "";
      h += '<tr' + hl + '><td><b>' + esc(s.name) + '</b></td><td class="money">' + money(s.recovered_amount) + '</td><td>' + s.recovery_rate_pct + '%</td><td>' + s.messages_sent + '</td><td>' + (s.fraud_contacted ? chip(String(s.fraud_contacted), "red") : "0") + '</td><td>' + s.precision_pct + '%</td></tr>';
    });
    h += '</tbody></table></div>';
    const e = d.exceptions;
    h += '<div class="hint" style="margin-top:10px"><b>Exceptions left for review:</b> ' + e.count + ' (' + money(e.amount) + ') — ' + e.fraud_quarantined + ' fraud quarantined, ' + e.hard_declines + ' hard declines, ' + e.below_threshold + ' below threshold.</div>';
    h += '<div class="hint" style="margin-top:8px"><b>Stopping rules:</b><ul style="margin:6px 0 0 18px;padding:0">' + d.stopping_rules.map(r => '<li>' + esc(r) + '</li>').join("") + '</ul></div>';
    $("benchOut").innerHTML = h;
    drawBenchChart(d, money);
  } catch(e){ $("benchOut").innerHTML = '<div class="hint">Benchmark failed.</div>'; }
  b.disabled = false; b.textContent = "Run benchmark";
}
function drawBenchChart(d, money){
  const svg = $("benchChart"); svg.classList.remove("hidden"); clear(svg);
  const max = Math.max(1, ...d.strategies.map(s => s.recovered_amount)), L = 118, bh = 38;
  d.strategies.forEach((s, i) => {
    const y = 16 + i * (bh + 22), w = (520 - L - 12) * s.recovered_amount / max, col = s.name.indexOf("Recover") === 0 ? C.lime : s.name.indexOf("Blast") === 0 ? C.yel : C.mut;
    txt(svg, L - 8, y + 24, s.name.replace(" (smart agent)", ""), 12, C.ink, "end");
    svg.appendChild(S("rect", {x:L, y, width:Math.max(w, 3), height:bh, fill:col, stroke:C.ink, "stroke-width":2}));
    txt(svg, L + 8, y + 17, money(s.recovered_amount), 13, "#000"); txt(svg, L + 8, y + 31, s.messages_sent + " msgs | " + s.fraud_contacted + " fraud contacted", 10, "#000", "start", 500);
  });
  svg.setAttribute("viewBox", "0 0 520 " + (24 + d.strategies.length * (bh + 22)));
}

// ---------------------------------------------------------------- drop-in API: try it
$("tryBtn").onclick = async () => {
  const body = {charge_id:"ch_demo_1", customer_email:"a@b.com", customer_name:"Aarav", amount:49900, currency:"INR", failure_code:"insufficient_funds"};
  const n = $("tryNode").value.trim(); if (/^\d+$/.test(n)) body.graph_node_id = parseInt(n, 10);
  $("tryOut").textContent = "POST /api/recover\n" + JSON.stringify(body, null, 2) + "\n\n...";
  try { const r = await fetch("/api/recover", {method:"POST", body:JSON.stringify(body)}); $("tryOut").textContent = "POST /api/recover\n" + JSON.stringify(body, null, 2) + "\n\nHTTP " + r.status + "\n" + JSON.stringify(await r.json(), null, 2); }
  catch(e){ $("tryOut").textContent = "Request failed: " + e.message; }
};

// ---------------------------------------------------------------- spotlight tour
const TOUR = [
  {tab:"overview", sel:"#pipeCard", title:"Live pipeline", body:"Every payment flows observe, fraud check, diagnose, score, draft, send. The boxes count real events; the squares move as events arrive. Two fraud signals can quarantine a payment before it is ever messaged."},
  {tab:"overview", sel:"#simToggle", title:"See it live", body:"No live traffic yet? Click this to stream realistic demo events through the real pipeline, including the graph check, and watch the diagram and charts move. It is always labeled SIMULATED."},
  {tab:"plan", sel:"#uploadCard", title:"Analyze real payments", body:"Drop a failed-payments CSV, or hit try the sample. Each row is fraud-checked, diagnosed, scored for recovery, and ranked by rupees."},
  {tab:"plan", sel:"#attackLink", title:"Two fraud signals", body:"Click the attack sample. Card-testing bursts are quarantined by the burst detector, and the graph model can independently quarantine a payment. Every row shows which signal fired and why."},
  {tab:"overview", sel:"#demoRecipient", title:"Receive the messages", body:"Put your own email here and every recovery message routes to you, so you can watch a real message land during the demo."},
  {tab:"fraud", sel:"#tab-fraud .note", title:"The fraud model", body:"A graph neural network trained on the real Elliptic Bitcoin transaction graph. It is a fraud signal only; it does not predict recovery. Score a random node and see its neighbourhood."},
  {tab:"benchmark", sel:"#benchCard", title:"Proven, and reproducible", body:"Run the benchmark on a labeled 52-payment dataset: the agent recovers most recoverable revenue with about half the messages and no fraudsters contacted. The graph model's own test metrics sit beside it."},
  {tab:"api", sel:"#dropinCard", title:"Drop-in integration", body:"Integrate in two minutes: point a webhook with AUTO_RECOVER, or call POST /api/recover from your code. Try a live request on this tab."},
  {title:"You're set", body:"Run the simulation, try the sample, and check the attack sample to see both fraud signals. Merchants can reach out for a custom plugin from the Drop-in API tab."}
];
let tIdx = 0;
function renderTourContent(){
  const s = TOUR[tIdx];
  $("tStep").textContent = "STEP " + (tIdx + 1) + " / " + TOUR.length; $("tTitle").textContent = s.title; $("tBody").textContent = s.body;
  $("tDots").innerHTML = TOUR.map((_, i) => '<i class="' + (i === tIdx ? "on" : "") + '"></i>').join("");
  $("tBack").style.visibility = tIdx === 0 ? "hidden" : "visible";
  $("tNext").textContent = tIdx === TOUR.length - 1 ? "Get started" : "Next";
}
function positionTour(){
  const s = TOUR[tIdx], ring = $("tRing"), tip = $("tTip"), el = s.sel ? document.querySelector(s.sel) : null;
  if (!el || !el.offsetParent){ ring.classList.add("hide"); tip.style.left = "50%"; tip.style.top = "50%"; tip.style.transform = "translate(-50%,-50%)"; return; }
  ring.classList.remove("hide"); tip.style.transform = "none";
  const r = el.getBoundingClientRect(), pad = 8;
  ring.style.left = (r.left - pad) + "px"; ring.style.top = (r.top - pad) + "px"; ring.style.width = (r.width + 2 * pad) + "px"; ring.style.height = (r.height + 2 * pad) + "px";
  const tipW = Math.min(340, window.innerWidth - 32), gap = 14;
  let left = Math.min(Math.max(16, r.left), window.innerWidth - tipW - 16), top = r.bottom + gap;
  if (top + tip.offsetHeight > window.innerHeight - 16) top = Math.max(16, r.top - gap - tip.offsetHeight);
  tip.style.left = left + "px"; tip.style.top = top + "px";
}
function showStep(){
  renderTourContent();
  const s = TOUR[tIdx]; if (s.tab) showTab(s.tab);
  if (s.tab === "plan" && $("uploadCard").classList.contains("hidden")) reset(); // the tour points at the upload card
  const el = s.sel ? document.querySelector(s.sel) : null;
  if (el){ el.scrollIntoView({block:"center", behavior:"smooth"}); setTimeout(positionTour, 380); } else positionTour();
}
function tourGo(d){ tIdx += d; if (tIdx >= TOUR.length){ closeTour(); return; } if (tIdx < 0) tIdx = 0; showStep(); }
function openTour(){ startSim(); tIdx = 0; $("tour").classList.add("show"); showStep(); }
function closeTour(){ $("tour").classList.remove("show"); try { localStorage.setItem("recover_tour_seen", "1"); } catch(e){} }
window.addEventListener("resize", () => { if ($("tour").classList.contains("show")) positionTour(); drawSpark(lastOv && lastOv.series); });
window.addEventListener("scroll", () => { if ($("tour").classList.contains("show")) positionTour(); }, true);

// ---------------------------------------------------------------- intro modal
const SLIDES = [
  {icon:"01", title:"What Recover is", body:"Businesses lose 10–15% of revenue to failed payments, and the same failed-payment stream hides card-testing fraud. Recover is an AI middleware on top of Stripe/Razorpay that recovers the real failures and quarantines the fraud, automatically."},
  {icon:"02", title:"About the data", body:"Real failed-payment data is private by law (PCI-DSS, GDPR, DPDP); it only exists inside a merchant's own gateway. So this prototype is validated two ways: against real Stripe test-mode webhook events, and on a labelled synthetic benchmark with known outcomes you can reproduce. In production it runs on the merchant's own live stream."},
  {icon:"03", title:"How it's designed", body:"An agent loop: fraud check with two signals (a card-testing burst detector and a graph neural network), diagnose each failure against real decline codes, score recovery probability with a logistic regression, then draft and send personalized outreach. Model proposes, safety rules dispose, and everything is ranked by expected rupees recovered."},
  {icon:"04", title:"The tech", body:"A dependency-free Go service, plus a small Python fraud service (graph model, precomputed scores, Lambda-ready). AI-written messages via Hugging Face with a template fallback, real email, a signature-verified Stripe webhook. If the graph model is down, Recover keeps working."}
];
let iIdx = 0;
function renderIntro(){
  const s = SLIDES[iIdx];
  $("iIcon").textContent = s.icon; $("iTitle").textContent = s.title; $("iBody").textContent = s.body;
  $("iDots").innerHTML = SLIDES.map((_, i) => '<i class="' + (i === iIdx ? "on" : "") + '"></i>').join("");
  $("iBack").style.visibility = iIdx === 0 ? "hidden" : "visible";
  $("iNext").textContent = iIdx === SLIDES.length - 1 ? "Take a tour" : "Next";
}
function introGo(d){ iIdx += d; if (iIdx >= SLIDES.length){ closeIntro(); openTour(); return; } if (iIdx < 0) iIdx = 0; renderIntro(); }
function openIntro(){ iIdx = 0; renderIntro(); $("intro").classList.add("show"); }
function closeIntro(){ $("intro").classList.remove("show"); try { localStorage.setItem("recover_intro_seen", "1"); } catch(e){} }

// ---------------------------------------------------------------- boot
showTab(location.hash.slice(1) || "overview");
pollOverview();
try { if (!localStorage.getItem("recover_intro_seen")) openIntro(); } catch(e){ openIntro(); }
