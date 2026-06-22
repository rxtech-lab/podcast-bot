# Kubernetes deployment — debate-bot

Deploys debate-bot behind `https://debatebot.rxlab.app`.

| File | What it is |
|------|-----------|
| `00-namespace.yaml`   | `debate-bot` namespace |
| `10-deployment.yaml`  | **StatefulSet** (video mode, port 3000), 3 replicas, per-pod 10Gi scratch PVC |
| `20-service.yaml`     | Load-balanced ClusterIP `:80 -> 3000` **+ headless `debate-bot-headless`** for per-pod DNS |
| `30-ingress.yaml`     | Ingress for `debatebot.rxlab.app` (class `nginx`) + TLS |
| `secrets.example.yaml`  | Template for app keys, `APP_PASSWORD`, **Turso, and S3** Secret |

## Horizontal scaling

This deployment runs **N replicas** behind one load-balanced Service. It scales
because all *shared* state lives off-pod:

- **Job + discussion metadata → Turso/libSQL** (`TURSO_CONNECTION_URL`). Every
  pod reads/writes the same database.
- **Finished mp3/mp4/vtt → S3 / R2** (`S3_*`). Any pod can serve any download.
- **Per-pod scratch** (in-progress HLS/render output) stays on each pod's own
  PVC via `volumeClaimTemplates`.

The one thing that *is* pod-bound is a **live audio stream**: the `ffmpeg -re`
LiveStream for an in-flight job lives in the memory of the pod that runs it. To
handle that, the app does **cross-pod routing**: each job is stamped with its
owner pod (`POD_NAME`), and a request for an in-flight job that the
load-balancer sent to the wrong pod is transparently reverse-proxied to the
owner over the headless service (`PEER_HOST_TEMPLATE`). Once a job finishes its
artefacts are in S3, so any pod answers directly with no proxy.

> Scale with `kubectl -n debate-bot scale statefulset/debate-bot --replicas=N`.
> Requires `TURSO_CONNECTION_URL` set; with it blank, run **replicas: 1** only
> (local SQLite per pod cannot be shared).

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

# 2. Fill in secrets (provider keys + APP_PASSWORD + Turso + S3), then apply.
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
- **Replicas** — defaults to 3. Needs `TURSO_CONNECTION_URL` + `S3_*` in the Secret;
  without them, set `replicas: 1`. `PEER_HOST_TEMPLATE` in the StatefulSet must match the
  headless service name/namespace/port if you rename anything.
- **APP_PASSWORD** — set in the Secret to gate the public UI; `/api/config` stays open so
  the login screen and the probes work.
- **Issuer** — assumes the cluster-wide `letsencrypt-prod` ClusterIssuer exists (it does,
  per `hk-transportation-mcp`). Verify with `kubectl get clusterissuer letsencrypt-prod`.
