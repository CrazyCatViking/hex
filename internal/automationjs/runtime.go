// Package automationjs executes JavaScript in fresh, capability-restricted
// QuickJS WebAssembly instances. The only application interface is bounded JSON.
package automationjs

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:generate go run generate.go

//go:embed engine.wasm
var engine []byte

//go:embed bootstrap.js
var bootstrap string

const (
	MaxSourceBytes = 128 << 10
	MaxJSONBytes   = 1 << 20
	heapBytes      = 32 << 20
	memoryPages    = 1024 // 64 MiB, including the engine and C stack.
	stackBytes     = 1 << 20
	computeTime    = 5 * time.Second
)

// Invoke is the run-bound capability dispatcher. It never receives a site or
// identity from JavaScript. Host implementations must honor ctx and bound work.
type Invoke func(ctx context.Context, operation string, input json.RawMessage) (any, error)

type executionKey struct{}

type execution struct {
	invoke    Invoke
	cancel    context.CancelFunc
	remaining time.Duration
	resumed   time.Time
	timer     *time.Timer
	expired   atomic.Bool
}

type compiledRuntime struct {
	runtime wazero.Runtime
	module  wazero.CompiledModule
}

var sandboxSlots = make(chan struct{}, 4)
var validationSlots = make(chan struct{}, 1)

var compiledEngine = sync.OnceValues(func() (*compiledRuntime, error) {
	ctx := context.Background()
	config := wazero.NewRuntimeConfig().WithMemoryLimitPages(memoryPages).WithCloseOnContextDone(true)
	runtime := wazero.NewRuntimeWithConfig(ctx, config)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return nil, err
	}
	if _, err := runtime.NewHostModuleBuilder("hex").NewFunctionBuilder().WithFunc(invokeOperation).Export("invoke").Instantiate(ctx); err != nil {
		return nil, err
	}
	compiled, err := runtime.CompileModule(ctx, engine)
	return &compiledRuntime{runtime: runtime, module: compiled}, err
})

// Validate compiles and resolves source without executing any user code.
func Validate(ctx context.Context, source, filename string) error {
	_, err := execute(ctx, source, filename, nil, nil, true)
	return err
}

func Execute(ctx context.Context, source, filename string, metadata json.RawMessage, invoke Invoke) (json.RawMessage, error) {
	return execute(ctx, source, filename, metadata, invoke, false)
}

