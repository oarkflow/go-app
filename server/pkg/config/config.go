package config

type AuthLogin struct {
	Route           string `bcl:"route"`
	UserModel       string `bcl:"user_model"`
	UsernameField   string `bcl:"username_field"`
	CredentialModel string `bcl:"credential_model"`
	PasswordField   string `bcl:"password_field"`
}
type AuthLogout struct {
	Route string `bcl:"route"`
}
type Auth struct {
	Login  AuthLogin  `bcl:"login"`
	Logout AuthLogout `bcl:"logout"`
}

type Config struct {
	Server           Server       `bcl:"server"`
	Provider         []Provider   `bcl:"provider"`
	Middleware       []Middleware `bcl:"middleware"`
	GlobalMiddleware []string     `bcl:"global_middleware"`
	Group            []Group      `bcl:"group"`
	Route            []Route      `bcl:"route"`
	DAG              []DAG        `bcl:"dag"`
	Models           []Model      `bcl:"model"`
	Auth             Auth         `bcl:"auth"`
}

type Model struct {
	Name     string       `bcl:"name"`
	Table    string       `bcl:"table"`
	Rest     bool         `bcl:"rest"`
	Prefix   string       `bcl:"prefix,optional"`
	Provider string       `bcl:"provider,optional"`
	Fields   []ModelField `bcl:"fields"`
}

type Provider struct {
	Name    string `bcl:"name"`
	Driver  string `bcl:"driver"`
	Type    string `bcl:"type"`
	DSN     string `bcl:"dsn"`
	Default bool   `bcl:"default"`
}

type HealthCheck struct {
	Enabled bool   `bcl:"enabled"`
	Path    string `bcl:"path"`
}

type Server struct {
	Name         string      `bcl:"name"`
	Address      string      `bcl:"address"`
	ReadTimeout  int         `bcl:"read_timeout"`
	WriteTimeout int         `bcl:"write_timeout"`
	HealthCheck  HealthCheck `bcl:"health_check"`
}

type Middleware struct {
	Name   string `bcl:"name"`
	Type   string `bcl:"type"`
	Secret string `bcl:"secret"`
}

type Group struct {
	Name       string   `bcl:"name"`
	Path       string   `bcl:"path"`
	Middleware []string `bcl:"middleware"`
}

type Request struct {
	Body   []string `bcl:"body"`
	Params []string `bcl:"params"`
}

type Response struct {
	Fields []string `bcl:"fields"`
}

type Route struct {
	Name       string   `bcl:"name"`
	Group      string   `bcl:"group"`
	Method     string   `bcl:"method"`
	Path       string   `bcl:"path"`
	Middleware []string `bcl:"middleware"`
	Handler    string   `bcl:"handler"`
	Request    Request  `bcl:"request"`
	Response   Response `bcl:"response"`
}

type DAG struct {
	Name string    `bcl:"name"`
	Node []NodeRaw `bcl:"node"`
	Edge []EdgeRaw `bcl:"edge"`
}

type NodeRaw struct {
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

type ModelField struct {
	Name       string `bcl:"name"`
	DataType   string `bcl:"data_type"`
	DefaultVal string `bcl:"default_val"`
	SoftDelete bool   `bcl:"soft_delete"`
}
