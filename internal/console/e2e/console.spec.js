const {test, expect} = require('@playwright/test');

test('browses, filters, searches, paginates, and safely renders memories', async ({page}) => {
  const requests = [];
  page.on('request', request => requests.push({url: request.url(), body: request.postData() || ''}));
  await page.goto('/console/');
  await page.getByLabel('Bearer credential').fill('wrong-key');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.getByRole('alert')).toContainText('invalid, expired, or revoked');

  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.locator('#status')).toHaveText('2 of 3 matching active memories loaded');
  await expect(page.locator('#topic-meta')).toHaveText('2 of 2 matching values shown.');
  await expect(page.locator('#provenance-attribution-values option')).toHaveAttribute('value', 'Avery User');
  await expect(page.getByText('1 of 2 readable Spaces')).toBeVisible();
  await expect(page.locator('#spaces').getByRole('button', {name: /Alpha Space/})).toBeVisible();
  await expect(page.locator('#spaces').getByRole('button', {name: /Beta Space/})).toHaveCount(0);
  await page.getByRole('button', {name: /Reader keys only/}).click();
  await expect(page.locator('.memory-eyebrow')).toContainText('Unknown space');
  await page.getByRole('button', {name: 'Load more Spaces'}).click();
  await expect(page.getByText('2 readable Spaces')).toBeVisible();
  await expect(page.locator('#spaces').getByRole('button', {name: /Beta Space/})).toBeVisible();
  await expect(page.getByRole('button', {name: /Reader keys only/})).toContainText('Beta Space');
  await expect(page.locator('.memory-eyebrow')).toContainText('Beta Space');
  await expect(page.getByRole('button', {name: /Keep HTML inert/})).toBeVisible();

  await page.getByRole('button', {name: /Keep HTML inert/}).click();
  await expect(page.getByRole('heading', {name: 'Keep HTML inert'})).toBeVisible();
  await expect(page.getByText('Prevent active content in durable memories')).toBeVisible();
  await expect(page.getByText('Athena')).toBeVisible();
  await expect(page.getByText('Claimed source attribution: Avery User')).toBeVisible();
  await expect(page.getByText('agent', {exact: true})).toBeVisible();
  await expect(page.locator('pre.body')).toContainText('<img src=x onerror=window.hacked=true>');
  expect(await page.evaluate(() => window.hacked)).toBeUndefined();
  await expect(page.locator('#memory img')).toHaveCount(0);

  await page.getByRole('button', {name: 'Load more'}).click();
  await expect(page.getByRole('button', {name: /Old deployment lesson/})).toBeVisible();
  await expect(page.locator('#status')).toHaveText('3 of 3 matching active memories loaded');

  await page.getByRole('button', {name: 'More filters'}).click();
  await page.getByLabel('Revision').fill('3');
  await page.getByLabel('Kind').selectOption('lesson');
  await expect(page.getByRole('button', {name: /Old deployment lesson/})).toBeVisible();
  const uncommittedBrowse = requests.filter(request => request.url.endsWith('/api/v1/browse')).at(-1);
  expect(JSON.parse(uncommittedBrowse.body)).not.toHaveProperty('revision');
  await page.getByLabel('Kind').selectOption('');
  await page.getByLabel('Revision').fill('2');
  await page.getByLabel('Provenance class').fill('verified');
  await page.getByLabel('Provenance locator').fill('architecture review');
  await page.getByLabel('Claimed source attribution').fill('Avery User');
  await page.getByLabel('Revision').press('Enter');
  const filteredBrowse = requests.filter(request => request.url.endsWith('/api/v1/browse')).at(-1);
  expect(JSON.parse(filteredBrowse.body)).toMatchObject({revision: 2, provenance_classes: ['verified'], provenance_locators: ['architecture review'], provenance_attributions: ['Avery User'], statuses: ['active']});
  await page.getByRole('button', {name: 'Clear filters'}).click();

  await page.getByLabel('Kind').selectOption('lesson');
  await expect(page.getByRole('button', {name: /Old deployment lesson/})).toBeVisible();
  await expect(page.getByRole('button', {name: /Keep HTML inert/})).toHaveCount(0);

  await page.getByLabel('Kind').selectOption('');
  await page.locator('#spaces').getByRole('button', {name: /Beta Space/}).click();
  await expect(page.getByRole('button', {name: /Beta security boundary/})).toBeVisible();

  await page.locator('#spaces').getByRole('button', {name: /All memories/}).click();
  await page.getByPlaceholder('Search decisions, constraints, root causes…').fill('inert html');
  await page.getByRole('button', {name: 'Search'}).click();
  await expect(page.getByText('Ranked by relevance')).toBeVisible();
  await expect(page.getByRole('button', {name: /Keep HTML inert/})).toBeVisible();
  await page.getByPlaceholder('Search decisions, constraints, root causes…').fill('');
  await page.getByLabel('Kind').selectOption('lesson');
  await expect(page.getByText('No relevant memories')).toBeVisible();
  const lastRecall = requests.filter(request => request.url.endsWith('/api/v1/recall')).at(-1);
  expect(JSON.parse(lastRecall.body).query).toBe('inert html');
  await page.getByLabel('Kind').selectOption('');
  await page.getByRole('button', {name: 'Browse newest'}).click();
  await expect(page.getByText('Newest first')).toBeVisible();

  await page.getByPlaceholder('Search decisions, constraints, root causes…').fill('inert html');
  await page.getByRole('button', {name: 'Search'}).click();
  await expect(page.getByRole('button', {name: 'Browse newest'})).toBeVisible();

  await page.getByRole('button', {name: 'Lock'}).click();
  await expect(page.getByRole('heading', {name: 'Browse what your agents remember.'})).toBeVisible();
  await expect(page.locator('#memory pre')).toHaveCount(0);
  await expect(page.locator('#topic-values option')).toHaveCount(0);
  await expect(page.getByLabel('Keep this tab unlocked after refresh')).not.toBeChecked();
  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.getByRole('button', {name: 'Browse newest'})).toBeHidden();
  await page.getByRole('button', {name: 'Lock'}).click();
  expect(await page.evaluate(() => ({local: localStorage.length, session: sessionStorage.length}))).toEqual({local: 0, session: 0});
  expect(await page.context().cookies()).toEqual([]);
  expect(page.url()).not.toContain('zm_test_reader');
  expect(requests.every(request => !request.url.includes('zm_test_reader') && !request.body.includes('zm_test_reader'))).toBe(true);

  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByLabel('Keep this tab unlocked after refresh').check();
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.getByRole('heading', {name: 'Memories'})).toBeVisible();
  await expect.poll(() => page.evaluate(() => sessionStorage.getItem('zestmem.console.credential'))).toBe('zm_test_reader');
  await page.reload();
  await expect(page.getByRole('heading', {name: 'Memories'})).toBeVisible();
  await page.getByRole('button', {name: 'Lock'}).click();
  expect(await page.evaluate(() => sessionStorage.getItem('zestmem.console.credential'))).toBeNull();
});

test('Lock aborts stale metadata from a previous credential', async ({page}) => {
  await page.goto('/console/');
  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.locator('#status')).toContainText('matching active');
  await page.getByLabel('Topic').fill('slow');
  await page.waitForTimeout(250);
  await page.getByLabel('Topic').fill('sec');
  await expect(page.locator('#topic-meta')).toHaveText('1 of 1 matching values shown.');
  await page.waitForTimeout(800);
  await expect(page.locator('#topic-meta')).toHaveText('1 of 1 matching values shown.');
  await page.getByLabel('Topic').fill('slow');
  await page.waitForTimeout(250);
  await page.getByRole('button', {name: 'Lock'}).click();
  await page.getByLabel('Bearer credential').fill('zm_test_second');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.locator('#topic-meta')).toHaveText('2 of 2 matching values shown.');
  await page.waitForTimeout(900);
  await expect(page.locator('#topic-meta')).toHaveText('2 of 2 matching values shown.');
  await expect(page.locator('#topic-values option')).toHaveCount(2);
});
