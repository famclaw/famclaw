package imageutil

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Helper function to create a test image with specified dimensions
func createTestImage(width, height int) []byte {
	// Create a new RGBA image
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	
	// Fill with a gradient pattern
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8((x * 255) / width)
			g := uint8((y * 255) / height)
			b := uint8(((x + y) * 255) / (width + height))
			img.Set(x, y, color.RGBA{r, g, b, 255})
		}
	}
	
	// Encode as JPEG
	buf := new(bytes.Buffer)
	jpeg.Encode(buf, img, &jpeg.Options{Quality: 90})
	
	return buf.Bytes()
}

func TestResizeImage(t *testing.T) {
	// Test case 1: Image smaller than max dimension should pass through unchanged
	t.Run("Small image passes through", func(t *testing.T) {
		// Create a small image (50x50)
		smallImageData := createTestImage(50, 50)
		
		resizedData, err := ResizeImage(smallImageData, 1280)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		// Should be roughly the same size since we don't resize smaller images
		assert.Greater(t, len(resizedData), 0)
	})
	
	// Test case 2: Landscape image larger than max dimension should be resized
	t.Run("Large landscape image resized", func(t *testing.T) {
		// Create a large landscape image (2000x1000)
		largeLandscapeData := createTestImage(2000, 1000)
		
		resizedData, err := ResizeImage(largeLandscapeData, 1280)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		
		// Verify it's smaller than the original
		assert.Less(t, len(resizedData), len(largeLandscapeData))
		
		// Decode the resized image to verify dimensions
		resizedImg, _, err := image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)
		
		bounds := resizedImg.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()
		
		// Should be within max dimension
		assert.True(t, width <= 1280 && height <= 1280)
		
		// Should preserve aspect ratio approximately
		origRatio := float64(2000) / float64(1000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)
	})
	
	// Test case 3: Portrait image larger than max dimension should be resized
	t.Run("Large portrait image resized", func(t *testing.T) {
		// Create a large portrait image (1000x2000)
		largePortraitData := createTestImage(1000, 2000)
		
		resizedData, err := ResizeImage(largePortraitData, 1280)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		
		// Verify it's smaller than the original
		assert.Less(t, len(resizedData), len(largePortraitData))
		
		// Decode the resized image to verify dimensions
		resizedImg, _, err := image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)
		
		bounds := resizedImg.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()
		
		// Should be within max dimension
		assert.True(t, width <= 1280 && height <= 1280)
		
		// Should preserve aspect ratio approximately
		origRatio := float64(1000) / float64(2000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)
	})
	
	// Test case 4: Square image larger than max dimension should be resized
	t.Run("Large square image resized", func(t *testing.T) {
		// Create a large square image (2000x2000)
		largeSquareData := createTestImage(2000, 2000)
		
		resizedData, err := ResizeImage(largeSquareData, 1280)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		
		// Verify it's smaller than the original
		assert.Less(t, len(resizedData), len(largeSquareData))
		
		// Decode the resized image to verify dimensions
		resizedImg, _, err := image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)
		
		bounds := resizedImg.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()
		
		// Should be within max dimension
		assert.True(t, width <= 1280 && height <= 1280)
		
		// Should preserve aspect ratio approximately
		origRatio := float64(2000) / float64(2000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)
	})
	
	// Test case 5: Edge case - image exactly at max dimension
	t.Run("Image at max dimension", func(t *testing.T) {
		// Create an image exactly at max dimension (1280x1280)
		exactImageData := createTestImage(1280, 1280)
		
		resizedData, err := ResizeImage(exactImageData, 1280)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		// Should be roughly the same size since we don't resize exact matches
		assert.Greater(t, len(resizedData), 0)
	})
	
	// Test case 6: Invalid image data should return error
	t.Run("Invalid image data", func(t *testing.T) {
		invalidData := []byte("this is not a valid image")
		
		_, err := ResizeImage(invalidData, 1280)
		assert.Error(t, err)
	})
}

// Test edge cases with different max dimensions
func TestResizeImageDifferentMaxDims(t *testing.T) {
	// Test with smaller max dimension
	t.Run("Smaller max dimension", func(t *testing.T) {
		// Create a large landscape image (2000x1000)
		largeLandscapeData := createTestImage(2000, 1000)
		
		resizedData, err := ResizeImage(largeLandscapeData, 640)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		
		// Decode the resized image to verify dimensions
		resizedImg, _, err := image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)
		
		bounds := resizedImg.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()
		
		// Should be within smaller max dimension
		assert.True(t, width <= 640 && height <= 640)
	})
}
