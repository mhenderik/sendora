# Kubernetes

A step-by-step guide to running Sendora on Kubernetes with [`deploy/kubernetes.yaml`](../deploy/kubernetes.yaml). The manifest is an **illustrative starting point**, not a tested production deployment.

[Back to the documentation index](README.md)

## Overview

The manifest defines:

- the `sendora` namespace;
- a 100 GiB `ReadWriteOnce` PersistentVolumeClaim for `/data`;
- the `sendora-config` **ConfigMap** with settings (see [Configuration](configuration.md#settings-from-a-configmap-or-environment-variables));
- a single-replica Deployment with the `Recreate` strategy, a non-root read-only container (UID/GID 10001), resource requests and limits, and `/api/health` probes;
- a private ClusterIP Service on port 80, and a commented-out HTTPS Ingress.

Secrets are created separately with `kubectl` and never stored in the manifest:

| Secret | Contents | Used as |
|---|---|---|
| `sendora-db-key` | the raw 32-byte database key (`database.key`) | `SENDORA_DB_KEY_FILE` (required) |
| `sendora-secrets` | e.g. `smtp-password`, `entra-client-secret` | `SENDORA_SMTP_PASSWORD_FILE`, `SENDORA_PROVIDER_<ID>_CLIENT_SECRET_FILE` (optional) |

Sendora is a **single-instance** application (SQLite with process-local locks): never run more than one replica.

## 1. Image

Use an image you trust and pin it by version or digest. The project's release workflow publishes images to `ghcr.io/mhenderik/sendora` when a version is tagged (see [Releases and images](#releases-and-images)). To build your own instead:

```sh
IMAGE=registry.example.com/sendora:0.9.0
docker build --build-arg VERSION=0.9.0 -t "$IMAGE" .
docker push "$IMAGE"
```

Build for your nodes' CPU architecture (or a multi-platform image), configure `imagePullSecrets` for a private registry, and replace the `image:` line of the Deployment.

## 2. Storage, database key and first start

1. Review PVC capacity and resource limits for your file sizes, concurrency, and temporary snapshots. The 100 GiB/CPU/memory values are examples, not capacity guarantees. Select a storage class suitable for SQLite filesystem locking and durability. The manifest omits `storageClassName` to use the cluster default; set it explicitly if needed, and confirm the PVC is Bound before troubleshooting startup.
2. Provision the volume root and any restored application files with **UID 10001 ownership**. The Pod runs without root as UID/GID 10001 with `fsGroup: 10001` and drops all capabilities. `fsGroup` can provide group access when the storage driver supports it, but **is not enough by itself**: Sendora changes the data root/subdirectories to 0700 and database files to 0600, which requires ownership. Root-owned PVC directories can therefore fail startup even if group-writable. Arrange ownership through the storage provisioner/administrator before starting; the example intentionally does not run a privileged/root permission-fixing init container. Do not use world-writable permissions.
3. Generate the database key **once**, on a trusted machine, outside the repository. It must be exactly **32 random raw bytes** (not hex, base64 or a password, and no trailing newline). Either of these works:

   ```sh
   # with OpenSSL (refuses to overwrite an existing file)
   (umask 077; set -C; openssl rand 32 > ./database.key)
   # or with Sendora's own tool (also refuses to overwrite)
   sendora-db keygen -key-file ./database.key
   ```

   Store a copy in a password manager or other protected off-cluster storage **before** you continue: losing the key makes the database and all its backups unreadable, and it can never be regenerated. Never generate a new key for an existing database.

   Then create the namespace and Secret separately; **do not put raw/base64 key material into the manifest or repository**:

   ```sh
   kubectl create namespace sendora
   kubectl -n sendora create secret generic sendora-db-key \
     --from-file=database.key=./database.key
   ```

   The key file must be readable by the operator running this command. The Secret projection is root-owned with mode **0440**; Pod `fsGroup: 10001` gives the non-root process read access without group-write or world access. The example mounts `/run/secrets/sendora/database.key` read-only and sets `SENDORA_DB_KEY_FILE` accordingly. Protect Kubernetes Secret access, enable encryption at rest for cluster secrets, and keep a separate off-cluster key backup; a Secret is not itself an off-host backup policy.

   For a **new PVC/database only**, temporarily set `SENDORA_DB_INITIALIZE` to `"true"` in the Deployment while access remains private, then apply the uncommented resources:

   ```sh
   kubectl apply -f deploy/kubernetes.yaml
   kubectl -n sendora get pvc,pods,services
   kubectl -n sendora logs deployment/sendora
   ```

   After private first-run setup, restore `SENDORA_DB_INITIALIZE` to `"false"` and reapply the manifest. Keep it false for upgrades, restored databases, and all later starts. The checked-in manifest defaults to false and will not create a missing database until you explicitly opt in. Existing plaintext databases require offline migration, not this initialization switch.

4. The example sets `HOST=0.0.0.0`, `PORT=8080`, and absolute `DATA_DIR=/data`. It sets the canonical `APP_URL` to **`https://sendora.example.com`**; replace this with your real HTTPS origin. This environment variable is supported and overrides the administrator `application_url` setting for links, callbacks, origin checks, and secure-cookie decisions.
5. Finish first-run setup using restricted HTTPS access at that canonical origin (the one-time setup code is in the pod log: `kubectl -n sendora logs deploy/sendora | grep "setup code"`, or set your own with `SENDORA_SETUP_CODE`), before exposing the Ingress publicly. A plain HTTP port-forward alone does not match the example's HTTPS origin and secure cookies. If you temporarily use port-forwarding for bootstrap, temporarily configure `APP_URL=http://localhost:8080` and access it through `kubectl -n sendora port-forward service/sendora 8080:80`, then restore the real HTTPS `APP_URL` before external access, email, or OAuth configuration. Do not leave both configurations active or expose bootstrap access publicly.

## 3. Settings with the ConfigMap

Every admin setting can be set in the `sendora-config` ConfigMap as `SENDORA_<SETTING>` (full list in [Configuration](configuration.md#settings-from-a-configmap-or-environment-variables)). The Deployment loads all its entries with `envFrom`. Configured values win at every start and are read-only in Admin → Settings; a wrong value stops the pod with an error naming the variable (check `kubectl -n sendora logs deploy/sendora`).

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: sendora-config
  namespace: sendora
data:
  SENDORA_ALLOW_SELF_REGISTRATION: "false"
  SENDORA_IDLE_LOGOUT_MINUTES: "60"
  SENDORA_SMTP_HOST: smtp.example.com
  SENDORA_SMTP_PORT: "587"
  SENDORA_SMTP_TLS: starttls
  SENDORA_SMTP_USER: sendora@example.com
  SENDORA_SMTP_FROM: Sendora <sendora@example.com>
  SENDORA_TRUSTED_PROXIES: 10.0.0.0/8   # your Ingress controller's pod network
```

ConfigMap changes are not picked up by a running pod. Apply the change and restart:

```sh
kubectl apply -f deploy/kubernetes.yaml
kubectl -n sendora rollout restart deployment/sendora
```

## 4. Secrets for SMTP and login providers

Put passwords and client secrets in a Secret, mount it as files and point to them with `_FILE` variables:

```sh
kubectl -n sendora create secret generic sendora-secrets \
  --from-file=smtp-password=./smtp-password \
  --from-file=entra-client-secret=./entra-client-secret
```

Then uncomment in the Deployment the `config-secrets` volume and volume mount (`/run/secrets/sendora-config`) and the variables:

```yaml
- name: SENDORA_SMTP_PASSWORD_FILE
  value: /run/secrets/sendora-config/smtp-password
- name: SENDORA_PROVIDER_ENTRA_CLIENT_SECRET_FILE
  value: /run/secrets/sendora-config/entra-client-secret
```

A variable that points to a missing file stops the pod, so create the Secret first.

## 5. Microsoft Entra ID (or another provider) from configuration

Add the provider to the ConfigMap; its client secret comes from the Secret above:

```yaml
  SENDORA_PROVIDER_ENTRA_TYPE: entra
  SENDORA_PROVIDER_ENTRA_NAME: Company login
  SENDORA_PROVIDER_ENTRA_TENANT_ID: 00000000-0000-0000-0000-000000000000
  SENDORA_PROVIDER_ENTRA_CLIENT_ID: 00000000-0000-0000-0000-000000000000
  SENDORA_PROVIDER_ENTRA_ALLOW_REGISTRATION: "true"
```

Register the callback URL `https://<your host>/api/auth/oidc/config-entra/callback` as a **Web** redirect URI in the Entra app registration. Use the **Application (client) ID** and the **Directory (tenant) ID** from the app's Overview page, not the Object ID. Details: [Microsoft Entra ID](authentication.md#microsoft-entra-id-single-tenant-setup).

## 6. HTTPS and the Ingress

Install/configure an Ingress controller, choose its `ingressClassName`, and point your hostname's DNS to its endpoint. Provision a TLS Secret named `sendora-tls` in the `sendora` namespace with a certificate valid for your real hostname; the example does not create certificates or assume a certificate-management integration. Uncomment the final Ingress block only after replacing its `nginx` class and **`sendora.example.com`** hostname as appropriate. Keep its hostname and TLS hosts aligned with `APP_URL`, and restrict access while completing setup.

The backend remains HTTP on port 8080 inside the cluster; terminate HTTPS at the trusted Ingress. Enforce HTTPS redirects through your controller's configuration and tune its controller-specific upload body limits/timeouts for the chunked API and long downloads as described in [Docker deployment behind HTTPS](deployment.md#docker-deployment-behind-https). No controller-specific annotations are provided because these settings are not portable. Probes check HTTP process health only, not storage consistency, available capacity, SMTP delivery, or the end-to-end HTTPS route.

Behind the Ingress, set `SENDORA_TRUSTED_PROXIES` to the address range of your Ingress controller. Without it every visitor appears to come from the controller's address, so rate limits and the automatic address blocking would treat all users as one (one attacker could block everybody).

## 7. Upgrades

1. Read the [changelog](../CHANGELOG.md).
2. Make a verified backup (Admin → Backups, or offline as described in [Backups](backup-and-migration.md)) and keep the database key.
3. Change the image to the new version (pin a version or digest, not `latest`) and apply. With `Recreate` the old pod stops before the new one starts; database migrations run at start and only add tables or columns.
4. Watch the start: `kubectl -n sendora rollout status deployment/sendora` and `kubectl -n sendora logs deploy/sendora`.

Rolling back to an older image after an upgrade is not supported once new columns exist; restore the backup taken before the upgrade instead.

## 8. Single instance, backups and migration

**Do not increase replicas or run another Deployment/Job against this data volume while Sendora is running.** `Recreate` prevents overlap during ordinary Deployment updates; it is not high availability or protection against another workload using the same PVC. `ReadWriteOnce` restricts node mounting, not necessarily multiple Pods on one node. Do not manually force-delete a Pod or start its replacement while the original process might still be running. Updates and failures can cause downtime.

Keep full, protected backups outside the PVC, along with deployment configuration and external secrets. Built-in backups include SQLite only, not shared bytes or appearance assets. For a full offline backup/restore, scale the Deployment to zero and confirm the Pod/process has stopped before mounting/copying storage; restore with UID 10001 ownership and start exactly one instance afterward. Do not casually delete the PVC: data retention on claim deletion depends on the storage class/PV reclaim policy. Test a full restore before relying on the deployment.

Stored file locations are resolved against the server's own `DATA_DIR/shares/<share id>/` using only each recorded file's base name, so a database or share folders moved to the PVC at `/data` keep working as long as each share folder is copied over intact. Retain an untouched full backup and verify old downloads after the move; neither the manifest nor changing `DATA_DIR` copies any data.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Pod restarts with `database key must be a restricted regular file` | the key Secret is not mounted with mode 0440 / readable through `fsGroup: 10001` |
| `database missing; explicit initialization required` | new, empty volume: set `SENDORA_DB_INITIALIZE: "true"` once (see step 2) |
| `configuration: SENDORA_…: …` | a ConfigMap value is invalid or a `_FILE` points to a missing file |
| Setup page asks for a code | read it from the log: `kubectl -n sendora logs deploy/sendora \| grep "setup code"` |
| Sign-in or links use the wrong host | `APP_URL` does not match the public HTTPS address |
| Everyone gets blocked or rate limited together | `SENDORA_TRUSTED_PROXIES` is not set behind the Ingress |
| Permission errors on `/data` | the volume root is not owned by UID 10001 |

## Releases and images

Pushing a version tag (for example `v0.9.0`) to the GitHub repository runs the release workflow (`.github/workflows/release.yml`). It builds the image for `linux/amd64` and `linux/arm64` and publishes it to the GitHub Container Registry as `ghcr.io/mhenderik/sendora:0.9.0`, `:0.9` and `:latest`. A new package on ghcr.io is private until its visibility is set to public in the package settings on GitHub; for a private package, create an `imagePullSecret` with a token that has `read:packages`.
