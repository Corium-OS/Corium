# Building your own image

The image that Corium publishes is a base. You derive your own image from it to
add things that a `corium:` block does not cover. Examples are a monitoring
agent, the CA bundle of your organization, a driver, or a backup client. You use
an ordinary `Containerfile`: a `FROM`, the things that you add, and a lint. The
example below is about thirty lines. The parts that need careful thought are
signing and the order of the rollout.

A derived image is also the supported method. If you install an agent on a
running node with `dnf`, it fails, because `/usr` is read-only. If you install
it into `/var` with a `runcmd`, it works only until the next upgrade, because
nothing puts it back. An agent that is part of the image survives upgrades.

## Before you start

- `podman` on the machine where you build, and
  [`cosign`](https://docs.sigstore.dev/) to sign the result.
- A registry that you can push to, and that your nodes can pull from.
- An answer to one question: **do you already have nodes that run Corium, or
  only machines that you did not install yet?** An existing fleet must get the
  signing policy one image before the agents that need it. New machines do not
  need this. See [3. Build it, and sign it](#3-build-it-and-sign-it).

---

## 1. The one rule that decides everything

The filesystem contract of Corium, from [concepts](concepts.md), decides if a
derived image works or fails without an error:

| | Who owns it | What that means for you |
|---|---|---|
| `/usr` | the image, read-only | **Put your things here.** Upgrades replace this directory, so its content moves forward with the node |
| `/etc` | the machine, merged on upgrade | Put defaults here only when no other location works. An edit by an operator always wins over yours |
| `/var` | the machine, **seeded at install and never updated by an image** | Write nothing here at build time |

The last row is the most expensive mistake, because nothing fails. A file that
you `COPY` into `/var` reaches every machine *installed* from your image. It
reaches no machine *upgraded* into it. Your new test node works correctly. The
fleet that you upgrade never gets the file, and nothing tells you.

Declare state directories in `tmpfiles.d` instead. systemd creates them at every
boot. Corium follows this rule for its own state, and the rule is necessary.

---

## 2. A worked example: adding an agent

In this example, you want `node_exporter` on every node.

```dockerfile
FROM ghcr.io/corium-os/corium:0.5.0

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

Put `node-exporter.service` next to it:

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

If your agent needs its own directory, declare it. Do not create it:

```
# /usr/lib/tmpfiles.d/node-exporter.conf
d /var/lib/node-exporter 0750 root root -
```

---

## 3. Build it, and sign it

> **Warning.** If you already have a fleet, use the two-stage rollout below. Do
> not put the policy and your agents in the same image. A node enforces the
> policy that it runs now, not the policy in the image that it moves to. The
> entry that accepts your repository must reach a node *before* the upgrade
> that needs it. If you do this in the wrong order, the nodes refuse every image
> that you build, and you can fix them only from the console.

```bash
podman build --tag ghcr.io/you/corium:0.5.0-1 --file Containerfile .
podman push ghcr.io/you/corium:0.5.0-1
```

The build ends with the lint. If the lint passes, that step prints nothing and
the build commits the image. If the lint fails, it names the problem and stops.
`--fatal-warnings` turns a warning into a non-zero exit, so the warning does not
scroll past. The usual warning is content written under `/var`, and it is
important. The install seeds `/var` and no upgrade updates it. Anything that you
put there never reaches a node that upgrades into the image.

**Then sign it. This step is required. You cannot upgrade a node onto an
unsigned image.**

Corium ships a `/etc/containers/policy.json` that requires a signature for
`ghcr.io/corium-os/corium`. For all other images, the default is
`insecureAcceptAnything`. Before a node stages an image, it reads the policy
scope that applies to that image. **The node refuses any image that the policy
accepts without a check of who built it**. Your repository uses the default, so
the node refuses it:

```
the node's signing policy does not require a signature for this image
```

This is intentional. It stops a typo from rebasing a Kubernetes node onto an
image from another person. To get past it, sign your image and declare the
signature in the policy. Your image then gets the same guarantee that Corium
gives its own image:

```bash
cosign generate-key-pair
cosign sign --key cosign.key ghcr.io/you/corium:0.5.0-1
```

Then make your image require the signature. Add your repository to the policy
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

The node accepts your image because **you** told it to, not because the image
claims to be Corium. This is the design of the signing policy. For this reason,
a derived image keeps the guarantee.

### Rolling it out to a fleet that already exists

The policy entry must be on the nodes before the image that needs it. For this
reason, you do two rollouts:

1. **Ship the policy first.** Build a derived image that adds only the policy
   entry and the public key. Sign it, and upgrade the fleet onto it. The image
   is signed, so the nodes accept it.
2. **Then ship everything else.** Put your agents, CA bundle, and drivers in the
   next image. The nodes now have a policy for it.

**Installing is not upgrading.** A machine installed from an ISO or a disk built
from your image never does that check, because no previous node exists to
refuse anything. New machines are easy. Only the fleet that you already have
needs the order above.

---

## 4. Put it on your nodes

A derived image is an image. All the tools that move a node to a new image work
without changes:

```bash
cctl upgrade node-a node-b --image ghcr.io/you/corium:0.5.0-1
```

The command upgrades one node at a time. It stops at the first node that does
not come back on the digest that it received. Rollback uses the same
`cctl rollback` as before. The previous image is still on the disk, so this
procedure is safe on an important cluster.

For a new machine, build an installer ISO or a disk from your image, the same
way that Corium builds its own. See [downloads](install/downloads.md).

---

## 5. The mistakes, in the order people make them

**Writing to `/var` at build time.** The section above explains this mistake.
It is here again because it is the only mistake that passes all the usual tests.

**Putting a unit in `/etc/systemd/system`.** It works at first. Then an upgrade
does a three-way merge of `/etc`, and it is not clear if the file belongs to you
or to the machine. `/usr/lib/systemd/system` belongs to the image.

**Forgetting `systemctl enable`.** The unit ships and the node boots, but nothing
runs. The agent looks broken, but it is only turned off.

**Deriving from `latest`.** Your build stops being reproducible when Corium
publishes a new release. Pin the version that you tested, `0.5.0` and not
`latest`, and change it only on purpose.

**Skipping `bootc container lint`.** It is the cheapest check available, and it
knows this filesystem contract. The CI of Corium refuses an image that fails it.

**An agent that fights the node.** `DynamicUser`, `ProtectSystem`, and similar
settings cost nothing here. They stop an agent from becoming the way into a
Kubernetes node. The daemons of Corium use them. Comments in their units say
which settings they removed, and why.

---

## What this does not cover

This page does not cover a change of the Kubernetes version. The k0s binary is
in `/usr`, and `build/k0s.lock` pins it. You can replace it in a derived image.
Then you are responsible for the version skew of your cluster, which is a larger
subject than this page. See [upgrades](upgrades.md).
