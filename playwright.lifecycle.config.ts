import {defineConfig} from '@playwright/test';
export default defineConfig({testDir:'./tests/browser',testMatch:'print-preview-lifecycle.spec.ts',workers:1,retries:0,use:{baseURL:'http://127.0.0.1:18095',browserName:'chromium',trace:'retain-on-failure'},reporter:[['list'],['html',{outputFolder:'playwright-report',open:'never'}]]});
