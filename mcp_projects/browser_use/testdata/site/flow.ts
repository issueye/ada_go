const page = await browser.active();
const title: string = await page.title();
const info: { title: string; length: number } = { title, length: title.length };
return info;
