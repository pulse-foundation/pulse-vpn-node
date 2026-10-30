# Single VLESS Connection And Transport Selection

Status: one VLESS/REALITY connection with raw TCP and Vision is implemented, including managed idle
startup and revisioned singleton creation. XHTTP selection and conversion remain architectural direction.

## Cardinality And Ownership

Keep one configured VLESS connection per Node; an unconfigured Node may remain idle. Select its
transport at creation. All accesses inherit this choice. Do not add per-user transport flags,
parallel raw/XHTTP listeners, listener collections or a port dispatcher for this feature.

Separate the Node's lifecycle identity, its connection identity/configuration and its stable access
identities. They need not become separate packages or tables merely for naming. A conversion preserves
those identities. The control panel owns client-to-access mappings and profile generation; the Node
stores credentials/access state and serves clients independently of the control panel.

Retain Go plus one unmodified, pinned core child. Evolve the existing single-VLESS settings at the
core boundary into typed transport variants when implementing the feature; do not create a core per
transport or pass arbitrary adapter JSON through the API. The adapter owns transport/flow mapping,
validation, rendering and observed runtime state. Raw TCP + Vision is the initial default. XHTTP's
fields and flow rules require separate validation and compatibility evidence.

Expose supported combinations through a versioned capability contract, scoped to the shipped agent
and adapter, not just the underlying binary's theoretical features. Reject unsupported combinations
explicitly. A future adapter must pass the same behavior checks before claiming support; there is no
silent transport/security fallback.

## Creation, Conversion And Recovery

Creation chooses one supported transport. Noninteractive init remains a fresh-state preparation tool,
not a transport conversion or credential rotation mechanism. Existing valid files must be preserved;
conflicting arguments fail rather than rewriting an installed service.

A future conversion API records durable, revisioned intent and an idempotent operation, with explicit
expected-revision checks. The application reconciler applies configuration; HTTP handlers do not launch
unowned work. Preserve a protected prior transport configuration for explicit rollback and retain the
existing desired/applying/verified-applied/failed distinction. Preserve connection/access identities,
compatible UUIDs and REALITY keys, enabled states and cumulative traffic. A required credential change
must be explicitly disclosed to the caller, not hidden inside conversion.

Only one transport is active. Existing sessions may close and old profiles may stop working; clients
need refreshed profiles. Readiness/controller success does not prove remote connectivity. Configuration
application and external end-to-end verification must remain distinguishable in the operation contract.
Rollback restores transport configuration while incorporating current access state; it must never
restore an old user snapshot and re-enable revoked identities. Consumers may need to refresh profiles
again after rollback.

Serialize access and transport changes or reject conflicting mutations with a retryable conflict;
never reconcile competing snapshots that overwrite each other's intent. Ordinary user changes keep
the no-routine-core-restart requirement. Transport maintenance may need a different disruption budget.
The Node still does not manage Docker or the host firewall. Any port publication/container recreation
belongs to the deployment controller and must be coordinated as explicit maintenance.

## Compatibility And Acceptance

Existing startup settings and managed users map to the raw TCP variant without regeneration. Preserve
current user names as accounting identities, UUIDs, secret files, disabled state and historical totals.
Use new reviewed migrations and a documented API/config transition when implementing persistence;
never rewrite released migrations. Do not use transport names as user IDs or reset counters during
conversion. Managed creation uses migration `003_node_lifecycle.sql` and the shared operation revision stream.
No conversion or XHTTP schema/API is implied.

Before enabling XHTTP/conversion, test supported client/core combinations, actual bidirectional traffic,
user creation and targeted revocation, unrelated-session behavior, traffic attribution, restart
persistence, invalid/bind-conflicting configurations, stale revisions, apply failures and rollback.
Revocations must survive failed conversion and rollback. Document which operations interrupt sessions.
Tests establish interoperability; they do not establish resistance to filtering in public networks.
