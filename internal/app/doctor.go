// SPDX-FileCopyrightText: 2026 goatest contributors
// SPDX-License-Identifier: MIT OR Apache-2.0

package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/P4suta/goatest/internal/cli"
	"github.com/P4suta/goatest/internal/config"
	"github.com/P4suta/goatest/internal/filemode"
	goanalysis "github.com/P4suta/goatest/internal/golang"
	"github.com/P4suta/goatest/internal/mutationbridge"
	"github.com/P4suta/goatest/internal/processtree"
	"github.com/P4suta/goatest/internal/report"
)

const (
	doctorOutputLimit           = 1 << 20
	doctorListingLimit          = 64 << 20
	doctorNameSampleSize        = 5
	doctorTruncationNotice      = "[goatest: doctor output truncated]"
	doctorQuickCommandTimeout   = 30 * time.Second
	doctorDefaultCommandTimeout = 10 * time.Minute
	doctorMinimumFreeBytes      = 512 << 20
	doctorGoEnvironmentFields   = 5
)

const (
	doctorGoModField = iota
	doctorGoWorkField
	doctorCGOEnabledField
	doctorGOOSField
	doctorGOARCHField
)

type doctorProcessTree interface {
	Kill() error
	Close() error
}

var startDoctorProcess = func(command *exec.Cmd) (doctorProcessTree, error) {
	return processtree.Start(command)
}

