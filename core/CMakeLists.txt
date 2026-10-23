cmake_minimum_required(VERSION 3.20)

project(jkmeeting_core LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

include(CTest)

enable_language(C)
if(NOT CMAKE_BUILD_TYPE AND NOT CMAKE_CONFIGURATION_TYPES)
    # libopus 会把空构建类型改成 Release（定义 NDEBUG），使 core 测试里的 assert 失效。
    set(CMAKE_BUILD_TYPE Debug CACHE STRING "" FORCE)
endif()
set(_jkmeeting_build_testing ${BUILD_TESTING})
# libopus 以 OPUS_BUILD_TESTING OR BUILD_TESTING 决定是否编译 tests/，而该目录已被裁剪。
set(BUILD_TESTING OFF)
set(OPUS_BUILD_TESTING OFF CACHE BOOL "" FORCE)
set(OPUS_BUILD_PROGRAMS OFF CACHE BOOL "" FORCE)
set(OPUS_INSTALL_PKG_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
set(OPUS_INSTALL_CMAKE_CONFIG_MODULE OFF CACHE BOOL "" FORCE)
if(APPLE AND CMAKE_SYSTEM_PROCESSOR STREQUAL "arm64")
    # libopus 只在 aarch64 / iOS 上自动假定 NEON。OPUS_MAY_HAVE_NEON 须保持 ON：
    # NEON 内建函数的声明依赖 OPUS_ARM_MAY_HAVE_NEON_INTR；其输出的 "ERROR..." 仅是提示文本。
    set(OPUS_PRESUME_NEON ON CACHE BOOL "" FORCE)
endif()
add_subdirectory(third_party/opus EXCLUDE_FROM_ALL)
set(BUILD_TESTING ${_jkmeeting_build_testing})

add_library(jkmeeting_core
    src/audio_processor.cpp
    src/streaming_resampler.cpp
    src/frame_timeline.cpp
    src/silence_detector.cpp
    src/audio_session.cpp
    src/ogg_opus_muxer.cpp
    src/opus_encoder.cpp
    src/ogg_opus_encoder.cpp
    src/ogg_packet_reader.cpp
    src/ogg_opus_decoder.cpp
)

target_include_directories(jkmeeting_core PUBLIC include)
target_compile_options(jkmeeting_core PRIVATE -Wall -Wextra -Wpedantic -Werror)
# 静态库的 PRIVATE 依赖不会传到最终链接；公开头文件仍不包含 opus.h。
target_link_libraries(jkmeeting_core PUBLIC opus)

function(jkmeeting_core_test name)
    add_executable(${name} tests/${name}.cpp)
    target_link_libraries(${name} PRIVATE jkmeeting_core)
    target_include_directories(${name} PRIVATE include tests)
    target_compile_options(${name} PRIVATE -Wall -Wextra -Wpedantic -Werror)
    add_test(NAME ${name} COMMAND ${name})
endfunction()

jkmeeting_core_test(audio_processor_test)
jkmeeting_core_test(streaming_resampler_test)
jkmeeting_core_test(frame_timeline_test)
jkmeeting_core_test(silence_detector_test)
jkmeeting_core_test(audio_session_test)
jkmeeting_core_test(ogg_opus_muxer_test)
jkmeeting_core_test(opus_encoder_test)
jkmeeting_core_test(ogg_opus_encoder_test)
jkmeeting_core_test(ogg_packet_reader_test)
jkmeeting_core_test(ogg_opus_decoder_test)

add_executable(audio_session_bench bench/audio_session_bench.cpp)
target_link_libraries(audio_session_bench PRIVATE jkmeeting_core)
target_compile_options(audio_session_bench PRIVATE -Wall -Wextra -Wpedantic -Werror)

add_executable(ogg_opus_encode tools/ogg_opus_encode.cpp)
target_link_libraries(ogg_opus_encode PRIVATE jkmeeting_core)
target_compile_options(ogg_opus_encode PRIVATE -Wall -Wextra -Wpedantic -Werror)

add_executable(ogg_opus_decode tools/ogg_opus_decode.cpp)
target_link_libraries(ogg_opus_decode PRIVATE jkmeeting_core)
target_compile_options(ogg_opus_decode PRIVATE -Wall -Wextra -Wpedantic -Werror)
