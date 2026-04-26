package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ByteSize is an int64 that unmarshals human-readable size strings like "8MiB", "2GiB".
type ByteSize int64

func (b *ByteSize) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		// Try decoding as a raw integer.
		var n int64
		if err2 := value.Decode(&n); err2 != nil {
			return fmt.Errorf("bytesize: cannot decode value: %w", err2)
		}
		*b = ByteSize(n)
		return nil
	}

	s = strings.TrimSpace(s)
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"GB", 1_000_000_000},
		{"MB", 1_000_000},
		{"KB", 1_000},
	}

	for _, sf := range suffixes {
		if strings.HasSuffix(s, sf.suffix) {
			numStr := strings.TrimSpace(strings.TrimSuffix(s, sf.suffix))
			var n int64
			if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
				return fmt.Errorf("bytesize: invalid number %q in %q", numStr, s)
			}
			*b = ByteSize(n * sf.mult)
			return nil
		}
	}

	// Plain integer string.
	var n int64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return fmt.Errorf("bytesize: unrecognised size string %q", s)
	}
	*b = ByteSize(n)
	return nil
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Listen          string        `yaml:"listen"`
	ExternalURL     string        `yaml:"external_url"`
	SessionTTL      time.Duration `yaml:"session_ttl"`
	UploadChunkSize ByteSize      `yaml:"upload_chunk_size"`
	MaxFileSize     ByteSize      `yaml:"max_file_size"`
}

// AdminConfig holds administrator credentials.
type AdminConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

// StorageConfig holds local storage settings.
type StorageConfig struct {
	SQLitePath string `yaml:"sqlite_path"`
}

// OSSConfig holds Aliyun OSS settings.
type OSSConfig struct {
	Endpoint         string        `yaml:"endpoint"`
	InternalEndpoint string        `yaml:"internal_endpoint"`
	AccessKeyID      string        `yaml:"access_key_id"`
	AccessKeySecret  string        `yaml:"access_key_secret"`
	Bucket           string        `yaml:"bucket"`
	Prefix           string        `yaml:"prefix"`
	PresignTTL       time.Duration `yaml:"presign_ttl"`
}

// VerifyConfig holds outbound HTTP verification settings.
type VerifyConfig struct {
	HTTPTimeout      time.Duration `yaml:"http_timeout"`
	FollowRedirects  int           `yaml:"follow_redirects"`
}

// Config is the top-level application configuration.
type Config struct {
	Server  ServerConfig  `yaml:"server"`
	Admin   AdminConfig   `yaml:"admin"`
	Storage StorageConfig `yaml:"storage"`
	OSS     OSSConfig     `yaml:"oss"`
	Verify  VerifyConfig  `yaml:"verify"`
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Listen:          ":8080",
			SessionTTL:      12 * time.Hour,
			UploadChunkSize: ByteSize(8 * (1 << 20)),  // 8 MiB
			MaxFileSize:     ByteSize(2 * (1 << 30)),  // 2 GiB
		},
		OSS: OSSConfig{
			PresignTTL: 10 * time.Minute,
		},
		Verify: VerifyConfig{
			HTTPTimeout:     5 * time.Second,
			FollowRedirects: 5,
		},
	}
}

// Load reads the YAML configuration file at path and returns a populated Config.
// Sensible defaults are applied before the file is parsed, so missing fields
// retain their default values.
func Load(path string) (*Config, error) {
	cfg := defaults()

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: open %q: %w", path, err)
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("config: parse %q: %w", path, err)
	}

	return cfg, nil
}
