package ebarimtv3

import (
	"encoding/json"
	"fmt"

	ebarimt3SdkServices "github.com/batorgil-it/ebarimt-pos3-go/services"
	"github.com/batorgil-it/ebarimt-pos3-go/constants"
	"github.com/batorgil-it/ebarimt-pos3-go/pos3"
	"github.com/batorgil-it/ebarimt-pos3-go/structs"
)

// registerDefaultCallbacks wires the built-in SDK logic into every callback
// processor.  The naming convention is:
//
//	"ebarimt:<step>"  — high-level SDK logic
//	"pos3:<step>"     — low-level HTTP primitive
//
// Plugins anchor their hooks relative to these names using Before/After.
func registerDefaultCallbacks(e *EbarimtClient) {
	// ── create ───────────────────────────────────────────────────────────────
	cb := e.Callback().Create()
	cb.Register("ebarimt:build_request", buildRequestCallback)
	cb.Register("ebarimt:send_receipt", sendReceiptCallback)
	cb.Register("ebarimt:save_db", saveDBCallback)
	cb.Register("ebarimt:send_mail", sendMailCallback)

	// ── POS operations ────────────────────────────────────────────────────────
	e.Callback().ReceiptSend().Register("ebarimt:receipt_send", receiptSendCallback)
	e.Callback().ReceiptDelete().Register("ebarimt:receipt_delete", receiptDeleteCallback)
	e.Callback().SendData().Register("ebarimt:send_data", sendDataCallback)
	e.Callback().Info().Register("ebarimt:info", infoCallback)
	e.Callback().BankAccounts().Register("ebarimt:bank_accounts", bankAccountsCallback)

	// ── Цахим төлбөрийн баримт / Public API operations ───────────────────────
	e.Callback().GetInfo().Register("ebarimt:get_info", getInfoCallback)
	e.Callback().GetTinInfo().Register("ebarimt:get_tin_info", getTinInfoCallback)
	e.Callback().GetBranchInfo().Register("ebarimt:get_branch_info", getBranchInfoCallback)
	e.Callback().GetSalesTotalData().Register("ebarimt:get_sales_total_data", getSalesTotalDataCallback)
	e.Callback().GetSalesListERP().Register("ebarimt:get_sales_list_erp", getSalesListERPCallback)
	e.Callback().SaveOprMerchants().Register("ebarimt:save_opr_merchants", saveOprMerchantsCallback)

	// ── Хялбар бүртгэл / Easy Register operations ────────────────────────────
	e.Callback().ConsumerInfo().Register("ebarimt:consumer_info", consumerInfoCallback)
	e.Callback().GetProfile().Register("ebarimt:get_profile", getProfileCallback)
	e.Callback().ApproveQr().Register("ebarimt:approve_qr", approveQrCallback)
	e.Callback().ForiegnerPassportInfo().Register("ebarimt:foreigner_passport_info", foriegnerPassportInfoCallback)
	e.Callback().ForiegnerCustomerNoInfo().Register("ebarimt:foreigner_customer_no_info", foriegnerCustomerNoInfoCallback)
	e.Callback().ForiegnerInfoRegister().Register("ebarimt:foreigner_info_register", foriegnerInfoRegisterCallback)

	// ── HTTP primitives ───────────────────────────────────────────────────────
	e.Callback().HTTPPosRequest().Register("pos3:http_pos_request", httpPosRequestCallback)
	e.Callback().HTTPRequest().Register("pos3:http_request", httpRequestCallback)
	e.Callback().Auth().Register("pos3:auth", authCallback)
}

// ─── create callbacks ─────────────────────────────────────────────────────────

// buildRequestCallback translates CreateInputModel → ReceiptRequest and groups
// items by tax type.
// Produces: Statement.ReceiptRequest, Statement.ReceiptItems.
func buildRequestCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	input := e.Statement.CreateInput
	req := e.buildRequest(*input)

	items, err := e.buildReceiptItemMap(input.Items, &req)
	if err != nil {
		e.AddError(err)
		return
	}

	e.Statement.ReceiptRequest = &req
	e.Statement.ReceiptItems = items
}

