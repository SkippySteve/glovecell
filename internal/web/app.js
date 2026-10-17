'use strict';
const view = document.querySelector('#view'), message = document.querySelector('#message');
const drafts = new Map(), audits = new Map();
const METHODS = ['GET', 'HEAD', 'POST', 'PUT', 'PATCH', 'DELETE', 'OPTIONS'];
const REGISTRIES = [['go', 'Go', 'Modules'], ['python', 'Python', 'PyPI'], ['rust', 'Rust', 'crates.io'], ['npm', 'npm', 'Packages']];
const TABS = [['overview', 'Overview'], ['repositories', 'Repositories'], ['access', 'Access'], ['history', 'History']];
let projects = [], existing = [], repositories = null, session = {}, loaded = false, loading = false, nameAutomatic = true, toastTimer;

async function api(path, method = 'GET', body) {
  const response = await fetch('/api/v1/' + path, {method, credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  if (!response.ok) throw new Error((await response.text()).trim());
  return response.json();
}
// h builds an element; properties are applied after children so select values resolve.
function h(tag, props, ...children) {
  const e = document.createElement(tag);
  for (const child of children.flat(Infinity)) if (child !== null && child !== undefined && child !== false) e.append(child);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') e.className = value;
    else if (key.startsWith('on') || ['value', 'checked', 'disabled', 'required', 'hidden', 'open'].includes(key)) e[key] = value;
    else if (key === 'data') Object.assign(e.dataset, value);
    else e.setAttribute(key, value === true ? '' : value);
  }
  return e;
}
function toast(text, error = false) {
  message.textContent = text; message.classList.toggle('error', error);
  clearTimeout(toastTimer); toastTimer = setTimeout(() => { message.textContent = ''; }, error ? 12000 : 6000);
}
function callout(tone, title, body, ...actions) {
  return h('div', {class: 'callout ' + tone}, h('span', {class: 'callout-icon', 'aria-hidden': 'true'}, tone === 'warn' || tone === 'danger' ? '!' : 'i'),
    h('div', null, title && h('strong', null, title), body), actions.length ? h('div', {class: 'actions'}, actions) : null);
}
function pill(state) { return h('span', {class: 'pill ' + state.tone}, state.label); }
function slugFromRepository(repository) {
  const leaf = (repository || '').split('/').pop() || '';
  return leaf.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-$/, '') || '';
}
const href = (name, tab) => '#/projects/' + encodeURIComponent(name) + (tab && tab !== 'overview' ? '/' + tab : '');
const settled = p => p.status?.observedGeneration === p.metadata.generation;
const pausedForEdits = p => p.spec.desiredState === 'Paused' && p.status?.phase === 'Paused' && settled(p);

function readyCondition(p) { return p.status?.conditions?.find(c => c.type === 'Ready'); }
// Mirrors the controller's Ready condition. Blocked preparation is surfaced, not hidden behind a phase.
function projectState(p) {
  const condition = readyCondition(p), phase = p.status?.phase || 'Requested';
  if (phase === 'Removed') return {label: 'Removed', tone: '', removed: true};
  if (condition && condition.observedGeneration !== undefined && condition.observedGeneration !== p.metadata.generation) return {label: 'Updating', tone: 'accent busy', message: 'Update requested. Waiting for the controller to apply it.'};
  if (!condition) return {label: phase, tone: 'accent busy', message: 'Request saved. Waiting for preparation to begin.'};
  const blocked = condition.status !== 'True' && ['PreparationPending', 'PolicyRejected', 'CleanupPending', 'T3ConnectPending'].includes(condition.reason) && !condition.message.startsWith('Waiting for');
  if (blocked) return {label: 'Needs attention', tone: 'warn', attention: phase === 'Removing' ? 'Removal needs attention' : 'Preparation needs attention', message: condition.message};
  if (phase === 'Ready') return {label: 'Ready', tone: 'ok'};
  if (phase === 'Paused') return {label: 'Paused', tone: ''};
  return {label: phase, tone: 'accent busy', message: condition.message};
}
function existingState(s) {
  if (/fail|error|reject/i.test(s.status || '')) return {label: s.status, tone: 'danger'};
  if (s.status && s.status !== 'Active' && s.status !== 'Unavailable') return {label: s.status, tone: 'accent busy'};
  if (s.mode === 'Running') return {label: 'Running', tone: 'ok'};
  if (s.mode === 'Suspended') return {label: 'Stopped', tone: ''};
  return {label: s.mode || s.status || 'Unknown', tone: ''};
}

