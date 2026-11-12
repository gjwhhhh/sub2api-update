package admin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/codexeval"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) CodexEvalPage(c *gin.Context) {
	c.Header("X-Frame-Options", "SAMEORIGIN")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; img-src data:; font-src data:; frame-src 'self' blob:; base-uri 'none'; form-action 'none'; frame-ancestors 'self'")
	c.Header("Cache-Control", "no-store")
	c.Data(200, "text/html; charset=utf-8", codexeval.Page())
}
func (h *AccountHandler) CodexEvalState(c *gin.Context) {
	raw, err := h.evaluation.State()
	if err != nil {
		response.Error(c, 500, err.Error())
		return
	}
	response.Success(c, json.RawMessage(raw))
}
func (h *AccountHandler) CodexEvalModels(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	models, err := h.accountTestService.CodexEvalModels(c.Request.Context(), id)
	if err != nil {
		if !response.ErrorFrom(c, err) {
			response.Error(c, http.StatusBadGateway, err.Error())
		}
		return
	}
	response.Success(c, models)
}
func (h *AccountHandler) CodexEvalStart(c *gin.Context) {
	var req codexeval.StartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	out, err := h.evaluation.Start(c.Request.Context(), req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, out)
}
func (h *AccountHandler) CodexEvalCancel(c *gin.Context) {
	var req struct {
		RunID string `json:"run_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.RunID == "" {
		response.BadRequest(c, "run_id is required")
		return
	}
	response.Success(c, map[string]bool{"cancelled": h.evaluation.Cancel(req.RunID)})
}
func (h *AccountHandler) CodexEvalRecord(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("account_id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	raw, status := h.evaluation.Record(c.Param("type"), id, c.Param("record_id"))
	if status != 200 {
		c.Data(status, "application/json", raw)
		return
	}
	response.Success(c, json.RawMessage(raw))
}
func (h *AccountHandler) CodexEvalClear(c *gin.Context) {
	var req struct {
		Scope string `json:"scope"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	raw, status := h.evaluation.Clear(req.Scope)
	if status != 200 {
		c.Data(status, "application/json", raw)
		return
	}
	response.Success(c, json.RawMessage(raw))
}

type codexEvalFlusher struct{ gin.ResponseWriter }

func (w codexEvalFlusher) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	w.Flush()
	return n, err
}
func (h *AccountHandler) CodexEval(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	var req struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err = json.Unmarshal(body, &req); err != nil || req.Model == "" || !req.Stream {
		response.BadRequest(c, "model and stream:true are required")
		return
	}
	resp, err := h.accountTestService.SendCodexEvaluation(c.Request.Context(), id, body, c.Request.Header)
	if err != nil {
		if !response.ErrorFrom(c, err) {
			response.Error(c, http.StatusBadGateway, err.Error())
		}
		return
	}
	defer func() { _ = resp.Body.Close() }()
	stop := context.AfterFunc(c.Request.Context(), func() { _ = resp.Body.Close() })
	defer stop()
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(resp.StatusCode)
	if _, err = io.Copy(codexEvalFlusher{c.Writer}, resp.Body); err != nil && c.Request.Context().Err() == nil {
		raw, _ := json.Marshal(map[string]string{"type": "error", "message": err.Error()})
		_, _ = codexEvalFlusher{c.Writer}.Write(append(append([]byte("event: error\ndata: "), raw...), '\n', '\n'))
	}
}
