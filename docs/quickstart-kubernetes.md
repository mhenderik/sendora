# Quick start: Kubernetes (bare minimum)

The minimum to try Sendora in a cluster with [`deploy/kubernetes-minimal.yaml`](../deploy/kubernetes-minimal.yaml): a volume, one pod and a service, reached through `kubectl port-forward`. For production (ConfigMap, Secrets, Ingress with HTTPS, hardened pod) follow the full [Kubernetes guide](kubernetes.md).

[Back to the documentation index](README.md)

Requirements: `kubectl` access to a cluster with a default StorageClass (any local cluster such as kind, minikube, k3s or Docker Desktop works) and `openssl`.

## 1. Database key and Secret

```sh
(umask 077; set -C; openssl rand 32 > database.key)
kubectl create namespace sendora
kubectl -n sendora create secret generic sendora-db-key --from-file=database.key=./database.key
```

The key is exactly 32 random bytes and encrypts the database. **Keep a copy in a safe place**: without it the data cannot be read, and it cannot be regenerated.

## 2. Deploy

```sh
kubectl apply -f https://raw.githubusercontent.com/mhenderik/sendora/main/deploy/kubernetes-minimal.yaml
kubectl -n sendora rollout status deploy/sendora
```

(or `kubectl apply -f deploy/kubernetes-minimal.yaml` from a clone of the repository.)

## 3. Create the administrator

```sh
kubectl -n sendora logs deploy/sendora | grep "setup code"
kubectl -n sendora port-forward svc/sendora 8080:80
```

Open http://localhost:8080, enter the setup code and create the administrator account.

## 4. After the first start

Database creation should only be allowed for the very first start, so a lost volume is never silently replaced by an empty database:

```sh
kubectl -n sendora set env deploy/sendora SENDORA_DB_INITIALIZE=false
```

## Remove it again

```sh
kubectl delete namespace sendora
```

This deletes the volume and all data (depending on your StorageClass reclaim policy).

## Next steps

The minimal manifest runs an init container as root once to give the volume to the app user, uses `APP_URL=http://localhost:8080` for the port-forward, and has no HTTPS, mail or sign-in providers configured. Moving on to a real deployment: [Kubernetes guide](kubernetes.md).