// sendReceiptCallback handles the split-send logic (NO_VAT first, then the
// remaining tax types).
//
// Sub-operations are executed by calling the receiptSend processor directly on
// the SAME e — not via RunReceiptSend() — so that context modifications made
// by a plugin's receiptSend before-hook (e.g. attaching an OTEL child span to
// e.Statement.Context) are visible to the nested httpPosRequest callbacks.
func sendReceiptCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}

	req := *e.Statement.ReceiptRequest
	items := e.Statement.ReceiptItems

	// ── NO_VAT batch (sent first per Ebarimt rules) ───────────────────────
	if len(items) > 0 && len(items[constants.TAX_NO_VAT].Items) > 0 {
		noVatOnly := map[constants.TaxType]structs.Receipt{
			constants.TAX_NO_VAT: items[constants.TAX_NO_VAT],
		}
		noVatReq := req
		e.buildReceipt(&noVatReq, noVatOnly)

		e.Statement.ReceiptRequest = &noVatReq
		e.Statement.ReceiptResponse = nil
		e.pluginCallbacks.ReceiptSend().Execute(e) // same e — shares context
		if e.Error != nil {
			return
		}
		if e.Statement.ReceiptResponse == nil || e.Statement.ReceiptResponse.Status != constants.POS_STATUS_SUCCESS {
			msg := ""
			if e.Statement.ReceiptResponse != nil {
				msg = e.Statement.ReceiptResponse.Message
			}
			e.AddError(fmt.Errorf("ebarimt NO_VAT send error: %v", msg))
			return
		}
		fmt.Println("Ebarimt NO VAT RESPONSE", *e.Statement.ReceiptResponse)
		delete(items, constants.TAX_NO_VAT)
	}

	// ── Main batch ────────────────────────────────────────────────────────
	mainReq := req
	e.buildReceipt(&mainReq, items)

	e.Statement.ReceiptRequest = &mainReq
	e.Statement.ReceiptResponse = nil
	e.pluginCallbacks.ReceiptSend().Execute(e) // same e — shares context
	if e.Error != nil {
		return
	}
	if e.Statement.ReceiptResponse == nil || e.Statement.ReceiptResponse.Status != constants.POS_STATUS_SUCCESS {
		msg := ""
		if e.Statement.ReceiptResponse != nil {
			msg = e.Statement.ReceiptResponse.Message
		}
		e.AddError(fmt.Errorf("ebarimt send error: %v", msg))
		return
	}
	fmt.Println("Ebarimt Other Tax Type RESPONSE", *e.Statement.ReceiptResponse)

	e.Statement.ReceiptResponse.OrgName = mainReq.OrgName
	e.Statement.ReceiptResponse.OrgCode = mainReq.OrgCode
}

// saveDBCallback persists the receipt response when a GORM DB is configured.
func saveDBCallback(e *EbarimtClient) {
	if e.Error != nil || e.DB == nil || e.Statement.ReceiptResponse == nil {
		return
	}
	ebarimt3SdkServices.SaveEbarimt(e.DB, e.Statement.ReceiptResponse)
}

// sendMailCallback emails the receipt when mail transport is configured.
func sendMailCallback(e *EbarimtClient) {
	if e.Error != nil || e.Statement.ReceiptResponse == nil {
		return
	}
	input := e.Statement.CreateInput
	if e.MailHost == "" || e.MailPort == "" || e.MailFrom == "" || e.MailPassword == "" || input.MailTo == "" {
		return
	}
	ebarimt3SdkServices.SendMail(ebarimt3SdkServices.EmailInput{
		Email:        input.MailTo,
		From:         e.MailFrom,
		Subject:      e.MailSubject,
		User:         e.MailUser,
		Password:     e.MailPassword,
		SmtpHost:     e.MailHost,
		SmtpPort:     e.MailPort,
		TemplatePath: e.TemplatePath,
		Response:     *e.Statement.ReceiptResponse,
	})
}

// ─── receiptSend callbacks ────────────────────────────────────────────────────

