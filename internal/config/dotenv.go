package config

import (
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
)

// DotenvPaths lists the .env files LoadDotenv consults, nearest first: the
// working directory, then the directory holding the executable.
func DotenvPaths() []string {
	paths := []string{".env"}
	if exe, err := os.Executable(); err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			exe = resolved
		}
		beside := filepath.Join(filepath.Dir(exe), ".env")
		if abs, err := filepath.Abs(paths[0]); err != nil || abs != beside {
			paths = append(paths, beside)
		}
	}
	return paths
}

// LoadDotenv fills gaps in the environment from the .env files, nearest first.
// A variable already set in the real environment always wins.
func LoadDotenv() []string {
	var loaded []string
	for _, path := range DotenvPaths() {
		values, err := godotenv.Read(path)
		if err != nil {
			continue
		}
		for key, value := range values {
			if _, ok := os.LookupEnv(key); !ok {
				os.Setenv(key, value)
			}
		}
		loaded = append(loaded, path)
	}
	return loaded
}
