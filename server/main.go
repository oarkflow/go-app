// main.go
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"os"
	"strings"
	"time"

	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/oarkflow/bcl"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

/* --- CONFIG STRUCTS --- */

type Config struct {
	Server     ServerConfig       `bcl:"server"`
	Middleware []MiddlewareConfig `bcl:"middleware"`
	Route      []RouteConfig      `bcl:"route"`
	DAG        []DAGConfig        `bcl:"dag"`
}

type ServerConfig struct {
	Address string `bcl:"address"`
}

type MiddlewareConfig struct {
	Name   string `bcl:"name"`
	Type   string `bcl:"type"`
	Secret string `bcl:"secret,optional"`
	Global bool   `bcl:"global,optional"`
}

type RouteConfig struct {
	Name       string   `bcl:"name"`
	Method     string   `bcl:"method"`
	Path       string   `bcl:"path"`
	Middleware []string `bcl:"middleware,optional"`
	Handler    string   `bcl:"handler"`
	Request    struct {
		Body   []string `bcl:"body,optional"`
		Params []string `bcl:"params,optional"`
	} `bcl:"request"`
	Response struct {
		Fields []string `bcl:"fields"`
	} `bcl:"response"`
}

type DAGConfig struct {
	Name string          `bcl:"name"`
	Node []NodeConfigRaw `bcl:"node"`
	Edge []EdgeRaw       `bcl:"edge"`
}

type NodeConfigRaw struct {
	Name   string   `bcl:"name"`
	Type   string   `bcl:"type"`
	Query  string   `bcl:"query,optional"`
	Input  []string `bcl:"input,optional"`
	Output []string `bcl:"output"`
}

type EdgeRaw struct {
	From string `bcl:"from"`
	To   string `bcl:"to"`
}

/* --- DB INIT --- */

func InitDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}
	schema := `
    PRAGMA foreign_keys=ON;
    CREATE TABLE IF NOT EXISTS users(id TEXT PRIMARY KEY, name TEXT, email TEXT UNIQUE, password TEXT);
    CREATE TABLE IF NOT EXISTS tokens(token TEXT PRIMARY KEY, userid TEXT, FOREIGN KEY(userid) REFERENCES users(id));
    INSERT OR IGNORE INTO users(id,name,email,password) VALUES (
      '1','Alice','alice@example.com','` + hashPassword("alicepass") + `'
    );
    `
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	return db, nil
}

func hashPassword(pw string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b)
}

/* --- DAG ENGINE --- */

type Task = map[string]interface{}
type Result = map[string]interface{}

type NodeProcessor interface {
	Process(ctx context.Context, input Task) (Result, error)
}

type NodeConfig struct {
	Name      string
	Input     []string
	Output    []string
	Processor NodeProcessor
}

type Edge struct{ From, To string }

type DAG struct {
	Nodes map[string]*NodeConfig
	Edges []Edge
}

func NewDAG() *DAG {
	return &DAG{Nodes: make(map[string]*NodeConfig)}
}

func (d *DAG) AddNode(cfg *NodeConfig) {
	d.Nodes[cfg.Name] = cfg
}

func (d *DAG) AddEdge(e Edge) {
	d.Edges = append(d.Edges, e)
}

func (d *DAG) Execute(ctx context.Context, input Task) (Task, error) {
	data := make(Task)
	for k, v := range input {
		data[k] = v
	}
	executed := make(map[string]bool)

	for len(executed) < len(d.Nodes) {
		progress := false
		for name, node := range d.Nodes {
			if executed[name] {
				continue
			}
			// Ensure required input from context if missing from task
			for _, inKey := range node.Input {
				if _, ok := data[inKey]; !ok {
					if v := ctx.Value(inKey); v != nil {
						data[inKey] = v
					}
				}
			}
			ready := true
			for _, inKey := range node.Input {
				if _, ok := data[inKey]; !ok {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			res, err := node.Processor.Process(ctx, data)
			if err != nil {
				return nil, err
			}
			for _, key := range node.Output {
				if v, ok := res[key]; ok {
					data[key] = v
				}
			}
			executed[name] = true
			progress = true
		}
		if !progress {
			return nil, errors.New("cycle or unmet dependencies in DAG")
		}
	}
	return data, nil
}

/* --- NODE TYPES --- */

type DBQueryNode struct {
	Query  string
	DB     *sql.DB
	OutKey string
}

func (n *DBQueryNode) Process(_ context.Context, in Task) (Result, error) {
	var params []interface{}
	for _, k := range nQueryParams(in, n.Query) {
		params = append(params, in[k])
	}
	row := n.DB.QueryRow(n.Query, params...)
	var id, name, email string
	if err := row.Scan(&id, &name, &email); err != nil {
		return nil, err
	}
	return Result{n.OutKey: map[string]string{"id": id, "name": name, "email": email}}, nil
}

func nQueryParams(in Task, query string) []string {
	// naive counting of "?" to match param order fallback
	count := strings.Count(query, "?")
	var keys []string
	for k := range in {
		keys = append(keys, k)
		if len(keys) == count {
			break
		}
	}
	return keys
}

type AuthVerifyNode struct {
	DB *sql.DB
}

func (n *AuthVerifyNode) Process(_ context.Context, in Task) (Result, error) {
	email := in["email"].(string)
	pass := in["password"].(string)
	var id, hash string
	if err := n.DB.QueryRow("SELECT id,password FROM users WHERE email=?", email).Scan(&id, &hash); err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(pass)) != nil {
		return nil, errors.New("invalid credentials")
	}
	return Result{"user": id}, nil
}

type AuthTokenNode struct {
	DB         *sql.DB
	JWTSecret  []byte
	TTLSeconds int64
}

func (n *AuthTokenNode) Process(_ context.Context, in Task) (Result, error) {
	uid := in["user"].(string)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   uid,
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(n.TTLSeconds) * time.Second)),
	})
	signed, err := token.SignedString(n.JWTSecret)
	if err != nil {
		return nil, err
	}
	if _, err := n.DB.Exec("INSERT INTO tokens(token,userid) VALUES(?,?)", signed, uid); err != nil {
		return nil, err
	}
	return Result{"token": signed}, nil
}

