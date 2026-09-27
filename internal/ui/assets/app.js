// The dashboard, in one file.
//
// Every value shown here comes off a machine -- hostnames, image references,
// unit names, and journal lines, which the node does not sanitise. Nothing is
// ever assigned to innerHTML: elements are built and filled with textContent,
// so a log line containing markup is a log line containing markup.

"use strict";

const REFRESH_MS = 10000;

const state = {
  nodes: [],
  selected: null,
  role: "",
  timer: null,
};

const el = (id) => document.getElementById(id);

// Both views refresh, so both say when they last did. A stale page that does
// not admit it is worse than no page at all when somebody is deciding whether
// a node came back.
const stamp = () => {
  el("refreshed").textContent = "updated " + new Date().toLocaleTimeString();
};

async function api(path, options) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options && options.headers) },
  });

  const body = await response.json().catch(() => ({}));

  if (!response.ok) {
    throw new Error(body.error || `${response.status} ${response.statusText}`);
  }

  return body;
}

// --- formatting ---------------------------------------------------------

// A field the node could not determine is left out rather than shown as a dash
// or a zero, which is the contract cctl status keeps. A blank is honest where
// a placeholder invites a guess.
function set(list, label, value) {
  if (value === undefined || value === null || value === "") return;

  const dt = document.createElement("dt");
  dt.textContent = label;

  const dd = document.createElement("dd");
  dd.textContent = value;

  list.append(dt, dd);
}

function duration(seconds) {
  if (!seconds) return "";

  const units = [["d", 86400], ["h", 3600], ["m", 60], ["s", 1]];
  const counts = [];
  let rest = Math.floor(seconds);

  for (const [suffix, size] of units) {
    counts.push([suffix, Math.floor(rest / size)]);
    rest %= size;
  }

  // The two most significant units, and the second only when it says
  // something: three days and one second is three days, not "3d 1s".
  const first = counts.findIndex(([, count]) => count > 0);
  if (first < 0) return "0s";

  const parts = [counts[first]];
  if (first + 1 < counts.length && counts[first + 1][1] > 0) parts.push(counts[first + 1]);

  return parts.map(([suffix, count]) => `${count}${suffix}`).join(" ");
}

const shortDigest = (digest) => (digest ? digest.slice(0, 19) + "…" : "");

// health decides a card's colour. A node that did not answer is bad; greenboot
// having failed, or an image staged and waiting for a reboot, is worth a
// second look; everything else is fine.
function health(summary) {
  if (summary.error) return "bad";

  const node = summary.node || {};

  if (node.health && node.health.greenboot === "failed") return "bad";
  if (node.bootstrapped && node.kubernetes && !node.kubernetes.active) return "warn";
  if (node.os && node.os.staged) return "warn";

  return "ok";
}

// --- overview -----------------------------------------------------------

async function loadOverview() {
  try {
    const body = await api("/api/overview");
    state.nodes = body.nodes || [];
    renderOverview();
    stamp();
  } catch (error) {
    el("refreshed").textContent = error.message;
  }
}

function renderOverview() {
  const grid = el("nodes");
  grid.replaceChildren();

  for (const summary of state.nodes) {
    grid.append(card(summary));
  }

  renderFacts();
}

// The homepage states the shape of the project in a row of pills before you
// scroll. The same gesture: the shape of the fleet, before you read a card.
function renderFacts() {
  const counted = { ok: 0, warn: 0, bad: 0 };
  for (const summary of state.nodes) counted[health(summary)] += 1;

  const facts = el("facts");
  facts.replaceChildren();

  const plural = (n, word) => `${n} ${word}${n === 1 ? "" : "s"}`;

  facts.append(pill("plain", plural(state.nodes.length, "node")));

  // Only what is true. A fleet with nothing wrong should not carry two pills
  // reading zero, which is the same restraint the node reports fields with.
  if (counted.ok) facts.append(pill("ok", `${counted.ok} healthy`));
  if (counted.warn) facts.append(pill("warn", plural(counted.warn, "warning")));
  if (counted.bad) facts.append(pill("bad", `${counted.bad} unreachable or failed`));
}

function pill(kind, text) {
  const span = document.createElement("span");
  span.className = "fact " + kind;
  span.textContent = text;

  return span;
}

