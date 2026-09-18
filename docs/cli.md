# cctl

`cctl` manages Corium nodes: it reads their state, drives their upgrades, and
decides which of them will obey you.

It runs on your machine and never on a node. That is not an arbitrary split —
it holds the private key that owns a fleet, and nothing holding that key belongs
in an operating system image. A node ships `corium-agent`, which does the local
work, and `corium-apid`, which answers `cctl` over the network.

Build it with `mise run build`; there is no release artefact yet.

---

## 1. What it needs to work

Three things, and the order they arrive in matters.

**An operator CA.** The certificate says which authority a node should trust;
the private key beside it signs the certificates you present. Nodes are given
the certificate and never the key.

**A client certificate**, signed by that CA, carrying your role. This is what a
node authenticates on every call.

**A node's fingerprint.** A node signs its own serving certificate — no private
key is ever carried in a Corium configuration, so there is nothing to sign it
with — which means a hostname proves nothing and the fingerprint proves
everything. `cctl` remembers one per node after the first contact.

```console
$ cctl pki init
$ cctl pki issue --role admin
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...
$ cctl status 192.168.1.51
```

---

## 2. From nothing to a managed node

### Make the CA

```console
$ cctl pki init
Created an operator CA in /home/you/.corium

  certificate  /home/you/.corium/operator-ca.pem
  private key  /home/you/.corium/operator-ca.key

The key owns every node that trusts this CA. It never leaves this
machine, and losing it means re-enrolling every node from its console.
```

It then prints the certificate ready to paste into cloud-init, which is all
`api.operatorCA` needs. `pki init` refuses to overwrite an existing CA: doing
that does not lose a file, it loses every node that pinned the old one, each
then needing a visit to its console. Move the directory aside if you mean it.

### Issue yourself a certificate

```console
$ cctl pki issue --role admin
Signed a client certificate for "you" as corium:admin, valid for 2160h0m0s.
```

`--role` is `readonly`, `operator` or `admin` (§4). Ninety days by default:
client certificates are the credential that travels on laptops, so they are the
ones worth keeping short, and reissuing needs no node to be touched.

### Claim a node

A node in maintenance mode prints this on its console, its serial port and its
journal:

```
Corium node is unenrolled and is not in a cluster.

  address       192.168.1.51:7443
  pairing code  K7QM-93XF
  fingerprint   SHA256:tQ2f...9c1a

  cctl enroll 192.168.1.51:7443 --code K7QM-93XF
```

Both values are there for a reason. The code authenticates you to the node; the
fingerprint authenticates the node to you.

```console
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...9c1a
Claimed 192.168.1.51:7443.

The node is restarting to require your client certificate, and its
bootstrap is released: it will now join the cluster its cloud-init
configuration describes.
```

Leaving `--fingerprint` out shows what answered and asks you to confirm it
against the console. It is refused when there is nobody there to ask: a script
that confirms whatever answers has checked nothing while appearing to.

A node whose configuration already names an operator CA never needs this — it
claimed itself at boot. Give `cctl` its fingerprint once, from the journal or
the instance console log, and it is remembered:

```console
$ cctl status 192.168.1.51 --fingerprint SHA256:tQ2f...9c1a
```

---

## 3. The commands

Every command below takes an address, and `--dir` to use an operator directory
other than `~/.corium`. The port defaults to `7443`.

### Reading

| Command | Role | What it does |
|---|---|---|
| `cctl health <node>` | readonly | Says the node answers, and what it authenticated you as |
| `cctl status <node>` | readonly | What the node is: role, cluster, images, k0s, health. `--json` for the reply verbatim |
| `cctl services <node>` | readonly | The units this API knows about, and their state |
| `cctl logs <node>` | readonly | A journal, per unit or across all of them |
| `cctl kubeconfig <node>` | **admin** | The cluster's administrator credentials |

`cctl health` is the one to reach for when something is wrong with your
credentials rather than with the node: it is the smallest call that proves a
certificate works.

```console
$ cctl status 192.168.1.51
192.168.1.51:7443

  hostname     corium-00a7a34c
  role         single
  cluster      apitest

  os           Fedora Linux 44 (Forty Four)
  kernel       7.2.5-200.fc44.x86_64
  booted       ghcr.io/corium-os/corium:0.1
  digest       sha256:bd67161...

  k0s          v1.36.4+k0s.0
  service      k0scontroller.service (running)
  greenboot    passed
  uptime       11m51s
```