function render(fromPoll = false) {
  const route = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean).map(decodeURIComponent);
  // Polling must not destroy keyboard focus or an in-progress edit.
  if (fromPoll && (route[0] === 'new' || view.querySelector('form[data-dirty]') || document.activeElement?.closest('#view form'))) return;
  let content;
  if (route[0] === 'new') content = createView();
  else if (route[0] === 'projects' && route[1]) content = projectView(route[1], TABS.some(([key]) => key === route[2]) ? route[2] : 'overview');
  else content = homeView();
  view.replaceChildren(...content);
}

function homeView() {
  document.title = 'Sandboxes';
  const head = h('div', {class: 'page-head'}, h('div', null, h('h1', null, 'Sandboxes'), h('p', null, 'Isolated workspaces for agents, each with its own storage, repository access and outbound rules.')),
    h('a', {class: 'button primary', href: '#/new'}, h('span', {'aria-hidden': 'true'}, '+'), 'New sandbox'));
  if (!loaded) return [head, h('div', {class: 'list'}, h('p', {class: 'empty'}, 'Loading sandboxes…'))];
  const active = projects.filter(p => p.status?.phase !== 'Removed'), removed = projects.filter(p => p.status?.phase === 'Removed');
  const rows = [
    ...active.map(p => { const state = projectState(p); return {name: p.metadata.name, rank: state.attention ? 0 : 1, node: h('a', {class: 'row', href: href(p.metadata.name), data: {project: p.metadata.name}},
      h('span', null, h('span', {class: 'row-title'}, p.metadata.name), h('span', {class: 'row-sub'}, p.spec.repository)),
      h('span', {class: 'row-meta'}, (p.spec.https || []).length ? (p.spec.https.length + ' outbound rule' + (p.spec.https.length === 1 ? '' : 's')) : ''),
      pill(state), h('span', {class: 'chev', 'aria-hidden': 'true'}, '›'))}; }),
    ...existing.map(s => {
      const waiting = (s.pending || 0) + (s.approvals || 0);
      const meta = h('span', {class: 'row-meta'},
        s.learning === 'Learning' ? h('span', {class: 'pill accent'}, 'Learning' + (s.learningExpires ? ' until ' + new Date(s.learningExpires * 1000).toLocaleTimeString([], {hour: 'numeric', minute: '2-digit'}) : '')) : null,
        waiting ? [h('span', {class: 'count'}, String(waiting)), 'awaiting review'] : null);
      return {name: s.id, rank: waiting ? 0 : 1, node: h('a', {class: 'row', href: '/sandboxes/?workspace=' + encodeURIComponent(s.id), data: {sandbox: s.id}},
        h('span', null, h('span', {class: 'row-title'}, s.id), h('span', {class: 'row-sub'}, s.namespace + ' · ' + s.sandbox)),
        meta, pill(existingState(s)), h('span', {class: 'chev', 'aria-hidden': 'true'}, '›'))};
    }),
  ].sort((a, b) => a.rank - b.rank || a.name.localeCompare(b.name));
  const out = [head];
  if (rows.length) out.push(h('div', {class: 'list', role: 'list', 'aria-label': 'Sandboxes'}, rows.map(r => { r.node.setAttribute('role', 'listitem'); return r.node; })));
  else out.push(h('div', {class: 'card empty'}, h('h2', null, 'No sandboxes yet'), h('p', null, 'Create one from a repository to give an agent an isolated place to work.'), h('a', {class: 'button primary', href: '#/new'}, 'New sandbox')));
  if (removed.length) out.push(h('details', {class: 'collapse'}, h('summary', null, 'Removed (' + removed.length + ')'),
    h('div', {class: 'list'}, removed.map(p => h('a', {class: 'row', href: href(p.metadata.name), data: {project: p.metadata.name}}, h('span', null, h('span', {class: 'row-title'}, p.metadata.name), h('span', {class: 'row-sub'}, p.spec.repository)), h('span'), pill(projectState(p)), h('span', {class: 'chev', 'aria-hidden': 'true'}, '›'))))));
  return out;
}

