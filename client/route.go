package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/template/html/v2"
)

type DynamicRoute struct {
	Method            string
	Path              string
	Handler           fiber.Handler
	Middlewares       []fiber.Handler
	GlobalMiddlewares []fiber.Handler
	Renderer          fiber.Views
}

func (dr *DynamicRoute) Serve(c *fiber.Ctx, mws ...fiber.Handler) error {
	middlewares := append(mws, dr.Middlewares...)
	for _, mw := range middlewares {
		if err := mw(c); err != nil {
			return err
		}
	}
	return dr.Handler(c)
}

type StaticRoute struct {
	Prefix    string `json:"prefix"`
	Directory string `json:"directory"`
}

type DynamicRouter struct {
	app               *fiber.App
	routes            map[string]map[string]*DynamicRoute
	staticRoutes      []StaticRoute
	GlobalMiddlewares []fiber.Handler
	lock              sync.RWMutex
}

func NewDynamicRouter(app *fiber.App) *DynamicRouter {
	dr := &DynamicRouter{
		app:    app,
		routes: make(map[string]map[string]*DynamicRoute),
	}
	app.All("/*", dr.dispatch)
	return dr
}

func (dr *DynamicRouter) Use(mw ...fiber.Handler) {
	dr.GlobalMiddlewares = append(dr.GlobalMiddlewares, mw...)
}

func (dr *DynamicRouter) dispatch(c *fiber.Ctx) error {
	dr.lock.RLock()
	defer dr.lock.RUnlock()
	method := c.Method()
	path := c.Path()
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[path]; exists {
			return route.Serve(c, dr.GlobalMiddlewares...)
		}
	}
	for _, sr := range dr.staticRoutes {
		if strings.HasPrefix(path, sr.Prefix) {
			relativePath := strings.TrimPrefix(path, sr.Prefix)
			filePath := filepath.Join(sr.Directory, relativePath)
			if _, err := os.Stat(filePath); err == nil {
				return c.SendFile(filePath)
			}
		}
	}

	return c.Status(fiber.StatusNotFound).SendString("Dynamic route not found")
}

func (dr *DynamicRouter) AddRoute(method, path string, handler fiber.Handler, middlewares ...fiber.Handler) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if dr.routes[method] == nil {
		dr.routes[method] = make(map[string]*DynamicRoute)
	}
	dr.routes[method][path] = &DynamicRoute{
		Method:      method,
		Path:        path,
		Handler:     handler,
		Middlewares: middlewares,
	}
	log.Printf("Added dynamic route: %s %s", method, path)
}

func (dr *DynamicRouter) UpdateRoute(method, path string, newHandler fiber.Handler) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[path]; exists {
			route.Handler = newHandler
			log.Printf("Updated dynamic route handler: %s %s", method, path)
			return
		}
	}
	log.Printf("Route not found for update: %s %s", method, path)
}

func (dr *DynamicRouter) RenameRoute(method, oldPath, newPath string) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[oldPath]; exists {
			delete(methodRoutes, oldPath)
			route.Path = newPath
			methodRoutes[newPath] = route
			log.Printf("Renamed route from %s to %s for method %s", oldPath, newPath, method)
			return
		}
	}
	log.Printf("Route not found for rename: %s %s", method, oldPath)
}

func (dr *DynamicRouter) AddMiddleware(method, path string, middlewares ...fiber.Handler) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[path]; exists {
			route.Middlewares = append(route.Middlewares, middlewares...)
			log.Printf("Added middleware to route: %s %s", method, path)
			return
		}
	}
	log.Printf("Route not found for adding middleware: %s %s", method, path)
}

func (dr *DynamicRouter) RemoveMiddleware(method, path string, middlewares ...fiber.Handler) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[path]; exists {
			newChain := make([]fiber.Handler, 0, len(route.Middlewares))
			for _, existing := range route.Middlewares {
				shouldRemove := false
				for _, rm := range middlewares {
					if &existing == &rm {
						shouldRemove = true
						break
					}
				}
				if !shouldRemove {
					newChain = append(newChain, existing)
				}
			}
			route.Middlewares = newChain
			log.Printf("Removed middleware from route: %s %s", method, path)
			return
		}
	}
	log.Printf("Route not found for removing middleware: %s %s", method, path)
}

func (dr *DynamicRouter) SetRenderer(method, path string, renderer fiber.Views) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	method = strings.ToUpper(method)
	if methodRoutes, ok := dr.routes[method]; ok {
		if route, exists := methodRoutes[path]; exists {
			route.Renderer = renderer
			log.Printf("Set custom renderer for route: %s %s", method, path)
			return
		}
	}
	log.Printf("Route not found for setting renderer: %s %s", method, path)
}

