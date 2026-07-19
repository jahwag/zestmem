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
  await expect(page.locator('pre.body')).toContainText('<img src=x onerror=window.hacked=true>');
  expect(await page.evaluate(() => window.hacked)).toBeUndefined();
  await expect(page.locator('#memory img')).toHaveCount(0);

  await page.getByRole('button', {name: 'Load more'}).click();
  await expect(page.getByRole('button', {name: /Old deployment lesson/})).toBeVisible();

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
  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.getByRole('button', {name: 'Browse newest'})).toBeHidden();
  await page.getByRole('button', {name: 'Lock'}).click();
  expect(await page.evaluate(() => ({local: localStorage.length, session: sessionStorage.length}))).toEqual({local: 0, session: 0});
  expect(await page.context().cookies()).toEqual([]);
  expect(page.url()).not.toContain('zm_test_reader');
  expect(requests.every(request => !request.url.includes('zm_test_reader') && !request.body.includes('zm_test_reader'))).toBe(true);
});
