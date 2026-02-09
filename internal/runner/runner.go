package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/rodrwan/slack-codex/internal/observability"
)

var ErrNeedsInput = errors.New("runner appears to be waiting for input")

const (
	scannerBufferInitial = 64 * 1024
	scannerBufferMax     = 8 * 1024 * 1024
)

type Spec struct {
	WorkspacePath     string
	Command           string
	Env               []string
	InactivityTimeout time.Duration
	ExecutionTimeout  time.Duration
}

type Result struct {
	CombinedOutput string
	NeedsInput     bool
	ExitErr        error
}

type Runner struct{}

func New() *Runner { return &Runner{} }

func (r *Runner) Run(ctx context.Context, spec Spec, onLine func(line string)) (Result, error) {
	start := time.Now()
	if spec.Command == "" {
		return Result{}, fmt.Errorf("runner command is empty")
	}
	if spec.ExecutionTimeout <= 0 {
		spec.ExecutionTimeout = 30 * time.Minute
	}
	if spec.InactivityTimeout <= 0 {
		spec.InactivityTimeout = 45 * time.Second
	}
	observability.Info("runner_start", observability.Fields{
		"workspace_path":     spec.WorkspacePath,
		"command_hash":       observability.HashText(spec.Command),
		"command_len":        len(spec.Command),
		"env_count":          len(spec.Env),
		"inactivity_timeout": spec.InactivityTimeout.String(),
		"execution_timeout":  spec.ExecutionTimeout.String(),
	})

	timeoutCtx, cancel := context.WithTimeout(ctx, spec.ExecutionTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "/bin/sh", "-lc", spec.Command)
	cmd.Dir = spec.WorkspacePath
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), spec.Env...)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("start command: %w", err)
	}

	var (
		buf      bytes.Buffer
		mu       sync.Mutex
		lastOut  = time.Now()
		copyDone = make(chan struct{}, 2)
	)

	copyStream := func(sc *bufio.Scanner, prefix string) {
		sc.Buffer(make([]byte, scannerBufferInitial), scannerBufferMax)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			lastOut = time.Now()
			buf.WriteString(prefix)
			buf.WriteString(line)
			buf.WriteByte('\n')
			mu.Unlock()
			if onLine != nil {
				onLine(prefix + line)
			}
		}
		if err := sc.Err(); err != nil {
			mu.Lock()
			buf.WriteString(prefix)
			buf.WriteString("scanner_error: ")
			buf.WriteString(err.Error())
			buf.WriteByte('\n')
			mu.Unlock()
			if onLine != nil {
				onLine(prefix + "scanner_error: " + err.Error())
			}
		}
		copyDone <- struct{}{}
	}

	go copyStream(bufio.NewScanner(stdout), "")
	go copyStream(bufio.NewScanner(stderr), "[stderr] ")

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case err := <-waitCh:
			<-copyDone
			<-copyDone
			mu.Lock()
			out := buf.String()
			mu.Unlock()
			if err != nil {
				observability.Warn("runner_wait_exit_error", observability.Fields{
					"duration_ms": time.Since(start).Milliseconds(),
					"error":       err.Error(),
					"output_len":  len(out),
				})
				return Result{CombinedOutput: out, ExitErr: err}, nil
			}
			observability.Info("runner_wait_ok", observability.Fields{
				"duration_ms": time.Since(start).Milliseconds(),
				"output_len":  len(out),
			})
			return Result{CombinedOutput: out}, nil
		case <-ticker.C:
			mu.Lock()
			idle := time.Since(lastOut)
			mu.Unlock()
			if idle > spec.InactivityTimeout {
				_ = cmd.Process.Kill()
				<-waitCh
				<-copyDone
				<-copyDone
				mu.Lock()
				out := buf.String()
				mu.Unlock()
				observability.Warn("runner_needs_input", observability.Fields{
					"duration_ms": time.Since(start).Milliseconds(),
					"idle_ms":     idle.Milliseconds(),
					"output_len":  len(out),
				})
				return Result{CombinedOutput: out, NeedsInput: true}, ErrNeedsInput
			}
		case <-timeoutCtx.Done():
			_ = cmd.Process.Kill()
			<-waitCh
			<-copyDone
			<-copyDone
			mu.Lock()
			out := buf.String()
			mu.Unlock()
			observability.Error("runner_execution_timeout", observability.Fields{
				"duration_ms": time.Since(start).Milliseconds(),
				"output_len":  len(out),
				"error":       timeoutCtx.Err().Error(),
			})
			return Result{CombinedOutput: out, ExitErr: timeoutCtx.Err()}, timeoutCtx.Err()
		}
	}
}
