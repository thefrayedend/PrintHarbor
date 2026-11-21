# PrintHarbor + stock OrcaSlicer in one image.
#
#   docker build -t printharbor .
#   docker run --network host -v printharbor-data:/data -v /path/to/models:/models:ro printharbor
#
# --network host lets PrintHarbor see your printers and answer harbor.local
# (mDNS). Add /models under Library → Folders… to browse your model folders.
#
# ORCA_APPIMAGE_URL must point at an official OrcaSlicer Linux AppImage from
# https://github.com/OrcaSlicer/OrcaSlicer/releases (file names change per
# release, so check the page and pin the version you've tested).
# NOTE: not yet tested end to end.

FROM golang:1.22-bookworm AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/printharbor ./cmd/printharbor

FROM debian:bookworm-slim AS orca
ARG ORCA_APPIMAGE_URL
RUN test -n "$ORCA_APPIMAGE_URL" || (echo "Set --build-arg ORCA_APPIMAGE_URL=<OrcaSlicer Linux AppImage URL>" && exit 1)
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl && rm -rf /var/lib/apt/lists/*
WORKDIR /opt
RUN curl -fsSL -o orca.AppImage "$ORCA_APPIMAGE_URL" && chmod +x orca.AppImage \
 && ./orca.AppImage --appimage-extract >/dev/null && mv squashfs-root orcaslicer && rm orca.AppImage

FROM debian:bookworm-slim
# Orca links GTK/WebKit even for the CLI; these cover the AppImage's needs.
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates libgtk-3-0 libwebkit2gtk-4.1-0 libgl1 libegl1 libgstreamer1.0-0 \
      libgstreamer-plugins-base1.0-0 libsecret-1-0 libosmesa6 locales \
 && rm -rf /var/lib/apt/lists/*
COPY --from=orca /opt/orcaslicer /opt/orcaslicer
COPY --from=build /out/printharbor /usr/local/bin/printharbor
ENV ORCA_PATH=/opt/orcaslicer/AppRun \
    ORCA_PROFILES=/opt/orcaslicer/resources/profiles
WORKDIR /data
EXPOSE 7621
# Optional: ffmpeg gives camera pictures from RTSP printers (Bambu X1/H2).
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
VOLUME ["/data"]
ENTRYPOINT ["printharbor", "-config", "/data/config.json"]
