# Production should supply a reviewed digest, not a floating toolchain.
ARG GO_IMAGE=golang:1.27.4-bookworm
FROM ${GO_IMAGE} AS build
# ADR-0087: the durable store is SQLCipher. mattn/go-sqlite3 links -lsqlite3,
# so the pinned engine is built once and shadowed through a libsqlite3 shim.
RUN apt-get update -qq && apt-get install -y -qq gcc make libssl-dev tcl curl ca-certificates >/dev/null 2>&1 && rm -rf /var/lib/apt/lists/*
ARG SQLCIPHER_VERSION=4.6.1
RUN curl -fsSL -o /tmp/sqlcipher.tar.gz https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v${SQLCIPHER_VERSION}.tar.gz \
    && cd /tmp && tar xzf sqlcipher.tar.gz && cd sqlcipher-${SQLCIPHER_VERSION} \
    && ./configure --enable-tempstore=yes CFLAGS="-DSQLITE_HAS_CODEC -DSQLCIPHER_CRYPTO_OPENSSL -O2" LDFLAGS="-lcrypto" >/dev/null \
    && make -j4 >/dev/null && make install >/dev/null \
    && mkdir -p /opt/sqlcipher-shim && ln -sf /usr/local/lib/libsqlcipher.so /opt/sqlcipher-shim/libsqlite3.so \
    && ln -sf /usr/local/lib/libsqlcipher.a /opt/sqlcipher-shim/libsqlite3.a \
    && ldconfig
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY app ./app
COPY broker ./broker
COPY client ./client
COPY cmd ./cmd
COPY contract ./contract
COPY httpapi ./httpapi
COPY identity ./identity
COPY internal ./internal
COPY materialize ./materialize
COPY provider ./provider
# Fully static: the cipher engine links in, so the scratch runtime stays.
ENV CGO_CFLAGS="-I/usr/local/include/sqlcipher" \
    CGO_LDFLAGS="-L/opt/sqlcipher-shim -static -lsqlcipher -lcrypto -lssl -lpthread -lm -ldl"
RUN go build -tags libsqlite3 -trimpath -buildvcs=false -ldflags='-s -w' -o /out/credential-broker ./cmd/credential-broker
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/credential-broker /credential-broker
USER 10001:10001
EXPOSE 8443
ENTRYPOINT ["/credential-broker"]
CMD ["serve", "--config", "/var/lib/credential-broker/config.json"]
