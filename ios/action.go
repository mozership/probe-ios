// Package ios is a Probe action that drives an app on an iOS simulator
// through an XCUITest runner, which it builds with the Mac's Xcode the
// first time a step needs it.
package ios

import (
	"context"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe-ios/internal/build"
	"github.com/mozership/probe-ios/internal/device"
	"github.com/mozership/probe-ios/internal/runner"
	"github.com/mozership/probe-ios/internal/runnersrc"
	"github.com/mozership/probe-ios/internal/xcode"
	"github.com/mozership/probe/actionrpc"
)

// CacheDirEnv overrides the directory the runner is built in and the
// runners of a run are kept in, such as for a CI cache.
const CacheDirEnv = "PROBE_IOS_CACHE_DIR"

// Server is the ios action, as Probe calls it.
type Server struct {
	log hclog.Logger
	// ctx is done once Probe stops the step, so that a build or a runner
	// that is starting is stopped with it.
	ctx context.Context
}

var (
	_ actionrpc.StepAction = (*Server)(nil)
	_ actionrpc.Cleaner    = (*Server)(nil)
)

// Serve serves the action to Probe.
func Serve() {
	// Probe sends SIGTERM to a step it stops, such as for its timeout, and
	// kills it a moment later. The signal stops what the step started,
	// such as xcodebuild, which would run on without it.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		time.Sleep(time.Second)
		os.Exit(1)
	}()
	actionrpc.Serve(func(log hclog.Logger) actionrpc.Action {
		return &Server{log: log, ctx: ctx}
	})
}

// Run runs a step without a run, which has no runner to keep; Probe calls
// RunStep.
func (a *Server) Run(with map[string]any) (map[string]any, error) {
	ret, _, err := a.RunStep(actionrpc.Call{With: with})
	return ret, err
}

// RunStep takes the actions of a step on the simulator.
func (a *Server) RunStep(call actionrpc.Call) (map[string]any, map[string]any, error) {
	actionrpc.LogParams(a.log, "received ios action request", call.With)
	start := time.Now()
	req, err := parseReq(call.With)
	if err != nil {
		return failed(start, &Req{}, Res{}, err)
	}
	res := Res{Results: map[string]string{}, FilePaths: map[string]string{}}
	if err := a.run(call, req, &res); err != nil {
		return failed(start, req, res, err)
	}
	ret, err := actionrpc.EncodeResult(Result{Req: *req, Res: res, RT: time.Since(start)})
	actionrpc.LogOutcome(a.log, "ios request", ret, err)
	return ret, nil, err
}

func (a *Server) run(call actionrpc.Call, req *Req, res *Res) error {
	ctx := a.context()
	t, err := xcode.Detect(ctx, xcode.Exec)
	if err != nil {
		return err
	}
	devices, err := device.List(ctx, xcode.Exec)
	if err != nil {
		return err
	}
	d, err := device.Resolve(devices, req.Device)
	if err != nil {
		return err
	}
	res.Device = d.UDID

	root, err := cacheDir()
	if err != nil {
		return err
	}
	arch, err := build.Arch(runtime.GOARCH)
	if err != nil {
		return err
	}
	r, err := build.Ensure(ctx, build.Options{
		Root:      filepath.Join(root, "runner"),
		Source:    runnersrc.FS,
		Project:   runnersrc.Project,
		Scheme:    runnersrc.Scheme,
		Toolchain: t,
		Arch:      arch,
		Log:       a.log,
		Exec:      build.Command,
	})
	if err != nil {
		return err
	}

	c, release, err := runner.Open(ctx, runner.Options{
		Root:      filepath.Join(root, "sessions"),
		RunID:     runID(call),
		UDID:      d.UDID,
		XCTestRun: r.XCTestRun,
		Test:      runnersrc.Test,
		Log:       a.log,
	})
	if err != nil {
		return err
	}
	defer release()
	return runActions(ctx, c, req, res)
}

// Cleanup stops the runners the run's steps started.
func (a *Server) Cleanup(call actionrpc.CleanupCall) error {
	root, err := cacheDir()
	if err != nil {
		return err
	}
	return runner.Cleanup(filepath.Join(root, "sessions"), call.RunID)
}

func (a *Server) context() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// runID is the run the step is of. A step called without one, as Run is,
// gets a run of its own, whose runner nothing cleans up; Probe always
// gives one.
func runID(call actionrpc.Call) string {
	if call.Step.RunID != "" {
		return call.Step.RunID
	}
	return "pid-" + time.Now().Format("20060102T150405.000000000")
}

func cacheDir() (string, error) {
	if dir := os.Getenv(CacheDirEnv); dir != "" {
		return dir, nil
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "probe-ios"), nil
}

func failed(start time.Time, req *Req, res Res, err error) (map[string]any, map[string]any, error) {
	if res.Results == nil {
		res.Results = map[string]string{}
	}
	if res.FilePaths == nil {
		res.FilePaths = map[string]string{}
	}
	res.Code = 1
	ret, encErr := actionrpc.EncodeResult(Result{Req: *req, Res: res, RT: time.Since(start), Status: 1})
	if encErr != nil {
		return map[string]any{}, nil, encErr
	}
	return ret, nil, err
}
