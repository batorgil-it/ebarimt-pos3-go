package ebarimtv3

import (
	"context"
	"sync"

	"github.com/batorgil-it/ebarimt-pos3-go/constants"
	"github.com/batorgil-it/ebarimt-pos3-go/pos3"
	"github.com/batorgil-it/ebarimt-pos3-go/structs"
	"github.com/batorgil-it/ebarimt-pos3-go/utils"
)

// Statement holds per-operation data that flows through the callback chain.
//
// Each public API call (Create, RunReceiptSend, RunGetInfo, …) shallow-clones
// the EbarimtClient and attaches a fresh Statement so that concurrent
// operations never share mutable state while all shared configuration (plugins,
// callbacks, the underlying POS connection) is reused without copying.
type Statement struct {
	// Context propagated from the caller. Defaults to context.Background().
	// Plugins that start OTEL spans write the enriched context back here so
	// that nested callbacks (e.g. httpPosRequest) pick up the parent span.
	Context context.Context

	// Operation is the human-readable name of the current operation
	// (e.g. "create", "get_info", "receipt_send").  Set by every Run* finisher
	// before the processor chain executes.  Used as the OTEL span name.
	Operation string

	// ── Generic operation input ──────────────────────────────────────────────
	//
	// Params holds the primary argument(s) for non-create operations.
	// The concrete type is documented on each Run* method; use a type switch
	// or type assertion in your plugin hooks.
	//
	// Multi-argument operations use a dedicated param struct:
	//   RunForiegnerPassportInfo → ForiegnerPassportInfoParams
	//   RunForiegnerInfoRegister → ForiegnerInfoRegisterParams
	Params interface{}

	// ── Create operation ─────────────────────────────────────────────────────

	CreateInput  *structs.CreateInputModel
	ReceiptItems map[constants.TaxType]structs.Receipt

	// ── ReceiptSend operation ─────────────────────────────────────────────────

	ReceiptRequest  *structs.ReceiptRequest
	ReceiptResponse *structs.ReceiptResponse

	// ── ReceiptDelete operation ───────────────────────────────────────────────

	DeleteRequest  *structs.ReceiptDeleteRequest
	DeleteResponse *structs.Response

	// ── SendData / Info (POS) operations ─────────────────────────────────────

	Response     *structs.Response
	InfoResponse *structs.InfoResponse

	// ── BankAccounts (POS) operation ─────────────────────────────────────────

	BankAccountsRes []structs.BankAccountData

	// ── Цахим төлбөрийн баримт / Public API responses ────────────────────────

	GetInfoRes           *structs.GetInfoResponse
	GetTinInfoRes        *structs.GetTinInfoResponse
	GetBranchInfoRes     *structs.GetBranchInfoResponse
	GetSalesTotalDataRes *structs.GetSalesTotalDataResponse // also used by GetSalesListERP
	SaveOprMerchantsRes  *structs.SaveOprMerchantsResponse

	// ── Хялбар бүртгэл / Easy Register responses ─────────────────────────────

	ConsumerInfoRes  *structs.ConsumerInfoResponse
	GetProfileRes    *structs.GetProfileResponse
	ApproveQrRes     *structs.ApproveQrResponse
	ForiegnerInfoRes *structs.ForiegnerInfoResponse // shared by all 3 foreigner methods

	// ── HTTP-level state (httpPosRequest / httpRequest / auth) ───────────────
	//
	// Set by the default callbacks before calling into pos3 and populated with
	// the response after the call.  Plugins use these fields in Before/After
	// hooks to instrument individual HTTP calls as child spans:
	//
	//   e.Callback().HTTPRequest().Before("pos3:http_request").
	//       Register("otel:http_start", func(e *EbarimtClient) {
	//           ctx, span := tracer.Start(e.Statement.Context, "http.get",
	//               trace.WithAttributes(
	//                   attribute.String("http.method", e.Statement.HTTP.APIDesc.Method),
	//                   attribute.String("http.url",    e.Statement.HTTP.ResolvedURL),
	//               ))
	//           e.Statement.Context = ctx
	//           e.Statement.Settings.Store("otel:span", span)
	//       })
	HTTP *HTTPStatement

	// ── Plugin-scoped key-value store ────────────────────────────────────────
	//
	//   e.Statement.Settings.Store("myplugin:key", value)
	//   if v, ok := e.Statement.Settings.Load("myplugin:key"); ok { … }
	Settings sync.Map
}

// ── Parameter structs for multi-argument operations ───────────────────────────

// ForiegnerPassportInfoParams is the Params type for RunForiegnerPassportInfo.
type ForiegnerPassportInfoParams struct {
	FNumber    string
	PassportNo string
}

// ForiegnerInfoRegisterParams is the Params type for RunForiegnerInfoRegister.
type ForiegnerInfoRegisterParams struct {
	PassportNo string
	Body       structs.ForiegnerInfoRequest
}

// ── HTTPStatement ─────────────────────────────────────────────────────────────

// HTTPStatement carries per-HTTP-call data for the httpPosRequest, httpRequest,
// and auth callback processors.  It is attached to Statement.HTTP by the
// default callbacks before the processor chain runs, giving Before hooks full
// visibility into the outbound call and After hooks access to the response.
type HTTPStatement struct {
	// APIDesc is the utils.API descriptor (Method, Url, DevUrl, IsAuth).
	APIDesc utils.API

	// Ext is the URL suffix appended to api.Url (e.g. a TIN query string).
	Ext string

	// ResolvedURL is the final URL after applying DevUrl/Ext.
	// Populated by pos3:http_pos_request / pos3:http_request.
	ResolvedURL string

	// Headers holds any custom request headers (e.g. X-API-KEY).
	Headers []pos3.CustomHeader

	// ReqBody is the JSON-serialised request body (nil for GET requests).
	ReqBody []byte

	// StatusCode is the HTTP response status code.
	StatusCode int

	// ResBody is the raw HTTP response body.
	ResBody []byte
}
