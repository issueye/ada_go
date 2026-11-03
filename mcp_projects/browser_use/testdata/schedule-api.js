// Read the schedule straight from the captured mySchedule XHR.
// Usage: browser-use run testdata/schedule-api.js
const page = await browser.active();
const recent = await page.requests({
  url_contains: "/scheduleStat/mySchedule",
  include_body: true,
  limit: 5,
});
const entry = recent.entries[recent.entries.length - 1];
if (!entry) throw new Error("no mySchedule request captured yet");
const body = JSON.parse(entry.res_body);
const data = body.data || {};
return {
  source: { id: entry.id, url: entry.url, request: JSON.parse(entry.req_body || "{}") },
  summary: data.summary || {},
  plan_notes: data.planNotes || [],
  days: (data.list || []).map((d) => ({
    date: d.scheduleDate,
    shifts: (d.shiftDetailList || []).map((s) => ({
      name: s.shiftName,
      start: s.workStart,
      end: s.workEnd,
      hours: s.shiftDuration,
      class_num: s.classNum,
      group: s.groupName,
      dept: s.deptName,
    })),
  })),
};
