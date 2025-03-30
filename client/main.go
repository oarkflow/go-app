// main.go
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
)

// KeyValuePair represents a key-value pair.
type KeyValuePair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// APIRequest represents the client payload.
// It supports headers as a JSON object and/or key-value pairs,
// and the request body can be provided as raw text, key-value pairs, or a file.
type APIRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Headers provided as JSON.
	Headers map[string]string `json:"headers"`
	// Alternatively, headers may be provided as key-value pairs.
	HeadersKVP []KeyValuePair `json:"headersKVP"`

	// BodyInputType: "raw", "keyvalue", or "file"
	BodyInputType string `json:"bodyInputType"`
	// When using raw mode, the client chooses a content type.
	BodyContentType string `json:"bodyContentType"`
	// Raw body text.
	RawBody string `json:"rawBody"`
	// When using keyvalue mode, these pairs will be sent as application/x-www-form-urlencoded.
	BodyKVP []KeyValuePair `json:"bodyKVP"`

	// File uploads: if BodyInputType is "file", the file is provided as multipart.
	// (The file is read from the multipart form and its content stored in RawBody.)
	BodyFile bool `json:"bodyFile"`
}

// APIResponse represents the proxied response.
type APIResponse struct {
	Status     string              `json:"status"`
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	Error      string              `json:"error,omitempty"`
	EnvVars    map[string]string   `json:"envVars,omitempty"`
}

// Workspace holds environment variables and cookies.
type Workspace struct {
	Name    string
	EnvVars map[string]string
	Cookies map[string]*http.Cookie
	mu      sync.RWMutex
}

var (
	// In-memory store for workspaces.
	workspaces = map[string]*Workspace{}
	// Protect workspaces map.
	wsMu sync.RWMutex
)

func getOrCreateWorkspace(name string) *Workspace {
	wsMu.Lock()
	defer wsMu.Unlock()
	if ws, ok := workspaces[name]; ok {
		return ws
	}
	ws := &Workspace{
		Name:    name,
		EnvVars: make(map[string]string),
		Cookies: make(map[string]*http.Cookie),
	}
	workspaces[name] = ws
	return ws
}

// replaceEnvVars replaces placeholders in the form {{var}} with the matching values from env.
func replaceEnvVars(input string, env map[string]string) string {
	re := regexp.MustCompile(`{{\s*(\w+)\s*}}`)
	return re.ReplaceAllStringFunc(input, func(match string) string {
		groups := re.FindStringSubmatch(match)
		if len(groups) < 2 {
			return match
		}
		if val, ok := env[groups[1]]; ok {
			return val
		}
		return match
	})
}

// mergeHeaders merges headers from JSON and key-value pairs.
func mergeHeaders(jsonHeaders map[string]string, kvp []KeyValuePair) map[string]string {
	merged := make(map[string]string)
	for k, v := range jsonHeaders {
		merged[k] = v
	}
	for _, pair := range kvp {
		if pair.Key != "" {
			merged[pair.Key] = pair.Value
		}
	}
	return merged
}

