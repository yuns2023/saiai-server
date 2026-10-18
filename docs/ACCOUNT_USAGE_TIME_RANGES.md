# Account and usage time ranges

Admin usage records and account usage statistics share a date/time picker with
minute precision and an explicit browser timezone. Last 24 hours is a rolling
24-hour interval; today, recent calendar days and this month end at the current
minute. Yesterday and last month end at the next period's midnight. Custom
edits retain their exact times and are not inferred back into a preset.

All precise ranges include the start and exclude the end. Usage logs, totals,
model distributions, trends and exports use the selected range. Complete
midnight-to-midnight usage ranges retain the calendar-date query path so the
existing calendar-only cleanup workflow remains available.

`GET /api/v1/admin/accounts/:id/stats` accepts paired RFC3339 `start_time` and
`end_time`, plus an IANA `timezone` for daily buckets. Explicit ranges may not
exceed 90 elapsed days. The existing `days` parameter remains supported (1–90,
default 30); precise timestamps take precedence. Invalid ranges return HTTP 400.

Account statistics default to the last 30 calendar days through the current
minute. Totals, peaks, distribution charts and performance follow the selected
range. The today overview is explicitly the intersection of today and that
range. Daily averages divide by days with usage; empty ranges report zero active
days and zero averages. The active-day denominator counts calendar days touched
by the half-open range, respecting timezone/DST boundaries.

Account management displays all groups and their full names, wrapping long
labels and expanding row height. Compact group badges remain available to other
callers. Identity and control columns are bounded while groups and usage columns
retain room for their content; horizontal scrolling remains available.