function ruleEditor(rule = {host: '', methods: ['GET', 'HEAD'], paths: ['/']}, disabled = false, onRemove) {
  const row = h('div', {class: 'rule'},
    h('label', {class: 'field'}, h('span', null, 'Hostname'), h('input', {name: 'host', value: rule.host, required: true, placeholder: 'api.example.com', autocapitalize: 'off', spellcheck: 'false', disabled})),
    h('label', {class: 'field'}, h('span', null, 'Path prefixes, one per line'), h('textarea', {name: 'paths', rows: '1', value: rule.paths.join('\n'), required: true, disabled})),
    disabled ? h('span') : h('button', {type: 'button', class: 'ghost small remove', 'aria-label': 'Remove destination ' + (rule.host || ''), onclick: () => { row.remove(); onRemove?.(); }}, 'Remove'),
    h('div', {class: 'methods field', role: 'group', 'aria-label': 'Methods'}, h('span', {class: 'field-label'}, 'Methods'),
      h('div', {class: 'chips'}, METHODS.map(m => h('label', {class: 'chip compact'}, h('input', {type: 'checkbox', name: 'method', value: m, checked: rule.methods.includes(m), disabled}), m)))));
  return row;
}
function readRules(root) {
  return [...root.querySelectorAll('.rule')].map(row => ({host: row.querySelector('[name=host]').value.trim().toLowerCase(), methods: [...row.querySelectorAll('[name=method]:checked')].map(input => input.value), paths: row.querySelector('[name=paths]').value.split('\n').map(s => s.trim()).filter(Boolean)}));
}
function validRules(root) {
  const missing = readRules(root).find(rule => !rule.methods.length);
  if (missing) { toast((missing.host || 'Each destination') + ': choose at least one method.', true); return false; }
  return true;
}