type AuthRevokeNode struct {
	DB *sql.DB
}

func (n *AuthRevokeNode) Process(ctx context.Context, _ Task) (Result, error) {
	token := ctx.Value("ctx_token").(string)
	if _, err := n.DB.Exec("DELETE FROM tokens WHERE token=?", token); err != nil {
		return nil, err
	}
	return Result{"success": true}, nil
}

/* --- MIDDLEWARE HELPERS --- */

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

/* --- BUILD DAGs FROM CONFIG --- */

func buildDAGs(cfg *Config, db *sql.DB) map[string]*DAG {
	dags := make(map[string]*DAG)
	secret := findSecret(cfg)
	for _, dc := range cfg.DAG {
		dag := NewDAG()
		for _, nd := range dc.Node {
			var proc NodeProcessor
			switch nd.Type {
			case "db_query":
				proc = &DBQueryNode{Query: nd.Query, DB: db, OutKey: nd.Output[0]}
			case "auth_verify":
				proc = &AuthVerifyNode{DB: db}
			case "auth_token":
				proc = &AuthTokenNode{DB: db, JWTSecret: []byte(secret), TTLSeconds: 3600}
			case "auth_revoke":
				proc = &AuthRevokeNode{DB: db}
			default:
				log.Fatalf("unsupported node type %s", nd.Type)
			}
			dag.AddNode(&NodeConfig{
				Name:      nd.Name,
				Input:     nd.Input,
				Output:    nd.Output,
				Processor: proc,
			})
		}
		for _, e := range dc.Edge {
			dag.AddEdge(Edge{From: e.From, To: e.To})
		}
		dags[dc.Name] = dag
	}
	return dags
}

func findSecret(cfg *Config) string {
	for _, m := range cfg.Middleware {
		if m.Type == "jwt" {
			return m.Secret
		}
	}
	log.Fatal("no jwt secret found in config")
	return ""
}

/* --- MAIN --- */

func main() {
	bt, err := os.ReadFile("config.bcl")
	if err != nil {
		log.Fatal("read config:", err)
	}
	var cfg Config
	if _, err := bcl.Unmarshal(bt, &cfg); err != nil {
		log.Fatal("bcl parse:", err)
	}

	db, err := InitDB("file:app.db?cache=shared&mode=rwc")
	if err != nil {
		log.Fatal("db init:", err)
	}

	app := fiber.New()
	globalMW := map[string]fiber.Handler{}
	for _, m := range cfg.Middleware {
		switch m.Type {
		case "logger":
			if m.Global {
				app.Use(logger.New())
			} else {
				globalMW[m.Name] = logger.New()
			}
		case "jwt":
			if m.Global {
				app.Use(WithJWT([]byte(m.Secret)))
			} else {
				globalMW[m.Name] = WithJWT([]byte(m.Secret))
			}
		default:
			log.Fatalf("unsupported middleware: %s", m.Type)
		}
	}

	dags := buildDAGs(&cfg, db)
	for _, r := range cfg.Route {
		dag := dags[r.Handler]
		h := func(c *fiber.Ctx) error {
			ctx := context.Background()
			if t := c.Locals("ctx_token"); t != nil {
				ctx = context.WithValue(ctx, "ctx_token", t.(string))
			}
			if u := c.Locals("ctx_userid"); u != nil {
				ctx = context.WithValue(ctx, "ctx_userid", u.(string))
			}

			task := Task{}
			if len(r.Request.Body) > 0 {
				bodyMap := map[string]interface{}{}
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

			out, err := dag.Execute(ctx, task)
			if err != nil {
				return c.Status(400).JSON(fiber.Map{"error": err.Error()})
			}
			resp := fiber.Map{}
			for _, f := range r.Response.Fields {
				resp[f] = out[f]
			}
			return c.JSON(resp)
		}

		var stack []fiber.Handler
		for _, mw := range r.Middleware {
			if fn, ok := globalMW[mw]; ok {
				stack = append(stack, fn)
			}
		}
		stack = append(stack, h)
		app.Add(strings.ToUpper(r.Method), r.Path, stack...)
	}

	log.Printf("Listening on %s...", cfg.Server.Address)
	log.Fatal(app.Listen(cfg.Server.Address))
}
