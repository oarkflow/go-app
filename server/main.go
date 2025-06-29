package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/gofiber/fiber/v2/middleware/requestid"
	"github.com/golang-jwt/jwt/v5"
	"github.com/oarkflow/bcl"
	"github.com/oarkflow/squealx"
	"github.com/oarkflow/xid"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/dag/server/pkg/config"
	"github.com/oarkflow/dag/server/pkg/dag"
	"github.com/oarkflow/dag/server/pkg/handlers"

	"github.com/joho/godotenv"
	"github.com/oarkflow/supervisor"
)

func WithJWT(secret []byte) fiber.Handler {
	return jwtware.New(jwtware.Config{
		SigningKey: jwtware.SigningKey{Key: secret},
		SuccessHandler: func(c *fiber.Ctx) error {
			tok := c.Locals("user").(*jwt.Token)
			sub := tok.Claims.(jwt.MapClaims)["sub"].(string)
			c.Locals("ctx_userid", sub)
			auth := c.Get("Authorization")
			if strings.HasPrefix(auth, "Bearer ") {
				c.Locals("ctx_token", auth[7:])
			}
			return c.Next()
		},
	})
}

func InitDB(name, driver, dsn string) (*squealx.DB, error) {
	db, err := squealx.Open(driver, dsn, name)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return db, nil
}

func staticHandler(dir, index string, cacheMax int) fiber.Handler {
	return func(c *fiber.Ctx) error {
		file := c.Params("*")
		if file == "" {
			file = index
		}
		fp := filepath.Join(dir, file)
		if cacheMax > 0 {
			c.Set("Cache-Control", "public, max-age="+strconv.Itoa(cacheMax))
		}
		return c.SendFile(fp, false)
	}
}

func main() {
	supervisor.Run("config.json", Run)
}