function createView() {
  document.title = 'New sandbox · Sandboxes';
  const rules = h('div', {class: 'rules'});
  const empty = h('p', {class: 'subtle'}, 'No additional destinations. Requests beyond repository and registry access are denied.');
  const syncEmpty = () => { empty.hidden = rules.children.length > 0; };
  const select = h('select', {name: 'repository', id: 'repositories', required: true, 'aria-label': 'Repository', disabled: !repositories?.length},
    repositories === null ? h('option', {value: ''}, 'Loading repositories…') : repositories.length ? repositories.map(r => h('option', {value: r.full_name}, r.full_name)) : h('option', {value: ''}, 'No repositories available'));
  const name = h('input', {name: 'name', id: 'name', required: true, pattern: '[a-z][a-z0-9-]{0,39}', maxlength: '40', placeholder: 'my-project', autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': 'name-help'});
  const auto = h('button', {type: 'button', class: 'link', id: 'auto-name'}, 'Use repository name');
  const sync = () => {
    if (nameAutomatic && select.value) { const slug = slugFromRepository(select.value); if (/^[a-z][a-z0-9-]{0,39}$/.test(slug)) name.value = slug; }
    auto.hidden = nameAutomatic;
  };
  auto.onclick = () => { nameAutomatic = true; sync(); name.focus(); };
  name.oninput = () => { nameAutomatic = false; sync(); };
  select.onchange = sync;
  const submit = h('button', {type: 'submit', class: 'primary', id: 'open-project', disabled: !repositories?.length}, 'Create sandbox');
  const form = h('form', {id: 'create', class: 'card', novalidate: false},
    h('section', {class: 'form-section'}, h('header', null, h('h2', null, 'Repository'), h('p', null, 'The sandbox checks this repository out and gets project-scoped Git and pull request access.')),
      h('div', {class: 'grid-2'},
        h('label', {class: 'field'}, h('span', null, 'Repository'), select),
        h('div', {class: 'field'}, h('label', {class: 'field-label', for: 'name'}, 'Sandbox name'), name,
          h('small', {id: 'name-help', class: 'inline-hint'}, h('span', null, 'Lowercase letters, numbers and dashes.'), auto)))),
    h('section', {class: 'form-section'}, h('header', null, h('h2', null, 'Package registries'), h('p', null, 'Allow package downloads (GET and HEAD) from reviewed registry hosts.')),
      h('div', {class: 'chips', role: 'group', 'aria-label': 'Package registries'}, REGISTRIES.map(([value, label, detail]) => h('label', {class: 'chip'}, h('input', {type: 'checkbox', name: 'registry', value}), h('strong', null, label), h('small', null, detail))))),
    h('section', {class: 'form-section'}, h('header', null, h('h2', null, 'Outbound access'), h('p', null, 'Add HTTPS destinations your tools need. A path of / allows the whole host.')),
      callout('', null, 'Private networks, localhost and cloud metadata are always blocked. Approved gateways provide scoped access to private services.'),
      rules, empty,
      h('div', null, h('button', {type: 'button', class: 'small', id: 'add-create-rule', onclick: () => { rules.append(ruleEditor(undefined, false, syncEmpty)); syncEmpty(); rules.lastChild.querySelector('input').focus(); }}, '+ Add destination'))),
    h('div', {class: 'card-foot'}, h('span', {class: 'subtle'}, 'Access can be changed later while the sandbox is paused.'),
      h('div', {class: 'actions'}, h('a', {class: 'button ghost', href: '#/'}, 'Cancel'), submit)));
  form.onsubmit = async event => {
    event.preventDefault();
    if (!validRules(rules)) return;
    submit.disabled = true;
    try {
      const registries = [...form.querySelectorAll('input[name=registry]:checked')].map(input => input.value);
      await api('workspaces', 'POST', {name: name.value, repository: select.value, registries, https: readRules(rules)});
      nameAutomatic = true; toast(name.value + ': sandbox requested. Preparation status appears here.');
      await load(); location.hash = href(name.value);
    } catch (error) { toast(error.message, true); submit.disabled = false; }
  };
  form.addEventListener('input', () => { form.dataset.dirty = 'true'; });
  sync(); syncEmpty();
  return [h('nav', {class: 'crumbs', 'aria-label': 'Breadcrumb'}, h('a', {href: '#/'}, 'Sandboxes'), h('span', {'aria-hidden': 'true'}, '/'), h('span', null, 'New')),
    h('div', {class: 'page-head'}, h('div', null, h('h1', null, 'New sandbox'), h('p', null, 'Pick a repository and decide what the sandbox can reach.'))), form];
}

async function setState(p, state, button) {
  if (state === 'Removed' && !confirm('Remove ' + p.metadata.name + '? This revokes access and deletes project storage.')) return;
  button.disabled = true;
  try {
    await api('workspaces/' + encodeURIComponent(p.metadata.name), 'PATCH', {desiredState: state, resourceVersion: p.metadata.resourceVersion});
    toast(p.metadata.name + ': ' + ({Running: 'resume', Paused: 'pause', Removed: 'removal'})[state] + ' requested');
    drafts.delete(p.metadata.name); await load(); render();
  } catch (error) { toast(error.message, true); button.disabled = false; }
}
function lifecycleButton(p, className = '') {
  if (p.spec.desiredState === 'Removed') return null;
  const resume = p.spec.desiredState === 'Paused';
  const button = h('button', {type: 'button', class: className}, resume ? 'Resume' : 'Pause');
  if (resume && (!settled(p) || p.status?.phase !== 'Paused') && p.metadata.uid) { button.disabled = true; button.title = 'Available once the sandbox reports Paused'; }
  button.onclick = () => setState(p, resume ? 'Running' : 'Paused', button);
  return button;
}

function projectView(name, tab) {
  document.title = name + ' · Sandboxes';
  const crumbs = h('nav', {class: 'crumbs', 'aria-label': 'Breadcrumb'}, h('a', {href: '#/'}, 'Sandboxes'), h('span', {'aria-hidden': 'true'}, '/'), h('span', null, name));
  const p = projects.find(item => item.metadata.name === name);
  if (!p) return [crumbs, h('div', {class: 'card empty'}, loaded ? [h('h2', null, 'Sandbox not found'), h('p', null, name + ' does not exist or was deleted.'), h('a', {class: 'button', href: '#/'}, 'Back to sandboxes')] : h('p', null, 'Loading…'))];
  const state = projectState(p), ready = p.status?.phase === 'Ready';
  const actions = h('div', {class: 'actions'},
    ready && session.t3Connect ? h('a', {class: 'button', href: 'https://app.t3.codes', target: '_blank', rel: 'noopener noreferrer'}, 'Open T3 Connect ↗') : null,
    lifecycleButton(p, ready ? 'primary' : ''));
  const out = [crumbs, h('header', {class: 'detail-head', data: {project: p.metadata.name}},
    h('div', null, h('div', {class: 'title-row'}, h('h1', null, p.metadata.name), pill(state)), h('p', {class: 'sub'}, p.spec.repository + (p.status?.namespace ? ' · ' + p.status.namespace : ''))), actions)];
  if (state.attention) out.push(callout('warn', state.attention, [h('p', null, state.message), h('p', {class: 'subtle'}, 'The controller retries automatically. Correct the setting described above; keep this sandbox request.')]));
  else if (state.message) out.push(callout('info', null, state.message));
  const counts = {repositories: 1 + (p.spec.repositories || []).length, access: (p.spec.https || []).length};
  out.push(h('nav', {class: 'tabs', 'aria-label': 'Sandbox sections'}, TABS.map(([key, label]) => h('a', {href: href(name, key), 'aria-current': key === tab ? 'page' : null}, label, counts[key] ? h('span', {class: 'subtle'}, String(counts[key])) : null))));
  out.push(h('div', {class: 'stack', data: {project: p.metadata.name}}, ({overview: overviewTab, repositories: repositoriesTab, access: accessTab, history: historyTab})[tab](p)));
  return out;
}

function overviewTab(p) {
  const ready = p.status?.phase === 'Ready', command = 'agent-project-controller connect ' + p.metadata.name;
  const connect = h('section', {class: 'card'}, h('div', {class: 'card-head'}, h('div', null, h('h2', null, 'Connect'), h('p', null, session.t3Connect ? 'Work in this sandbox from T3 Connect.' : 'Attach a terminal from a machine with controller access.'))),
    h('div', {class: 'card-body stack'}, !ready ? h('p', {class: 'muted'}, 'Connection details appear when the sandbox is ready.')
      : session.t3Connect ? [h('p', {class: 'muted'}, 'Choose this project environment in T3. Sign in with the account authorized for the controller.'), h('div', null, h('a', {class: 'button primary', href: 'https://app.t3.codes', target: '_blank', rel: 'noopener noreferrer'}, 'Open T3 Connect ↗'))]
      : h('div', {class: 'copy-field'}, h('code', null, command), h('button', {type: 'button', class: 'small', onclick: async event => { try { await navigator.clipboard.writeText(command); event.target.textContent = 'Copied'; } catch { toast('Copy failed; select the command instead.', true); } }}, 'Copy'))));
  const facts = h('dl', {class: 'facts'}, [['Repository', p.spec.repository], ['Status', projectState(p).label], ['Requested state', p.spec.desiredState], ['Namespace', p.status?.namespace || '—'], ['Outbound rules', String((p.spec.https || []).length)], ['Extra repositories', String((p.spec.repositories || []).length)]].map(([term, value]) => h('div', null, h('dt', null, term), h('dd', null, value))));
  const out = [connect, facts];
  if (p.spec.desiredState !== 'Removed') {
    const remove = h('button', {type: 'button', class: 'danger'}, 'Remove sandbox');
    remove.onclick = () => setState(p, 'Removed', remove);
    out.push(h('section', {class: 'card'}, h('div', {class: 'card-body danger-zone'}, h('div', null, h('h2', null, 'Remove sandbox'), h('p', {class: 'muted'}, 'Revokes repository access and deletes project storage. Pause instead to keep files and credentials.')), remove)));
  }
  return out;
}

function repositoriesTab(p) {
  const hostLabel = project => project.host === 'github' ? (project.public ? 'Public GitHub' : 'Private GitHub') : 'Forgejo';
  const list = h('section', {class: 'card'}, h('div', {class: 'card-head'}, h('div', null, h('h2', null, 'Repositories in this sandbox'), h('p', null, 'Checked out on the sandbox volume. Protected branches and merges stay with the repository owner.'))),
    h('ul', {class: 'items'}, h('li', null, h('div', {class: 'item-main'}, h('strong', null, p.spec.repository), h('code', {class: 'subtle'}, '/workspace/repo')), h('span', {class: 'pill accent'}, 'Primary')),
      (p.spec.repositories || []).map(project => h('li', null, h('div', {class: 'item-main'}, h('strong', null, project.repository), h('code', {class: 'subtle'}, '/workspace/projects/' + project.name)), h('span', {class: 'pill'}, hostLabel(project))))));
  if (p.spec.desiredState === 'Removed') return [list];
  if (!pausedForEdits(p)) return [list, callout('', 'Add another repository', 'Pause this sandbox to enroll another repository on its existing volume.', p.spec.desiredState === 'Running' ? lifecycleButton(p, 'small') : null)];
  const name = h('input', {id: 'enroll-name', required: true, pattern: '[a-z][a-z0-9-]{0,39}', maxlength: '40', placeholder: 'docs', autocapitalize: 'off', spellcheck: 'false', 'aria-describedby': 'enroll-name-help'});
  const host = h('select', {'aria-label': 'Repository host'}, h('option', {value: 'forgejo'}, 'My Forgejo repository'), h('option', {value: 'github'}, 'Public GitHub repository'), session.githubOwner ? h('option', {value: 'github-private'}, 'Private GitHub repository for ' + session.githubOwner) : null);
  const repository = h('input', {required: true, placeholder: 'owner/repository', autocapitalize: 'off', spellcheck: 'false'});
  const submit = h('button', {type: 'submit', class: 'primary'}, 'Add repository');
  const form = h('form', {class: 'card'}, h('div', {class: 'card-head'}, h('div', null, h('h2', null, 'Add a repository'), h('p', null, 'Resume afterwards to clone it and register it in T3.'))),
    h('div', {class: 'card-body grid-2'}, h('label', {class: 'field'}, h('span', null, 'Repository host'), host), h('label', {class: 'field'}, h('span', null, 'Repository, owner/name'), repository),
      h('div', {class: 'field'}, h('label', {class: 'field-label', for: 'enroll-name'}, 'Directory name'), name, h('small', {id: 'enroll-name-help'}, 'Checked out at /workspace/projects/<name>.'))),
    h('div', {class: 'card-foot'}, h('span'), submit));
  form.addEventListener('input', () => { form.dataset.dirty = 'true'; });
  form.onsubmit = async event => {
    event.preventDefault(); submit.disabled = true;
    try {
      await api('workspaces/' + encodeURIComponent(p.metadata.name) + '/repositories', 'POST', {resourceVersion: p.metadata.resourceVersion, project: {name: name.value, host: host.value === 'github-private' ? 'github' : host.value, repository: repository.value.trim(), public: host.value === 'github'}});
      toast('Repository enrollment requested. Wait for Paused, then Resume to clone it.');
      await load(); render();
    } catch (error) { toast(error.message, true); submit.disabled = false; }
  };
  return [list, form];
}

function accessTab(p) {
  const name = p.metadata.name, canEdit = pausedForEdits(p), draft = drafts.get(name);
  const current = draft?.rules || p.spec.https || [];
  const rules = h('div', {class: 'rules'});
  const empty = h('p', {class: 'subtle'}, 'No additional HTTPS destinations are allowed.');
  const syncEmpty = () => { empty.hidden = rules.children.length > 0; form.dataset.dirty = 'true'; remember(); };
  const remember = () => drafts.set(name, {rules: readRules(rules), resourceVersion: drafts.get(name)?.resourceVersion || p.metadata.resourceVersion});
  for (const rule of current) rules.append(ruleEditor(rule, !canEdit, syncEmpty));
  empty.hidden = current.length > 0;
  const form = h('form', {class: 'card access-form'}, h('div', {class: 'card-head'}, h('div', null, h('h2', null, 'Outbound HTTPS'), h('p', null, 'Only listed hosts, methods and path prefixes are allowed. A path of / permits the whole host. Registry rules permit downloads, not specific package versions.'))),
    h('div', {class: 'card-body stack'}, !canEdit && p.spec.desiredState !== 'Removed' ? callout('warn', 'Pause to change access', 'Pause the sandbox and wait for Paused to edit these rules. Saving keeps it paused until you choose Resume.', p.spec.desiredState === 'Running' ? lifecycleButton(p, 'small') : null) : null, rules, empty));
  if (canEdit) {
    const save = h('button', {type: 'submit', class: 'primary'}, 'Save access');
    form.append(h('div', {class: 'card-foot'}, h('button', {type: 'button', class: 'small', onclick: () => { rules.append(ruleEditor(undefined, false, syncEmpty)); syncEmpty(); rules.lastChild.querySelector('input').focus(); }}, '+ Add destination'),
      h('div', {class: 'actions'}, draft ? h('button', {type: 'button', class: 'ghost', onclick: () => { drafts.delete(name); render(); }}, 'Discard changes') : null, save)));
    form.addEventListener('input', () => { form.dataset.dirty = 'true'; remember(); });
    form.onsubmit = async event => {
      event.preventDefault();
      if (!validRules(rules)) return;
      save.disabled = true;
      try {
        await api('workspaces/' + encodeURIComponent(name) + '/access', 'PATCH', {resourceVersion: drafts.get(name)?.resourceVersion || p.metadata.resourceVersion, https: readRules(rules)});
        drafts.delete(name); toast(name + ': access update requested. The project remains paused.');
        await load(); render();
      } catch (error) { toast(error.message, true); save.disabled = false; }
    };
  }
  return [callout('info', null, 'Private networks, localhost and cloud metadata are always blocked. Repository access is managed separately and cannot be widened by these rules.'), form];
}

function historyTab(p) {
  const name = p.metadata.name, body = h('ol', {class: 'timeline', 'aria-label': 'Access history'});
  const fill = records => body.replaceChildren(...(records.length ? records.map(r => h('li', null, h('time', {datetime: r.time}, new Date(r.time).toLocaleString([], {dateStyle: 'medium', timeStyle: 'short'})), h('div', null, h('strong', null, r.action), r.message ? h('p', null, r.message) : null)))
    : [h('li', null, h('span'), h('p', null, 'No audit records yet.'))]));
  if (audits.has(name)) fill(audits.get(name)); else body.append(h('li', null, h('span'), h('p', null, 'Loading history…')));
  api('workspaces/' + encodeURIComponent(name) + '/audit').then(records => {
    records = records.sort((a, b) => b.time.localeCompare(a.time)); audits.set(name, records); fill(records);
  }).catch(error => toast(error.message, true));
  return [h('section', {class: 'card'}, h('div', {class: 'card-head'}, h('div', null, h('h2', null, 'Access history'), h('p', null, 'Lifecycle, access and preparation changes, newest first.'))), body)];
}

async function existingSandboxes() {
  if (!session.sandboxControl) return [];
  try {
    const response = await fetch('/sandboxes/workspaces', {credentials: 'same-origin'});
    return response.ok ? await response.json() : [];
  } catch { return []; }
}
async function load() {
  if (loading) return;
  loading = true;
  try { [projects, existing] = await Promise.all([api('workspaces').then(items => items || []), existingSandboxes()]); loaded = true; }
  catch (error) { toast(error.message, true); }
  finally { loading = false; }
}
async function poll() { await load(); render(true); }

window.addEventListener('hashchange', () => { render(); window.scrollTo(0, 0); view.focus({preventScroll: true}); });
api('repositories').then(items => { repositories = items || []; }).catch(error => { repositories = []; toast(error.message, true); }).finally(() => { if (location.hash.startsWith('#/new')) render(); });
api('session').then(value => {
  session = value;
  if (session.oidc) {
    const logout = document.querySelector('#console-logout'); logout.hidden = false;
    logout.onclick = async () => { const response = await fetch('/auth/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'same-origin'}); if (response.ok) location.assign('/auth/signed-out'); else toast('Sign-out failed; try again.', true); };
  }
}).catch(error => toast(error.message, true)).finally(() => { poll(); setInterval(poll, 10000); });
render();
