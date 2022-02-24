#!/bin/bash

echo "Building Go functions"
go_test_dirs=("metric" "log" "timeout" "trace")
cd src
for go_dir in "${go_test_dirs[@]}"; do
    echo "go build for $ARCHITECTURE"
    env GOOS=linux GOARCH=$ARCHITECTURE go build -ldflags="-s -w" -o bin/"$go_dir" go-tests/"$go_dir"/main.go
done