# One image holds both halves: the Go service and the built frontend it serves.
# They are built in separate stages so a change to one does not rebuild the
# other, and neither toolchain ends up in the image that ships.

FROM node:22-alpine AS web

WORKDIR /web

# The lockfile is copied on its own so a source-only change reuses the install
# layer, which is by far the slowest step here.
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY frontend/ ./
RUN npm run build

# Pinned to the go directive in go.mod.
FROM golang:1.23 AS build

WORKDIR /src

COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# CGO off, so the result is a static binary that runs in an image with no libc.
# The symbol table is stripped because it is dead weight on an appliance.
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/nvr .

# An empty directory to seed the data volume from. Distroless has no shell, so
# there is no way to create one in the final stage; copying it from here is what
# gets the volume created owned by the user the service runs as.
RUN mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot

WORKDIR /app

# The camera list holds passwords, so it lives on a mounted volume rather than
# baked into the image.
VOLUME ["/data"]

COPY --from=build /out/nvr /app/nvr
COPY --from=web /web/dist /app/web
COPY --from=build --chown=nonroot:nonroot /out/data /data

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/app/nvr"]
CMD ["-addr", ":8080", "-data", "/data/cameras.json", "-static", "/app/web"]
