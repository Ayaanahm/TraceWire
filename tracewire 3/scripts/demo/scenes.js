// Records each scene in its own context, logging how long the page took to
// be ready so the loading can be trimmed. node scenes.js <outdir> <cardsdir>
const { chromium } = require('playwright');
const fs = require('fs');
const [out, cards] = process.argv.slice(2);
const sleep = ms => new Promise(r => setTimeout(r, ms));
const api = (method, path, body) => fetch('http://localhost:8080' + path, { method, body: body && JSON.stringify(body) });

const CAP = `#cap{position:fixed;left:50%;bottom:44px;transform:translateX(-50%);z-index:99999;background:#14263A;color:#F4F6F8;
  font:500 26px/1.3 Barlow,system-ui,sans-serif;padding:12px 24px;border-radius:6px;width:max-content;max-width:1100px;text-align:center;box-shadow:0 3px 0 #D99A00}`;
const caption = (page, text) => page.evaluate(([css, t]) => {
  let el = document.getElementById('cap');
  if (!el) { const s = document.createElement('style'); s.textContent = css; document.head.appendChild(s);
    el = document.createElement('div'); el.id = 'cap'; document.body.appendChild(el); }
  el.textContent = t; el.style.display = t ? 'block' : 'none';
}, [CAP, text]);
const cursor = page => page.evaluate(() => {
  const c = document.createElement('div');
  c.style.cssText = 'position:fixed;width:20px;height:20px;border-radius:50%;background:rgba(217,154,0,.9);border:2px solid #14263A;z-index:99998;pointer-events:none;transform:translate(-50%,-50%);left:-50px;top:-50px';
  document.body.appendChild(c);
  addEventListener('mousemove', e => { c.style.left = e.clientX + 'px'; c.style.top = e.clientY + 'px'; }, true);
});
async function drag(page, sel, value) {
  const el = await page.$(sel), box = await el.boundingBox();
  const [min, max, cur] = await el.evaluate(e => [+e.min, +e.max, +e.value]);
  const x = v => box.x + 9 + (box.width - 18) * (v - min) / (max - min), y = box.y + box.height / 2;
  await page.mouse.move(x(cur), y, { steps: 10 }); await page.mouse.down();
  await page.mouse.move(x(value), y, { steps: 22 }); await page.mouse.up();
  await el.evaluate((e, v) => { e.value = v; e.dispatchEvent(new Event('input')); }, value);
}
async function moveTo(page, sel) {
  const b = await (await page.$(sel)).boundingBox();
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 24 });
}

const log = [];
async function scene(browser, name, url, ready, body) {
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 }, locale: 'en-US',
    recordVideo: { dir: `${out}/${name}`, size: { width: 1280, height: 720 } } });
  const page = await ctx.newPage(); const t0 = Date.now();
  await page.goto(url); await ready(page); await cursor(page);
  const tr = Date.now();
  const extra = await body(page);
  const tEnd = Date.now();
  await ctx.close();
  log.push({ name, start: (tr - t0) / 1000, dur: (tEnd - tr) / 1000 });
  return extra;
}
const settle = ms => async p => { await p.waitForLoadState('networkidle').catch(() => {}); await sleep(ms); };

(async () => {
  await api('DELETE', '/console/api/faults');
  await api('PUT', '/console/api/traffic', { rps: 80 });
  await sleep(30000);
  const browser = await chromium.launch();

  await scene(browser, '1-hook', `file://${cards}/card.html?k=hook`, settle(800), async () => sleep(3500));

  const href = await scene(browser, '2-console', 'http://localhost:8080/', settle(1500), async p => {
    await caption(p, 'Three Go services, 80 orders a second. Every dot is a real request.');
    await p.mouse.move(640, 200, { steps: 20 }); await sleep(3500);
    await caption(p, 'Break inventory: 25% of its requests now stall for 320 ms');
    await drag(p, '#f-slow', 25); await drag(p, '#f-delay', 320); await drag(p, '#f-err', 4);
    await sleep(2500);
    await caption(p, 'p99 crosses the 250 ms target, and inventory is flagged slow');
    await p.mouse.move(1000, 160, { steps: 25 });
    await sleep(4500);
    await p.evaluate(() => scrollTo({ top: document.body.scrollHeight, behavior: 'smooth' }));
    await caption(p, 'The slowest request: nearly all of its time is spent in inventory');
    await sleep(1200); await moveTo(p, '#fall-link'); await sleep(3000);
    return p.$eval('#fall-link', a => a.href);
  });

  await scene(browser, '3-jaeger', href, settle(1500), async p => {
    await caption(p, 'The same request as an OpenTelemetry trace across all three services');
    await p.mouse.move(400, 300, { steps: 20 }); await sleep(4500);
  });

  await scene(browser, '4-grafana', 'http://localhost:3000/d/tracewire-overview?orgId=1&from=now-5m&to=now&refresh=5s&kiosk',
    settle(3000), async p => {
      await caption(p, 'Prometheus and Grafana track the SLO and page on error-budget burn');
      await sleep(4500);
    });

  await scene(browser, '5-fix', 'http://localhost:8080/', settle(1500), async p => {
    await caption(p, 'One click restores inventory');
    await moveTo(p, '#restore'); await sleep(500);
    await p.mouse.down(); await p.mouse.up(); await p.click('#restore');
    await sleep(2500);
    await caption(p, 'and latency drops back under the target');
    await sleep(4500);
  });

  await scene(browser, '6-proof', `file://${cards}/card.html?k=proof`, settle(800), async () => sleep(5500));
  await scene(browser, '7-end', `file://${cards}/card.html?k=end`, settle(800), async () => sleep(4500));

  await browser.close();
  await api('DELETE', '/console/api/faults');
  fs.writeFileSync(`${out}/scenes.json`, JSON.stringify(log, null, 1));
  console.log(log);
})();
