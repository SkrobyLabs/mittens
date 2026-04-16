package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/SkrobyLabs/mittens/pkg/pool"
)

func (k *Kitchen) StartRuntime(ctx context.Context, brokerAddr, brokerToken, kitchenAddr string) (string, error) {
	if k == nil || k.pm == nil {
		return "", fmt.Errorf("kitchen pool manager not configured")
	}
	brokerToken = strings.TrimSpace(brokerToken)
	if k.workerBkr != nil && k.workerBkr.ln != nil {
		if strings.TrimSpace(kitchenAddr) == "" {
			return brokerAdvertiseAddr(k.workerBkr.ln.Addr().String()), nil
		}
		return strings.TrimSpace(kitchenAddr), nil
	}

	broker := NewWorkerBroker(k.pm, brokerAddr, brokerToken)
	if err := broker.Listen(); err != nil {
		return "", err
	}

	advertisedAddr := strings.TrimSpace(kitchenAddr)
	if advertisedAddr == "" {
		advertisedAddr = brokerAdvertiseAddr(broker.ln.Addr().String())
	}

	gitMgr, err := k.gitManager()
	if err != nil {
		_ = broker.Close()
		return "", err
	}
	scheduler := NewScheduler(k.pm, k.hostAPI, k.router, gitMgr, k.planStore, k.lineageMgr, k.cfg.Concurrency, "kitchen-"+k.project.Key)
	scheduler.failurePolicy = k.cfg.FailurePolicy
	scheduler.kitchenAddr = advertisedAddr
	scheduler.notify = k.sendNotify
	scheduler.activatePlan = k.ApprovePlan
	scheduler.activateWaitingPlans = k.activateWaitingPlans
	scheduler.keepDeadWorkers = k.keepDeadWorkers

	k.workerBkr = broker
	k.scheduler = scheduler

	go func() {
		<-ctx.Done()
		_ = broker.Close()
	}()
	go func() {
		if err := broker.Serve(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "kitchen worker broker: %v\n", err)
		}
	}()
	go k.forwardRuntimeEvents(ctx)
	go scheduler.Run(ctx)

	return advertisedAddr, nil
}

func (k *Kitchen) forwardRuntimeEvents(ctx context.Context) {
	if k == nil || k.hostAPI == nil {
		return
	}
	events, err := k.hostAPI.SubscribeEvents(ctx)
	if err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			if event.Type == "worker_recycled" && k.pm != nil && strings.TrimSpace(event.WorkerID) != "" {
				workerID := strings.TrimSpace(event.WorkerID)
				if err := k.pm.RequestRecycle(workerID); err != nil {
					fmt.Fprintf(os.Stderr, "kitchen runtime recycle request for worker %s: %v\n", workerID, err)
				}
			}
			k.sendNotify(runtimeEventNotification(event))
		}
	}
}

func runtimeEventNotification(event pool.RuntimeEvent) pool.Notification {
	id := strings.TrimSpace(event.WorkerID)
	if id == "" {
		id = strings.TrimSpace(event.AssignmentID)
	}
	message := strings.TrimSpace(event.Message)
	if strings.TrimSpace(event.AssignmentID) != "" {
		if message == "" {
			message = "assignment " + strings.TrimSpace(event.AssignmentID)
		} else {
			message = message + " [assignment " + strings.TrimSpace(event.AssignmentID) + "]"
		}
	}
	return pool.Notification{
		Type:    "runtime_" + strings.TrimSpace(event.Type),
		ID:      id,
		Message: message,
	}
}

func brokerAdvertiseAddr(listenAddr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listenAddr))
	if err != nil {
		return strings.TrimSpace(listenAddr)
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "0.0.0.0", "::", "127.0.0.1", "::1", "localhost":
		host = "host.docker.internal"
	}
	return net.JoinHostPort(host, port)
}
