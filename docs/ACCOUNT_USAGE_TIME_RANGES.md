# Account and usage time ranges

Admin usage records and account usage statistics share a date/time picker with
minute precision and an explicit browser timezone. Last 24 hours is a rolling
24-hour interval ending at the next whole minute, so the current minute is
included. Today, recent calendar days and this month are whole calendar days
that run to the next local midnight, so they are never empty or inverted in the
first minute after midnight and rows written after the range was picked still
match. On admin usage records, applying filters or refreshing re-evaluates
these presets against the clock. Yesterday and last month end at the next
period's midnight. Custom edits retain their exact times and are not inferred
back into a preset.

Default ranges: admin usage records, the admin dashboard, the user dashboard
and the user usage records page open on today. On both usage records pages,
reset returns to today and explicit dates or times in the URL take precedence.
Both dashboards chart today in hourly buckets; choosing a range longer than one
day switches the trend to daily buckets. Ops monitoring keeps its rolling
windows and account statistics keep their 30-day default.

All precise ranges include the start and exclude the end. Usage logs, totals,
model distributions, trends and exports use the selected range. Complete
midnight-to-midnight usage ranges retain the calendar-date query path so the
existing calendar-only cleanup workflow remains available.

`GET /api/v1/admin/accounts/:id/stats` accepts paired RFC3339 `start_time` and
`end_time`, plus an IANA `timezone` for daily buckets. Explicit ranges may not
exceed 90 elapsed days. The existing `days` parameter remains supported (1–90,
default 30); precise timestamps take precedence. Invalid ranges return HTTP 400.

Account statistics default to the last 30 calendar days through the end of
today. Totals, peaks, distribution charts and performance follow the selected
range. The today overview is explicitly the intersection of today and that
range. Daily averages divide by days with usage; empty ranges report zero active
days and zero averages. `ActualDaysUsed` is the number of daily buckets with
usage and supplies the average denominator. `Days` separately counts calendar
days touched by the half-open range, respecting timezone/DST boundaries.

Account cost display: the account list today cell, the account statistics
modal and the admin usage records summary show account billed
(`total_cost × account_rate_multiplier`) and no separate standard/base cost,
because the two are equal at a 1x account multiplier. Hosted rows carry an
account multiplier of 0, so their reference usage (`SUM(metered_cost)`) is shown
as a separate "hosted usage ≈$" line whenever hosted requests exist, also at
$0. Admin `GET /api/v1/admin/usage/stats` always returns `total_account_cost`
and returns `total_metered_cost` when hosted rows match; account statistics
return `total_hosted_requests` and `total_hosted_cost`. In the account
statistics modal the model and endpoint tables label `actual_cost` as account
billed, and row drill-downs are limited to that account and range.

Account management displays all groups and their full names, wrapping long
labels and expanding row height. Compact group badges remain available to other
callers. Identity and control columns are bounded while groups and usage columns
retain room for their content; horizontal scrolling remains available.
