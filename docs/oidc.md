# OIDC login

Corium does not model the kube-apiserver's flags, so authentication is one of
the things it leaves to the [`k0s.patch`](reference.md#315-k0spatch--the-escape-hatch)
escape hatch. This page wires an OpenID Connect provider (Keycloak, Authentik,
Dex, Okta, Google, …) to a cluster's API server, so `kubectl` logs people in
through it instead of sharing an admin certificate.

Since 0.3.5 this can be applied to a node **already in service**: a change under
`k0s` is reconciled by `cctl apply`, which re-renders `/etc/k0s/k0s.yaml` and
restarts the control plane. See [ADR 8](adr/0008-day-two-reconcile.md).

## Configure the API server

Add the OIDC flags under `k0s.patch` in the node's `corium:` document:

```yaml
corium:
  role: single
  k0s:
    patch:
      spec:
        api:
          extraArgs:
            oidc-issuer-url: https://id.example.com/realms/main
            oidc-client-id: kubernetes
            oidc-username-claim: sub
            oidc-username-prefix: "-"
            oidc-groups-claim: groups
```

- `oidc-issuer-url` — the provider's issuer, exactly as it appears in the token's
  `iss` claim (trailing slash included). The API server must be able to reach its
  discovery and JWKS endpoints.
- `oidc-client-id` — the OAuth client you created for Kubernetes; the token's
  `aud` must contain it.
- `oidc-username-claim` — which claim becomes the user's name.
  `oidc-username-prefix: "-"` disables the `issuer#` prefix Kubernetes otherwise
  prepends to a non-`email` claim.
- `oidc-groups-claim` — the claim carrying group names, for group-based RBAC.

Apply it. On a node that has not bootstrapped yet it is baked in at first boot;
on a running node, `cctl apply` reconciles it live:

```console
$ cctl apply 192.168.1.51 --file controller.yaml
Reconciled 192.168.1.51:7443.
  re-applied  k0s
  restarted   k0scontroller.service
```

## Grant access with RBAC

Authenticating is not authorizing: a freshly logged-in user has no permissions
until RBAC gives them some (until then, a login succeeds but every command is a
`403`). Bind a group — robust, because it does not depend on one person's
username — or a single user:

```bash
# everyone in the "platform-admins" group becomes cluster-admin
kubectl create clusterrolebinding oidc-admins \
  --clusterrole=cluster-admin --group=platform-admins
```

## A kubeconfig that logs in

Users authenticate with [kubelogin](https://github.com/int128/kubelogin)
(`kubectl oidc-login`), which drives the browser flow and caches the token. No
certificate or secret is stored in the kubeconfig:

```yaml
apiVersion: v1
kind: Config
clusters:
  - name: mycluster
    cluster:
      server: https://<api-server>:6443
      certificate-authority-data: <cluster CA>
contexts:
  - name: mycluster
    context: { cluster: mycluster, user: oidc }
current-context: mycluster
users:
  - name: oidc
    user:
      exec:
        apiVersion: client.authentication.k8s.io/v1beta1
        command: kubectl
        args:
          - oidc-login
          - get-token
          - --oidc-issuer-url=https://id.example.com/realms/main
          - --oidc-client-id=kubernetes
          - --oidc-extra-scope=email
          - --oidc-extra-scope=profile
```

`cctl kubeconfig <node>` prints the admin kubeconfig; copy its `server` and
`certificate-authority-data` into the `clusters` block above and replace the
`users` section with the exec block, so no admin credential is left in the file.

## Gotchas

- **`email_verified`.** With `oidc-username-claim: email`, Kubernetes rejects a
  token whose `email_verified` claim is `false` — a `401` that reads like a
  broken login. Either mark the email verified at the provider, or key on `sub`
  as shown above.
- **Scopes.** Only request scopes your provider advertises (see
  `scopes_supported` at `<issuer>/.well-known/openid-configuration`); an unknown
  scope can make the login fail outright.
- **Reachability.** The API server validates tokens against the provider's JWKS,
  so it must be able to reach the issuer — including when the issuer is hosted in
  the same cluster.
- **Decode a token** to check `iss`, `aud` and your username claim when a login
  is refused:

  ```bash
  kubectl oidc-login get-token --oidc-issuer-url=<issuer> --oidc-client-id=<id> \
    | jq -r .status.token | cut -d. -f2 | base64 -d | jq
  ```