// receiptSendCallback prepares the HTTPStatement for the POS receipt endpoint
// and then executes the httpPosRequest processor on the SAME e.
//
// Span hierarchy created by an OTEL plugin:
//
//	receiptSend (created in receiptSend before-hook)
//	  └── pos3.httpPosRequest (created in httpPosRequest before-hook)
func receiptSendCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.PosReceiptSendAPI,
		ReqBody: marshalBody(e.Statement.ReceiptRequest),
	}
	// Execute httpPosRequest chain on the same e so context flows through.
	e.pluginCallbacks.HTTPPosRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ReceiptResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ReceiptResponse = &resp
}

// ─── receiptDelete callbacks ──────────────────────────────────────────────────

func receiptDeleteCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.PosReceiptDeleteAPI,
		ReqBody: marshalBody(e.Statement.DeleteRequest),
	}
	e.pluginCallbacks.HTTPPosRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.Response
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.DeleteResponse = &resp
}

// ─── sendData callbacks ───────────────────────────────────────────────────────

func sendDataCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.PosSendAPI,
	}
	e.pluginCallbacks.HTTPPosRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.Response
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.Response = &resp
}

// ─── info callbacks ───────────────────────────────────────────────────────────

func infoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.PosInfoAPI,
	}
	e.pluginCallbacks.HTTPPosRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.InfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.InfoResponse = &resp
}

// ─── httpPosRequest callbacks ─────────────────────────────────────────────────

// httpPosRequestCallback performs the actual POS endpoint HTTP call.
// Before this runs, Statement.HTTP must be populated with APIDesc and ReqBody.
// After this runs, Statement.HTTP.ResBody and Statement.HTTP.StatusCode are set.
//
// An OTEL plugin registers Before/After hooks on this callback to create the
// innermost HTTP span:
//
//	e.Callback().HTTPPosRequest().
//	    Before("pos3:http_pos_request").
//	    Register("otel:pos_http_start", func(e *EbarimtClient) {
//	        ctx, span := tracer.Start(e.Statement.Context, "http.request",
//	            trace.WithAttributes(
//	                attribute.String("http.method", e.Statement.HTTP.APIDesc.Method),
//	                attribute.String("http.url",    e.Statement.HTTP.ResolvedURL),
//	            ))
//	        e.Statement.Context = ctx
//	        e.Statement.Settings.Store("otel:http_span", span)
//	    })
//
//	e.Callback().HTTPPosRequest().
//	    After("pos3:http_pos_request").
//	    Register("otel:pos_http_end", func(e *EbarimtClient) {
//	        if v, ok := e.Statement.Settings.Load("otel:http_span"); ok {
//	            span := v.(trace.Span)
//	            span.SetAttributes(attribute.Int("http.status_code", e.Statement.HTTP.StatusCode))
//	            if e.Error != nil { span.RecordError(e.Error) }
//	            span.End()
//	            e.Statement.Settings.Delete("otel:http_span")
//	        }
//	    })
func httpPosRequestCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	h := e.Statement.HTTP
	raw, err := e.Pos3.ExecHTTPPosRequest(
		e.Statement.Context,
		unmarshalBody(h.ReqBody),
		h.APIDesc,
		h.Ext,
		nil,
	)
	if err != nil {
		e.AddError(err)
		return
	}
	h.ResBody = raw
}

// ─── httpRequest callbacks ────────────────────────────────────────────────────

// httpRequestCallback performs a public/government API HTTP call.
// Mirrors httpPosRequestCallback — see its doc-comment for the OTEL pattern.
// It forwards Statement.HTTP.Headers so callers can include custom headers
// such as X-API-KEY.
func httpRequestCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	h := e.Statement.HTTP
	raw, err := e.Pos3.ExecHTTPRequest(
		e.Statement.Context,
		unmarshalBody(h.ReqBody),
		h.APIDesc,
		h.Ext,
		h.Headers,
	)
	if err != nil {
		e.AddError(err)
		return
	}
	h.ResBody = raw
}

// ─── auth callbacks ───────────────────────────────────────────────────────────

// authCallback fetches (or returns the cached) bearer token.
// It is not wired into the normal HTTP flow automatically; callers invoke
// RunAuth() when they need explicit control over token refresh tracing.
func authCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	_, err := e.Pos3.ExecAuth(e.Statement.Context)
	if err != nil {
		e.AddError(err)
	}
}

// ─── BankAccounts callback ────────────────────────────────────────────────────

func bankAccountsCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	tin, _ := e.Statement.Params.(string)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.PosBankAccAPI,
		Ext:     "tin=" + tin,
	}
	e.pluginCallbacks.HTTPPosRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp []structs.BankAccountData
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.BankAccountsRes = resp
}

// ─── Цахим төлбөрийн баримт callbacks ────────────────────────────────────────

func getInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	tin, _ := e.Statement.Params.(string)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetInfoAPI,
		Ext:     tin,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetInfoRes = &resp
}

func getTinInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	regNo, _ := e.Statement.Params.(string)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetTinInfoAPI,
		Ext:     regNo,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetTinInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetTinInfoRes = &resp
}

func getBranchInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetBranchInfoAPI,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetBranchInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetBranchInfoRes = &resp
}

func getSalesTotalDataCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetSalesTotalAPI,
		ReqBody: marshalBody(e.Statement.Params),
		Headers: apiKeyHeader(e),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetSalesTotalDataResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetSalesTotalDataRes = &resp
}

func getSalesListERPCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetSalesListERPAPI,
		ReqBody: marshalBody(e.Statement.Params),
		Headers: apiKeyHeader(e),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetSalesTotalDataResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetSalesTotalDataRes = &resp
}

func saveOprMerchantsCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.SaveOprMerchantsAPI,
		ReqBody: marshalBody(e.Statement.Params),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.SaveOprMerchantsResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.SaveOprMerchantsRes = &resp
}

// ─── Хялбар бүртгэл callbacks ─────────────────────────────────────────────────

func consumerInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	regNo, _ := e.Statement.Params.(string)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.ConsumerInfoAPI,
		Ext:     regNo,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ConsumerInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ConsumerInfoRes = &resp
}

func getProfileCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.GetProfileAPI,
		ReqBody: marshalBody(e.Statement.Params),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.GetProfileResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.GetProfileRes = &resp
}

func approveQrCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.ApproveQrAPI,
		ReqBody: marshalBody(e.Statement.Params),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ApproveQrResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ApproveQrRes = &resp
}

func foriegnerPassportInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	p, _ := e.Statement.Params.(ForiegnerPassportInfoParams)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.ForiegnerPassportInfoAPI,
		Ext:     p.PassportNo + "/" + p.FNumber,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ForiegnerInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ForiegnerInfoRes = &resp
}

func foriegnerCustomerNoInfoCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	loginName, _ := e.Statement.Params.(string)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.ForiegnerCustomerNoInfoAPI,
		Ext:     loginName,
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ForiegnerInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ForiegnerInfoRes = &resp
}

func foriegnerInfoRegisterCallback(e *EbarimtClient) {
	if e.Error != nil {
		return
	}
	p, _ := e.Statement.Params.(ForiegnerInfoRegisterParams)
	e.Statement.HTTP = &HTTPStatement{
		APIDesc: pos3.ForiegnerInfoRegAPI,
		Ext:     p.PassportNo,
		ReqBody: marshalBody(p.Body),
	}
	e.pluginCallbacks.HTTPRequest().Execute(e)
	if e.Error != nil {
		return
	}
	var resp structs.ForiegnerInfoResponse
	if err := json.Unmarshal(e.Statement.HTTP.ResBody, &resp); err != nil {
		e.AddError(err)
		return
	}
	e.Statement.ForiegnerInfoRes = &resp
}

// apiKeyHeader returns the X-API-KEY header slice for operations that require it.
func apiKeyHeader(e *EbarimtClient) []pos3.CustomHeader {
	key := e.Pos3.GetApiKey()
	if key == "" {
		return nil
	}
	return []pos3.CustomHeader{{Name: "X-API-KEY", Value: key}}
}

// ─── internal helpers ─────────────────────────────────────────────────────────

// unmarshalBody deserialises the stored ReqBody bytes back to interface{} so
// that ExecHTTPPosRequest / ExecHTTPRequest can re-serialise it when building
// the HTTP request.  Returns nil for empty bodies (GET requests).
func unmarshalBody(b []byte) interface{} {
	if len(b) == 0 {
		return nil
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return nil
	}
	return v
}
