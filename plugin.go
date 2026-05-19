package ebarimtv3

import (
	"errors"
	"fmt"
	"sort"
)

// Plugin is the extension contract every plugin must satisfy.
// Name must be globally unique — it is used as the registry key.
// Initialize is called exactly once when the plugin is registered via Use().
type Plugin interface {
	Name() string
	Initialize(*EbarimtClient) error
}

// ErrPluginRegistered is returned by Use when a plugin with the same name is
// already stored in the registry.
var ErrPluginRegistered = errors.New("plugin already registered")

// ─── Callbacks registry ──────────────────────────────────────────────────────

// callbacks holds one processor per operation type.
type callbacks struct {
	processors map[string]*Processor
}

func initCallbacks(e *EbarimtClient) *callbacks {
	return &callbacks{
		processors: map[string]*Processor{
			// ── High-level POS operations ─────────────────────────────────────
			"create":        {client: e},
			"receiptSend":   {client: e},
			"receiptDelete": {client: e},
			"sendData":      {client: e},
			"info":          {client: e},
			"bankAccounts":  {client: e},

			// ── Цахим төлбөрийн баримт / Public API operations ────────────────
			"getInfo":           {client: e},
			"getTinInfo":        {client: e},
			"getBranchInfo":     {client: e},
			"getSalesTotalData": {client: e},
			"getSalesListERP":   {client: e},
			"saveOprMerchants":  {client: e},

			// ── Хялбар бүртгэл / Easy Register operations ─────────────────────
			"consumerInfo":           {client: e},
			"getProfile":             {client: e},
			"approveQr":              {client: e},
			"foriegnerPassportInfo":  {client: e},
			"foriegnerCustomerNoInfo": {client: e},
			"foriegnerInfoRegister":  {client: e},

			// ── Low-level HTTP primitives — sub-spans of the above ────────────
			"httpPosRequest": {client: e},
			"httpRequest":    {client: e},
			"auth":           {client: e},
		},
	}
}

// Named accessors — one per operation type.

// POS operations.
func (c *callbacks) Create() *Processor        { return c.processors["create"] }
func (c *callbacks) ReceiptSend() *Processor   { return c.processors["receiptSend"] }
func (c *callbacks) ReceiptDelete() *Processor { return c.processors["receiptDelete"] }
func (c *callbacks) SendData() *Processor      { return c.processors["sendData"] }
func (c *callbacks) Info() *Processor          { return c.processors["info"] }
func (c *callbacks) BankAccounts() *Processor  { return c.processors["bankAccounts"] }

// Цахим төлбөрийн баримт / Public API operations.
func (c *callbacks) GetInfo() *Processor           { return c.processors["getInfo"] }
func (c *callbacks) GetTinInfo() *Processor        { return c.processors["getTinInfo"] }
func (c *callbacks) GetBranchInfo() *Processor     { return c.processors["getBranchInfo"] }
func (c *callbacks) GetSalesTotalData() *Processor { return c.processors["getSalesTotalData"] }
func (c *callbacks) GetSalesListERP() *Processor   { return c.processors["getSalesListERP"] }
func (c *callbacks) SaveOprMerchants() *Processor  { return c.processors["saveOprMerchants"] }

// Хялбар бүртгэл / Easy Register operations.
func (c *callbacks) ConsumerInfo() *Processor           { return c.processors["consumerInfo"] }
func (c *callbacks) GetProfile() *Processor             { return c.processors["getProfile"] }
func (c *callbacks) ApproveQr() *Processor              { return c.processors["approveQr"] }
func (c *callbacks) ForiegnerPassportInfo() *Processor  { return c.processors["foriegnerPassportInfo"] }
func (c *callbacks) ForiegnerCustomerNoInfo() *Processor { return c.processors["foriegnerCustomerNoInfo"] }
func (c *callbacks) ForiegnerInfoRegister() *Processor  { return c.processors["foriegnerInfoRegister"] }

// HTTP primitives.
func (c *callbacks) HTTPPosRequest() *Processor { return c.processors["httpPosRequest"] }
func (c *callbacks) HTTPRequest() *Processor    { return c.processors["httpRequest"] }
func (c *callbacks) Auth() *Processor           { return c.processors["auth"] }

// ─── Processor ───────────────────────────────────────────────────────────────

// processor manages the ordered list of hooks for one operation.
type Processor struct {
	client *EbarimtClient
	fns    []func(*EbarimtClient) // compiled, sorted — used at runtime
	cbs    []*callback            // raw, unsorted  — mutated at init time
}

