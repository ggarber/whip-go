FROM debian:sid AS builder
RUN apt-get update --allow-releaseinfo-change && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        openssl \
        git \
        tzdata \
        build-essential \
        pkg-config \
        libx11-dev \
        libxext-dev \
        libvpx-dev \
        ffmpeg \
        libx264-dev \
        golang-go && \
   apt-get clean && \
   rm -rf /var/lib/apt/lists/*
WORKDIR /opt/whip-go
COPY . /opt/whip-go
RUN go build


