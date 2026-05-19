package ebarimtv3

import (
	"context"
	"encoding/json"

	ebarimt3SdkServices "github.com/batorgil-it/ebarimt-pos3-go/services"
	models "github.com/batorgil-it/ebarimt-pos3-go/structs"

	"github.com/batorgil-it/ebarimt-pos3-go/constants"
	"github.com/batorgil-it/ebarimt-pos3-go/pos3"
	"github.com/batorgil-it/ebarimt-pos3-go/structs"
	"github.com/batorgil-it/ebarimt-pos3-go/utils"
	"gorm.io/gorm"
)

// EbarimtClient is the main SDK entry point.  In addition to embedding the
// low-level pos3.Pos3 HTTP client it now carries a plugin registry and an
// ordered callback pipeline so that third-party plugins can hook into every
// operation without forking the SDK.
//
// Shared state (Pos3, DB, mail config, plugins, callbacks) lives directly on
// the struct.  Per-operation state is isolated in a Statement that is created
// fresh for each operation via withStatement().
type EbarimtClient struct {
	// Low-level POS 3.0 HTTP client (shared across clones).
	// Embedded so all pos3.Pos3 methods (GetInfo, GetTinInfo, …) are promoted
	// directly onto EbarimtClient.  Access the interface value itself via
	// e.Pos3 when you need to call a method without triggering any override.
	pos3.Pos3

	// Optional integrations — shared, not mutated after construction.
	DB           *gorm.DB
	MailHost     string
	MailPort     string
	MailFrom     string
	MailSubject  string
	MailPassword string
	MailUser     string
	TemplatePath string

	// Plugin registry — populated by Use() at startup.
	plugins map[string]Plugin

	// Callback pipeline — one processor per operation type.
	pluginCallbacks *callbacks

	// defaultCtx is the context used as the base for every new Statement.
	// Set via WithContext; falls back to context.Background().
	defaultCtx context.Context

	// Per-operation fields.  Non-nil only during an active operation on a
	// clone created by withStatement(); nil on the "root" client.
	Statement *Statement
	Error     error
}

// Input is the constructor configuration for New().
type Input struct {
	Endpoint    string
	PosNo       string
	MerchantTin string
	IsDev       bool

	// Optional integrations.
	DB           *gorm.DB // When set, receipts are persisted automatically.
	MailHost     string
	MailPort     string
	MailFrom     string
	MailSubject  string
	MailPassword string
	MailUser     string
	TemplatePath string

	// Plugins to register during construction (equivalent to GORM's
	// Config.Plugins map).  Use() can also be called after New().
	Plugins []Plugin
}

// New constructs an EbarimtClient, initialises the callback pipeline with the
// built-in default callbacks, and registers any plugins supplied via
// Input.Plugins.
func New(input Input) (*EbarimtClient, error) {
	posv3 := pos3.New(pos3.ConnectionInput{
		PosEndpoint: input.Endpoint,
		PosNo:       input.PosNo,
		MerchantTin: input.MerchantTin,
		IsDev:       input.IsDev,
	})

	if input.DB != nil {
		ebarimt3SdkServices.Register(input.DB)
	}

	e := &EbarimtClient{
		Pos3:         posv3,
		DB:           input.DB,
		MailHost:     input.MailHost,
		MailPort:     input.MailPort,
		MailFrom:     input.MailFrom,
		MailSubject:  input.MailSubject,
		MailPassword: input.MailPassword,
		MailUser:     input.MailUser,
		TemplatePath: input.TemplatePath,
		plugins:      make(map[string]Plugin),
	}

	e.pluginCallbacks = initCallbacks(e)
	registerDefaultCallbacks(e)

	for _, p := range input.Plugins {
		if err := e.Use(p); err != nil {
			return nil, err
		}
	}

	return e, nil
}

// ─── Plugin API ───────────────────────────────────────────────────────────────

// Use registers a plugin.  Initialize is called once; on success the plugin is
// stored under its name.  Duplicate names return ErrPluginRegistered.
func (e *EbarimtClient) Use(p Plugin) error {
	name := p.Name()
	if _, ok := e.plugins[name]; ok {
		return ErrPluginRegistered
	}
	if err := p.Initialize(e); err != nil {
		return err
	}
	e.plugins[name] = p
	return nil
}

// Callback returns the callback registry so that plugins (and callers) can
// register, remove, or replace hooks on any operation processor.
func (e *EbarimtClient) Callback() *callbacks {
	return e.pluginCallbacks
}

// AddError records the first non-nil error on the client context.  Subsequent
// callbacks check e.Error != nil and skip their work when it is set.
func (e *EbarimtClient) AddError(err error) {
	if err != nil && e.Error == nil {
		e.Error = err
	}
}

