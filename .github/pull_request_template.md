<!--
Links below are relative to the repository root, not to this file. GitHub
resolves relative links in a pull request body against the repo root, which is
the only place this template is read as a document.
-->

## What and why

<!--
What changes, and what problem it solves. The diff already says what the code
does; use this space for why it needed doing.
-->

## How it was verified

<!--
Be specific about what you actually ran, not what you believe should work.
Several of this project's worst bugs passed review and unit tests, and only
turned up on a real machine:

  - a systemd ordering cycle that silently deleted the bootstrap job, so the
    node booted perfectly and did nothing
  - every node registering as "fedora", so they overwrote each other's Node
    object while the cluster reported healthy
  - the HA controller registering the virtual IP as its own address, which
    worked flawlessly until the first failover

None of these failed loudly. If this PR touches the image, the agent, or the
deployment path, say where it ran.
-->

---

## Checklist

### Code

- [ ] Everything added is in **English** — identifiers, comments, logs, docs, commit messages
- [ ] `make test` passes
- [ ] `make lint` and `make fmt` are clean
- [ ] New behaviour has tests; any bug fix starts with a test that reproduced it
- [ ] No new dependency, configuration key, or abstraction was added without being asked for

### Documentation

Documentation is part of the change, not a follow-up.

- [ ] [`docs/reference.md`](docs/reference.md) updated if the `corium:` schema changed — including defaults and validation rules
- [ ] [`docs/quickstart.md`](docs/quickstart.md) updated if the workflow changed
- [ ] [`docs/examples/`](docs/examples/) updated, and every example still passes `corium-agent validate`
- [ ] An [ADR](docs/adr/) added if this changes one of the decisions recorded in [`AGENTS.md`](AGENTS.md)
- [ ] Any worked example in the docs still matches what the agent actually renders
- [ ] `python3 website/sync-docs.py` re-run and the result committed, if anything under [`docs/`](docs/) changed
- [ ] [`CHANGELOG.md`](CHANGELOG.md) updated under `## [Unreleased]`, if a user would notice this change

### If the OS image changed

- [ ] The image builds, and `bootc container lint` reports **no warnings**
- [ ] The filesystem contract is respected: `/usr` read-only and image-owned, `/etc` three-way merged, `/var` persistent and seeded only at install
- [ ] systemd units ship in `/usr/lib/systemd/system/` and are enabled at build time
- [ ] State directories are declared in `tmpfiles.d`, not created with `mkdir` during the build
- [ ] Downloaded artefacts are pinned by digest or checksum, verified against a value stored in this repository

### If cluster bootstrap changed

- [ ] Bootstrap is still idempotent — running twice on a node that already joined is a no-op, not a re-bootstrap
- [ ] Rendering is still deterministic; HA controllers differing only by join token produce identical `k0s.yaml`
- [ ] Failure is loud and early: the node stops with a reason in the journal rather than half-joining
- [ ] Secrets are never logged, and are written `0600`

### Security

- [ ] No secrets, tokens, kubeconfigs or private keys committed — including in examples, which use obvious placeholders
- [ ] No default passwords or shared secrets shipped in scripts

---

## CI

<!--
Three workflows exist. On a pull request:

  - CI runs gofmt, `go mod tidy`, build, vet, tests with the race detector,
    golangci-lint, and validates every example under docs/examples/ and
    deploy/proxmox/.
  - Image builds the image with `bootc container lint --fatal-warnings`, and
    never publishes: a pull request from a fork must not be able to push an
    image that nodes would later pull.
  - Pages does not run on pull requests at all. That is why the sync-docs.py
    box above matters -- nothing will catch a stale site copy until after the
    merge, when the deployment goes red.

What none of them do is run anything on a machine. The boxes above are a
statement about what you ran, not about what was confirmed for you. Please say
so explicitly if you skipped any.
-->
