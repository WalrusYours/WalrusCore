# Standalone build, context = this folder:   docker build -t walrus walrus/
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/walrus ./cmd/walrus

FROM gcr.io/distroless/static-debian12:nonroot AS walrus
COPY --from=build /out/walrus /walrus
EXPOSE 8080
ENTRYPOINT ["/walrus"]

