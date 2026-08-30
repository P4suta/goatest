// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package config loads the optional strict .goatest.toml v1 configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const FileName = ".goatest.toml"

type Resource struct {
	Command   []string
	Timeout   time.Duration
	Shared    bool
	Exclusive bool
}

type Generation struct {
	Command      []string
	AllowedPaths []string
}

type Acceptance struct {
	ID      string
	Reason  string
	Expires time.Time
}

type Config struct {
	Version    int
	Contract   string
	Resources  map[string]Resource
	Generation Generation
	Acceptance []Acceptance
}

type rawConfig struct {
	Version    int                    `toml:"version"`
	Contract   string                 `toml:"contract"`
	Resources  map[string]rawResource `toml:"resources"`
	Generation rawGeneration          `toml:"generation"`
	Acceptance []rawAcceptance        `toml:"acceptance"`
}

type rawResource struct {
	Command   []string `toml:"command"`
	Timeout   string   `toml:"timeout"`
	Shared    bool     `toml:"shared"`
	Exclusive bool     `toml:"exclusive"`
}

type rawGeneration struct {
	Command      []string `toml:"command"`
	AllowedPaths []string `toml:"allowed_paths"`
}

type rawAcceptance struct {
	ID      string `toml:"id"`
	Reason  string `toml:"reason"`
	Expires string `toml:"expires"`
}

type configWritableFile interface {
	Name() string
	Write([]byte) (int, error)
	Sync() error
	Chmod(os.FileMode) error
	Close() error
}

var (
	openConfigFile = func(name string, flag int, mode os.FileMode) (configWritableFile, error) {
		return os.OpenFile(name, flag, mode)
	}
	createConfigTemp = func(directory, pattern string) (configWritableFile, error) {
		return os.CreateTemp(directory, pattern)
	}
	marshalConfig    = toml.Marshal
	removeConfigFile = os.Remove
	renameConfigFile = os.Rename
)

func defaults() Config {
	return Config{Version: 1, Contract: "standard-v1", Resources: map[string]Resource{}}
}

func Load(root string) (Config, error) {
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults(), nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("goatest: read config: %w", err)
	}
	decoder := toml.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var raw rawConfig
	if err := decoder.Decode(&raw); err != nil {
		return Config{}, fmt.Errorf("goatest: decode %s: %w", FileName, err)
	}
	if raw.Version != 1 {
		return Config{}, fmt.Errorf("goatest: config version %d: expected 1", raw.Version)
	}
	if raw.Contract == "" {
		raw.Contract = "standard-v1"
	}
	if raw.Contract != "standard-v1" && raw.Contract != "deep-v1" {
		return Config{}, fmt.Errorf("goatest: config contract %q: expected standard-v1 or deep-v1", raw.Contract)
	}
	result := Config{
		Version:   1,
		Contract:  raw.Contract,
		Resources: make(map[string]Resource, len(raw.Resources)),
		Generation: Generation{
			Command:      append([]string(nil), raw.Generation.Command...),
			AllowedPaths: append([]string(nil), raw.Generation.AllowedPaths...),
		},
	}
	for name, rawResource := range raw.Resources {
		if name == "" || len(rawResource.Command) == 0 || rawResource.Command[0] == "" {
			return Config{}, fmt.Errorf("goatest: resource %q requires a command", name)
		}
		timeout := 30 * time.Second
		if rawResource.Timeout != "" {
			parsed, parseErr := time.ParseDuration(rawResource.Timeout)
			if parseErr != nil {
				return Config{}, fmt.Errorf("goatest: resource %q timeout %q is invalid: %w", name, rawResource.Timeout, parseErr)
			}
			if parsed <= 0 {
				return Config{}, fmt.Errorf("goatest: resource %q timeout %q is not positive", name, rawResource.Timeout)
			}
			timeout = parsed
		}
		if rawResource.Shared && rawResource.Exclusive {
			return Config{}, fmt.Errorf("goatest: resource %q cannot be shared and exclusive", name)
		}
		result.Resources[name] = Resource{
			Command:   append([]string(nil), rawResource.Command...),
			Timeout:   timeout,
			Shared:    rawResource.Shared,
			Exclusive: rawResource.Exclusive,
		}
	}
	for _, rawAcceptance := range raw.Acceptance {
		if rawAcceptance.ID == "" || rawAcceptance.Reason == "" || rawAcceptance.Expires == "" {
			return Config{}, errors.New("goatest: every acceptance requires id, reason, and expires")
		}
		expires, parseErr := time.Parse(time.RFC3339, rawAcceptance.Expires)
		if parseErr != nil {
			return Config{}, fmt.Errorf("goatest: acceptance %q expiry: %w", rawAcceptance.ID, parseErr)
		}
		result.Acceptance = append(result.Acceptance, Acceptance{
			ID: rawAcceptance.ID, Reason: rawAcceptance.Reason, Expires: expires,
		})
	}
	return result, nil
}

