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
	Metrics          Metrics      `bcl:"metrics"`
	Tracing          Tracing      `bcl:"tracing"`
	Docs             Docs         `bcl:"docs"`
	Features         Features     `bcl:"features"`
	AuthGuard        []AuthGuard  `bcl:"auth_guard"`
}

type Model struct {
	Name     string       `bcl:"name"`
	Table    string       `bcl:"table"`
	Rest     bool         `bcl:"rest"`
	Prefix   string       `bcl:"prefix"`
	Provider string       `bcl:"provider"`
	Fields   []ModelField `bcl:"fields"`
}

type Pool struct {
	MaxOpenConns    int `bcl:"max_open_conns"`
	MaxIdleConns    int `bcl:"max_idle_conns"`
	ConnMaxLifetime int `bcl:"conn_max_lifetime"` // in seconds
}

type Provider struct {
	Name    string         `bcl:"name"`
	Driver  string         `bcl:"driver"`
	Type    string         `bcl:"type"`
	DSN     string         `bcl:"dsn"`
	Default bool           `bcl:"default"`
	Pool    Pool           `bcl:"pool"`
	Options map[string]any `bcl:"options"`
}

type HealthCheck struct {
	Enabled bool   `bcl:"enabled"`
	Path    string `bcl:"path"`
}

type Maintenance struct {
	Enabled bool   `bcl:"enabled"`
	Route   string `bcl:"route"`
	HTML    string `bcl:"html"`
}

type Migrations struct {
	Enabled bool   `bcl:"enabled"`
	Dir     string `bcl:"dir"`
	Table   string `bcl:"table"`
}

type Server struct {
	Name            string      `bcl:"name"`
	Address         string      `bcl:"address"`
	ReadTimeout     int         `bcl:"read_timeout"`
	WriteTimeout    int         `bcl:"write_timeout"`
	IdleTimeout     int         `bcl:"idle_timeout"`
	HeaderTimeout   int         `bcl:"header_timeout"`
	ShutdownTimeout int         `bcl:"shutdown_timeout"`
	BodyLimit       int         `bcl:"body_limit"`
	HealthCheck     HealthCheck `bcl:"health_check"`
	TLS             TLSConfig   `bcl:"tls"`
	Maintenance     Maintenance `bcl:"maintenance"`
	Migrations      Migrations  `bcl:"migrations"`
}

type TLSConfig struct {
	CertFile     string   `bcl:"cert_file"`
	KeyFile      string   `bcl:"key_file"`
	MinVersion   string   `bcl:"min_version"`
	CipherSuites []string `bcl:"cipher_suites"`
}

type Metrics struct {
	Enabled   bool   `bcl:"enabled"`
	Path      string `bcl:"path"`
	Provider  string `bcl:"provider"`
	Namespace string `bcl:"namespace"`
}

type Tracing struct {
	Enabled    bool   `bcl:"enabled"`
	Provider   string `bcl:"provider"`
	Endpoint   string `bcl:"endpoint"`
	Sampler    string `bcl:"sampler"`
	SamplerArg int    `bcl:"sampler_arg"`
}

type Docs struct {
	Enabled bool   `bcl:"enabled"`
	Path    string `bcl:"path"`
	Spec    string `bcl:"spec"`
}

type Features struct {
	BetaFeature bool `bcl:"beta_feature"`
	NewUI       bool `bcl:"new_ui"`
}

type StaticConfig struct {
	Dir      string `bcl:"dir"`
	Index    string `bcl:"index"`
	CacheMax int    `bcl:"cache_max"`
}

type StaticRoute struct {
	Dir      string `bcl:"dir"`
	Index    string `bcl:"index"`
	CacheMax int    `bcl:"cache_max"`
}

type AuthGuard struct {
	Name        string `bcl:"name"`
	Placeholder string `bcl:"placeholder"`
	Key         string `bcl:"key"`
	Scheme      string `bcl:"scheme"`
}

type Middleware struct {
	Name             string   `bcl:"name"`
	Type             string   `bcl:"type"`
	Secret           string   `bcl:"secret"`
	Format           string   `bcl:"format"`
	Level            string   `bcl:"level"`
	Output           string   `bcl:"output"`
	Max              int      `bcl:"max"`
	Window           int      `bcl:"window"`
	Key              string   `bcl:"key"`
	FailureThreshold int      `bcl:"failure_threshold"`
	RecoveryTimeout  int      `bcl:"recovery_timeout"`
	HeaderName       string   `bcl:"header_name"`
	Generator        string   `bcl:"generator"`
	MaxFiles         int      `bcl:"max_files"`
	MaxSize          int      `bcl:"max_size"`
	AllowOrigins     []string `bcl:"allow_origins"`
	AllowMethods     []string `bcl:"allow_methods"`
	AllowHeaders     []string `bcl:"allow_headers"`
	ExposeHeaders    []string `bcl:"expose_headers"`
	AllowCredentials bool     `bcl:"allow_credentials"`
	MaxAge           int      `bcl:"max_age"`
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

type ValidationRule struct {
	Field    string   `bcl:"field"`
	Required bool     `bcl:"required"`
	Type     string   `bcl:"type"`
	Enum     []string `bcl:"enum"`
	Min      *int     `bcl:"min"`
	Max      *int     `bcl:"max"`
	Pattern  string   `bcl:"pattern"`
}

type Pagination struct {
	Enabled   bool   `bcl:"enabled"`
	PageField string `bcl:"page_field"`
	SizeField string `bcl:"size_field"`
	Default   int    `bcl:"default"`
	Max       int    `bcl:"max"`
}

type Sorting struct {
	Enabled    bool     `bcl:"enabled"`
	Fields     []string `bcl:"fields"`
	Default    string   `bcl:"default"`
	AllowMulti bool     `bcl:"allow_multi"`
}

type ErrorFormat struct {
	CodeField    string `bcl:"code_field"`
	MessageField string `bcl:"message_field"`
}

type ResponseFormat struct {
	Envelope string `bcl:"envelope"`
}

type Hook struct {
	Type   string `bcl:"type"`   // pre, post
	Script string `bcl:"script"` // path to script or inline
}

type Route struct {
	Name           string           `bcl:"name"`
	Group          string           `bcl:"group"`
	Method         string           `bcl:"method"`
	Path           string           `bcl:"path"`
	Middleware     []string         `bcl:"middleware"`
	Handler        string           `bcl:"handler"`
	Request        Request          `bcl:"request"`
	Response       Response         `bcl:"response"`
	QueryParams    []string         `bcl:"query_params"`
	Headers        []string         `bcl:"headers"`
	Validation     []ValidationRule `bcl:"validation"`
	Pagination     Pagination       `bcl:"pagination"`
	Sorting        Sorting          `bcl:"sorting"`
	ErrorFormat    ErrorFormat      `bcl:"error_format"`
	ResponseFormat ResponseFormat   `bcl:"response_format"`
	Hooks          []Hook           `bcl:"hooks"`
	Version        string           `bcl:"version"`
	Static         StaticRoute      `bcl:"static"`
}

type DAG struct {
	Name    string    `bcl:"name"`
	Node    []NodeRaw `bcl:"node"`
	Edge    []EdgeRaw `bcl:"edge"`
	Version string    `bcl:"version"`
	Hooks   []Hook    `bcl:"hooks"`
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
