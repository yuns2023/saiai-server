---
version: 1
slug: "src"
primary_target: "src"
related_targets: []
---

## Scope

AOGCode customer app (all signed-in pages plus login/register/key-usage). Mode: Operate. The landing page `/` is a tiny Persuade surface inside the same world: one positioning sentence and the sign-in entry, nothing else (PRODUCT.md: no evidence on hand).

Audience/job: developers, small teams and resellers grab or rotate a key, check balance and what calls cost, top up, then leave.

## Direction contract

THESIS: AOGCode reads like a bank electronic receipt (电子回单): every yuan is a ruled line you can audit. Refuses the KPI-card dashboard of gradient tiles, icon cards and hero metrics.

OWN-WORLD: White paper content on a cool blue-grey shell rail; deep blue-black ink for text; receipt blue for primary actions, links and the selected nav item; 1px hairline rules instead of cards or shadows; a double rule under every receipt header. Seal red appears only on the round "已入账" stamp and on destructive/error states. Dark theme is the same receipt at night: ink-navy ground, paper-white type, lighter receipt blue. System CJK sans throughout, amounts in tabular figures right-aligned, keys and commands in mono (data, not costume).

STORY: The visitor sees their balance as a receipt header, scans recent charges as statement lines, opens any line to see tokens × unit price × rate = amount, copies a key or the aog command, and goes back to their coding tool.

FIRST VIEWPORT: Overview page. Left 208px text-only nav rail on the shell tint (site name top; Overview, Keys, Usage, Top up, Account; language, theme and sign-out at the bottom). Content column: receipt header row with account name and email as small labels, balance as the largest figure on the page, then today's spend, today's requests and this month's spend as a key-value line; double rule; "Recent charges" statement table: time, model, key, rate, amount. Primary action "New key" top right of the content, secondary "Top up".

FORM: IMPECCABLE'S PICK card (bank receipt), position 1 of the ordered grounded list in re-roll round 1, chosen by the user over the assigned 12306 ticket direction; seed:39a186fa210b5f6e. Signature move: the inline charge breakdown that unfolds under a statement line, and the "已入账" seal that stamps once (200ms) when a redeem or payment lands.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Unresolved

- aog client install command: placeholder pointing at /saiai-cli until the aog client ships.