// WithContext returns a shallow clone of the client that will use ctx as the
// base context for every subsequent operation.  This mirrors GORM's
// db.WithContext(ctx) pattern and is the idiomatic way to attach a
// request-scoped context (e.g. an OpenTelemetry root span) to all SDK calls:
//
//	ctx, span := tracer.Start(req.Context(), "checkout")
//	defer span.End()
//	result, err := client.WithContext(ctx).Create(input)
func (e *EbarimtClient) WithContext(ctx context.Context) *EbarimtClient {
	clone := *e
	clone.defaultCtx = ctx
	return &clone
}

// withStatement creates a shallow clone of e suitable for a single operation.
// The clone shares all configuration (Pos3, DB, mail, plugins, callbacks) but
// gets its own fresh Statement (seeded with defaultCtx) and a nil Error so
// that concurrent operations never interfere with each other.
func (e *EbarimtClient) withStatement() *EbarimtClient {
	ctx := e.defaultCtx
	if ctx == nil {
		ctx = context.Background()
	}
	clone := *e
	clone.Statement = &Statement{Context: ctx}
	clone.Error = nil
	return &clone
}

// ─── Public API ───────────────────────────────────────────────────────────────

// Create builds receipt(s) from the given model and sends them to the POS
// endpoint.  Internally it runs the "create" callback chain so plugins can
// hook before and after each step.
func (e *EbarimtClient) Create(input models.CreateInputModel) (*structs.ReceiptResponse, error) {
	ctx := e.withStatement()
	ctx.Statement.Operation = "create"
	ctx.Statement.CreateInput = &input
	e.pluginCallbacks.Create().Execute(ctx)
	return ctx.Statement.ReceiptResponse, ctx.Error
}

// RunReceiptSend executes the full receiptSend → httpPosRequest callback chain
// for the given request.  Callers outside of an active Create() operation
// should use this method so that plugins intercept the call.
//
// Within the create callback chain, sendReceiptCallback calls the processors
// directly on the shared Statement to preserve context propagation.
func (e *EbarimtClient) RunReceiptSend(req structs.ReceiptRequest) (*structs.ReceiptResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "receipt_send"
	op.Statement.ReceiptRequest = &req
	e.pluginCallbacks.ReceiptSend().Execute(op)
	return op.Statement.ReceiptResponse, op.Error
}

// RunReceiptDelete executes the receiptDelete → httpPosRequest callback chain.
func (e *EbarimtClient) RunReceiptDelete(req structs.ReceiptDeleteRequest) (*structs.Response, error) {
	op := e.withStatement()
	op.Statement.Operation = "receipt_delete"
	op.Statement.DeleteRequest = &req
	e.pluginCallbacks.ReceiptDelete().Execute(op)
	return op.Statement.DeleteResponse, op.Error
}

// RunSendData executes the sendData → httpPosRequest callback chain.
func (e *EbarimtClient) RunSendData() (*structs.Response, error) {
	op := e.withStatement()
	op.Statement.Operation = "send_data"
	e.pluginCallbacks.SendData().Execute(op)
	return op.Statement.Response, op.Error
}

// RunInfo executes the info → httpPosRequest callback chain.
func (e *EbarimtClient) RunInfo() (*structs.InfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "info"
	e.pluginCallbacks.Info().Execute(op)
	return op.Statement.InfoResponse, op.Error
}

// RunAuth executes the auth callback chain, which fetches (or returns the
// cached) bearer token.  Useful when a plugin needs to trace the
// authentication call as a distinct span.
func (e *EbarimtClient) RunAuth() error {
	op := e.withStatement()
	op.Statement.Operation = "auth"
	e.pluginCallbacks.Auth().Execute(op)
	return op.Error
}

// RunHTTPPosRequest executes the low-level httpPosRequest callback chain and
// unmarshals the raw response bytes into dest.  Useful for plugins or advanced
// callers that want to send arbitrary POS API requests with full tracing.
func (e *EbarimtClient) RunHTTPPosRequest(dest interface{}, body interface{}, api utils.API, ext string, headers []pos3.CustomHeader) error {
	op := e.withStatement()
	op.Statement.Operation = "http_pos_request"
	op.Statement.HTTP = &HTTPStatement{
		ReqBody: marshalBody(body),
		APIDesc: api,
		Ext:     ext,
	}
	e.pluginCallbacks.HTTPPosRequest().Execute(op)
	if op.Error != nil {
		return op.Error
	}
	if dest != nil && len(op.Statement.HTTP.ResBody) > 0 {
		return json.Unmarshal(op.Statement.HTTP.ResBody, dest)
	}
	return nil
}

