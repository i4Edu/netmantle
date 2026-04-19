# syntax=docker/dockerfile:1.6
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/i4Edu/netmantle/internal/version.Version=${VERSION}" \
    -o /out/netmantle ./cmd/netmantle
# Pre-create the runtime data dir so it is owned by the nonroot UID
# (65532) in the final image, which makes the embedded sqlite DB and
# git-backed config store writable without requiring an external volume.
RUN mkdir -p /out/data && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static:nonroot
COPY --from=build --chown=65532:65532 /out/data /var/lib/netmantle
WORKDIR /var/lib/netmantle
COPY --from=build /out/netmantle /usr/local/bin/netmantle
COPY config.example.yaml /etc/netmantle/config.yaml
EXPOSE 8080
USER nonroot:nonroot
ENV NETMANTLE_DATABASE_DSN=/var/lib/netmantle/netmantle.db \
    NETMANTLE_STORAGE_CONFIG_REPO_ROOT=/var/lib/netmantle/configs
ENTRYPOINT ["/usr/local/bin/netmantle"]
CMD ["serve", "--config", "/etc/netmantle/config.yaml"]
