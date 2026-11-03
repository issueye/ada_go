// Extract the visible "排班查询" calendar from the hospital scheduling app.
// Usage: browser-use run testdata/schedule-extract.js
const page = await browser.active();
return await page.evaluate(() => {
  const pad = (n) => String(n).padStart(2, "0");
  const inputs = [...document.querySelectorAll(".yh-range-editor input")];
  const range = {
    start: inputs[0] ? inputs[0].value : "",
    end: inputs[1] ? inputs[1].value : "",
  };
  const year = Number((range.start || "2026").slice(0, 4)) || 2026;

  const summary = {};
  for (const card of document.querySelectorAll(".stat-card")) {
    const label = card.querySelector(".stat-label");
    const lines = (card.innerText || "").split("\n").map((s) => s.trim()).filter(Boolean);
    if (!label || lines.length === 0) continue;
    summary[label.textContent.trim()] = Number(lines[lines.length - 1]);
  }
  summary["排班总时长（小时）"] = summary["排班总时长"] ?? summary["排班总时长（小时）"];
  delete summary["排班总时长"];

  const weekdays = [...document.querySelectorAll(".schedule-calendar .weekday-cell")]
    .map((e) => e.textContent.trim());
  const cells = [...document.querySelectorAll(".schedule-calendar .day-cell")];

  let month = Number((range.start || "2026-11").slice(5, 7)) || 11;
  let prevDay = 0;
  const days = cells.map((cell, i) => {
    const day = Number((cell.querySelector(".day-number__value") || {}).textContent || 0);
    const label = cell.querySelector(".month-label");
    if (label) {
      const m = label.textContent.match(/(\d+)/);
      if (m) month = Number(m[1]);
    } else if (cell.classList.contains("is-other-month")) {
      if (prevDay > 20 && day < 15) month = month === 12 ? 1 : month + 1;
      else if (prevDay < 15 && day > 20) month = month === 1 ? 12 : month - 1;
    }
    prevDay = day;
    const chips = [...cell.querySelectorAll(".shift-chip")].map((c) => {
      const l = c.querySelector(".shift-chip__label");
      const t = c.querySelector(".shift-chip__time");
      return { label: l ? l.textContent.trim() : "", time: t ? t.textContent.trim() : "" };
    });
    const restEl = cell.querySelector(".rest-label");
    const holidayEl = cell.querySelector(".holiday-name");
    const rest = restEl ? restEl.textContent.trim() : "";
    const holiday = holidayEl ? holidayEl.textContent.trim() : "";
    const shift = chips.map((c) => c.label + (c.time ? " " + c.time : "")).join("; ");
    return {
      date: year + "-" + pad(month) + "-" + pad(day),
      weekday: weekdays[i % 7],
      day,
      month,
      other_month: cell.classList.contains("is-other-month"),
      today: cell.classList.contains("is-today"),
      shift,
      shifts: chips,
      rest,
      holiday,
      status: shift || rest || holiday || "",
    };
  });

  return { range, year, summary, weekdays, days };
});
