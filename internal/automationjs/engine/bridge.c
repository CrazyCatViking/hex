// Hex's deliberately small QuickJS ABI. No quickjs-libc, module loader,
// filesystem, network, process or bytecode interfaces are linked into it.
#include "quickjs.h"
#include <stdlib.h>

#define MAX_JSON (1024 * 1024)

__attribute__((import_module("hex"), import_name("invoke")))
extern int host_invoke(const char *method, size_t method_len,
                       const char *input, size_t input_len,
                       char *output, size_t capacity);

static JSRuntime *runtime;
static JSContext *context;
static char *output;
static size_t output_len;

static JSModuleDef *reject_module(JSContext *ctx, const char *name, void *opaque)
{
    JS_ThrowTypeError(ctx, "imports are unavailable in automation scripts: %s", name);
    return NULL;
}

static JSValue invoke(JSContext *ctx, JSValueConst this_val, int argc,
                      JSValueConst *argv)
{
    if (argc != 2 || !JS_IsString(argv[0]) || !JS_IsString(argv[1]))
        return JS_ThrowTypeError(ctx, "expected an operation and JSON input");

    size_t method_len, input_len;
    const char *method = JS_ToCStringLen(ctx, &method_len, argv[0]);
    const char *input = JS_ToCStringLen(ctx, &input_len, argv[1]);
    if (!method || !input) {
        if (method) JS_FreeCString(ctx, method);
        if (input) JS_FreeCString(ctx, input);
        return JS_EXCEPTION;
    }
    if (method_len > 32 || input_len > MAX_JSON) {
        JS_FreeCString(ctx, method);
        JS_FreeCString(ctx, input);
        return JS_ThrowRangeError(ctx, "operation input exceeds its limit");
    }

    char *response = malloc(MAX_JSON + 1);
    if (!response) {
        JS_FreeCString(ctx, method);
        JS_FreeCString(ctx, input);
        return JS_ThrowOutOfMemory(ctx);
    }
    int length = host_invoke(method, method_len, input, input_len, response, MAX_JSON);
    JS_FreeCString(ctx, method);
    JS_FreeCString(ctx, input);
    JSValue result;
    if (length < 0 || length > MAX_JSON) {
        result = JS_ThrowInternalError(ctx, "automation operation failed");
    } else {
        response[length] = '\0';
        result = JS_ParseJSON(ctx, response, length, "<hex-operation>");
    }
    free(response);
    return result;
}

int initialize(size_t heap_limit, size_t stack_limit)
{
    runtime = JS_NewRuntime();
    if (!runtime) return -1;
    JS_SetMemoryLimit(runtime, heap_limit);
    JS_SetMaxStackSize(runtime, stack_limit);
    JS_SetModuleLoaderFunc(runtime, NULL, reject_module, NULL);
    context = JS_NewContext(runtime);
    if (!context) return -1;
    JSValue global = JS_GetGlobalObject(context);
    int status = JS_SetPropertyStr(context, global, "__hexInvoke",
                                   JS_NewCFunction(context, invoke, "invoke", 2));
    JS_FreeValue(context, global);
    return status < 0 ? -1 : 0;
}

static int failure(void)
{
    JSValue exception = JS_GetException(context);
    JSValue stack = JS_GetPropertyStr(context, exception, "stack");
    size_t message_len = 0, stack_len = 0;
    const char *message = JS_ToCStringLen(context, &message_len, exception);
    const char *trace = JS_IsUndefined(stack) ? NULL : JS_ToCStringLen(context, &stack_len, stack);
    if (message_len > 8192) message_len = 8192;
    if (stack_len > 8192) stack_len = 8192;
    output_len = message_len + (trace ? stack_len + 1 : 0);
    output = malloc(output_len + 1);
    if (output) {
        if (message) memcpy(output, message, message_len);
        if (trace) {
            output[message_len] = '\n';
            memcpy(output + message_len + 1, trace, stack_len);
        }
        output[output_len] = '\0';
    } else output_len = 0;
    if (message) JS_FreeCString(context, message);
    if (trace) JS_FreeCString(context, trace);
    JS_FreeValue(context, stack);
    JS_FreeValue(context, exception);
    return 1;
}

