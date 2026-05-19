# Extendable Plugin API — Design Guide

Derived from the GORM source (`gorm.io/gorm`). Use this document as a blueprint to implement the same pattern in your own Go library.

---

## Table of Contents

1. [Core Concepts](#core-concepts)
2. [Architecture Overview](#architecture-overview)
3. [Step-by-Step Implementation](#step-by-step-implementation)
   - [1. Define the Plugin Interface](#1-define-the-plugin-interface)
   - [2. Define the Callback / Hook System](#2-define-the-callback--hook-system)
   - [3. Attach Callbacks to Your Core DB/Engine Struct](#3-attach-callbacks-to-your-core-dbengine-struct)
   - [4. Implement `Use()` for Plugin Registration](#4-implement-use-for-plugin-registration)
   - [5. Execute the Callback Chain on Each Operation](#5-execute-the-callback-chain-on-each-operation)
   - [6. Register Default Callbacks (Your Core Logic)](#6-register-default-callbacks-your-core-logic)
   - [7. Expose Ordering and Conditional Registration](#7-expose-ordering-and-conditional-registration)
4. [Complete Minimal Example](#complete-minimal-example)
5. [Writing a Plugin (Consumer Perspective)](#writing-a-plugin-consumer-perspective)
6. [Advanced Patterns](#advanced-patterns)
   - [Session-scoped State](#session-scoped-state)
   - [Wrapping the Connection Pool](#wrapping-the-connection-pool)
   - [Conditional Callbacks with Match](#conditional-callbacks-with-match)
   - [Removing or Replacing Built-in Callbacks](#removing-or-replacing-built-in-callbacks)
7. [Error Handling](#error-handling)
8. [Design Decisions and Trade-offs](#design-decisions-and-trade-offs)

---

## Core Concepts

GORM's plugin system is built on **three interlocking ideas**:

| Concept | GORM type | Purpose |
|---|---|---|
| **Plugin lifecycle** | `Plugin` interface | One-time registration and initialization |
| **Callback processor** | `processor` / `callbacks` | Ordered list of `func(*DB)` hooks per operation type |
| **Shared execution context** | `Statement` / `*DB` | Single object that flows through every hook |

Plugins do not "intercept" calls. Instead they **register named functions** into an ordered pipeline. The pipeline runs once per user-initiated operation (query, create, update, delete, …).

---

## Architecture Overview

```
User calls db.Query(...)
         │
         ▼
  finisher_api.go         ← sets up Statement, calls processor.Execute()
         │
         ▼
  processor.Execute()
    ├─ resolve scopes
    ├─ apply StatementModifier
    ├─ parse model / set ReflectValue
    └─ for _, fn := range p.fns { fn(db) }   ← sorted hook chain
              │         │         │
         gorm:begin   your:hook  gorm:commit
         _transaction  runs here _transaction
```

Plugins add their `fn` into `p.fns` by calling:

```go
db.Callback().Query().After("gorm:query").Register("myplugin:trace", myHookFn)
```

The processor **re-sorts** its list every time a `Register` / `Remove` / `Replace` call is made (at init time, not at query time), so the runtime cost is just iterating a `[]func(*DB)`.

---

## Step-by-Step Implementation

### 1. Define the Plugin Interface

```go
// Plugin is the extension contract.
// Name must be unique — it is used as the registry key.
// Initialize is called exactly once when the plugin is registered.
type Plugin interface {
    Name() string
    Initialize(*Engine) error
}
```

Keep this interface **tiny**. All real work happens through the callback API that `Initialize` receives via `*Engine`.

---

### 2. Define the Callback / Hook System

You need three types: `callbacks` (the registry), `processor` (one per operation kind), and `callback` (a single entry).

```go
// callbacks holds one processor per operation type.
type callbacks struct {
    processors map[string]*processor
}

// processor manages the ordered list of hooks for one operation (e.g. "query").
type processor struct {
    engine    *Engine
    fns       []func(*Engine)   // compiled, sorted — used at runtime
    callbacks []*callback       // raw, unsorted — mutated at init
    Clauses   []string          // optional: clause build order for SQL libs
}

// callback is a single named hook with optional ordering constraints.
type callback struct {
    name      string
    before    string
    after     string
    remove    bool
    replace   bool
    match     func(*Engine) bool
    handler   func(*Engine)
    processor *processor
}
```

Expose named accessors on `callbacks` for each operation kind:

```go
func (c *callbacks) Create() *processor { return c.processors["create"] }
func (c *callbacks) Query()  *processor { return c.processors["query"]  }
func (c *callbacks) Update() *processor { return c.processors["update"] }
func (c *callbacks) Delete() *processor { return c.processors["delete"] }
// add more as needed: Row, Raw, Upsert, …
```

Provide the fluent builder API on `processor`:

```go
func (p *processor) Register(name string, fn func(*Engine)) error {
    return (&callback{processor: p}).Register(name, fn)
}
func (p *processor) Before(name string) *callback { return &callback{before: name, processor: p} }
func (p *processor) After(name string)  *callback { return &callback{after: name,  processor: p} }
func (p *processor) Match(fn func(*Engine) bool) *callback {
    return &callback{match: fn, processor: p}
}
func (p *processor) Remove(name string)                  error { ... }
func (p *processor) Replace(name string, fn func(*Engine)) error { ... }
```

And on `callback` (so you can chain):

```go
func (c *callback) Before(name string) *callback { c.before = name; return c }
func (c *callback) After(name string)  *callback { c.after  = name; return c }

func (c *callback) Register(name string, fn func(*Engine)) error {
    c.name    = name
    c.handler = fn
    c.processor.callbacks = append(c.processor.callbacks, c)
    return c.processor.compile()  // re-sort immediately
}
```

The **`compile` + `sortCallbacks`** functions (see [Complete Minimal Example](#complete-minimal-example)) build the final `[]func(*Engine)` using a topological sort driven by `before`/`after` constraints.

---

### 3. Attach Callbacks to Your Core DB/Engine Struct

```go
type Config struct {
    // ... your config fields ...
    Plugins   map[string]Plugin
    callbacks *callbacks
    // any shared sync.Map for caching, etc.
}

type Engine struct {
    *Config
    // per-operation state goes in a Statement equivalent
}

func initializeCallbacks(e *Engine) *callbacks {
    return &callbacks{
        processors: map[string]*processor{
            "create": {engine: e},
            "query":  {engine: e},
            "update": {engine: e},
            "delete": {engine: e},
        },
    }
}
```

Then in your `Open` / `New` constructor:

```go
func Open(opts ...Option) (*Engine, error) {
    cfg := &Config{Plugins: map[string]Plugin{}}
    // apply options ...
    e := &Engine{Config: cfg}
    e.callbacks = initializeCallbacks(e)

    // let the dialector (or built-in defaults) register core callbacks
    RegisterDefaultCallbacks(e)

    // run AfterInitialize for any plugins supplied via config
    for _, p := range cfg.Plugins {
        if err := p.Initialize(e); err != nil {
            return nil, err
        }
    }
    return e, nil
}

// Callback exposes the callback manager to plugins and users.
func (e *Engine) Callback() *callbacks { return e.callbacks }
```

---

### 4. Implement `Use()` for Plugin Registration

```go
var ErrPluginRegistered = errors.New("plugin already registered")

func (e *Engine) Use(plugin Plugin) error {
    name := plugin.Name()
    if _, ok := e.Plugins[name]; ok {
        return ErrPluginRegistered
    }
    if err := plugin.Initialize(e); err != nil {
        return err
    }
    e.Plugins[name] = plugin
    return nil
}
```

Key rules enforced here:
- **Duplicate guard** — same name → error, no silent override.
- **Initialize-before-store** — a failing `Initialize` leaves `Plugins` unchanged.
- **No thread-safety on `Use`** — plugins are expected to be registered at startup, not concurrently. If needed, add a `sync.RWMutex`.

---

### 5. Execute the Callback Chain on Each Operation

Each public API method (e.g. `engine.Query(...)`) eventually calls:

```go
func (p *processor) Execute(e *Engine) *Engine {
    // 1. optional: resolve lazy scopes
    // 2. optional: parse model / reflect destination
    // 3. run the hook chain
    for _, fn := range p.fns {
        fn(e)
        if e.Error != nil {
            break  // stop on first error, or choose to continue — your call
        }
    }
    return e
}
```

A concrete finisher looks like:

```go
func (e *Engine) Find(dest interface{}) *Engine {
    tx := e.getInstance()
    tx.Statement.Dest = dest
    return tx.callbacks.Query().Execute(tx)
}
```

The `Statement` (or equivalent context object) carries all per-operation data (SQL builder, destination pointer, model, variables, errors). Each `fn(e)` in the chain reads from and writes to `e.Statement`.

---

### 6. Register Default Callbacks (Your Core Logic)

Your core functionality is **itself a set of callbacks**, registered under the `"yourlibrary:*"` namespace at startup. This keeps the architecture uniform — you and plugins use the same API.

```go
func RegisterDefaultCallbacks(e *Engine) {
    qCb := e.Callback().Query()
    qCb.Register("mylib:before_query", beforeQuery)
    qCb.Register("mylib:query",        executeQuery)
    qCb.Register("mylib:after_query",  afterQuery)

    cCb := e.Callback().Create()
    cCb.Register("mylib:begin_transaction", beginTransaction)
    cCb.Register("mylib:create",            executeCreate)
    cCb.Register("mylib:commit_or_rollback", commitOrRollback)
    // ...
}
```

Use a **consistent prefix** (`mylib:`) so plugins can safely anchor their hooks relative to yours:

```go
// Plugin: run after mylib's query but before result mapping
e.Callback().Query().After("mylib:query").Register("tracing:record_sql", recordSQL)
```

---

### 7. Expose Ordering and Conditional Registration

The full sort algorithm (from GORM) supports:

| Constraint | Meaning |
|---|---|
| `.Before("other:name")` | This hook runs before `other:name` |
| `.After("other:name")` | This hook runs after `other:name` |
| `.Before("*")` | This hook runs before everything currently registered |
| `.After("*")` | This hook runs after everything currently registered |
| `.Match(func(*Engine) bool)` | This hook is only included in the compiled chain when `match` returns true (evaluated at compile time, i.e. at registration time using `p.engine` state) |

All of these are **composable**:

```go
e.Callback().Create().
    Match(func(e *Engine) bool { return !e.SkipTransactions }).
    Before("mylib:create").
    Register("tracing:begin_span", beginSpan)
```

---

## Complete Minimal Example

Below is a self-contained Go package you can drop into your project as a starting point.

```go
package engine

import (
    "context"
    "errors"
    "fmt"
    "sort"
)

// ── Interfaces ────────────────────────────────────────────────────────────────

type Plugin interface {
    Name() string
    Initialize(*Engine) error
}

// ── Core engine ───────────────────────────────────────────────────────────────

type Engine struct {
    Plugins   map[string]Plugin
    Error     error
    Statement *Statement
    callbacks *callbacks
}

type Statement struct {
    Context context.Context
    Model   interface{}
    Dest    interface{}
    SQL     string
    // add fields as needed
}

var ErrPluginRegistered = errors.New("plugin already registered")

func New() *Engine {
    e := &Engine{Plugins: map[string]Plugin{}}
    e.callbacks = initializeCallbacks(e)
    RegisterDefaultCallbacks(e)
    return e
}

func (e *Engine) Use(p Plugin) error {
    if _, ok := e.Plugins[p.Name()]; ok {
        return ErrPluginRegistered
    }
    if err := p.Initialize(e); err != nil {
        return err
    }
    e.Plugins[p.Name()] = p
    return nil
}

func (e *Engine) Callback() *callbacks { return e.callbacks }

func (e *Engine) AddError(err error) {
    if err != nil && e.Error == nil {
        e.Error = err
    }
}

// ── Callback system ───────────────────────────────────────────────────────────

type callbacks struct {
    processors map[string]*processor
}

func initializeCallbacks(e *Engine) *callbacks {
    return &callbacks{processors: map[string]*processor{
        "query":  {engine: e},
        "create": {engine: e},
    }}
}

func (c *callbacks) Query()  *processor { return c.processors["query"]  }
func (c *callbacks) Create() *processor { return c.processors["create"] }

type processor struct {
    engine    *Engine
    fns       []func(*Engine)
    callbacks []*callback
}

type callback struct {
    name      string
    before    string
    after     string
    remove    bool
    replace   bool
    match     func(*Engine) bool
    handler   func(*Engine)
    processor *processor
}

func (p *processor) Execute(e *Engine) *Engine {
    for _, fn := range p.fns {
        fn(e)
        if e.Error != nil {
            break
        }
    }
    return e
}

func (p *processor) Register(name string, fn func(*Engine)) error {
    return (&callback{processor: p}).Register(name, fn)
}

func (p *processor) Before(name string) *callback { return &callback{before: name, processor: p} }
func (p *processor) After(name string) *callback  { return &callback{after: name, processor: p} }
func (p *processor) Match(fn func(*Engine) bool) *callback {
    return &callback{match: fn, processor: p}
}

func (p *processor) Remove(name string) error {
    c := &callback{name: name, remove: true, processor: p}
    p.callbacks = append(p.callbacks, c)
    return p.compile()
}

func (p *processor) Replace(name string, fn func(*Engine)) error {
    c := &callback{name: name, handler: fn, replace: true, processor: p}
    p.callbacks = append(p.callbacks, c)
    return p.compile()
}

func (c *callback) Before(name string) *callback { c.before = name; return c }
func (c *callback) After(name string) *callback  { c.after = name; return c }

func (c *callback) Register(name string, fn func(*Engine)) error {
    c.name, c.handler = name, fn
    c.processor.callbacks = append(c.processor.callbacks, c)
    return c.processor.compile()
}

func (p *processor) compile() error {
    var active []*callback
    removed := map[string]bool{}
    for _, cb := range p.callbacks {
        if cb.remove {
            removed[cb.name] = true
        }
        if cb.match == nil || cb.match(p.engine) {
            active = append(active, cb)
        }
    }
    fns, err := sortCallbacks(active, removed)
    if err != nil {
        return err
    }
    p.fns = fns
    return nil
}

func sortCallbacks(cs []*callback, removed map[string]bool) ([]func(*Engine), error) {
    names := make([]string, 0, len(cs))
    for _, c := range cs {
        names = append(names, c.name)
    }

    var sorted []string

    // stable pre-sort: Before("*") callbacks first, After("*") last
    sort.SliceStable(cs, func(i, j int) bool {
        if cs[j].before == "*" && cs[i].before != "*" { return true }
        if cs[j].after == "*" && cs[i].after != "*"   { return true }
        return false
    })

    var sortOne func(c *callback) error
    sortOne = func(c *callback) error {
        getRIdx := func(strs []string, s string) int {
            for i := len(strs) - 1; i >= 0; i-- {
                if strs[i] == s { return i }
            }
            return -1
        }

        if c.before != "" {
            if c.before == "*" {
                if getRIdx(sorted, c.name) == -1 {
                    sorted = append([]string{c.name}, sorted...)
                }
            } else if idx := getRIdx(sorted, c.before); idx != -1 {
                if cur := getRIdx(sorted, c.name); cur == -1 {
                    sorted = append(sorted[:idx], append([]string{c.name}, sorted[idx:]...)...)
                } else if cur > idx {
                    return fmt.Errorf("conflicting callback %s before %s", c.name, c.before)
                }
            } else if idx := getRIdx(names, c.before); idx != -1 {
                cs[idx].after = c.name
            }
        }

        if c.after != "" {
            if c.after == "*" {
                if getRIdx(sorted, c.name) == -1 {
                    sorted = append(sorted, c.name)
                }
            } else if idx := getRIdx(sorted, c.after); idx != -1 {
                if getRIdx(sorted, c.name) == -1 {
                    sorted = append(sorted, c.name)
                }
            } else if idx := getRIdx(names, c.after); idx != -1 {
                after := cs[idx]
                if after.before == "" {
                    after.before = c.name
                }
                if err := sortOne(after); err != nil { return err }
                if err := sortOne(c);    err != nil { return err }
            }
        }

        getRIdx2 := func(strs []string, s string) int {
            for i := len(strs) - 1; i >= 0; i-- {
                if strs[i] == s { return i }
            }
            return -1
        }
        if getRIdx2(sorted, c.name) == -1 {
            sorted = append(sorted, c.name)
        }
        return nil
    }

    for _, c := range cs {
        if err := sortOne(c); err != nil {
            return nil, err
        }
    }

    var fns []func(*Engine)
    for _, name := range sorted {
        if removed[name] {
            continue
        }
        for i := len(cs) - 1; i >= 0; i-- {
            if cs[i].name == name && !cs[i].remove {
                fns = append(fns, cs[i].handler)
                break
            }
        }
    }
    return fns, nil
}

// ── Default callbacks (your core logic) ──────────────────────────────────────

func RegisterDefaultCallbacks(e *Engine) {
    e.Callback().Query().Register("mylib:query", func(e *Engine) {
        // execute the actual query here
        fmt.Printf("[mylib] executing query: %s\n", e.Statement.SQL)
    })

    e.Callback().Create().Register("mylib:create", func(e *Engine) {
        fmt.Printf("[mylib] executing create for model: %T\n", e.Statement.Model)
    })
}
```

---

## Writing a Plugin (Consumer Perspective)

Once your library implements the pattern above, a plugin author writes:

```go
package tracingplugin

import "yourlib/engine"

type TracingPlugin struct {
    serviceName string
}

func New(serviceName string) *TracingPlugin {
    return &TracingPlugin{serviceName: serviceName}
}

// Name returns the unique plugin identifier.
func (t *TracingPlugin) Name() string { return "tracing" }

// Initialize wires the plugin into the engine's callback chain.
func (t *TracingPlugin) Initialize(e *engine.Engine) error {
    // Run before the core query handler
    e.Callback().Query().
        Before("mylib:query").
        Register("tracing:start_span", t.startSpan)

    // Run after the core query handler (even if it errored)
    e.Callback().Query().
        After("mylib:query").
        Register("tracing:finish_span", t.finishSpan)

    // For creates, run first of all
    e.Callback().Create().
        Before("*").
        Register("tracing:create_span", t.startSpan)

    return nil
}

func (t *TracingPlugin) startSpan(e *engine.Engine) {
    fmt.Printf("[tracing:%s] → starting span\n", t.serviceName)
}

func (t *TracingPlugin) finishSpan(e *engine.Engine) {
    fmt.Printf("[tracing:%s] ← finishing span (err=%v)\n", t.serviceName, e.Error)
}
```

Registration at app startup:

```go
e := engine.New()

if err := e.Use(tracingplugin.New("my-service")); err != nil {
    log.Fatal(err)
}
```

---

## Advanced Patterns

### Session-scoped State

Plugins often need to store per-request data (span IDs, tenant info, etc.) without mutating shared config. Use a `sync.Map` on the `Statement`:

```go
// Store during a hook
e.Statement.Settings.Store("tracing:span_id", spanID)

// Retrieve in a later hook
if v, ok := e.Statement.Settings.Load("tracing:span_id"); ok {
    spanID := v.(string)
}
```

For statement-instance-scoped keys (won't collide across concurrent sessions sharing the same Statement pointer):

```go
key := fmt.Sprintf("%p:tracing:span_id", e.Statement)
e.Statement.Settings.Store(key, spanID)
```

---

### Wrapping the Connection Pool

A plugin can swap the connection pool to add instrumentation at the driver level:

```go
func (p *MyPlugin) Initialize(e *Engine) error {
    e.Config.ConnPool = &instrumentedPool{
        inner:  e.Config.ConnPool,
        plugin: p,
    }
    return nil
}
```

This is how GORM's `PreparedStmtDB` works — it wraps the raw pool with a prepared-statement cache, transparently, without any change to the callback chain.

---

### Conditional Callbacks with Match

Use `.Match(fn)` to **exclude a callback from the compiled chain** based on engine state at registration time:

```go
// Only include transaction callbacks when the user hasn't disabled them
e.Callback().Create().
    Match(func(e *Engine) bool { return !e.SkipDefaultTransaction }).
    Register("mylib:begin_transaction", beginTransaction)
```

`Match` is evaluated **once at compile time** (i.e. when `Register` is called), not on every query. If your condition depends on per-request state, check it inside the handler function instead.

---

### Removing or Replacing Built-in Callbacks

Plugins can surgically remove or replace any named hook:

```go
// Remove the built-in soft-delete hook and install a custom one
e.Callback().Delete().Remove("mylib:soft_delete")
e.Callback().Delete().After("mylib:delete").Register("myplugin:audit_delete", auditDelete)

// Replace the built-in query executor entirely
e.Callback().Query().Replace("mylib:query", myCustomQueryFn)
```

`Replace` appends a `replace=true` marker; during `compile()`, the last entry with a given name wins.

---

## Error Handling

| Scenario | Behavior |
|---|---|
| `Use(plugin)` with a duplicate name | Returns `ErrPluginRegistered`; plugin map unchanged |
| `plugin.Initialize(e)` returns an error | `Use` returns that error; plugin is **not** stored |
| A hook calls `e.AddError(err)` | Error is set on `e.Error`; subsequent hooks can check and bail early |
| `sortCallbacks` detects a cycle | Returns `fmt.Errorf("conflicting callback …")`; `compile()` logs the error |
| `Remove` on a non-existent name | Silently no-ops (the name just never appears in `sorted`) |

Recommended pattern inside a hook:

```go
func myHook(e *Engine) {
    if e.Error != nil {
        return  // bail out of the chain early
    }
    // do work …
    if err := doSomething(); err != nil {
        e.AddError(err)
    }
}
```

---

## Design Decisions and Trade-offs

| Decision | Rationale | Alternative |
|---|---|---|
| **`Plugin` interface has only 2 methods** | Keeps plugins easy to write; all power comes from the callback API | Richer interface with `Teardown`, `OnError`, etc. — adds complexity for little gain |
| **Callbacks compiled at registration, not at query time** | Zero runtime overhead per query; sorting happens once at startup | Lazy compile per query — adds latency but allows dynamic registration at runtime |
| **`match` evaluated at compile time** | Avoids per-query closure overhead | Evaluate at Execute time — gives true per-request conditionality but costs a function call per hook per query |
| **Shared `*Config` across cloned `*DB` values** | Clones share the same callback processors — no re-registration needed | Deep-copying config — safer for concurrent sessions but expensive and rarely needed |
| **No `Teardown`/`Close` on Plugin** | Plugins live for the lifetime of the engine | Add `io.Closer` to Plugin if you need cleanup (connections, goroutines) |
| **String names for hooks** | Human-readable, easy to debug, stable across refactors | Symbol/func-pointer keys — faster lookup but harder to introspect and document |
| **`Before("*")` / `After("*")` wildcards** | Provides a fast path for "run first" / "run last" without knowing other hook names | Explicit priority integers — simpler sort but less expressive |
