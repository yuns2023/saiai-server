-- Subscription usage windows anchored at starts_at.
--
-- Until now the daily/weekly/monthly windows started at midnight of the day
-- of first use and, after ending, restarted at midnight of the day of the
-- next request, while the expiry is starts_at + N days. The monthly window
-- therefore ended up to a day before the expiry and a fresh, full monthly
-- window opened for the last hours of the subscription. The server now lays
-- the windows on a grid anchored at starts_at (starts_at + k * 1/7/30 days).
--
-- This migration moves the current windows of unexpired subscriptions onto
-- that grid once. starts_at and usage are not changed. A window column is
-- changed only when it is set, not in the future, not yet ended and not on
-- the grid. The old start is midnight-truncated, so the intended grid point
-- lies less than 24 h after it: the new start is the latest grid point no
-- later than (old start + 24 h) and no later than now (an earlier point when
-- the window drifted, which only brings the next reset forward). A window
-- that has already ended is left alone: the next request resets it onto the
-- grid. A window that started more than 24 h before starts_at is left alone.
-- Rows already on the grid are not touched, so a second run changes nothing.
--
-- The previous binary works against these values (it compares window start +
-- fixed length with now) but writes midnight starts again on its next reset,
-- so the previous gateway must be stopped before this binary starts.

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '60s';

UPDATE user_subscriptions us
SET daily_window_start = COALESCE(c.daily_new, us.daily_window_start),
    weekly_window_start = COALESCE(c.weekly_new, us.weekly_window_start),
    monthly_window_start = COALESCE(c.monthly_new, us.monthly_window_start),
    updated_at = NOW()
FROM (
    SELECT s.id,
           CASE
               WHEN s.daily_window_start IS NULL
                 OR s.daily_window_start > NOW()
                 OR s.daily_window_start + make_interval(secs => 86400) <= NOW()
                 OR LEAST(s.daily_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
                 OR mod(extract(epoch FROM (s.daily_window_start - s.starts_at)), 86400) = 0
               THEN NULL
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.daily_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 86400) * 86400)
           END AS daily_new,
           CASE
               WHEN s.weekly_window_start IS NULL
                 OR s.weekly_window_start > NOW()
                 OR s.weekly_window_start + make_interval(secs => 604800) <= NOW()
                 OR LEAST(s.weekly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
                 OR mod(extract(epoch FROM (s.weekly_window_start - s.starts_at)), 604800) = 0
               THEN NULL
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.weekly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 604800) * 604800)
           END AS weekly_new,
           CASE
               WHEN s.monthly_window_start IS NULL
                 OR s.monthly_window_start > NOW()
                 OR s.monthly_window_start + make_interval(secs => 2592000) <= NOW()
                 OR LEAST(s.monthly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) < s.starts_at
                 OR mod(extract(epoch FROM (s.monthly_window_start - s.starts_at)), 2592000) = 0
               THEN NULL
               ELSE s.starts_at + make_interval(secs =>
                   floor(extract(epoch FROM (LEAST(s.monthly_window_start + INTERVAL '24 hours' - INTERVAL '1 microsecond', NOW()) - s.starts_at)) / 2592000) * 2592000)
           END AS monthly_new
    FROM user_subscriptions s
    WHERE s.deleted_at IS NULL
      AND s.expires_at > NOW()
      AND s.starts_at <= NOW()
) c
WHERE us.id = c.id
  AND (c.daily_new IS NOT NULL OR c.weekly_new IS NOT NULL OR c.monthly_new IS NOT NULL);