func (service Service) doctor(ctx context.Context, root string) (report.Report, error) {
	result := report.Report{Schema: report.SchemaV1, RunKind: report.RunOperation, Verdict: report.VerdictCompleted}
	loaded, err := config.Load(root)
	if err != nil {
		return doctorFailure(result, "config", config.FileName, err), nil
	}
	result.Contract = loaded.Contract
	digest, digestErr := configurationDigest(root, cli.Request{})
	result.Configuration.Digest = digest
	if digestErr != nil {
		return doctorFailure(result, "config", "configuration-metadata", digestErr), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "config", Status: "ready", Detail: "strict config v1"})
	profile, profileErr := mutationbridge.Profile(loaded.Contract)
	if profileErr != nil {
		return doctorFailure(result, "mutation", "mutation-profile", profileErr), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "mutation-profile", Status: "ready", Detail: profile})
	goBinary := service.GoBinary
	if goBinary == "" {
		goBinary = "go"
	}
	environment := service.environment()
	offline := withEnvironment(environment, map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTELEMETRY": "off", "GOTOOLCHAIN": "local",
	})
	version, err := doctorCommand(ctx, root, offline, doctorQuickCommandTimeout, goBinary, "version")
	if err != nil {
		return doctorFailure(result, "toolchain", "go-version", err), nil
	}
	result.Toolchain = report.Toolchain{Go: strings.TrimSpace(version), Goatest: "local", OS: runtime.GOOS, Arch: runtime.GOARCH}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "go-version", Status: "ready", Detail: strings.TrimSpace(version)})
	envOutput, err := doctorCommand(ctx, root, offline, doctorQuickCommandTimeout, goBinary, "env", "GOMOD", "GOWORK", "CGO_ENABLED", "GOOS", "GOARCH")
	if err != nil {
		return doctorFailure(result, "toolchain", "go-env", err), nil
	}
	values := splitDoctorLines(envOutput, doctorGoEnvironmentFields)
	if values[doctorGoModField] == "" || values[doctorGoModField] == os.DevNull {
		return doctorFailure(result, "workspace", "module", errors.New("go env GOMOD does not identify a module")), nil
	}
	result.Evidence = append(result.Evidence,
		report.Evidence{Kind: "doctor", ID: "module", Status: "ready", Detail: filepath.ToSlash(values[doctorGoModField])},
		report.Evidence{Kind: "doctor", ID: "workspace", Status: doctorOptionalStatus(values[doctorGoWorkField]), Detail: filepath.ToSlash(values[doctorGoWorkField])},
		report.Evidence{Kind: "doctor", ID: "cgo", Status: doctorBooleanStatus(values[doctorCGOEnabledField] == "1"), Detail: "CGO_ENABLED=" + values[doctorCGOEnabledField]},
	)
	packages := slices.Clone(loaded.Project.Packages)
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	listArgs := []string{"list", "-deps", "-mod=readonly"}
	if len(loaded.Execution.BuildTags) != 0 {
		listArgs = append(listArgs, "-tags="+strings.Join(loaded.Execution.BuildTags, ","))
	}
	listArgs = append(listArgs, packages...)
	if _, err := doctorCommand(ctx, root, offline, loaded.Execution.Timeout, goBinary, listArgs...); err != nil {
		return doctorFailure(result, "dependency", "offline-dependencies", err), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "offline-dependencies", Status: "ready", Detail: strings.Join(packages, ",")})
	keys, keysErr := doctorBehaviourKeys(ctx, root, offline, loaded, goBinary, packages)
	if keysErr != nil {
		return doctorFailure(result, "dependency", "behaviour-keys", keysErr), nil
	}
	result.Evidence = append(result.Evidence, keys)
	raceArgs := []string{"test", "-run=^$", "-race"}
	if len(loaded.Execution.BuildTags) != 0 {
		raceArgs = append(raceArgs, "-tags="+strings.Join(loaded.Execution.BuildTags, ","))
	}
	raceArgs = append(raceArgs, packages...)
	if len(loaded.Execution.TestBinaryArgs) != 0 {
		raceArgs = append(raceArgs, "-args")
		raceArgs = append(raceArgs, loaded.Execution.TestBinaryArgs...)
	}
	if _, err := doctorCommand(ctx, root, offline, loaded.Execution.Timeout, goBinary, raceArgs...); err != nil {
		return doctorFailure(result, "race", "race-detector", err), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "race-detector", Status: "ready", Detail: values[doctorGOOSField] + "/" + values[doctorGOARCHField]})
	if git, err := doctorCommand(ctx, root, environment, doctorQuickCommandTimeout, "git", "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(git) != "true" {
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "git", Status: "unavailable", Detail: doctorErrorDetail(err)})
		result.Limitations = append(result.Limitations, report.Limitation{Code: "git-unavailable", Summary: "changeset scope and Git identity cannot be resolved"})
	} else {
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "git", Status: "ready"})
	}
	resourceNames := make([]string, 0, len(loaded.Resources))
	for name := range loaded.Resources {
		resourceNames = append(resourceNames, name)
	}
	slices.Sort(resourceNames)
	for _, name := range resourceNames {
		command := loaded.Resources[name].Command[0]
		if err := doctorProviderCommand(root, command); err != nil {
			return doctorFailure(result, "provider", "resource-provider-"+name, err), nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{
			Kind: "doctor", ID: "resource-provider-" + name, Status: "ready", Detail: command,
		})
	}
	if len(loaded.Generation.Command) != 0 {
		command := loaded.Generation.Command[0]
		if err := doctorProviderCommand(root, command); err != nil {
			return doctorFailure(result, "provider", "generation-provider", err), nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "generation-provider", Status: "ready", Detail: command})
	}

	for _, directory := range []string{".goatest", "reports"} {
		if err := probeWritableDirectory(service.doctorFilesystem, filepath.Join(root, directory)); err != nil {
			return doctorFailure(result, "filesystem", "writable-"+directory, err), nil
		}
		result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "writable-" + directory, Status: "ready", Detail: directory})
	}
	free, err := diskFreeBytes(root)
	if err != nil {
		return doctorFailure(result, "filesystem", "disk", err), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "disk", Status: "ready", Detail: fmt.Sprintf("free-bytes=%d", free)})
	if free < doctorMinimumFreeBytes {
		return doctorFailure(result, "filesystem", "disk-capacity", fmt.Errorf("only %d bytes are free", free)), nil
	}
	return result, nil
}

