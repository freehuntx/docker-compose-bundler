#!/bin/bash

# Build script for docker-compose-bundler

echo "Building docker-compose-bundler..."

# Get dependencies
go mod download

# Build the binary
go build -o docker-compose-bundler main.go

if [ $? -eq 0 ]; then
    echo "Build successful! Binary created: docker-compose-bundler"
    echo "Usage: ./docker-compose-bundler <docker-compose.yml> [output.tar.gz]"
else
    echo "Build failed!"
    exit 1
fi