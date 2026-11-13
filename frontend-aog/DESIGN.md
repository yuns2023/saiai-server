---
name: AOGCode
description: Customer console for an API relay serving coding tools — every charge readable like a bank statement.
colors:
  receipt-blue: "#1d4e89"
  receipt-blue-deep: "#163e6e"
  receipt-blue-wash: "#e6edf6"
  seal-red: "#b8262c"
  seal-wash: "#fbeceb"
  ledger-ink: "#14203a"
  ink-secondary: "#47536e"
  ink-tertiary: "#66708a"
  paper: "#ffffff"
  shell-rail: "#f1f3f7"
  hairline: "#d9dee7"
  hairline-soft: "#eceff4"
  settled-green: "#1d7347"
  pending-amber: "#8a5a00"
typography:
  figure:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans SC, system-ui, sans-serif"
    fontSize: "2.25rem"
    fontWeight: 600
    lineHeight: 1
    letterSpacing: "-0.01em"
    fontFeature: "tnum"
  headline:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans SC, system-ui, sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.4
  title:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans SC, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.5
  body:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans SC, system-ui, sans-serif"
    fontSize: "0.875rem"
    fontWeight: 400
    lineHeight: 1.6
  label:
    fontFamily: "PingFang SC, Hiragino Sans GB, Microsoft YaHei, Noto Sans SC, system-ui, sans-serif"
    fontSize: "0.75rem"
    fontWeight: 400
    letterSpacing: "0.02em"
  data-mono:
    fontFamily: "ui-monospace, SFMono-Regular, Cascadia Mono, JetBrains Mono, Menlo, Consolas, monospace"
    fontSize: "0.75rem"
    fontWeight: 400
rounded:
  control: "4px"
  tag: "3px"
  dialog: "6px"
spacing:
  row: "0.7rem"
  group: "1rem"
  section: "2.25rem"
components:
  button-primary:
    backgroundColor: "{colors.receipt-blue}"
    textColor: "{colors.paper}"
    rounded: "{rounded.control}"
    height: "2.25rem"
    padding: "0 0.95rem"
  button-primary-hover:
    backgroundColor: "{colors.receipt-blue-deep}"
  button-line:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ledger-ink}"
    rounded: "{rounded.control}"
    height: "2.25rem"
  button-danger:
    textColor: "{colors.seal-red}"
    rounded: "{rounded.control}"
  field:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ledger-ink}"
    rounded: "{rounded.control}"
    height: "2.5rem"
    padding: "0 0.75rem"
  nav-item-active:
    backgroundColor: "{colors.receipt-blue-wash}"
    textColor: "{colors.receipt-blue}"
    rounded: "{rounded.control}"
    height: "2.25rem"
---

# Design System: AOGCode

## Overview

**Creative North Star: "The Electronic Receipt (电子回单)"**

AOGCode reads like a bank's electronic receipt and statement. Money is the subject, so the interface behaves like the paperwork people already trust for money: a receipt header that states who and how much, a double rule that closes it, and statement lines underneath where every amount can be opened up and audited. Paper-white content sits next to a cool blue-grey rail; ink is a deep blue-black, and one receipt blue marks what you can act on or where you are.

Density is that of a working statement, not a marketing page: hairline rules separate rows, figures line up in tabular columns, and hierarchy comes from size and weight rather than boxes. There are no cards, no tiles of KPIs, no gradients. The one ceremonial moment is the round red "已入账" seal that stamps once when a redeem code or payment lands.

The same receipt is printed at night for dark mode: ink-navy ground, paper-white type, a lighter receipt blue. Language and appearance preferences share keys with the classic admin console so the two apps agree.

**Key Characteristics:**
- Receipt header + double rule at the top of every money page.
- Statement tables with right-aligned tabular amounts; a line unfolds into its own itemised receipt.
- Rules, not containers: 1px hairlines and one 3px double rule; no shadows on content.
- One action colour (receipt blue); seal red only for the credited seal, errors and destructive actions.

## Colors

Restrained: cool neutrals tinted toward the ink, one blue that does the work, one red that is earned.

### Primary
- **Receipt Blue**: primary buttons, links, the selected nav item, focus rings, active segmented controls. Its deep variant is the hover state; its wash tints the selected nav item and a freshly created key row.

### Secondary
- **Seal Red**: the "已入账" seal, error text, invalid field borders, destructive buttons and the unread-count badge. Never decoration.

### Neutral
- **Ledger Ink**: all primary text and the strong rules (table header underline, double rule).
- **Ink Secondary / Ink Tertiary**: supporting text and labels. Tertiary still clears 4.5:1 on paper.
- **Paper**: content ground. **Shell Rail**: the navigation rail and code blocks, one step cooler than paper.
- **Hairline / Hairline Soft**: row separators and field borders; soft for rows inside a table.
- **Settled Green / Pending Amber**: status tags only (active, completed / pending, cap reached), always on their own washes.

