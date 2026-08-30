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

# ffmpeg, built here as one static binary that does exactly four things.
#
# The final image is distroless: no package manager, no shell, nothing to run an
# install with, so a static binary copied in is the only way to add a program to
# it. The ready-made static builds are the obvious source and they are 80 to
# 135MB, because they carry every codec there is; this image was 17.8MB before
# any of this, and putting a hundred megabytes of codecs nothing calls into an
# appliance is a poor trade for the one codec that is called.
#
# So it is configured down to what this service asks of it: read MJPEG in AVI,
# write H.264 in MP4, read that back, and decode it to a frame count. That is
# 5.7MB, and one minute of build time that a layer cache spends once.
#
# Two of the flags are not obvious. wrapped_avframe is what `-f null` encodes
# with, and the verification pass decodes into null. The HEVC decoder is not
# used at all: ffmpeg 7.1 puts a reference to AOM film grain in shared H.264 and
# HEVC code, and HEVC is what pulls that object in, so without it the link
# fails.
FROM alpine:3.22 AS ffmpeg

RUN apk add --no-cache build-base nasm pkgconf curl tar xz x264-dev x264-libs

# Pinned by version and checked by hash: a build that reaches the network gets
# what it asked for or nothing.
ARG FFMPEG_VERSION=7.1.1
ARG FFMPEG_SHA256=733984395e0dbbe5c046abda2dc49a5544e7e0e1e2366bba849222ae9e3a03b1
RUN curl -sSL "https://ffmpeg.org/releases/ffmpeg-${FFMPEG_VERSION}.tar.xz" -o ffmpeg.tar.xz \
 && echo "${FFMPEG_SHA256}  ffmpeg.tar.xz" | sha256sum -c - \
 && tar xf ffmpeg.tar.xz

WORKDIR /ffmpeg-${FFMPEG_VERSION}
RUN ./configure \
      --prefix=/out --bindir=/out \
      --disable-everything --disable-shared --enable-static \
      --disable-doc --disable-debug --disable-network --disable-autodetect \
      --disable-ffplay --disable-ffprobe --disable-alsa --disable-iconv \
      --enable-small --enable-gpl --enable-libx264 \
      --enable-encoder=libx264 --enable-encoder=wrapped_avframe \
      --enable-decoder=mjpeg --enable-decoder=h264 --enable-decoder=hevc \
      --enable-parser=mjpeg --enable-parser=h264 \
      --enable-demuxer=avi --enable-demuxer=mov \
      --enable-muxer=mp4 --enable-muxer=null \
      --enable-protocol=file --enable-protocol=pipe \
      --enable-filter=scale --enable-filter=format --enable-filter=null --enable-filter=copy \
      --extra-cflags=-static --extra-ldflags=-static --pkg-config-flags=--static \
 && make -j"$(nproc)" \
 && make install

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

# The camera list holds passwords and the recordings are the point of the
# service, so both live on a mounted volume rather than in the image.
VOLUME ["/data"]

COPY --from=build /out/nvr /app/nvr
COPY --from=web /web/dist /app/web
COPY --from=build --chown=nonroot:nonroot /out/data /data

# Held recordings are re-encoded from MJPEG in AVI to H.264 in MP4, which is a
# fraction of the size and the only one of the two a browser can play. This is
# the one thing in the image the service can do without: a build with this line
# removed still runs, still records and still serves, and simply holds larger
# recordings.
COPY --from=ffmpeg /out/ffmpeg /usr/local/bin/ffmpeg

EXPOSE 8080
USER nonroot:nonroot

ENTRYPOINT ["/app/nvr"]
CMD ["-addr", ":8080", "-data", "/data/cameras.json", "-recordings", "/data/recordings", "-static", "/app/web"]
