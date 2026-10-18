'use strict';
// Invoked by TestBrowserRuns against fixture run records, never a live deployment.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE_PATH || 'playwright');
(async () => {
  const browser = await chromium.launch({executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium', headless: true});
  try {
    const origin = process.env.APC_TEST_WEB_ORIGIN;
    const errors = [];
    for (const [width, scheme] of [[1100, 'light'], [390, 'dark']]) {
      const context = await browser.newContext({ignoreHTTPSErrors: true, httpCredentials: {username: 'operator', password: process.env.APC_TEST_WEB_PASSWORD}, viewport: {width, height: 850}, colorScheme: scheme});
      const page = await context.newPage();
      page.on('pageerror', error => errors.push(error.message));
      page.on('console', entry => { if (entry.type() === 'error' && /Content Security Policy/.test(entry.text())) errors.push(entry.text()); });
      await page.goto(origin);
      await page.getByRole('link', {name: 'Runs', exact: true}).click();
      await page.getByRole('heading', {name: 'Runs', exact: true}).waitFor();
      await page.getByRole('link', {name: 'run_fe7f0fa105c5'}).click();
      await page.getByText('2 of 2 tasks failed', {exact: true}).waitFor();
      await page.getByText('agent_not_in_foreground', {exact: true}).waitFor();
      await page.goto(origin + '/#/runs/run_sample');
      const agent = page.locator('.factory-agent-section');
      await agent.getByText('Written by claude · not verified by the collector').waitFor();
      if (await agent.getByRole('button').count()) throw new Error('Agent report rendered a control');
      await page.getByRole('button', {name: 'Drop the newline', exact: true}).click();
      const command = await page.locator('.factory-confirm code').textContent();
      if (command !== "kubectl -n agent-workspace-example exec orca -- orca orchestration gate-resolve --id 'gate_cli_output' --resolution 'Drop the newline'") throw new Error('Unexpected gate command: ' + command);
      if (await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)) throw new Error(`Run report overflows at ${width}px`);
      await page.goto(origin + '/#/runs/run_forged');
      await page.getByText('run_forged: the run record failed validation and was not displayed').waitFor();
      await context.close();
    }
    if (errors.length) throw new Error(errors.join('\n'));
    console.log('Browser qualification passed: run list, failed and healthy run reports, labelled agent reports without controls, gate confirmation command, schema rejection, phone width, dark mode and CSP.');
  } finally { await browser.close(); }
})().catch(error => {console.error(error.message);process.exit(1);});
