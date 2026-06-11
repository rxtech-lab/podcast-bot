# Kubernetes deployment — debate-bot

Deploys debate-bot behind `https://debatebot.rxlab.app`.

| File | What it is |
|------|-----------|
| `00-namespace.yaml`   | `debate-bot` namespace |
| `10-deployment.yaml`  | Deployment (video mode, port 3000) + 10Gi PVC for `/app/out` |
| `20-service.yaml`     | ClusterIP Service `:80 -> 3000` |
| `30-ingress.yaml`     | Ingress for `debatebot.rxlab.app` (class `nginx`) + TLS |
| `secrets.example.yaml`  | Template for the app keys / `APP_PASSWORD` Secret |

## How HTTPS works here

Matches the `hk-transportation-mcp` setup: the **nginx** ingress controller terminates TLS
in-cluster, and the cluster-wide cert-manager **`letsencrypt-prod`** ClusterIssuer (HTTP-01)
issues the cert. The Ingress just carries `cert-manager.io/cluster-issuer: letsencrypt-prod`
plus a `tls:` block; cert-manager creates and auto-renews the `debate-bot-tls` secret. No
Cloudflare token and no issuer manifest are needed — `letsencrypt-prod` already exists
cluster-wide and is reused across apps.

> HTTP-01 requires `debatebot.rxlab.app` to resolve to the nginx ingress controller's
> external IP **directly** (not proxied through Cloudflare's orange cloud), or the ACME
> challenge on port 80 will fail.

## Deploy

```bash
# 1. DNS: point debatebot.rxlab.app at the nginx ingress controller's external IP
#    (DNS-only / grey-cloud if the record is in Cloudflare, so HTTP-01 can reach it).

# 2. Fill in secrets (provider keys + APP_PASSWORD), then apply.
cp k8s/secrets.example.yaml k8s/secrets.yaml
$EDITOR k8s/secrets.yaml
kubectl apply -f k8s/secrets.yaml

# 3. Apply the rest.
kubectl apply -f k8s/

# 4. Watch the cert go Ready (HTTP-01 is usually under a minute once DNS resolves).
kubectl -n debate-bot get certificate,ingress,pods
kubectl -n debate-bot describe certificate debate-bot-tls
```

## Notes / things to adjust

- **Image** — `sirily11/debate-bot:latest`. Build & push it (or repoint to your registry)
  before applying. `imagePullPolicy: Always` picks up new `:latest` pushes on pod restart.
- **Mode** — runs `--mode video` (browser uploads `script.md`, downloads `.mp4`). For
  TV-channel **stream** mode, drop the `args:` override and mount a topics dir + a
  `channels.json` configmap instead.
- **APP_PASSWORD** — set in the Secret to gate the public UI; `/api/config` stays open so
  the login screen and the probes work.
- **Issuer** — assumes the cluster-wide `letsencrypt-prod` ClusterIssuer exists (it does,
  per `hk-transportation-mcp`). Verify with `kubectl get clusterissuer letsencrypt-prod`.
