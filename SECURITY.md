# Security policy

## Supported versions

Security fixes are made for the current `main` branch and, when maintainers
publish releases, the latest supported release. Older snapshots may not receive
backports.

## Reporting a vulnerability

Do not open a public issue, discussion, or pull request for an unpatched
vulnerability. Use this repository's **Security** tab and choose **Report a
vulnerability** to submit a private report. If private vulnerability reporting
is unavailable, contact the repository owner through their GitHub profile and
request a private reporting channel before sharing technical details.

Include, when available:

- affected version or commit;
- deployment assumptions needed to reproduce the issue;
- a minimal reproduction without real credentials or user data;
- expected and observed behavior;
- likely impact and any known mitigations.

Please allow maintainers time to investigate and coordinate a fix before public
disclosure. The project cannot promise a particular response or remediation
deadline.

## Secrets and incident response

Never send a live API key, OAuth token, session cookie, private key, database
dump, or unredacted production log in a report. If a credential has entered Git
history, an Actions log, an issue, or an artifact, treat it as compromised:
revoke or rotate it at its issuer first. Deleting the visible text is not a
substitute for rotation.

## Deployment responsibility

SAIAI Server is a security boundary for provider credentials and user traffic.
Self-hosting operators are responsible for, at minimum:

- terminating TLS and restricting administrative access;
- using unique database, Redis, JWT, TOTP, and administrator secrets;
- keeping provider credentials out of images, source control, and command-line
  arguments;
- reviewing outbound-network and upstream-host policy;
- applying supported updates and monitoring authentication and gateway logs;
- backing up and protecting PostgreSQL and any mounted application data; and
- following upstream provider terms and applicable data-protection rules.

Example deployment files are templates, not a complete production security
policy.