A field the node could not determine is left out rather than shown as a dash or
a zero. This is usually read just before doing something irreversible, and a
blank is honest where a placeholder invites a guess. A machine provisioned
without a Corium configuration says so plainly instead of looking broken.

The **digest** is the field an incident turns on: a tag says what was asked
for, a digest says what booted.

### Logs

```console
$ cctl logs 192.168.1.51 --unit k0scontroller --since 15m --lines 200
23:50:11 info    k0scontroller          Starting kube-router
23:50:12 warn    k0scontroller          node not ready: waiting for CNI

$ cctl logs 192.168.1.51 --unit kernel --lines 20
$ cctl logs 192.168.1.51 --follow
```

| Flag | Default | Notes |
|---|---|---|
| `--unit` | every known unit | A unit name, or `kernel` for the kernel's own messages |
| `--lines` | 200 | Capped at 10000 by the node |
| `--since` | — | A duration: `15m`, `2h` |
| `--follow` | off | Streams until you stop it, for at most an hour |

Leaving `--unit` out reads everything the API knows about, which is what you
want before you know where the problem is. Units come from a fixed list — an
API that hands a unit name to `systemctl` can start anything on the machine.

One thing to weigh before handing somebody `corium:readonly`: it reads
journals, and journals are not sanitised.

### Acting on a node

| Command | Role | What it does |
|---|---|---|
| `cctl restart <node> --unit <unit>` | operator | Restarts k0s |
| `cctl cordon <node>` / `--undo` | operator | Stops new pods being scheduled, or reverses it. **Controllers only** |
| `cctl drain <node>` | operator | Cordons, then evicts. **Controllers only** — see below |
| `cctl reboot <node>` | admin | Restarts the machine |
| `cctl shutdown <node>` | admin | Powers it off |

**`cordon` and `drain` work on controllers only**, and since most of a cluster
is workers, that is the common case rather than the rare one. A node acts on
itself and nothing else, and only a controller holds cluster admin credentials
locally — a worker has the kubelet's, which cannot evict a pod.

Draining a worker is a cluster operation rather than a node one, so the tool
for it is `kubectl`, pointed at credentials this CLI will fetch for you:

```console
$ cctl kubeconfig <a controller> > kube.yaml
$ kubectl --kubeconfig kube.yaml drain corium-w1 --ignore-daemonsets --delete-emptydir-data
$ kubectl --kubeconfig kube.yaml uncordon corium-w1
```

The node says as much when you ask it directly:

```console
$ cctl drain 192.168.1.52
cctl: 409 Conflict: only a controller holds cluster admin credentials, and
this node is not one. Draining a worker is a cluster operation: ...
```

Note what this does *not* stop: `cctl upgrade` still works on a worker. The
upgrade path drains where it can and reboots undrained where it cannot, which
is the same behaviour `corium-upgrade-apply.service` has always had.

A drain that cannot finish is **not** forced. It usually means a pod disruption
budget is saying this workload cannot lose a replica right now, which is
exactly when overriding it is wrong — and the node is left cordoned so you can
decide.

Restarting is a shorter list than reading. `corium-bootstrap.service` is
readable and deliberately not restartable: re-running it on a node that has
already joined a cluster destroys data, and no certificate can ask for it.

```console
$ cctl restart 192.168.1.51 --unit corium-bootstrap
cctl: 403 Forbidden: corium-bootstrap.service: this unit is not one the API
will restart (first-boot configuration; runs once)
```

`reboot` and `shutdown` answer before they act. Without a reply you could not
tell "the node refused" from "the node obeyed" — both look like a connection
that died.

### Upgrades

```console
$ cctl upgrade node-1 node-2 node-3 --image ghcr.io/corium-os/corium:0.2
[1/3] node-1:7443
        staged sha256:bbbb2222
        draining and rebooting.......
        up on sha256:bbbb2222
[2/3] node-2:7443
...
```

One node at a time, and it **stops at the first that does not come back** — a
rollout that carries on past a broken machine turns one outage into a
cluster-wide one. The error says how many were upgraded, because a
half-upgraded cluster is a decision somebody has to make.