// Execute runs the compiled hook chain.
// It stops early on the first error recorded via AddError.
func (p *Processor) Execute(e *EbarimtClient) *EbarimtClient {
	for _, fn := range p.fns {
		fn(e)
		if e.Error != nil {
			break
		}
	}
	return e
}

// Register adds a named hook to this processor's chain.
func (p *Processor) Register(name string, fn func(*EbarimtClient)) error {
	return (&callback{processor: p}).Register(name, fn)
}

// Before returns a callback builder that will insert the hook before the named hook.
func (p *Processor) Before(name string) *callback {
	return &callback{before: name, processor: p}
}

// After returns a callback builder that will insert the hook after the named hook.
func (p *Processor) After(name string) *callback {
	return &callback{after: name, processor: p}
}

// Match returns a callback builder whose hook is included in the chain only
// when fn returns true (evaluated once at registration time).
func (p *Processor) Match(fn func(*EbarimtClient) bool) *callback {
	return &callback{match: fn, processor: p}
}

// Remove marks a named hook for removal from the compiled chain.
func (p *Processor) Remove(name string) error {
	p.cbs = append(p.cbs, &callback{name: name, remove: true, processor: p})
	return p.compile()
}

// Replace substitutes the handler for an existing named hook.
func (p *Processor) Replace(name string, fn func(*EbarimtClient)) error {
	p.cbs = append(p.cbs, &callback{name: name, handler: fn, replace: true, processor: p})
	return p.compile()
}

// ─── Callback (single hook entry) ────────────────────────────────────────────

// callback is a single named hook with optional ordering constraints.
type callback struct {
	name      string
	before    string
	after     string
	remove    bool
	replace   bool
	match     func(*EbarimtClient) bool
	handler   func(*EbarimtClient)
	processor *Processor
}

func (c *callback) Before(name string) *callback { c.before = name; return c }
func (c *callback) After(name string) *callback  { c.after = name; return c }

// Register finalises the callback and compiles the processor's sorted chain.
func (c *callback) Register(name string, fn func(*EbarimtClient)) error {
	c.name, c.handler = name, fn
	c.processor.cbs = append(c.processor.cbs, c)
	return c.processor.compile()
}

// ─── compile + topological sort ──────────────────────────────────────────────

func (p *Processor) compile() error {
	var active []*callback
	removed := map[string]bool{}
	for _, cb := range p.cbs {
		if cb.remove {
			removed[cb.name] = true
		}
		if cb.match == nil || cb.match(p.client) {
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

func sortCallbacks(cs []*callback, removed map[string]bool) ([]func(*EbarimtClient), error) {
	names := make([]string, 0, len(cs))
	for _, c := range cs {
		names = append(names, c.name)
	}

	var sorted []string

	// Pre-sort: Before("*") callbacks first, After("*") callbacks last.
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[j].before == "*" && cs[i].before != "*" {
			return true
		}
		if cs[j].after == "*" && cs[i].after != "*" {
			return true
		}
		return false
	})

	getRIdx := func(strs []string, s string) int {
		for i := len(strs) - 1; i >= 0; i-- {
			if strs[i] == s {
				return i
			}
		}
		return -1
	}

	var sortOne func(c *callback) error
	sortOne = func(c *callback) error {
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
			} else if getRIdx(sorted, c.after) != -1 {
				if getRIdx(sorted, c.name) == -1 {
					sorted = append(sorted, c.name)
				}
			} else if idx := getRIdx(names, c.after); idx != -1 {
				after := cs[idx]
				if after.before == "" {
					after.before = c.name
				}
				if err := sortOne(after); err != nil {
					return err
				}
				if err := sortOne(c); err != nil {
					return err
				}
			}
		}

		if getRIdx(sorted, c.name) == -1 {
			sorted = append(sorted, c.name)
		}
		return nil
	}

	for _, c := range cs {
		if err := sortOne(c); err != nil {
			return nil, err
		}
	}

	var fns []func(*EbarimtClient)
	for _, name := range sorted {
		if removed[name] {
			continue
		}
		// Last-registered wins (supports Replace semantics).
		for i := len(cs) - 1; i >= 0; i-- {
			if cs[i].name == name && !cs[i].remove {
				fns = append(fns, cs[i].handler)
				break
			}
		}
	}
	return fns, nil
}
