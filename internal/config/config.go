package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

var (
	Setting = &configs{}
	envPath = "config/config.env"
)

type configs struct {
	Server server `envPrefix:"SERVER_"`
}

type server struct {
	Port         string        `env:"PORT" envDefault:"8080"`
	RunMode      string        `env:"RUNMODE" envDefault:"debug"`
	ReadTimeout  time.Duration `env:"READ_TIMEOUT" envDefault:"60s"`
	WriteTimeout time.Duration `env:"WRITE_TIMEOUT" envDefault:"60s"`
}

// Setup Initial configuration
func Setup() error {
	if err := loadEnvFile(); err != nil {
		return err
	}

	err := env.Parse(Setting)
	if err != nil {
		return fmt.Errorf("parse env config failed: %w", err)
	}
	return nil
}

func loadEnvFile() error {
	for _, candidate := range envCandidates() {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(candidate); err == nil {
			if err := godotenv.Load(candidate); err != nil {
				return fmt.Errorf("load env file failed: %w", err)
			}
			return nil
		}
	}
	return nil
}

func envCandidates() []string {
	candidates := []string{envPath}
	exePath, err := os.Executable()
	if err != nil {
		return candidates
	}
	exeDir := filepath.Dir(exePath)
	candidates = append(candidates,
		filepath.Join(exeDir, "config", "config.env"),
		filepath.Join(exeDir, "..", "config", "config.env"),
	)
	return candidates
}
