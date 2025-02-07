package qrcode

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"strings"
)

// Image method converts a QR Code into a PNG image with the given scale and border.
func (q *QrCode) Image(cfg ...ImageConfig) (*image.RGBA, error) {
	var config ImageConfig
	if len(cfg) > 0 {
		config = cfg[0]
	}
	if config.Border <= 0 {
		config.Border = 2
	}
	if config.Scale <= 0 {
		config.Scale = 15
	}
	border := config.Border
	scale := config.Scale

	if border > (math.MaxInt32/2) || int64(q.GetSize())+int64(border)*2 > math.MaxInt32/int64(scale) {
		return nil, errors.New("scale or border too large")
	}

	size := q.GetSize() + border*2
	imageWidth := size * scale
	imageHeight := size * scale
	result := image.NewRGBA(image.Rect(0, 0, imageWidth, imageHeight))
	// Iterate over each pixel in the image
	for y := 0; y < imageHeight; y++ {
		for x := 0; x < imageWidth; x++ {
			moduleX := x/scale - border
			moduleY := y/scale - border
			isDark := q.GetModule(moduleX, moduleY)
			if isDark {
				result.Set(x, y, color.Black)
			} else {
				result.Set(x, y, color.White)
			}
		}
	}

	return result, nil
}

type ImageConfig struct {
	Scale      int
	Border     int
	LightColor string
	DarkColor  string
}

// The SaveAsPNG method converts a QR Code into a PNG image with the given scale and border.
func (q *QrCode) SaveAsPNG(dest string, cfg ...ImageConfig) error {
	result, err := q.Image(cfg...)
	if err != nil {
		return err
	}

	return SaveAsPNG(result, dest)
}

// The Base64PNG method converts a QR Code into a PNG image with the given scale and border.
func (q *QrCode) Base64PNG(cfg ...ImageConfig) (string, error) {
	result, err := q.Image(cfg...)
	if err != nil {
		return "", err
	}
	buf := new(bytes.Buffer)
	err = png.Encode(buf, result)
	b := buf.Bytes()
	return "data:image/png;base64," + toBase64(b), nil
}

func toBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

func (q *QrCode) SaveAsSVG(dest string, cfg ...ImageConfig) error {
	var config ImageConfig
	if len(cfg) > 0 {
		config = cfg[0]
	}
	if config.Border < 0 {
		config.Border = 4
	}
	border := config.Border
	lightColor := config.LightColor
	darkColor := config.DarkColor

	var brd = int64(border)
	sb := strings.Builder{}
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	sb.WriteString("<!DOCTYPE svg PUBLIC \"-//W3C//DTD SVG 1.1//EN\" \"http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd\">\n")
	sb.WriteString(fmt.Sprintf("<svg xmlns=\"http://www.w3.org/2000/svg\" version=\"1.1\" viewBox=\"0 0 %d %d\" stroke=\"none\">\n", int64(q.GetSize())+brd*2, int64(q.GetSize())+brd*2))
	sb.WriteString("\t<rect width=\"100%\" height=\"100%\" fill=\"" + lightColor + "\"/>\n")
	sb.WriteString("\t<path d=\"")

	for y := 0; y < q.GetSize(); y++ {
		for x := 0; x < q.GetSize(); x++ {
			if q.GetModule(x, y) {
				if x != 0 || y != 0 {
					sb.WriteString(" ")
				}
				sb.WriteString(fmt.Sprintf("M%d,%dh1v1h-1z", int64(x)+brd, int64(y)+brd))
			}
		}
	}
	sb.WriteString("\" fill=\"" + darkColor + "\"/>\n")
	sb.WriteString("</svg>\n")

	return SaveAsSvg(sb.String(), dest)
}

// SaveAsPNG is a helper function that creates a file with the given path,
// and writes the provided image into this file as a PNG.
func SaveAsPNG(img *image.RGBA, dest string) error {
	file, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer file.Close()

	err = png.Encode(file, img)
	if err != nil {
		return err
	}

	return nil
}

func SaveAsSvg(svg string, dest string) error {
	svgFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer svgFile.Close()
	_, err = svgFile.WriteString(svg)
	return err
}