// RunHTTPRequest executes the low-level httpRequest callback chain and
// unmarshals the raw response bytes into dest.
func (e *EbarimtClient) RunHTTPRequest(dest interface{}, body interface{}, api utils.API, ext string, headers []pos3.CustomHeader) error {
	op := e.withStatement()
	op.Statement.Operation = "http_request"
	op.Statement.HTTP = &HTTPStatement{
		ReqBody: marshalBody(body),
		APIDesc: api,
		Ext:     ext,
	}
	e.pluginCallbacks.HTTPRequest().Execute(op)
	if op.Error != nil {
		return op.Error
	}
	if dest != nil && len(op.Statement.HTTP.ResBody) > 0 {
		return json.Unmarshal(op.Statement.HTTP.ResBody, dest)
	}
	return nil
}

// marshalBody is a best-effort JSON serialisation helper used when storing the
// request body in HTTPStatement before the hook chain fires.
func marshalBody(body interface{}) []byte {
	if body == nil {
		return nil
	}
	b, _ := json.Marshal(body)
	return b
}

// ─── Run* finishers for every Pos3 interface method ──────────────────────────
//
// Each method creates a fresh Statement, sets Statement.Params, runs the
// named callback processor, and returns the typed result.  Plugins register
// Before/After hooks on the corresponding Callback() processor to intercept,
// trace, or modify the call.
//
// Example — trace every GetInfo call:
//
//	client.Callback().GetInfo().
//	    Before("ebarimt:get_info").
//	    Register("otel:get_info", func(e *EbarimtClient) {
//	        ctx, span := tracer.Start(e.Statement.Context, "ebarimt.getInfo")
//	        e.Statement.Context = ctx
//	        e.Statement.Settings.Store("otel:span", span)
//	    })

// ── POS ───────────────────────────────────────────────────────────────────────

// RunBankAccounts executes the bankAccounts callback chain.
// Statement.Params: string (tin).
func (e *EbarimtClient) RunBankAccounts(tin string) ([]structs.BankAccountData, error) {
	op := e.withStatement()
	op.Statement.Operation = "bank_accounts"
	op.Statement.Params = tin
	e.pluginCallbacks.BankAccounts().Execute(op)
	return op.Statement.BankAccountsRes, op.Error
}

// ── Цахим төлбөрийн баримт ────────────────────────────────────────────────────

// RunGetInfo executes the getInfo callback chain.
// Statement.Params: string (customerTin).
func (e *EbarimtClient) RunGetInfo(customerTin string) (*structs.GetInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_info"
	op.Statement.Params = customerTin
	e.pluginCallbacks.GetInfo().Execute(op)
	return op.Statement.GetInfoRes, op.Error
}

// RunGetTinInfo executes the getTinInfo callback chain.
// Statement.Params: string (regNo).
func (e *EbarimtClient) RunGetTinInfo(regNo string) (*structs.GetTinInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_tin_info"
	op.Statement.Params = regNo
	e.pluginCallbacks.GetTinInfo().Execute(op)
	return op.Statement.GetTinInfoRes, op.Error
}

// RunGetBranchInfo executes the getBranchInfo callback chain.
// Statement.Params: nil.
func (e *EbarimtClient) RunGetBranchInfo() (*structs.GetBranchInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_branch_info"
	e.pluginCallbacks.GetBranchInfo().Execute(op)
	return op.Statement.GetBranchInfoRes, op.Error
}

// RunGetSalesTotalData executes the getSalesTotalData callback chain.
// Statement.Params: structs.GetSalesTotalDataRequest.
func (e *EbarimtClient) RunGetSalesTotalData(body structs.GetSalesTotalDataRequest) (*structs.GetSalesTotalDataResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_sales_total_data"
	op.Statement.Params = body
	e.pluginCallbacks.GetSalesTotalData().Execute(op)
	return op.Statement.GetSalesTotalDataRes, op.Error
}

// RunGetSalesListERP executes the getSalesListERP callback chain.
// Statement.Params: structs.GetSalesListERPRequest.
// Result is in Statement.GetSalesTotalDataRes (shared type).
func (e *EbarimtClient) RunGetSalesListERP(body structs.GetSalesListERPRequest) (*structs.GetSalesTotalDataResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_sales_list_erp"
	op.Statement.Params = body
	e.pluginCallbacks.GetSalesListERP().Execute(op)
	return op.Statement.GetSalesTotalDataRes, op.Error
}

