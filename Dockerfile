# Base images are pinned by digest (the tag stays for readability); Dependabot
# proposes updates. Build and runtime use Debian 13 "trixie": Debian 12
# "bookworm" left regular security support on 2026-07-11 (LTS only since).

# The web build output is platform independent: always build it on the build
# platform, also when cross-building locally (releases build natively per platform).
FROM --platform=$BUILDPLATFORM node:22-alpine@sha256:1314fb30ec37592d5845e5a160d522c0e680972aa534549940b8d73bde88481a AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build the exact engine required by Sendora, not Debian's system SQLite.
FROM golang:1.26-trixie@sha256:cff05dd6b81c901f2e8249eb6f91c69a60451d0f12c26bfef08e476a85b32d4d AS sqlcipher
RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential ca-certificates curl libssl-dev pkg-config tcl \
    && rm -rf /var/lib/apt/lists/*
RUN mkdir /tmp/sqlcipher-download /tmp/sqlcipher-source /tmp/sqlcipher-build \
    && curl -fL --retry 3 https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v4.20.0.tar.gz \
       -o /tmp/sqlcipher-download/source.tar.gz \
    && printf '%s  %s\n' '38705f51b61ac0636a5fd064f9a77fafabc7227b2aa2513f1b14c8bb1102affc' \
       /tmp/sqlcipher-download/source.tar.gz | sha256sum -c - \
    && tar -xzf /tmp/sqlcipher-download/source.tar.gz --strip-components=1 -C /tmp/sqlcipher-source
WORKDIR /tmp/sqlcipher-build
# Upstream's current makefile names its library sqlite3. Give this isolated
# engine its own library/header/pkg-config names to prevent mixed-engine links.
ENV SQLCIPHER_CFLAGS="-O2 -DSQLITE_HAS_CODEC -DSQLITE_TEMP_STORE=2 -DSQLITE_THREADSAFE=1 -DSQLITE_EXTRA_INIT=sqlcipher_extra_init -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown"
RUN /tmp/sqlcipher-source/configure --disable-tcl --with-tempstore=yes \
      CFLAGS="$SQLCIPHER_CFLAGS" LDFLAGS="-lcrypto" \
    && make -j"$(nproc)" sqlite3.c shell.c \
    && cc $SQLCIPHER_CFLAGS -fPIC -shared -Wl,-soname,libsqlcipher.so.0 \
       -o libsqlcipher.so.0 sqlite3.c -lcrypto -lm -ldl -lpthread \
    && install -d /usr/local/lib/pkgconfig /usr/local/include/sqlcipher \
    && install -m 0755 libsqlcipher.so.0 /usr/local/lib/ \
    && ln -s libsqlcipher.so.0 /usr/local/lib/libsqlcipher.so \
    && install -m 0644 sqlite3.h sqlite3ext.h /usr/local/include/sqlcipher/ \
    && printf '%s\n' 'prefix=/usr/local' 'libdir=${prefix}/lib' 'includedir=${prefix}/include/sqlcipher' \
       'Name: SQLCipher' 'Description: SQLCipher encrypted database engine' 'Version: 4.20.0' \
       'Libs: -L${libdir} -lsqlcipher' 'Libs.private: -lcrypto -lm -ldl -lpthread' \
       'Cflags: -I${includedir}' > /usr/local/lib/pkgconfig/sqlcipher.pc \
    && ldconfig \
    && cc $SQLCIPHER_CFLAGS -I. -o /usr/local/bin/sqlcipher shell.c \
       -L/usr/local/lib -lsqlcipher -lcrypto -lm -ldl -lpthread \
    && install -D -m 0644 /tmp/sqlcipher-source/LICENSE.md /usr/share/licenses/sqlcipher/LICENSE.md

FROM sqlcipher AS build
WORKDIR /src
ARG VERSION=0.1.5
COPY go.mod go.sum ./
# go.mod replaces mattn/go-sqlite3 with this SQLCipher linkage fork.
COPY third_party/ ./third_party/
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /sendora . \
    && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o /sendora-db ./cmd/sendora-db
# The engine is exactly 4.20.0 and both programs link it, not a system SQLite.
# (The runtime image has no sqlcipher shell; Sendora checks the version again
# at every start and logs it.)
RUN sqlcipher :memory: 'PRAGMA cipher_version;' | grep '^4\.20\.0 ' \
    && for bin in /sendora /sendora-db; do \
         ldd "$bin" | grep -q 'libsqlcipher\.so\.0 => /usr/local/lib/libsqlcipher\.so\.0' \
         && ! ldd "$bin" | grep -q 'libsqlite3' || exit 1; \
       done

FROM debian:trixie-slim@sha256:a72b4433de1cf7d6bae0c4cee74d0547f58601a643b3375f070c51600d63b6b2
ARG VERSION=0.1.5
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Sendora" \
      org.opencontainers.image.description="Self-hosted file sharing with an encrypted SQLCipher database" \
      org.opencontainers.image.source="https://github.com/mhenderik/sendora" \
      org.opencontainers.image.url="https://github.com/mhenderik/sendora" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${REVISION}"
# libssl3t64 is trixie's name for the OpenSSL 3 runtime library (libcrypto).
# No setuid/setgid programs: nothing in the container can gain privileges.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata libssl3t64 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 app && useradd --uid 10001 --gid 10001 --no-create-home app \
    && mkdir /data && chown app:app /data \
    && find / -xdev -type f -perm /6000 -exec chmod a-s {} + \
    && test -z "$(find / -xdev -type f -perm /6000)"
COPY --from=sqlcipher /usr/local/lib/libsqlcipher.so* /usr/local/lib/
COPY --from=sqlcipher /usr/share/licenses/sqlcipher/LICENSE.md /usr/share/licenses/sqlcipher/LICENSE.md
RUN ldconfig
USER 10001:10001
ENV DATA_DIR=/data PORT=8080 HOST=0.0.0.0 SENDORA_DB_INITIALIZE=false
VOLUME /data
EXPOSE 8080
# "sendora healthcheck" asks http://127.0.0.1:$PORT/api/health (2 s timeout).
# The start period covers a first start or an integrity check of a large database.
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --retries=3 CMD ["/sendora", "healthcheck"]
COPY LICENSE /usr/share/licenses/sendora/LICENSE
COPY --from=build /sendora /sendora
COPY --from=build /sendora-db /usr/local/bin/sendora-db
ENTRYPOINT ["/sendora"]
