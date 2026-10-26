'use strict';
// Invoked by TestBrowserDownloads against its fake project sandbox, never a live deployment.
const {chromium} = require(process.env.PLAYWRIGHT_MODULE_PATH || 'playwright');
(async () => {
  const browser = await chromium.launch({executablePath: process.env.CHROMIUM_PATH || '/usr/bin/chromium', headless: true});
  try {
    const origin = process.env.APC_TEST_WEB_ORIGIN;
    const context = await browser.newContext({ignoreHTTPSErrors: true, viewport: {width: 1100, height: 900}});
    await context.addCookies([{name: '__Host-apc-session', value: process.env.APC_TEST_SESSION, url: origin, secure: true, httpOnly: true, sameSite: 'Lax'}]);
    const page = await context.newPage();
    const errors = []; page.on('pageerror', error => errors.push(error.message));
    page.on('console', entry => { if (entry.type() === 'error' && /Content Security Policy/.test(entry.text())) errors.push(entry.text()); });
    const shot = async name => { if (process.env.APC_TEST_SCREENSHOT_DIR) await page.screenshot({path: process.env.APC_TEST_SCREENSHOT_DIR + '/' + name + '.png', fullPage: true}); };
    const noOverflow = async name => { if (await page.evaluate(() => document.documentElement.scrollWidth > window.innerWidth)) throw new Error(name + ' overflows the viewport'); };
    const confirm = page.locator('dialog.confirm-dialog');

    await page.goto(origin + '/#/projects/project/downloads');
    await page.getByRole('heading', {name: 'Downloads', exact: true}).waitFor();
    // The header offers the section from other pages too.
    const url = page.getByLabel('URL', {exact: true});
    await url.fill('http://github.com/o/r/releases/download/v1/tool.tgz');
    await page.getByText('Only https URLs are downloaded; http is refused.').waitFor();
    if (await page.getByRole('button', {name: 'Check', exact: true}).isEnabled()) throw new Error('Check enabled for an http URL');

    // 1. Check: the chain shows the redirect, the new host and masked query strings.
    await url.fill('https://github.com/o/r/releases/download/v1/tool.tgz');
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    const chain = page.getByTestId('download-chain');
    await page.locator('[data-testid="download-chain"][data-state="ready"]').waitFor();
    await chain.getByText('Redirect chain (1 redirect)').waitFor();
    await chain.getByText('new host: objects.example.com').waitFor();
    await chain.getByText('302 redirect').waitFor();
    await chain.getByText('200 OK').waitFor();
    if (await chain.getByText(/secret-1/).count()) throw new Error('query string shown before Show was ticked');
    await chain.getByText(/token=•••/).waitFor();
    await chain.getByLabel('Show query strings').check();
    await chain.getByText(/token=secret-1/).waitFor();
    await chain.getByLabel('Show query strings').uncheck();
    await page.getByText('This download crosses hosts: github.com → objects.example.com.').waitFor();
    const dest = page.getByLabel('Save as');
    if (await dest.inputValue() !== '~/downloads/tool.tgz') throw new Error('destination not prefilled: ' + await dest.inputValue());
    await dest.fill('/etc/cron.d/x');
    await page.getByText('Save under ~/ (the sandbox home) or /workspace/.').waitFor();
    await dest.fill('~/downloads/tool.tgz');
    await shot('downloads-chain');

    // 2. Approve: the dialog lists the exact URLs; the fresh signature stops the download.
    await page.getByRole('button', {name: 'Download', exact: true}).click();
    await confirm.getByText('new host').first().waitFor();
    await confirm.getByText(/not github.com/).waitFor();
    await shot('downloads-confirm');
    await confirm.getByRole('button', {name: 'Download', exact: true}).click();
    await page.locator('[data-download][data-state="changed"]').waitFor();
    await page.getByTestId('chain-change').getByText(/Same host and path, different query string/).waitFor();
    await shot('downloads-changed');

    // 3. One-click re-approval of the new chain, accepting refreshed query strings.
    await page.getByRole('button', {name: 'Check the new chain'}).click();
    await page.locator('[data-testid="download-chain"][data-state="ready"]').waitFor();
    if (!await page.getByLabel(/Accept a changed query string/).isChecked()) throw new Error('re-approval did not accept query-only changes');
    await page.getByRole('button', {name: 'Approve new chain and download'}).click();
    await confirm.getByRole('button', {name: 'Download', exact: true}).click();
    await page.locator('[data-download][data-state="done"]').waitFor();
    await page.getByText('SHA-256').waitFor();
    await page.getByText(/10 B \(10 bytes\)/).waitFor();
    await page.getByRole('list', {name: 'Recent downloads'}).getByText('done · ~/downloads/tool.tgz').waitFor();
    await shot('downloads-done');

    // 4. History has the audit records; query strings stay hashed there.
    await page.goto(origin + '/#/projects/project/history');
    await page.getByText('download-finished').first().waitFor();
    if (await page.getByText(/secret-/).count()) throw new Error('query string in History');
    await shot('downloads-history');

    // 5. Upload from this computer: Files -> Upload tab, pick a file, confirm,
    // see progress and the sandbox's SHA-256 next to the browser's.
    await page.goto(origin + '/#/projects/project/downloads');
    await page.getByRole('tab', {name: 'Upload from this computer'}).click();
    await page.getByRole('heading', {name: 'Upload from this computer', exact: true}).waitFor();
    const bytes = Buffer.alloc(17 << 20);
    for (let i = 0; i < bytes.length; i++) bytes[i] = i % 251;
    const want = require('crypto').createHash('sha256').update(bytes).digest('hex');
    await page.locator('#up-file').setInputFiles({name: 'report.bin', mimeType: 'application/octet-stream', buffer: bytes});
    const saveAs = page.getByLabel('Save as');
    if (await saveAs.inputValue() !== '~/uploads/report.bin') throw new Error('upload destination not prefilled: ' + await saveAs.inputValue());
    await saveAs.fill('~/../x');
    await page.getByText('The path must not contain empty, . or .. parts.').waitFor();
    await saveAs.fill('~/uploads/report.bin');
    await shot('uploads-ready');
    await page.getByRole('button', {name: 'Upload', exact: true}).click();
    await confirm.getByText(/no cookies or passwords for other sites are sent/).waitFor();
    await shot('uploads-confirm');
    await confirm.getByRole('button', {name: 'Upload', exact: true}).click();
    await page.locator('[data-upload][data-state="done"]').waitFor({timeout: 60000});
    if (await page.getByTestId('upload-sandbox-sha').innerText() !== want) throw new Error('sandbox SHA-256 differs from the file');
    await page.getByText('same file').waitFor();
    await page.getByRole('list', {name: 'Recent uploads'}).getByText('done · ~/uploads/report.bin').waitFor();
    await shot('uploads-done');
    // The same destination again without overwrite: the sandbox refuses before any byte is sent.
    await page.getByRole('button', {name: 'Upload', exact: true}).click();
    await confirm.getByRole('button', {name: 'Upload', exact: true}).click();
    await page.getByText('a file already exists at the destination; tick overwrite to replace it', {exact: true}).waitFor();
    await page.goto(origin + '/#/projects/project/history');
    await page.getByText('upload-finished').first().waitFor();

    // Narrow screens: the chain wraps instead of overflowing.
    await page.setViewportSize({width: 390, height: 844});
    await page.goto(origin + '/#/projects/project/downloads');
    await page.getByRole('heading', {name: 'Downloads', exact: true}).waitFor();
    await url.fill('https://github.com/o/r/releases/download/v1/tool.tgz');
    await page.getByRole('button', {name: 'Check', exact: true}).click();
    await page.locator('[data-testid="download-chain"][data-state="ready"]').waitFor();
    await shot('downloads-mobile');
    await noOverflow('downloads on a phone');
    if (errors.length) throw new Error('page errors: ' + errors.join('; '));
    console.log('downloads qualified');
  } finally {
    await browser.close();
  }
})().catch(error => { console.error(error); process.exit(1); });
