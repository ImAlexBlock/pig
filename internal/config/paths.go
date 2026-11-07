package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// Paths resolves the on-disk locations rooted at $PIG_HOME.
type Paths struct {
	Home string
}

// Resolve returns the resolved paths. PIG_HOME must be absolute when set.
func Resolve() (Paths, error) {
	if h := os.Getenv("PIG_HOME"); h != "" {
		if !filepath.IsAbs(h) {
			return Paths{}, fmt.Errorf("config: PIG_HOME: must be an absolute path")
		}
		return Paths{Home: filepath.Clean(h)}, nil
	}
	hd, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("config: home: %v", err)
	}
	return Paths{Home: filepath.Join(hd, ".pig")}, nil
}

// Ensure creates the home directory with mode 0700.
func (p Paths) Ensure() error {
	if err := os.MkdirAll(p.Home, 0o700); err != nil {
		return fmt.Errorf("config: home: %v", err)
	}
	if err := os.Chmod(p.Home, 0o700); err != nil {
		return fmt.Errorf("config: home: %v", err)
	}
	return nil
}

func (p Paths) Config() string    { return filepath.Join(p.Home, "config.toml") }
func (p Paths) Auth() string      { return filepath.Join(p.Home, "auth.env") }
func (p Paths) DB() string        { return filepath.Join(p.Home, "pig.db") }
func (p Paths) PID() string       { return filepath.Join(p.Home, "pig.pid") }
func (p Paths) Lock() string      { return filepath.Join(p.Home, "pig.lock") }
func (p Paths) Artifacts() string { return filepath.Join(p.Home, "artifacts") }
func (p Paths) LogsDir() string   { return filepath.Join(p.Home, "logs") }
func (p Paths) Log() string       { return filepath.Join(p.Home, "logs", "daemon.log") }
func (p Paths) Workdir() string   { return filepath.Join(p.Home, "workdir") }

// Socket returns the configured socket path, defaulting to $PIG_HOME/pig.sock.
func (p Paths) Socket(configured string) string {
	if configured != "" {
		return configured
	}
	return filepath.Join(p.Home, "pig.sock")
}

// ArtifactPath returns the path of one artifact file.
func (p Paths) ArtifactPath(sessionID, artifactID string) string {
	return filepath.Join(p.Home, "artifacts", sessionID, artifactID)
}
