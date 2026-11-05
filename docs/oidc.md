# OIDC login

Corium does not model the flags of the kube-apiserver. So it leaves
authentication to the [`k0s.patch`](reference.md#315-k0spatch--the-escape-hatch)
escape hatch. This page connects an OpenID Connect provider (for example
Keycloak, Authentik, Dex, Okta, or Google) to the API server of a cluster.
`kubectl` then logs people in through the provider, and nobody shares an admin
certificate.

From 0.3.5, you can apply this to a node that is already in service.
`cctl apply` reconciles a change under `k0s`. It renders `/etc/k0s/k0s.yaml`
again and restarts the control plane. See [ADR 8](adr/0008-day-two-reconcile.md).

## Configure the API server

Add the OIDC flags under `k0s.patch` in the `corium:` document of the node:

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

- `oidc-issuer-url`: the issuer of the provider, exactly as it is in the `iss`
  claim of the token, with the trailing slash if there is one. The API server
  must be able to reach the discovery and JWKS endpoints of the issuer.
- `oidc-client-id`: the OAuth client that you created for Kubernetes. The `aud`
  of the token must contain it.
- `oidc-username-claim`: the claim that becomes the name of the user.
  `oidc-username-prefix: "-"` disables the `issuer#` prefix. Without it,
  Kubernetes adds that prefix to a claim that is not `email`.
- `oidc-groups-claim`: the claim that carries group names, for RBAC by group.

Apply the configuration. On a node that has not bootstrapped yet, the agent
applies it at first boot. On a running node, `cctl apply` reconciles it while
the node runs:

```console
$ cctl apply 192.168.1.51 --file controller.yaml
Reconciled 192.168.1.51:7443.
  re-applied  k0s
  restarted   k0scontroller.service
```

## Grant access with RBAC

Authentication is not authorization. A user who just logged in has no
permissions until RBAC gives them some. Until then, the login succeeds, but
every command returns `403`. Bind a group or a single user. A group is the
better choice, because it does not depend on the username of one person:

```bash
# everyone in the "platform-admins" group becomes cluster-admin
kubectl create clusterrolebinding oidc-admins \
  --clusterrole=cluster-admin --group=platform-admins
```

## A kubeconfig that logs in

Users authenticate with [kubelogin](https://github.com/int128/kubelogin)
(`kubectl oidc-login`). It runs the browser flow and caches the token. The
kubeconfig stores no certificate and no secret:

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

`cctl kubeconfig <node>` prints the admin kubeconfig. Copy its `server` and
`certificate-authority-data` into the `clusters` block above. Then replace the
`users` section with the exec block, so that no admin credential stays in the
file.

## Gotchas

- `email_verified`. With `oidc-username-claim: email`, Kubernetes rejects a
  token whose `email_verified` claim is `false`. You get a `401` that looks like
  a broken login. Mark the email as verified at the provider, or use `sub` as
  shown above.
- Scopes. Request only the scopes that your provider advertises. You find them
  in `scopes_supported` at `<issuer>/.well-known/openid-configuration`. An
  unknown scope can make the login fail completely.
- Reachability. The API server validates tokens against the JWKS of the
  provider. So it must be able to reach the issuer, also when the issuer runs in
  the same cluster.
- Decode a token. If a login is refused, decode the token and look at `iss`,
  `aud` and your username claim:

  ```bash
  kubectl oidc-login get-token --oidc-issuer-url=<issuer> --oidc-client-id=<id> \
    | jq -r .status.token | cut -d. -f2 | base64 -d | jq
  ```
