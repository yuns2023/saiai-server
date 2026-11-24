-- Subscription usage windows anchored at starts_at.
--
-- Until now the daily/weekly/monthly windows started at midnight of the day
-- of first use and, after ending, restarted at midnight of the day of the
-- next request. The expiry is starts_at + N days, so the monthly window
-- ended up to a day before the expiry and a fresh, full monthly window opened
-- for the last hours of the subscription. The server now lays the windows on
-- a grid anchored at starts_at (starts_at + k * 1/7/30 days), and a renewal
-- of an expired subscription restarts the period (starts_at = renewal time).
--
-- This migration moves the windows of current (unexpired) subscriptions onto
-- that grid once. Usage is kept. It only touches rows that are not already
-- aligned, so running it again changes nothing.
--
-- 1. Subscriptions whose length is not a whole number of 30-day periods (an
--    expired subscription renewed late keeps its old starts_at today; admin
--    adjustments add any number of days) get starts_at moved to the start of
--    the current 30-day period counted back from the expiry, so the last
--    monthly window ends exactly at the expiry. Skipped when that would move
--    starts_at earlier, and for expiries capped at the 2099 maximum.
-- 2. For each window column that is set, not in the future, not yet ended and
--    not on the grid: the old start is midnight-truncated, so the intended
--    grid point lies less than 24 h after it. The new start is the latest
--    grid point no later than (old start + 24 h) and no later than now.
--    When there is none, the window began in an earlier period (the old start
--    is more than 24 h before starts_at, i.e. a late renewal moved in step 1):
--    the window restarts at starts_at with zero usage, as a renewal now does.
--    A window that has already ended is left alone: the next request resets
--    it onto the grid.
--
-- Small data (saiai.top: 10 current subscriptions, other sites none). The
-- previous binary keeps working against these values (it only compares
-- window start + fixed length with now), but would write midnight starts
-- again on its next reset, so the release must stop the previous gateway
-- before this binary starts (the normal cutover).

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

-- Step 1: re-anchor misaligned subscription lengths.
UPDATE user_subscriptions us
SET starts_at = r.new_starts_at,
    updated_at = NOW()
FROM (
    SELECT id,
           expires_at - make_interval(secs =>
               ceil(extract(epoch FROM (expires_at - NOW())) / 2592000) * 2592000
           ) AS new_starts_at
    FROM user_subscriptions
    WHERE deleted_at IS NULL
      AND expires_at > NOW()
      AND expires_at < TIMESTAMPTZ '2099-12-31 00:00:00+00'
      AND abs(
            extract(epoch FROM (expires_at - starts_at))
            - round(extract(epoch FROM (expires_at - starts_at)) / 2592000) * 2592000
          ) > 1
) r
WHERE us.id = r.id
  AND r.new_starts_at > us.starts_at;

-- Step 2: snap each window start onto the starts_at grid.
UPDATE user_subscriptions us
SET daily_window_start = COALESCE(c.daily_new, us.daily_window_start),
    weekly_window_start = COALESCE(c.weekly_new, us.weekly_window_start),
    monthly_window_start = COALESCE(c.monthly_new, us.monthly_window_start),
    daily_usage_usd = CASE WHEN c.daily_restart THEN 0 ELSE us.daily_usage_usd END,
    weekly_usage_usd = CASE WHEN c.weekly_restart THEN 0 ELSE us.weekly_usage_usd END,
    monthly_usage_usd = CASE WHEN c.monthly_restart THEN 0 ELSE us.monthly_usage_usd END,
    updated_at = NOW()
FROM (
    SELECT s.id,
           CASE
               WHEN s.daily_window_start IS NULL
                 OR s.daily_window_start > NOW()
                 OR s.daily_window_start + make_interval(secs => 86400) <= NOW()
                 OR abs(mod(extract(epoch FROM (s.daily_window_start - s.starts_at)), 86400)) < 0.001
               THEN NULL
               WHEN LEAST(s.daily_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
               THEN s.starts_at
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.daily_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 86400) * 86400)
           END AS daily_new,
           (s.daily_window_start IS NOT NULL AND s.daily_window_start <= NOW()
               AND s.daily_window_start + make_interval(secs => 86400) > NOW()
               AND LEAST(s.daily_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at) AS daily_restart,
           CASE
               WHEN s.weekly_window_start IS NULL
                 OR s.weekly_window_start > NOW()
                 OR s.weekly_window_start + make_interval(secs => 604800) <= NOW()
                 OR abs(mod(extract(epoch FROM (s.weekly_window_start - s.starts_at)), 604800)) < 0.001
               THEN NULL
               WHEN LEAST(s.weekly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
               THEN s.starts_at
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.weekly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 604800) * 604800)
           END AS weekly_new,
           (s.weekly_window_start IS NOT NULL AND s.weekly_window_start <= NOW()
               AND s.weekly_window_start + make_interval(secs => 604800) > NOW()
               AND LEAST(s.weekly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at) AS weekly_restart,
           CASE
               WHEN s.monthly_window_start IS NULL
                 OR s.monthly_window_start > NOW()
                 OR s.monthly_window_start + make_interval(secs => 2592000) <= NOW()
                 OR abs(mod(extract(epoch FROM (s.monthly_window_start - s.starts_at)), 2592000)) < 0.001
               THEN NULL
               WHEN LEAST(s.monthly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
               THEN s.starts_at
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.monthly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 2592000) * 2592000)
           END AS monthly_new,
           (s.monthly_window_start IS NOT NULL AND s.monthly_window_start <= NOW()
               AND s.monthly_window_start + make_interval(secs => 2592000) > NOW()
               AND LEAST(s.monthly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at) AS monthly_restart
    FROM user_subscriptions s
    WHERE s.deleted_at IS NULL
      AND s.expires_at > NOW()
      AND s.starts_at <= NOW()
) c
WHERE us.id = c.id
  AND (c.daily_new IS NOT NULL OR c.weekly_new IS NOT NULL OR c.monthly_new IS NOT NULL);
