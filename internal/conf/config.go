package conf

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/viper"
	"github.com/suprelory/octopus-plus/internal/utils/log"
)

type Server struct {
	Host string `mapstructure:"host"`
	Port int    `mapstructure:"port"`
	// MaxRequestBodyMB 是客户端请求体上限（MB）。<=0 表示不限制。
	// 不覆盖 /v1/images（由 OCTOPUS_IMAGES_BODY_MAX_MB 控制，会落盘不驻留内存）
	// 与 /api/v1/setting/import（管理员数据库导入，体积不可预测）。
	MaxRequestBodyMB int `mapstructure:"max_request_body_mb"`
	// TrustedProxies 是可信反向代理的启动默认值。数据库尚无
	// trusted_proxies 设置时会用它初始化；之后由设置页面中的运行时值接管。
	//
	// 为空（默认）表示不信任任何代理：客户端 IP 等于 TCP 对端地址，
	// X-Forwarded-For / X-Real-IP 一律忽略。登录限流依赖这一点 —— 否则任何人
	// 伪造一个 XFF 头就能换一个限流桶。
	//
	// 部署在 nginx / LB 后面时填代理地址，如 ["127.0.0.1"] 或 ["10.0.0.0/8"]，
	// 否则所有请求会共用代理的 IP，登录限流会误伤同一代理后的其他用户。
	TrustedProxies []string `mapstructure:"trusted_proxies"`
}

type Log struct {
	Level           string `mapstructure:"level"`
	Format          string `mapstructure:"format"`
	Caller          bool   `mapstructure:"caller"`
	StacktraceLevel string `mapstructure:"stacktrace_level"`
	Access          struct {
		Enabled         bool `mapstructure:"enabled"`
		SlowThresholdMS int  `mapstructure:"slow_threshold_ms"`
	} `mapstructure:"access"`
	Relay struct {
		Summary bool `mapstructure:"summary"`
	} `mapstructure:"relay"`
}

type Database struct {
	Type string `mapstructure:"type"`
	Path string `mapstructure:"path"`
}

type Startup struct {
	CacheInitTimeoutSeconds int `mapstructure:"cache_init_timeout_seconds"`
}

type Config struct {
	Server   Server   `mapstructure:"server"`
	Log      Log      `mapstructure:"log"`
	Database Database `mapstructure:"database"`
	Startup  Startup  `mapstructure:"startup"`
}

var AppConfig Config

// DefaultMaxRequestBodyMB 是客户端请求体的默认上限。取 32 MB：足够容纳带图片
// 的多模态对话（base64 后约 1.3 倍膨胀），又不至于让单个请求吃掉过多内存。
const DefaultMaxRequestBodyMB = 32

// MaxRequestBodyBytes 返回客户端请求体上限（字节）。返回 0 表示不限制。
func MaxRequestBodyBytes() int64 {
	mb := AppConfig.Server.MaxRequestBodyMB
	if mb <= 0 {
		return 0
	}
	return int64(mb) * 1024 * 1024
}

func CacheInitTimeout() time.Duration {
	seconds := AppConfig.Startup.CacheInitTimeoutSeconds
	if seconds <= 0 {
		seconds = 120
	}
	return time.Duration(seconds) * time.Second
}

func configReader(path string) *viper.Viper {
	reader := viper.New()
	if path != "" {
		reader.SetConfigFile(path)
	} else {
		reader.SetConfigName("config")
		reader.SetConfigType("json")
		reader.AddConfigPath("data")
	}

	reader.AutomaticEnv()
	reader.SetEnvPrefix(APP_NAME)
	reader.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	setDefaults(reader)
	return reader
}

// ReadDatabaseConfig resolves the same file, defaults and environment as start,
// without creating a config file, logging to stdout or changing AppConfig.
func ReadDatabaseConfig(path string) (Database, error) {
	reader := configReader(path)
	if err := reader.ReadInConfig(); err != nil {
		if _, missing := err.(viper.ConfigFileNotFoundError); path != "" || !missing {
			return Database{}, err
		}
	}
	return Database{Type: reader.GetString("database.type"), Path: reader.GetString("database.path")}, nil
}

func Load(path string) error {
	reader := configReader(path)
	if err := reader.ReadInConfig(); err == nil {
		log.Infof("Using config file: %s", reader.ConfigFileUsed())
	} else {
		if _, ok := err.(viper.ConfigFileNotFoundError); ok {
			log.Infof("Config file not found, creating default config")
			if err := os.MkdirAll("data", 0755); err != nil {
				log.Errorf("Failed to create data directory: %v", err)
			}
			if err := reader.SafeWriteConfigAs("data/config.json"); err != nil {
				log.Errorf("Failed to create default config: %v", err)
			}
		} else {
			return fmt.Errorf("error reading config file: %w", err)
		}
	}

	if err := reader.Unmarshal(&AppConfig); err != nil {
		return fmt.Errorf("unable to decode config into struct: %w", err)
	}
	return nil
}

func setDefaults(reader *viper.Viper) {
	reader.SetDefault("server.host", "0.0.0.0")
	reader.SetDefault("server.port", 8080)
	reader.SetDefault("server.max_request_body_mb", DefaultMaxRequestBodyMB)
	reader.SetDefault("server.trusted_proxies", []string{})
	reader.SetDefault("database.type", "sqlite")
	reader.SetDefault("database.path", "data/data.db")
	reader.SetDefault("log.level", "info")
	reader.SetDefault("log.format", "console")
	reader.SetDefault("log.caller", false)
	reader.SetDefault("log.stacktrace_level", "error")
	reader.SetDefault("log.access.enabled", false)
	reader.SetDefault("log.access.slow_threshold_ms", 3000)
	reader.SetDefault("log.relay.summary", true)
	reader.SetDefault("startup.cache_init_timeout_seconds", 120)
}
