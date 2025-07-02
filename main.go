package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/docker/docker/api/types/build"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"gopkg.in/yaml.v3"
)

// XBundle holds bundle metadata
type XBundle struct {
	Name    string `yaml:"name"`
	Version string `yaml:"version"`
}

type DockerCompose struct {
	Version  string                 `yaml:"version"`
	Services map[string]Service     `yaml:"services"`
	Networks map[string]interface{} `yaml:"networks,omitempty"`
	Volumes  map[string]interface{} `yaml:"volumes,omitempty"`
	Configs  map[string]interface{} `yaml:"configs,omitempty"`
	Secrets  map[string]interface{} `yaml:"secrets,omitempty"`
	XBundle  *XBundle               `yaml:"x-bundle"`
}

type Service struct {
	Image       string                 `yaml:"image,omitempty"`
	Build       interface{}            `yaml:"build,omitempty"`
	Environment interface{}            `yaml:"environment,omitempty"` // Can be []string or map[string]string
	Volumes     []string               `yaml:"volumes,omitempty"`
	Ports       []string               `yaml:"ports,omitempty"`
	Networks    []string               `yaml:"networks,omitempty"`
	DependsOn   interface{}            `yaml:"depends_on,omitempty"` // Can be []string or map[string]interface{}
	Command     interface{}            `yaml:"command,omitempty"`
	Entrypoint  interface{}            `yaml:"entrypoint,omitempty"`
	Restart     string                 `yaml:"restart,omitempty"`
	Extra       map[string]interface{} `yaml:",inline"`
}

type BuildConfig struct {
	Context    string            `yaml:"context,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty"`
	Args       map[string]string `yaml:"args,omitempty"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: docker-compose-bundler <docker-compose.yml> [output.tar.gz]")
		os.Exit(1)
	}

	composeFile := os.Args[1]
	outputFile := "bundle.tar.gz"
	if len(os.Args) > 2 {
		outputFile = os.Args[2]
	}

	bundler := NewBundler()
	if err := bundler.Bundle(composeFile, outputFile); err != nil {
		log.Fatal(err)
	}

	fmt.Printf("Successfully created bundle: %s\n", outputFile)
}

type Bundler struct {
	client              *client.Client
	ctx                 context.Context
	freshlyPulledImages map[string]bool // Track images pulled during this run
}

func NewBundler() *Bundler {
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatal("Failed to create Docker client:", err)
	}

	return &Bundler{
		client:              cli,
		ctx:                 context.Background(),
		freshlyPulledImages: make(map[string]bool),
	}
}

func (b *Bundler) Bundle(composeFile, outputFile string) error {
	// Read and parse docker-compose.yml
	compose, err := b.parseComposeFile(composeFile)
	if err != nil {
		return fmt.Errorf("failed to parse compose file: %w", err)
	}

	// Validate x-bundle
	if compose.XBundle == nil {
		return fmt.Errorf("missing x-bundle entry in compose file")
	}
	if compose.XBundle.Name == "" {
		return fmt.Errorf("missing name in x-bundle")
	}
	if compose.XBundle.Version == "" {
		return fmt.Errorf("missing version in x-bundle")
	}
	if !isValidSemver(compose.XBundle.Version) {
		return fmt.Errorf("invalid version in x-bundle, must be valid semantic versioning (e.g., 1.2.3)")
	}

	bundleName := compose.XBundle.Name
	bundleVersion := compose.XBundle.Version

	// Process services and collect image information
	imageMap := make(map[string]string) // original -> saved tar filename

	for serviceName, service := range compose.Services {
		imageName, err := b.processServiceWithBundle(serviceName, &service, filepath.Dir(composeFile), bundleName, bundleVersion)
		if err != nil {
			return fmt.Errorf("failed to process service %s: %w", serviceName, err)
		}

		if imageName != "" {
			tarFileName := fmt.Sprintf("%s.tar", sanitizeFilename(imageName))
			imageMap[imageName] = tarFileName
			// Update the service in the compose struct
			compose.Services[serviceName] = service
		}
	}

	// Create temporary directory for bundle contents
	tempDir, err := os.MkdirTemp("", "docker-compose-bundle-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Create images directory
	imagesDir := filepath.Join(tempDir, "images")
	if err := os.MkdirAll(imagesDir, 0755); err != nil {
		return fmt.Errorf("failed to create images directory: %w", err)
	}

	// Save images to tar files
	for imageName, tarFileName := range imageMap {
		tarPath := filepath.Join(imagesDir, tarFileName)
		if err := b.saveImage(imageName, tarPath); err != nil {
			return fmt.Errorf("failed to save image %s: %w", imageName, err)
		}
	}

	// Update compose file to use bundled images
	b.updateComposeForBundle(compose, imageMap)

	// Write updated compose file
	updatedComposePath := filepath.Join(tempDir, "docker-compose.yml")
	if err := b.writeComposeFile(compose, updatedComposePath); err != nil {
		return fmt.Errorf("failed to write updated compose file: %w", err)
	}

	// Create load script
	if err := b.createLoadScript(tempDir); err != nil {
		return fmt.Errorf("failed to create load script: %w", err)
	}

	// Create README
	if err := b.createReadme(tempDir); err != nil {
		return fmt.Errorf("failed to create README: %w", err)
	}

	// Create the final tar.gz bundle
	if err := b.createTarGz(tempDir, outputFile); err != nil {
		return fmt.Errorf("failed to create bundle: %w", err)
	}

	// Cleanup built and freshly pulled images
	if err := b.cleanupImages(compose); err != nil {
		fmt.Printf("Warning: failed to cleanup some images: %v\n", err)
	}
	if err := b.cleanupFreshlyPulledImages(); err != nil {
		fmt.Printf("Warning: failed to cleanup some freshly pulled images: %v\n", err)
	}
	return nil
}

func (b *Bundler) parseComposeFile(filename string) (*DockerCompose, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	var compose DockerCompose
	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	return &compose, nil
}

// isValidSemver checks if a version string is valid semver (simple regex)
func isValidSemver(version string) bool {
	semverRegex := regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[\w\.-]+)?(?:\+[\w\.-]+)?$`)
	return semverRegex.MatchString(version)
}

