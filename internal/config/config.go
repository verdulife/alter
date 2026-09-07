package config

// Config holds application-wide configuration.
type Config struct {
	Version string
}

// Load returns the default configuration.
func Load() Config {
	return Config{
		Version: "0.1.0",
	}
}
