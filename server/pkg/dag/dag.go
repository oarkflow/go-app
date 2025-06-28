package dag

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/oarkflow/squealx"
	"golang.org/x/crypto/bcrypt"

	"github.com/oarkflow/dag/server/pkg/config"
)

type Task = map[string]any
type Result = map[string]any

type NodeProcessor interface {
	Process(ctx context.Context, input Task) (Result, error)
}

type NodeConfig struct {
	Config    config.NodeRaw
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
	d.Nodes[cfg.Config.Name] = cfg
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
			for _, inKey := range node.Config.Input {
				if _, ok := data[inKey]; !ok {
					if v := ctx.Value(inKey); v != nil {
						data[inKey] = v
					}
				}
			}
			ready := true
			for _, inKey := range node.Config.Input {
				if _, ok := data[inKey]; !ok {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			start := time.Now()
			res, err := node.Processor.Process(ctx, data)
			log.Printf("Node %s executed in %v", name, time.Since(start))
			if err != nil {
				return nil, err
			}
			for _, key := range node.Config.Output {
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

// Node types

type DBQueryNode struct {
	Query  string
	DB     *squealx.DB
	OutKey string
	Config config.NodeRaw
}

func (n *DBQueryNode) Process(_ context.Context, in Task) (Result, error) {
	params := make(map[string]any)
	for k, v := range in {
		params[k] = v
	}
	for k, v := range n.Config.FieldMapping {
		if val, ok := params[v]; ok {
			params[k] = val
		}
	}
	var resultMap map[string]any
	if err := n.DB.Select(&resultMap, n.Query, params); err != nil {
		return nil, err
	}
	if len(n.Config.QueryFieldMapping) > 0 {
		mapped := make(map[string]any)
		for k, v := range resultMap {
			if newKey, ok := n.Config.QueryFieldMapping[k]; ok {
				mapped[newKey] = v
			} else {
				mapped[k] = v
			}
		}
		resultMap = mapped
	}
	return Result{n.OutKey: resultMap}, nil
}

type AuthVerifyNode struct {
	DB              *squealx.DB
	UsernameField   string
	PasswordField   string
	UserTable       string
	CredentialTable string
	Config          config.NodeRaw
	AuthCfg         config.Auth
}

func (n *AuthVerifyNode) Process(_ context.Context, in Task) (Result, error) {
	username, ok := in[n.AuthCfg.Login.UsernameField].(string)
	if !ok {
		return nil, fmt.Errorf("username field %s missing or invalid", n.AuthCfg.Login.UsernameField)
	}
	userQuery := fmt.Sprintf("SELECT * FROM %s WHERE %s = :username", n.UserTable, n.UsernameField)
	userParams := map[string]any{"username": username}
	var user map[string]any
	if err := n.DB.Select(&user, userQuery, userParams); err != nil {
		return nil, fmt.Errorf("user not found or error: %v", err)
	}
	credQuery := fmt.Sprintf("SELECT * FROM %s WHERE id = :id", n.CredentialTable)
	var storagePass map[string]any
	if err := n.DB.Select(&storagePass, credQuery, user); err != nil {
		return nil, fmt.Errorf("credentials not found or error: %v", err)
	}
	pass, ok := in[n.AuthCfg.Login.PasswordField].(string)
	if !ok {
		return nil, fmt.Errorf("password field %s missing", n.AuthCfg.Login.PasswordField)
	}
	dbPass, ok := storagePass[n.PasswordField].(string)
	if err := bcrypt.CompareHashAndPassword([]byte(dbPass), []byte(pass)); err != nil {
		return nil, errors.New("invalid credentials")
	}
	return Result{"user": user["id"]}, nil
}

type AuthTokenNode struct {
	DB         *squealx.DB
	JWTSecret  []byte
	TTLSeconds int64
	Config     config.NodeRaw
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
	DB     *squealx.DB
	Config config.NodeRaw
}

func (n *AuthRevokeNode) Process(ctx context.Context, _ Task) (Result, error) {
	token := ctx.Value("ctx_token").(string)
	if _, err := n.DB.Exec("DELETE FROM tokens WHERE token=?", token); err != nil {
		return nil, err
	}
	return Result{"success": true}, nil
}

// BuildDAGs builds all DAGs from config
func BuildDAGs(cfg *config.Config, dbProviders map[string]*squealx.DB, defaultDB *squealx.DB, modelsMap map[string]config.Model, authCfg config.Auth) map[string]*DAG {
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
					Query:  nd.Query,
					DB:     dbConn,
					OutKey: nd.Output[0],
					Config: nd,
				}
			case "auth_verify":
				userModel, ok := modelsMap[authCfg.Login.UserModel]
				if !ok {
					log.Fatalf("user model %s not found", authCfg.Login.UserModel)
				}
				credModel, ok := modelsMap[authCfg.Login.CredentialModel]
				if !ok {
					log.Fatalf("credential model %s not found", authCfg.Login.CredentialModel)
				}
				userTable := userModel.Table
				if userTable == "" {
					userTable = toName(authCfg.Login.UserModel)
				}
				credTable := credModel.Table
				if credTable == "" {
					credTable = toName(authCfg.Login.CredentialModel)
				}
				proc = &AuthVerifyNode{
					DB:              dbConn,
					UsernameField:   authCfg.Login.UsernameField,
					PasswordField:   authCfg.Login.PasswordField,
					UserTable:       userTable,
					CredentialTable: credTable,
					Config:          nd,
					AuthCfg:         authCfg,
				}
			case "auth_token":
				proc = &AuthTokenNode{DB: dbConn, JWTSecret: []byte(secret), TTLSeconds: 3600}
			case "auth_revoke":
				proc = &AuthRevokeNode{DB: dbConn}
			default:
				log.Fatalf("unsupported node type %s", nd.Type)
			}
			dag.AddNode(&NodeConfig{
				Processor: proc,
				Config:    nd,
			})
		}
		for _, e := range dc.Edge {
			dag.AddEdge(Edge{From: e.From, To: e.To})
		}
		dags[dc.Name] = dag
	}
	return dags
}

func findSecret(cfg *config.Config) string {
	for _, m := range cfg.Middleware {
		if m.Type == "jwt" {
			return m.Secret
		}
	}
	log.Fatal("no jwt secret found in config")
	return ""
}

func toName(word string) string {
	name := strings.ReplaceAll(strings.ToLower(word), "-", "_")
	return singularToPlural(name)
}

func singularToPlural(word string) string {
	if len(word) == 0 {
		return word
	}
	if strings.HasSuffix(word, "s") {
		return word
	}
	if strings.HasSuffix(word, "y") {
		return word[:len(word)-1] + "ies"
	}
	return word + "s"
}
