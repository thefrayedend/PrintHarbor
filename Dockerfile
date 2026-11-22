# PrintHarbor + OrcaSlicer in one image.
#
#   docker run -d --name printharbor --restart unless-stopped --network host \
#     -v printharbor-data:/data -v /path/to/models:/models:ro \
#     ghcr.io/thefrayedend/printharbor:latest
#
# --network host lets PrintHarbor find your printers and answer harbor.local.
# Then open http://<this machine>:7621. Add /models under Library → Folders…
#
# The image is built and tested by .github/workflows/build.yml: it has to slice
# a real model before it is pushed. To update, pull the new image and start it
# again; everything you made is in the /data volume.

FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/printharbor ./cmd/printharbor

FROM ubuntu:24.04 AS orca
ARG ORCA_VERSION=2.5.1
ARG ORCA_APPIMAGE_URL=https://github.com/OrcaSlicer/OrcaSlicer/releases/download/v2.5.1/OrcaSlicer_Linux_AppImage_Ubuntu2404_V2.5.1.AppImage
ARG ORCA_APPIMAGE_SHA256=073b8105c928356141db0ddbce88b81d52c7a9e2442f9baae0a9089c89ff04eb
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl git && rm -rf /var/lib/apt/lists/*
WORKDIR /opt
RUN curl -fsSL -o orca.AppImage "$ORCA_APPIMAGE_URL" \
 && echo "$ORCA_APPIMAGE_SHA256  orca.AppImage" | sha256sum -c - \
 && chmod +x orca.AppImage && ./orca.AppImage --appimage-extract >/dev/null \
 && mv squashfs-root orcaslicer && rm orca.AppImage && chmod -R a+rX orcaslicer
# OrcaSlicer 2.5 packs its profiles in a form only it reads, so the readable
# ones come from its source for the same version. (PrintHarbor then keeps them
# up to date by itself, in /data.)
RUN git clone --depth 1 --filter=blob:none --sparse --branch "v$ORCA_VERSION" https://github.com/OrcaSlicer/OrcaSlicer.git /tmp/orca-src \
 && git -C /tmp/orca-src sparse-checkout set resources/profiles \
 && mkdir /opt/orca-profiles && cp -r /tmp/orca-src/resources/profiles/. /opt/orca-profiles/ \
 && find /opt/orca-profiles -type f ! -name '*.json' -delete && rm -rf /tmp/orca-src

FROM ubuntu:24.04
# What OrcaSlicer's program needs to start (it links GTK and WebKit even when
# it only slices), and ffmpeg for camera pictures from RTSP printers.
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      ca-certificates tzdata libgtk-3-0t64 libwebkit2gtk-4.1-0 libsoup-3.0-0 libmanette-0.2-0 \
      libgl1 libegl1 libopengl0 libgstreamer1.0-0 libgstreamer-plugins-base1.0-0 libgstreamer-gl1.0-0 \
      libsecret-1-0 libsm6 ffmpeg \
 && rm -rf /var/lib/apt/lists/*
COPY --from=orca /opt/orcaslicer /opt/orcaslicer
COPY --from=orca /opt/orca-profiles /opt/orca-profiles
COPY --from=build /out/printharbor /usr/local/bin/printharbor
ENV ORCA_PATH=/opt/orcaslicer/AppRun \
    ORCA_PROFILES=/opt/orca-profiles \
    PRINTHARBOR_CONTAINER=1 \
    HOME=/data/home
LABEL org.opencontainers.image.source=https://github.com/thefrayedend/PrintHarbor \
      org.opencontainers.image.licenses=AGPL-3.0
WORKDIR /data
EXPOSE 7621
VOLUME ["/data"]
ENTRYPOINT ["printharbor", "-config", "/data/config.json"]
