cmake_minimum_required(VERSION 3.16)
project(simple-mcp-cpp LANGUAGES CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release CACHE STRING "Build type" FORCE)
endif()

# The library is header + sources; nothing outside the C++ standard library and
# POSIX sockets is used.
set(MCP_SOURCES
  src/json.cpp
  src/jsonrpc.cpp
  src/types.cpp
  src/transport_stdio.cpp
  src/transport_http.cpp
  src/server.cpp
  src/client.cpp
)

add_library(mcp STATIC ${MCP_SOURCES})
target_include_directories(mcp PUBLIC ${CMAKE_CURRENT_SOURCE_DIR}/include)
target_compile_options(mcp PRIVATE -Wall -Wextra)

enable_testing()

add_executable(test_mcp tests/test_main.cpp)
target_link_libraries(test_mcp PRIVATE mcp pthread)
add_test(NAME cpp_unit COMMAND test_mcp)

# Demo binaries. server_main speaks stdio (what Claude Desktop expects) or,
# with -http, a minimal Streamable HTTP endpoint. client_main spawns the server
# as a child process, the way a real agent host does.
add_executable(mcp_server examples/server_main.cpp)
target_link_libraries(mcp_server PRIVATE mcp pthread)

add_executable(mcp_client examples/client_main.cpp)
target_link_libraries(mcp_client PRIVATE mcp pthread)

# Connects to the Go server over HTTP. It is also the only test the C++ HTTP
# transport gets: there is no HTTP server in this tree, so nothing else can
# reach it.
add_executable(mcp_http_client examples/http_client_main.cpp)
target_link_libraries(mcp_http_client PRIVATE mcp pthread)
