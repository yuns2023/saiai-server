# Frontend toolchain

The frontend uses Tailwind CSS 4 through `@tailwindcss/postcss` and
TypeScript ESLint 8 with the existing ESLint 8 configuration. Use the pinned
pnpm version from `frontend/package.json` and install with `--frozen-lockfile`.
These development-tool migrations remove the unpatched `braces` dependency;
they do not add audit exceptions or change Gateway authentication behavior.

Tailwind CSS 4 requires Safari 16.4+, Chrome 111+, or Firefox 128+.
See the [official upgrade guide](https://tailwindcss.com/docs/upgrade-guide).
Older browsers are no longer within the supported frontend browser baseline.

`frontend/src/style.css` explicitly loads the retained JavaScript theme and
declares shared component utilities. Vue style blocks using `@apply` must
`@reference` this stylesheet with a relative path. Template utility names use
the v4 shadow, radius, outline, and gradient equivalents; DOM event names such
as `blur` must not be renamed. The custom 2px backdrop blur is named `2xs` so
it does not replace the built-in 4px `xs` value.

Run frontend validation serially:

```bash
cd frontend
pnpm install --frozen-lockfile
pnpm run lint:check
pnpm run typecheck
pnpm exec vitest run --maxWorkers=1 --no-file-parallelism
pnpm run build
pnpm audit --audit-level=high --json > /tmp/saiai-frontend-audit.json
python3 ../tools/check_pnpm_audit_exceptions.py \
  --audit /tmp/saiai-frontend-audit.json \
  --exceptions ../.github/audit-exceptions.yml
```

`TailwindMigration.spec.ts` covers shared utility compilation, isolated Vue
style references, and preservation of input/textarea blur events. Browser
acceptance must additionally compare light/dark and desktop/mobile layouts,
form focus states, navigation, tables, and dialogs against the previous build.
