const page = await browser.active();
await page.goto("http://127.0.0.1:8765/form");
console.log("loaded", await page.title());
await page.fill("#q", "script test", { submit: true });
await page.waitForURL("/done", { timeout: 5000 });
const text = await page.read();
return { title: await page.title(), text };