func (dr *DynamicRouter) AddStaticRoute(prefix, directory string) {
	dr.lock.Lock()
	defer dr.lock.Unlock()
	dr.staticRoutes = append(dr.staticRoutes, StaticRoute{
		Prefix:    prefix,
		Directory: directory,
	})
	log.Printf("Added static route: %s -> %s", prefix, directory)
}

type RendererConfig struct {
	ID        string `json:"id"`
	Root      string `json:"root"`
	Prefix    string `json:"prefix"`
	UseIndex  bool   `json:"use_index"`
	Compress  bool   `json:"compress"`
	Index     string `json:"index"`
	Extension string `json:"extension"`
}

type APIRoute struct {
	RouteURI    string                 `json:"route_uri"`
	RouteMethod string                 `json:"route_method"`
	Description string                 `json:"description"`
	Model       string                 `json:"model"`
	Operation   string                 `json:"operation"`
	HandlerKey  string                 `json:"handler_key"`
	Request     map[string]interface{} `json:"request,omitempty"`
}

type APIEndpoints struct {
	Routes []APIRoute `json:"routes"`
}

var handlerMapping = map[string]fiber.Handler{
	"print:check": func(c *fiber.Ctx) error {
		log.Println("print:check handler invoked")
		return c.SendString("print:check executed")
	},
	"view-html": func(c *fiber.Ctx) error {
		return c.Render("index", fiber.Map{"Title": "HTML View"})
	},
	"view-json": func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"message": "JSON View"})
	},
	"view-html-content": func(c *fiber.Ctx) error {
		c.Type("html")
		return c.SendString("<html><body><h1>HTML Content View</h1></body></html>")
	},
	"command:run": func(c *fiber.Ctx) error {
		return c.SendString("Command executed")
	},
}

func main() {
	defaultEngine := html.New("./static/dist", ".html")
	app := fiber.New(fiber.Config{
		Views: defaultEngine,
	})
	app.Static("/public", "./public")
	dynamicRouter := NewDynamicRouter(app)
	rendererJSON, err := os.ReadFile("renderer.json")
	if err != nil {
		panic(err)
	}
	var rendererConfigs []RendererConfig
	if err := json.Unmarshal(rendererJSON, &rendererConfigs); err != nil {
		log.Fatalf("Error parsing renderer JSON: %v", err)
	}
	for _, rc := range rendererConfigs {
		root := filepath.Clean(rc.Root)
		err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if d.IsDir() {
				relativePath := strings.TrimPrefix(path, root)
				if relativePath != "" && !strings.HasPrefix(relativePath, "/") {
					relativePath = "/" + relativePath
				}
				if relativePath != "" {
					rootPath := filepath.Join(rc.Prefix, relativePath)
					dynamicRouter.AddStaticRoute(rootPath, path)
					dynamicRouter.AddStaticRoute(relativePath, path)
				}
			}
			return nil
		})
		if rc.UseIndex {
			customEngine := html.New(rc.Root, rc.Extension)
			route := rc.Prefix
			dynamicRouter.AddRoute("GET", route, func(c *fiber.Ctx) error {
				c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
				return customEngine.Render(c, rc.Index, fiber.Map{
					"Title": "Custom Renderer - " + rc.ID,
				})
			})
			dynamicRouter.SetRenderer("GET", route, customEngine)
		}
	}
	apiBytes, err := os.ReadFile("api.json")
	if err != nil {
		panic(err)
	}
	var apiConfig APIEndpoints
	if err := json.Unmarshal(apiBytes, &apiConfig); err != nil {
		log.Fatalf("Error parsing API routes JSON: %v", err)
	}
	for _, route := range apiConfig.Routes {
		handler, exists := handlerMapping[route.HandlerKey]
		if !exists {
			log.Printf("Handler not found for key: %s", route.HandlerKey)
			continue
		}
		dynamicRouter.AddRoute(route.RouteMethod, route.RouteURI, handler)
	}
	dynamicRouter.AddRoute("GET", "/hello", func(c *fiber.Ctx) error {
		return c.SendString("Hello from the dynamic router!")
	})
	go func() {
		time.Sleep(30 * time.Second)
		dynamicRouter.UpdateRoute("GET", "/hello", func(c *fiber.Ctx) error {
			return c.SendString("Updated handler response!")
		})
	}()
	go func() {
		time.Sleep(40 * time.Second)
		dynamicRouter.RenameRoute("GET", "/hello", "/greetings")
	}()
	log.Fatal(app.Listen(":3000"))
}
