'use strict';
const message = document.querySelector('#message');
const drafts = new Map(), openPanels = new Set();
let removedOpen = false;
let refreshing = false, nameAutomatic = true;
async function api(path, method = 'GET', body) {
  const response = await fetch('/api/v1/' + path, {method, credentials: 'same-origin', headers: {'Content-Type': 'application/json'}, body: body === undefined ? undefined : JSON.stringify(body)});
  if (!response.ok) throw new Error((await response.text()).trim());
  return response.json();
}
function element(tag, text, className) { const e = document.createElement(tag); if (text) e.textContent = text; if (className) e.className = className; return e; }
function label(text, input) { const result = element('label', text); result.append(input); return result; }
function ruleRow(root, rule = {host: '', methods: ['GET', 'HEAD'], paths: ['/']}, disabled = false) {
  const row = element('div', null, 'rule-row');
  const host = element('input'); host.name = 'host'; host.value = rule.host; host.required = true; host.placeholder = 'api.example.com';
  const methods = element('input'); methods.name = 'methods'; methods.value = rule.methods.join(', '); methods.required = true;
  const paths = element('textarea'); paths.name = 'paths'; paths.value = rule.paths.join('\n'); paths.rows = 2; paths.required = true;
  for (const input of [host, methods, paths]) input.disabled = disabled;
  row.append(label('Hostname', host), label('Methods', methods), label('Path prefixes, one per line', paths));
  if (!disabled) { const remove = element('button', 'Remove destination', 'secondary'); remove.type = 'button'; remove.onclick = () => { row.remove(); root.dispatchEvent(new Event('input', {bubbles: true})); updateCreateMeta(); }; row.append(remove); }
  root.append(row);
  updateCreateMeta();
}
function readRules(root) { return [...root.querySelectorAll('.rule-row')].map(row => ({host: row.querySelector('[name=host]').value.trim(), methods: row.querySelector('[name=methods]').value.split(',').map(s => s.trim()).filter(Boolean), paths: row.querySelector('[name=paths]').value.split('\n').map(s => s.trim()).filter(Boolean)})); }
function toolLabel(value) {
  return ({go: 'Go', python: 'Python', rust: 'Rust', npm: 'npm'})[value] || value;
}
function slugFromRepository(repository) {
  const leaf = (repository || '').split('/').pop() || '';
  return leaf.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40).replace(/-$/, '') || '';
}
function updateCreateMeta() {
  const form = document.querySelector('#create');
  if (!form) return;
  const name = form.querySelector('#name');
  const repositories = form.querySelector('#repositories');
  const summaryName = document.querySelector('#summary-name');
  const summaryRepo = document.querySelector('#summary-repo');
  const summaryTools = document.querySelector('#summary-tools');
  const nameMode = document.querySelector('#name-mode');
  const ruleCount = document.querySelector('#rule-count');
  const path = document.querySelector('#workbench-path');
  if (nameAutomatic && repositories?.value) {
    const slug = slugFromRepository(repositories.value);
    if (slug && /^[a-z][a-z0-9-]{0,39}$/.test(slug)) name.value = slug;
  }
  if (summaryName) summaryName.textContent = name?.value || 'Name your sandbox';
  if (summaryRepo) summaryRepo.textContent = repositories?.value || 'Select a repository';
  if (path) path.textContent = name?.value ? 'Projects / ' + name.value : 'Projects';
  if (nameMode) nameMode.textContent = nameAutomatic ? 'Named automatically from repository' : 'Custom project name';
  const tools = [...form.querySelectorAll('input[name=registry]:checked')].map(input => toolLabel(input.value));
  if (summaryTools) summaryTools.textContent = tools.length ? tools.join(', ') : 'None selected';
  for (const tool of form.querySelectorAll('.tool')) tool.classList.toggle('selected', tool.querySelector('input')?.checked);
  if (ruleCount) {
    const count = document.querySelectorAll('#create-rules .rule-row').length;
    ruleCount.textContent = count + ' configured';
  }
}
function statusPanel(p) {
  const condition = p.status?.conditions?.find(c => c.type === 'Ready');
  const panel = element('div', null, 'project-status');
  if (condition && condition.observedGeneration !== undefined && condition.observedGeneration !== p.metadata.generation) {
    panel.append(element('p', 'Update requested. Waiting for the controller to apply it.')); return panel;
  }
  if (!condition) { panel.append(element('p', 'Request saved. Waiting for preparation to begin.')); return panel; }
  const blocked = condition.status !== 'True' && ['PreparationPending', 'PolicyRejected', 'CleanupPending'].includes(condition.reason) && !condition.message.startsWith('Waiting for');
  if (blocked) {
    panel.classList.add('needs-attention');
    panel.append(element('strong', p.status?.phase === 'Removing' ? 'Removal needs attention' : 'Preparation needs attention'));
  }
  panel.append(element('p', condition.message));
  if (blocked) panel.append(element('p', 'The controller will retry automatically. Correct the setting described above; keep this project request.', 'hint'));
  return panel;
}
function accessPanel(p) {
  const details = element('details', null, 'access-panel');
  details.append(element('summary', 'Access and egress'));
  details.open = openPanels.has(p.metadata.name);
  details.ontoggle = () => { if (details.open) openPanels.add(p.metadata.name); else openPanels.delete(p.metadata.name); };
  details.append(element('h4', 'Repository access'), element('p', p.spec.repository + ': project-scoped Git and pull request access. Protected branches and merges remain controlled by the repository owner.'));
  details.append(element('h4', 'Outbound HTTPS'), element('p', 'Only listed hosts, methods and path prefixes are allowed. Repository access is managed separately. A path of / permits the whole host. Registry rules permit downloads, not selected package versions.', 'hint'));
  const canEdit = p.spec.desiredState === 'Paused' && p.status?.phase === 'Paused' && p.status?.observedGeneration === p.metadata.generation;
  if (!canEdit && p.spec.desiredState !== 'Removed') details.append(element('p', 'Pause the project and wait for Paused to change access. Saving keeps it paused until you choose Resume.', 'notice'));
  const form = element('form', null, 'access-form'), rules = element('div', null, 'rules');
  const draft = drafts.get(p.metadata.name);
  for (const rule of draft?.rules || p.spec.https || []) ruleRow(rules, rule, !canEdit);
  form.append(rules);
  if (!(draft?.rules || p.spec.https || []).length) form.append(element('p', 'No additional HTTPS destinations are allowed.', 'hint'));
  if (canEdit) {
    const add = element('button', 'Add HTTPS destination', 'secondary'); add.type = 'button'; add.onclick = () => { ruleRow(rules); rules.dispatchEvent(new Event('input', {bubbles: true})); };
    const save = element('button', 'Save access'); save.type = 'submit';
    const discard = element('button', 'Discard edits', 'secondary'); discard.type = 'button'; discard.onclick = () => { drafts.delete(p.metadata.name); refresh(); };
    form.append(add, save, discard);
    form.oninput = () => { const initial = drafts.get(p.metadata.name); drafts.set(p.metadata.name, {rules: readRules(rules), resourceVersion: initial?.resourceVersion || p.metadata.resourceVersion}); };
    form.onsubmit = async event => {
      event.preventDefault(); save.disabled = true;
      try {
        await api('workspaces/' + encodeURIComponent(p.metadata.name) + '/access', 'PATCH', {resourceVersion: drafts.get(p.metadata.name)?.resourceVersion || p.metadata.resourceVersion, https: readRules(rules)});
        drafts.delete(p.metadata.name); message.textContent = p.metadata.name + ': access update requested. The project remains paused.';
        document.activeElement?.blur(); await refresh();
      } catch (error) { message.textContent = error.message; save.disabled = false; }
    };
  }
  details.append(form); return details;
}
async function refresh() {
  if (refreshing) return;
  refreshing = true;
  try {
    const projects = await api('workspaces');
    // Polling must not destroy keyboard focus or an in-progress access edit.
    if (document.activeElement?.closest('.access-form')) return;
    const root = document.querySelector('#projects'); root.replaceChildren();
    const active = [], archived = [];
    for (const p of projects || []) (p.status?.phase === 'Removed' ? archived : active).push(p);
    const count = document.querySelector('#project-count');
    if (count) count.textContent = active.length + ' project' + (active.length === 1 ? '' : 's');
    for (const p of active) root.append(await projectArticle(p));
    if (!active.length) root.append(element('p', archived.length ? 'No active project sandboxes.' : 'No project environments have been requested yet.', 'hint'));
    if (archived.length) {
      const folded = element('details', null, 'removed-projects');
      folded.open = removedOpen;
      folded.ontoggle = () => { removedOpen = folded.open; };
      folded.append(element('summary', 'Removed (' + archived.length + ')'));
      for (const p of archived) folded.append(await projectArticle(p));
      root.append(folded);
    }
  } catch (error) { message.textContent = error.message; }
  finally { refreshing = false; }
}
async function projectArticle(p) {
      const article = element('article'); article.dataset.project = p.metadata.name;
      const head = element('div', null, 'project-head');
      head.append(element('h3', p.metadata.name), element('span', p.status?.phase || 'Requested', 'phase')); article.append(head, element('p', p.spec.repository), statusPanel(p));
      if (p.status?.phase === 'Ready') {
        article.append(element('code', 'agent-project-controller connect ' + p.metadata.name));
        article.append(element('code', 'agent-project-controller register --watch'));
        try {
          const clients = await api('workspaces/' + encodeURIComponent(p.metadata.name) + '/clients');
          const lines = (clients.clients || []).filter(c => c.state).map(c => c.kind + ': ' + c.state + (c.path ? ' @ ' + c.path : ''));
          if (lines.length) article.append(element('p', 'Desktop clients — ' + lines.join('; '), 'hint'));
          else article.append(element('p', 'Desktop clients — not registered yet. Run register --watch on an operator machine.', 'hint'));
        } catch (error) {
          article.append(element('p', 'Desktop clients — ledger unavailable', 'hint'));
        }
      }
      article.append(accessPanel(p));
      const buttons = element('div', null, 'actions');
      if (p.spec.desiredState !== 'Removed') for (const [text, state] of [[p.spec.desiredState === 'Paused' ? 'Resume' : 'Pause', p.spec.desiredState === 'Paused' ? 'Running' : 'Paused'], ['Remove project', 'Removed']]) {
        const button = element('button', text, state === 'Removed' ? 'danger' : 'secondary');
        if (state === 'Running' && (p.status?.observedGeneration !== p.metadata.generation || p.status?.phase !== 'Paused') && p.metadata.uid) button.disabled = true;
        button.onclick = async () => {
          if (state === 'Removed' && !confirm('Remove ' + p.metadata.name + '? This revokes access and deletes project storage.')) return;
          button.disabled = true;
          try { await api('workspaces/' + encodeURIComponent(p.metadata.name), 'PATCH', {desiredState: state, resourceVersion: p.metadata.resourceVersion}); message.textContent = p.metadata.name + ': ' + state.toLowerCase() + ' requested'; drafts.delete(p.metadata.name); await refresh(); }
          catch (error) { message.textContent = error.message; button.disabled = false; }
        }; buttons.append(button);
      }
      const audit = element('button', 'View access history', 'secondary');
      audit.onclick = async () => {
        try { const records = await api('workspaces/' + encodeURIComponent(p.metadata.name) + '/audit'); const history = element('pre', records.sort((a, b) => a.time.localeCompare(b.time)).map(r => r.time + '  ' + r.action + (r.message ? '\n  ' + r.message : '')).join('\n') || 'No audit records yet.'); article.append(history); audit.remove(); }
        catch (error) { message.textContent = error.message; }
      }; buttons.append(audit); article.append(buttons); return article;
}
const customRules = document.querySelector('#create-rules');
const createForm = document.querySelector('#create');
document.querySelector('#add-create-rule').onclick = () => ruleRow(customRules);
document.querySelector('#auto-name').onclick = () => {
  nameAutomatic = true;
  updateCreateMeta();
};
createForm.querySelector('#name').addEventListener('input', () => { nameAutomatic = false; updateCreateMeta(); });
createForm.querySelector('#repositories').addEventListener('change', updateCreateMeta);
createForm.addEventListener('change', updateCreateMeta);
createForm.addEventListener('input', event => {
  if (event.target.closest('#create-rules')) updateCreateMeta();
});
createForm.onsubmit = async event => {
  event.preventDefault(); const form = event.target, button = document.querySelector('#open-project'); button.disabled = true;
  try {
    const data = new FormData(form), registries = [...form.querySelectorAll('input[name=registry]:checked')].map(input => input.value);
    await api('workspaces', 'POST', {name: data.get('name'), repository: data.get('repository'), registries, https: readRules(customRules)});
    form.reset(); customRules.replaceChildren(); nameAutomatic = true; updateCreateMeta();
    message.textContent = 'Project requested. Preparation status will appear below.'; await refresh();
  } catch (error) { message.textContent = error.message; }
  finally { button.disabled = false; }
};
api('repositories').then(repos => {
  const select = document.querySelector('#repositories');
  for (const r of repos || []) { const option = element('option', r.full_name); option.value = r.full_name; select.append(option); }
  updateCreateMeta();
}).catch(error => { message.textContent = error.message; });
api('session').then(session => {
  document.querySelector('#existing-sandboxes').hidden = !session.sandboxControl;
  if (session.oidc) {
    const logout = document.querySelector('#logout'); logout.hidden = false;
    logout.onclick = async () => { const response = await fetch('/auth/logout', {method: 'POST', headers: {'Content-Type': 'application/json'}, credentials: 'same-origin'}); if (response.ok) location.assign('/auth/signed-out'); else message.textContent = 'Sign-out failed; try again.'; };
  }
}).catch(error => { message.textContent = error.message; });
updateCreateMeta();
refresh(); setInterval(refresh, 10000);
