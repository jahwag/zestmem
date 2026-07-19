const {test, expect} = require('@playwright/test');

test('mobile Space drawer and detail navigation remain usable', async ({page}) => {
  await page.goto('/console/');
  await page.getByLabel('Bearer credential').fill('zm_test_reader');
  await page.getByRole('button', {name: 'Open memory browser'}).click();
  await expect(page.getByRole('heading', {name: 'Memories', exact: true})).toBeVisible();

  const spaces = page.getByRole('button', {name: 'Spaces', exact: true});
  await spaces.click();
  await expect(spaces).toHaveAttribute('aria-expanded', 'true');
  await expect(page.getByRole('button', {name: 'Close spaces', exact: true})).toBeFocused();
  await page.keyboard.press('Escape');
  await expect(spaces).toBeFocused();
  await expect(spaces).toHaveAttribute('aria-expanded', 'false');

  await spaces.click();
  await expect(page.locator('#spaces').getByRole('button', {name: /Alpha Space/})).toBeVisible();
  await page.locator('#spaces').getByRole('button', {name: /Alpha Space/}).click();
  await expect(spaces).toHaveAttribute('aria-expanded', 'false');
  const lastMemory = page.getByRole('button', {name: /Alpha memory 20/});
  await expect(lastMemory).toBeVisible();
  await lastMemory.click();
  await expect(page.getByRole('heading', {name: 'Alpha memory 20'})).toBeVisible();
  await expect.poll(async () => page.locator('#memory').evaluate(node => {
    const box = node.getBoundingClientRect();
    return box.top >= 0 && box.top < window.innerHeight;
  })).toBe(true);

  await spaces.click();
  await page.setViewportSize({width: 1200, height: 844});
  await expect(spaces).toBeHidden();
  await expect.poll(() => page.evaluate(() => ({
      open: document.body.classList.contains('sidebar-open'),
      content: document.querySelector('.content').inert,
      brand: document.querySelector('.brand').inert,
      connection: document.getElementById('connection').inert,
    }))).toEqual({open: false, content: false, brand: false, connection: false});
});
