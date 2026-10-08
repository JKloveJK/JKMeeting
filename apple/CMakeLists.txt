cmake_minimum_required(VERSION 3.20)

project(jkmeeting_ios_bridge LANGUAGES CXX OBJCXX)

set(CMAKE_CXX_STANDARD 20)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)

include(CTest)

find_library(FOUNDATION_FRAMEWORK Foundation REQUIRED)

add_subdirectory("${CMAKE_CURRENT_SOURCE_DIR}/../core" "${CMAKE_CURRENT_BINARY_DIR}/core")

add_library(jkmeeting_ios_bridge
    Bridge/JKMeetingCore.mm
)

target_include_directories(jkmeeting_ios_bridge PUBLIC Bridge)
target_compile_options(jkmeeting_ios_bridge PRIVATE -Wall -Wextra -Wpedantic -Werror -fobjc-arc)
target_link_libraries(jkmeeting_ios_bridge PRIVATE jkmeeting_core ${FOUNDATION_FRAMEWORK})

add_executable(jkmeeting_core_bridge_test
    BridgeTests/JKMeetingCoreBridgeTests.mm
)

target_compile_options(jkmeeting_core_bridge_test PRIVATE -Wall -Wextra -Wpedantic -Werror -fobjc-arc)
target_link_libraries(jkmeeting_core_bridge_test PRIVATE jkmeeting_ios_bridge ${FOUNDATION_FRAMEWORK})

add_test(NAME jkmeeting_core_bridge_test COMMAND jkmeeting_core_bridge_test)
