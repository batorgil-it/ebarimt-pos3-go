package pos3

import (
	"context"

	"github.com/batorgil-it/ebarimt-pos3-go/structs"
	"github.com/batorgil-it/ebarimt-pos3-go/utils"
)

type pos3 struct {
	posEndpoint string
	apiKey      string
	token       *structs.TokenResponse
	merchanTin  string
	posNo       string
	isDev       bool
	// ctx is the context used for all HTTP requests on this instance.
	// Set via WithContext; defaults to context.Background().
	ctx context.Context
}

type ConnectionInput struct {
	PosEndpoint string
	ApiKey      string
	PosNo       string
	MerchantTin string
	IsDev       bool
}

func New(input ConnectionInput) Pos3 {
	return &pos3{
		apiKey:      input.ApiKey,
		posEndpoint: input.PosEndpoint,
		merchanTin:  input.MerchantTin,
		posNo:       input.PosNo,
		isDev:       input.IsDev,
		ctx:         context.Background(),
	}
}

// Pos3 is the low-level interface for all POS 3.0 and government API calls.
type Pos3 interface {
	// WithContext returns a shallow clone of the client that uses ctx for all
	// subsequent HTTP requests, enabling context cancellation and propagation
	// of OpenTelemetry spans to the transport layer.
	WithContext(ctx context.Context) Pos3

	// ── HTTP primitives ──────────────────────────────────────────────────────
	//
	// These are exposed so that the EbarimtClient callback layer can route
	// low-level HTTP calls through the httpRequest / httpPosRequest / auth
	// callback processors, creating sub-spans beneath the operation span.

	// ExecHTTPRequest performs a public-API HTTP call using the given context.
	ExecHTTPRequest(ctx context.Context, body interface{}, api utils.API, ext string, headers []CustomHeader) ([]byte, error)
	// ExecHTTPPosRequest performs a POS-endpoint HTTP call using the given context.
	ExecHTTPPosRequest(ctx context.Context, body interface{}, api utils.API, ext string, headers []CustomHeader) ([]byte, error)
	// ExecAuth fetches (or returns the cached) bearer token using the given context.
	ExecAuth(ctx context.Context) (structs.TokenResponse, error)

	// ── Inputs ───────────────────────────────────────────────────────────────
	GetMerchantTin() string
	GetPosNo() string
	GetApiKey() string

	// ── Цахим төлбөрийн баримт ───────────────────────────────────────────────
	GetInfo(customerTin string) (structs.GetInfoResponse, error)
	GetTinInfo(regNo string) (structs.GetTinInfoResponse, error)
	GetBranchInfo() (structs.GetBranchInfoResponse, error)
	GetSalesTotalData(body structs.GetSalesTotalDataRequest) (structs.GetSalesTotalDataResponse, error)
	GetSalesListERP(body structs.GetSalesListERPRequest) (structs.GetSalesTotalDataResponse, error)
	SaveOprMerchants(body structs.SaveOprMerchantsRequest) (structs.SaveOprMerchantsResponse, error)

	// ── Хялбар бүртгэл ───────────────────────────────────────────────────────
	ConsumerInfo(regNo string) (structs.ConsumerInfoResponse, error)
	GetProfile(body structs.GetProfileRequest) (structs.GetProfileResponse, error)
	ApproveQr(body structs.ApproveQrRequest) (structs.ApproveQrResponse, error)
	ForiegnerPassportInfo(fNumber, passportNo string) (structs.ForiegnerInfoResponse, error)
	ForiegnerCustomerNoInfo(loginName string) (structs.ForiegnerInfoResponse, error)
	ForiegnerInfoRegister(passportNo string, body structs.ForiegnerInfoRequest) (structs.ForiegnerInfoResponse, error)

	// ── POS ──────────────────────────────────────────────────────────────────
	ReceiptSend(body structs.ReceiptRequest) (structs.ReceiptResponse, error)
	ReceiptDelete(body structs.ReceiptDeleteRequest) (structs.Response, error)
	SendData() (structs.Response, error)
	Info() (structs.InfoResponse, error)
	BankAccounts(tin string) ([]structs.BankAccountData, error)
}

// WithContext returns a shallow clone of the pos3 client bound to ctx.
// All HTTP requests made by the clone will use http.NewRequestWithContext(ctx, …),
// which allows an instrumented http.RoundTripper to attach trace spans.
func (p *pos3) WithContext(ctx context.Context) Pos3 {
	clone := *p
	clone.ctx = ctx
	return &clone
}

// ExecHTTPRequest is the public entry-point for the httpRequest primitive.
func (p *pos3) ExecHTTPRequest(ctx context.Context, body interface{}, api utils.API, ext string, headers []CustomHeader) ([]byte, error) {
	clone := *p
	clone.ctx = ctx
	return clone.httpRequest(body, api, ext, headers)
}

// ExecHTTPPosRequest is the public entry-point for the httpPosRequest primitive.
func (p *pos3) ExecHTTPPosRequest(ctx context.Context, body interface{}, api utils.API, ext string, headers []CustomHeader) ([]byte, error) {
	clone := *p
	clone.ctx = ctx
	return clone.httpPosRequest(body, api, ext, headers)
}

// ExecAuth is the public entry-point for the auth (token fetch) primitive.
func (p *pos3) ExecAuth(ctx context.Context) (structs.TokenResponse, error) {
	clone := *p
	clone.ctx = ctx
	return clone.auth()
}
