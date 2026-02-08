package runner

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

var ErrNeedsInput = errors.New("runner appears to be waiting for input")

type Spec struct {
	WorkspacePath     string
	Command           string
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
	if spec.Command == "" {
		return Result{}, fmt.Errorf("runner command is empty")
	}
	if spec.ExecutionTimeout <= 0 {
		spec.ExecutionTimeout = 30 * time.Minute
	}
	if spec.InactivityTimeout <= 0 {
		spec.InactivityTimeout = 45 * time.Second
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, spec.ExecutionTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, "/bin/sh", "-lc", spec.Command)
	cmd.Dir = spec.WorkspacePath
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
				return Result{CombinedOutput: out, ExitErr: err}, nil
			}
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
			return Result{CombinedOutput: out, ExitErr: timeoutCtx.Err()}, timeoutCtx.Err()
		}
	}
}
