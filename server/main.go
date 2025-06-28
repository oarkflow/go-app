// main.go
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	jwtware "github.com/gofiber/contrib/jwt"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/golang-jwt/jwt/v5"
	"github.com/oarkflow/bcl"
	"github.com/oarkflow/squealx"
	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

var (
	gDefaultDB *squealx.DB
	modelsMap  map[string]ModelConfig // Global map of models for CRUD operations
)

/* --- CONFIG STRUCTS --- */

type ProviderConfig struct {
	Name    string `bcl:"name"`
	Driver  string `bcl:"driver"`
	Type    string `bcl:"type"`
	DSN     string `bcl:"dsn"`
	Default bool   `bcl:"default"`
}

type ServerConfig struct {
	Address      string `bcl:"address"`
	ReadTimeout  int    `bcl:"read_timeout"`
	WriteTimeout int    `bcl:"write_timeout"`
}

type MiddlewareConfig struct {
	Name   string `bcl:"name"`
	Type   string `bcl:"type"`
	Secret string `bcl:"secret"`
}

type GroupConfig struct {
	Name       string   `bcl:"name"`
	Path       string   `bcl:"path"`
	Middleware []string `bcl:"middleware"`
}

type RouteConfig struct {
	Name       string   `bcl:"name"`
	Group      string   `bcl:"group"`
	Method     string   `bcl:"method"`
	Path       string   `bcl:"path"`
	Middleware []string `bcl:"middleware"`
	Handler    string   `bcl:"handler"`
	Request    struct {
		Body   []string `bcl:"body"`
		Params []string `bcl:"params"`
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

// Updated NodeConfigRaw with mapping fields.
type NodeConfigRaw struct {
	Name               string            `bcl:"name"`
	Type               string            `bcl:"type"`
	Query              string            `bcl:"query"`
	Input              []string          `bcl:"input"`
	Output             []string          `bcl:"output"`
	Provider           string            `bcl:"provider"`
	FieldMapping       map[string]string `bcl:"field_mapping"`
	InsertFieldMapping map[string]string `bcl:"insert_field_mapping"`
	UpdateFieldMapping map[string]string `bcl:"update_field_mapping"`
	QueryFieldMapping  map[string]string `bcl:"query_field_mapping"`
}

type EdgeRaw struct {
	From string `bcl:"from"`
	To   string `bcl:"to"`
}

// NEW: Model types for defining table/source in provider.
type ModelField struct {
	Name       string `bcl:"name"`
	DataType   string `bcl:"data_type"`
	DefaultVal string `bcl:"default_val"`
	SoftDelete bool   `bcl:"soft_delete"`
}

type ModelConfig struct {
	Name   string       `bcl:"name"`
	Table  string       `bcl:"table"`
	Rest   bool         `bcl:"rest"`
	Prefix string       `bcl:"prefix,optional"` // new optional prefix for route
	Fields []ModelField `bcl:"fields"`
}

type Config struct {
	Server           ServerConfig       `bcl:"server"`
	Provider         []ProviderConfig   `bcl:"provider"`
	Middleware       []MiddlewareConfig `bcl:"middleware"`
	GlobalMiddleware []string           `bcl:"global_middleware"`
	Group            []GroupConfig      `bcl:"group"`
	Route            []RouteConfig      `bcl:"route"`
	DAG              []DAGConfig        `bcl:"dag"`
	Models           []ModelConfig      `bcl:"model"`
}

/* --- DB INIT --- */

// Refactored InitDB to accept a driver parameter from config.bcl.
func InitDB(name, driver, dsn string) (*squealx.DB, error) {
	db, err := squealx.Open(driver, dsn, name)
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
	Query              string
	DB                 *squealx.DB
	OutKey             string
	FieldMapping       map[string]string
	InsertFieldMapping map[string]string
	UpdateFieldMapping map[string]string
	QueryFieldMapping  map[string]string
}

func (n *DBQueryNode) Process(_ context.Context, in Task) (Result, error) {
	var params []interface{}
	for _, k := range nQueryParams(in, n.Query) {
		params = append(params, in[k])
	}
	// use sqlx Get to retrieve into a struct
	var user struct {
		ID    string `db:"id"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}
	if err := n.DB.Get(&user, n.Query, params...); err != nil {
		return nil, err
	}
	// initial result from DB
	result := Result{n.OutKey: map[string]string{"id": user.ID, "name": user.Name, "email": user.Email}}
	// Apply query field mapping if provided.
	if n.QueryFieldMapping != nil && len(n.QueryFieldMapping) > 0 {
		orig := result[n.OutKey].(map[string]string)
		mapped := make(map[string]string)
		for k, v := range orig {
			if newKey, ok := n.QueryFieldMapping[k]; ok {
				mapped[newKey] = v
			} else {
				mapped[k] = v
			}
		}
		result[n.OutKey] = mapped
	}
	return result, nil
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
	DB *squealx.DB
}

func (n *AuthVerifyNode) Process(_ context.Context, in Task) (Result, error) {
	email := in["email"].(string)
	pass := in["password"].(string)
	var user struct {
		ID       string `db:"id"`
		Password string `db:"password"`
	}
	if err := n.DB.Get(&user, "SELECT id,password FROM users WHERE email=?", email); err != nil {
		return nil, err
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(pass)) != nil {
		return nil, errors.New("invalid credentials")
	}
	return Result{"user": user.ID}, nil
}

type AuthTokenNode struct {
	DB         *squealx.DB
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
	DB *squealx.DB
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

func buildDAGs(cfg *Config, dbProviders map[string]*squealx.DB, defaultDB *squealx.DB) map[string]*DAG {
	dags := make(map[string]*DAG)
	secret := findSecret(cfg)
	for _, dc := range cfg.DAG {
		dag := NewDAG()
		for _, nd := range dc.Node {
			var dbConn *squealx.DB
			if nd.Provider != "" {
				var ok bool
				dbConn, ok = dbProviders[nd.Provider]
				if !ok {
					log.Fatalf("provider %s not found for node %s", nd.Provider, nd.Name)
				}
			} else {
				dbConn = defaultDB
			}
			var proc NodeProcessor
			switch nd.Type {
			case "db_query":
				proc = &DBQueryNode{
					Query:              nd.Query,
					DB:                 dbConn,
					OutKey:             nd.Output[0],
					FieldMapping:       nd.FieldMapping,
					InsertFieldMapping: nd.InsertFieldMapping,
					UpdateFieldMapping: nd.UpdateFieldMapping,
					QueryFieldMapping:  nd.QueryFieldMapping,
				}
			case "auth_verify":
				proc = &AuthVerifyNode{DB: dbConn}
			case "auth_token":
				proc = &AuthTokenNode{DB: dbConn, JWTSecret: []byte(secret), TTLSeconds: 3600}
			case "auth_revoke":
				proc = &AuthRevokeNode{DB: dbConn}
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

/* --- UPDATED: CRUD HANDLERS --- */

func createModelHandler(c *fiber.Ctx) error {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	m, ok := modelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	var payload map[string]interface{}
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
	}
	table := m.Table
	if table == "" {
		table = modelName
	}
	cols := []string{}
	placeholders := []string{}
	values := []interface{}{}
	for _, field := range m.Fields {
		if v, exists := payload[field.Name]; exists {
			cols = append(cols, field.Name)
			placeholders = append(placeholders, "?")
			values = append(values, v)
		}
	}
	if len(cols) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "no valid fields provided"})
	}
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(cols, ", "), strings.Join(placeholders, ", "))
	res, err := gDefaultDB.Exec(query, values...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	id, _ := res.LastInsertId()
	return c.JSON(fiber.Map{"id": id})
}

func readModelHandler(c *fiber.Ctx) error {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	id := c.Params("id")
	m, ok := modelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = modelName
	}
	query := fmt.Sprintf("SELECT * FROM %s WHERE id = ?", table)
	row := gDefaultDB.QueryRow(query, id)
	result := make(map[string]interface{})
	cols := []string{}
	for _, f := range m.Fields {
		cols = append(cols, f.Name)
		result[f.Name] = new(interface{})
	}
	args := []interface{}{}
	for _, f := range m.Fields {
		args = append(args, result[f.Name])
	}
	if err := row.Scan(args...); err != nil {
		if err == sql.ErrNoRows {
			return c.Status(404).JSON(fiber.Map{"error": "record not found"})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	for k, v := range result {
		result[k] = *(v.(*interface{}))
	}
	return c.JSON(fiber.Map{"record": result})
}

func updateModelHandler(c *fiber.Ctx) error {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	id := c.Params("id")
	m, ok := modelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = modelName
	}
	var payload map[string]interface{}
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid JSON"})
	}
	sets := []string{}
	values := []interface{}{}
	for _, field := range m.Fields {
		if v, exists := payload[field.Name]; exists {
			sets = append(sets, fmt.Sprintf("%s = ?", field.Name))
			values = append(values, v)
		}
	}
	if len(sets) == 0 {
		return c.Status(400).JSON(fiber.Map{"error": "no valid fields to update"})
	}
	values = append(values, id)
	query := fmt.Sprintf("UPDATE %s SET %s WHERE id = ?", table, strings.Join(sets, ", "))
	_, err := gDefaultDB.Exec(query, values...)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

func deleteModelHandler(c *fiber.Ctx) error {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	id := c.Params("id")
	m, ok := modelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = modelName
	}
	softDelete := false
	var softField string
	for _, field := range m.Fields {
		if field.SoftDelete {
			softDelete = true
			softField = field.Name
			break
		}
	}
	var err error
	if softDelete {
		query := fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", table, softField)
		_, err = gDefaultDB.Exec(query, true, id)
	} else {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = ?", table)
		_, err = gDefaultDB.Exec(query, id)
	}
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(fiber.Map{"success": true})
}

// NEW: Handler to list all rows for a model.
func listModelHandler(c *fiber.Ctx) error {
	modelName := c.Params("model")
	if modelName == "" {
		if val := c.Locals("model"); val != nil {
			modelName = val.(string)
		}
	}
	m, ok := modelsMap[modelName]
	if !ok {
		return c.Status(404).JSON(fiber.Map{"error": "model not found"})
	}
	table := m.Table
	if table == "" {
		table = modelName
	}
	// Build query to fetch all fields.
	query := fmt.Sprintf("SELECT * FROM %s", table)
	rows, err := gDefaultDB.Query(query)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	defer rows.Close()

	// Create a slice of maps.
	var results []map[string]interface{}
	cols, err := rows.Columns()
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}
		if err := rows.Scan(columnPointers...); err != nil {
			return c.Status(500).JSON(fiber.Map{"error": err.Error()})
		}
		record := make(map[string]interface{})
		for i, colName := range cols {
			val := columnPointers[i].(*interface{})
			record[colName] = *val
		}
		results = append(results, record)
	}
	return c.JSON(fiber.Map{"records": results})
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

	// Initialize DB connections for providers using driver from config.
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
	// Set global default DB and build models map.
	gDefaultDB = defaultDB
	modelsMap = make(map[string]ModelConfig)
	for _, m := range cfg.Models {
		modelsMap[m.Name] = m
	}

	app := fiber.New(fiber.Config{EnablePrintRoutes: true})

	// Build mapping for all middleware.
	globalMW := map[string]fiber.Handler{}
	for _, m := range cfg.Middleware {
		switch m.Type {
		case "logger":
			globalMW[m.Name] = logger.New()
		case "jwt":
			globalMW[m.Name] = WithJWT([]byte(m.Secret))
		default:
			log.Fatalf("unsupported middleware: %s", m.Type)
		}
	}

	// Register global middleware as specified in GlobalMiddleware.
	for _, mwName := range cfg.GlobalMiddleware {
		if mw, ok := globalMW[mwName]; ok {
			app.Use(mw)
		} else {
			log.Fatalf("global middleware %s not found", mwName)
		}
	}

	dags := buildDAGs(&cfg, dbProviders, defaultDB)

	// Modified route registration to support CRUD operations.
	for _, r := range cfg.Route {
		fullPath := r.Path
		if r.Group != "" {
			var grp *GroupConfig
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
		// Use CRUD handlers if the route handler indicates a CRUD operation.
		switch r.Handler {
		case "crud_create":
			h = createModelHandler
		case "crud_read":
			h = readModelHandler
		case "crud_update":
			h = updateModelHandler
		case "crud_delete":
			h = deleteModelHandler
		default:
			// Use DAG-based handler.
			dag := dags[r.Handler]
			h = func(c *fiber.Ctx) error {
				ctx := context.Background()
				if t := c.Locals("ctx_token"); t != nil {
					ctx = context.WithValue(ctx, "ctx_token", t.(string))
				}
				if u := c.Locals("ctx_userid"); u != nil {
					ctx = context.WithValue(ctx, "ctx_userid", u.(string))
				}
				task := make(Task)
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

	// NEW: Automatically register CRUD routes for models with Rest=true.
	for _, m := range cfg.Models {
		if m.Rest {
			// Use model prefix if provided; otherwise, use pluralized model name.
			routePrefix := m.Prefix
			if routePrefix == "" {
				routePrefix = singularToPlural(m.Name)
			}
			// Register CRUD routes without "/crud" prefix.
			app.Post("/"+routePrefix, func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return createModelHandler(c)
			})
			app.Get("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return readModelHandler(c)
			})
			app.Get("/"+routePrefix, func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return listModelHandler(c)
			})
			app.Put("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return updateModelHandler(c)
			})
			app.Delete("/"+routePrefix+"/:id", func(c *fiber.Ctx) error {
				c.Locals("model", m.Name)
				return deleteModelHandler(c)
			})
		}
	}

	log.Printf("Listening on %s...", cfg.Server.Address)
	log.Fatal(app.Listen(cfg.Server.Address))
}

// NEW: Helper function to convert a singular word to its naive plural form.
func singularToPlural(word string) string {
	if len(word) == 0 {
		return word
	}
	// if already ends with "s", assume plural.
	if strings.HasSuffix(word, "s") {
		return word
	}
	// if word ends with "y", convert "y" to "ies"
	if strings.HasSuffix(word, "y") {
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}

// NEW: Helper function to convert a plural word to its naive singular form.
func pluralToSingular(word string) string {
	if len(word) == 0 {
		return word
	}
	if strings.HasSuffix(word, "ies") {
		return word[:len(word)-3] + "y"
	}
	if strings.HasSuffix(word, "s") {
		return word[:len(word)-1]
	}
	return word
}
