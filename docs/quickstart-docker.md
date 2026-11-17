# Quick start: Docker (one command)

The bare minimum to try Sendora on your own machine with the published image. For a lasting setup use the [Docker Compose files](../deploy/compose/README.md); for a cluster see the [Kubernetes quick start](quickstart-kubernetes.md).

[Back to the documentation index](README.md)

## 1. Folder and database key

```sh
mkdir sendora && cd sendora
mkdir data
(umask 077; set -C; openssl rand 32 > database.key)
```

The key is exactly 32 random bytes and encrypts the database. **Keep a copy in a safe place**: without it the data cannot be read, and it cannot be regenerated.

On **Linux** the container runs as UID/GID 10001 and must own the data folder and be able to read the key:

```sh
sudo chown 10001:10001 data && sudo chmod 700 data
sudo chown root:10001 database.key && sudo chmod 0440 database.key
```

(Docker Desktop on macOS and Windows handles this itself.)

## 2. Start

```sh
docker run -d --name sendora -p 127.0.0.1:8080:8080 \
  -v "$PWD/data:/data" \
  -v "$PWD/database.key:/run/secrets/database.key:ro" \
  -e SENDORA_DB_KEY_FILE=/run/secrets/database.key \
  -e SENDORA_DB_INITIALIZE=true \
  -e APP_URL=http://localhost:8080 \
  ghcr.io/mhenderik/sendora:0.1.2
```

## 3. Create the administrator

```sh
docker logs sendora | grep "setup code"
```

Open http://localhost:8080, enter the setup code and create the administrator account. The code is printed only while no account exists and changes on every restart: always use the last line. More ways to find it: [First-run setup code](https://github.com/mhenderik/sendora/wiki/Accounts-and-Authentication#first-run-setup-code).

## 4. Later starts

`SENDORA_DB_INITIALIZE=true` is only for creating the database the very first time. Recreate the container without it:

```sh
docker rm -f sendora
docker run -d --name sendora --restart unless-stopped -p 127.0.0.1:8080:8080 \
  -v "$PWD/data:/data" \
  -v "$PWD/database.key:/run/secrets/database.key:ro" \
  -e SENDORA_DB_KEY_FILE=/run/secrets/database.key \
  -e APP_URL=http://localhost:8080 \
  ghcr.io/mhenderik/sendora:0.1.2
```

Any admin setting can be added as `-e SENDORA_<SETTING>=value` (see [Configuration](configuration.md#settings-from-a-configmap-or-environment-variables)). To stop: `docker rm -f sendora`; your data stays in `data/`.

## Next steps

- Reachable from other machines or the internet: use the [Compose files with automatic HTTPS](../deploy/compose/README.md#public-server-with-https).
- Outgoing mail, sign-in with Microsoft Entra ID and other settings: [Configuration](configuration.md), [Email](https://github.com/mhenderik/sendora/wiki/Email-and-Notifications), [Accounts and authentication](https://github.com/mhenderik/sendora/wiki/Accounts-and-Authentication).
- Backups: [Upgrades and backups](https://github.com/mhenderik/sendora/wiki/Upgrades-and-Backups).
