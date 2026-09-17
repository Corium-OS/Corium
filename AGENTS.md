# AGENTS.md

Operating instructions for AI agents and human contributors working in this repository.

---

## 1. Language policy — non-negotiable

**Everything committed to this repository is written in English.** No exceptions.

This applies to:

- Source code: identifiers, function names, variable names, package names.
- Comments and docstrings.
- Log messages, error strings, CLI help text, and user-facing output.
- Documentation, README files, ADRs, and diagrams.
- Commit messages, branch names, pull request titles and bodies, issue titles and bodies.
- Test names and test fixtures.
- YAML/TOML keys, JSON Schema descriptions, and configuration examples.

Conversations with maintainers may happen in any language. The artefacts never do.

Rationale: Corium is infrastructure software intended for an international audience. A codebase with mixed-language identifiers is unreviewable and unmaintainable.

---

## 2. What Corium is

Corium is an immutable, container-native Linux distribution that boots directly into a
Kubernetes node. It combines three things:

1. **Fedora bootc** as the base OS (`quay.io/fedora/fedora-bootc`). The operating system
   *is* a container image: built with a `Containerfile`, pushed to a registry, versioned by
   digest, scanned with ordinary container tooling, and installed with `bootc-image-builder`.
2. **k0s** as the Kubernetes distribution, baked into the read-only `/usr` and upgraded by
   rolling a new OS image, not by mutating the running system.
3. **A cloud-init abstraction layer** that exposes a small, declarative, high-level `corium:`
   configuration surface for the common cases, while leaving the full raw cloud-init and
   k0s configuration reachable as an escape hatch.

The design goal, stated as a constraint: **a node should be describable in twenty lines of
YAML, and every one of those lines should be optional.**

### Non-goals

- Corium is not a fleet-management control plane. It provisions nodes; it does not manage them.
- Corium does not fork, patch, or vendor k0s. It configures and packages upstream k0s.
- Corium does not invent a new configuration language. It extends cloud-config.

---

## 3. Architectural decisions

These are settled. Changing one requires an ADR in `docs/adr/` and an explicit decision from
the maintainers — do not quietly work around them.

| # | Decision | Rationale |
|---|----------|-----------|
| 1 | Base image is **`quay.io/fedora/fedora-bootc`**, derived via `Containerfile` | OSTree/composefs atomic updates with cloud-init as the idiomatic first-boot surface. Fedora CoreOS was evaluated and rejected: it is Ignition-native, and layering cloud-init on it means neutralising Zincati and Afterburn to reach a place fedora-bootc already is |
| 2 | Kubernetes distribution is **k0s** | Single static binary, zero host dependencies, all state confined to `/var/lib/k0s`, control plane isolated from workloads by default |
| 3 | The k0s binary lives in **`/usr`** (read-only, image-owned) | Immutability is the product. Kubernetes upgrades ship as a new OS image |
| 4 | Kubernetes upgrades are **image-based**: new image → `bootc upgrade` → reboot → atomic rollback available | One upgrade mechanism, one version axis, free rollback |
| 5 | k0s **Autopilot is disabled** | Autopilot mutates the k0s binary in place, which contradicts decision 3 |
| 6 | Configuration is resolved from a **chain of sources**, cloud-init first among them | cloud-init covers every cloud and hypervisor, but bare metal, PXE and appliances have no datasource. `/etc/corium/config.yaml`, the kernel command line and an image default cover the rest |
| 7 | **No Ignition.** cloud-init is the only first-boot mechanism | Two provisioning systems on one node means two authorities over users, SSH keys and networking, and races between them |
| 8 | `corium-agent` and all tooling are written in **Go** | Same ecosystem as k0s and Kubernetes; static binaries drop cleanly into a read-only `/usr` |
| 9 | **Every abstraction has an escape hatch** | `corium:` covers the common path; raw `write_files`, `runcmd`, and a verbatim k0s config patch must always remain available |
| 10 | **Software RAID covers spare disks, not the root filesystem** | A root array is an install-time decision the `corium:` block is read too late to make, and the bootc path for one is broken upstream. See [ADR 3](docs/adr/0003-software-raid-scope.md) |

