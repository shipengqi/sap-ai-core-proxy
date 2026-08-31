package openai

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"github.com/gin-gonic/gin"
)

const maxAudioSize = 25 * 1024 * 1024 // 25 MB

var allowedAudioFormats = map[string]bool{
	"mp3": true, "mp4": true, "mpeg": true, "mpga": true,
	"m4a": true, "wav": true, "webm": true, "ogg": true,
	"flac": true,
}

var allowedResponseFormats = map[string]bool{
	"json": true, "text": true, "srt": true, "verbose_json": true, "vtt": true,
}

// Whisper proxies POST /openai/v1/audio/transcriptions to SAP AI Core.
// It accepts multipart/form-data, validates the file, and re-encodes the
// request as multipart before forwarding to the deployment's /audio/transcriptions endpoint.
func (h *Handler) Whisper(c *gin.Context) {
	if err := c.Request.ParseMultipartForm(maxAudioSize + 1*1024*1024); err != nil {
		c.JSON(http.StatusBadRequest, errorBody("parse multipart form: "+err.Error()))
		return
	}

	modelStr := c.Request.FormValue("model")
	if modelStr == "" {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: model"))
		return
	}
	c.Set("model", modelStr)

	file, header, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusBadRequest, errorBody("missing required field: file"))
		return
	}
	defer func() { _ = file.Close() }()

	filename := header.Filename
	if idx := strings.LastIndex(filename, "."); idx >= 0 {
		ext := strings.ToLower(filename[idx+1:])
		if !allowedAudioFormats[ext] {
			c.JSON(http.StatusBadRequest, errorBody(fmt.Sprintf("unsupported audio format: %s", ext)))
			return
		}
	}

	if header.Size > maxAudioSize {
		c.JSON(http.StatusBadRequest, errorBody(fmt.Sprintf("file too large: %d bytes (max 25MB)", header.Size)))
		return
	}

	fileBytes, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("read file: "+err.Error()))
		return
	}

	if rf := c.Request.FormValue("response_format"); rf != "" && !allowedResponseFormats[rf] {
		c.JSON(http.StatusBadRequest, errorBody(fmt.Sprintf("unsupported response_format: %s", rf)))
		return
	}

	// Re-encode as multipart for upstream.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	mh := make(textproto.MIMEHeader)
	mh.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, filename))
	ct := header.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}
	mh.Set("Content-Type", ct)
	fw, err := mw.CreatePart(mh)
	if err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("create multipart: "+err.Error()))
		return
	}
	if _, err = fw.Write(fileBytes); err != nil {
		c.JSON(http.StatusInternalServerError, errorBody("write file part: "+err.Error()))
		return
	}

	for key, vals := range c.Request.MultipartForm.Value {
		for _, val := range vals {
			_ = mw.WriteField(key, val)
		}
	}
	_ = mw.Close()

	dep, err := h.deployments.GetDeployment(c.Request.Context(), modelStr)
	if err != nil {
		c.JSON(http.StatusNotFound, errorBody(err.Error()))
		return
	}

	upstreamURL := dep.DeployedURL + "/audio/transcriptions?api-version=2024-02-01"
	slog.Info("calling whisper model", "model", modelStr, "deployment_id", dep.ID)

	resp, err := h.client.Do(c.Request.Context(), http.MethodPost, upstreamURL, &buf,
		map[string]string{"Content-Type": mw.FormDataContentType()})
	if err != nil {
		c.JSON(http.StatusBadGateway, errorBody(err.Error()))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(resp.Body)
	respCT := resp.Header.Get("Content-Type")
	if respCT == "" {
		respCT = "application/json"
	}
	c.Data(resp.StatusCode, respCT, respBody)
}
