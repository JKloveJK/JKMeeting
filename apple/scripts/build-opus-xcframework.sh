#!/bin/sh
# 用 core/third_party/opus 的 CMake 构建 macos-arm64、ios-arm64、ios-arm64-simulator 三个静态库，
# 合成 apple/third_party/Opus.xcframework。版本戳一致时跳过重建。
set -eu

ROOT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
OPUS_DIR="$ROOT_DIR/core/third_party/opus"
OUT_DIR="$ROOT_DIR/apple/third_party"
XCFRAMEWORK="$OUT_DIR/Opus.xcframework"
STAMP="$OUT_DIR/Opus.xcframework.stamp"
BUILD_DIR="$OUT_DIR/opus-build"
DEPLOYMENT_TARGET=26.0

opus_version=$(cat "$OPUS_DIR/package_version")
stamp_content=$(cat <<EOF
opus: $opus_version
deployment_target: $DEPLOYMENT_TARGET
macosx: $(xcrun --sdk macosx --show-sdk-version) arm64 OPUS_PRESUME_NEON=ON
iphoneos: $(xcrun --sdk iphoneos --show-sdk-version) arm64 CMAKE_SYSTEM_PROCESSOR=aarch64
iphonesimulator: $(xcrun --sdk iphonesimulator --show-sdk-version) arm64 CMAKE_SYSTEM_PROCESSOR=aarch64
script: $(shasum -a 256 "$0" | cut -d ' ' -f 1)
EOF
)

if [ -d "$XCFRAMEWORK" ] && [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$stamp_content" ]; then
  printf 'Opus.xcframework 已是最新（%s）\n' "$opus_version"
  exit 0
fi

rm -rf "$XCFRAMEWORK" "$STAMP" "$BUILD_DIR"
mkdir -p "$BUILD_DIR"

build_slice() {
  name=$1
  shift
  printf '==> 构建 %s\n' "$name"
  cmake -S "$OPUS_DIR" -B "$BUILD_DIR/$name" \
    -DCMAKE_BUILD_TYPE=Release \
    -DCMAKE_OSX_ARCHITECTURES=arm64 \
    -DCMAKE_OSX_DEPLOYMENT_TARGET=$DEPLOYMENT_TARGET \
    -DBUILD_TESTING=OFF \
    -DOPUS_BUILD_TESTING=OFF \
    -DOPUS_BUILD_PROGRAMS=OFF \
    -DOPUS_INSTALL_PKG_CONFIG_MODULE=OFF \
    -DOPUS_INSTALL_CMAKE_CONFIG_MODULE=OFF \
    "$@" >"$BUILD_DIR/$name.configure.log"
  cmake --build "$BUILD_DIR/$name" --target opus --parallel >"$BUILD_DIR/$name.build.log"
  if ! grep -q OPUS_ARM_PRESUME_NEON_INTR "$BUILD_DIR/$name/CMakeFiles/opus.dir/flags.make"; then
    printf '%s 未启用 NEON，见 %s\n' "$name" "$BUILD_DIR/$name.configure.log" >&2
    exit 1
  fi
}

build_slice macos-arm64 -DOPUS_PRESUME_NEON=ON
build_slice ios-arm64 -DCMAKE_SYSTEM_NAME=iOS -DCMAKE_OSX_SYSROOT=iphoneos -DCMAKE_SYSTEM_PROCESSOR=aarch64
build_slice ios-arm64-simulator -DCMAKE_SYSTEM_NAME=iOS -DCMAKE_OSX_SYSROOT=iphonesimulator \
  -DCMAKE_SYSTEM_PROCESSOR=aarch64

xcodebuild -create-xcframework \
  -library "$BUILD_DIR/macos-arm64/libopus.a" \
  -library "$BUILD_DIR/ios-arm64/libopus.a" \
  -library "$BUILD_DIR/ios-arm64-simulator/libopus.a" \
  -output "$XCFRAMEWORK"
printf '%s\n' "$stamp_content" >"$STAMP"
printf 'Opus.xcframework 已生成：%s（%s）\n' "$XCFRAMEWORK" "$(du -sh "$XCFRAMEWORK" | cut -f 1)"