---

## 4. Repository layout

```
.
├── AGENTS.md                  # this file
├── README.md
├── CHANGELOG.md               # what a user would notice, release by release
├── Containerfile              # the OS image build
├── mise.toml                  # pinned toolchain, and every task: `mise tasks`
├── build/
│   ├── files/                 # overlay tree copied verbatim into the image
│   │   ├── usr/
│   │   │   ├── lib/systemd/system/    # units (NEVER /etc/systemd/system)
│   │   │   ├── lib/sysctl.d/          # kernel settings for Kubernetes
│   │   │   └── lib/modules-load.d/
│   │   └── etc/cloud/cloud.cfg.d/     # cloud-init defaults (only /etc exception)
│   ├── k0s.lock               # pinned k0s version + checksums (trust anchor)
│   └── scripts/               # build-time RUN scripts, one concern each
├── cmd/
│   └── corium-agent/          # first-boot agent entrypoint
├── internal/
│   ├── config/                # corium: schema, parsing, validation, defaults
│   ├── k0s/                   # k0s.yaml rendering, token handling, service wiring
│   ├── source/                # where a node's configuration comes from
│   └── bootstrap/             # first-boot state machine
├── docs/
│   ├── adr/                   # architecture decision records
│   └── examples/              # example cloud-config documents
└── .github/workflows/         # image build, sign, publish; Go CI
```

---

## 5. Immutable OS rules

The filesystem contract is the single most common source of bugs in this kind of project.
Internalise it before writing anything that touches disk.

- **`/usr` is read-only and image-owned.** Binaries, systemd units, static defaults, and
  schemas go here. Its entire contents are replaced on every OS upgrade. Nothing may write
  to it at runtime.
- **`/etc` is machine-local and mutable.** OSTree performs a three-way merge on upgrade
  between the image's defaults and local modifications. Never assume a change shipped in a
  later image will reach a file the operator has already edited.
- **`/var` is persistent and never touched by the image.** It holds runtime state only:
  `/var/lib/k0s`, container storage, logs, databases. Never bake data into `/var` expecting
  it to ship with the image — it is only seeded at install time.

Consequences to respect:

- Ship systemd units in `/usr/lib/systemd/system/`, and enable them at **build time** with
  `systemctl enable` inside a `RUN` step so the preset is baked in. Do not write to
  `/etc/systemd/system/` from the image build.
- Set kernel arguments via TOML files in `/usr/lib/bootc/kargs.d/`, never by editing the
  bootloader configuration.
- Never disable SELinux. Label files correctly instead.
- Pin base images and all downloaded artefacts **by digest**, not by mutable tag.
- Verify checksums and signatures of anything fetched during the build. A build that can be
  silently poisoned upstream is a supply-chain hole in an OS image.

---

## 6. Go conventions

- Target the current stable Go release. Keep `go.mod` honest.
- Standard layout: `cmd/` for entrypoints, `internal/` for everything not meant to be
  imported by third parties. Do not create a `pkg/` directory without a concrete external
  consumer.
- Formatting is `gofmt` plus `goimports`. Linting is `golangci-lint`. Both are enforced in CI
  and are not advisory. `mise.toml` pins all three, so `mise run fmt` and `mise run lint`
  work on a machine that has never installed any of them.
- Wrap errors with context using `fmt.Errorf("...: %w", err)`. Never discard an error with
  `_` without a comment explaining why it is safe.
- Use `log/slog` for structured logging. The agent's output lands in the journal — make it
  greppable. No `fmt.Println` for diagnostics.
- No third-party dependency without justification. The agent is baked into an OS image; every
  dependency is attack surface that ships to every node. Prefer the standard library.