Before each node it checks the node is fit to lose: bootstrapped, k0s running,
and the last boot not judged bad by greenboot. After each one it checks the
node came back **on the digest it was sent to**, not merely that it answers.

`--settle` is how long to wait for a node to return; fifteen minutes by
default, which is a reboot into a new OS image with k0s starting behind it.

A node refuses an image its own signing policy would accept unsigned:

```console
$ cctl upgrade node-1 --image quay.io/fedora-ostree-desktops/silverblue:44
cctl: 403 Forbidden: the node's signing policy does not require a signature
for this image: quay.io/fedora-ostree-desktops/silverblue:44
```

That is the check that stops a typo rebasing a Kubernetes node onto a desktop.
It is not a label check — anybody can label an image "Corium" — it is
`/etc/containers/policy.json`, which the image ships requiring a cosign
signature for Corium's own repository. Running your own derived images means
adding your repository and key there, which is also how you say you trust them.

```console
$ cctl rollback 192.168.1.51
```

`rollback` marks the previous image as the next to boot and deliberately does
**not** reboot. It exists because somebody is already having a bad day; the
reboot stays yours to schedule. A node that has only ever booted one image has
nowhere to go back to, and says so.

### Getting a kubeconfig

```console
$ cctl kubeconfig 192.168.1.51 > ~/.kube/corium.yaml
$ cctl kubeconfig 192.168.1.51 --output ~/.kube/corium.yaml
```

Standard output by default, and deliberately not `~/.kube/config`: merging into
somebody's existing contexts is a decision with no undo. `--output` writes a
file at `0600` and refuses to replace one without `--force`.

**This is `admin`, and it outranks every other command here.** The rest act on
one machine; this hands over a cluster, to somebody nothing in this API can
take it back from. The node records that it happened in its journal.

The server address it points at is the cluster's virtual IP on an HA control
plane, and otherwise the address you reached the node on. A kubeconfig aimed at
one particular controller stops working the first time that controller does,
which is why the virtual IP wins where there is one.

`--server` overrides it — for a load balancer in front of the control plane, a
name rather than an address, or a port that is not `6443`:

```console
$ cctl kubeconfig 192.168.1.51 --server k8s.example.com
$ cctl kubeconfig 192.168.1.51 --server 192.168.0.200:8443
```

A bare address gets `https://` and `:6443`; a whole URL is left alone.

Only a controller can answer: a worker holds kubelet credentials, which are not
an administrator's, and it says so rather than handing over something that
looks right and is not.

### Handing a node to somebody else

```console
$ cctl pki init --dir ~/.corium-2027 --name "corium operators 2027"
$ cctl ca rotate node-1 node-2 --dir ~/.corium --to ~/.corium-2027
node-1:7443 now obeys the CA in /home/you/.corium-2027
```

`cctl` mints a certificate under the new CA and sends it to each node as proof.
The node verifies it chains to the CA it is being asked to obey, and refuses
otherwise. The failure being guarded against is not recoverable over the
network: rotate to a CA you cannot issue certificates under, and the node will
only ever accept somebody else.

Rotation touches neither cluster membership nor node identity. The fingerprint
you have pinned stays valid; only who may manage it changes.

If the CA's key is *lost* there is nothing to rotate with, and the way back is
the console — `corium-agent api set-ca --file operator-ca.pem`, run as root on
the node itself (§6).

### Erasing a node

```console
$ cctl reset 192.168.1.51
cctl: this erases corium-00a7a34c (192.168.1.51:7443): it leaves its cluster,
forgets its owner and reboots unclaimed. Re-run with --confirm
corium-00a7a34c to mean it

$ cctl reset 192.168.1.51 --confirm corium-00a7a34c
```

The node's own name has to be sent back to it, because an address in a shell's
history is a poor guard against this landing on the wrong machine. It is
checked twice: by `cctl` against what the node calls itself, and by the node.

The order is fixed. Drain, best effort — a node whose cluster has already gone
is exactly the node somebody wants to reset. Then `k0s reset`, which takes it
out of the cluster and wipes `/var/lib/k0s`. Then the bootstrap marker. **Only
then** does the node forget its owner, and last of all it reboots. Doing it the
other way round could leave a cluster member nobody owns.

The serving identity goes too, so the fingerprint remembered here stops
matching — which is the point: a machine handed on with the certificate its
previous owner pinned is one that owner's tooling would still accept.

