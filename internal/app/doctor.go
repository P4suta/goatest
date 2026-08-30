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
	"github.com/P4suta/goatest/internal/processtree"
	"github.com/P4suta/goatest/internal/report"
)

const doctorOutputLimit = 1 << 20

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
	goBinary := service.GoBinary
	if goBinary == "" {
		goBinary = "go"
	}
	environment := service.Environment
	if environment == nil {
		environment = os.Environ()
	}
	offline := withEnvironment(environment, map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GOTELEMETRY": "off", "GOTOOLCHAIN": "local",
	})
	version, err := doctorCommand(ctx, root, offline, 30*time.Second, goBinary, "version")
	if err != nil {
		return doctorFailure(result, "toolchain", "go-version", err), nil
	}
	result.Toolchain = report.Toolchain{Go: strings.TrimSpace(version), Goatest: "local", OS: runtime.GOOS, Arch: runtime.GOARCH}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "go-version", Status: "ready", Detail: strings.TrimSpace(version)})
	envOutput, err := doctorCommand(ctx, root, offline, 30*time.Second, goBinary, "env", "GOMOD", "GOWORK", "CGO_ENABLED", "GOOS", "GOARCH")
	if err != nil {
		return doctorFailure(result, "toolchain", "go-env", err), nil
	}
	values := splitDoctorLines(envOutput, 5)
	if values[0] == "" || values[0] == os.DevNull {
		return doctorFailure(result, "workspace", "module", errors.New("go env GOMOD does not identify a module")), nil
	}
	result.Evidence = append(result.Evidence,
		report.Evidence{Kind: "doctor", ID: "module", Status: "ready", Detail: filepath.ToSlash(values[0])},
		report.Evidence{Kind: "doctor", ID: "workspace", Status: doctorOptionalStatus(values[1]), Detail: filepath.ToSlash(values[1])},
		report.Evidence{Kind: "doctor", ID: "cgo", Status: doctorBooleanStatus(values[2] == "1"), Detail: "CGO_ENABLED=" + values[2]},
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
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "race-detector", Status: "ready", Detail: values[3] + "/" + values[4]})
	if git, err := doctorCommand(ctx, root, environment, 30*time.Second, "git", "rev-parse", "--is-inside-work-tree"); err != nil || strings.TrimSpace(git) != "true" {
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
	free, err := diskFreeBytes(root)
	if err != nil {
		return doctorFailure(result, "filesystem", "disk", err), nil
	}
	result.Evidence = append(result.Evidence, report.Evidence{Kind: "doctor", ID: "disk", Status: "ready", Detail: fmt.Sprintf("free-bytes=%d", free)})
	if free < 512<<20 {
		return doctorFailure(result, "filesystem", "disk-capacity", fmt.Errorf("only %d bytes are free", free)), nil
	}
	return result, nil
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
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
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

func doctorCommand(ctx context.Context, root string, environment []string, timeout time.Duration, name string, arguments ...string) (string, error) {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.Command(name, arguments...)
	command.Dir = root
	command.Env = slices.Clone(environment)
	var output limitedDoctorBuffer
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
	truncated bool
}

func (buffer *limitedDoctorBuffer) Write(data []byte) (int, error) {
	original := len(data)
	remaining := doctorOutputLimit - buffer.Len()
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
		result += "\n[goatest: doctor output truncated]"
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
