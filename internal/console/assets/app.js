(() => {
  'use strict';

  let token = '';
  let spaces = [];
  let catalogCursor = '';
  let catalogTotal = 0;
  let selectedSpace = '';
  let mode = 'browse';
  let committedQuery = '';
  let nextCursor = '';
  let loadGeneration = 0;
  let selectedMemory = '';
  let openFocusTimer = 0;
  const $ = id => document.getElementById(id);

  class APIError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }

  async function api(path, options = {}) {
    const headers = {...(options.headers || {}), Authorization: `Bearer ${token}`};
    if (options.body) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, {...options, headers, cache: 'no-store'});
    if (!response.ok) {
      const message = response.status === 401
        ? 'That credential is invalid, expired, or revoked.'
        : response.status === 429
          ? 'Too many requests. Wait a moment and try again.'
          : `ZestMem could not complete the request (${response.status}).`;
      throw new APIError(message, response.status);
    }
    return response.json();
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  function formatDate(value) {
    if (!value) return '';
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? '' : new Intl.DateTimeFormat(undefined, {
      dateStyle: 'medium', timeStyle: 'short'
    }).format(date);
  }

  function spaceName(id) {
    return spaces.find(space => space.space_id === id)?.name || 'Unknown space';
  }

  async function loadCatalogPage(cursor = '') {
    const suffix = cursor ? `?cursor=${encodeURIComponent(cursor)}` : '';
    return api(`/api/v1/catalog${suffix}`);
  }

  function renderSpaces() {
    const nav = $('spaces');
    nav.replaceChildren();
    const all = element('button', `space${selectedSpace ? '' : ' active'}`);
    all.type = 'button';
    all.dataset.spaceId = '';
    all.setAttribute('aria-pressed', String(!selectedSpace));
    all.append(element('strong', '', 'All memories'), element('span', '', 'Every readable Space'));
    nav.append(all);

    for (const space of spaces) {
      const button = element('button', 'space');
      button.type = 'button';
      button.dataset.spaceId = space.space_id;
      const active = selectedSpace === space.space_id;
      button.classList.toggle('active', active);
      button.setAttribute('aria-pressed', String(active));
      button.append(element('strong', '', space.name), element('span', '', `${space.type} · ${space.slug}`));
      nav.append(button);
    }
    $('space-count').textContent = catalogCursor
      ? `${spaces.length} of ${catalogTotal} readable Spaces`
      : `${spaces.length} readable ${spaces.length === 1 ? 'Space' : 'Spaces'}`;
    $('load-spaces').hidden = !catalogCursor;
    const topics = [...new Set(spaces.flatMap(space => space.topics || []))].sort((a, b) => a.localeCompare(b));
    const topic = $('topic');
    const selectedTopic = topic.value;
    topic.replaceChildren(new Option('All topics', ''));
    for (const value of topics) topic.append(new Option(value, value));
    if (topics.includes(selectedTopic)) topic.value = selectedTopic;
    document.querySelectorAll('.space-name[data-space-id]').forEach(label => {
      label.textContent = spaceName(label.dataset.spaceId);
    });
  }

  function handleSpaceClick(event) {
    const button = event.target.closest('.space');
    if (!button) return;
    selectedSpace = button.dataset.spaceId;
    $('spaces').querySelectorAll('.space').forEach(item => {
      const active = item === button;
      item.classList.toggle('active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    closeSidebar();
    loadResults(false);
  }

  function filters(cursor = '') {
    const input = {limit: 20};
    if (selectedSpace) input.space_ids = [selectedSpace];
    if ($('kind').value) input.kinds = [$('kind').value];
    if ($('topic').value) input.topics = [$('topic').value];
    if (cursor) input.cursor = cursor;
    return input;
  }

  function currentQuery() {
    return $('query').value.trim();
  }

  async function requestPage(cursor) {
    const input = filters(cursor);
    if (mode === 'search') {
      input.query = committedQuery;
      const result = await api('/api/v1/recall', {method: 'POST', body: JSON.stringify(input)});
      return {items: result.hits || [], cursor: result.next_cursor || ''};
    }
    const result = await api('/api/v1/browse', {method: 'POST', body: JSON.stringify(input)});
    return {items: result.items || [], cursor: result.next_cursor || ''};
  }

  async function loadResults(append) {
    const generation = append ? loadGeneration : ++loadGeneration;
    const cursor = append ? nextCursor : '';
    if (!append) {
      selectedMemory = '';
      $('results').replaceChildren();
      showMemoryPlaceholder();
    }
    setBusy(true);
    $('status').textContent = mode === 'search' ? 'Searching memories…' : 'Loading newest memories…';
    try {
      const page = await requestPage(cursor);
      if (generation !== loadGeneration) return;
      renderResults(page.items, append);
      nextCursor = page.cursor;
      $('load-more').hidden = !nextCursor;
      $('result-kicker').textContent = mode === 'search' ? 'Ranked by relevance' : 'Newest first';
      $('results-title').textContent = mode === 'search' ? 'Search results' : 'Memories';
      const scope = selectedSpace ? ` in ${spaceName(selectedSpace)}` : '';
      $('status').textContent = `${page.items.length}${append ? ' more' : ''} ${page.items.length === 1 ? 'memory' : 'memories'}${scope}`;
    } catch (error) {
      if (generation !== loadGeneration) return;
      handleError(error, $('status'));
    } finally {
      if (generation === loadGeneration) setBusy(false);
    }
  }

  function renderResults(items, append) {
    const container = $('results');
    if (!append) container.replaceChildren();
    if (!items.length && !append) {
      const empty = element('div', 'empty-list');
      empty.append(element('span', '', '○'), element('h3', '', mode === 'search' ? 'No relevant memories' : 'No memories here'),
        element('p', '', mode === 'search' ? 'Try broader words or fewer filters.' : 'Choose another Space or remove a filter.'));
      container.append(empty);
      return;
    }
    for (const item of items) {
      const button = element('button', 'hit');
      button.type = 'button';
      button.dataset.memoryId = item.memory_id;
      button.setAttribute('aria-pressed', 'false');
      const top = element('span', 'hit-topline');
      const resultSpace = element('span', 'space-name', spaceName(item.space_id));
      resultSpace.dataset.spaceId = item.space_id;
      top.append(element('span', `kind kind-${item.kind}`, item.kind), resultSpace);
      if (item.updated_at) top.append(element('time', '', formatDate(item.updated_at)));
      button.append(top, element('strong', '', item.title), element('p', '', item.excerpt));
      if ((item.topics || []).length) {
        const topics = element('span', 'hit-topics');
        for (const topic of item.topics.slice(0, 4)) topics.append(element('span', '', topic));
        button.append(topics);
      }
      button.addEventListener('click', () => openMemory(item.memory_id, button));
      container.append(button);
    }
  }

  async function openMemory(id, button) {
    selectedMemory = id;
    document.querySelectorAll('.hit').forEach(item => {
      const active = item === button;
      item.classList.toggle('active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    const article = $('memory');
    article.replaceChildren(element('p', 'loading', 'Opening memory…'));
    try {
      const memory = await api(`/api/v1/memories/${encodeURIComponent(id)}`);
      if (selectedMemory !== id) return;
      renderMemory(memory);
    } catch (error) {
      if (selectedMemory !== id) return;
      handleError(error, article);
    }
  }

  function renderMemory(memory) {
    const revision = memory.current_revision;
    const article = $('memory');
    article.replaceChildren();
    const heading = element('div', 'memory-heading');
    const meta = element('div', 'memory-eyebrow');
    const detailSpace = element('span', 'space-name', spaceName(memory.space_id));
    detailSpace.dataset.spaceId = memory.space_id;
    meta.append(element('span', `kind kind-${revision.kind}`, revision.kind), element('span', '', `Revision ${revision.number}`), detailSpace);
    heading.append(meta, element('h2', '', revision.title));
    const topics = element('div', 'topics');
    for (const topic of revision.topics || []) topics.append(element('span', '', topic));
    heading.append(topics);
    article.append(heading, element('pre', 'body', revision.body));

    const facts = element('dl', 'memory-facts');
    addFact(facts, 'Remembered', formatDate(memory.created_at));
    addFact(facts, 'Revision made', formatDate(revision.created_at));
    addFact(facts, 'Valid until', revision.valid_until ? formatDate(revision.valid_until) : 'No expiry');
    addFact(facts, 'Status', memory.status);
    article.append(facts);

    const reason = element('section', 'detail-section');
    reason.append(element('h3', '', 'Reason for remembering'), element('p', '', revision.reason));
    article.append(reason);

    const provenance = element('section', 'detail-section provenance');
    provenance.append(element('h3', '', 'Provenance'));
    const sources = revision.provenance || [];
    if (!sources.length) provenance.append(element('p', 'muted', 'No provenance recorded.'));
    for (const source of sources) {
      const row = element('div', 'source');
      row.append(element('strong', '', source.class), element('span', '', source.locator || 'No locator'));
      provenance.append(row);
    }
    article.append(provenance);
    if (window.matchMedia('(max-width: 760px)').matches) {
      article.tabIndex = -1;
      article.focus({preventScroll: true});
      article.scrollIntoView({behavior: window.matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth', block: 'start'});
    }
  }

  function addFact(list, label, value) {
    const group = element('div', 'fact');
    group.append(element('dt', '', label), element('dd', '', value));
    list.append(group);
  }

  function showMemoryPlaceholder() {
    const empty = element('div', 'empty-state');
    empty.append(element('span', '', '⌁'), element('h2', '', 'Choose a memory'),
      element('p', '', 'Select a result to read its current revision, topics, provenance, and reason for remembering.'));
    $('memory').replaceChildren(empty);
  }

  function setBusy(busy) {
    $('load-more').disabled = busy;
    $('kind').disabled = busy;
    $('topic').disabled = busy;
  }

  function handleError(error, target) {
    if (error instanceof APIError && error.status === 401 && token) {
      lock();
      $('login-error').textContent = error.message;
      return;
    }
    target.textContent = error.message || 'Something went wrong.';
  }

  function openSidebar() {
    document.body.classList.add('sidebar-open');
    $('open-sidebar').setAttribute('aria-expanded', 'true');
    $('space-sidebar').setAttribute('role', 'dialog');
    $('space-sidebar').setAttribute('aria-modal', 'true');
    $('sidebar-backdrop').hidden = false;
    document.querySelector('.content').inert = true;
    document.querySelector('.brand').inert = true;
    $('connection').inert = true;
    window.clearTimeout(openFocusTimer);
    openFocusTimer = window.setTimeout(() => $('close-sidebar').focus(), 200);
  }

  function closeSidebar(restoreFocus = true) {
    const wasOpen = document.body.classList.contains('sidebar-open');
    window.clearTimeout(openFocusTimer);
    document.body.classList.remove('sidebar-open');
    $('open-sidebar').setAttribute('aria-expanded', 'false');
    $('space-sidebar').removeAttribute('role');
    $('space-sidebar').removeAttribute('aria-modal');
    $('sidebar-backdrop').hidden = true;
    document.querySelector('.content').inert = false;
    document.querySelector('.brand').inert = false;
    $('connection').inert = false;
    if (wasOpen && restoreFocus) $('open-sidebar').focus();
  }

  function lock() {
    token = '';
    spaces = [];
    catalogCursor = '';
    catalogTotal = 0;
    selectedSpace = '';
    committedQuery = '';
    mode = 'browse';
    selectedMemory = '';
    nextCursor = '';
    loadGeneration++;
    $('token').value = '';
    $('query').value = '';
    $('clear-search').hidden = true;
    $('kind').value = '';
    $('topic').value = '';
    $('workspace').hidden = true;
    $('connection').hidden = true;
    $('login').hidden = false;
    $('results').replaceChildren();
    closeSidebar(false);
    $('token').focus();
  }

  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    token = $('token').value.trim();
    $('login-error').textContent = '';
    const submit = event.submitter;
    submit.disabled = true;
    submit.textContent = 'Connecting…';
    try {
      const catalog = await loadCatalogPage();
      spaces = catalog.spaces || [];
      catalogCursor = catalog.next_cursor || '';
      catalogTotal = spaces.length + (catalog.omitted_count || 0);
      $('token').value = '';
      renderSpaces();
      $('login').hidden = true;
      $('workspace').hidden = false;
      $('connection').hidden = false;
      mode = 'browse';
      committedQuery = '';
      await loadResults(false);
      $('query').focus();
    } catch (error) {
      token = '';
      $('login-error').textContent = error.message;
      $('token').focus();
    } finally {
      submit.disabled = false;
      submit.textContent = 'Open memory browser';
    }
  });

  $('search-form').addEventListener('submit', event => {
    event.preventDefault();
    committedQuery = currentQuery();
    mode = committedQuery ? 'search' : 'browse';
    $('clear-search').hidden = mode !== 'search';
    loadResults(false);
  });
  $('clear-search').addEventListener('click', () => {
    $('query').value = '';
    committedQuery = '';
    mode = 'browse';
    $('clear-search').hidden = true;
    loadResults(false);
    $('query').focus();
  });
  $('kind').addEventListener('change', () => loadResults(false));
  $('topic').addEventListener('change', () => loadResults(false));
  $('load-more').addEventListener('click', () => loadResults(true));
  $('load-spaces').addEventListener('click', async event => {
    const button = event.currentTarget;
    button.disabled = true;
    try {
      const catalog = await loadCatalogPage(catalogCursor);
      const known = new Set(spaces.map(space => space.space_id));
      spaces.push(...(catalog.spaces || []).filter(space => !known.has(space.space_id)));
      catalogCursor = catalog.next_cursor || '';
      renderSpaces();
    } catch (error) {
      handleError(error, $('space-count'));
    } finally {
      button.disabled = false;
    }
  });
  $('spaces').addEventListener('click', handleSpaceClick);
  $('logout').addEventListener('click', lock);
  $('open-sidebar').addEventListener('click', openSidebar);
  $('close-sidebar').addEventListener('click', () => closeSidebar());
  $('sidebar-backdrop').addEventListener('click', () => closeSidebar());
  document.addEventListener('keydown', event => {
    if (event.key === 'Escape' && document.body.classList.contains('sidebar-open')) {
      event.preventDefault();
      closeSidebar();
    }
  });
  const drawerBreakpoint = window.matchMedia('(max-width: 1050px)');
  const normalizeDrawer = () => {
    if (!drawerBreakpoint.matches && document.body.classList.contains('sidebar-open')) closeSidebar(false);
  };
  drawerBreakpoint.addEventListener('change', normalizeDrawer);
  window.addEventListener('resize', normalizeDrawer);
  new ResizeObserver(normalizeDrawer).observe(document.documentElement);
})();
