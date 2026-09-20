# cctl

`cctl` manages Corium nodes: it reads their state, drives their upgrades, and
decides which of them will obey you. This page tracks `main`. The current
stable release is **0.3.0**, and commands added since are marked
*new since 0.3.0*.

| Command | Role | What it does | Section |
|---|---|---|---|
| `cctl pki init` | none; local | Creates the operator CA this fleet will trust | [The operator PKI](#the-operator-pki) |
| `cctl pki issue` | none; local | Signs a client certificate for yourself | [The operator PKI](#the-operator-pki) |
| `cctl enroll` | a pairing code, not a role | Claims an unenrolled node, using the code on its console | [Claim a node](#claim-a-node) |
| `cctl apply` | `corium:admin` | Gives a node the `corium:` document it bootstraps with, and re-applies the safe part of one afterwards | [Applying a configuration](#applying-a-configuration) |
| `cctl status` | `corium:readonly` | What the node is: role, cluster, images, k0s, health | [Reading](#reading) |
| `cctl services` | `corium:readonly` | The units this API knows about, and their state | [Reading](#reading) |
| `cctl logs` | `corium:readonly` | A journal, per unit or across all of them | [Logs](#logs) |
| `cctl health` | `corium:readonly` | Says the node answers, and what it authenticated you as | [Reading](#reading) |
| `cctl restart` | `corium:operator` | Restarts k0s | [Acting on a node](#acting-on-a-node) |
| `cctl cordon` | `corium:operator` | Stops new pods being scheduled, or reverses it. **Controllers only** | [Acting on a node](#acting-on-a-node) |
| `cctl drain` | `corium:operator` | Cordons, then evicts. **Controllers only** | [Acting on a node](#acting-on-a-node) |
| `cctl upgrade` | `corium:operator` to stage, `corium:admin` to finish | Moves nodes to another OS image, one at a time | [Upgrades](#upgrades) |
| `cctl rollback` | `corium:admin` | Marks a node's previous image as the next to boot | [Upgrades](#upgrades) |
| `cctl reboot` | `corium:admin` | Restarts the machine | [Acting on a node](#acting-on-a-node) |
| `cctl shutdown` | `corium:admin` | Powers it off | [Acting on a node](#acting-on-a-node) |
| `cctl reset` | `corium:admin` | Erases a node: it leaves its cluster, forgets its owner and reboots | [Erasing a node](#erasing-a-node) |
| `cctl access ssh` | `corium:readonly` to list, `corium:admin` to add or revoke | The SSH keys the API trusts for a user that already exists | [SSH access](#ssh-access) |
| `cctl ca rotate` | `corium:admin` | Hands nodes to a different operator CA | [Handing a node to somebody else](#handing-a-node-to-somebody-else) |
| `cctl kubeconfig` | `corium:admin` | The cluster's administrator credentials | [Getting a kubeconfig](#getting-a-kubeconfig) |
| `cctl worker-config` | `corium:admin` | Mints a join token on a controller and prints a worker's `corium:` block | [Generating a worker's configuration](#generating-a-workers-configuration) |
| `cctl version` | none; local | Prints the version and the commit it was built from | [Version](#version) |

`cctl <command> -h` prints one command's flags, and `cctl -h` prints this list
as the binary itself knows it.

## Where it runs, and how to install it

It runs on your machine and never on a node. That is not an arbitrary split —
it holds the private key that owns a fleet, and nothing holding that key belongs
in an operating system image. A node ships `corium-agent`, which does the local
work, and `corium-apid`, which answers `cctl` over the network.

Install it with `mise use -g 'github:Corium-OS/Corium[exe=cctl]@0.3.0'`. The
quotes matter in zsh, and the version is not optional. The other route is an
archive from the
[release page](https://github.com/Corium-OS/Corium/releases/latest), and
[downloads](install/downloads.md#installing-cctl) covers both, including how to
verify what you fetched. From a checkout, `mise run build`.

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

```text
Corium node is unenrolled and is not in a cluster.

  address       192.168.1.51:7443
  pairing code  K7QM-93XF
  fingerprint   SHA256:tQ2f...9c1a

  cctl enroll 192.168.1.51:7443 --code K7QM-93XF
```

Both values are there for a reason. The code authenticates you to the node; the
fingerprint authenticates the node to you.

You will also find it above the login prompt on the node's screen — the
hypervisor console, the IPMI view, whatever you have — because a one-shot line
scrolls away and an unclaimed node is one you cannot log in to anyway. It is
removed the moment the node is claimed, so a prompt never advertises a code
that no longer works.

```console
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...9c1a
Claimed 192.168.1.51:7443.

The node is restarting to require your client certificate, and its
bootstrap is released: it will now join the cluster its cloud-init
configuration describes.
```

| Flag | Default | Notes |
|---|---|---|
| `--code` | — | The pairing code printed on the node's console. A node with `api.insecure` set asks for none |
| `--fingerprint` | — | The fingerprint printed beside it. Without this you are asked to confirm what answered |
| `--config` | — | A `corium:` document to give the node as part of claiming it; it bootstraps with this instead of what it booted with |
| `--yes` | off | Claim a node that will bootstrap the moment it is claimed, without being asked to confirm it |

Leaving `--fingerprint` out shows what answered and asks you to confirm it
against the console. It is refused when there is nobody there to ask: a script
that confirms whatever answers has checked nothing while appearing to.

### Claiming a node that already knows what it is

Claiming is what releases a held bootstrap. So a node whose own cloud-init
names a `role` does not sit and wait once you have claimed it — it starts
becoming that node, and getting back out means `cctl reset`, a drain and a
reboot.

The node says so rather than doing it on your behalf:

```console
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...9c1a

claiming this node releases its bootstrap, and the document it booted with
names a role: it becomes a single node named "ks-stor" in cluster "corium"
immediately, which cannot be undone without cctl reset. Send the configuration
it should build instead, or claim it again saying you meant this.

Claim it anyway? [y/N]
```

Answering `y` claims it. `--config` is the other answer: send the document it
should build instead, and the question does not arise, because you have said
what you want. Nothing is claimed while the question is open and the pairing
code is not spent, so either answer is free.

**A node waiting to be told what it is — one whose document names no role — is
claimed without any of this**, because claiming it builds nothing. That is the
fleet pattern (see [configuration §3.2](reference.md#32-role)), and a prompt on
the recommended path would only teach people to dismiss it.

In a script, pass `--yes`. Without a terminal the refusal is returned rather
than asked, and it names the flag.

A node whose configuration already names an operator CA never needs this — it
claimed itself at boot. Give `cctl` its fingerprint once, from the journal or
the instance console log, and it is remembered:

```console
$ cctl status 192.168.1.51 --fingerprint SHA256:tQ2f...9c1a
```

### Tell a node what it is

Claiming a node says who owns it. It does not say what it should become — that
comes from the document it booted with, which for a machine with no cloud-init
datasource is nothing at all.

`--config` closes that gap, and a node told to wait for one holds its bootstrap
until it arrives:

```yaml
# The whole cloud-config, identical on every machine in the fleet. It names no
# role, which is how a node says it is waiting to be told what it is.
corium:
  api:
    enabled: true
```

```console
$ cctl enroll 192.168.1.51 --code K7QM-93XF --fingerprint SHA256:tQ2f...9c1a \
    --config controller-01.yaml
Claimed 192.168.1.51:7443.

The node is restarting to require your client certificate, and its
bootstrap is released: it will now join the cluster the configuration
you just sent describes.
```

The document travels **with** the claim rather than after it, and that ordering
is the feature: claiming a node is what releases its bootstrap, so a document
sent a moment later would be racing a machine that has already started becoming
something.

For a node that is already claimed, and for a node that has already
bootstrapped, the command is `cctl apply`:
[Applying a configuration](#applying-a-configuration).

---

## 3. The commands

Most of these take a node's address, and the port defaults to `7443`. Three
take no address at all, because they never leave your machine: `cctl pki init`,
`cctl pki issue` and `cctl version`.

Every command that does reach a node shares two flags, so they are listed here
once rather than in each table below:

| Flag | Default | Notes |
|---|---|---|
| `--dir` | `~/.corium` | The operator directory to read credentials from (§5). On every command except `cctl version` |
| `--fingerprint` | the remembered one | Overrides what `config.yaml` holds for this address, and records it |

`cctl upgrade` and `cctl ca rotate` take `--dir` and not `--fingerprint`: both
take a list of nodes, where a single fingerprint would mean nothing.

### The operator PKI

These two make the credentials every other command presents. They reach no node
and need no role, which is the point — they are what a role comes from.

```console
$ cctl pki init
$ cctl pki issue --role admin
```

`cctl pki init`:

| Flag | Default | Notes |
|---|---|---|
| `--dir` | `~/.corium` | Where to create the CA. It refuses to overwrite one |
| `--name` | `corium operators` | Common name for the CA certificate |

`cctl pki issue`:

| Flag | Default | Notes |
|---|---|---|
| `--dir` | `~/.corium` | The directory holding the CA to sign with |
| `--role` | `admin` | `readonly`, `operator` or `admin` (§4) |
| `--name` | your `$USER`, or `corium operator` | Common name for the certificate |
| `--lifetime` | `2160h` — ninety days | How long the certificate is valid |

Both are shown in §2, which is where the reasoning behind them sits.

### Applying a configuration

`cctl apply` gives a node the `corium:` document it will bootstrap with, and on
a node that has already bootstrapped it re-applies the part of that document
which is safe to change in service. *Re-applying on a bootstrapped node is new
since 0.2.0; before that it was a flat refusal.*

```console
$ cctl apply 192.168.1.51 --file controller-01.yaml
Applied to 192.168.1.51:7443.

  written to  /etc/corium/config.yaml
  role        controller+worker

The node bootstraps with this the next time its bootstrap runs --
now, if it was holding for one.
```

| Flag | Default | Notes |
|---|---|---|
| `--file` | — | Required. The document to send; `-` reads standard input |

The file may be a whole cloud-config with a `corium:` key or the block on its
own. It is validated on your machine before it is sent and again by the node
before it is written, so a document that would fail at boot is refused while
somebody is still watching.

> **Warning.** The document is the node's entire configuration, not a patch.
> Leaving `api:` out of it turns the management API off at the next boot, and
> `cctl apply` says so when it spots it.

What the command does depends on where the node is in its life:

| The node | What `cctl apply` does |
|---|---|
| Has not bootstrapped | `applied`: the document is written, and is what the node will bootstrap with |
| Has bootstrapped, and only the add-on set differs | `reconciled`, or `unchanged` when the document matches what it is already running |
| Has bootstrapped, and an identity field differs | `409`, naming the field it refused and pointing at `cctl reset` |

Rewriting what a node *is* while it is in service would leave its configuration
and its behaviour saying two different things, which is the thing Corium's
provisioning model exists to prevent. See
[ADR 8](adr/0008-day-two-reconcile.md).

The identity fields are `role`, `cluster`, `join`, `node`, `network`, `storage`,
`raid`, `wireguard`, `ha`, `upgrades`, `api` and `k0s`. The safe subset today is
`addons` alone, and it is expected to grow. A reconcile regenerates the node's
k0s configuration and cycles the control plane so k0s installs or updates the
chart, and the change is live rather than deferred to the next boot; only a
controller does that, because a worker's charts are declared by the controllers
and rewriting its copy would change nothing it runs. The whole rule is enforced
by the node, not by `cctl` and not by your role — an `admin` certificate does
not get past it either.

Two edges are worth knowing. **Removing** an add-on is refused, not applied:
k0s installs a chart from its configuration but does not uninstall one dropped
from it, so a node that reported the removal done would be lying — delete the
release with `kubectl delete chart <name> -n kube-system` and drop it from the
document, and it stays out at the next bootstrap. And a node **bootstrapped by
a release older than this feature** has no record of what it was built with, so
its first day-two apply is refused until it is re-bootstrapped through
`cctl reset`; a node bootstrapped since records that baseline itself.

### Reading

| Command | Role | What it does |
|---|---|---|
| `cctl health <node>` | `corium:readonly` | Says the node answers, and what it authenticated you as |
| `cctl status <node>` | `corium:readonly` | What the node is: role, cluster, images, k0s, health |
| `cctl services <node>` | `corium:readonly` | The units this API knows about, and their state |
| `cctl logs <node>` | `corium:readonly` | A journal, per unit or across all of them |
| `cctl kubeconfig <node>` | `corium:admin` | The cluster's administrator credentials |

`cctl status` is the only one of these with a flag of its own:

| Flag | Default | Notes |
|---|---|---|
| `--json` | off | Prints the node's reply verbatim, for something other than a person to read |

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
| `--lines` | 200, applied by the node | Capped at 10000 by the node |
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
| `cctl restart <node> --unit <unit>` | `corium:operator` | Restarts k0s |
| `cctl cordon <node>` / `--undo` | `corium:operator` | Stops new pods being scheduled, or reverses it. **Controllers only** |
| `cctl drain <node>` | `corium:operator` | Cordons, then evicts. **Controllers only** — see below |
| `cctl reboot <node>` | `corium:admin` | Restarts the machine |
| `cctl shutdown <node>` | `corium:admin` | Powers it off |

| Flag | Default | Notes |
|---|---|---|
| `--unit` | — | Required by `cctl restart`. `cctl services <node>` lists what may be restarted |
| `--undo` | off | `cctl cordon` only: puts the node back into service |

`cctl drain`, `cctl reboot` and `cctl shutdown` take the two shared flags and
nothing else.

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

### SSH access

The API grants no shell of its own (§7), but it can trust an SSH key for a user
that **already exists** on the node, so you can get a shell the ordinary way
when the surfaces above are not enough. It never creates the account: the user,
its password and its shell are cloud-init's, and Corium manages only a key file
of its own, beside the user's own `~/.ssh`.

| Command | Role | What it does |
|---|---|---|
| `cctl access ssh add <node> --user <user>` | `corium:admin` | Trusts a public key for an existing user |
| `cctl access ssh list <node>` | `corium:readonly` | Lists trusted keys, by fingerprint |
| `cctl access ssh revoke <node> --key-fingerprint <fp>` | `corium:admin` | Stops trusting a key |

| Flag | Default | Notes |
|---|---|---|
| `--user` | — | Required by `add`. The user must already exist on the node |
| `--key-file` | standard input | Read the public key from this file |
| `--key` | — | The public key itself, inline, instead of a file |
| `--key-fingerprint` | — | Required by `revoke`. The SHA256 fingerprint that `list` prints |

> **Warning.** `add` is **admin** because trusting a key grants a shell, the one
> thing that steps outside the guard rails the rest of the API keeps. The node
> logs it by fingerprint. Listing is **readonly**: a public key is not a secret.

```console
$ cctl access ssh add 192.168.1.51 --user core --key-file ~/.ssh/id_ed25519.pub
trusted SHA256:PZ8s… (ssh-ed25519) for core

$ cctl access ssh list 192.168.1.51
  core             ssh-ed25519     SHA256:PZ8s…  you@laptop

$ cctl access ssh revoke 192.168.1.51 --key-fingerprint SHA256:PZ8s…
revoked SHA256:PZ8s… for core
```

The key comes from `--key-file`, or `--key` inline, or standard input — so
`cctl access ssh add <node> --user core < key.pub` works, and so does piping
`ssh-add -L`.

The user must exist first. A key for an account cloud-init never created is a
`409`, with the way round it:

```console
$ cctl access ssh add 192.168.1.51 --user ghost --key-file key.pub
cctl: 409 Conflict: "ghost": no such user on this node; create it with
cloud-init before adding a key for it
```

Corium keeps these keys in a file of its own, so `list` and `revoke` never touch
a key you put in the user's `~/.ssh/authorized_keys` by hand, and `cctl reset`
removes every key the API was trusting. See ADR 5.

### Upgrades

| Flag | Default | Notes |
|---|---|---|
| `--image` | — | Required. The image to move the nodes to |
| `--settle` | `15m` | How long to wait for one node to come back; a reboot into a new OS image with k0s starting behind it |

> **Warning.** `cctl upgrade` takes one node at a time and **stops at the first
> that does not come back**. A rollout that carries on past a broken machine
> turns one outage into a cluster-wide one. The error says how many were
> upgraded, because a half-upgraded cluster is a decision somebody has to make.

```console
$ cctl upgrade node-1 node-2 node-3 --image ghcr.io/corium-os/corium:0.3
[1/3] node-1:7443
        staged sha256:bbbb2222
        draining and rebooting.......
        up on sha256:bbbb2222
[2/3] node-2:7443
...
```

Before each node it checks the node is fit to lose: bootstrapped, k0s running,
and the last boot not judged bad by greenboot. After each one it checks the
node came back **on the digest it was sent to**, not merely that it answers.

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
nowhere to go back to, and says so. It takes the two shared flags and nothing
else.

### Getting a kubeconfig

| Flag | Default | Notes |
|---|---|---|
| `--server` | the cluster's virtual IP, or the address you reached the node on | What clients should reach the control plane on |
| `--output` | standard output | Writes a file at `0600` |
| `--force` | off | Replace an existing `--output` file |

> **Warning.** This is `admin`, and it outranks every other command here. The
> rest act on one machine; this hands over a cluster, to somebody nothing in
> this API can take it back from. The node records that it happened in its
> journal.

```console
$ cctl kubeconfig 192.168.1.51 > ~/.kube/corium.yaml
$ cctl kubeconfig 192.168.1.51 --output ~/.kube/corium.yaml
```

Standard output by default, and deliberately not `~/.kube/config`: merging into
somebody's existing contexts is a decision with no undo. `--output` writes a
file at `0600` and refuses to replace one without `--force`.

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

### Generating a worker's configuration

*New in 0.3.0.*

| Flag | Default | Notes |
|---|---|---|
| `--expiry` | `1h` | How long the minted join token stays valid; a positive duration, checked before the round trip |
| `--name` | — | `node.name` for the generated worker |
| `--label` | — | A node label, `key=value`; repeat the flag for more than one |

> **Warning.** The token is inline in the clear — that is the point — so the
> block this prints is a secret. It goes to standard output alone, so
> `> worker.yaml` writes only YAML, and the reminder goes to standard error.

> **Warning.** This is `admin`, and only a controller can answer: the token is
> minted by k0s, which a worker does not run. A worker says so rather than
> failing obscurely.

```console
$ cctl worker-config 192.168.1.51 --name w-1 --label corium.dev/pool=general > worker.yaml
Minted a worker join token on 192.168.1.51:7443, valid 1h. It is inline in the
block above -- treat the output as a secret, and mint a fresh one once it expires.
```

It asks a controller to mint a fresh worker join token — `k0s token create` on
your behalf — and prints the `corium:` block for a new worker to standard
output:

```yaml
corium:
  role: worker
  node:
    name: w-1
    labels:
      corium.dev/pool: general
  join:
    token: <a freshly minted worker token, inline>
```

The block is the worker's alone to paste into a cloud-config; its users, SSH
keys and anything else stay yours to add.

### Handing a node to somebody else

| Flag | Default | Notes |
|---|---|---|
| `--to` | — | Required. A second operator directory holding the CA to move to, as made by `cctl pki init --dir` |
| `--role` | `admin` | The role of the certificate minted under the new CA |
| `--lifetime` | `2160h` — ninety days | How long that certificate is valid |

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

| Flag | Default | Notes |
|---|---|---|
| `--confirm` | — | Required. The node's own name, which it checks before erasing itself |

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

### Version

```console
$ cctl version
cctl 0.3.0 (b853821)
```

The version and the short commit are stamped in at link time. A release binary
reports its tag with the leading `v` removed; a binary built from a checkout
reports what `git describe --tags --always --dirty` says, so a build from an
uncommitted tree announces itself as dirty. It takes no flags, reads no
operator directory and reaches no node, which makes it the one command that
works before anything else is set up.

---

## 4. Roles

Carried in the client certificate's organisation, and checked by the node on
every call. Every route names the lowest role that may use it. This table is
the authoritative one; the role columns in §3 repeat it.

| Role | Reaches |
|---|---|
| `corium:readonly` | `health`, `status`, `services`, `logs`, `access ssh list` |
| `corium:operator` | …and `restart`, `cordon`, `drain`, and an upgrade's *staging* |
| `corium:admin` | …and `apply`, `reboot`, `shutdown`, `reset`, `rollback`, an upgrade's *apply*, `ca rotate`, `kubeconfig`, `worker-config`, `access ssh add`/`revoke` |

`corium:readonly` is the spelling the certificate carries and the one a node
checks for. `cctl pki issue --role` and `cctl ca rotate --role` take the short
form — `readonly`, `operator`, `admin` — and write the full name in.

Three commands sit outside this. `cctl enroll` is made before any trust exists,
so the pairing code stands in for a role. `cctl pki` and `cctl version` reach no
node at all.

`cctl upgrade` spans two roles: pulling an image changes nothing else and is
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
| Rewriting what a node *is*, in service | A node already running Kubernetes cannot have its role, cluster, name, network or disks rewritten underneath it. `cctl apply` re-applies the safe subset (the add-on set) day-two and refuses each of those, naming it; changing one is `cctl reset`. See [ADR 8](adr/0008-day-two-reconcile.md) |
| A reconcile loop | Nothing watches a document and corrects a running node toward it. `cctl apply` re-applies the safe subset when you ask it to, once, and does nothing between one apply and the next |
| Anything Kubernetes beyond `kubeconfig` | The API hands over the admin kubeconfig once and does nothing else with Kubernetes. It does not proxy the apiserver, list pods, or keep credentials for you |
| A fleet inventory | `cctl` acts on addresses you supply. There is no registry and no desired state; a rolling change is a loop over the addresses you name |

The last one is the design, not an omission: a node knows only about itself, and
the sequencing that needs to know about the others lives here rather than on any
node. See [ADR 4](adr/0004-management-api.md).

Granting SSH access is not a counter-example. `cctl access ssh` (§3) trusts a
key for a user that already exists, but the API still runs no command itself:
the shell that follows is the operating system's `sshd`, and all the API decides
is whose key it will read. See ADR 5.

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

What `cctl` itself returns to the shell is a much shorter list, which matters if
you are scripting it:

| Exit code | When |
|---|---|
| `0` | The command did what it said. `cctl -h` and `cctl <command> -h` exit `0` too |
| `1` | Every failure, whatever caused it, with `cctl: <what went wrong>` on standard error |
| `2` | A flag `cctl` does not recognise. This comes from Go's flag package, before the command runs |

There is no per-cause exit code. A `403` from the node, an address that never
answered and a missing client certificate all exit `1`, so a script that needs
to tell them apart has to read the message or the status code in it.

A handshake that fails before any of those is its own case:

```console
$ cctl status 192.168.1.51
cctl: 192.168.1.51:7443 does not accept your certificate: it is signed by a CA
this node no longer trusts.
```

The raw TLS alert for this is `certificate required`, which reads as though the
client sent nothing. It usually did — Go sends no certificate at all when the
one it holds was signed by a CA the server did not name as acceptable — so the
real cause is a CA that was rotated out from under this directory. Use the
directory it was rotated to, or recover the node from its console with
`corium-agent api set-ca`.

A `409` is worth dwelling on, because it is the one that is easy to read as a
fault and usually is not. A node with nothing staged, or with no earlier image
to roll back to, or a worker that holds no credentials able to evict a pod, are
all ordinary states rather than problems.