static JSValue await_value(JSValue value)
{
    while (JS_IsPromise(value)) {
        JSPromiseStateEnum state = JS_PromiseState(context, value);
        if (state == JS_PROMISE_PENDING) {
            JSContext *job_context;
            int status = JS_ExecutePendingJob(runtime, &job_context);
            if (status <= 0) {
                JS_FreeValue(context, value);
                if (status < 0) return JS_EXCEPTION;
                return JS_ThrowTypeError(context, "script returned an unresolved promise");
            }
            continue;
        }
        JSValue result = JS_PromiseResult(context, value);
        JS_FreeValue(context, value);
        if (state == JS_PROMISE_REJECTED) return JS_Throw(context, result);
        value = result;
    }
    return value;
}

int validate_script(const char *source, size_t length, const char *filename)
{
    JSValue compiled = JS_Eval(context, source, length, filename,
                               JS_EVAL_TYPE_MODULE | JS_EVAL_FLAG_COMPILE_ONLY);
    if (JS_IsException(compiled)) return failure();
    int status = JS_ResolveModule(context, compiled);
    JS_FreeValue(context, compiled);
    if (status < 0) return failure();
    return 0;
}

int run_script(const char *source, size_t length, const char *filename,
               const char *bootstrap, size_t bootstrap_len)
{
    JSValue setup = JS_Eval(context, bootstrap, bootstrap_len, "<hex>", JS_EVAL_TYPE_GLOBAL);
    if (JS_IsException(setup)) return failure();
    JS_FreeValue(context, setup);

    JSValue compiled = JS_Eval(context, source, length, filename,
                               JS_EVAL_TYPE_MODULE | JS_EVAL_FLAG_COMPILE_ONLY);
    if (JS_IsException(compiled)) return failure();
    JSModuleDef *module = JS_VALUE_GET_PTR(compiled);
    JSValue evaluated = await_value(JS_EvalFunction(context, compiled));
    if (JS_IsException(evaluated)) return failure();
    JS_FreeValue(context, evaluated);

    JSValue namespace = JS_GetModuleNamespace(context, module);
    JSValue function = JS_GetPropertyStr(context, namespace, "default");
    JS_FreeValue(context, namespace);
    if (!JS_IsFunction(context, function)) {
        JS_FreeValue(context, function);
        JS_ThrowTypeError(context, "script must export a default function");
        return failure();
    }
    JSValue global = JS_GetGlobalObject(context);
    JSValue hex = JS_GetPropertyStr(context, global, "hex");
    JS_FreeValue(context, global);
    JSValue result = await_value(JS_Call(context, function, JS_UNDEFINED, 1, &hex));
    JS_FreeValue(context, hex);
    JS_FreeValue(context, function);
    if (JS_IsException(result)) return failure();

    // Drain jobs so un-awaited work cannot survive the run or bypass its budget.
    JSContext *job_context;
    int job_status;
    while ((job_status = JS_ExecutePendingJob(runtime, &job_context)) > 0) {}
    if (job_status < 0) {
        JS_FreeValue(context, result);
        return failure();
    }
    if (JS_IsUndefined(result)) {
        JS_FreeValue(context, result);
        result = JS_NULL;
    }
    JSValue json = JS_JSONStringify(context, result, JS_UNDEFINED, JS_UNDEFINED);
    JS_FreeValue(context, result);
    if (JS_IsException(json)) return failure();
    const char *text = JS_ToCStringLen(context, &output_len, json);
    if (!text) {
        JS_FreeValue(context, json);
        return failure();
    }
    if (output_len > MAX_JSON || JS_IsUndefined(json)) {
        JS_FreeCString(context, text);
        JS_FreeValue(context, json);
        JS_ThrowRangeError(context, "script must return JSON of at most 1 MiB");
        return failure();
    }
    output = malloc(output_len + 1);
    if (!output) {
        JS_FreeCString(context, text);
        JS_FreeValue(context, json);
        JS_ThrowOutOfMemory(context);
        return failure();
    }
    memcpy(output, text, output_len + 1);
    JS_FreeCString(context, text);
    JS_FreeValue(context, json);
    return 0;
}

const char *result_ptr(void) { return output; }
size_t result_len(void) { return output_len; }
