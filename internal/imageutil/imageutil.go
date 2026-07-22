// Package imageutil provides shared image processing utilities.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "image/gif"
	_ "image/png"
)

// ResizeImage resizes an image to fit within maxDim while preserving aspect ratio.
// Returns the image data in JPEG format.
func ResizeImage(imageData []byte, maxDim int) ([]byte, error) {
	// Parse the original image
	img, _, err := image.Decode(bytes.NewReader(imageData))
	if err != nil {
		return nil, fmt.Errorf("decoding image: %w", err)
	}

	// Get original dimensions
	bounds := img.Bounds()
	origWidth := bounds.Dx()
	origHeight := bounds.Dy()

	// Calculate new dimensions preserving aspect ratio
	var newWidth, newHeight int
	if origWidth > maxDim || origHeight > maxDim {
		// Needs scaling
		if origWidth > origHeight {
			// Landscape image
			newWidth = maxDim
			newHeight = int(float64(origHeight) * float64(maxDim) / float64(origWidth))
		} else {
			// Portrait or square image
			newHeight = maxDim
			newWidth = int(float64(origWidth) * float64(maxDim) / float64(origHeight))
		}
	} else {
		// Already within limits
		newWidth = origWidth
		newHeight = origHeight
	}

	// Ensure we don't go below 1 pixel
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}

	var resized image.Image
	if newWidth == origWidth && newHeight == origHeight {
		// No scaling needed, use original
		resized = img
	} else {
		// Create a new image with the target dimensions
		resizedImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))
		// Scale the image using Catmull-Rom resampling
		draw.CatmullRom.Scale(resizedImg, resizedImg.Bounds(), img, img.Bounds(), draw.Over, nil)
		resized = resizedImg
	}

	// Encode to JPEG
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, resized, &jpeg.Options{Quality: 85})
	if err != nil {
		return nil, fmt.Errorf("encoding image: %w", err)
	}

	return buf.Bytes(), nil
}

// MaxImageBytes is the maximum size in bytes for an image (5MB).
const MaxImageBytes = 5 * 1024 * 1024

// IsValidImageMIME checks if a MIME type represents a valid image.
func IsValidImageMIME(mime string) bool {
	return mime != "" && (mime == "image/jpeg" || mime == "image/jpg" || mime == "image/png" || mime == "image/gif" || mime == "image/webp")
}