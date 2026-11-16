package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Config struct {
	AppPort string
	DB      DBConfig
	Redis   RedisConfig
	Session SessionConfig
}

type SessionConfig struct {
	TTL          time.Duration
	CookieSecure bool
	CookieName   string
}

type DBConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	Name     string
	SSLMode  string
}

func (c DBConfig) DSN() string {
	u := &url.URL{Scheme: "postgres", Host: net.JoinHostPort(c.Host, c.Port), Path: "/" + c.Name, User: url.UserPassword(c.User, c.Password)}
	q := u.Query()
	q.Set("sslmode", c.SSLMode)
	u.RawQuery = q.Encode()
	return u.String()
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

// Load читает конфиг из ENV. Сначала пытается загрузить .env (для локального запуска),
// затем читает переменные окружения. .env не перезаписывает уже установленные переменные —
// это важно, когда приложение запускается в Docker, где ENV приходит из docker-compose.
func Load() (*Config, error) {
	_ = godotenv.Load() // игнорируем ошибку: .env может отсутствовать

	redisDB, err := strconv.Atoi(getEnv("REDIS_DB", "0"))
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_DB: %w", err)
	}
	if redisDB < 0 {
		return nil, fmt.Errorf("REDIS_DB must be nonnegative")
	}
	ttl, err := time.ParseDuration(getEnv("SESSION_TTL", "24h"))
	if err != nil || ttl < time.Minute || ttl > 30*24*time.Hour {
		return nil, fmt.Errorf("SESSION_TTL must be a duration between 1m and 720h")
	}
	secure, err := strconv.ParseBool(getEnv("COOKIE_SECURE", "false"))
	if err != nil {
		return nil, fmt.Errorf("invalid COOKIE_SECURE: %w", err)
	}
	port, err := strconv.Atoi(getEnv("APP_PORT", "8080"))
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("APP_PORT must be between 1 and 65535")
	}

	return &Config{
		AppPort: getEnv("APP_PORT", "8080"),
		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5532"),
			User:     getEnv("DB_USER", "auth"),
			Password: getEnv("DB_PASSWORD", ""),
			Name:     getEnv("DB_NAME", "authdb"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Addr:     getEnv("REDIS_ADDR", "localhost:56379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       redisDB,
		},
		Session: SessionConfig{TTL: ttl, CookieSecure: secure, CookieName: "session"},
	}, nil
}

func getEnv(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}
