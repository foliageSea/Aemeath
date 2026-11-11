cmake_minimum_required(VERSION 3.22)

# Keep the firmware app descriptor in sync with every other release artifact.
set(AEMEATH_VERSION_FILE "${CMAKE_CURRENT_LIST_DIR}/../VERSION")
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS "${AEMEATH_VERSION_FILE}")
file(READ "${AEMEATH_VERSION_FILE}" PROJECT_VER)
string(STRIP "${PROJECT_VER}" PROJECT_VER)
if(NOT PROJECT_VER MATCHES "^[0-9]+\\.[0-9]+\\.[0-9]+([-+][0-9A-Za-z.+-]+)?$")
    message(FATAL_ERROR "VERSION must contain a semantic version")
endif()

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(aemeath)
