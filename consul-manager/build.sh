#!/bin/bash

# Clean previous build
rm -rf dist
mkdir -p dist/static

# Build for Linux (AMD64)
echo "🐧 Building for Linux (AMD64)..."
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/consul-manager-linux main.go
echo "✅ Built binary"

# Copy resources
echo "📂 Copying resource files..."
cp config.json dist/
cp -r static/* dist/static/

# Basic README
echo "Run: chmod +x consul-manager-linux && ./consul-manager-linux" > dist/README.txt

# Create ZIP
echo "📦 Zipping package..."
cd dist
zip -r ../consul-manager-deploy.zip .
cd ..

echo "🎉 Done! Deploy file: consul-manager-deploy.zip"
echo "👉 Upload this zip to your server, unzip, and run."
