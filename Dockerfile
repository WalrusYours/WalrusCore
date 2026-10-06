# Standalone build, context = this folder:   docker build -t walrus walrus/
# Runs on the build host's own architecture and cross-compiles, so multi-arch images need no QEMU.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0-dev
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/walrus ./cmd/walrus

FROM gcr.io/distroless/static-debian12:nonroot AS walrus
COPY --from=build /out/walrus /walrus
EXPOSE 8080
ENTRYPOINT ["/walrus"]