// RunSaveOprMerchants executes the saveOprMerchants callback chain.
// Statement.Params: structs.SaveOprMerchantsRequest.
func (e *EbarimtClient) RunSaveOprMerchants(body structs.SaveOprMerchantsRequest) (*structs.SaveOprMerchantsResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "save_opr_merchants"
	op.Statement.Params = body
	e.pluginCallbacks.SaveOprMerchants().Execute(op)
	return op.Statement.SaveOprMerchantsRes, op.Error
}

// ── Хялбар бүртгэл ────────────────────────────────────────────────────────────

// RunConsumerInfo executes the consumerInfo callback chain.
// Statement.Params: string (regNo).
func (e *EbarimtClient) RunConsumerInfo(regNo string) (*structs.ConsumerInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "consumer_info"
	op.Statement.Params = regNo
	e.pluginCallbacks.ConsumerInfo().Execute(op)
	return op.Statement.ConsumerInfoRes, op.Error
}

// RunGetProfile executes the getProfile callback chain.
// Statement.Params: structs.GetProfileRequest.
func (e *EbarimtClient) RunGetProfile(body structs.GetProfileRequest) (*structs.GetProfileResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "get_profile"
	op.Statement.Params = body
	e.pluginCallbacks.GetProfile().Execute(op)
	return op.Statement.GetProfileRes, op.Error
}

// RunApproveQr executes the approveQr callback chain.
// Statement.Params: structs.ApproveQrRequest.
func (e *EbarimtClient) RunApproveQr(body structs.ApproveQrRequest) (*structs.ApproveQrResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "approve_qr"
	op.Statement.Params = body
	e.pluginCallbacks.ApproveQr().Execute(op)
	return op.Statement.ApproveQrRes, op.Error
}

// RunForiegnerPassportInfo executes the foriegnerPassportInfo callback chain.
// Statement.Params: ForiegnerPassportInfoParams{FNumber, PassportNo}.
func (e *EbarimtClient) RunForiegnerPassportInfo(fNumber, passportNo string) (*structs.ForiegnerInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "foreigner_passport_info"
	op.Statement.Params = ForiegnerPassportInfoParams{FNumber: fNumber, PassportNo: passportNo}
	e.pluginCallbacks.ForiegnerPassportInfo().Execute(op)
	return op.Statement.ForiegnerInfoRes, op.Error
}

// RunForiegnerCustomerNoInfo executes the foriegnerCustomerNoInfo callback chain.
// Statement.Params: string (loginName).
func (e *EbarimtClient) RunForiegnerCustomerNoInfo(loginName string) (*structs.ForiegnerInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "foreigner_customer_no_info"
	op.Statement.Params = loginName
	e.pluginCallbacks.ForiegnerCustomerNoInfo().Execute(op)
	return op.Statement.ForiegnerInfoRes, op.Error
}

// RunForiegnerInfoRegister executes the foriegnerInfoRegister callback chain.
// Statement.Params: ForiegnerInfoRegisterParams{PassportNo, Body}.
func (e *EbarimtClient) RunForiegnerInfoRegister(passportNo string, body structs.ForiegnerInfoRequest) (*structs.ForiegnerInfoResponse, error) {
	op := e.withStatement()
	op.Statement.Operation = "foreigner_info_register"
	op.Statement.Params = ForiegnerInfoRegisterParams{PassportNo: passportNo, Body: body}
	e.pluginCallbacks.ForiegnerInfoRegister().Execute(op)
	return op.Statement.ForiegnerInfoRes, op.Error
}

// ─── Pass-through helpers (unchanged behaviour) ───────────────────────────────

// CalculateTotals computes VAT totals for a set of line items without making
// any network calls.
func (e *EbarimtClient) CalculateTotals(items []models.CreateItemInputModel) (*models.CalculateTotalsOutputModel, error) {
	var output models.CalculateTotalsOutputModel

	for _, item := range items {
		output.TotalVat += func() float64 {
			if item.TaxType == constants.TAX_VAT_ABLE {
				if item.IsCityTax {
					return utils.GetVatWithCityTax(item.TotalAmount)
				}
				return utils.GetVat(item.TotalAmount)
			}
			return 0
		}()

		output.TotalAmount += item.TotalAmount

		output.TotalCityTax += func() float64 {
			if item.TaxType == constants.TAX_NO_VAT {
				return 0
			}
			if item.IsCityTax {
				if item.TaxType == constants.TAX_VAT_ABLE {
					return utils.GetCityTax(item.TotalAmount)
				}
				return utils.GetCityTaxWithoutVat(item.TotalAmount)
			}
			return 0
		}()
	}

	return &output, nil
}

