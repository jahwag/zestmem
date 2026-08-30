(() => {
  'use strict';

  let spaces = [];
  let catalogCursor = '';
  let catalogTotal = 0;
  let selectedSpace = '';
  let mode = 'browse';
  let committedQuery = '';
  let nextCursor = '';
  let loadedCount = 0;
  let totalCount = 0;
  let loadGeneration = 0;
  let selectedMemory = '';
  let openFocusTimer = 0;
  let authGeneration = 0;
  let authController = new AbortController();
  let committedAdvancedFilters = {statuses: ['active']};
  const $ = id => document.getElementById(id);
  const metadataTimers = new Map();
  const metadataGenerations = new Map();

  class APIError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }

  function newAuthSession() {
    authController.abort();
    authController = new AbortController();
    authGeneration++;
  }

  function stale(error) {
    return error?.name === 'AbortError';
  }

  async function api(path, options = {}) {
    const generation = authGeneration;
    const headers = {...(options.headers || {})};
    if (options.body) headers['Content-Type'] = 'application/json';
    const response = await fetch(path, {...options, headers, credentials: 'same-origin', signal: authController.signal, cache: 'no-store'});
    if (generation !== authGeneration) throw new DOMException('Stale session', 'AbortError');
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

  function selectedStatuses() {
    return $('memory-status').value === 'all' ? ['active', 'retracted'] : [$('memory-status').value || 'active'];
  }

  async function loadMetadata(facet, query = '', cursor = '') {
    const input = {facet, query, limit: 100, statuses: selectedStatuses()};
    if (selectedSpace) input.space_ids = [selectedSpace];
    if (cursor) input.cursor = cursor;
    return api('/api/v1/metadata', {method: 'POST', body: JSON.stringify(input)});
  }

  async function populateMetadata(facet, listID, metaID, query = '') {
    const generation = (metadataGenerations.get(listID) || 0) + 1;
    metadataGenerations.set(listID, generation);
    try {
      const values = [];
      let totalCount = 0;
      let cursor = '';
      do {
        const result = await loadMetadata(facet, query.trim(), cursor);
        if (metadataGenerations.get(listID) !== generation) return;
        values.push(...(result.values || []));
        totalCount = result.total_count || 0;
        cursor = result.next_cursor || '';
      } while (cursor && values.length < 2000);
      const list = $(listID);
      list.replaceChildren();
      for (const item of values) {
        const option = new Option(item.value, item.value);
        option.label = `${item.value} · ${item.memory_count}`;
        list.append(option);
      }
      $(metaID).textContent = `${values.length} of ${totalCount} matching values shown${cursor ? '; type to narrow' : ''}.`;
    } catch (error) {
      if (stale(error)) return;
      if (error.status === 401) lock();
    }
  }

  function scheduleMetadata(inputID, facet, listID, metaID) {
    const prior = metadataTimers.get(inputID);
    if (prior) clearTimeout(prior);
    metadataTimers.set(inputID, setTimeout(() => populateMetadata(facet, listID, metaID, $(inputID).value), 180));
  }

  function refreshMetadata() {
    return Promise.all([
      populateMetadata('topic', 'topic-values', 'topic-meta', $('topic').value),
      populateMetadata('provenance_class', 'provenance-class-values', 'provenance-class-meta', $('provenance-class').value),
      populateMetadata('provenance_locator', 'provenance-locator-values', 'provenance-locator-meta', $('provenance-locator').value),
      populateMetadata('provenance_attribution', 'provenance-attribution-values', 'provenance-attribution-meta', $('provenance-attribution').value)
    ]);
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
    document.querySelectorAll('.space-name[data-space-id]').forEach(label => {
      label.textContent = spaceName(label.dataset.spaceId);
    });
  }

  function handleSpaceClick(event) {
    const button = event.target.closest('.space');
    if (!button) return;
    if (!commitAdvancedFilters()) return;
    selectedSpace = button.dataset.spaceId;
    $('spaces').querySelectorAll('.space').forEach(item => {
      const active = item === button;
      item.classList.toggle('active', active);
      item.setAttribute('aria-pressed', String(active));
    });
    closeSidebar();
    refreshMetadata();
    loadResults(false, true);
  }

  function filters(cursor = '') {
    const input = {limit: 20};
    if (selectedSpace) input.space_ids = [selectedSpace];
    if ($('kind').value) input.kinds = [$('kind').value];
    if ($('topic').value.trim()) input.topics = [$('topic').value.trim()];
    Object.assign(input, committedAdvancedFilters);
    if (cursor) input.cursor = cursor;
    return input;
  }

  function readAdvancedFilters() {
    const input = {statuses: selectedStatuses()};
    const revision = $('revision').valueAsNumber;
    if (Number.isInteger(revision) && revision > 0) input.revision = revision;
    if ($('provenance-class').value.trim()) input.provenance_classes = [$('provenance-class').value.trim()];
    if ($('provenance-locator').value) input.provenance_locators = [$('provenance-locator').value];
    if ($('provenance-attribution').value) input.provenance_attributions = [$('provenance-attribution').value];
    for (const [id, field, end] of [
      ['remembered-from', 'remembered_from', false], ['remembered-to', 'remembered_to', true],
      ['revised-from', 'revised_from', false], ['revised-to', 'revised_to', true],
      ['valid-from', 'valid_from', false], ['valid-to', 'valid_to', true]
    ]) {
      const value = $(id).value;
      if (value) {
        const date = new Date(`${value}T00:00:00`);
        if (end) date.setDate(date.getDate() + 1);
        input[field] = date.toISOString();
      }
    }
    return input;
  }

  function validateDateRanges() {
    let valid = true;
    for (const [fromID, toID] of [['remembered-from', 'remembered-to'], ['revised-from', 'revised-to'], ['valid-from', 'valid-to']]) {
      const from = $(fromID);
      const to = $(toID);
      to.setCustomValidity(from.value && to.value && from.value > to.value ? 'The To date must be on or after the From date.' : '');
      valid = to.checkValidity() && valid;
    }
    return valid;
  }

  function commitAdvancedFilters() {
    const form = $('advanced-filters');
    if (!validateDateRanges() || !form.checkValidity()) {
      setAdvancedFiltersExpanded(true);
      const invalid = form.querySelector(':invalid');
      if (invalid) {
        invalid.reportValidity();
        invalid.focus();
      }
      $('status').textContent = 'Fix the highlighted advanced filter before loading memories.';
      return false;
    }
    committedAdvancedFilters = readAdvancedFilters();
    return true;
  }

  function advancedFiltersDirty() {
    return JSON.stringify(readAdvancedFilters()) !== JSON.stringify(committedAdvancedFilters) || !$('advanced-filters').checkValidity();
  }

  function matchingDescription() {
    const statuses = committedAdvancedFilters.statuses || ['active'];
    const parts = [statuses.length > 1 ? 'active and retracted' : statuses[0]];
    if ($('kind').value) parts.push($('kind').value);
    if ($('topic').value.trim()) parts.push(`topic “${$('topic').value.trim()}”`);
    if (mode === 'search') parts.push('search');
    if (Object.keys(committedAdvancedFilters).some(key => key !== 'statuses')) parts.push('advanced filters');
    return parts.join(', ');
  }

  function currentQuery() {
    return $('query').value.trim();
  }

  async function requestPage(cursor) {
    const input = filters(cursor);
    if (mode === 'search') {
      input.query = committedQuery;
      const result = await api('/api/v1/recall', {method: 'POST', body: JSON.stringify(input)});
      return {items: result.hits || [], cursor: result.next_cursor || '', totalCount: result.total_count || 0};
    }
    const result = await api('/api/v1/browse', {method: 'POST', body: JSON.stringify(input)});
    return {items: result.items || [], cursor: result.next_cursor || '', totalCount: result.total_count || 0};
  }

  async function loadResults(append, filtersCommitted = false) {
    if (append && advancedFiltersDirty()) return loadResults(false);
    if (!append && !filtersCommitted && !commitAdvancedFilters()) return;
    const generation = append ? loadGeneration : ++loadGeneration;
    const cursor = append ? nextCursor : '';
    if (!append) {
      selectedMemory = '';
      loadedCount = 0;
      totalCount = 0;
      nextCursor = '';
      $('load-more').hidden = true;
      $('results').replaceChildren();
      showMemoryPlaceholder();
    }
    setBusy(true);
    $('status').textContent = mode === 'search' ? 'Searching memories…' : 'Loading newest memories…';
    try {
      const page = await requestPage(cursor);
      if (generation !== loadGeneration) return;
      renderResults(page.items, append);
      loadedCount = append ? loadedCount + page.items.length : page.items.length;
      totalCount = Number.isInteger(page.totalCount) ? page.totalCount : loadedCount;
      nextCursor = page.cursor;
      $('load-more').hidden = !nextCursor;
      $('result-kicker').textContent = mode === 'search' ? 'Ranked by relevance' : 'Newest first';
      $('results-title').textContent = mode === 'search' ? 'Search results' : 'Memories';
      const scope = selectedSpace ? ` in ${spaceName(selectedSpace)}` : '';
      $('status').textContent = mode === 'search'
        ? `${loadedCount} of ${totalCount} bounded ranked ${totalCount === 1 ? 'candidate' : 'candidates'} loaded (${matchingDescription()})${scope}`
        : `${loadedCount} of ${totalCount} matching ${matchingDescription()} ${totalCount === 1 ? 'memory' : 'memories'} loaded${scope}`;
    } catch (error) {
      if (generation !== loadGeneration) return;
      if (stale(error)) return;
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
      if (stale(error)) return;
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
    const title = element('h2', '', revision.title);
    title.tabIndex = -1;
    heading.append(meta, title);
    const topics = element('div', 'topics');
    for (const topic of revision.topics || []) topics.append(element('span', '', topic));
    heading.append(topics);
    article.append(heading, element('pre', 'body', revision.body));

  const facts = element('dl', 'memory-facts');
  addFact(facts, 'Remembered', formatDate(memory.created_at));
  addFact(facts, 'Revision made', formatDate(revision.created_at));
  const retrieval = memory.retrieval || {};
  addFact(facts, 'Last recalled', retrieval.last_recalled_at ? formatDate(retrieval.last_recalled_at) : 'Never');
  addFact(facts, 'Recall count', String(retrieval.recall_count || 0));
  addFact(facts, 'Last recalled by', retrieval.last_recalled_by || 'Never');
  addFact(facts, 'Last agent read', retrieval.last_read_at ? formatDate(retrieval.last_read_at) : 'Never');
  addFact(facts, 'Agent read count', String(retrieval.read_count || 0));
  addFact(facts, 'Authenticated revision actor', revision.actor_display_name || 'Not set');
    addFact(facts, 'Authenticated Principal ID', revision.actor_id || 'Unknown Principal');
    addFact(facts, 'Valid until', revision.valid_until ? formatDate(revision.valid_until) : 'No expiry');
    addFact(facts, 'Status', memory.status);
    article.append(facts);

    const associations = memory.historical_associations || [];
    if (associations.length) {
      const section = element('section', 'detail-section associations');
      section.append(element('h3', '', 'Historical association'));
      const list = element('ol', 'association-list');
      list.setAttribute('role', 'list');
      for (const association of associations) {
        const item = element('li', 'association');
        const relationship = association.relationship === 'intended_agent'
          ? 'Administratively recorded intended agent'
          : `Historical association (${association.relationship || 'unknown'})`;
        const facts = element('dl', 'association-facts');
        addFact(facts, relationship, association.principal_display_name || association.principal_id);
        addFact(facts, 'Principal ID', association.principal_id);
        addFact(facts, 'Asserted by', `${association.asserted_by_display_name || 'Unknown operator'} · ${association.asserted_by}`);
        addFact(facts, 'Asserted', formatDate(association.asserted_at));
        const basis = association.basis_locator || 'No locator';
        item.append(facts, element('p', '', `Basis: revision ${association.basis_revision} · ${association.basis_class}: ${basis}`));
        item.append(element('p', '', `Reason: ${association.reason}`));
        item.append(element('p', 'muted', 'Does not imply this Principal authored or supplied the Memory, change the authenticated revision Actor, or grant ownership/access.'));
        list.append(item);
      }
      section.append(list);
      article.append(section);
    }

    const reason = element('section', 'detail-section');
    reason.append(element('h3', '', 'Reason for remembering'), element('p', '', revision.reason));
    article.append(reason);

    const provenance = element('section', 'detail-section provenance');
    provenance.append(element('h3', '', 'Provenance'));
    const sources = revision.provenance || [];
    if (!sources.length) provenance.append(element('p', 'muted', 'No provenance recorded.'));
    for (const source of sources) {
      const row = element('div', 'source');
      const detail = element('span', '', source.locator || 'No locator');
      if (source.attribution) detail.append(document.createElement('br'), element('em', '', `Claimed source attribution: ${source.attribution}`));
      row.append(element('strong', '', source.class), detail);
      provenance.append(row);
    }
    article.append(provenance);
    $('detail-status').textContent = `Memory loaded: ${revision.title}`;
    if (window.matchMedia('(max-width: 760px)').matches) {
      title.focus({preventScroll: true});
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
    for (const id of ['kind', 'topic', 'memory-status', 'revision', 'provenance-class', 'provenance-locator',
      'provenance-attribution', 'remembered-from', 'remembered-to', 'revised-from', 'revised-to', 'valid-from', 'valid-to', 'apply-filters']) {
      $(id).disabled = busy;
    }
  }

  function handleError(error, target) {
    if (error instanceof APIError && error.status === 401) {
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
    $('close-sidebar').focus();
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

  function setAdvancedFiltersExpanded(expanded) {
    $('advanced-filters').hidden = !expanded;
    $('toggle-filters').setAttribute('aria-expanded', String(expanded));
    $('toggle-filters').textContent = expanded ? 'Fewer filters' : 'More filters';
  }

  function clearFilterValues() {
    $('kind').value = '';
    $('topic').value = '';
    $('memory-status').value = 'active';
    for (const id of ['revision', 'provenance-class', 'provenance-locator', 'provenance-attribution',
      'remembered-from', 'remembered-to', 'revised-from', 'revised-to', 'valid-from', 'valid-to']) {
      $(id).value = '';
    }
  }

  function lock() {
    newAuthSession();
    fetch('/console/auth/logout', {method: 'POST', credentials: 'same-origin', cache: 'no-store'}).catch(() => {});
    spaces = [];
    catalogCursor = '';
    catalogTotal = 0;
    selectedSpace = '';
    committedQuery = '';
    mode = 'browse';
    selectedMemory = '';
    nextCursor = '';
    loadGeneration++;
    for (const timer of metadataTimers.values()) clearTimeout(timer);
    metadataTimers.clear();
    metadataGenerations.clear();
    $('token').value = '';
    $('query').value = '';
    $('clear-search').hidden = true;
    clearFilterValues();
    committedAdvancedFilters = {statuses: ['active']};
    $('workspace').hidden = true;
    $('connection').hidden = true;
    $('login').hidden = false;
    $('results').replaceChildren();
    $('spaces').replaceChildren();
    showMemoryPlaceholder();
    for (const id of ['topic-values', 'provenance-class-values', 'provenance-locator-values', 'provenance-attribution-values']) $(id).replaceChildren();
    for (const id of ['topic-meta', 'provenance-class-meta', 'provenance-locator-meta', 'provenance-attribution-meta', 'status', 'detail-status', 'space-count']) $(id).textContent = '';
    closeSidebar(false);
    $('token').focus();
  }

  async function configureLoginMethods() {
    const response = await fetch('/console/auth/config', {
      credentials: 'same-origin', cache: 'no-store'
    }).catch(() => null);
    if (!response?.ok) return;
    const config = await response.json().catch(() => ({}));
    $('oidc-login').hidden = !config.oidc;
    $('login-divider').hidden = !config.oidc;
  }

  async function openMemoryBrowser() {
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
    await refreshMetadata();
    await loadResults(false);
    $('query').focus();
  }

  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    const credential = $('token').value.trim();
    newAuthSession();
    $('login-error').textContent = '';
    const submit = event.submitter;
    submit.disabled = true;
    submit.textContent = 'Connecting…';
    try {
      const login = await fetch('/console/auth/token', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        credentials: 'same-origin',
        cache: 'no-store',
        body: JSON.stringify({token: credential})
      });
      if (!login.ok) throw new APIError('That credential is invalid, expired, or revoked.', login.status);
      await openMemoryBrowser();
    } catch (error) {
      if (stale(error)) return;
      $('login-error').textContent = error.message;
      $('token').focus();
    } finally {
      submit.disabled = false;
      submit.textContent = 'Open memory browser';
    }
  });

  $('search-form').addEventListener('submit', event => {
    event.preventDefault();
    if (!commitAdvancedFilters()) return;
    committedQuery = currentQuery();
    mode = committedQuery ? 'search' : 'browse';
    $('clear-search').hidden = mode !== 'search';
    loadResults(false, true);
  });
  $('clear-search').addEventListener('click', () => {
    if (!commitAdvancedFilters()) return;
    $('query').value = '';
    committedQuery = '';
    mode = 'browse';
    $('clear-search').hidden = true;
    loadResults(false, true);
    $('query').focus();
  });
  $('kind').addEventListener('change', () => {
    if (commitAdvancedFilters()) loadResults(false, true);
  });
  $('topic').addEventListener('change', () => {
    if (commitAdvancedFilters()) loadResults(false, true);
  });
  $('topic').addEventListener('input', () => scheduleMetadata('topic', 'topic', 'topic-values', 'topic-meta'));
  $('provenance-class').addEventListener('input', () => scheduleMetadata('provenance-class', 'provenance_class', 'provenance-class-values', 'provenance-class-meta'));
  $('provenance-locator').addEventListener('input', () => scheduleMetadata('provenance-locator', 'provenance_locator', 'provenance-locator-values', 'provenance-locator-meta'));
  $('provenance-attribution').addEventListener('input', () => scheduleMetadata('provenance-attribution', 'provenance_attribution', 'provenance-attribution-values', 'provenance-attribution-meta'));
  $('toggle-filters').addEventListener('click', event => {
    const expanded = $('advanced-filters').hidden;
    setAdvancedFiltersExpanded(expanded);
  });
  $('advanced-filters').addEventListener('submit', event => {
    event.preventDefault();
    if (!commitAdvancedFilters()) return;
    refreshMetadata();
    loadResults(false, true);
  });
  $('memory-status').addEventListener('change', refreshMetadata);
  $('clear-filters').addEventListener('click', () => {
    clearFilterValues();
    refreshMetadata();
    loadResults(false);
  });
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
      if (stale(error)) return;
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
  (async () => {
    await configureLoginMethods();
    const response = await fetch('/console/auth/session', {
      credentials: 'same-origin', cache: 'no-store'
    }).catch(() => null);
    if (response?.ok) {
      newAuthSession();
      try {
        await openMemoryBrowser();
      } catch (error) {
        if (!stale(error)) lock();
      }
    }
  })();
})();
