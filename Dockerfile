FROM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# Build the exact engine required by Sendora, not Debian's system SQLite.
FROM golang:1.26-bookworm AS sqlcipher
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
    && sqlcipher :memory: 'PRAGMA cipher_version;' | grep '^4\.20\.0 ' \
    && install -D -m 0644 /tmp/sqlcipher-source/LICENSE.md /usr/share/licenses/sqlcipher/LICENSE.md

FROM sqlcipher AS build
WORKDIR /src
ARG VERSION=0.9.1
COPY go.mod go.sum ./
# go.mod replaces mattn/go-sqlite3 with this SQLCipher linkage fork.
COPY third_party/ ./third_party/
RUN go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
RUN CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /sendora . \
    && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o /sendora-db ./cmd/sendora-db

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata libssl3 wget \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 app && useradd --uid 10001 --gid 10001 --no-create-home app \
    && mkdir /data && chown app:app /data
COPY --from=sqlcipher /usr/local/lib/libsqlcipher.so* /usr/local/lib/
COPY --from=sqlcipher /usr/local/bin/sqlcipher /usr/local/bin/sqlcipher
COPY --from=sqlcipher /usr/share/licenses/sqlcipher/LICENSE.md /usr/share/licenses/sqlcipher/LICENSE.md
RUN ldconfig && sqlcipher :memory: 'PRAGMA cipher_version;' | grep '^4\.20\.0 '
USER 10001:10001
ENV DATA_DIR=/data PORT=8080 HOST=0.0.0.0 SENDORA_DB_INITIALIZE=false
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:8080/api/health || exit 1
COPY LICENSE /usr/share/licenses/sendora/LICENSE
COPY --from=build /sendora /sendora
COPY --from=build /sendora-db /usr/local/bin/sendora-db
ENTRYPOINT ["/sendora"]
