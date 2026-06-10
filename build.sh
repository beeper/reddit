#!/bin/sh
set -eu

BINARY_NAME=reddit go tool maubuild "$@"
