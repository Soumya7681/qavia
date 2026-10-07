// Print an HTML report to PDF (BE-5.9.3).
//
// Chromium via Playwright, which this image already carries. Two things worth naming:
//
//   - The page is loaded from the filesystem and given no network. `waitUntil: 'load'`
//     rather than `networkidle`, because a document with no network never reaches
//     network idle and would hang until the driver's wall clock killed it.
//   - `printBackground` is on, because the report uses background colours to carry
//     meaning: a status pill printed without its background is a word with no state.

import { chromium } from '@playwright/test';

const [source, output] = process.argv.slice(2);
if (!source || !output) {
  console.error('print.mjs: expected a source HTML path and an output PDF path');
  process.exit(64);
}

const browser = await chromium.launch({
  args: ['--no-sandbox', '--disable-dev-shm-usage'],
});

try {
  const page = await browser.newPage();

  // Nothing external is permitted, but the document itself is loaded from the
  // filesystem, so the predicate has to let file:// through: a blanket pattern blocks
  // the navigation as well as the resources and the page never loads at all.
  await page.route(
    (url) => url.protocol !== 'file:' && url.protocol !== 'data:',
    (route) => route.abort(),
  );

  await page.goto(`file://${source}`, { waitUntil: 'load', timeout: 30_000 });
  await page.pdf({
    path: output,
    format: 'A4',
    printBackground: true,
    margin: { top: '14mm', bottom: '16mm', left: '12mm', right: '12mm' },
    displayHeaderFooter: true,
    headerTemplate: '<div></div>',
    footerTemplate:
      '<div style="width:100%;font-size:9px;color:#5b6070;padding:0 12mm;'
      + 'display:flex;justify-content:space-between">'
      + '<span class="title"></span>'
      + '<span><span class="pageNumber"></span> / <span class="totalPages"></span></span>'
      + '</div>',
  });
} finally {
  await browser.close();
}
