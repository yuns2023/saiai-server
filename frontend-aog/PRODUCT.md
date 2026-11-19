# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Vue 3 + TypeScript + Vite + vue-router + Pinia + vue-i18n + Tailwind CSS 4, matching the incumbent `frontend/` toolchain so CI and the Go embed pipeline can treat both apps the same. This app is a separate project (`frontend-aog/`) that shares only the backend API (`/api/v1`) and the auth storage contract with the classic frontend; it reuses no classic components or styles.

## Users

- Individual developers who buy credit and use it in Claude Code every working day.
- Small teams and studios: one person tops up, then issues separate API keys to members and watches usage per key.
- Resellers and channel partners who hand out redeem codes to their own customers.
- Owners of their own Claude subscription who have it hosted on the site and use it through their keys (hosted accounts, assigned by an administrator).

Typical visit: open the site to grab or rotate a key, check balance and what recent calls cost, top up, then leave. Most time is spent in the coding tool, not on this site.

## Product Purpose

CCYou.dev is a Claude Code entry point: the site serves Claude Code only, so the UI offers Anthropic groups only (`src/lib/platforms.ts`) and never shows OpenAI / Codex / Gemini. A user gets one account and API keys that work with the official Claude Code client. Success means the user configures a client once, it keeps working, and they always know what they spent and why.

## Positioning

- Stable and compatible with the official clients: official Claude Code features keep working through the relay, and the AITP client sets a machine up in one step.
- Transparent billing: every call shows what it cost and which rate it was billed at.

## Operating Context

- Access is invite-only: a new user needs an invitation code. Existing users can generate their own codes once they have topped up at least once: a credited stablecoin transfer, a completed online payment, or a redeemed code that was not bought by a user with balance (weekly cap set by the operator).
- Users arrive with Claude Code already installed and want a one-line AITP install command from https://aitp.lol (manual base URL plus key as a fallback).
- Usage review happens per key and per model, often by a team lead checking members' spend.
- Top-up is by stablecoin transfer (USDT / USDC on Arbitrum or Solana, 1 coin = $1 of balance, credited once the block is final: about 1 minute on Solana, about 20 minutes on Arbitrum) or by entering a redeem code on `/billing`. Users can also spend balance on a balance redeem code at `/billing` to give away or to top up someone else (operator switch `redeem_purchase_enabled`, not in the public settings, so the landing page mentions it unconditionally); such codes never count toward invitation eligibility, whoever redeems them. Online fiat payment is switched off on this site; the externally hosted payment flow and its `/purchase` return URL stay in the code and must keep working if the operator turns it back on.
- Hosted accounts: an administrator assigns a user's own Claude account to that user; the owner then sees its quota windows and per-call usage at `/hosted`. A hosting service fee may apply, shown as a multiple of the standard price on keys and key lookup. When the operator turns on self-onboarding, any signed-in user can also submit their own Claude account (setup-token authorisation, optionally with their own proxy) at `/hosted`; an administrator approves it, and the owner can pause it or ask for it to be taken down.
- The site operator administers everything through the classic admin console at `/admin`; this app is the customer-facing surface only.

## Capabilities and Constraints

- Public: landing page (positioning, three-step connect, how to top up, hosted accounts, illustrative receipt), login (email + password, Google and GitHub when enabled), email registration (verification code, invitation code, promo code and Turnstile are backend toggles), forgot/reset password, key usage lookup without login (`/key-usage`).
- Signed in: overview (balance, today's and this month's spend, recent charges, hosted-account and invitation lines when they apply), API keys (create/edit/delete, connection instructions), usage records and stats, hosted accounts (owners, plus everyone while self-onboarding is open), top-up (stablecoin, redeem codes, buying redeem codes when the operator allows it; plan purchase and orders when online payment is on), invitations, account (profile, change password), announcements.
- Deliberately out of scope: two-factor authentication, sign out of all devices, custom menu pages, CCS import.
- Not live, so never described on any page until it ships: a sharing marketplace or owner earnings, loyalty levels, a status page, public pricing.
- Site name and logo come from the backend public settings (`site_name`, `site_logo`); never hard-code the brand in a way the operator cannot change.
- Languages: Chinese and English. Themes: light and dark.
- Admins sign in through this app and are sent to `/admin` (classic console); both apps share the same browser session.
- The local client is AITP (github.com/2742zdz/AITP); its name and install commands live in one place (`src/lib/connect.ts`) so they can be swapped.

## Brand Commitments

- Name: CCYou.dev ("你的 Claude Code 专属入口"); Claude Code only.
- Must look completely different from the incumbent sub2api-derived UI and be minimalist.

## Evidence on Hand

None yet. No customer counts, testimonials, uptime figures, benchmarks, fee rates or public pricing may be shown or invented. The landing page states only what the site does today (connect steps, payment methods with their confirmation times, hosted accounts on request) and carries the illustrative receipt labelled as an example.

## Product Principles

1. Get the user back to their coding tool fast: key and setup instructions are never more than one step away.
2. Every number about money is explainable: show the cost and the rate behind it, never a bare total.
3. Say only what is true and current; no marketing claims without data behind them.
4. Claude Code only: no provider choice, no platform names in the UI.
