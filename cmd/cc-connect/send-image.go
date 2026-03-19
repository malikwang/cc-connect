package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func runSendImage(args []string) {
	var project, sessionKey, dataDir, imagePath string
	var useStdin bool

	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--project", "-p":
			if i+1 < len(args) {
				i++
				project = args[i]
			}
		case "--session", "-s":
			if i+1 < len(args) {
				i++
				sessionKey = args[i]
			}
		case "--data-dir":
			if i+1 < len(args) {
				i++
				dataDir = args[i]
			}
		case "--stdin":
			useStdin = true
		case "--help", "-h":
			printSendImageUsage()
			return
		default:
			positional = append(positional, args[i])
		}
	}

	var imageData []byte
	var err error

	if useStdin {
		imageData, err = io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading stdin: %v\n", err)
			os.Exit(1)
		}
	} else if len(positional) > 0 {
		imagePath = positional[0]
		imageData, err = os.ReadFile(imagePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading image file: %v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Fprintln(os.Stderr, "Error: image file path required (or use --stdin)")
		printSendImageUsage()
		os.Exit(1)
	}

	if len(imageData) == 0 {
		fmt.Fprintln(os.Stderr, "Error: image data is empty")
		os.Exit(1)
	}

	// If not provided via flags, try environment variables
	if project == "" {
		project = os.Getenv("CC_PROJECT")
	}
	if sessionKey == "" {
		sessionKey = os.Getenv("CC_SESSION_KEY")
	}

	// Detect MIME type
	mimeType := detectImageMimeType(imageData, imagePath)

	sockPath := resolveSocketPath(dataDir)
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: cc-connect is not running (socket not found: %s)\n", sockPath)
		os.Exit(1)
	}

	// Encode image data as base64 for JSON transport
	encodedData := base64.StdEncoding.EncodeToString(imageData)

	payload, _ := json.Marshal(map[string]string{
		"project":     project,
		"session_key": sessionKey,
		"image_data":  encodedData,
		"mime_type":   mimeType,
	})

	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", sockPath)
			},
		},
	}

	resp, err := client.Post("http://unix/send-image", "application/json", bytes.NewReader(payload))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to connect: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "Error: %s\n", strings.TrimSpace(string(body)))
		os.Exit(1)
	}

	fmt.Println("Image sent successfully.")
}

func detectImageMimeType(data []byte, filename string) string {
	// Try to detect from file extension first
	if filename != "" {
		ext := strings.ToLower(filepath.Ext(filename))
		switch ext {
		case ".png":
			return "image/png"
		case ".jpg", ".jpeg":
			return "image/jpeg"
		case ".gif":
			return "image/gif"
		case ".webp":
			return "image/webp"
		case ".bmp":
			return "image/bmp"
		}
	}

	// Detect from magic bytes
	if len(data) >= 8 {
		if data[0] == 0x89 && data[1] == 'P' && data[2] == 'N' && data[3] == 'G' {
			return "image/png"
		}
		if data[0] == 0xFF && data[1] == 0xD8 {
			return "image/jpeg"
		}
		if string(data[:4]) == "GIF8" {
			return "image/gif"
		}
		if string(data[:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "WEBP" {
			return "image/webp"
		}
		if data[0] == 'B' && data[1] == 'M' {
			return "image/bmp"
		}
	}

	// Default to PNG
	return "image/png"
}

func mimeTypeToExtension(mimeType string) string {
	ext, _ := mime.ExtensionsByType(mimeType)
	if len(ext) > 0 {
		return ext[0]
	}
	return ".png"
}

func printSendImageUsage() {
	fmt.Println(`Usage: cc-connect send-image [options] <image-file>
       cc-connect send-image [options] --stdin < image.png
       cat image.png | cc-connect send-image [options] --stdin

Send an image to an active cc-connect session.

Options:
  -p, --project <name>     Target project (optional if only one project)
  -s, --session <key>      Target session key (optional, picks first active)
      --stdin              Read image from stdin
      --data-dir <path>    Data directory (default: ~/.cc-connect)
  -h, --help               Show this help

Examples:
  cc-connect send-image screenshot.png
  cc-connect send-image -p my-project chart.png
  cc-connect send-image --stdin < output.png`)
}
