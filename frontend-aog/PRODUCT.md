# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Stack

Vue 3 + TypeScript + Vite + vue-router + Pinia + vue-i18n + Tailwind CSS 4, matching the incumbent `frontend/` toolchain so CI and the Go embed pipeline can treat both apps the same. This app is a separate project (`frontend-aog/`) that shares only the backend API (`/api/v1`) and the auth storage contract with the classic frontend; it reuses no classic components or styles.

## Users

- Individual developers who buy credit and use it in coding tools (Claude Code, Codex CLI, Gemini CLI and similar) every working day.
- Small teams and studios: one person tops up, then issues separate API keys to members and watches usage per key.
- Resellers and channel partners who buy redeem codes or plans and pass them on to their own customers.

Typical visit: open the site to grab or rotate a key, check balance and what recent calls cost, top up, then leave. Most time is spent in the coding tool, not on this site.

## Product Purpose

AOGCode is an API relay focused on the models coding tools use: A = Anthropic, O = OpenAI, G = Google. A user gets one account and API keys that work with the official coding clients. Success means the user configures a client once, it keeps working, and they always know what they spent and why.

## Positioning

- Stable and compatible with the official clients: official CLI and desktop features keep working through the relay, and the `aog` client sets a machine up in one step.
- Transparent billing: every call shows what it cost and which rate it was billed at.

## Operating Context

- Users arrive with a coding tool already installed and want a base URL plus a key, or a one-line `aog` install command.
- Usage review happens per key and per model, often by a team lead checking members' spend.
- Payments go through externally hosted pages; after paying, the payment provider sends the browser back to `/purchase` (admin-configured return URL), which this app must keep serving.
- The site operator administers everything through the classic admin console at `/admin`; this app is the customer-facing surface only.

## Capabilities and Constraints

- Public: landing page (one-sentence positioning + sign in), login, email registration (verification code, invitation code and promo code are backend toggles), forgot/reset password, key usage lookup without login (`/key-usage`).
- Signed in: overview (balance, today's usage), API keys (create/edit/delete, connection instructions), usage records and stats, top-up (redeem codes, plan purchase, orders), account (profile, change password), announcements.
- Deliberately out of scope: two-factor authentication, sign out of all devices, hosted accounts, custom menu pages, third-party OAuth login, CCS import.
- Site name and logo come from the backend public settings (`site_name`, `site_logo`); never hard-code the brand in a way the operator cannot change.
- Languages: Chinese and English. Themes: light and dark.
- Admins sign in through this app and are sent to `/admin` (classic console); both apps share the same browser session.
- The client is being renamed from `saiai` to `aog` and may become a separate open-source project; its name and install command must live in one place so they can be swapped.

## Brand Commitments

- Name: AOGCode (A = Anthropic, O = OpenAI, G = Google).
- Must look completely different from the incumbent sub2api-derived UI and be minimalist.

## Evidence on Hand

None yet. No customer counts, testimonials, uptime figures, benchmarks or public pricing may be shown or invented. The landing page carries only the positioning sentence and the sign-in entry.

## Product Principles

1. Get the user back to their coding tool fast: key and setup instructions are never more than one step away.
2. Every number about money is explainable: show the cost and the rate behind it, never a bare total.
3. Say only what is true and current; no marketing claims without data behind them.
4. One account, three model families: the provider is a detail, not a navigation tree.
