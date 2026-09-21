# Building your own image

Corium's published image is a base. Deriving your own is how you add the things
a `corium:` block was never going to cover — a monitoring agent, your
organisation's CA bundle, a driver, a backup client. The mechanism is an
ordinary `Containerfile` — a `FROM`, whatever you are adding, and a lint — and
the worked example below is about thirty lines of it. Signing and the order you
roll it out in are the parts that take thought.

It is also the supported way to do it. Installing an agent on a running node
with `dnf` does not work, because `/usr` is read-only; installing it into `/var`
with a `runcmd` works exactly until the next upgrade, when nothing puts it back.
An agent that is part of the image is an agent that survives.

## Before you start

- `podman` on the machine you build from, and
  [`cosign`](https://docs.sigstore.dev/) to sign what it produces.
- A registry you can push to, and that your nodes can pull from.
- An answer to one question: **do you already have nodes running Corium, or
  only machines you have yet to install?** An existing fleet needs the signing
  policy shipped one image ahead of the agents that rely on it; new machines do
  not. See [3. Build it, and sign it](#3-build-it-and-sign-it).

---

## 1. The one rule that decides everything

Corium's filesystem contract, from [concepts](concepts.md), is what makes a
derived image either sound or quietly broken:

| | Who owns it | What that means for you |
|---|---|---|
| `/usr` | the image, read-only | **Put your things here.** This is what upgrades replace, so this is what follows a node forward |
| `/etc` | the machine, merged on upgrade | Ships defaults here only when there is nowhere else. An operator's edit wins over yours, for ever |
| `/var` | the machine, **seeded at install and never updated by an image** | Write nothing here at build time |

That last row is the mistake that costs the most, because it does not fail. A
file you `COPY` into `/var` reaches every machine *installed* from your image
and no machine *upgraded* into it. Your test node, freshly installed, works
perfectly. The fleet you upgrade never sees it, and nothing says so.

State directories are declared in `tmpfiles.d` instead, and systemd creates them
on every boot. That is a rule Corium follows for its own state and it is not
decoration.

---

## 2. A worked example: adding an agent

Say you want `node_exporter` on every node.

```dockerfile
FROM ghcr.io/corium-os/corium:0.3.3

ARG VERSION=1.10.0
ARG SHA256=a1b2c3...   # the checksum you looked up, pinned here

# The binary goes in /usr, which is what an upgrade carries forward.
#
# Pinned by checksum and not by "latest": this is a build that will be repeated
# months from now, and a download that silently changed underneath it is a node
# fleet running something nobody chose.
RUN curl --fail --silent --show-error --location --retry 3 \
        --output /tmp/node_exporter.tar.gz \
        "https://github.com/prometheus/node_exporter/releases/download/v${VERSION}/node_exporter-${VERSION}.linux-amd64.tar.gz" \
    && echo "${SHA256}  /tmp/node_exporter.tar.gz" | sha256sum --check \
    && tar -xzf /tmp/node_exporter.tar.gz -C /tmp \
    && install -m 0755 "/tmp/node_exporter-${VERSION}.linux-amd64/node_exporter" \
        /usr/bin/node_exporter \
    && rm -rf /tmp/node_exporter*

# Units belong in /usr/lib/systemd/system. Never /etc/systemd/system: that is
# the machine's, and an operator who disables your unit there should stay
# disabled across upgrades.
COPY node-exporter.service /usr/lib/systemd/system/node-exporter.service

# Enabled at build time, so a node boots with it running rather than waiting
# for someone to turn it on.
RUN systemctl enable node-exporter.service

# The same check CI runs on Corium's own image. It catches the /var mistake
# above, among others, and it is cheap.
RUN bootc container lint --fatal-warnings
```

With `node-exporter.service` beside it:

```ini
[Unit]
Description=Prometheus node exporter
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/bin/node_exporter
Restart=always
DynamicUser=yes
ProtectSystem=strict
ProtectHome=yes
NoNewPrivileges=yes

[Install]
WantedBy=multi-user.target
```

If your agent needs a directory of its own, declare it rather than creating it:

```
# /usr/lib/tmpfiles.d/node-exporter.conf
d /var/lib/node-exporter 0750 root root -
```

---

## 3. Build it, and sign it

> **Warning.** A node enforces the policy it is currently running, not the one
> in the image it is moving to. The entry that makes your repository acceptable
> has to reach a node *before* the upgrade that needs it. Getting this
> backwards leaves nodes that refuse every image you build, and the way back is
> the console. If you already have a fleet, use the two-stage rollout below
> rather than putting the policy and your agents in the same image.

```bash
podman build --tag ghcr.io/you/corium:0.3.3-1 --file Containerfile .
podman push ghcr.io/you/corium:0.3.3-1
```

The build ends on the lint. A clean one prints nothing from that step and
commits the image; a failing one names what it found and stops, because
`--fatal-warnings` turns a warning into a non-zero exit rather than something
that scrolls past. Content written under `/var` is the warning to expect, and it
matters: `/var` is seeded at install and never updated, so anything you put
there never reaches a node that upgrades into the image.

**Then sign it. This is not optional hardening — an unsigned image cannot be
upgraded onto a node.**

Corium ships a `/etc/containers/policy.json` that requires a signature for
`ghcr.io/corium-os/corium`, with a default of `insecureAcceptAnything` for
everything else. Before staging an image, a node checks the policy scope that
would apply to it and **refuses any image the policy would accept without
checking who built it**. Your repository falls under the default, so it is
refused:

```
the node's signing policy does not require a signature for this image
```

That is deliberate: it is what stops a typo rebasing a Kubernetes node onto
somebody else's image. The way through it is to sign yours and say so, which
gives your image the same guarantee Corium gives its own:

```bash
cosign generate-key-pair
cosign sign --key cosign.key ghcr.io/you/corium:0.3.3-1
```

Then teach your image to require it, by adding your repository to the policy
inside your own image:

```json
"ghcr.io/you/corium": [
    {
        "type": "sigstoreSigned",
        "keyPath": "/usr/share/containers/your-cosign.pub",
        "signedIdentity": { "type": "matchRepository" }
    }
]
```

The node accepts your image because **you** said so, not because it claims to be
Corium. That is the whole design of the signing policy, and it is why deriving
an image does not mean giving up the guarantee.

### Rolling it out to a fleet that already exists

The policy entry has to be on the nodes before the image that relies on it, so
it takes two rollouts:

1. **Ship the policy first.** Build a derived image that adds only the policy
   entry and the public key, sign it, and upgrade the fleet onto that. It is
   signed, so the nodes accept it.
2. **Then ship everything else.** Your agents, CA bundle and drivers go in the
   next image, which the nodes now hold a policy for.

**Installing is not upgrading.** A machine installed from an ISO or a disk built
from your image never runs that check, because there is no previous node to
refuse anything. New machines are easy; it is the fleet you already have that
needs the order above.

---

## 4. Put it on your nodes

A derived image is an image. Everything that moves a node to a new one works
unchanged:

```bash
cctl upgrade node-a node-b --image ghcr.io/you/corium:0.3.3-1
```

One node at a time, stopping at the first that does not come back on the digest
it was sent to. Rollback is the same `cctl rollback` as ever — the previous
image is still on the disk, which is the other half of why this is safe to do
to a cluster you care about.

For a new machine, build an installer ISO or a disk from your image the way
Corium builds its own; see [downloads](install/downloads.md).

---

## 5. The mistakes, in the order people make them

**Writing to `/var` at build time.** Covered above, and worth repeating because
it is the only one that passes every test you are likely to run.

**Putting a unit in `/etc/systemd/system`.** It works, and then an upgrade
three-way merges `/etc` and you are arguing with a machine about whose file it
is. `/usr/lib/systemd/system` is the image's.

**Forgetting `systemctl enable`.** The unit ships, the node boots, nothing runs,
and it looks like the agent is broken rather than switched off.

**Deriving from `latest`.** Your build stops being reproducible the moment
Corium publishes. Pin the version you tested — `0.3.3`, not `latest` — and move
it deliberately.

**Skipping `bootc container lint`.** It is the cheapest check available and it
knows about this filesystem contract. Corium's own CI would refuse an image
that fails it.

**An agent that fights the node.** `DynamicUser`, `ProtectSystem` and friends
cost nothing here and keep an agent from being the reason a Kubernetes node is
compromised. Corium's own daemons carry them, and say in comments which ones had
to be dropped and why.

---

## What this does not cover

Changing the Kubernetes version. The k0s binary lives in `/usr` and is pinned by
`build/k0s.lock`; replacing it in a derived image is possible and puts the
version skew of your cluster in your hands, which is a larger subject than this
page. See [upgrades](upgrades.md).
