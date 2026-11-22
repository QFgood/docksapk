package hub

import (
	"html/template"
	"net/http"
	"strings"
)

var demoPage = template.Must(template.New("demo").Parse(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>项目表单</title>
<style>
*{box-sizing:border-box}body{font-family:system-ui,sans-serif;margin:0;background:#eff5f2;color:#123930;padding:24px}
main{max-width:490px;margin:28px auto;background:white;border-radius:22px;padding:26px}
h1{font-size:1.65rem;margin-top:0}p{line-height:1.65}
input,button{font:inherit;min-height:46px;padding:8px 12px;border-radius:10px;width:100%;margin-top:9px}
input{border:1px solid #829e95}button{background:#10493f;border:0;color:white}
output{display:block;background:#dcf3e7;padding:14px;border-radius:10px;margin-top:20px;overflow-wrap:anywhere}
</style><main><h1>测试项目</h1>
<p>输入一段测试文字，表单将由这个项目的 Go 服务接收。刷新此页也能验证项目 Cookie。</p>
<form method="post" action="/demo/"><label>测试内容<input name="message" required maxlength="100"></label><button>提交表单</button></form>
{{if .Message}}<output>项目服务已收到：{{.Message}}</output>{{end}}
<p>{{if .Returning}}项目 Cookie 已保留。{{else}}项目已设置测试 Cookie。{{end}}</p>
</main></html>`))

func (s *Server) demo(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/demo/" {
		http.NotFound(w, r)
		return
	}
	_, err := r.Cookie("docks_demo_session")
	view := struct {
		Message   string
		Returning bool
	}{Returning: err == nil}
	if !view.Returning {
		http.SetCookie(w, &http.Cookie{Name: "docks_demo_session", Value: randomID(12), Path: "/demo/",
			HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 7 * 24 * 3600})
	}
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Invalid form", 400)
			return
		}
		view.Message = strings.TrimSpace(r.PostForm.Get("message"))
		if !cleanField(view.Message, 400) {
			http.Error(w, "Invalid message", 400)
			return
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'")
	_ = demoPage.Execute(w, view)
}
