'use strict';

const $ = (id) => document.getElementById(id);
const snapshot = $('embedded-report');
const state = { report: null, rows: [], filter: 'all', query: '', selected: null, focusReturn: null };
const labels = { ok: 'In sync', unknown: 'Untracked', changed: 'Local changes', tool_changed: 'Tool changed', generated_changed: 'Tool changed', missing: 'Missing', unverified: 'Not verified', error: 'Check failed', conflict: 'Conflicting edits', unexpected: 'Not wanted here', ignored: 'Ignored', inactive: 'Not selected', available: 'Project only' };
const severity = { conflict: 0, error: 1, missing: 2, changed: 3, tool_changed: 4, generated_changed: 4, unexpected: 5, unverified: 6, unknown: 7, ignored: 8, ok: 9 };
const list = (value) => Array.isArray(value) ? value : [];
const string = (value, fallback = '') => typeof value === 'string' ? value : fallback;
const element = (tag, className, text) => {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (text !== undefined) node.textContent = text;
  return node;
};
function icon(path, className) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 20 20');
  svg.setAttribute('aria-hidden', 'true');
  if (className) svg.setAttribute('class', className);
  const shape = document.createElementNS(svg.namespaceURI, 'path');
  shape.setAttribute('d', path);
  svg.append(shape);
  return svg;
}
const symbols = {
  skills: 'M4 2h8l4 4v12H4z M12 2v5h4 M7 11h6 M7 14h4',
  mcp: 'M7 3v4 M13 3v4 M5 7h10v2a5 5 0 0 1-10 0z M10 14v4',
  plugins: 'M7 3h6v4h4v6h-4v4H7v-4H3V7h4z',
  scope: 'M7 3H3v4 M13 3h4v4 M17 13v4h-4 M7 17H3v-4 M6 10l3 3 5-6',
  tool: 'm4 5 4 5-4 5 M11 15h5',
  person: 'M13 6a3 3 0 1 1-6 0 3 3 0 0 1 6 0 M4 17v-2a6 6 0 0 1 12 0v2',
  share: 'M8 6H4v10h10v-4 M11 3h6v6 M17 3l-8 8',
  unknown: 'M7 7a3 3 0 1 1 5 2c-2 1-2 2-2 3 M10 16v.1',
};
function ownerSymbol(value) {
  if (value === 'manual') return symbols.person;
  if (value === 'skillshare') return symbols.share;
  if (['native', 'plugin', 'builtin', 'codex-system', 'claude-sync', 'native-plugin'].includes(value)) return symbols.plugins;
  if (['td', 'jev', 'skills-cli'].includes(value) || string(value).startsWith('tool:')) return symbols.tool;
  return symbols.unknown;
}
for (const tab of document.querySelectorAll('.section-tab')) {
  tab.prepend(icon(symbols[tab.dataset.view], 'nav-icon'));
}
function statusNode(status) {
  return element('span', `status ${Object.hasOwn(labels, status) ? status : 'unverified'}`, labels[status] || status || 'Not verified');
}
function isAttention(row) { return !['ok', 'ignored', 'inactive', 'available'].includes(row.status); }
function isChanged(row) { return ['changed', 'tool_changed', 'generated_changed', 'conflict'].includes(row.status); }
function basename(path) { return string(path).split('/').filter(Boolean).at(-1) || 'Unnamed skill'; }
function ownerLabel(value) {
  if (string(value).startsWith('tool:')) return `${value.slice(5)} (tool)`;
  const names = { manual: 'You', skillshare: 'Skillshare', 'skills-cli': 'skills CLI', td: 'td CLI', jev: 'jev CLI', builtin: 'Agent built-in', 'codex-system': 'Codex built-in', 'claude-sync': 'Claude account sync', native: 'Agent / plugin', plugin: 'Agent plugin', unknown: 'Not recorded' };
  return names[value] || value || 'Not recorded';
}
function makeRows(report) {
  const inventory = list(report.inventory?.items);
  const findings = list(report.findings).filter((finding) => finding.owner !== 'mcp' && !string(finding.id).startsWith('mcp:'));
  const covered = new Set();
  const rows = findings.map((finding) => {
    const paths = list(finding.locations);
    const item = inventory.find((candidate) => candidate.id === finding.id || candidate.name === finding.name && list(candidate.locations).some((path) => paths.includes(path)));
    if (item) covered.add(item.id || item.path);
    return { ...item, ...finding, name: finding.name || item?.name || basename(finding.id), locations: paths.length ? paths : list(item?.locations), status: finding.status || 'unverified' };
  });
  for (const item of inventory) {
    if (covered.has(item.id || item.path)) continue;
    const match = rows.some((row) => row.id === item.id || list(item.locations).some((path) => list(row.locations).includes(path)));
    if (!match) rows.push({ ...item, status: 'unknown', message: 'Found on this machine, but not recorded in the catalog. It has been left in place.' });
  }
  return rows.sort((a, b) => (severity[a.status] ?? 6) - (severity[b.status] ?? 6) || a.name.localeCompare(b.name));
}
function plural(count, one, many = `${one}s`) { return `${count} ${count === 1 ? one : many}`; }
function renderReport(report) {
  state.report = report;
  state.rows = makeRows(report);
  const machine = report.machine || {};
  $('machine-name').textContent = machine.hostname || 'This computer';
  $('machine-platform').textContent = [machine.os === 'darwin' ? 'macOS' : machine.os === 'linux' ? 'Linux' : machine.os, machine.arch].filter(Boolean).join(' · ') || 'Platform not reported';
  $('machine-groups').replaceChildren();
  for (const group of list(machine.groups)) $('machine-groups').append(element('span', 'group-pill', group));
  if (!list(machine.groups).length) $('machine-groups').append(element('span', 'group-pill empty-group', 'No matching groups'));
  const date = new Date(report.generated_at);
  $('last-check').textContent = Number.isNaN(date.valueOf()) ? 'Checked now' : `Checked ${date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`;
  const attention = state.rows.filter(isAttention).length;
  const mcpAttention = list(report.findings).filter((finding) => (finding.owner === 'mcp' || string(finding.id).startsWith('mcp:')) && isAttention(finding)).length;
  const totalAttention = attention + mcpAttention;
  const unknown = state.rows.filter((row) => row.status === 'unknown').length;
  const changed = state.rows.filter(isChanged).length;
  const issues = [...list(report.inventory?.issues), ...list(report.mcp?.issues), ...list(report.plugins?.issues), ...list(report.issues)];
  $('summary-title').textContent = issues.length ? 'Some checks need attention' : totalAttention ? `${plural(totalAttention, 'item')} to look at` : 'Your recorded setup matches';
  $('summary-copy').textContent = issues.length ? `${plural(issues.length, 'issue')} prevented a complete check. Open Scan coverage for details.` : unknown ? `${plural(unknown, 'untracked skill')} found${changed ? `; ${plural(changed, 'skill')} changed` : ''}. Everything is still in place.` : changed ? 'Review the differences before accepting or applying changes.' : attention ? 'Review the inventory below. No files have been changed.' : state.rows.length ? 'No differences found within the scanned folders. Nothing was changed.' : 'No skills found in the scanned folders. Check Scan coverage to see where we looked.';
  if (mcpAttention) $('summary-copy').textContent += ` ${plural(mcpAttention, 'MCP connection')} also need${mcpAttention === 1 ? 's' : ''} review.`;
  $('summary-indicator').className = `indicator ${issues.length ? 'error' : totalAttention ? 'attention' : 'ok'}`;
  $('summary-totals').textContent = `${plural(state.rows.length, 'skill')} · ${plural(list(report.mcp?.entries).length, 'connection')}`;
  $('skill-count').textContent = state.rows.length;
  $('mcp-count').textContent = list(report.mcp?.entries).length;
  $('plugin-count').textContent = list(report.plugins?.entries).length;
  $('attention-count').textContent = attention;
  $('issue-count').textContent = issues.length;
  $('issue-count').hidden = !issues.length;
  renderSkills();
  renderMCP();
  renderPlugins();
  renderScope(issues);
  if (state.selected) {
    const updated = state.rows.find((row) => row.id === state.selected);
    if (updated) renderDetail(updated); else closeDetail();
  }
}
function renderSkills() {
  const matches = state.rows.filter((row) => {
    const filterMatch = state.filter === 'all' || state.filter === 'attention' && isAttention(row) || state.filter === 'changed' && isChanged(row) || state.filter === 'ok' && row.status === 'ok';
    const searchText = [row.name, row.id, row.source, row.owner, row.owner_hint, ...list(row.locations)].join(' ').toLocaleLowerCase();
    return filterMatch && searchText.includes(state.query.toLocaleLowerCase());
  });
  const fragment = document.createDocumentFragment();
  for (const row of matches) {
    const button = element('button', `skill-row${row.id === state.selected ? ' selected' : ''}`);
    button.type = 'button';
    button.setAttribute('aria-label', `${row.name}, ${labels[row.status] || row.status}. Show details`);
    button.setAttribute('aria-expanded', String(row.id === state.selected));
    const identity = element('span', 'skill-identity');
    identity.append(element('span', 'skill-name', row.name));
    identity.append(element('span', 'skill-source', row.source || row.path || list(row.locations)[0] || 'Source not recorded'));
    const owner = element('span', 'skill-owner');
    const ownerName = element('span', 'owner-name', ownerLabel(row.owner || row.owner_hint));
    ownerName.prepend(icon(ownerSymbol(row.owner || row.owner_hint), 'owner-icon'));
    owner.append(ownerName);
    if (row.status === 'unknown' && (row.owner || row.owner_hint)) owner.append(element('small', '', 'Detected, not confirmed'));
    button.append(identity, owner, statusNode(row.status), icon('m7 4 6 6-6 6', 'skill-arrow'));
    button.addEventListener('click', () => openDetail(row, button));
    fragment.append(button);
  }
  if (!matches.length) {
    const empty = element('div', 'empty');
    empty.append(element('strong', '', state.rows.length ? 'No skills match this view.' : 'No skills found yet.'));
    empty.append(element('p', '', state.rows.length ? 'Try a different filter or search.' : 'Open Scan coverage to check the folders being inspected.'));
    fragment.append(empty);
  }
  $('skill-list').replaceChildren(fragment);
  $('list-footnote').textContent = `${matches.length} of ${state.rows.length} skills shown. Unknown items are left in place.`;
}
function renderMCP() {
  const entries = list(state.report?.mcp?.entries);
  const fragment = document.createDocumentFragment();
  for (const entry of entries) {
    const article = element('article', 'connection');
    const identity = element('div');
    identity.append(element('h3', '', entry.name || 'Unnamed connection'), element('span', 'connection-client', entry.client || 'Client not reported'));
    const info = element('div', 'connection-info');
    const summary = typeof entry.summary === 'string' ? entry.summary : entry.summary ? JSON.stringify(entry.summary) : 'Connection configuration found';
    info.append(element('p', '', summary), element('p', 'connection-path', entry.path || ''));
    const finding = list(state.report?.findings).find((item) => item.id === `mcp:${entry.client}:${entry.name}`);
    if (finding) { info.append(statusNode(finding.status), element('p', '', finding.message)); }
    else if (entry.status) info.append(statusNode(entry.status));
    if (entry.managed === false) info.append(element('p', '', 'Not recorded in this catalog.'));
    article.append(identity, info);
    fragment.append(article);
  }
  for (const finding of list(state.report?.findings).filter((item) => string(item.id).startsWith('mcp:') && item.status === 'missing')) {
    const article = element('article', 'connection');
    const identity = element('div');
    identity.append(element('h3', '', finding.name || 'Missing connection'), statusNode('missing'));
    article.append(identity, element('p', 'connection-info', finding.message || 'The accepted connection is missing.'));
    fragment.append(article);
  }
  if (!fragment.childNodes.length) fragment.append(element('p', 'empty', 'No MCP connections were found in the inspected configuration files.'));
  $('mcp-list').replaceChildren(fragment);
}
function renderScope(issues) {
  const context = $('catalog-context');
  context.replaceChildren();
  metaRow(context, 'Scope', state.report?.scope === 'project' ? 'Project catalog' : 'Home catalog');
  metaRow(context, 'Catalog', state.report?.catalog, true);
  metaRow(context, 'Target root', state.report?.target, true);
  const roots = [...new Set([...list(state.report?.inventory?.roots), ...list(state.report?.mcp?.roots), ...list(state.report?.plugins?.skill_roots)])];
  $('root-list').replaceChildren(...roots.map((root) => element('li', '', root)));
  if (!roots.length) $('root-list').append(element('li', '', 'No scan roots were reported.'));
  const groups = list(state.report?.machine?.groups);
  $('group-explanation').textContent = groups.length ? `This machine matches ${groups.join(', ')}. Membership comes from the conditions in your catalog, rather than a manually selected profile.` : 'No configured group conditions matched this machine. Items wanted everywhere still apply.';
  $('issues-list').replaceChildren(...issues.map((issue) => element('li', '', typeof issue === 'string' ? issue : JSON.stringify(issue))));
  if (!issues.length) $('issues-list').append(element('li', 'no-issues', 'No scan errors reported. Tool checks are shown separately for each skill.'));
}
function renderPlugins() {
  const fragment = document.createDocumentFragment();
  for (const entry of list(state.report?.plugins?.entries)) {
    const article = element('article', 'connection');
    const identity = element('div');
    identity.append(element('h3', '', entry.name || 'Unnamed plugin'), element('span', 'connection-client', entry.client || 'Agent not reported'));
    const info = element('div', 'connection-info');
    info.append(element('p', '', entry.enabled ? 'Enabled in agent settings' : 'Disabled in agent settings'));
    info.append(element('p', '', entry.version ? `Installed version: ${entry.version}` : entry.client === 'codex' ? 'Active version is managed by Codex; not inferred from cache.' : 'Installed version not verified.'));
    if (entry.evidence) info.append(element('p', '', `Evidence: ${entry.evidence}`));
    if (entry.path) info.append(element('p', 'connection-path', entry.path));
    article.append(identity, info);
    fragment.append(article);
  }
  if (!fragment.childNodes.length) fragment.append(element('p', 'empty', 'No native plugin declarations were found in the inspected agent settings.'));
  $('plugin-list').replaceChildren(fragment);
}
function metaRow(dl, label, value, code = false) {
  dl.append(element('dt', '', label));
  const dd = element('dd');
  dd.append(element(code ? 'code' : 'span', '', value || 'Not recorded'));
  dl.append(dd);
}
function section(title, description) {
  const node = element('section', 'detail-section');
  node.append(element('h3', '', title));
  if (description) node.append(element('p', '', description));
  return node;
}
function changesSection(title, description, changes, emptyText) {
  const node = section(title, description);
  if (!changes.length) {
    node.append(element('p', '', emptyText));
    return node;
  }
  for (const change of changes) {
    const details = element('details', 'diff-file');
    details.open = changes.length === 1;
    const summary = element('summary', '', change.path || 'File');
    summary.append(element('span', 'kind', change.kind || 'changed'));
    const pre = element('pre', 'diff-body');
    if (change.binary) pre.textContent = 'Binary contents differ.';
    else {
      const diff = string(change.diff) || (change.before || change.after ? `Before:\n${change.before || '(absent)'}\n\nAfter:\n${change.after || '(absent)'}` : 'Contents or file metadata differ. Run the CLI diff for details.');
      for (const line of diff.split('\n')) {
        const kind = line.startsWith('+++') || line.startsWith('---') || line.startsWith('@@') ? 'header' : line.startsWith('+') ? 'add' : line.startsWith('-') ? 'remove' : '';
        pre.append(element('span', `diff-line ${kind}`, line));
      }
    }
    details.append(summary, pre);
    node.append(details);
  }
  return node;
}
function shellQuote(value) { return `'${String(value).replaceAll("'", "'\\''")}'`; }
function addCommand(parent, command) {
  if (state.report?.catalog) command += ` --repo ${shellQuote(state.report.catalog)}`;
  if (state.report?.target) command += ` --home ${shellQuote(state.report.target)}`;
  const block = element('div', 'command');
  block.append(element('code', '', command));
  const button = element('button', '', 'Copy');
  button.type = 'button';
  button.setAttribute('aria-label', 'Copy command');
  button.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(command); toast('Command copied. Run it in your terminal.'); }
    catch { toast('Could not copy. Select the command and copy it manually.'); }
  });
  block.append(button);
  parent.append(block);
}
function renderDetail(row) {
  const content = document.createDocumentFragment();
  content.append(statusNode(row.status), element('h2', '', row.name));
  content.lastChild.id = 'detail-name';
  content.append(element('p', 'detail-message', row.message || 'Inspect the recorded ownership, installed locations, and content comparisons below.'));
  const meta = element('dl', 'detail-meta');
  metaRow(meta, row.status === 'unknown' ? 'Owner hint' : 'Managed by', ownerLabel(row.owner || row.owner_hint));
  metaRow(meta, 'Source', row.source, true);
  if (row.tool_version) metaRow(meta, 'Tool version', row.tool_version, true);
  if (row.tool_method || row.provider_method || row.method) metaRow(meta, 'Comparison', row.tool_method || row.provider_method || row.method);
  metaRow(meta, 'Catalog ID', row.id, true);
  content.append(meta);
  const installed = list(row.installed_changes), generated = list(row.generated_changes);
  const accepted = row.status !== 'unknown' && row.status !== 'ignored';
  const toolOwner = ['td', 'jev'].includes(row.owner || row.owner_hint) || string(row.owner).startsWith('tool:') || !!row.tool_version;
  const comparison = element('div', 'comparison');
  for (const [label, value, changed] of [
    ['Accepted', accepted ? 'Recorded' : 'Not saved', false],
    ['Installed', row.status === 'available' ? 'Project only' : row.status === 'missing' ? 'Missing' : installed.length ? 'Changed' : ['unverified', 'error', 'inactive', 'unexpected'].includes(row.status) ? 'Not verified' : accepted ? 'Matches saved' : 'Found locally', installed.length > 0],
    ['Tool output', generated.length ? 'Changed' : row.tool_version && row.status !== 'unverified' ? 'Matches saved' : toolOwner ? 'Not verified' : 'Not applicable', generated.length > 0],
  ]) {
    const cell = element('div', `comparison-cell${changed ? ' changed' : ''}`);
    cell.append(element('span', '', label), element('strong', '', value));
    comparison.append(cell);
  }
  content.append(comparison);
  if (row.provider_error) content.append(element('p', 'notice notice-error', `Tool comparison: ${row.provider_error}`));
  const locations = section('Where it lives');
  const paths = [...new Set([...list(row.locations), row.path].filter(Boolean))];
  const ul = element('ul', 'location-list');
  for (const path of paths) ul.append(element('li', '', path));
  if (!paths.length) ul.append(element('li', '', 'No installed location found.'));
  locations.append(ul);
  content.append(locations);
  if (accepted) content.append(changesSection('Installed vs. accepted', 'Changes on this machine compared with the contents you saved.', installed, row.status === 'available' ? 'Saved for project use; no global installation is expected.' : row.status === 'missing' ? 'The expected installation was not found.' : ['inactive', 'unexpected'].includes(row.status) ? 'Not compared: this skill is outside the machine’s selected rules.' : row.status === 'unverified' || row.status === 'error' ? 'This comparison may be incomplete. See the check message above.' : 'No content differences reported.'));
  if (toolOwner || generated.length) content.append(changesSection('Current tool output vs. accepted', 'What the installed tool supplies today, compared with your saved version.', generated, row.status === 'unverified' || row.status === 'error' || !row.tool_version ? 'Could not verify current tool output. No match is assumed.' : 'No content differences reported.'));
  if (list(row.tool_installed_changes).length) content.append(changesSection('Current tool output vs. installed', 'What would change from the files currently on this machine.', list(row.tool_installed_changes), 'No content differences reported.'));
  const next = section(row.status === 'unknown' ? 'Bring it into the catalog' : 'Inspect from your terminal');
  if (row.status === 'unknown' && paths.length) {
    const hint = row.owner_hint || row.owner;
    const owner = ['td', 'jev', 'skillshare'].includes(hint) ? hint : ['native', 'codex-system', 'claude-sync', 'builtin', 'plugin'].includes(hint) ? 'native' : 'manual';
    addCommand(next, `dotagents adopt ${shellQuote(row.path || paths[0])} --owner ${owner}`);
    next.append(element('p', 'command-note', 'Adopting records the current contents. Review the owner first; use --groups to limit where it belongs.'));
  } else {
    addCommand(next, `dotagents diff ${shellQuote(row.id)}`);
    next.append(element('p', 'command-note', 'The dashboard is read-only. Accepting or applying changes is a separate, explicit command.'));
  }
  content.append(next);
  $('detail-content').replaceChildren(content);
}
function openDetail(row, button) {
  state.selected = row.id;
  state.focusReturn = button;
  renderDetail(row);
  $('detail').hidden = false;
  setPanelMode();
  $('detail').scrollTop = 0;
  $('close-detail').focus({ preventScroll: true });
  renderSkills();
}
function closeDetail() {
  $('detail').hidden = true;
  state.selected = null;
  setPanelMode();
  renderSkills();
  const rows = [...$('skill-list').querySelectorAll('.skill-row')];
  const restore = rows.find((row) => row.getAttribute('aria-label') === state.focusReturn?.getAttribute('aria-label'));
  restore?.focus({ preventScroll: true });
}
function setPanelMode() {
  const modal = !!state.selected && matchMedia('(max-width:720px)').matches;
  document.querySelector('main').inert = modal;
  document.querySelector('.masthead').inert = modal;
  if (modal) { $('detail').setAttribute('role', 'dialog'); $('detail').setAttribute('aria-modal', 'true'); }
  else { $('detail').removeAttribute('role'); $('detail').removeAttribute('aria-modal'); }
}
matchMedia('(max-width:720px)').addEventListener('change', setPanelMode);
function toast(message) {
  $('toast').textContent = message;
  $('toast').hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { $('toast').hidden = true; }, 3500);
}
async function refresh() {
  if (snapshot) { toast('Saved snapshot. Run dotagents export again for a fresh report.'); return; }
  const button = $('refresh');
  if (button.disabled) return;
  button.disabled = true;
  button.querySelector('span').textContent = 'Checking…';
  $('load-error').hidden = true;
  try {
    const response = await fetch('/api/report', { cache: 'no-store', credentials: 'same-origin', signal: AbortSignal.timeout(60000) });
    if (!response.ok) {
      let message = `The check returned an error (${response.status}).`;
      try { const body = await response.json(); if (body.error) message = body.error; } catch {}
      throw new Error(message);
    }
    renderReport(await response.json());
  } catch (error) {
    $('load-error').textContent = `${error.name === 'TimeoutError' ? 'The check took too long.' : error.message || 'Could not reach the local server.'} ${state.report ? 'The previous report is still shown. ' : ''}Check the terminal running dotagents, then try Check now again.`;
    $('load-error').hidden = false;
    if (!state.report) {
      $('summary-title').textContent = 'The check did not complete';
      $('summary-copy').textContent = 'No result is assumed. Your setup has not been changed.';
      $('summary-indicator').className = 'indicator error';
      $('skill-list').replaceChildren(element('p', 'empty', 'No report available. Resolve the connection error and check again.'));
    }
  } finally {
    button.disabled = false;
    button.querySelector('span').textContent = 'Check now';
  }
}
$('refresh').addEventListener('click', refresh);
$('close-detail').addEventListener('click', closeDetail);
$('search').addEventListener('input', (event) => { state.query = event.target.value; renderSkills(); });
document.querySelectorAll('[data-filter]').forEach((button) => button.addEventListener('click', () => {
  state.filter = button.dataset.filter;
  document.querySelectorAll('[data-filter]').forEach((item) => { item.classList.toggle('active', item === button); item.setAttribute('aria-pressed', String(item === button)); });
  renderSkills();
}));
document.querySelectorAll('[data-view]').forEach((button) => button.addEventListener('click', () => {
  document.querySelectorAll('[data-view]').forEach((item) => { item.classList.toggle('active', item === button); if (item === button) item.setAttribute('aria-current', 'page'); else item.removeAttribute('aria-current'); });
  $('registry').hidden = button.dataset.view !== 'skills';
  $('mcp-view').hidden = button.dataset.view !== 'mcp';
  $('plugins-view').hidden = button.dataset.view !== 'plugins';
  $('scope-view').hidden = button.dataset.view !== 'scope';
  if (state.selected) closeDetail();
}));
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape' && state.selected) { closeDetail(); return; }
  if (event.key === 'Tab' && $('detail').getAttribute('aria-modal') === 'true') {
    const controls = [...$('detail').querySelectorAll('button, summary, a[href], input, [tabindex="0"]')];
    if (event.shiftKey && document.activeElement === controls[0]) { event.preventDefault(); controls.at(-1)?.focus(); }
    else if (!event.shiftKey && document.activeElement === controls.at(-1)) { event.preventDefault(); controls[0]?.focus(); }
  }
  if (event.key === '/' && !['INPUT', 'TEXTAREA'].includes(document.activeElement?.tagName) && !$('registry').hidden) { event.preventDefault(); $('search').focus(); }
});
if (snapshot) {
  $('refresh').querySelector('span').textContent = 'Saved report';
  document.querySelector('.local-label').lastChild.textContent = 'Saved snapshot';
  try {
    renderReport(JSON.parse(snapshot.textContent));
    $('last-check').textContent = $('last-check').textContent.replace('Checked', 'Saved');
  } catch {
    $('load-error').textContent = 'This saved report could not be read. Run dotagents export again to create a new copy.';
    $('load-error').hidden = false;
    $('summary-title').textContent = 'The saved report could not be read';
    $('summary-copy').textContent = 'No result is assumed.';
    $('summary-indicator').className = 'indicator error';
  }
} else {
  refresh();
}
