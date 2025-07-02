# Docker Compose Bundler

A Go utility that bundles a Docker Compose stack with all its images for offline deployment.

> Disclaimer: Im not a go developer and this project is heavily vibe-coded!

## Features

- Analyzes docker-compose.yml files
- Builds images from build contexts
- Pulls remote images
- Saves all images as tar files
- Creates a self-contained bundle that can be deployed without internet access
- Includes load scripts for both Linux/Mac and Windows

## Installation

```bash
go build -o docker-compose-bundler
```

## Usage

```bash
./docker-compose-bundler <docker-compose.yml> [output.tar.gz]
```

Example:
```bash
./docker-compose-bundler docker-compose.yml my-stack-bundle.tar.gz
```

## What it does

1. **Parses** your docker-compose.yml file
2. **Builds** any services that have `build:` directives
3. **Pulls** any services that reference remote images
4. **Saves** all images as tar files
5. **Updates** the compose file to use the bundled images
6. **Creates** a tar.gz bundle containing:
   - Modified docker-compose.yml
   - images/ directory with all image tar files
   - load-images.sh (for Linux/Mac)
   - load-images.bat (for Windows)
   - README with deployment instructions

## Bundle Structure

The generated bundle contains:

```
bundle.tar.gz
├── docker-compose.yml      # Updated compose file
├── images/                 # Directory with image tar files
│   ├── image1.tar
│   ├── image2.tar
│   └── ...
├── load-images.sh         # Linux/Mac script to load images
├── load-images.bat        # Windows script to load images
└── README.md             # Deployment instructions
```

## Deployment (Offline)

On the target machine:

1. Extract the bundle:
   ```bash
   tar -xzf bundle.tar.gz
   cd bundle/
   ```

2. Load the images:
   - Linux/Mac: `./load-images.sh`
   - Windows: `load-images.bat`

3. Start the stack:
   ```bash
   docker-compose up -d
   ```

## Requirements

- Go 1.24 or later
- Docker Engine running locally
- docker-compose.yml file to bundle

## Example docker-compose.yml

```yaml
x-bundle:
  name: example
  version: 0.0.1
services:
  web:
    build: ./web
    ports:
      - "8080:80"
  
  database:
    image: postgres:15
    environment:
      POSTGRES_PASSWORD: secret
    volumes:
      - db-data:/var/lib/postgresql/data

volumes:
  db-data:
```

This tool will:
- Build the `web` service from the `./web` directory
- Pull the `postgres:15` image
- Bundle both images for offline deployment

## License

MIT