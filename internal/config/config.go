// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

// Package config loads the optional strict .goatest.toml v1 configuration.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/testargs"
	"github.com/pelletier/go-toml/v2"
)

const FileName = ".goatest.toml"

type Resource struct {
	Command     []string
	Timeout     time.Duration
	Shared      bool
	Exclusive   bool
	Environment []string
}

type Generation struct {
	Command      []string
	AllowedPaths []string
	Environment  []string
}

type Project struct {
	Packages []string
	Exclude  []string
}

type Execution struct {
	BuildTags      []string
	TestBinaryArgs []string
	Environment    []string
	Timeout        time.Duration
	Jobs           int
}

type Cache struct {
	MaxBytes int64
	TTL      time.Duration
}

type Acceptance struct {
	ID      string
	Reason  string
	Expires time.Time
	Owner   string
	Ticket  string
}

type Config struct {
	Version    int
	Contract   string
	Project    Project
	Execution  Execution
	Cache      Cache
	Resources  map[string]Resource
	Generation Generation
	Acceptance []Acceptance
}

type rawConfig struct {
	Version    int                    `toml:"version"`
	Contract   string                 `toml:"contract"`
	Project    rawProject             `toml:"project"`
	Execution  rawExecution           `toml:"execution"`
	Cache      rawCache               `toml:"cache"`
	Resources  map[string]rawResource `toml:"resources"`
	Generation rawGeneration          `toml:"generation"`
	Acceptance []rawAcceptance        `toml:"acceptance"`
}

type rawProject struct {
	Packages []string `toml:"packages"`
	Exclude  []string `toml:"exclude"`
}

type rawExecution struct {
	BuildTags      []string `toml:"build_tags"`
	TestBinaryArgs []string `toml:"test_binary_args"`
	Environment    []string `toml:"environment"`
	Timeout        string   `toml:"timeout"`
	Jobs           int      `toml:"jobs"`
}

type rawCache struct {
	MaxBytes int64  `toml:"max_bytes"`
	TTL      string `toml:"ttl"`
}

type rawResource struct {
	Command     []string `toml:"command"`
	Timeout     string   `toml:"timeout"`
	Shared      bool     `toml:"shared"`
	Exclusive   bool     `toml:"exclusive"`
	Environment []string `toml:"environment"`
}

type rawGeneration struct {
	Command      []string `toml:"command"`
	AllowedPaths []string `toml:"allowed_paths"`
	Environment  []string `toml:"environment"`
}