### Named Rules
**The Earned Red Rule.** Seal red appears only where something was credited, failed, or is about to be destroyed. If a red element survives a screen with no money event and no error, remove it.

**The One Voice Rule.** Receipt blue is the only interactive colour. Status colours never become buttons.

## Typography

**Body Font:** the platform CJK sans (PingFang SC / Microsoft YaHei / Noto Sans SC, then system-ui)
**Label/Mono Font:** ui-monospace stack, for keys, commands, model ids and request ids only

**Character:** One workhorse sans carries everything; numbers are always tabular so columns of money line up like a printed statement.

### Hierarchy
- **Figure** (600, 2.25rem, line-height 1): the balance on Overview and Top up. One per page.
- **Headline** (600, 1.25rem): page titles.
- **Title** (600, 1rem): section titles inside a page.
- **Body** (400, 0.875rem, 1.6): table cells, form text, prose (announcements capped at 70ch).
- **Label** (400, 0.75rem, +0.02em, tertiary ink): field labels and the receipt header's left column.
- **Data mono** (400, 0.75rem): API keys, install commands, model names, request ids.

### Named Rules
**The Tabular Money Rule.** Every number that can be compared — amounts, tokens, counts, dates in tables — uses tabular figures and right alignment in its column.

**The Mono Is Data Rule.** Monospace only for strings a user copies or matches (keys, commands, model ids); never as "technical" decoration.

## Layout

Two-column shell on desktop: a fixed 13rem navigation rail on the shell tint, and a content column capped at 72rem with 2.5rem side padding. Below 1024px the rail becomes an off-canvas drawer behind a 3rem top bar.

Pages read top-down like a statement: title and actions, receipt header closed by the double rule, then sections spaced about 2.25rem apart with more space above a section title than below it. Tables collapse by priority on small screens (key column from 768px, tokens and rate from 640px), and the scroll container never widens the page.

## Elevation & Depth

Flat. Content never floats; separation is done with rules and the shell tint. Two exceptions carry a soft offset shadow: toasts, and the illustrative receipt on the landing page (a single lifted sheet). Modal dialogs sit on a 45% ink scrim.

### Named Rules
**The Paper Stays Flat Rule.** If content needs separating, add a rule or space, not a shadow or a card.

## Shapes

Small, quiet corners: 4px on controls and fields, 3px on tags, 6px on dialogs. Tables and sections have no corners at all because they have no boxes. The seal is the only circle in the system.

## Components

### Buttons
- **Shape:** gently squared (4px), 2.25rem tall; 1.875rem in tables.
- **Primary:** receipt blue on paper-white text; hover deepens to receipt blue deep.
- **Line:** paper ground, hairline border that darkens to tertiary ink on hover.
- **Quiet:** text-only receipt blue, wash background on hover; used for row actions.
- **Danger:** seal-red outline and text, seal wash on hover; destructive actions always confirm in a dialog.

### Inputs / Fields
- **Style:** paper ground, hairline border, 4px corners, 2.5rem tall.
- **Focus:** border and a 1px ring in receipt blue.
- **Error / Disabled:** seal-red border with an inline message below / shell-tinted ground with tertiary text.

### Navigation
- **Rail:** text items with 17px stroke icons, 2.25rem rows. Active item: receipt-blue wash and receipt-blue text. Admins get a dashed-border link to the classic console under the list. Preferences and sign-out sit at the bottom.

### Statement table (signature)
Header row underlined by a strong ink rule, rows separated by soft hairlines, amounts right-aligned in tabular figures. Clicking a line unfolds an itemised receipt beneath it: tokens × unit price per part, the standard total, each multiplier, a double rule, and the charged amount — revealed with a 200ms top-down clip.

### Receipt header (signature)
Account labels on the left, the balance as the page's single figure, secondary totals as a right-aligned key-value line, closed by the 3px double rule.

### Credited seal (signature)
A 96px double-ring circle in seal red with "已入账" and the date, rotated −9°, stamping in once over 220ms with an ease-out from 1.35× scale. Appears only after a redeem or payment is confirmed by the server.

## Do's and Don'ts

### Do:
- **Do** close every money header with the double rule (3px double, ledger ink).
- **Do** show how a charge was computed wherever a charge is shown (tokens × price, multipliers, result).
- **Do** keep amounts right-aligned with tabular figures, at 4 decimals for sub-dollar charges and 2 for balances.
- **Do** theme browser surfaces from the palette: selection, caret, focus ring, scrollbar.

### Don't:
- **Don't** wrap sections or metrics in cards, tiles or shadows.
- **Don't** use seal red for anything that isn't a credit, an error, or a destructive action.
- **Don't** put an eyebrow or kicker above headings.
- **Don't** invent figures on public pages; illustrative receipts carry an "example, not real prices" label.
