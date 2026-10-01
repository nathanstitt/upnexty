# Build environment for out-of-tree kernel modules for the Luckfox Lyra.
#
# The board's kernel is Rockchip's BSP 6.1.99 (armbian/linux-rockchip branch
# rk-6.1-rkr5), 32-bit ARM, Thumb-2, preempt, no module versioning. Modules
# built here only have to match that vermagic string; the compiler version is
# not part of it, so Ubuntu's cross gcc is fine in place of the arm-none
# 10.3 toolchain Luckfox used.
#
# Runs natively on an arm64 Docker host (Apple Silicon) -- the cross compiler
# targets 32-bit ARM either way. See scripts/build-usb-audio.sh.
FROM ubuntu:22.04

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
        bc bison flex build-essential ca-certificates \
        gcc-arm-linux-gnueabihf kmod libelf-dev libssl-dev lz4 \
        python3 rsync \
    && rm -rf /var/lib/apt/lists/*

ENV ARCH=arm CROSS_COMPILE=arm-linux-gnueabihf-
WORKDIR /src