function card(summary) {
  const node = summary.node || {};

  const button = document.createElement("button");
  button.type = "button";
  button.className = "card " + health(summary);
  button.addEventListener("click", () => select(summary.address));

  const name = document.createElement("h4");
  name.textContent = node.hostname || summary.address;
  button.append(name);

  // A node that did not answer has no hostname to head the card with, so the
  // address is already the title. Repeating it underneath says nothing.
  if (node.hostname) {
    const address = document.createElement("div");
    address.className = "addr";
    address.textContent = summary.address;
    button.append(address);
  }

  const list = document.createElement("dl");

  if (summary.error) {
    set(list, "unreachable", summary.error);
    const reason = list.lastElementChild;
    reason.className = "reason";
    reason.title = summary.error;
  } else if (!node.bootstrapped) {
    set(list, "state", "not bootstrapped");
    set(list, "os", node.os && node.os.name);
  } else {
    set(list, "role", node.role);
    set(list, "cluster", node.cluster);
    set(list, "k0s", node.kubernetes && node.kubernetes.version);
    set(list, "service", node.kubernetes && node.kubernetes.service
      ? node.kubernetes.service + (node.kubernetes.active ? " (running)" : " (stopped)")
      : "");
    set(list, "greenboot", node.health && node.health.greenboot);
    set(list, "uptime", duration(node.health && node.health.uptimeSeconds));
    if (node.os && node.os.staged) set(list, "staged", shortDigest(node.os.staged.digest));
  }

  button.append(list);

  return button;
}

// --- detail -------------------------------------------------------------

// Which node is open lives in the URL fragment, so a reload comes back to the
// node rather than to the list, the browser's back button does what it looks
// like it does, and a tab left open on a machine is a link somebody can send.
// The fragment never leaves the browser, which is why the node goes here and
// the token does not.
function select(address) {
  window.location.hash = encodeURIComponent(address);
}

function back() {
  window.location.hash = "";
}