type rawAcceptance struct {
	ID      string `toml:"id"`
	Reason  string `toml:"reason"`
	Expires string `toml:"expires"`
	Owner   string `toml:"owner,omitempty"`
	Ticket  string `toml:"ticket,omitempty"`
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
	return Config{
		Version: 1, Contract: "standard-v1", Project: Project{Packages: []string{"./..."}},
		Execution: Execution{Timeout: 10 * time.Minute}, Cache: Cache{MaxBytes: 5 << 30, TTL: 30 * 24 * time.Hour},
		Resources: map[string]Resource{},
	}
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
	projectPackages := slices.Clone(raw.Project.Packages)
	if len(projectPackages) == 0 {
		projectPackages = []string{"./..."}
	}
	if err := validateProjectExcludes(raw.Project.Exclude); err != nil {
		return Config{}, err
	}
	executionTimeout := 10 * time.Minute
	if raw.Execution.Timeout != "" {
		parsed, parseErr := time.ParseDuration(raw.Execution.Timeout)
		if parseErr != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("goatest: execution timeout %q must be a positive duration", raw.Execution.Timeout)
		}
		executionTimeout = parsed
	}
	if raw.Execution.Jobs < 0 {
		return Config{}, errors.New("goatest: execution jobs must not be negative")
	}
	testBinaryArgs, err := testargs.Normalize(raw.Execution.TestBinaryArgs)
	if err != nil {
		return Config{}, fmt.Errorf("goatest: execution: %w", err)
	}
	if err := validateEnvironmentNames("execution", raw.Execution.Environment); err != nil {
		return Config{}, err
	}
	if err := validateEnvironmentNames("generation", raw.Generation.Environment); err != nil {
		return Config{}, err
	}
	cacheMaxBytes := raw.Cache.MaxBytes
	if cacheMaxBytes == 0 {
		cacheMaxBytes = 5 << 30
	}
	if cacheMaxBytes < 0 {
		return Config{}, errors.New("goatest: cache max_bytes must not be negative")
	}
	cacheTTL := 30 * 24 * time.Hour
	if raw.Cache.TTL != "" {
		parsed, parseErr := time.ParseDuration(raw.Cache.TTL)
		if parseErr != nil || parsed <= 0 {
			return Config{}, fmt.Errorf("goatest: cache ttl %q must be a positive duration", raw.Cache.TTL)
		}
		cacheTTL = parsed
	}
	result := Config{
		Version:  1,
		Contract: raw.Contract,
		Project:  Project{Packages: projectPackages, Exclude: slices.Clone(raw.Project.Exclude)},
		Execution: Execution{
			BuildTags: slices.Clone(raw.Execution.BuildTags), TestBinaryArgs: testBinaryArgs,
			Environment: slices.Clone(raw.Execution.Environment), Timeout: executionTimeout, Jobs: raw.Execution.Jobs,
		},
		Cache:     Cache{MaxBytes: cacheMaxBytes, TTL: cacheTTL},
		Resources: make(map[string]Resource, len(raw.Resources)),
		Generation: Generation{
			Command:      append([]string(nil), raw.Generation.Command...),
			AllowedPaths: append([]string(nil), raw.Generation.AllowedPaths...),
			Environment:  append([]string(nil), raw.Generation.Environment...),
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
		if err := validateEnvironmentNames(fmt.Sprintf("resource %q", name), rawResource.Environment); err != nil {
			return Config{}, err
		}
		result.Resources[name] = Resource{
			Command:     append([]string(nil), rawResource.Command...),
			Timeout:     timeout,
			Shared:      rawResource.Shared,
			Exclusive:   rawResource.Exclusive,
			Environment: slices.Clone(rawResource.Environment),
		}
	}
	acceptanceIDs := make(map[string]struct{}, len(raw.Acceptance))
	for _, rawAcceptance := range raw.Acceptance {
		if strings.TrimSpace(rawAcceptance.ID) == "" || strings.TrimSpace(rawAcceptance.Reason) == "" || rawAcceptance.Expires == "" {
			return Config{}, errors.New("goatest: every acceptance requires id, reason, and expires")
		}
		if rawAcceptance.ID != strings.TrimSpace(rawAcceptance.ID) || rawAcceptance.Reason != strings.TrimSpace(rawAcceptance.Reason) ||
			rawAcceptance.Owner != strings.TrimSpace(rawAcceptance.Owner) || rawAcceptance.Ticket != strings.TrimSpace(rawAcceptance.Ticket) {
			return Config{}, fmt.Errorf("goatest: acceptance %q contains leading or trailing whitespace", rawAcceptance.ID)
		}
		if _, duplicate := acceptanceIDs[rawAcceptance.ID]; duplicate {
			return Config{}, fmt.Errorf("goatest: acceptance %q is duplicated", rawAcceptance.ID)
		}
		acceptanceIDs[rawAcceptance.ID] = struct{}{}
		expires, parseErr := time.Parse(time.RFC3339, rawAcceptance.Expires)
		if parseErr != nil {
			return Config{}, fmt.Errorf("goatest: acceptance %q expiry: %w", rawAcceptance.ID, parseErr)
		}
		result.Acceptance = append(result.Acceptance, Acceptance{
			ID: rawAcceptance.ID, Reason: rawAcceptance.Reason, Expires: expires,
			Owner: rawAcceptance.Owner, Ticket: rawAcceptance.Ticket,
		})
	}
	return result, nil
}

// initTemplate is what Init writes: the two active keys the strict defaults
// need, and every other section as commented guidance, so that turning a
// setting on is uncommenting a line rather than hunting documentation. The
// strict parser ignores comments, so loading this file yields exactly the
// defaults.
const initTemplate = `# goatest strict configuration. version is required; every other key is
# optional and unknown keys are refused. The commented values below are the
# defaults or examples; uncomment a line to change a setting.
version = 1

# contract selects the fault model: "standard-v1" or "deep-v1".
contract = "standard-v1"

# [project]
# packages = ["./..."]        # Go package patterns the assurance covers
# exclude = ["generated/**"]  # path patterns recorded as explicit limitations

# [execution]
# build_tags = ["integration"]   # tags applied to every build and test
# test_binary_args = ["-short"]  # only -short and -test.parallel are accepted
# environment = ["FEATURE_MODE"] # variable names tests may read; values never reach reports
# timeout = "10m"                # budget for each executed command
# jobs = 4                       # mutation workers (bounded at 4)

# [cache]
# max_bytes = 5368709120 # 5 GiB evidence cache budget
# ttl = "720h"           # entries older than this are collected

# One table per managed test resource; docs/protocols.md defines the provider
# contract. Tests declare a capability with goatest.Integration("postgres") or
# a //goatest:resources directive.
# [resources.postgres]
# command = ["./tools/postgres-provider"]
# timeout = "30s"
# shared = true                    # one instance serves every target
# exclusive = true                 # serializes access instead; at most one of shared and exclusive
# environment = ["POSTGRES_IMAGE"] # variable names forwarded to the provider

# [generation]
# command = ["./tools/test-generator"] # candidate generator; docs/protocols.md defines the contract
# allowed_paths = ["**/*_test.go", "**/testdata/fuzz/**"]
# environment = ["GENERATOR_TOKEN"]

# Explicit, expiring finding acceptances, normally written by 'goatest accept'.
# [[acceptance]]
# id = "0123456789abcdef"
# reason = "reviewed equivalent boundary"
# expires = "2026-12-31T00:00:00Z"
# owner = "quality-team"
# ticket = "QA-123"
`

