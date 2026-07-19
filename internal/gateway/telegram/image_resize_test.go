package telegram

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
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

// Helper function to create a PNG test image with specified dimensions
func createTestPNGImage(width, height int) []byte {
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

	// Encode as PNG
	buf := new(bytes.Buffer)
	png.Encode(buf, img)

	return buf.Bytes()
}

func TestResizeImageToConstraints(t *testing.T) {
	// Test case 1: Image smaller than constraints should pass through unchanged
	t.Run("Small image passes through", func(t *testing.T) {
		// Create a small image (50x50)
		smallImageData := createTestImage(50, 50)

		resizedData, err := resizeImageToConstraints(smallImageData)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		// Should be roughly the same size since we don't resize smaller images
		assert.Greater(t, len(resizedData), 0)
	})

	// Test case 2: Landscape image larger than max width should be resized
	t.Run("Large landscape image resized to fit width", func(t *testing.T) {
		// Create a large landscape image (2000x1000)
		largeLandscapeData := createTestImage(2000, 1000)

		resizedData, err := resizeImageToConstraints(largeLandscapeData)
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

		// Should fit within 1280x720
		assert.True(t, width <= 1280 && height <= 720)

		// Should preserve aspect ratio approximately
		origRatio := float64(2000) / float64(1000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)

		// Should be scaled to fit width of 1280 (since that's the limiting factor)
		assert.Equal(t, 1280, width)
	})

	// Test case 3: Portrait image larger than max height should be resized
	t.Run("Large portrait image resized to fit height", func(t *testing.T) {
		// Create a large portrait image (1000x2000)
		largePortraitData := createTestImage(1000, 2000)

		resizedData, err := resizeImageToConstraints(largePortraitData)
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

		// Should fit within 1280x720
		assert.True(t, width <= 1280 && height <= 720)

		// Should preserve aspect ratio approximately
		origRatio := float64(1000) / float64(2000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)

		// Should be scaled to fit height of 720 (since that's the limiting factor)
		assert.Equal(t, 720, height)
	})

	// Test case 4: Square image larger than both constraints should be resized
	t.Run("Large square image resized", func(t *testing.T) {
		// Create a large square image (2000x2000)
		largeSquareData := createTestImage(2000, 2000)

		resizedData, err := resizeImageToConstraints(largeSquareData)
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

		// Should fit within 1280x720
		assert.True(t, width <= 1280 && height <= 720)

		// Should preserve aspect ratio approximately
		origRatio := float64(2000) / float64(2000)
		resizedRatio := float64(width) / float64(height)
		assert.InEpsilon(t, origRatio, resizedRatio, 0.01)
	})

	// Test case 5: Edge case - image exactly at constraints
	t.Run("Image at constraints", func(t *testing.T) {
		// Create an image exactly at constraints (1280x720)
		exactImageData := createTestImage(1280, 720)

		resizedData, err := resizeImageToConstraints(exactImageData)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)
		// Should be roughly the same size since we don't resize exact matches
		assert.Greater(t, len(resizedData), 0)
	})

	// Test case 6: Edge case - image slightly larger than constraints
	t.Run("Image slightly larger than constraints", func(t *testing.T) {
		// Create an image slightly larger than constraints (1300x750)
		largerImageData := createTestImage(1300, 750)

		resizedData, err := resizeImageToConstraints(largerImageData)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)

		// Decode the resized image to verify dimensions
		resizedImg, _, err := image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)

		bounds := resizedImg.Bounds()
		width := bounds.Dx()
		height := bounds.Dy()

		// Should fit within 1280x720
		assert.True(t, width <= 1280 && height <= 720)

		// Should be scaled properly
		assert.True(t, width >= 1200 || height >= 700) // At least somewhat scaled
	})

	// Test case 7: Test with PNG image
	t.Run("PNG image handling", func(t *testing.T) {
		// Create a large PNG image (2000x1000)
		largePNGData := createTestPNGImage(2000, 1000)

		resizedData, err := resizeImageToConstraints(largePNGData)
		assert.NoError(t, err)
		assert.NotNil(t, resizedData)

		// Should still be able to decode the result (should be JPEG encoded)
		_, _, err = image.Decode(bytes.NewReader(resizedData))
		assert.NoError(t, err)
	})
}
