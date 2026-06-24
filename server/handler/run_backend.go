package handler

import (
	"context"
	"fmt"
	"log"

	"github.com/ifnodoraemon/openDataAnalysis/agent"
	"github.com/ifnodoraemon/openDataAnalysis/config"
	"github.com/ifnodoraemon/openDataAnalysis/session"
)

type runExecution struct {
	Context     context.Context
	Session     *session.Session
	UserInput   string
	RuntimeVars func() []agent.RuntimeContextBlock
	Emit        func(agent.WSEvent)
	OnDone      func()
}

type runBackend interface {
	Run(runExecution) error
}

type inProcessRunBackend struct{}

var runBackends = map[string]runBackend{
	"inprocess": inProcessRunBackend{},
}

func dispatchRunExecution(exec runExecution) error {
	backendName := configuredRunBackend()
	backend, ok := runBackends[backendName]
	if !ok {
		return fmt.Errorf("run backend %q is not implemented by this server binary; use RUN_BACKEND=inprocess for development or deploy a durable worker backend", backendName)
	}
	return backend.Run(exec)
}

func (inProcessRunBackend) Run(exec runExecution) error {
	if exec.Session == nil {
		return fmt.Errorf("cannot start run: session runtime is not initialized")
	}
	if exec.Session.Engine == nil {
		return fmt.Errorf("cannot start run: agent engine is not initialized")
	}
	go func() {
		if exec.OnDone != nil {
			defer exec.OnDone()
		}
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[PANIC RECOVER] inProcessRunBackend.Run: %v", r)
				if exec.Emit != nil {
					exec.Emit(agent.WSEvent{
						Type: agent.EventError,
						Data: agent.ErrorData{Message: fmt.Sprintf("Internal agent error: %v", r)},
					})
				}
			}
		}()
		exec.Session.Engine.Run(exec.Context, exec.UserInput, exec.RuntimeVars, exec.Emit)
	}()
	return nil
}

func configuredRunBackend() string {
	if config.Cfg == nil {
		return "inprocess"
	}
	if backend := config.NormalizeBackend(config.Cfg.RunBackend); backend != "" {
		return backend
	}
	return "inprocess"
}