- Every exported symbol carries a doc comment beginning with its own name.
- Contexts are passed explicitly as the first parameter. Never store a `context.Context` in a
  struct.

---

## 7. corium-agent behaviour

The agent runs on first boot, reads the `corium:` block, and renders the node's
configuration. It is the heart of the abstraction and is held to a higher standard than the
rest of the tree.

- **Idempotent.** Running it twice on the same node must be a no-op the second time, not a
  re-bootstrap. Bootstrapping a node that already joined a cluster is a data-loss bug, and
  historically the single most common failure mode in this category of project.
- **Fail loudly and early.** Validate the whole configuration before mutating anything. A
  node that refuses to boot with a clear error in the journal is strictly better than a node
  that half-joins a cluster.
- **Never log secrets.** Join tokens, kubeconfigs, and private keys are redacted in all
  output. Write them with mode `0600` and correct ownership.
- **Deterministic rendering.** The same input produces a byte-identical `k0s.yaml`. Sort map
  keys; do not depend on Go map iteration order.
- **No network calls in validation.** Parsing and validating a config must work offline, so
  it can run in CI and in `corium validate`.

---

## 8. Testing

- Unit tests accompany every package in `internal/`. Config rendering is table-driven with
  golden files in `testdata/`.
- Golden files are regenerated with an explicit `-update` flag, never by hand.
- Any bug fix starts with a failing test that reproduces it.
- Integration tests boot a real image in a VM. They are slower, live behind a build tag, and
  are not a substitute for unit tests.
- Do not mock what you can construct. Prefer real structs and temporary directories over
  interface-heavy mocking.

---

## 9. Commits and pull requests

- Conventional Commits: `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`, `build:`,
  `ci:`. Scope is encouraged: `feat(agent): ...`, `fix(k0s): ...`.
- Subject line in the imperative mood, no trailing period, under 72 characters.
- The body explains **why**, not what — the diff already says what.
- One logical change per commit. Do not mix a refactor with a behaviour change.
- Pull requests describe the user-visible effect and the testing performed. If the change
  affects the `corium:` schema, the PR updates `docs/` and the examples in the same commit.
- Anything under `docs/` also has copies on the documentation site. Re-run
  `mise run docs-sync` and commit the result, or the Pages workflow fails after
  the merge — it does not run on pull requests, so nothing will warn you earlier.
- Never commit secrets, tokens, kubeconfigs, or private keys. Not even in examples — use
  obvious placeholders.

---

## 10. Releases

- Semantic versioning, and Corium is **below 1.0**: a minor release is permitted to change
  or remove configuration an earlier one accepted. Use that permission sparingly, and
  never silently.
- A user-visible change adds an entry to `CHANGELOG.md` under `## [Unreleased]`, in the
  same pull request. Say what a user would notice, not what moved in the tree.
- Tags are `vX.Y.Z`, or `vX.Y.Z-rc.N` for a candidate. Tagging publishes and signs an
  image to a public registry and moves `latest`. It cannot be undone.
- The procedure, and what has to be verified on a real machine before a release, are in
  [`.github/RELEASING.md`](.github/RELEASING.md).

---

## 11. Working agreements for agents

- **Read before you write.** This project has a strong architectural opinion; code that
  fights it will be rejected regardless of quality.
- **Do not add a dependency, a configuration key, or an abstraction layer without being asked.**
  Scope creep in an OS image is expensive and permanent.
- **When a decision is genuinely ambiguous, ask.** Do not guess at semantics for anything
  touching cluster bootstrap, token handling, or the filesystem contract.
- **Verify claims about upstream behaviour.** bootc, FCOS, and k0s are all moving quickly.
  Check the current documentation rather than relying on recalled behaviour, and cite the
  source in the PR when the behaviour is subtle.
- **Prefer deleting code to adding it.** The best version of this project is the smallest one
  that works.