function route() {
  const address = decodeURIComponent(window.location.hash.replace(/^#/, ""));

  if (!address) {
    state.selected = null;
    el("detail").hidden = true;
    el("overview").hidden = false;
    loadOverview();

    return;
  }

  if (address === state.selected) return;

  state.selected = address;
  el("overview").hidden = true;
  el("detail").hidden = false;
  el("detail-title").textContent = address;
  el("action-result").textContent = "";
  el("logs").replaceChildren();
  showDetailError("");
  loadDetail();
}

function showDetailError(message) {
  const box = el("detail-error");
  box.textContent = message;
  box.hidden = !message;
}

async function loadDetail() {
  const address = state.selected;
  if (!address) return;

  const at = "/api/nodes/" + encodeURIComponent(address);

  showDetailError("");

  try {
    const body = await api(at);
    state.role = body.role || "";

    const badge = el("detail-role");
    badge.textContent = state.role ? "as " + state.role : "";
    badge.hidden = !state.role;
    renderFields(body.node || {});
    gateActions();
  } catch (error) {
    showDetailError(error.message);
  }

  try {
    const body = await api(at + "/services");
    renderServices(body.services || []);
    stamp();
  } catch (error) {
    showDetailError(error.message);
  }
}

function renderFields(node) {
  const list = el("detail-fields");
  list.replaceChildren();

  set(list, "hostname", node.hostname);
  set(list, "machine id", node.machineID);
  set(list, "role", node.role);
  set(list, "cluster", node.cluster);
  set(list, "endpoint", node.endpoint);
  if (!node.bootstrapped) set(list, "state", "not bootstrapped");

  const os = node.os || {};
  set(list, "os", os.name);
  set(list, "kernel", os.kernel);

  if (os.booted) {
    set(list, "booted", os.booted.image);
    set(list, "digest", os.booted.digest);
    set(list, "version", os.booted.version);
  }

  // Staged is what the next boot would run. It is named apart from the booted
  // image because the gap between the two is a reboot somebody has not taken.
  if (os.staged) {
    set(list, "staged", os.staged.image);
    set(list, "staged digest", os.staged.digest);
  }

  const kubernetes = node.kubernetes || {};
  set(list, "k0s", kubernetes.version);
  set(list, "service", kubernetes.service
    ? kubernetes.service + (kubernetes.active ? " (running)" : " (stopped)")
    : "");

  const nodeHealth = node.health || {};
  set(list, "greenboot", nodeHealth.greenboot);
  set(list, "uptime", duration(nodeHealth.uptimeSeconds));

  const management = node.management || {};
  set(list, "claimed by", management.claimedBy);
  if (management.unauthenticated) set(list, "claim", "unauthenticated");
  if (management.openEnrolment) set(list, "enrolment", "open to anyone");
}

function renderServices(services) {
  const body = el("services").querySelector("tbody");
  body.replaceChildren();

  const units = el("log-unit");
  const chosen = units.value;
  units.replaceChildren(new Option("every unit", ""));

  for (const service of services) {
    const row = document.createElement("tr");

    const name = document.createElement("td");
    name.append(document.createTextNode(service.name));

    const tag = document.createElement("span");
    tag.className = "state " + (service.active || "");
    tag.textContent = [service.active, service.sub].filter(Boolean).join(" / ");
    name.append(tag);
    row.append(name);

    const action = document.createElement("td");

    // The node decides what may be restarted; corium-bootstrap is readable and
    // deliberately not restartable, because re-running it on a node that has
    // joined a cluster destroys data.
    if (service.restartable !== false) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "btn";
      button.textContent = "Restart";
      button.dataset.needs = "operator";
      button.addEventListener("click", () => restart(service.name));
      action.append(button);
    }

    row.append(action);
    body.append(row);

    units.append(new Option(service.name.replace(/\.service$/, ""), service.name.replace(/\.service$/, "")));
  }

  units.append(new Option("kernel", "kernel"));
  units.value = chosen;

  gateActions();
}

// --- acting -------------------------------------------------------------

const RANK = { "corium:readonly": 0, "corium:operator": 1, "corium:admin": 2 };

// gateActions disables what this certificate cannot do, so a refusal is
// visible before it is a failed request.
function gateActions() {
  const rank = RANK[state.role];
  const allowed = rank === undefined || rank >= RANK["corium:operator"];

  for (const button of document.querySelectorAll("[data-needs=operator], [data-act]")) {
    button.disabled = !allowed;
    button.title = allowed ? "" : "needs corium:operator; this certificate is " + state.role;
  }
}

async function act(label, path, body, confirmation) {
  if (confirmation && !window.confirm(confirmation)) return;

  const result = el("action-result");
  result.textContent = label + "…";

  try {
    const reply = await api("/api/nodes/" + encodeURIComponent(state.selected) + path, {
      method: "POST",
      body: JSON.stringify(body || {}),
    });

    result.textContent = reply.status || reply.service ? (reply.status || "restarted") : "done";
    loadDetail();
  } catch (error) {
    result.textContent = error.message;
  }
}

const restart = (unit) => act("restarting " + unit, "/restart", { unit },
  `Restart ${unit} on ${state.selected}?`);

// --- logs ---------------------------------------------------------------

async function loadLogs() {
  const query = new URLSearchParams();
  if (el("log-unit").value) query.set("unit", el("log-unit").value);
  if (el("log-since").value) query.set("since", el("log-since").value);
  query.set("lines", "500");

  const pane = el("logs");
  pane.textContent = "reading…";

  try {
    const body = await api(
      "/api/nodes/" + encodeURIComponent(state.selected) + "/logs?" + query.toString());
    renderLogs(body.records || []);
  } catch (error) {
    pane.textContent = error.message;
  }
}

// Syslog priorities, as cctl logs prints them.
const PRIORITY = ["emerg", "alert", "crit", "err", "warning", "notice", "info", "debug"];

function renderLogs(records) {
  const pane = el("logs");
  pane.replaceChildren();

  if (records.length === 0) {
    pane.textContent = "nothing in this window";

    return;
  }

  for (const record of records) {
    const line = document.createElement("div");
    const level = PRIORITY[record.priority] || "";

    const time = document.createElement("span");
    time.className = "time";
    time.textContent = new Date(record.time).toLocaleTimeString() + " ";
    line.append(time);

    const severity = document.createElement("span");
    severity.className = level;
    severity.textContent = level.padEnd(8);
    line.append(severity);

    if (record.unit) {
      const unit = document.createElement("span");
      unit.className = "unit";
      unit.textContent = record.unit.replace(/\.service$/, "").padEnd(22).slice(0, 22) + " ";
      line.append(unit);
    }

    line.append(document.createTextNode(record.message || ""));
    pane.append(line);
  }

  pane.scrollTop = pane.scrollHeight;
}

// --- wiring -------------------------------------------------------------

function schedule() {
  clearInterval(state.timer);

  if (!el("auto").checked) return;

  state.timer = setInterval(() => {
    if (state.selected) loadDetail();
    else loadOverview();
  }, REFRESH_MS);
}

el("back").addEventListener("click", back);
el("reload").addEventListener("click", () => (state.selected ? loadDetail() : loadOverview()));
el("auto").addEventListener("change", schedule);
el("log-load").addEventListener("click", loadLogs);

for (const button of document.querySelectorAll("[data-act]")) {
  button.addEventListener("click", () => {
    const what = button.dataset.act;

    if (what === "cordon") act("cordoning", "/cordon", { undo: false });
    if (what === "uncordon") act("uncordoning", "/cordon", { undo: true });
    if (what === "drain") {
      act("draining", "/drain", {},
        `Drain ${state.selected}? Its workloads are evicted. This takes minutes and is not forced.`);
    }
  });
}

window.addEventListener("hashchange", route);

route();
schedule();
