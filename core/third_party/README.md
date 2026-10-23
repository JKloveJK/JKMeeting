# core 第三方依赖

P2 阶段只允许引入 libopus 一个第三方库（见 [客户端全栈学习路线 §4.3](../../docs/客户端全栈学习路线.md) 与 [ADR 0006](../../docs/decisions/0006-opus-ogg-container.md)）。

## libopus

| 项 | 值 |
| --- | --- |
| 版本 | 1.6.1 |
| 来源 | `https://downloads.xiph.org/releases/opus/opus-1.6.1.tar.gz` |
| SHA256 | `6ffcb593207be92584df15b32466ed64bbec99109f007c82205f0194572411a1`（与官方 `SHA256SUMS.txt` 核对一致） |
| 许可证 | BSD-3-Clause（见 `opus/COPYING`） |
| 引入日期 | 2026-10-22 |

### 裁剪

解压后删除以下目录和文件，体积由 29 MB 降至 6 MB：

- `dnn/`：深度 PLC / DRED / OSCE 的神经网络代码与模型，相关选项默认关闭，项目不使用。
- `tests/`、`doc/`
- `meson/`、`meson.build`、`meson_options.txt`

**必须保留**（许可证与构建所需）：`COPYING`、`AUTHORS`、`README`、`package_version`、`configure.ac`（CMake 从中读取版本号）、`Makefile.am`、`*_sources.mk`、`*_headers.mk`（CMake 从中读取源文件清单）、`cmake/`。

### 升级步骤

```sh
VERSION=1.6.1
curl -fsSLO https://downloads.xiph.org/releases/opus/opus-$VERSION.tar.gz
curl -fsSL https://downloads.xiph.org/releases/opus/SHA256SUMS.txt | grep " opus-$VERSION.tar.gz" | shasum -a 256 -c
tar xzf opus-$VERSION.tar.gz
rsync -a --delete --exclude /dnn --exclude /tests --exclude /doc --exclude /meson --exclude 'meson*' \
  opus-$VERSION/ core/third_party/opus/
make test-core && make opus-xcframework
```

升级后需要同步更新本文件中的版本号和校验和，以及 `apple/scripts/build-opus-xcframework.sh` 中的版本戳。

### 构建注意事项

- `core/CMakeLists.txt` 在 `add_subdirectory` 之前会临时关闭父工程的 `BUILD_TESTING`。libopus 的判断条件是 `OPUS_BUILD_TESTING OR BUILD_TESTING`，否则会去编译已删除的 `tests/`。
- macOS arm64 上 `CMAKE_SYSTEM_PROCESSOR` 为 `arm64`，不匹配 libopus 判断用的 `aarch64`，需要显式设置 `OPUS_PRESUME_NEON=ON`。iOS 交叉编译时需要传入 `-DCMAKE_SYSTEM_PROCESSOR=aarch64`。
