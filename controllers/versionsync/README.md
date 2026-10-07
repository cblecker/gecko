# Version catalog synchronization

The controller selects one source for the environment:

- `--source-type=cincinnati` (default) probes production graph channels named
  `<Channel name>-<major.minor>`, starting at each Channel's `minimumSupportedVersion`.
  Discovery continues through later minors and majors, including 4.23 to 5.0.
  Three consecutive empty minors end a major, two empty majors end discovery,
  and a sync permits at most 256 probes. `--source-url` selects the graph endpoint;
  when omitted, the existing `--cincinnati-url` and `--arch` settings apply.
- `--source-type=release-controller --source-url=<base URL>` discovers CI streams
  and reads their accepted tags. Use an architecture-specific endpoint, such as
  `https://amd64.ocp.releases.ci.openshift.org`, without `/graph`. That endpoint
  determines architecture; `--arch` applies only to Cincinnati.

Helm exposes `source.type` and `source.url`. Channel definitions need only their
name, `minimumSupportedVersion`, `fleetMinorVersion`, and pinned `installDefaultVersion`. There is no separate
stream list or built-in set of Channel names.

## CI discovery

Each sync reads `/api/v1/releasestreams/all` once. From this active-stream index,
Channel names match these exact naming conventions:

| Pattern | Example Channel | Matching streams |
| --- | --- | --- |
| `<major>-<Channel name>` | `stable` | `4-stable`, `5-stable` |
| `<major>.<minor>.0-0.<Channel name>` | `nightly` | `4.23.0-0.nightly`, `5.0.0-0.nightly` |

Major-wide streams are eligible from the minimum-supported major onward; per-minor streams
must be at or above the minimum-supported major/minor. Accepted releases from both forms are
filtered against that Channel's inclusive minimum supported version. Comparisons are numeric
and allow prereleases within the selected minor. Canonical version and Kubernetes
resource-name validation still apply. Exact matching excludes variants such as
`5.0.0-0.nightly-art23398` from a Channel named `nightly`.

Matching streams are read through
`/api/v1/releasestream/<name>/tags?phase=Accepted`, which returns all accepted tags
without pagination. Phases are checked again locally. The CI graph cannot supply
this catalog: its nodes span streams, and its channel filter selects upgrade edges.
New matching streams are picked up on the next sync without a Channel edit.

## Failure and default handling

A configured Channel with no eligible matching stream fails the entire sync,
preserves the existing Version catalog, logs the missing match, and reports
`DefaultVersionAvailable=Unknown`. Index/tag fetch failures, malformed responses,
invalid minimum-supported versions, and conflicting payloads also preserve the snapshot.

A matching stream with no accepted releases is different from a missing stream:
it can produce an empty Channel while other Channels still synchronize. An
entirely empty supported catalog preserves the previous snapshot. Stale Versions
are deleted only after the complete desired snapshot is fetched and all desired
Versions are created/updated. API write failures can leave partial updates; the
next sync retries them.

After a successful sync, a pinned default absent from its own Channel reports
`DefaultVersionAvailable=False`; synchronization continues and the pin remains
unchanged. Available pins report True, and fetch/apply failures report Unknown.
Raising the minimum supported version can remove older memberships and make a pin unavailable.
Status includes the observed Channel generation; unchanged conditions retain their
transition time. Status-write failures are logged and retried on the next sync.
The condition stays private through Orlop's existing public-condition allowlist.

Cluster/NodePool APIs, request validation, and release resolution are unchanged.
Integration's stable/nightly definitions and environment rollout belong in the
infrastructure repository.