// Init creates the smallest strict v1 configuration and never overwrites an
// existing file.
func Init(root string) error {
	path := filepath.Join(root, FileName)
	file, err := openConfigFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("goatest: %s already exists", FileName)
		}
		return fmt.Errorf("goatest: create %s: %w", FileName, err)
	}
	data := []byte("version = 1\ncontract = \"standard-v1\"\n")
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = removeConfigFile(path)
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		_ = removeConfigFile(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = removeConfigFile(path)
		return err
	}
	return nil
}

// AddAcceptance appends one explicit, expiring finding acceptance through an
// atomic rewrite of the strict known-key configuration.
func AddAcceptance(root string, acceptance Acceptance) error {
	if strings.TrimSpace(acceptance.ID) == "" || strings.TrimSpace(acceptance.Reason) == "" || acceptance.Expires.IsZero() {
		return errors.New("goatest: acceptance requires id, reason, and expiry")
	}
	loaded, err := Load(root)
	if err != nil {
		return err
	}
	for _, existing := range loaded.Acceptance {
		if existing.ID == acceptance.ID {
			return fmt.Errorf("goatest: acceptance %q already exists", acceptance.ID)
		}
	}
	loaded.Acceptance = append(loaded.Acceptance, acceptance)
	slices.SortFunc(loaded.Acceptance, func(a, b Acceptance) int { return strings.Compare(a.ID, b.ID) })
	return save(root, loaded)
}

func save(root string, input Config) error {
	raw := rawConfig{
		Version: input.Version, Contract: input.Contract,
		Resources: make(map[string]rawResource, len(input.Resources)),
		Generation: rawGeneration{
			Command: slices.Clone(input.Generation.Command), AllowedPaths: slices.Clone(input.Generation.AllowedPaths),
		},
	}
	for name, resource := range input.Resources {
		raw.Resources[name] = rawResource{
			Command: slices.Clone(resource.Command), Timeout: resource.Timeout.String(),
			Shared: resource.Shared, Exclusive: resource.Exclusive,
		}
	}
	for _, acceptance := range input.Acceptance {
		raw.Acceptance = append(raw.Acceptance, rawAcceptance{
			ID: acceptance.ID, Reason: acceptance.Reason, Expires: acceptance.Expires.UTC().Format(time.RFC3339),
		})
	}
	data, err := marshalConfig(raw)
	if err != nil {
		return fmt.Errorf("goatest: encode %s: %w", FileName, err)
	}
	path := filepath.Join(root, FileName)
	temporary, err := createConfigTemp(root, ".goatest-config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = removeConfigFile(temporaryPath) }()
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := renameConfigFile(temporaryPath, path); err != nil {
		if removeErr := removeConfigFile(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return errors.Join(err, removeErr)
		}
		return renameConfigFile(temporaryPath, path)
	}
	return nil
}
