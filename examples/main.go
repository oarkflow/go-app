package main

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"strings" // Import the strings package for NewReader
	
	"github.com/PuerkitoBio/goquery"
	"github.com/dop251/goja"
	"github.com/tdewolff/parse/css"
)

// fetchHTML downloads the HTML content of the URL.
func fetchHTML(url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	
	bytes, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(bytes), nil
}

// parseHTML builds a DOM-like structure using goquery.
func parseHTML(htmlContent string) (*goquery.Document, error) {
	// Using strings.NewReader instead of a custom stringReader.
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(htmlContent))
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// Dummy function to parse CSS.
func parseCSS(cssContent string) {
	lexer := css.NewLexer(strings.NewReader(cssContent))
	for {
		tt, text := lexer.Next()
		if tt == css.ErrorToken {
			break
		}
		// For demonstration: simply print out tokens.
		fmt.Printf("Token: %v, Text: %s\n", tt, text)
	}
}

// Dummy function to execute JavaScript using goja.
func executeJavaScript(jsCode string) {
	vm := goja.New()
	_, err := vm.RunString(jsCode)
	if err != nil {
		log.Println("JS Error:", err)
	} else {
		fmt.Println("JavaScript executed successfully.")
	}
}

// A very basic renderer that “draws” a rectangle and some text to an image.
func renderToImage(output string) error {
	// Create a blank RGBA image
	img := image.NewRGBA(image.Rect(0, 0, 800, 600))
	// Fill background with white color
	draw.Draw(img, img.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	
	// For demonstration, draw a simple black rectangle somewhere on the image.
	for x := 100; x < 300; x++ {
		for y := 100; y < 200; y++ {
			img.Set(x, y, color.Black)
		}
	}
	
	// Save the image to a file
	outFile, err := os.Create(output)
	if err != nil {
		return err
	}
	defer outFile.Close()
	
	return png.Encode(outFile, img)
}

func main() {
	// URL to render
	url := "https://oarkflow.com"
	
	// 1. Fetch HTML content
	htmlContent, err := fetchHTML(url)
	if err != nil {
		log.Fatalf("Failed to fetch HTML: %v", err)
	}
	
	// 2. Parse HTML to build DOM tree
	doc, err := parseHTML(htmlContent)
	if err != nil {
		log.Fatalf("Failed to parse HTML: %v", err)
	}
	// (For demonstration, print the page title)
	title := doc.Find("title").First().Text()
	fmt.Println("Page Title:", title)
	
	// 3. Find and parse CSS (This is a placeholder.)
	cssContent := "body { background-color: #fff; }"
	parseCSS(cssContent)
	
	// 4. Execute JavaScript (Again, a placeholder.)
	jsCode := "console.log('Hello from JavaScript');"
	executeJavaScript(jsCode)
	
	// 5. Render to an image
	err = renderToImage("output.png")
	if err != nil {
		log.Fatalf("Failed to render image: %v", err)
	}
	
	fmt.Println("Rendered image saved as output.png")
}