func execute(ctx context.Context, source, filename string, metadata json.RawMessage, invoke Invoke, validate bool) (json.RawMessage, error) {
	if len(source) == 0 || len(source) > MaxSourceBytes {
		return nil, fmt.Errorf("script source must be 1–%d bytes", MaxSourceBytes)
	}
	slots := sandboxSlots
	if validate {
		slots = validationSlots
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	compiled, err := compiledEngine()
	if err != nil {
		return nil, fmt.Errorf("initialize JavaScript engine: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &execution{invoke: invoke, cancel: cancel, remaining: computeTime, resumed: time.Now()}
	state.timer = time.AfterFunc(computeTime, func() {
		state.expired.Store(true)
		cancel()
	})
	defer state.timer.Stop()
	ctx = context.WithValue(ctx, executionKey{}, state)
	// No filesystem, arguments, environment, stdin or network are supplied.
	module, err := compiled.runtime.InstantiateModule(ctx, compiled.module, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize").WithSysWalltime())
	if err != nil {
		return nil, fmt.Errorf("instantiate JavaScript sandbox: %w", err)
	}
	defer module.Close(context.Background())
	result, err := module.ExportedFunction("initialize").Call(ctx, heapBytes, stackBytes)
	if err != nil || int32(result[0]) < 0 {
		return nil, executionError(ctx, state, err)
	}
	sourcePointer, err := putString(ctx, module, source)
	if err != nil {
		return nil, err
	}
	namePointer, err := putString(ctx, module, filename)
	if err != nil {
		return nil, err
	}
	if validate {
		result, err = module.ExportedFunction("validate_script").Call(ctx, sourcePointer, uint64(len(source)), namePointer)
	} else {
		if !json.Valid(metadata) || len(metadata) > MaxJSONBytes {
			return nil, errors.New("script context must be JSON of at most 1 MiB")
		}
		quoted, err := json.Marshal(string(metadata))
		if err != nil {
			return nil, err
		}
		setup := "globalThis.__hexMetadata = JSON.parse(" + string(quoted) + ");\n" + bootstrap
		setupPointer, setupErr := putString(ctx, module, setup)
		if setupErr != nil {
			return nil, setupErr
		}
		result, err = module.ExportedFunction("run_script").Call(ctx, sourcePointer, uint64(len(source)), namePointer, setupPointer, uint64(len(setup)))
	}
	if err != nil {
		return nil, executionError(ctx, state, err)
	}
	if validate && result[0] == 0 {
		return nil, nil
	}
	data, err := readResult(ctx, module)
	if err != nil {
		return nil, executionError(ctx, state, err)
	}
	if result[0] != 0 {
		if len(data) == 0 {
			return nil, errors.New("JavaScript exhausted its memory limit")
		}
		return nil, errors.New(string(data))
	}
	if !json.Valid(data) {
		return nil, errors.New("script must return a JSON value")
	}
	return data, nil
}

func executionError(ctx context.Context, state *execution, err error) error {
	if state.expired.Load() {
		return errors.New("JavaScript exceeded its 5-second computation budget")
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return errors.New("JavaScript exhausted its memory limit")
	}
	return fmt.Errorf("JavaScript sandbox failed (memory or stack limit): %w", err)
}

func putString(ctx context.Context, module api.Module, text string) (uint64, error) {
	result, err := module.ExportedFunction("malloc").Call(ctx, uint64(len(text)+1))
	if err != nil {
		return 0, err
	}
	if result[0] == 0 {
		return 0, errors.New("JavaScript sandbox exhausted its memory limit")
	}
	if !module.Memory().Write(uint32(result[0]), append([]byte(text), 0)) {
		return 0, errors.New("invalid JavaScript memory allocation")
	}
	return result[0], nil
}

func readResult(ctx context.Context, module api.Module) (json.RawMessage, error) {
	pointer, err := module.ExportedFunction("result_ptr").Call(ctx)
	if err != nil {
		return nil, err
	}
	length, err := module.ExportedFunction("result_len").Call(ctx)
	if err != nil {
		return nil, err
	}
	if length[0] > MaxJSONBytes {
		return nil, errors.New("script result exceeds 1 MiB")
	}
	data, ok := module.Memory().Read(uint32(pointer[0]), uint32(length[0]))
	if !ok {
		return nil, errors.New("invalid JavaScript result memory")
	}
	return append(json.RawMessage(nil), data...), nil
}

func invokeOperation(ctx context.Context, module api.Module, methodPointer, methodLength, inputPointer, inputLength, outputPointer, capacity uint32) int32 {
	state, ok := ctx.Value(executionKey{}).(*execution)
	if !ok || state.invoke == nil || ctx.Err() != nil || methodLength > 32 || inputLength > MaxJSONBytes || capacity > MaxJSONBytes {
		return -1
	}
	method, ok := module.Memory().Read(methodPointer, methodLength)
	if !ok {
		return -1
	}
	input, ok := module.Memory().Read(inputPointer, inputLength)
	if !ok || !json.Valid(input) {
		return -1
	}
	// Computation is metered separately from time spent waiting on authorized
	// host operations. The parent run deadline still bounds those operations.
	if !state.timer.Stop() {
		state.expired.Store(true)
		state.cancel()
		return -1
	}
	state.remaining -= time.Since(state.resumed)
	if state.remaining <= 0 {
		state.expired.Store(true)
		state.cancel()
		return -1
	}
	defer func() {
		state.resumed = time.Now()
		state.timer.Reset(max(state.remaining, time.Nanosecond))
	}()
	output, operationErr := state.invoke(ctx, string(method), append(json.RawMessage(nil), input...))
	response := struct {
		Output any    `json:"output,omitempty"`
		Error  string `json:"error,omitempty"`
	}{Output: output}
	if operationErr != nil {
		response.Output = nil
		response.Error = operationErr.Error()
	}
	data, err := json.Marshal(response)
	if err != nil || len(data) > int(capacity) {
		data = []byte(`{"error":"operation output exceeds 1 MiB or is not JSON"}`)
	}
	if !module.Memory().Write(outputPointer, data) {
		return -1
	}
	return int32(len(data))
}