// processServiceWithBundle tags built images with bundle name and version
func (b *Bundler) processServiceWithBundle(serviceName string, service *Service, baseDir, bundleName, bundleVersion string) (string, error) {
	if service.Build != nil {
		imageName := fmt.Sprintf("bundles/%s/%s:%s", bundleName, serviceName, bundleVersion)
		buildConfig, err := parseBuildConfig(service.Build)
		if err != nil {
			return "", err
		}
		if err := b.buildImage(buildConfig, baseDir, imageName); err != nil {
			return "", err
		}
		service.Image = imageName
		service.Build = nil
		return imageName, nil
	}
	if service.Image != "" {
		if err := b.pullImageIfNotExists(service.Image); err != nil {
			return "", err
		}
		return service.Image, nil
	}
	return "", nil
}

func (b *Bundler) cleanupImages(compose *DockerCompose) error {
	// Track which images were built by this bundler
	builtImages := make(map[string]bool)

	for _, service := range compose.Services {
		if service.Image != "" && strings.HasPrefix(service.Image, "bundled-") {
			builtImages[service.Image] = true
		}
	}

	// Remove only the images we built
	for imageName := range builtImages {
		fmt.Printf("Removing built image %s...\n", imageName)
		_, err := b.client.ImageRemove(b.ctx, imageName, image.RemoveOptions{
			Force:         false,
			PruneChildren: true,
		})
		if err != nil {
			// Log but don't fail the entire operation
			fmt.Printf("Warning: failed to remove image %s: %v\n", imageName, err)
		}
	}

	return nil
}

func (b *Bundler) cleanupFreshlyPulledImages() error {
	for imageName := range b.freshlyPulledImages {
		fmt.Printf("Removing freshly pulled image %s...\n", imageName)
		_, err := b.client.ImageRemove(b.ctx, imageName, image.RemoveOptions{
			Force:         false,
			PruneChildren: true,
		})
		if err != nil {
			fmt.Printf("Warning: failed to remove freshly pulled image %s: %v\n", imageName, err)
		}
	}
	return nil
}

func parseBuildConfig(build interface{}) (*BuildConfig, error) {
	switch v := build.(type) {
	case string:
		return &BuildConfig{Context: v}, nil
	case map[string]interface{}:
		data, err := yaml.Marshal(v)
		if err != nil {
			return nil, err
		}
		var config BuildConfig
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, err
		}
		return &config, nil
	default:
		return nil, fmt.Errorf("invalid build configuration type")
	}
}