// Init creates the annotated strict v1 configuration and never overwrites an
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
	data := []byte(initTemplate)
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
	if acceptance.ID != strings.TrimSpace(acceptance.ID) || acceptance.Reason != strings.TrimSpace(acceptance.Reason) ||
		acceptance.Owner != strings.TrimSpace(acceptance.Owner) || acceptance.Ticket != strings.TrimSpace(acceptance.Ticket) {
		return errors.New("goatest: acceptance fields must not have leading or trailing whitespace")
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
		Project: rawProject{Packages: slices.Clone(input.Project.Packages), Exclude: slices.Clone(input.Project.Exclude)},
		Execution: rawExecution{
			BuildTags: slices.Clone(input.Execution.BuildTags), TestBinaryArgs: slices.Clone(input.Execution.TestBinaryArgs),
			Environment: slices.Clone(input.Execution.Environment), Timeout: input.Execution.Timeout.String(), Jobs: input.Execution.Jobs,
		},
		Cache:     rawCache{MaxBytes: input.Cache.MaxBytes, TTL: input.Cache.TTL.String()},
		Resources: make(map[string]rawResource, len(input.Resources)),
		Generation: rawGeneration{
			Command: slices.Clone(input.Generation.Command), AllowedPaths: slices.Clone(input.Generation.AllowedPaths),
			Environment: slices.Clone(input.Generation.Environment),
		},
	}
	for name, resource := range input.Resources {
		raw.Resources[name] = rawResource{
			Command: slices.Clone(resource.Command), Timeout: resource.Timeout.String(),
			Shared: resource.Shared, Exclusive: resource.Exclusive, Environment: slices.Clone(resource.Environment),
		}
	}
	for _, acceptance := range input.Acceptance {
		raw.Acceptance = append(raw.Acceptance, rawAcceptance{
			ID: acceptance.ID, Reason: acceptance.Reason, Expires: acceptance.Expires.UTC().Format(time.RFC3339),
			Owner: acceptance.Owner, Ticket: acceptance.Ticket,
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
		backupPath := temporaryPath + ".backup"
		backupErr := renameConfigFile(path, backupPath)
		if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
			return errors.Join(err, backupErr)
		}
		retryErr := renameConfigFile(temporaryPath, path)
		if retryErr != nil {
			if backupErr == nil {
				if restoreErr := renameConfigFile(backupPath, path); restoreErr != nil {
					return errors.Join(err, retryErr, fmt.Errorf("goatest: restore previous config from %s: %w", backupPath, restoreErr))
				}
			}
			return errors.Join(err, retryErr)
		}
		if backupErr == nil {
			_ = removeConfigFile(backupPath)
		}
	}
	return nil
}

func validateEnvironmentNames(section string, names []string) error {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !validEnvironmentName(name) {
			return fmt.Errorf("goatest: %s environment name %q is invalid", section, name)
		}
		canonical := strings.ToUpper(name)
		if _, exists := seen[canonical]; exists {
			return fmt.Errorf("goatest: %s environment name %q is duplicated", section, name)
		}
		seen[canonical] = struct{}{}
	}
	return nil
}

func validEnvironmentName(name string) bool {
	if name == "" || !validEnvironmentNameStart(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		if !validEnvironmentNameCharacter(name[index]) {
			return false
		}
	}
	return true
}

func validEnvironmentNameStart(character byte) bool {
	return character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
}

func validEnvironmentNameCharacter(character byte) bool {
	return validEnvironmentNameStart(character) || character >= '0' && character <= '9'
}

func validateProjectExcludes(patterns []string) error {
	seen := make(map[string]struct{}, len(patterns))
	for _, pattern := range patterns {
		if pattern == "" || strings.TrimSpace(pattern) != pattern || strings.HasPrefix(pattern, "/") ||
			strings.ContainsAny(pattern, "\\:\x00") {
			return fmt.Errorf("goatest: project exclude pattern %q is invalid", pattern)
		}
		for _, part := range strings.Split(pattern, "/") {
			if part == ".." {
				return fmt.Errorf("goatest: project exclude pattern %q escapes the project", pattern)
			}
		}
		if _, err := path.Match(pattern, "probe"); err != nil {
			return fmt.Errorf("goatest: project exclude pattern %q is invalid: %w", pattern, err)
		}
		if _, duplicate := seen[pattern]; duplicate {
			return fmt.Errorf("goatest: project exclude pattern %q is duplicated", pattern)
		}
		seen[pattern] = struct{}{}
	}
	return nil
}
