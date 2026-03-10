package secrets

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
)

// DotEnvSecretProvider resolves secrets from a dotenv-style file.
// By default it reads ~/.forge/secrets/.env.
type DotEnvSecretProvider struct {
	Path string
}

func NewDotEnvSecretProvider(path string) *DotEnvSecretProvider {
	if strings.TrimSpace(path) == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			path = filepath.Join(home, ".forge", "secrets", ".env")
		}
	}
	return &DotEnvSecretProvider{Path: path}
}

func (p *DotEnvSecretProvider) Resolve(ctx context.Context, key string) (string, error) {
	_ = ctx
	if strings.TrimSpace(p.Path) == "" {
		return "", ErrSecretNotFound
	}

	file, err := os.Open(p.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrSecretNotFound
		}
		return "", err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		keyPart, valuePart, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		if strings.TrimSpace(keyPart) != key {
			continue
		}
		value := strings.TrimSpace(valuePart)
		if len(value) >= 2 {
			if strings.HasPrefix(value, "\"") && strings.HasSuffix(value, "\"") {
				value = value[1 : len(value)-1]
			} else if strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'") {
				value = value[1 : len(value)-1]
			}
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", ErrSecretNotFound
}
