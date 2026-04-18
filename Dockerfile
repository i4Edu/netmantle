# syntax=docker/dockerfile:1.6
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-X github.com/i4Edu/netmantle/internal/version.Version=${VERSION}" \
    -o /out/netmantle ./cmd/netmantle

FROM gcr.io/distroless/static:nonroot
WORKDIR /var/lib/netmantle
COPY --from=build /out/netmantle /usr/local/bin/netmantle
COPY config.example.yaml /etc/netmantle/config.yaml
EXPOSE 8080
USER nonroot:nonroot
ENV NETMANTLE_DATABASE_DSN=/var/lib/netmantle/netmantle.db \
    NETMANTLE_STORAGE_CONFIG_REPO_ROOT=/var/lib/netmantle/configs
ENTRYPOINT ["/usr/local/bin/netmantle"]
CMD ["serve", "--config", "/etc/netmantle/config.yaml"]
