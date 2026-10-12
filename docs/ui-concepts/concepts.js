"use strict";
const designs = [
  [
    "guided",
    "01",
    "Guided",
    "A little guidance. A lot less setup.",
    "One decision at a time, with sensible defaults.",
  ],
  [
    "overview",
    "02",
    "Overview",
    "Your projects, with room to work.",
    "A familiar console with a focused creation panel.",
  ],
  [
    "studio",
    "03",
    "Access studio",
    "Decide what gets through.",
    "Readable access rules with a live request preview.",
  ],
  [
    "workbench",
    "04",
    "Workbench",
    "Build. Test. Keep control.",
    "A developer workspace with policy and cluster controls together.",
  ],
  [
    "launcher",
    "05",
    "Launcher",
    "What are we building today?",
    "Start from a project and adjust only what you need.",
  ],
];
const repositories = [
  "demo/catalog-api",
  "demo/web-client",
  "demo/data-pipeline",
  "demo/rust-worker",
];
const inferred = [["go"], ["npm"], ["python"], ["rust"]];
const languages = [
  ["go", "Go", "Modules"],
  ["python", "Python", "PyPI"],
  ["rust", "Rust", "crates.io"],
  ["npm", "npm", "Packages"],
];
const $ = (s) => document.querySelector(s);
const escape = (value) =>
  String(value).replace(
    /[&<>"']/g,
    (c) =>
      ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[
        c
      ],
  );
let design = Math.max(
  0,
  designs.findIndex((d) => d[0] === location.hash.slice(1)),
);
let step = 0,
  editing = null,
  draft,
  projects,
  toastTimer;
function fresh() {
  return {
    repo: repositories[0],
    name: "catalog-api",
    automatic: true,
    tools: ["go"],
    egress: "selected",
    cluster: false,
    nodes: "1",
    rules: [
      {
        host: "api.example.com",
        intent: "read",
        path: "/v1/items",
        match: "children",
      },
    ],
  };
}
function reset() {
  projects = [
    { ...fresh(), id: 1, name: "catalog-api-dev", status: "Running" },
    {
      ...fresh(),
      id: 2,
      repo: repositories[1],
      name: "web-client-preview",
      tools: ["npm"],
      status: "Paused",
      cluster: true,
    },
  ];
  draft = fresh();
  step = 0;
  editing = null;
  if ($("#editor").open) $("#editor").close();
  render();
}
function toast(text) {
  $("#toast").textContent = text;
  $("#toast").classList.add("visible");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => $("#toast").classList.remove("visible"), 4200);
}
function pattern(rule) {
  const base =
    rule.match === "children" ? rule.path.replace(/\/$/, "") : rule.path;
  const literal = base.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return rule.match === "children"
    ? "^" + literal + "(?:/.*)?" + "$"
    : "^" + literal + "$";
}
function methods(intent) {
  return (
    {
      read: ["GET", "HEAD"],
      write: ["GET", "HEAD", "POST", "PUT", "PATCH", "DELETE"],
      submit: ["POST"],
    }[intent] || []
  );
}
function field(title, control, hint = "") {
  control = control.replace(
    /<(input|select)/,
    `<$1 aria-label="${escape(title)}"`,
  );
  return `<label class="field"><span>${title}</span>${control}${hint ? `<small>${hint}</small>` : ""}</label>`;
}
function identity() {
  return `<section class="form-section"><div class="section-heading"><span class="number">01</span><div><h2>Choose your project</h2><p>Use the repository name, or make it your own.</p></div></div>${field("Repository", `<select id="repo">${repositories.map((r) => `<option ${draft.repo === r ? "selected" : ""}>${r}</option>`).join("")}</select>`)}${field("Sandbox name", `<input id="name" maxlength="40" required pattern="[a-z][a-z0-9-]{0,39}" value="${escape(draft.name)}">`)}<div class="inline"><span class="muted" id="name-mode">${draft.automatic ? "Named automatically from repository" : "Custom sandbox name"}</span><button type="button" class="text-button" id="auto-name">Use repository name</button></div></section>`;
}
function tools() {
  return `<section class="form-section"><div class="section-heading"><span class="number">02</span><div><h2>Give your tools access</h2><p>Package downloads, without hand-writing registry rules.</p></div></div><div class="tool-grid">${languages.map(([id, title, subtitle]) => `<label class="tool ${draft.tools.includes(id) ? "selected" : ""}"><input type="checkbox" name="tool" value="${id}" ${draft.tools.includes(id) ? "checked" : ""}><span class="tool-mark">${title.slice(0, 2)}</span><span><strong>${title}</strong><small>${subtitle}</small></span></label>`).join("")}</div><p class="hint">Suggested from the demo repository. You can change every selection. Downloads use GET and HEAD.</p></section>`;
}
function network() {
  return `<section class="form-section"><div class="section-heading"><span class="number">03</span><div><h2>Choose outbound access</h2><p>Private and local networks are always blocked.</p></div></div><div class="choices">${[
    [
      "selected",
      "Selected destinations",
      "Repository, selected registries, and your rules.",
    ],
    [
      "web",
      "Full public web",
      "Any public web destination. No local or private networks.",
    ],
    [
      "none",
      "No outbound access",
      "Work offline. No repository or package downloads.",
    ],
  ]
    .map(
      ([v, t, s]) =>
        `<label class="choice ${draft.egress === v ? "selected" : ""}"><input type="radio" name="egress" value="${v}" ${draft.egress === v ? "checked" : ""}><span><strong>${t}</strong><small>${s}</small></span></label>`,
    )
    .join(
      "",
    )}</div><div class="boundary"><span class="shield">◇</span><span>Always blocked: localhost, private networks, link-local addresses, and cloud metadata.</span><strong>Locked</strong></div>${draft.egress === "selected" ? ruleBuilder() : `<p class="hint">${draft.egress === "web" ? "Destination, method, and path restrictions are disabled for public web traffic. Your selected tool presets remain saved." : "Demo source is already present. A real sandbox would need a separate offline source workflow."}</p>`}</section>`;
}
function ruleBuilder() {
  return `<div class="rule-builder"><details class="advanced-access" ${design === 2 || design === 3 ? "open" : ""}><summary>Additional destinations <span>${draft.rules.length} configured</span></summary><div class="inline"><h3>Additional destinations</h3><button type="button" id="add-rule" class="text-button">+ Add destination</button></div>${draft.rules
    .map(
      (r, i) =>
        `<div class="rule" data-rule="${i}"><div class="rule-fields">${field("Public hostname", `<input data-key="host" value="${escape(r.host)}" placeholder="api.example.com" required>`)}${field(
          "Requests",
          `<select data-key="intent">${[
            ["read", "Read only"],
            ["submit", "Submit only"],
            ["write", "Read and write"],
          ]
            .map(
              ([v, t]) =>
                `<option value="${v}" ${r.intent === v ? "selected" : ""}>${t}</option>`,
            )
            .join("")}</select>`,
        )}${field("Path", `<input data-key="path" value="${escape(r.path)}" required placeholder="/v1/items">`)}${field("Match", `<select data-key="match"><option value="children" ${r.match === "children" ? "selected" : ""}>This folder and below</option><option value="exact" ${r.match === "exact" ? "selected" : ""}>Exactly this path</option></select>`)}</div><div class="inline"><small class="rule-sentence">${escape(r.host)} · ${methods(r.intent).join(", ")} · ${escape(r.path)}</small><button type="button" class="text-button remove-rule" data-index="${i}" aria-label="Remove destination ${i + 1}">Remove</button></div><details><summary>Generated rule details</summary><p class="hint">Preview syntax only. Backend policy translation still needs implementation.</p><code class="regex">${escape(pattern(r))}</code></details></div>`,
    )
    .join(
      "",
    )}<p class="hint">Pick a request type and enter a path. Folder matching includes child paths automatically. Special characters become literals in the generated regex.</p></details></div>`;
}
function cluster() {
  return `<section class="form-section"><div class="section-heading"><span class="number">04</span><div><h2>A cluster for testing</h2><p>A disposable Kubernetes cluster inside this sandbox.</p></div></div><label class="cluster-choice"><span class="cluster-icon">▧</span><span><strong>Create a test cluster</strong><small>Separate from the host cluster. No host credentials.</small></span><input id="cluster" type="checkbox" role="switch" ${draft.cluster ? "checked" : ""}></label>${draft.cluster ? `<div class="cluster-settings">${field("Cluster size", `<select id="nodes"><option value="1" ${draft.nodes === "1" ? "selected" : ""}>1 node · quick tests</option><option value="3" ${draft.nodes === "3" ? "selected" : ""}>3 nodes · distributed tests</option></select>`)}<p class="hint">Demo uses a preloaded image. A real runtime must support nested clusters. Public egress never permits reaching the host cluster.</p></div>` : ""}</section>`;
}
function summary() {
  return `<aside class="summary"><p class="eyebrow">Your sandbox</p><h2 id="summary-name">${escape(draft.name) || "Name your sandbox"}</h2><p>${escape(draft.repo)}</p><dl><div><dt>Tools</dt><dd id="summary-tools">${draft.tools.map((t) => languages.find((l) => l[0] === t)[1]).join(", ") || "None selected"}</dd></div><div><dt>Outbound</dt><dd>${{ selected: "Selected destinations", web: "Full public web", none: "Offline" }[draft.egress]}</dd></div><div><dt>Local network</dt><dd>Always blocked</dd></div><div><dt>Test cluster</dt><dd>${draft.cluster ? draft.nodes + " node" + (draft.nodes === "3" ? "s" : "") : "Off"}</dd></div></dl><div class="summary-note">Everything here is simulated. No repositories, clusters, or access policies will change.</div></aside>`;
}
function actions(last = true) {
  return `<div class="form-actions">${design === 0 && editing === null && step > 0 ? '<button type="button" id="back">Back</button>' : '<span class="hint">Changes stay in this browser tab.</span>'}<button type="submit" class="primary">${last ? (editing !== null ? "Save demo changes" : "Create demo sandbox") : "Continue"} <span aria-hidden="true">→</span></button></div>`;
}
function projectList() {
  return `<section class="projects"><div class="inline"><h2>Project sandboxes</h2><span class="muted">${projects.length} demo sandboxes</span></div>${projects.length ? projects.map((p) => `<article class="project"><div class="project-symbol">${escape(p.name.slice(0, 2).toUpperCase())}</div><div class="project-info"><h3>${escape(p.name)}</h3><p>${escape(p.repo)}</p><div class="project-meta"><span>${p.tools.join(" · ") || "No tools"}</span><span>${p.egress === "web" ? "Public web" : p.egress === "none" ? "Offline" : "Selected access"}</span><span>${p.cluster ? p.nodes + "-node test cluster" : "No test cluster"}</span></div></div><span class="status ${p.status.toLowerCase()}">${p.status}</span><div class="project-actions"><button data-action="edit" data-id="${p.id}">Access & cluster</button><button data-action="pause" data-id="${p.id}">${p.status === "Paused" ? "Resume" : "Pause"}</button></div></article>`).join("") : "<p>Create a demo sandbox to try the controls.</p>"}</section>`;
}
function requestPreview() {
  return `<aside class="request-preview"><p class="eyebrow">Try a request</p><h2>Would it get through?</h2><p>Illustrative HTTPS policy check.</p>${field("Method", `<select id="probe-method"><option>GET</option><option>POST</option><option>DELETE</option><option>HEAD</option></select>`)}${field("HTTPS URL", `<input id="probe-url" value="https://api.example.com/v1/items/42">`)}<button type="button" id="probe" class="primary">Check request</button><p id="probe-result" role="status">Try your rule, or test https://localhost/.</p><small>Models the destination rules above. Repository presets, registry presets, and DNS resolution are outside this preview.</small></aside>`;
}
function render() {
  document.body.dataset.design = designs[design][0];
  $("#concepts").innerHTML = designs
    .map(
      (d, i) =>
        `<button data-design="${i}" ${i === design ? 'aria-current="page"' : ""}><span>${d[1]}</span> ${d[2]}</button>`,
    )
    .join("");
  const heading = `<header class="page-heading"><p class="eyebrow">${design === 3 ? "CONTROL / WORKSPACE" : "Agent project controller"}</p><h1>${designs[design][3]}</h1><p>${designs[design][4]}</p></header>`;
  let body = "";
  if (design === 0)
    body = `${heading}<div class="guided-shell"><nav class="steps" aria-label="Setup steps">${["Project", "Tools & access", "Test cluster", "Review"].map((s, i) => `<button type="button" data-step="${i}" ${step === i ? 'aria-current="step"' : ""}><span>${i + 1}</span>${s}</button>`).join("")}</nav><div class="setup-layout"><form id="create-form" class="setup">${step === 0 ? identity() : step === 1 ? tools() + network() : step === 2 ? cluster() : `<div class="review-intro"><h2>Ready to make room for ${escape(draft.name)}?</h2><p>Review your choices on the right. You can go back to change anything.</p></div>`}${actions(step === 3)}</form>${summary()}</div></div>${projectList()}`;
  if (design === 1)
    body = `<div class="console-shell"><aside class="sidebar"><div class="brand">▦ Sandbox</div><span class="nav-active">Projects</span><span>Access policies</span><span>Test clusters</span><div class="sidebar-bottom">Design 02<br>Local demo</div></aside><main>${heading}<div class="overview-layout"><div>${projectList()}<div class="editorial-note"><h3>Start with the repository.</h3><p>The name and package tools follow your project. Access stays explicit, and local networks stay blocked.</p></div></div><form id="create-form" class="setup"><h2>New project sandbox</h2>${identity()}${tools()}${network()}${cluster()}${actions()}</form></div></main></div>`;
  if (design === 2)
    body = `${heading}<div class="studio-top"><span class="studio-tab">Sandbox setup</span><span class="muted">Public destinations only</span></div><div class="studio-layout"><form id="create-form" class="setup">${identity()}${tools()}${network()}${cluster()}${actions()}</form><div class="studio-aside">${requestPreview()}${summary()}</div></div>${projectList()}`;
  if (design === 3)
    body = `<div class="workbench-header"><span class="brand">[ sandbox ]</span><span>Projects / ${escape(draft.name)}</span><span class="status">LOCAL DEMO</span></div>${heading}<form id="create-form"><div class="workbench-grid"><div class="setup">${identity()}${tools()}</div><div class="setup policy-pane">${network()}</div><div class="setup">${cluster()}${summary()}</div></div>${actions()}</form>${projectList()}`;
  if (design === 4)
    body = `${heading}<form id="create-form" class="launcher-form"><div class="launcher-start">${identity()}</div><div class="launcher-grid"><div class="setup">${tools()}${cluster()}</div><div class="setup">${network()}</div></div><div class="launcher-bottom">${summary()}${actions()}</div></form>${projectList()}`;
  $("#app").innerHTML = `<div class="page">${body}</div>`;
  bind($("#app"));
}
function syncName(root) {
  const value = root.querySelector("#name").value;
  draft.name = value;
  root.querySelector("#name-mode").textContent = draft.automatic
    ? "Named automatically from repository"
    : "Custom sandbox name";
  document
    .querySelectorAll("#summary-name")
    .forEach((e) => (e.textContent = value || "Name your sandbox"));
}
function rerender() {
  if (editing !== null) renderEditor();
  else render();
}
function valid(root) {
  if (
    !root.querySelector("#name") &&
    !/^[a-z][a-z0-9-]{0,39}$/.test(draft.name)
  ) {
    toast(
      "Use a name starting with a letter, then lowercase letters, numbers, or hyphens.",
    );
    return false;
  }
  if (!/^[a-z][a-z0-9-]{0,39}$/.test(draft.name)) {
    toast(
      "Use a name starting with a letter, then lowercase letters, numbers, or hyphens.",
    );
    return false;
  }
  if (projects.some((p) => p.name === draft.name && p.id !== editing)) {
    toast("That sandbox name is already in use. Choose another name.");
    return false;
  }
  if (
    draft.egress === "selected" &&
    draft.rules.some(
      (r) =>
        !publicHost(r.host) || !r.path.startsWith("/") || /[?#]/.test(r.path),
    )
  ) {
    toast(
      "Use a public hostname and a path starting with /. Leave out queries and fragments.",
    );
    return false;
  }
  return true;
}
function publicHost(host) {
  const value = host.toLowerCase();
  return (
    /^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$/.test(value) &&
    !/(^|\.)(localhost|local|internal|test|invalid|arpa)$/.test(value) &&
    !value.endsWith(".localhost") &&
    !value.endsWith(".local") &&
    !value.endsWith(".internal") &&
    value !== "metadata.google.internal"
  );
}
function bind(root) {
  const form = root.querySelector("form");
  root.querySelector("#repo")?.addEventListener("change", (e) => {
    draft.repo = e.target.value;
    if (draft.automatic) draft.name = draft.repo.split("/").pop();
    draft.tools = [...inferred[repositories.indexOf(draft.repo)]];
    rerender();
  });
  root.querySelector("#name")?.addEventListener("input", () => {
    draft.automatic = false;
    syncName(root);
  });
  root.querySelector("#auto-name")?.addEventListener("click", () => {
    draft.automatic = true;
    draft.name = draft.repo.split("/").pop();
    rerender();
  });
  root.querySelectorAll('[name="tool"]').forEach((e) =>
    e.addEventListener("change", () => {
      draft.tools = [...root.querySelectorAll('[name="tool"]:checked')].map(
        (e) => e.value,
      );
      e.closest(".tool").classList.toggle("selected", e.checked);
      document
        .querySelectorAll("#summary-tools")
        .forEach(
          (e) => (e.textContent = draft.tools.join(", ") || "None selected"),
        );
    }),
  );
  root.querySelectorAll('[name="egress"]').forEach((e) =>
    e.addEventListener("change", () => {
      draft.egress = e.value;
      rerender();
    }),
  );
  root.querySelector("#cluster")?.addEventListener("change", (e) => {
    draft.cluster = e.target.checked;
    rerender();
  });
  root.querySelector("#nodes")?.addEventListener("change", (e) => {
    draft.nodes = e.target.value;
    rerender();
  });
  root.querySelector("#add-rule")?.addEventListener("click", () => {
    draft.rules.push({
      host: "",
      intent: "read",
      path: "/",
      match: "children",
    });
    rerender();
    root = editing !== null ? $("#editor") : $("#app");
    root.querySelector(".rule:last-of-type input")?.focus();
  });
  root.querySelectorAll(".remove-rule").forEach((e) =>
    e.addEventListener("click", () => {
      draft.rules.splice(Number(e.dataset.index), 1);
      rerender();
    }),
  );
  root.querySelectorAll("[data-rule]").forEach((row) =>
    row.querySelectorAll("[data-key]").forEach((e) =>
      e.addEventListener(e.tagName === "SELECT" ? "change" : "input", () => {
        const r = draft.rules[Number(row.dataset.rule)];
        r[e.dataset.key] = e.value;
        row.querySelector('[data-key="path"]').value = r.path;
        row.querySelector(".regex").textContent = pattern(r);
        row.querySelector(".rule-sentence").textContent =
          `${r.host} · ${methods(r.intent).join(", ")} · ${r.path}`;
      }),
    ),
  );
  root.querySelector("#back")?.addEventListener("click", () => {
    step--;
    render();
  });
  root.querySelectorAll("[data-step]").forEach((e) =>
    e.addEventListener("click", () => {
      step = Number(e.dataset.step);
      render();
    }),
  );
  form?.addEventListener("submit", (e) => {
    e.preventDefault();
    if (!valid(root)) return;
    if (design === 0 && editing === null && step < 3) {
      step++;
      render();
      return;
    }
    if (editing !== null) {
      projects = projects.map((p) =>
        p.id === editing
          ? { ...structuredClone(draft), id: p.id, status: p.status }
          : p,
      );
      $("#editor").close();
      editing = null;
    } else
      projects.unshift({
        ...structuredClone(draft),
        id: Date.now(),
        status: "Running",
      });
    toast(`${draft.name}: demo configuration saved. No external changes.`);
    draft = fresh();
    step = 0;
    render();
  });
  root.querySelectorAll("[data-action]").forEach((e) =>
    e.addEventListener("click", () => {
      const p = projects.find((p) => p.id === Number(e.dataset.id));
      if (e.dataset.action === "pause") {
        p.status = p.status === "Paused" ? "Running" : "Paused";
        toast(`${p.name}: ${p.status.toLowerCase()} in demo.`);
        render();
      } else {
        editing = p.id;
        draft = structuredClone(p);
        renderEditor();
        $("#editor").showModal();
      }
    }),
  );
  root.querySelector("#probe")?.addEventListener("click", () => {
    let result = "Blocked. Enter a valid HTTPS URL.";
    try {
      const url = new URL(root.querySelector("#probe-url").value),
        method = root.querySelector("#probe-method").value;
      if (
        url.protocol === "https:" &&
        !url.username &&
        !url.password &&
        (!url.port || url.port === "443")
      ) {
        if (!publicHost(url.hostname))
          result =
            "Blocked. Local, private, or unrecognized destinations never qualify.";
        else if (draft.egress === "web")
          result = "Allowed in demo. Full public web access is selected.";
        else if (
          draft.egress === "selected" &&
          draft.rules.some(
            (r) =>
              r.host.toLowerCase() === url.hostname &&
              methods(r.intent).includes(method) &&
              new RegExp(pattern(r)).test(url.pathname),
          )
        )
          result = "Allowed in demo. Host, method, and path match.";
        else result = "Blocked. No additional destination rule matches.";
      }
    } catch {}
    root.querySelector("#probe-result").textContent = result;
  });
}
function renderEditor() {
  $("#editor").innerHTML =
    `<div class="inline"><div><p class="eyebrow">Edit demo sandbox</p><h2 id="dialog-title">${escape(draft.name)}</h2></div><button type="button" id="close-editor" aria-label="Close editor">×</button></div><p class="hint">Real policy edits may require pausing. This demo only changes browser memory.</p><form>${identity()}${tools()}${network()}${cluster()}${actions()}</form>`;
  $("#close-editor").onclick = () => {
    $("#editor").close();
    editing = null;
    draft = fresh();
    render();
  };
  bind($("#editor"));
}
$("#editor").addEventListener("close", () => {
  if (!$("#editor").open) $("#editor").replaceChildren();
});
$("#editor").addEventListener("cancel", () => {
  editing = null;
  draft = fresh();
  render();
});
$("#concepts").addEventListener("click", (e) => {
  const button = e.target.closest("[data-design]");
  if (!button) return;
  design = Number(button.dataset.design);
  step = 0;
  history.replaceState(null, "", "#" + designs[design][0]);
  render();
});
window.addEventListener("hashchange", () => {
  const next = designs.findIndex((d) => d[0] === location.hash.slice(1));
  if (next >= 0) {
    design = next;
    step = 0;
    render();
  }
});
$("#reset").addEventListener("click", () => {
  reset();
  toast("Demo reset.");
});
reset();
