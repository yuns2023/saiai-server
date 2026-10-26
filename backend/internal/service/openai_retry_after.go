package service

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// parseOpenAIRetryAfter accepts HTTP delay-seconds or an HTTP-date. Anchor
// relative delays to header receipt, not to completion of the error body read.
// Unusable values fall back to the existing bounded generic-429 backoff. Usable
// values are capped at the escalated generic backoff: this path only sees 429s
// without a quota reset, and a later 429 re-arms the wait.
func parseOpenAIRetryAfter(value string, observedAt time.Time) *time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	digits := true
	for _, ch := range value {
		if ch < '0' || ch > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
			return nil
		}
		return capOpenAIRetryAfter(observedAt.Add(time.Duration(seconds)*time.Second), observedAt)
	}
	resetAt, err := http.ParseTime(value)
	if err != nil || !resetAt.After(observedAt) {
		return nil
	}
	return capOpenAIRetryAfter(resetAt, observedAt)
}

func capOpenAIRetryAfter(resetAt, observedAt time.Time) *time.Time {
	if maxResetAt := observedAt.Add(openAINoReset429EscalatedFor); resetAt.After(maxResetAt) {
		resetAt = maxResetAt
	}
	return &resetAt
}
