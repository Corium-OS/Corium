# An appliance image

A Corium image that carries its own configuration, so a machine built from it
boots into a management API and waits to be told what it is. No cloud-init, no
console visit, no pairing code — you install it and then drive it with `cctl`.

It is built here rather than published, because what makes it convenient is
also what makes it dangerous, and that should be a decision somebody makes
rather than an image somebody downloads.

---

## Read this first

`config.yaml` sets `api.insecure`. **The first client to reach port 7443 owns
the machine for the rest of its life.** Not just once: the CA it pins is the CA
the node obeys from then on, and rotating it needs a certificate signed by that
same CA. Recovery is a trip to the console.

What keeps it from being reckless is the rule the rest of Corium enforces
anyway — an unclaimed node is in no cluster, so whoever wins the race gets a
bare machine rather than your workloads. But they get its future.

Build this for a bench, a lab, or a provisioning network you control end to
end. Do not build it for anything reachable from a network you do not.

**If you have an operator CA, you do not need any of this.** Bake
`api.operatorCA` into `config.yaml` instead of `api.insecure`: a CA certificate
is not a secret, it is safe in an image, and it gives you the same zero-touch
install with no unauthenticated port at all. That is the better recipe whenever
it is available to you, and the only reason it is not the default here is that
it needs a CA you have already made.

---

## Building it

```bash
# The image, derived from whichever Corium release you want to base it on.
IMAGE=ghcr.io/corium-os/corium:0.3.6 mise run appliance-image

# An installer ISO from it.
IMAGE=localhost/corium-appliance:dev mise run artefact-anaconda-iso
```

Both need a Linux host with podman, like every other artefact task.

The image you build is **not signed**. Corium's own `/etc/containers/policy.json`
requires a signature for `ghcr.io/corium-os/corium`, and your image is not in
that scope, so it installs fine — but a node later pointed at it with
`bootc upgrade` falls back to the default policy. If you intend to upgrade
nodes onto images you build, add your own scope and key to that file; see
[docs/upgrades.md](../../docs/upgrades.md).

---

## The ISO works. The qcow2 needs care.

This is the part worth knowing before you spend twenty minutes on a build.

A node resolves its configuration from a chain of sources, and **the chain
stops at the first source that yields a document** — not at the first document
that contains a `corium:` block. `/usr/share/corium/config.yaml` is last in
that chain, so anything earlier wins.

- **An installer ISO on bare metal has no cloud-init datasource.** Nothing
  earlier in the chain yields anything, the baked-in configuration is read, and
  the machine comes up waiting. This is the case the recipe is for.

- **A qcow2 on a hypervisor or a cloud usually does.** cloud-init writes
  `/var/lib/cloud/instance/cloud-config.txt` whenever it is given any user-data
  at all — including user-data that only adds an SSH key. That file is
  non-empty, so it wins the chain, and having no `corium:` key it ends the
  search rather than falling through. The baked-in configuration is never read,
  and the node boots and does nothing:

  ```
  msg="configuration found" source=cloud-init
  msg="no corium configuration found, leaving node unconfigured"
  ```

  So a qcow2 built from this image only behaves as an appliance if you boot it
  with **no cloud-init user-data whatsoever**. For an appliance managed entirely
  over the API that is reasonable — you were not going to SSH into it — but it
  has to be deliberate.

  Where you do want cloud-init, put the `corium:` block in the user-data and
  skip this recipe: that is what `docs/examples/awaiting-config.yaml` is.

The precedence is not an accident. A document without a `corium:` block is a
statement that this machine is not a Corium node, and falling through to a
baked-in default would mean a typo in a key name could silently enrol a machine
into a cluster nobody pointed it at.

---

## Using one

```console
$ cctl enroll 192.168.1.60            # no --code: the node asks for nothing
$ cctl apply 192.168.1.60 --file controller-01.yaml
```

Or in one step, which is what you want when the node is holding for a
configuration anyway:

```console
$ cctl enroll 192.168.1.60 --config controller-01.yaml
```

`cctl status` reports for the rest of that node's life that its ownership was
established with nothing proved, and rotating the CA does not clear it. That is
on purpose: somebody auditing a fleet is entitled to find the machines that
were claimed this way.
