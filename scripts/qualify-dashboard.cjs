'use strict';
// Invoked by TestBrowserDashboard against its fake Kubernetes client, never a live deployment.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE_PATH || 'playwright');
(async () => {
  const browser = await chromium.launch({executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium', headless: true});
  try {
    const origin = process.env.APC_TEST_WEB_ORIGIN;
    const context = await browser.newContext({ignoreHTTPSErrors: true, httpCredentials: {username: 'operator', password: process.env.APC_TEST_WEB_PASSWORD}, viewport: {width: 1100, height: 850}});
    const page = await context.newPage();
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    page.on('console', entry => { if (entry.type() === 'error' && /Content Security Policy/.test(entry.text())) errors.push(entry.text()); });
    const noOverflow = async name => { if (await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)) throw new Error(name + ' overflows the viewport'); };
    const shot = async name => { if (process.env.APC_TEST_SCREENSHOT_DIR) await page.screenshot({path: process.env.APC_TEST_SCREENSHOT_DIR + '/' + name + '.png', fullPage: true}); };
    await page.goto(origin);
    await page.getByRole('heading', {name: 'Sandboxes', exact: true}).waitFor();
    const summary = page.getByTestId('egress-summary');
    // Open sandboxes sort first and the panel reports them.
    await summary.getByText('1 of 2 open to the public web.').waitFor();
    await page.locator('tr[data-sandbox="orca"]').getByText(/^Open until/).waitFor();
    await shot('home-open');
    // Lock everything instantly.
    await page.getByRole('button', {name: 'Lock all', exact: true}).click();
    await summary.getByText('All 2 sandboxes are locked to approved requests.').waitFor();
    await page.getByText('Web access locked on 2 sandboxes').waitFor();
    // Open one sandbox from its row.
    const pilot = page.locator('tr[data-sandbox="shared-pilot"]');
    await pilot.getByRole('button', {name: 'Open web access for shared-pilot'}).click();
    await pilot.getByText(/^Open until/).waitFor();
    await pilot.getByRole('button', {name: 'Lock web access for shared-pilot'}).waitFor();
    // Open everything for four hours.
    await page.getByRole('radio', {name: 'Open for 4 hours'}).click();
    await page.getByRole('button', {name: 'Open all', exact: true}).click();
    await summary.getByText('2 of 2 open to the public web.').waitFor();
    await pilot.getByText('awaiting review').waitFor();
    await shot('home');
    // Console projects keep fixed rules; attention is surfaced.
    const blocked = page.locator('tr[data-project="blocked-project"]');
    await blocked.getByText('Needs attention', {exact: true}).waitFor();
    await blocked.getByRole('link', {name: 'blocked-project'}).click();
    await page.getByText('Preparation needs attention', {exact: true}).waitFor();
    await page.getByText(/require at least 1 approval \(currently 0\)/).waitFor();
    await page.getByRole('link', {name: 'Sandboxes', exact: true}).first().click();
    await page.locator('tr[data-project="paused-project"]').getByRole('link', {name: 'paused-project'}).click();
    await page.getByRole('tab', {name: 'Access'}).click();
    await page.getByRole('button', {name: '+ Add destination', exact: true}).click();
    await page.getByLabel('Hostname').fill('api.example.com');
    await page.getByRole('group', {name: 'Methods'}).getByText('HEAD', {exact: true}).click();
    await page.getByLabel('Path prefixes, one per line').fill('/v1');
    await page.getByRole('button', {name: 'Save access', exact: true}).click();
    await page.getByText('paused-project: access update requested. The project remains paused.', {exact: true}).waitFor();
    await page.getByRole('tab', {name: /^Repositories/}).click();
    await page.getByLabel('Directory name', {exact: true}).fill('enrolled-extra');
    await page.getByLabel('Repository host', {exact: true}).selectOption('forgejo');
    await page.getByLabel('Repository, owner/name', {exact: true}).fill('steve/extra');
    await page.getByRole('button', {name: 'Add repository', exact: true}).click();
    await page.getByText('/workspace/projects/enrolled-extra', {exact: true}).waitFor();
    await page.goto(origin + '/');
    await page.getByRole('button', {name: 'New sandbox'}).first().click();
    await page.getByRole('heading', {name: 'New sandbox', exact: true}).waitFor();
    await page.waitForFunction(() => document.querySelector('#name')?.value === 'repo');
    await page.getByLabel('Sandbox name').fill('browser-project');
    await page.getByLabel('Repository', {exact: true}).selectOption('steve/repo');
    await page.getByRole('button', {name: 'Create sandbox', exact: true}).click();
    await page.getByRole('heading', {name: 'browser-project', exact: true}).waitFor();
    const header = page.locator('header.detail-head[data-project="browser-project"]');
    await header.getByRole('button', {name: 'Pause', exact: true}).click();
    await header.getByRole('button', {name: 'Resume', exact: true}).click();
    await header.getByRole('button', {name: 'Pause', exact: true}).waitFor();
    page.on('dialog', dialog => dialog.accept());
    await page.getByRole('button', {name: 'Remove sandbox', exact: true}).click();
    await page.getByText('browser-project: removal requested', {exact: true}).waitFor();
    await page.getByRole('tab', {name: 'History'}).click();
    await page.getByRole('list', {name: 'Access history'}).waitFor();
    await page.waitForFunction(() => !document.querySelector('[aria-label="Access history"]').textContent.includes('Loading history'));
    await shot('detail');
    await page.getByRole('link', {name: 'Sandboxes', exact: true}).first().click();
    // Approvals and other detail stay on the mounted sandbox-control page.
    await page.locator('tr[data-sandbox="shared-pilot"]').getByRole('link', {name: 'shared-pilot'}).click();
    await page.getByRole('heading', {name: 'Existing sandbox access', exact: true}).waitFor();
    for (const name of ['shared-pilot', 'codex-a', 'grok-a', 'cursor-a']) await page.getByRole('link', {name, exact: true}).waitFor();
    await page.getByRole('link', {name: 'shared-pilot', exact: true}).click();
    await page.getByRole('button', {name: 'Save existing access', exact: true}).click();
    await page.waitForURL(/\/sandboxes\/\?workspace=shared-pilot&tab=access/);
    await page.getByRole('link', {name: 'Sandboxes', exact: true}).click();
    await page.getByRole('heading', {name: 'Sandboxes', exact: true}).waitFor();
    await page.setViewportSize({width: 390, height: 844});
    await page.getByRole('button', {name: 'Lock all', exact: true}).waitFor();
    await noOverflow('Dashboard');
    await shot('home-mobile');
    await page.goto(origin + '/#/projects/paused-project/access');
    await page.getByRole('heading', {name: 'paused-project', exact: true}).waitFor();
    await noOverflow('Sandbox detail');
    await page.emulateMedia({colorScheme: 'dark'});
    await page.setViewportSize({width: 1100, height: 850});
    await page.goto(origin + '/');
    await page.getByTestId('egress-summary').waitFor();
    await shot('home-dark');
    if (errors.length) throw new Error(errors.join('\n'));
    console.log('Browser qualification passed: instant open and lock for all and single sandboxes, unified sandbox list, visible preparation blocker, paused egress editing, create/pause/resume/remove, audit and existing sandbox controls in one authenticated app.');
  } finally { await browser.close(); }
})().catch(error => {console.error(error.message);process.exit(1);});
