# Production should supply a reviewed digest, not a floating toolchain.
ARG GO_IMAGE=golang:1.27.1-bookworm
FROM ${GO_IMAGE} AS build
WORKDIR /src
COPY go.mod ./
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
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/credential-broker ./cmd/credential-broker
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/credential-broker /credential-broker
USER 10001:10001
EXPOSE 8443
ENTRYPOINT ["/credential-broker"]
CMD ["serve", "--config", "/var/lib/credential-broker/config.json"]
