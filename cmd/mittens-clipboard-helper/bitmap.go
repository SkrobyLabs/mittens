package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/png"
)

// BITMAPINFOHEADER is the Windows BITMAPINFOHEADER structure.
type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

const (
	biRGB       = 0
	biBitfields = 3
)

// dibToPNG converts a Device Independent Bitmap (DIB) to PNG bytes.
// The data starts with a BITMAPINFOHEADER (or BITMAPV5HEADER for isDIBV5=true).
func dibToPNG(data []byte, isDIBV5 bool) ([]byte, error) {
	if len(data) < 40 {
		return nil, fmt.Errorf("DIB data too short: %d bytes", len(data))
	}

	var hdr bitmapInfoHeader
	if err := binary.Read(bytes.NewReader(data[:40]), binary.LittleEndian, &hdr); err != nil {
		return nil, fmt.Errorf("failed to read BITMAPINFOHEADER: %w", err)
	}

	width := int(hdr.BiWidth)
	height := int(hdr.BiHeight)

	// Negative height means top-down (origin is top-left).
	topDown := false
	if height < 0 {
		height = -height
		topDown = true
	}

	if width <= 0 || height <= 0 || width > 32768 || height > 32768 {
		return nil, fmt.Errorf("invalid dimensions: %dx%d", width, height)
	}

	// Determine where pixel data starts (after the header and optional color masks/table).
	pixelOffset := int(hdr.BiSize)

	// For BI_BITFIELDS, 3 DWORD color masks follow the header (unless already
	// included in a V4/V5 header which is larger than 40 bytes).
	if hdr.BiCompression == biBitfields && hdr.BiSize == 40 {
		pixelOffset += 12
	}

	// For 8-bit or less, skip the color table.
	if hdr.BiBitCount <= 8 {
		colors := int(hdr.BiClrUsed)
		if colors == 0 {
			colors = 1 << int(hdr.BiBitCount)
		}
		pixelOffset += colors * 4
	}

	if pixelOffset >= len(data) {
		return nil, fmt.Errorf("pixel data offset (%d) exceeds data length (%d)", pixelOffset, len(data))
	}

	pixels := data[pixelOffset:]

	switch hdr.BiBitCount {
	case 32:
		return convert32bpp(pixels, width, height, topDown, hdr.BiCompression, data)
	case 24:
		return convert24bpp(pixels, width, height, topDown)
	default:
		return nil, fmt.Errorf("unsupported bit depth: %d", hdr.BiBitCount)
	}
}

// convert32bpp converts 32-bit BGRA pixel data to PNG.
func convert32bpp(pixels []byte, width, height int, topDown bool, compression uint32, fullData []byte) ([]byte, error) {
	rowStride := width * 4
	needed := rowStride * height
	if len(pixels) < needed {
		return nil, fmt.Errorf("32bpp: need %d bytes but have %d", needed, len(pixels))
	}

	// Read color masks for BI_BITFIELDS if present.
	var rMask, gMask, bMask, aMask uint32
	hasMasks := false

	if compression == biBitfields {
		// Masks are at bytes 40-51 for a 40-byte header, or embedded in V4/V5 headers.
		hdrSize := binary.LittleEndian.Uint32(fullData[:4])
		var maskData []byte
		if hdrSize == 40 && len(fullData) >= 52 {
			maskData = fullData[40:52]
		} else if hdrSize >= 56 && len(fullData) >= 56 {
			// V4/V5 header: masks at offsets 40, 44, 48 (and alpha at 52 if available).
			maskData = fullData[40:52]
			if hdrSize >= 60 && len(fullData) >= 60 {
				aMask = binary.LittleEndian.Uint32(fullData[56:60])
			}
		}
		if len(maskData) >= 12 {
			rMask = binary.LittleEndian.Uint32(maskData[0:4])
			gMask = binary.LittleEndian.Uint32(maskData[4:8])
			bMask = binary.LittleEndian.Uint32(maskData[8:12])
			hasMasks = true
		}
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		rowStart := srcY * rowStride

		for x := 0; x < width; x++ {
			off := rowStart + x*4
			var r, g, b, a uint8

			if hasMasks {
				pixel := binary.LittleEndian.Uint32(pixels[off : off+4])
				r = extractChannel(pixel, rMask)
				g = extractChannel(pixel, gMask)
				b = extractChannel(pixel, bMask)
				if aMask != 0 {
					a = extractChannel(pixel, aMask)
				} else {
					a = 255
				}
			} else {
				// Standard BGRA layout.
				b = pixels[off+0]
				g = pixels[off+1]
				r = pixels[off+2]
				a = pixels[off+3]
				// Many apps set alpha to 0 for opaque pixels in CF_DIB.
				// If all alpha bytes are 0, treat as fully opaque (handled below).
			}

			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}

	// If all alpha values are 0, the image likely has no real alpha channel —
	// set all pixels to fully opaque.
	if !hasMasks || aMask == 0 {
		fixOpaqueAlpha(img)
	}

	return encodePNG(img)
}

// convert24bpp converts 24-bit BGR pixel data to PNG.
func convert24bpp(pixels []byte, width, height int, topDown bool) ([]byte, error) {
	// Rows are padded to 4-byte boundaries.
	rowStride := (width*3 + 3) &^ 3
	needed := rowStride * height
	if len(pixels) < needed {
		return nil, fmt.Errorf("24bpp: need %d bytes but have %d", needed, len(pixels))
	}

	img := image.NewNRGBA(image.Rect(0, 0, width, height))

	for y := 0; y < height; y++ {
		srcY := y
		if !topDown {
			srcY = height - 1 - y
		}
		rowStart := srcY * rowStride

		for x := 0; x < width; x++ {
			off := rowStart + x*3
			b := pixels[off+0]
			g := pixels[off+1]
			r := pixels[off+2]
			img.SetNRGBA(x, y, color.NRGBA{R: r, G: g, B: b, A: 255})
		}
	}

	return encodePNG(img)
}

// extractChannel extracts a color channel value from a pixel using a bitmask.
func extractChannel(pixel, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	// Find the position of the lowest set bit.
	shift := 0
	m := mask
	for m&1 == 0 {
		shift++
		m >>= 1
	}
	// Count the number of bits in the mask.
	bits := 0
	for m&1 == 1 {
		bits++
		m >>= 1
	}
	val := (pixel & mask) >> uint(shift)
	// Scale to 8-bit.
	if bits < 8 {
		val = val << uint(8-bits) | val>>uint(2*bits-8)
	} else if bits > 8 {
		val = val >> uint(bits-8)
	}
	return uint8(val)
}

// fixOpaqueAlpha sets all alpha values to 255 if every pixel has alpha == 0.
func fixOpaqueAlpha(img *image.NRGBA) {
	pix := img.Pix
	allZero := true
	for i := 3; i < len(pix); i += 4 {
		if pix[i] != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		for i := 3; i < len(pix); i += 4 {
			pix[i] = 255
		}
	}
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("PNG encode: %w", err)
	}
	return buf.Bytes(), nil
}