Two things worth knowing:

- What the node comes back as depends on its configuration. One with
  `api.operatorCA` re-claims itself and re-bootstraps, which is a genuine
  reprovision. One in maintenance mode comes back unclaimed with a new pairing
  code.
- The reboot takes a **staged** image if one is waiting. Check `cctl status`
  first if that matters.

---

## 4. Roles

Carried in the client certificate's organisation, and checked by the node on
every call. Every route names the lowest role that may use it.

| Role | Reaches |
|---|---|
| `corium:readonly` | `health`, `status`, `services`, `logs` |
| `corium:operator` | …and `restart`, `cordon`, `drain`, and an upgrade's *staging* |
| `corium:admin` | …and `reboot`, `shutdown`, `reset`, `rollback`, an upgrade's *apply*, `ca rotate`, `kubeconfig` |

`cctl upgrade` spans both: pulling an image changes nothing else and is
operator work, while applying it takes the node out of service. So an operator
can stage an upgrade and cannot finish one — `cctl upgrade` needs `admin` to
run through.

A certificate signed by the operator CA but carrying no recognised role is
authenticated and *not* authorised. It gets a 403 saying how to reissue it,
because signing a certificate without naming a role is not a way to grant every
role.

---

## 5. Your directory

`~/.corium` by default, `--dir` otherwise. The directory is kept at `0700`
whatever the files inside it are, and narrowed if it was already wider: it
holds the key that owns a fleet.

| File | Mode | What it is |
|---|---|---|
| `operator-ca.pem` | `0644` | The CA certificate. Public — this is what goes in cloud-init |
| `operator-ca.key` | `0600` | The key that owns every node trusting it. Never leaves this machine |
| `client.crt` | `0644` | Your certificate, signed by the CA above |
| `client.key` | `0600` | Its key |
| `config.yaml` | `0600` | Remembered node fingerprints, and nothing else |

`config.yaml` holds fingerprints because a node signs its own certificate.
Passing `--fingerprint` explicitly records it, which is what a node claimed
from cloud-init needs once.

---

## 6. What runs on the node instead

Two things are deliberately not in `cctl`, because they are local by nature.

`corium-agent api set-ca --file <cert>` replaces the operator CA a node obeys.
It is the way back when the key that owns a fleet is lost. It opens no port and
accepts no request — root on the machine already owns it, so it grants nothing
that was not already granted — and the node keeps its cluster membership, which
is what makes it a recovery rather than a reset. It refuses a node nobody has
claimed: installing a CA there would be enrolment walking around the pairing
code.

`corium-agent validate <file>` parses and checks a configuration without
applying it, offline. It is the same code the node runs at boot, so it gives
the same answer on your workstation as on the machine.

---

## 7. What it will not do

| Not there | Why |
|---|---|
| `cctl exec` | The moment an API can run any command it is SSH with a worse client, and every argument for keeping its surface small stops applying |
| `cctl apply-config` | Provisioning is cloud-init's job. A node that needs different configuration is reprovisioned |
| Anything Kubernetes beyond `kubeconfig` | The API hands over the admin kubeconfig once and does nothing else with Kubernetes. It does not proxy the apiserver, list pods, or keep credentials for you |
| A fleet inventory | `cctl` acts on addresses you supply. There is no registry, no desired state, and nothing that reconciles |

The last one is the design, not an omission: a node knows only about itself, and
the sequencing that needs to know about the others lives here rather than on any
node. See [ADR 4](adr/0004-management-api.md).

---

## 8. Reading a refusal

The status codes are chosen so that the code alone tells you where to look.

| Code | Means | Look at |
|---|---|---|
| `400` | The request is malformed | What you typed |
| `401` | A pairing code did not match | The node's console |
| `403` | Authenticated, not authorised — or the node refuses on policy | Your role, or the image |
| `409` | Nothing is wrong; the node cannot do it in this state | The node: unclaimed, nothing staged, no rollback, a worker asked to drain |
| `500` | The node failed at it | Its journal: `cctl logs <node> --unit corium-apid` |

A `409` is worth dwelling on, because it is the one that is easy to read as a
fault and usually is not. A node with nothing staged, or with no earlier image
to roll back to, or a worker that holds no credentials able to evict a pod, are
all ordinary states rather than problems.