func sendRequestHandler(c *fiber.Ctx) error {
	// Get workspace from query parameter; default is "default"
	wsName := c.Query("workspace", "default")
	workspace := getOrCreateWorkspace(wsName)

	contentType := c.Get("Content-Type")
	var reqPayload APIRequest
	var err error

	if strings.HasPrefix(contentType, "multipart/form-data") {
		reqPayload.Method = c.FormValue("method")
		reqPayload.URL = c.FormValue("url")
		reqPayload.BodyInputType = c.FormValue("bodyInputType")
		reqPayload.BodyContentType = c.FormValue("bodyContentType")
		reqPayload.RawBody = c.FormValue("rawBody")
		headersStr := c.FormValue("headers")
		if headersStr != "" {
			if err := json.Unmarshal([]byte(headersStr), &reqPayload.Headers); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON for headers"})
			}
		}
		headersKVPStr := c.FormValue("headersKVP")
		if headersKVPStr != "" {
			if err := json.Unmarshal([]byte(headersKVPStr), &reqPayload.HeadersKVP); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON for headersKVP"})
			}
		}
		bodyKVPStr := c.FormValue("bodyKVP")
		if bodyKVPStr != "" {
			if err := json.Unmarshal([]byte(bodyKVPStr), &reqPayload.BodyKVP); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON for bodyKVP"})
			}
		}
		if reqPayload.BodyInputType == "file" {
			fileHeader, err := c.FormFile("file")
			if err == nil {
				f, err := fileHeader.Open()
				if err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error opening file: " + err.Error()})
				}
				defer f.Close()
				buf := new(bytes.Buffer)
				if _, err := io.Copy(buf, f); err != nil {
					return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error reading file: " + err.Error()})
				}
				reqPayload.RawBody = buf.String()
			}
		}
	} else {
		// For non-multipart, expect JSON.
		if err := c.BodyParser(&reqPayload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Error decoding JSON: " + err.Error()})
		}
	}

	// Get a snapshot of environment variables.
	workspace.mu.RLock()
	envCopy := make(map[string]string)
	for k, v := range workspace.EnvVars {
		envCopy[k] = v
	}
	workspace.mu.RUnlock()

	// Substitute environment variables in URL and raw body.
	reqPayload.URL = replaceEnvVars(reqPayload.URL, envCopy)
	reqPayload.RawBody = replaceEnvVars(reqPayload.RawBody, envCopy)
	// Merge headers and substitute env vars.
	headers := mergeHeaders(reqPayload.Headers, reqPayload.HeadersKVP)
	for k, v := range headers {
		headers[k] = replaceEnvVars(v, envCopy)
	}

	// Build outbound request.
	var outboundReq *http.Request
	if reqPayload.BodyInputType == "keyvalue" {
		// Encode body as URL-encoded form.
		formData := url.Values{}
		for _, pair := range reqPayload.BodyKVP {
			if pair.Key != "" {
				formData.Set(pair.Key, replaceEnvVars(pair.Value, envCopy))
			}
		}
		outboundReq, err = http.NewRequest(reqPayload.Method, reqPayload.URL, strings.NewReader(formData.Encode()))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error creating outbound request: " + err.Error()})
		}
		// If Content-Type is not provided via headers, set it.
		if _, ok := headers["Content-Type"]; !ok {
			outboundReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	} else if reqPayload.BodyInputType == "raw" {
		// Use raw body text.
		var bodyReader io.Reader
		if reqPayload.RawBody != "" {
			bodyReader = strings.NewReader(reqPayload.RawBody)
		}
		outboundReq, err = http.NewRequest(reqPayload.Method, reqPayload.URL, bodyReader)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error creating outbound request: " + err.Error()})
		}
		// Set content type if not set in headers.
		if _, ok := headers["Content-Type"]; !ok && reqPayload.BodyContentType != "" {
			outboundReq.Header.Set("Content-Type", reqPayload.BodyContentType)
		}
	} else if reqPayload.BodyInputType == "file" {
		// Build multipart form for file upload.
		var b bytes.Buffer
		writer := multipart.NewWriter(&b)
		part, err := writer.CreateFormFile("file", "uploaded_file")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error creating form file: " + err.Error()})
		}
		_, err = part.Write([]byte(reqPayload.RawBody))
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error writing file content: " + err.Error()})
		}
		writer.Close()
		outboundReq, err = http.NewRequest(reqPayload.Method, reqPayload.URL, &b)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error creating outbound request: " + err.Error()})
		}
		outboundReq.Header.Set("Content-Type", writer.FormDataContentType())
	} else {
		// No body.
		outboundReq, err = http.NewRequest(reqPayload.Method, reqPayload.URL, nil)
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error creating outbound request: " + err.Error()})
		}
	}

	// Set headers on outbound request.
	for key, value := range headers {
		outboundReq.Header.Set(key, value)
	}

	// Attach cookies from workspace.
	workspace.mu.RLock()
	for _, cookie := range workspace.Cookies {
		outboundReq.AddCookie(cookie)
	}
	workspace.mu.RUnlock()

	// Execute outbound request.
	resp, err := http.DefaultClient.Do(outboundReq)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error executing request: " + err.Error()})
	}
	defer resp.Body.Close()

	// Update workspace cookies with response cookies.
	for _, cookie := range resp.Cookies() {
		workspace.mu.Lock()
		workspace.Cookies[cookie.Name] = cookie
		workspace.mu.Unlock()
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"error": "Error reading response: " + err.Error()})
	}

	responsePayload := APIResponse{
		Status:     resp.Status,
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       string(respBody),
	}

	workspace.mu.RLock()
	responsePayload.EnvVars = workspace.EnvVars
	workspace.mu.RUnlock()

	return c.JSON(responsePayload)
}

func workspacesHandler(c *fiber.Ctx) error {
	wsMu.RLock()
	defer wsMu.RUnlock()

	switch c.Method() {
	case fiber.MethodGet:
		names := []string{}
		for name := range workspaces {
			names = append(names, name)
		}
		return c.JSON(names)
	case fiber.MethodPost:
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.BodyParser(&payload); err != nil || payload.Name == "" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid workspace name"})
		}
		getOrCreateWorkspace(payload.Name)
		return c.SendStatus(fiber.StatusCreated)
	default:
		return c.SendStatus(fiber.StatusMethodNotAllowed)
	}
}

func envHandler(c *fiber.Ctx) error {
	wsName := c.Query("workspace", "default")
	workspace := getOrCreateWorkspace(wsName)
	switch c.Method() {
	case fiber.MethodGet:
		workspace.mu.RLock()
		defer workspace.mu.RUnlock()
		return c.JSON(workspace.EnvVars)
	case fiber.MethodPost:
		var payload map[string]string
		if err := c.BodyParser(&payload); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "Invalid JSON"})
		}
		workspace.mu.Lock()
		for k, v := range payload {
			workspace.EnvVars[k] = v
		}
		workspace.mu.Unlock()
		return c.SendStatus(fiber.StatusOK)
	default:
		return c.SendStatus(fiber.StatusMethodNotAllowed)
	}
}

func main() {
	// Create default workspace.
	getOrCreateWorkspace("default")

	app := fiber.New()
	app.Use(logger.New())

	// Serve static files (frontend assets)
	app.Static("/static", "./static")
	app.Static("/", "./templates") // Serves index.html at "/"

	// Endpoints.
	app.Post("/sendRequest", sendRequestHandler)
	app.All("/workspaces", workspacesHandler)
	app.All("/env", envHandler)

	log.Println("Server started on http://localhost:8080")
	if err := app.Listen(":8080"); err != nil {
		log.Fatalf("Error starting server: %v", err)
	}
}
