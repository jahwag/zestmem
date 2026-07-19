(() => {
  'use strict';
  let token = '';
  let selectedSpace = '';
  const $ = id => document.getElementById(id);

  async function api(path, options = {}) {
    const headers = {...(options.headers || {}), Authorization: `Bearer ${token}`};
    if (options.body) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, {...options, headers, cache: 'no-store'});
    if (!response.ok) throw new Error(response.status === 401 ? 'Invalid or expired API key.' : `Request failed (${response.status}).`);
    return response.json();
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function renderSpaces(catalog) {
    $('spaces').replaceChildren();
    for (const space of catalog.spaces || []) {
      const button = element('button', 'space');
      button.type = 'button';
      button.append(element('strong', '', space.name), element('span', '', `${space.type} · ${space.slug}`));
      button.addEventListener('click', () => {
        selectedSpace = selectedSpace === space.space_id ? '' : space.space_id;
        document.querySelectorAll('.space').forEach(x => x.classList.remove('active'));
        if (selectedSpace) button.classList.add('active');
      });
      $('spaces').append(button);
    }
  }

  function renderResults(result) {
    const container = $('results');
    container.replaceChildren();
    const hits = result.hits || [];
    if (!hits.length) {
      container.append(element('p', 'empty', 'No relevant memories found.'));
      return;
    }
    for (const hit of hits) {
      const button = element('button', 'hit');
      button.type = 'button';
      const meta = element('span', 'meta', `${hit.kind} · revision ${hit.revision}`);
      button.append(meta, element('strong', '', hit.title), element('p', '', hit.excerpt));
      button.addEventListener('click', () => openMemory(hit.memory_id, button));
      container.append(button);
    }
  }

  async function openMemory(id, button) {
    document.querySelectorAll('.hit').forEach(x => x.classList.remove('active'));
    button.classList.add('active');
    const memory = await api(`/api/v1/memories/${encodeURIComponent(id)}`);
    const revision = memory.current_revision;
    const article = $('memory');
    article.replaceChildren();
    article.append(element('span', 'meta', `${revision.kind} · revision ${revision.number}`), element('h2', '', revision.title));
    const topics = element('div', 'topics');
    for (const topic of revision.topics || []) topics.append(element('span', '', topic));
    article.append(topics, element('pre', 'body', revision.body));
    const provenance = element('section', 'provenance');
    provenance.append(element('h3', '', 'Provenance'));
    for (const source of revision.provenance || []) provenance.append(element('p', '', `${source.class}: ${source.locator || 'unverified'}`));
    article.append(provenance);
  }

  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    token = $('token').value.trim();
    $('login-error').textContent = '';
    try {
      const catalog = await api('/api/v1/catalog');
      $('token').value = '';
      renderSpaces(catalog);
      $('login').hidden = true;
      $('workspace').hidden = false;
      $('logout').hidden = false;
      $('query').focus();
    } catch (error) {
      token = '';
      $('login-error').textContent = error.message;
    }
  });

  $('search-form').addEventListener('submit', async event => {
    event.preventDefault();
    $('status').textContent = 'Searching…';
    try {
      const input = {query: $('query').value.trim(), limit: 20};
      if (selectedSpace) input.space_ids = [selectedSpace];
      const result = await api('/api/v1/recall', {method: 'POST', body: JSON.stringify(input)});
      renderResults(result);
      $('status').textContent = `${(result.hits || []).length} memories recalled`;
    } catch (error) {
      $('status').textContent = error.message;
    }
  });

  $('logout').addEventListener('click', () => {
    token = '';
    selectedSpace = '';
    $('workspace').hidden = true;
    $('logout').hidden = true;
    $('login').hidden = false;
    $('memory').replaceChildren(element('p', 'empty', 'Select a recalled memory to read its full revision and provenance.'));
  });
})();
