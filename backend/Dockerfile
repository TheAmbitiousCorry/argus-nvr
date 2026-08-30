# Build with the toolchain pinned to the go directive in go.mod.
FROM golang:1.23 AS build

WORKDIR /src

# Dependencies are copied first so a source-only change reuses the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO is off so the result is a static binary that runs in a distroless image.
# The symbol table is stripped because it is dead weight on an appliance.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/nvr .

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# The camera list holds passwords, so it belongs on a mounted volume rather
# than baked into the image.
VOLUME ["/data"]

COPY --from=build /out/nvr /app/nvr

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/app/nvr"]
CMD ["-addr", ":8080", "-data", "/data/cameras.json", "-static", "/app/web"]
