# End-user GitHub and Google login

GitHub and Google end-user login credentials are deployment-owned settings.
They are intentionally not editable through the admin API or stored in the
settings table.

## Configuration

Configure either provider in `config.yaml` or with the equivalent environment
variables:

```yaml
github_oauth:
  enabled: true
  client_id: "..."
  client_secret: "..."
  redirect_url: "https://gateway.example.com/api/v1/auth/oauth/github/callback"

google_oauth:
  enabled: true
  client_id: "..."
  client_secret: "..."
  redirect_url: "https://gateway.example.com/api/v1/auth/oauth/google/callback"
```

The environment variable names are `GITHUB_OAUTH_ENABLED`,
`GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`,
`GITHUB_OAUTH_REDIRECT_URL`, and the corresponding `GOOGLE_OAUTH_*` names.
Redirect URLs must use HTTPS except for loopback development URLs and must
exactly match the URL registered with the provider.

The variables are passed through by all versioned Compose templates and are
listed in `deploy/.env.example`. Keep both providers disabled until their
registered callback URL, client ID, and client secret have been configured.

Provider authorization, token, user-info endpoints, and scopes are fixed in
the server. The server requests `read:user user:email` from GitHub and
`openid email profile` from Google.

## Identity and registration rules

- The persistent identity key is `(provider, provider subject)`, never email.
- GitHub login requires a primary verified email. Google login requires
  `email_verified=true`.
- Provider access and refresh tokens are never persisted.
- Every flow uses an HttpOnly state cookie and PKCE S256.
- If an unlinked local account already owns the verified email, login fails
  with `OAUTH_ACCOUNT_LINK_REQUIRED`; the provider is never silently attached.
- An existing user can link a provider from the profile page. Linking requires
  an authenticated local session plus fresh password verification, or fresh
  TOTP verification when TOTP is enabled for that user. The short-lived link
  context is bound to the provider, state, and target user with HMAC.
- A linked user with TOTP enabled must complete the normal local TOTP login
  challenge after provider authentication. OAuth does not bypass local 2FA.
- Successful explicit bindings emit a structured log with provider, local user
  ID, and client IP, but never provider subject, email, or token. Alert when one
  client IP binds identities to three or more distinct users within one hour.
- New accounts follow the normal registration-enabled, email-domain, default
  subscription, and invitation-code policies.
- Invitation handoff uses a ten-minute, one-time database session. Only a
  SHA-256 hash of the random handoff token is stored, and the row contains no
  target user ID.

There is no automatic migration from the removed LinuxDo login. Historical
synthetic LinuxDo email addresses remain reserved so they cannot be reclaimed
as local accounts. Before enabling the replacement providers, operators should
identify affected LinuxDo-only accounts. Restore access through the existing
admin user-management process by assigning a real, verified email and forcing
a password reset; do not bind a GitHub/Google identity by matching email in SQL.
After the user signs in locally, they can perform the explicit link flow.

## Database and rollout notes

Migration `086_add_login_oauth_identities.sql` is additive: it creates only the
`auth_identities` and `oauth_registration_sessions` tables and their indexes.
The previous application version does not read these tables, so application
rollback can leave them in place. Do not drop them during a routine rollback;
doing so would discard provider bindings and in-flight registration handoffs.

Re-check the protected branch immediately before merge for migration filename
collisions. Once any environment has applied the file, its name and contents
are immutable.
