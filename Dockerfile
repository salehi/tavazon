# ---- build ----
# Same image as the dev toolchain, so the host caches only one image.
FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -mod=vendor -ldflags "-s -w" -o /tavazon ./cmd/tavazon

# ---- run ----
# scratch: Tavazon is a static CGO-free binary with embedded tzdata, no outbound
# TLS and no DNS/user lookups, so it needs nothing from a base image.
FROM scratch
COPY --from=build /tavazon /tavazon
COPY config.example.json /config.json
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/tavazon", "-config", "/config.json"]
