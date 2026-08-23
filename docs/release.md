# Pulse release contract

This repository contains three clients and a server image. A release is a named,
reproducible artifact from an ancestor of `main`; a successful build is not a
publish and a registry signature is not a provenance attestation.

HLM-506 deliberately adds release gates only. It does not publish to npm, PyPI,
or a container registry, push a release tag, change account settings, or deploy.

## Artifact identities

| Artifact | Release identity | Current rollback target |
| --- | --- | --- |
| Nest | `@achrononlimited/pulse-nest@X.Y.Z`, tag `pulse-nest-vX.Y.Z` | npm dist-tag to a known good immutable version |
| Python | `pulse-client==X.Y.Z` (name must be claimed before documentation promises `pip install`) | PyPI has no unpublish rollback; release a higher fixed version |
| Go | `clients/go/vX.Y.Z` tag for the submodule | publish a higher version with a `retract` directive |
| Server | `ghcr.io/achronon/pulse@sha256:<digest>` | `NONE` until versioned image digests are retained and deployment is by digest |

The historical `pulse-nest-v0.1.0` tag is not a release template: it points at
the old `@achronon/pulse-nest` package name. A future tag must be an ancestor of
`origin/main`, and its package name and version must match the intended artifact.

## Required dry-run gates

The pull-request workflow runs every client test suite, builds the Python wheel,
checks the package install path, exercises the seven-field check-in contract
against a stub server, and builds the server image with `docker build` without
push. It also runs negative controls for each release boundary so a weakened
guard fails loudly.

The tag-triggered npm workflow additionally enforces:

1. The package name is `@achrononlimited/pulse-nest`.
2. The tag version equals `package.json.version` and the tag commit is an
   ancestor of `origin/main`.
3. An existing npm version is immutable: its registry `dist.shasum` must equal
   the locally rebuilt tarball.
4. The tarball is rebuilt with Node 20/npm from `setup-node`, `npm ci`,
   `npm run build`, and `npm pack`; a release note records the resulting
   digest. The current `0.1.0` rebuild is expected to be
   `931ea85914306146628983e457adb9ea1faf4dbe`. Pinning the toolchain matters:
   newer local Node/npm versions can emit a different compressed tar stream
   even when the unpacked files are identical.
5. The packed artifact installs into a throwaway consumer and its check-in body
   remains compatible with the current seven-field server contract.
6. If `publishConfig.provenance` claims `true`, an existing published version
   must have a registry attestation. npm's registry signing metadata is not a
   substitute for this check.
7. `python -m build`, `twine check`, a clean-venv install, and an import smoke
   run without reaching TestPyPI. The PyPI name check expects `pulse-client` to
   remain unclaimed until an owner deliberately claims it.
8. Go runs `go vet ./...` and `go test ./...`; a release tag must be shaped as
   `clients/go/vX.Y.Z`.
9. The server is built and run locally, then `/healthz` and an unauthenticated
   development check-in smoke are tested. The image is never pushed by this
   contract.

## Compatibility contract

The server's `DisallowUnknownFields` behavior is intentional. The compatibility
fixture is the current seven-field body:

`status`, `project`, `next_expected_at`, `interval_seconds`, `grace_seconds`,
`max_runtime_seconds`, and `duration_seconds`.

New clients must not add a field until the server is deployed and the server
owner has confirmed that it accepts that field. Client check-ins remain
fail-open: a monitoring outage must never fail the wrapped job.

## Provenance and owner gates

The current manifest retains `publishConfig.provenance: true` as a fail-closed
claim. The already-published `0.1.0` artifact must pass the registry attestation
check before a tag release can proceed. If the owner decides to keep publishing
without OIDC provenance, that is an explicit account/release-policy decision and
the manifest claim must be changed in a separately approved change; this ticket
does not weaken it automatically.

Before any real release, an owner must also decide:

- npm 2FA authorization-only versus npm Trusted Publishing (OIDC);
- whether to claim the unoccupied `pulse-client` PyPI name;
- GitHub branch protection and required client checks on `main`;
- server image retention and deployment by immutable digest, which creates the
  first honest server rollback target.

Until the last item exists, the server release record must literally say
`Rollback target: NONE`.

## Release record

Every published artifact must record:

- package/image name and version/tag;
- source commit, proven to be an ancestor of `main`;
- tarball/image digest and exact rebuild command;
- provenance status (`attested` or `not attested` with the reason);
- the seven-field wire contract and whether it changed;
- minimum server requirement; and
- the rollback target, or the literal string `NONE`.
