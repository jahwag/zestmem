const {defineConfig, devices} = require('@playwright/test');

module.exports = defineConfig({
  testDir: './internal/console/e2e',
  timeout: 60_000,
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: process.env.CI ? 2 : 0,
  reporter: process.env.CI ? 'github' : 'list',
  use: {
    baseURL: 'http://127.0.0.1:4173',
    trace: 'retain-on-failure'
  },
  projects: [
    {name: 'chromium', testIgnore: '**/mobile.spec.js', use: {...devices['Desktop Chrome']}},
    {name: 'firefox', testIgnore: '**/mobile.spec.js', use: {...devices['Desktop Firefox']}},
    {name: 'webkit', testIgnore: '**/mobile.spec.js', use: {...devices['Desktop Safari']}},
    {name: 'mobile-chromium', testMatch: '**/mobile.spec.js', use: {...devices['Desktop Chrome'], viewport: {width: 390, height: 844}}},
    {name: 'mobile-webkit', testMatch: '**/mobile.spec.js', use: {...devices['iPhone 15']}},
  ],
  webServer: {
    command: 'go run ./internal/console/e2e/server',
    url: 'http://127.0.0.1:4173/console/',
    reuseExistingServer: !process.env.CI,
    timeout: 30_000
  }
});
