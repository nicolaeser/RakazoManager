package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

var ErrInterrupted = errors.New("interrupted")

type Request struct {
	Name           string
	Args           []string
	Directory      string
	Environment    []string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	Capture        bool
	InheritSignals bool
}

type Result struct {
	Stdout string
	Stderr string
}

type Runner interface {
	Run(context.Context, Request) (Result, error)
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, request Request) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	executable, err := exec.LookPath(request.Name)
	if err != nil {
		return Result{}, fmt.Errorf("%s is not installed or not on PATH", request.Name)
	}
	cmd := exec.Command(executable, request.Args...)
	cmd.Dir = request.Directory
	if !request.InheritSignals {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	if len(request.Environment) > 0 {
		cmd.Env = append(os.Environ(), request.Environment...)
	}
	cmd.Stdin = request.Stdin

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if request.Capture {

		if request.Stdout != nil {
			cmd.Stdout = io.MultiWriter(request.Stdout, &stdout)
		} else {
			cmd.Stdout = &stdout
		}
		if request.Stderr != nil {
			cmd.Stderr = io.MultiWriter(request.Stderr, &stderr)
		} else {
			cmd.Stderr = &stderr
		}
	} else {

		cmd.Stdout = request.Stdout
		cmd.Stderr = request.Stderr
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("%s %s: %s", request.Name, strings.Join(request.Args, " "), err.Error())
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
	}()

	var runErr error
	select {
	case runErr = <-waitDone:
	case <-ctx.Done():
		if request.InheritSignals {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGTERM)
			}
		} else {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGINT)
		}
		timer := time.NewTimer(3 * time.Second)
		select {
		case runErr = <-waitDone:
			timer.Stop()
		case <-timer.C:
			if request.InheritSignals {
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
			} else {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			runErr = <-waitDone
		}
		if ctx.Err() != nil {
			return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ctx.Err()
		}
	}

	if runErr != nil {
		if isInterrupted(runErr) {
			return Result{Stdout: stdout.String(), Stderr: stderr.String()}, ErrInterrupted
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = runErr.Error()
		}
		return Result{Stdout: stdout.String(), Stderr: stderr.String()}, fmt.Errorf("%s %s: %s", request.Name, strings.Join(request.Args, " "), detail)
	}
	return Result{Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

func isInterrupted(err error) bool {
	if errors.Is(err, ErrInterrupted) || errors.Is(err, context.Canceled) {
		return true
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	if status.Signaled() {
		switch status.Signal() {
		case syscall.SIGINT, syscall.SIGTERM:
			return true
		}
	}
	return status.ExitStatus() == 130
}

func IsInterrupted(err error) bool {
	return err != nil && (errors.Is(err, ErrInterrupted) || errors.Is(err, context.Canceled) || isInterrupted(err))
}
