# User API key setup guidance

## Scope

Make configuration the primary next step after creating a user API key.
Keep the existing managed Claude/Codex one-command contract, Gemini examples,
API payloads and administrator key views unchanged. This is a frontend-only
change with no Client release, database migration or deployment authority.

## Implementation plan

1. Replace the user table's raw-key column with a primary **Use Key** action
   next to the key name, and retain explicit reveal/copy in **More actions**.
   The setup action stays near the start of horizontally scrollable tables.
   Raw-key reveal is initially off and resets when that dialog closes or
   changes keys.
2. Capture the create response and immediately open the existing setup dialog.
   Resolve its platform from the returned group or the available group list;
   do not depend on a refreshed, potentially filtered/paginated key list.
   Editing and failed creation must not open setup. Extend the user tour to
   the setup command and remove obsolete once-only-key claims.
3. Default Windows browsers to PowerShell while preserving manual shell
   selection. Explain copying, execution in the local terminal, and starting
   the configured client. Preserve API-route selection, command quoting and
   clipboard/history warnings.
4. Validate the actual user view, dialog privacy, create/edit/failure flows,
   both languages, shell defaults and existing bootstrap commands, then run
   the frontend suite, lint, typecheck, build and mocked browser regression.

## Boundaries

- Hiding a table column is a presentation change, not a new security boundary.
  The authenticated API still supplies keys and setup commands contain them.
- Creating a key or copying a command does not prove the user's local machine
  is configured. Show creation/copy feedback only, never a configured badge.
- The browser does not execute commands or contact a local proxy. No provider
  or model request is part of validation.
- No new runtime mode, persistent onboarding status or configuration toggle
  is introduced. Existing keys remain manageable and raw-key integration
  remains available behind an explicit user action.
- Repository delivery and test/production activation require their own
  authorization and exact release checks; local implementation is not rollout.

## Local validation

- The complete frontend suite passes: 68 files, 429 tests. Tests cover
  creation-response handling independent of refreshed lists, group fallback,
  failed creation, editing, tour advancement, raw-key reveal/copy, shell
  defaults and the existing command-generation contract.
- Typecheck and production frontend build pass. Lint has no errors and retains
  six pre-existing unused-variable warnings. The build reports warnings from
  unchanged shared CSS selectors, tooling and mixed static/dynamic imports;
  those unrelated warnings are not changed by this UI task.
- Three Chromium browser scenarios pass using synthetic keys, mocked APIs and
  operating-system user agents: Chinese Windows desktop, Chinese Linux mobile
  viewport, and English macOS desktop in dark mode. They check primary actions,
  explicit reveal/copy and reset, automatic setup after creation, shell defaults,
  clipboard contents, reopening existing keys and viewport overflow. This is
  mocked frontend regression, not native-client or remote-site acceptance.
- No external browser request, provider/model request or setup-command execution
  occurs in those scenarios. Tour sequencing is covered by component/unit tests;
  full interactive-tour browser acceptance is not claimed.
- These are local implementation checks, not deployment or acceptance evidence.
  Administrator interfaces, backend APIs, Client bundles and migrations are
  unchanged. Test activation and production promotion remain separate steps.