func (b *Bundler) buildImage(config *BuildConfig, baseDir, imageName string) error {
	buildContext := config.Context
	if !filepath.IsAbs(buildContext) {
		buildContext = filepath.Join(baseDir, buildContext)
	}

	dockerfile := config.Dockerfile
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}

	fmt.Printf("Building image %s from %s...\n", imageName, buildContext)

	// Create tar of build context
	buildContextTar, err := createBuildContextTar(buildContext)
	if err != nil {
		return err
	}
	defer buildContextTar.Close()

	// Convert BuildArgs from map[string]string to map[string]*string
	buildArgs := make(map[string]*string)
	for k, v := range config.Args {
		value := v // Create a copy to avoid pointer to loop variable
		buildArgs[k] = &value
	}

	buildOptions := build.ImageBuildOptions{
		Dockerfile: dockerfile,
		Tags:       []string{imageName},
		Remove:     true,
		BuildArgs:  buildArgs,
	}

	resp, err := b.client.ImageBuild(b.ctx, buildContextTar, buildOptions)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Read build output
	decoder := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Stream string `json:"stream"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("build error: %s", msg.Error)
		}
		if msg.Stream != "" {
			fmt.Print(msg.Stream)
		}
	}

	return nil
}

func (b *Bundler) pullImageIfNotExists(imageName string) error {
	// Check if image exists locally
	_, err := b.client.ImageInspect(b.ctx, imageName)
	if err == nil {
		fmt.Printf("Image %s already exists locally\n", imageName)
		return nil
	}

	fmt.Printf("Pulling image %s...\n", imageName)

	reader, err := b.client.ImagePull(b.ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()

	// Mark as freshly pulled
	b.freshlyPulledImages[imageName] = true

	// Read pull output
	decoder := json.NewDecoder(reader)
	for {
		var msg struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := decoder.Decode(&msg); err != nil {
			if err == io.EOF {
				break
			}
			return err
		}
		if msg.Error != "" {
			return fmt.Errorf("pull error: %s", msg.Error)
		}
	}

	return nil
}

func (b *Bundler) saveImage(imageName, outputPath string) error {
	fmt.Printf("Saving image %s to %s...\n", imageName, outputPath)

	reader, err := b.client.ImageSave(b.ctx, []string{imageName})
	if err != nil {
		return err
	}
	defer reader.Close()

	file, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(file, reader)
	return err
}

func (b *Bundler) updateComposeForBundle(compose *DockerCompose, imageMap map[string]string) {
	// Update services to use local tar files
	for _, service := range compose.Services {
		if service.Image != "" && imageMap[service.Image] != "" {
			// The image will be loaded from tar, so we keep the original image name
			// The load script will handle loading the images
		}
	}
}

func (b *Bundler) writeComposeFile(compose *DockerCompose, outputPath string) error {
	data, err := yaml.Marshal(compose)
	if err != nil {
		return err
	}

	return os.WriteFile(outputPath, data, 0644)
}

func (b *Bundler) createLoadScript(tempDir string) error {
	script := `#!/bin/bash
set -e

echo "Loading Docker images..."

# Load all images from the images directory
for image in images/*.tar; do
    if [ -f "$image" ]; then
        echo "Loading $image..."
        docker load -i "$image"
    fi
done

echo "All images loaded successfully!"
echo "You can now run: docker-compose up -d"
`

	scriptPath := filepath.Join(tempDir, "load-images.sh")
	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return err
	}

	// Also create a Windows batch script
	batScript := `@echo off
echo Loading Docker images...

for %%f in (images\*.tar) do (
    echo Loading %%f...
    docker load -i "%%f"
)

echo All images loaded successfully!
echo You can now run: docker-compose up -d
`

	batPath := filepath.Join(tempDir, "load-images.bat")
	return os.WriteFile(batPath, []byte(batScript), 0755)
}

func (b *Bundler) createReadme(tempDir string) error {
	readme := `# Docker Compose Bundle

This bundle contains a Docker Compose stack with all required images for offline deployment.

## Contents

- docker-compose.yml - The Docker Compose configuration
- images/ - Directory containing all Docker images as tar files
- load-images.sh - Script to load all images (Linux/Mac)
- load-images.bat - Script to load all images (Windows)

## Usage

1. Extract this bundle to your desired location
2. Load the Docker images:
   - On Linux/Mac: ./load-images.sh
   - On Windows: load-images.bat
3. Start the stack: docker-compose up -d

## Requirements

- Docker Engine installed
- Docker Compose installed

Note: No internet connection is required after extracting this bundle.
`

	readmePath := filepath.Join(tempDir, "README.md")
	return os.WriteFile(readmePath, []byte(readme), 0644)
}

func (b *Bundler) createTarGz(sourceDir, outputFile string) error {
	file, err := os.Create(outputFile)
	if err != nil {
		return err
	}
	defer file.Close()

	gzWriter := gzip.NewWriter(file)
	defer gzWriter.Close()

	tarWriter := tar.NewWriter(gzWriter)
	defer tarWriter.Close()

	return filepath.Walk(sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}

		// Skip the source directory itself
		if relPath == "." {
			return nil
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}

		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}

		if !info.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			_, err = io.Copy(tarWriter, file)
			return err
		}

		return nil
	})
}

func createBuildContextTar(contextPath string) (io.ReadCloser, error) {
	reader, writer := io.Pipe()

	go func() {
		tarWriter := tar.NewWriter(writer)
		defer writer.Close()
		defer tarWriter.Close()

		filepath.Walk(contextPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			relPath, err := filepath.Rel(contextPath, path)
			if err != nil {
				return err
			}

			// Skip .git directory and other common ignore patterns
			if strings.HasPrefix(relPath, ".git") {
				return filepath.SkipDir
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				return err
			}

			header.Name = filepath.ToSlash(relPath)

			if err := tarWriter.WriteHeader(header); err != nil {
				return err
			}

			if !info.IsDir() {
				file, err := os.Open(path)
				if err != nil {
					return err
				}
				defer file.Close()

				_, err = io.Copy(tarWriter, file)
				return err
			}

			return nil
		})
	}()

	return reader, nil
}

func sanitizeFilename(name string) string {
	// Replace special characters that might cause issues in filenames
	replacer := strings.NewReplacer(
		"/", "-",
		":", "-",
		"\\", "-",
		"*", "-",
		"?", "-",
		"\"", "-",
		"<", "-",
		">", "-",
		"|", "-",
	)
	return replacer.Replace(name)
}
