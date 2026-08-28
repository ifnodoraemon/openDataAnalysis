package handler

import (
	"fmt"
	"io"
	"log"
	"net/http"

	"github.com/ifnodoraemon/openDataAnalysis/internal/jsoncontract"
)

func decodeRequestJSON(r *http.Request, out interface{}) error {
	if r == nil || r.Body == nil {
		return fmt.Errorf("请求体不能为空")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("读取请求体失败：%w", err)
	}
	if err := jsoncontract.Decode(body, out); err != nil {
		return fmt.Errorf("JSON 请求无效：%w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	encoded, err := jsoncontract.Marshal(payload)
	if err != nil {
		log.Printf("response JSON encoding failed: %v", err)
		http.Error(w, "服务器内部错误", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if _, err := w.Write(append(encoded, '\n')); err != nil {
		log.Printf("response JSON write failed: %v", err)
	}
}
