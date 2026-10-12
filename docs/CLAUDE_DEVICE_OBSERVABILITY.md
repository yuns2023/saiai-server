# Claude Code device information

The admin user's Claude Code device list returns `client_hints` with the last
non-empty inbound `user_agent`, `os`, `arch`, `runtime`, `runtime_version`, and
`sdk_version` values. These come from User-Agent and the corresponding
X-Stainless headers before account-specific rewriting. Missing headers preserve
previously observed values. They are client-reported display information, not
verified hardware identifiers, and do not affect device identity or admission.

Migration 099 adds a JSONB column to the existing SQL-managed device table;
there is no Ent schema for this table. Historical rows start with empty hints
and are populated when the device is next seen. Reactivation preserves captured
fields and merges newly reported values. The admin UI shows missing values as
an em dash.

Bounded carpool recorded and overflow devices return `last_owner`, an
authenticated request snapshot containing user ID, username, email, Key ID and
Key name. The UI labels it “Last user” / “Last Key”: a device can use different
Keys or users, and the latest known identity replaces the entire snapshot.
Names describe the request-time identity; later renames are reflected on a
subsequent request after authentication cache refresh. Raw Keys and authorization
headers are never copied into these display records. Authentication cache
snapshots preserve Key name and user email so cached authentication retains the
same display information as uncached authentication.

Existing Redis records remain readable without an owner. Their ownership is
populated on a subsequent authenticated request. Unlimited carpool retains its
existing behavior of not collecting new device registry entries.
