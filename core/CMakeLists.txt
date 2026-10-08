cmake_minimum_required(VERSION 3.20)

project(jkmeeting_core LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

include(CTest)

add_library(jkmeeting_core
    src/audio_processor.cpp
)

target_include_directories(jkmeeting_core PUBLIC include)
target_compile_options(jkmeeting_core PRIVATE -Wall -Wextra -Wpedantic -Werror)

add_executable(audio_processor_test
    tests/audio_processor_test.cpp
)

target_link_libraries(audio_processor_test PRIVATE jkmeeting_core)

target_include_directories(audio_processor_test PRIVATE include)

target_compile_options(audio_processor_test PRIVATE -Wall -Wextra -Wpedantic -Werror)

add_test(NAME audio_processor_test COMMAND audio_processor_test)