func Run(ctx context.Context) error {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("error on read .env:", err)
	}
	bt, err := os.ReadFile("config.bcl")
	if err != nil {
		log.Fatal("read config:", err)
	}
	var cfg config.Config
	if _, err := bcl.Unmarshal(bt, &cfg); err != nil {
		log.Fatal("bcl parse:", err)
	}
	dbProviders := make(map[string]*squealx.DB)
	var defaultDB *squealx.DB
	for _, p := range cfg.Provider {
		dbConn, err := InitDB(p.Name, p.Driver, p.DSN)
		if err != nil {
			log.Fatalf("db init for provider %s: %v", p.Name, err)
		}
		dbProviders[p.Name] = dbConn
		if p.Default {
			defaultDB = dbConn
		}
	}
	if defaultDB == nil {
		log.Fatal("no default database provider found in config")
	}
	handlers.DefaultDB = defaultDB
	handlers.ModelsMap = make(map[string]config.Model)
	for _, m := range cfg.Models {
		handlers.ModelsMap[m.Name] = m
	}
	handlers.AuthCfg = cfg.Auth
	if cfg.Server.BodyLimit == 0 {
		cfg.Server.BodyLimit = 10000000
	}
	srvConfig := fiber.Config{
		AppName:      cfg.Server.Name,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeout) * 1000,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeout) * 1000,
		IdleTimeout:  time.Duration(cfg.Server.IdleTimeout) * 1000,
	}

	globalMW := map[string]fiber.Handler{}
	for _, m := range cfg.Middleware {
		switch m.Type {
		case "logger":
			globalMW[m.Name] = logger.New()
		case "jwt":
			globalMW[m.Name] = WithJWT([]byte(m.Secret))
		case "cors":
			globalMW[m.Name] = cors.New()
		case "ratelimit":
			globalMW[m.Name] = limiter.New()
		case "compress":
			globalMW[m.Name] = compress.New()
		case "recover":
			globalMW[m.Name] = recover.New()
		case "circuit_breaker":
			// No-op: Fiber does not have built-in circuit breaker, user can implement custom
		case "request_id":
			globalMW[m.Name] = requestid.New(requestid.Config{
				Header: m.HeaderName,
				Generator: func() string {
					// Use UUID or custom generator if needed
					return xid.New().String()
				},
			})
		default:
			log.Fatalf("unsupported middleware: %s", m.Type)
		}
	}
	app := fiber.New(srvConfig)
	if cfg.Server.HealthCheck.Enabled {
		path := cfg.Server.HealthCheck.Path
		if path == "" {
			path = "/health"
		}
		app.Get(path, func(c *fiber.Ctx) error {
			return c.Status(200).JSON(fiber.Map{"status": "ok"})
		})
	}
	// Maintenance mode middleware
	if cfg.Server.Maintenance.Enabled {
		maintenancePath := cfg.Server.Maintenance.Route
		if maintenancePath == "" {
			maintenancePath = "/maintenance"
		}
		app.Get(maintenancePath, func(c *fiber.Ctx) error {
			if cfg.Server.Maintenance.HTML != "" {
				return c.SendFile(cfg.Server.Maintenance.HTML, false)
			}
			return c.Status(503).SendString("Service is under maintenance")
		})
		app.Use(func(c *fiber.Ctx) error {
			if c.Path() != maintenancePath {
				return c.Redirect(cfg.Server.Maintenance.Route)
			}
			return c.Next()
		})
	}

	// Metrics endpoint
	if cfg.Metrics.Enabled {
		app.Get(cfg.Metrics.Path, func(c *fiber.Ctx) error {
			// TODO: Integrate with Prometheus or other metrics provider
			return c.SendString("# Metrics endpoint (stub)\n")
		})
	}

	// Tracing middleware (stub)
	if cfg.Tracing.Enabled {
		app.Use(func(c *fiber.Ctx) error {
			// TODO: Integrate with OpenTelemetry or Jaeger
			return c.Next()
		})
	}

	// Feature flags endpoint (optional, for demonstration)
	app.Get("/system/features", func(c *fiber.Ctx) error {
		return c.JSON(cfg.Features)
	})

	// Docs endpoint (Swagger/OpenAPI)
	if cfg.Docs.Enabled && cfg.Docs.Path != "" && cfg.Docs.Spec != "" {
		app.Get(cfg.Docs.Path, func(c *fiber.Ctx) error {
			return c.SendFile(cfg.Docs.Spec, false)
		})
	}

	// Register static routes before other routes
	for _, r := range cfg.Route {
		if r.Handler == "staticHandler" && r.Static.Dir != "" {
			app.Get(r.Path, staticHandler(r.Static.Dir, r.Static.Index, r.Static.CacheMax))
		}
	}
	for _, mwName := range cfg.GlobalMiddleware {
		if mw, ok := globalMW[mwName]; ok {
			app.Use(mw)
		} else {
			log.Fatalf("global middleware %s not found", mwName)
		}
	}
	dags := dag.BuildDAGs(&cfg, dbProviders, defaultDB, handlers.ModelsMap, handlers.AuthCfg)
	for _, r := range cfg.Route {
		if r.Handler == "staticHandler" && r.Static.Dir != "" {
			// Already registered above
			continue
		}
		fullPath := r.Path
		if r.Version != "" {
			fullPath = "/v" + r.Version + fullPath
		}
		if r.Group != "" {
			var grp *config.Group
			for i := range cfg.Group {
				if cfg.Group[i].Name == r.Group {
					grp = &cfg.Group[i]
					break
				}
			}
			if grp == nil {
				log.Fatalf("group %s not found for route %s", r.Group, r.Name)
			}
			fullPath = grp.Path + fullPath
			r.Middleware = append(grp.Middleware, r.Middleware...)
		}
		var h fiber.Handler
		switch r.Handler {
		case "create":
			h = handlers.Create
		case "read":
			h = handlers.Read
		case "update":
			h = handlers.Update
		case "delete":
			h = handlers.Delete
		case "list":
			h = handlers.List
		default:
			dagInstance := dags[r.Handler]
			h = func(c *fiber.Ctx) error {
				ctx := context.Background()
				if t := c.Locals("ctx_token"); t != nil {
					ctx = context.WithValue(ctx, "ctx_token", t.(string))
				}
				if u := c.Locals("ctx_userid"); u != nil {
					ctx = context.WithValue(ctx, "ctx_userid", u.(string))
				}
				task := make(map[string]any)
				if len(r.Request.Body) > 0 {
					bodyMap := map[string]any{}
					if err := c.BodyParser(&bodyMap); err != nil {
						return formatError(c, r, "invalid JSON", 400)
					}
					for _, b := range r.Request.Body {
						if val, ok := bodyMap[b]; ok {
							task[b] = val
						}
					}
				}
				for _, p := range r.Request.Params {
					task[p] = c.Params(p)
				}
				for _, qp := range r.QueryParams {
					task[qp] = c.Query(qp)
				}
				for _, h := range r.Headers {
					task[h] = c.Get(h)
				}
				for _, rule := range r.Validation {
					val, ok := task[rule.Field]
					if rule.Required && (!ok || val == "" || val == nil) {
						return formatError(c, r, "missing required field: "+rule.Field, 400)
					}
					if rule.Type != "" && ok && val != nil {
						switch rule.Type {
						case "string":
							if _, ok := val.(string); !ok {
								return formatError(c, r, "invalid type for field: "+rule.Field, 400)
							}
						case "int":
							switch val.(type) {
							case int, int64, float64:
							default:
								return formatError(c, r, "invalid type for field: "+rule.Field, 400)
							}
						}
					}
					if len(rule.Enum) > 0 && ok && val != nil {
						found := false
						for _, enumVal := range rule.Enum {
							if val == enumVal {
								found = true
								break
							}
						}
						if !found {
							return formatError(c, r, "invalid value for field: "+rule.Field, 400)
						}
					}
				}
				if r.Pagination.Enabled {
					page := c.Query(r.Pagination.PageField, "1")
					size := c.Query(r.Pagination.SizeField, "")
					task["pagination_page"] = page
					task["pagination_size"] = size
				}
				if r.Sorting.Enabled {
					sort := c.Query("sort", r.Sorting.Default)
					task["sort"] = sort
				}
				for _, hook := range r.Hooks {
					if hook.Type == "pre" && hook.Script != "" {
						// No-op: user can implement script execution here if needed
					}
				}
				out, err := dagInstance.Execute(ctx, task)
				if err != nil {
					return formatError(c, r, err.Error(), 400)
				}
				for _, hook := range r.Hooks {
					if hook.Type == "post" && hook.Script != "" {
						// No-op: user can implement script execution here if needed
					}
				}
				resp := make(fiber.Map)
				for _, f := range r.Response.Fields {
					resp[f] = out[f]
				}
				if r.ResponseFormat.Envelope != "" {
					return c.JSON(fiber.Map{r.ResponseFormat.Envelope: resp})
				}
				return c.JSON(resp)
			}
		}
		var stack []fiber.Handler
		for _, mw := range r.Middleware {
			if fn, ok := globalMW[mw]; ok {
				stack = append(stack, fn)
			}
		}
		stack = append(stack, h)
		app.Add(strings.ToUpper(r.Method), fullPath, stack...)
	}

	for _, m := range cfg.Models {
		if m.Rest {
			routePrefix := m.Prefix
			if routePrefix == "" {
				routePrefix = handlers.ToSlug(m.Name)
			}
			app.Post("/"+routePrefix, func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return handlers.Create(c)
			})
			app.Get("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return handlers.Read(c)
			})
			app.Get("/"+routePrefix, func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return handlers.List(c)
			})
			app.Put("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return handlers.Update(c)
			})
			app.Delete("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return handlers.Delete(c)
			})
		}
	}
	// OpenAPI docs endpoint (stub)
	app.Get("/docs/openapi.json", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"openapi": "3.0.0",
			"info": fiber.Map{
				"title":   cfg.Server.Name,
				"version": "1.0.0",
			},
			"paths": fiber.Map{},
		})
	})

	tlsEnabled := cfg.Server.TLS.CertFile != "" && cfg.Server.TLS.KeyFile != ""

	errCh := make(chan error, 1)
	go func() {
		log.Printf("Listening on %s...", cfg.Server.Address)
		if tlsEnabled {
			errCh <- app.ListenTLS(cfg.Server.Address, cfg.Server.TLS.CertFile, cfg.Server.TLS.KeyFile)
		} else {
			errCh <- app.Listen(cfg.Server.Address)
		}
	}()

	<-ctx.Done()
	log.Println("Draining server connections and shutting down")

	shutdownTimeout := time.Duration(cfg.Server.ShutdownTimeout) * time.Second
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownCh := make(chan struct{})
	go func() {
		_ = app.Shutdown()
		close(shutdownCh)
	}()
	select {
	case <-shutdownCh:
		log.Println("Server shutdown completed")
	case <-shutdownCtx.Done():
		log.Println("Server shutdown timed out")
	}

	return <-errCh
}

func formatError(c *fiber.Ctx, r config.Route, msg string, code int) error {
	field := "error"
	msgField := "message"
	if r.ErrorFormat.CodeField != "" {
		field = r.ErrorFormat.CodeField
	}
	if r.ErrorFormat.MessageField != "" {
		msgField = r.ErrorFormat.MessageField
	}
	return c.Status(code).JSON(fiber.Map{field: code, msgField: msg})
}