type doctorProbeFilesystem struct {
	Stat      func(string) (os.FileInfo, error)
	MkdirAll  func(string, os.FileMode) error
	WriteFile func(string, []byte, os.FileMode) error
	Remove    func(string) error
}

func (hooks doctorProbeFilesystem) resolved() doctorProbeFilesystem {
	if hooks.Stat == nil {
		hooks.Stat = os.Stat
	}
	if hooks.MkdirAll == nil {
		hooks.MkdirAll = os.MkdirAll
	}
	if hooks.WriteFile == nil {
		hooks.WriteFile = os.WriteFile
	}
	if hooks.Remove == nil {
		hooks.Remove = os.Remove
	}
	return hooks
}

func probeWritableDirectory(hooks doctorProbeFilesystem, directory string) error {
	hooks = hooks.resolved()
	created := false
	if _, err := hooks.Stat(directory); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := hooks.MkdirAll(directory, filemode.ReadableDirectory); err != nil {
			return err
		}
		created = true
	}
	probe := filepath.Join(directory, fmt.Sprintf(".goatest-doctor-probe-%d", os.Getpid()))
	if err := hooks.WriteFile(probe, []byte("goatest doctor writability probe"), filemode.ReadableFile); err != nil {
		if created {
			_ = hooks.Remove(directory)
		}
		return err
	}
	if err := hooks.Remove(probe); err != nil {
		return err
	}
	if created {
		if err := hooks.Remove(directory); err != nil {
			return err
		}
	}
	return nil
}

