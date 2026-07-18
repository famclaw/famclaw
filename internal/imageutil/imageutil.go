// Package imageutil provides shared image processing utilities.
package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"

	"golang.org/x/image/draw"
)

// ResizeImage resizes an image to fit within maxDim while preserving aspect ratio.
// Returns the resized image data in JPEG format.
// If the image is already smaller than maxDim, it returns the original data unchanged.
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

	// If image is already smaller than max dimensions, return as-is
	if origWidth <= maxDim && origHeight <= maxDim {
		return imageData, nil
	}

	// Calculate new dimensions preserving aspect ratio
	var newWidth, newHeight int
	if origWidth > origHeight {
		// Landscape image
		newWidth = maxDim
		newHeight = int(float64(origHeight) * float64(maxDim) / float64(origWidth))
	} else {
		// Portrait or square image
		newHeight = maxDim
		newWidth = int(float64(origWidth) * float64(maxDim) / float64(origHeight))
	}

	// Ensure we don't go below 1 pixel
	if newWidth < 1 {
		newWidth = 1
	}
	if newHeight < 1 {
		newHeight = 1
	}

	// Create a new image with the target dimensions
	resizedImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Scale the image using bilinear interpolation
	draw.NearestNeighbor.Scale(resizedImg, resizedImg.Bounds(), img, img.Bounds(), draw.Over, nil)

	// Encode the resized image to JPEG
	var buf bytes.Buffer
	err = jpeg.Encode(&buf, resizedImg, &jpeg.Options{Quality: 85})
	if err != nil {
		return nil, fmt.Errorf("encoding resized image: %w", err)
	}

	return buf.Bytes(), nil
}
