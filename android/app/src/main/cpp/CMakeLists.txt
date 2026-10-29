cmake_minimum_required(VERSION 3.22.1)
project(jkmeeting_android LANGUAGES C CXX)

# 直接复用仓库 core 的同一份 CMake 工程：Android 接入不允许改 core 源码，
# 这里若需要任何 core 侧补丁，都说明抽象有缺口，必须先记录到 P4 报告。
get_filename_component(REPO_ROOT "${CMAKE_CURRENT_SOURCE_DIR}/../../../../.." ABSOLUTE)
# vendored opus 裁剪了 dnn/arm 等 RTCD 源文件：桌面（Apple）因 RTCD 检测路径不同未受影响，
# Android 上则会引用缺失文件。此处在壳层关闭 intrinsics 走便携 C 实现（功能等价，性能基线仍以桌面为准），
# 属于壳层构建配置，core 零修改；缺口记录于 P4 报告。
set(OPUS_DISABLE_INTRINSICS ON CACHE BOOL "" FORCE)
add_subdirectory("${REPO_ROOT}/core" "${CMAKE_BINARY_DIR}/core")

# AGP 会构建 CMake 全部目标：core 桌面单测/bench 在 NDK clang 下会因 designated initializer
# 省略字段触发 -Werror=missing-field-initializers（桌面 clang 不报）。壳层为这些目标降级该警告，
# 不改 core 源码与警告口径；它们不进入 APK，只是构建时被顺带编译。
foreach(desktop_target
    audio_processor_test streaming_resampler_test frame_timeline_test silence_detector_test
    audio_session_test ogg_opus_muxer_test opus_encoder_test ogg_opus_encoder_test
    ogg_packet_reader_test ogg_opus_decoder_test jitter_buffer_test
    audio_session_bench ogg_opus_encode ogg_opus_decode)
    if(TARGET ${desktop_target})
        target_compile_options(${desktop_target} PRIVATE -Wno-error=missing-field-initializers)
    endif()
endforeach()

add_library(jkmeeting_jni SHARED jni_bridge.cpp)
target_link_libraries(jkmeeting_jni PRIVATE jkmeeting_core log)
target_compile_options(jkmeeting_jni PRIVATE -Wall -Wextra)
