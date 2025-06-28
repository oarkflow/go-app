package main

import (
	"context"
	"log"
	"os"
	"strings"

	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/compress"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/oarkflow/bcl"
	"github.com/oarkflow/squealx"
	_ "modernc.org/sqlite"

	"github.com/oarkflow/dag/server/handlers"
	"github.com/oarkflow/dag/server/pkg/config"
	"github.com/oarkflow/dag/server/pkg/dag"
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

func main() {
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
	app := fiber.New(fiber.Config{EnablePrintRoutes: true})
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
		default:
			log.Fatalf("unsupported middleware: %s", m.Type)
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
		fullPath := r.Path
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
			fullPath = grp.Path + r.Path
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
						return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
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
				out, err := dagInstance.Execute(ctx, task)
				if err != nil {
					return c.Status(400).JSON(fiber.Map{"error": err.Error(), "action": "dag execution", "dag": r.Handler})
				}
				resp := make(fiber.Map)
				for _, f := range r.Response.Fields {
					resp[f] = out[f]
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
	log.Printf("Listening on %s...", cfg.Server.Address)
	log.Fatal(app.Listen(cfg.Server.Address))
}
