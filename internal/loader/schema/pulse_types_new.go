package schema

// New pulse (check) types added on top of the general SDK. All config structs
// live here (no SDK dependency) so they always compile and parse regardless of
// which build tags are enabled; only the job implementations are build-tagged.

// PulseRedisConfig checks a Redis server by issuing a RESP PING. It supports
// both AUTH (password/username) and DB selection.
type PulseRedisConfig struct {
	Addr     string `yaml:"addr" json:"addr"` // host:port
	Password string `yaml:"password" json:"password"`
	Username string `yaml:"username" json:"username"`
	DB       int    `yaml:"db" json:"db"`
	Retries  int    `yaml:"retries" json:"retries"`
}

func (c *PulseRedisConfig) Copy() PulseConfig {
	newConfig := new(PulseRedisConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseRedisConfig) isPulseConfigs() {}

// PulsePostgresConfig checks a PostgreSQL server by connecting and issuing a
// ping. Either DSN or the discrete host/port/user fields may be supplied.
type PulsePostgresConfig struct {
	DSN      string `yaml:"dsn" json:"dsn"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	User     string `yaml:"user" json:"user"`
	Password string `yaml:"password" json:"password"`
	Database string `yaml:"database" json:"database"`
	SSLMode  string `yaml:"sslmode" json:"sslmode"`
	Retries  int    `yaml:"retries" json:"retries"`
}

func (c *PulsePostgresConfig) Copy() PulseConfig {
	newConfig := new(PulsePostgresConfig)
	*newConfig = *c
	return newConfig
}

func (*PulsePostgresConfig) isPulseConfigs() {}

// PulseMySQLConfig checks a MySQL server by connecting and issuing a COM_PING.
// Either DSN or the discrete host/port/user fields may be supplied.
type PulseMySQLConfig struct {
	DSN      string `yaml:"dsn" json:"dsn"`
	Host     string `yaml:"host" json:"host"`
	Port     int    `yaml:"port" json:"port"`
	User     string `yaml:"user" json:"user"`
	Password string `yaml:"password" json:"password"`
	Database string `yaml:"database" json:"database"`
	Retries  int    `yaml:"retries" json:"retries"`
}

func (c *PulseMySQLConfig) Copy() PulseConfig {
	newConfig := new(PulseMySQLConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseMySQLConfig) isPulseConfigs() {}

// PulseMongoConfig checks a MongoDB server by issuing a ping command.
type PulseMongoConfig struct {
	URI     string `yaml:"uri" json:"uri"` // mongodb:// connection string
	Retries int    `yaml:"retries" json:"retries"`
}

func (c *PulseMongoConfig) Copy() PulseConfig {
	newConfig := new(PulseMongoConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseMongoConfig) isPulseConfigs() {}

// PulseRabbitMQConfig checks a RabbitMQ broker by opening an AMQP connection
// and channel.
type PulseRabbitMQConfig struct {
	URL     string `yaml:"url" json:"url"` // amqp://user:pass@host:port/vhost
	Retries int    `yaml:"retries" json:"retries"`
}

func (c *PulseRabbitMQConfig) Copy() PulseConfig {
	newConfig := new(PulseRabbitMQConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseRabbitMQConfig) isPulseConfigs() {}

// PulseKafkaConfig checks a Kafka cluster by connecting to seed brokers and
// issuing a metadata ping.
type PulseKafkaConfig struct {
	Brokers []string `yaml:"brokers" json:"brokers"` // seed brokers host:port
	Retries int      `yaml:"retries" json:"retries"`
}

func (c *PulseKafkaConfig) Copy() PulseConfig {
	newConfig := new(PulseKafkaConfig)
	*newConfig = *c
	newConfig.Brokers = append([]string(nil), c.Brokers...)
	return newConfig
}

func (*PulseKafkaConfig) isPulseConfigs() {}

// PulseTLSConfig checks a TLS endpoint by completing a handshake and reports
// days-to-expiry of the leaf certificate. Pure stdlib (crypto/tls + crypto/x509),
// so it is always compiled with no build tag.
type PulseTLSConfig struct {
	Host               string `yaml:"host" json:"host"`
	Port               int    `yaml:"port" json:"port"`
	ServerName         string `yaml:"server_name" json:"server_name"`     // SNI name; defaults to Host
	WarnDays           int    `yaml:"warn_days" json:"warn_days"`         // warn when cert expires within N days
	CriticalDays       int    `yaml:"critical_days" json:"critical_days"` // fail when cert expires within N days
	InsecureSkipVerify bool   `yaml:"insecure_skip_verify" json:"insecure_skip_verify"`
	Retries            int    `yaml:"retries" json:"retries"`
}

func (c *PulseTLSConfig) Copy() PulseConfig {
	newConfig := new(PulseTLSConfig)
	*newConfig = *c
	return newConfig
}

func (*PulseTLSConfig) isPulseConfigs() {}