func doctorProviderCommand(root, name string) error {
	if !filepath.IsAbs(name) && !strings.ContainsAny(name, `/\`) {
		_, err := exec.LookPath(name)
		return err
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, filepath.FromSlash(name))
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("provider command %s is not a regular file", name)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&filemode.AnyExecute == 0 {
		return fmt.Errorf("provider command %s is not executable", name)
	}
	return nil
}

func doctorFailure(input report.Report, kind, id string, cause error) report.Report {
	input.Verdict = report.VerdictError
	input.Evidence = append(input.Evidence, report.Evidence{Kind: "doctor", ID: id, Status: "failed", Detail: cause.Error()})
	input.Findings = append(input.Findings, report.Finding{
		ID: report.FindingID("doctor", id), Kind: "doctor-" + kind, Summary: cause.Error(),
	})
	return input
}

func doctorBehaviourKeys(
	ctx context.Context,
	root string,
	environment []string,
	loaded config.Config,
	goBinary string,
	packages []string,
) (report.Evidence, error) {
	arguments := []string{"list", "-json", "-mod=readonly"}
	if len(loaded.Execution.BuildTags) != 0 {
		arguments = append(arguments, "-tags="+strings.Join(loaded.Execution.BuildTags, ","))
	}
	arguments = append(arguments, packages...)
	listing, err := doctorCommandWithLimit(ctx, root, environment, loaded.Execution.Timeout, doctorListingLimit, goBinary, arguments...)
	if err != nil {
		return report.Evidence{}, err
	}
	if strings.Contains(listing, doctorTruncationNotice) {
		return report.Evidence{}, errors.New("go list -json produced more output than goatest will read")
	}
	model, err := goanalysis.DecodePackages(strings.NewReader(listing))
	if err != nil {
		return report.Evidence{}, err
	}
	widened := make([]string, 0, len(model.Packages))
	for path, candidate := range goanalysis.RepositoryReadCandidates(root, model.Packages) {
		if candidate.Unobservable {
			widened = append(widened, path)
		}
	}
	slices.Sort(widened)
	if len(widened) == 0 {
		return report.Evidence{
			Kind: "doctor", ID: "behaviour-keys", Status: "ready",
			Detail: fmt.Sprintf("%d packages, none statically widened", len(model.Packages)),
		}, nil
	}
	return report.Evidence{
		Kind: "doctor", ID: "behaviour-keys", Status: "widened",
		Detail: fmt.Sprintf("%d of %d packages read past the test action log and key the whole tree: %s",
			len(widened), len(model.Packages), strings.Join(doctorNameSample(widened), ", ")),
	}, nil
}

func doctorNameSample(names []string) []string {
	if len(names) <= doctorNameSampleSize {
		return names
	}
	sample := slices.Clone(names[:doctorNameSampleSize])
	return append(sample, fmt.Sprintf("and %d more", len(names)-doctorNameSampleSize))
}

func doctorCommand(ctx context.Context, root string, environment []string, timeout time.Duration, name string, arguments ...string) (string, error) {
	return doctorCommandWithLimit(ctx, root, environment, timeout, doctorOutputLimit, name, arguments...)
}

func doctorCommandWithLimit(
	ctx context.Context,
	root string,
	environment []string,
	timeout time.Duration,
	limit int,
	name string,
	arguments ...string,
) (string, error) {
	if timeout <= 0 {
		timeout = doctorDefaultCommandTimeout
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.Command(name, arguments...)
	command.Dir = root
	command.Env = slices.Clone(environment)
	output := limitedDoctorBuffer{limit: limit}
	command.Stdout, command.Stderr = &output, &output
	tree, err := startDoctorProcess(command)
	if err != nil {
		return output.String(), fmt.Errorf("%s %s: %w", name, strings.Join(arguments, " "), err)
	}
	wait := make(chan error, 1)
	go func() { wait <- command.Wait() }()
	select {
	case runErr := <-wait:
		closeErr := tree.Close()
		if runErr != nil || closeErr != nil {
			return output.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(arguments, " "), errors.Join(runErr, closeErr), strings.TrimSpace(output.String()))
		}
	case <-commandContext.Done():
		killErr := tree.Kill()
		runErr := <-wait
		closeErr := tree.Close()
		return output.String(), fmt.Errorf("%s %s timed out: %w: %s", name, strings.Join(arguments, " "),
			errors.Join(commandContext.Err(), killErr, runErr, closeErr), strings.TrimSpace(output.String()))
	}
	return output.String(), nil
}

type limitedDoctorBuffer struct {
	bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *limitedDoctorBuffer) Write(data []byte) (int, error) {
	original := len(data)
	limit := buffer.limit
	if limit <= 0 {
		limit = doctorOutputLimit
	}
	remaining := limit - buffer.Len()
	if remaining <= 0 {
		buffer.truncated = true
		return original, nil
	}
	if len(data) > remaining {
		data = data[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.Buffer.Write(data)
	return original, nil
}

func (buffer *limitedDoctorBuffer) String() string {
	result := buffer.Buffer.String()
	if buffer.truncated {
		result += "\n" + doctorTruncationNotice
	}
	return result
}

func withEnvironment(base []string, overrides map[string]string) []string {
	values := make(map[string]string)
	names := make(map[string]string)
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key != "" {
			upper := strings.ToUpper(key)
			values[upper], names[upper] = value, key
		}
	}
	for key, value := range overrides {
		upper := strings.ToUpper(key)
		values[upper], names[upper] = value, key
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, names[key]+"="+value)
	}
	slices.Sort(result)
	return result
}

func splitDoctorLines(output string, count int) []string {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(output), "\r\n", "\n"), "\n")
	result := make([]string, count)
	for index := range min(count, len(lines)) {
		result[index] = strings.TrimSpace(lines[index])
	}
	return result
}

func doctorOptionalStatus(value string) string {
	if value == "" || value == os.DevNull || strings.EqualFold(value, "off") {
		return "not-configured"
	}
	return "ready"
}

func doctorBooleanStatus(value bool) string {
	if value {
		return "ready"
	}
	return "disabled"
}

func doctorErrorDetail(err error) string {
	if err == nil {
		return "not a Git work tree"
	}
	return err.Error()
}
