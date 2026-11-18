import { defineConfig } from '@playwright/test';

// Runs against a live Paxve (default https://paxve.com). Each run signs up a throwaway trial tenant.
export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 180_000,
  expect: { timeout: 15_000 },
  reporter: [['list']],
  use: {
    baseURL: process.env.E2E_BASE_URL || 'https://paxve.com',
    viewport: { width: 1366, height: 860 },
    actionTimeout: 15_000,
    navigationTimeout: 30_000,
    trace: 'off',
  },
});
