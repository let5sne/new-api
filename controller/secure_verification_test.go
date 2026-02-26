package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type secureVerifyResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

func setupSecureVerificationRouterForTest(t *testing.T) *gin.Engine {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	model.DB = db
	model.LOG_DB = db
	common.UsingSQLite = true
	common.RedisEnabled = false

	if err = db.AutoMigrate(&model.User{}, &model.TwoFA{}, &model.PasskeyCredential{}, &model.Log{}); err != nil {
		t.Fatalf("迁移测试表失败: %v", err)
	}

	user := &model.User{
		Id:       1,
		Username: "test_user",
		Password: "12345678",
		Status:   common.UserStatusEnabled,
	}
	if err = db.Create(user).Error; err != nil {
		t.Fatalf("插入测试用户失败: %v", err)
	}

	credential := &model.PasskeyCredential{
		UserID:       1,
		CredentialID: "AQ==",
		PublicKey:    "AQ==",
	}
	if err = db.Create(credential).Error; err != nil {
		t.Fatalf("插入测试 passkey 凭证失败: %v", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(sessions.Sessions("secure-verify-test", cookie.NewStore([]byte("secure-verify-secret"))))

	authMiddleware := func(c *gin.Context) {
		c.Set("id", 1)
		c.Next()
	}

	r.POST("/api/verify", authMiddleware, UniversalVerify)
	r.POST("/api/protected", authMiddleware, middleware.SecureVerificationRequired(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"success": true,
			"message": "ok",
		})
	})
	r.POST("/api/test/passkey/mark", authMiddleware, func(c *gin.Context) {
		session := sessions.Default(c)
		session.Set(PasskeyVerifiedOnceKey, true)
		if saveErr := session.Save(); saveErr != nil {
			common.ApiError(c, saveErr)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"success": true,
		})
	})
	return r
}

func postJSONAndDecode(t *testing.T, r *gin.Engine, path string, body string, cookieValue string) (int, secureVerifyResponse, string) {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if cookieValue != "" {
		req.Header.Set("Cookie", cookieValue)
	}

	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	var resp secureVerifyResponse
	if err := common.Unmarshal([]byte(recorder.Body.String()), &resp); err != nil {
		t.Fatalf("解析响应失败: %v, body=%s", err, recorder.Body.String())
	}

	return recorder.Code, resp, normalizeCookie(recorder.Header().Get("Set-Cookie"))
}

func normalizeCookie(setCookieHeader string) string {
	if setCookieHeader == "" {
		return ""
	}
	parts := strings.SplitN(setCookieHeader, ";", 2)
	return parts[0]
}

func mergeCookie(current string, fromResponse string) string {
	if fromResponse == "" {
		return current
	}
	return fromResponse
}

func TestUniversalVerifyPasskeyRequiresOneTimeMarker(t *testing.T) {
	r := setupSecureVerificationRouterForTest(t)

	statusCode, resp, _ := postJSONAndDecode(t, r, "/api/verify", `{"method":"passkey"}`, "")
	if statusCode != http.StatusOK {
		t.Fatalf("预期返回 200，实际为: %d", statusCode)
	}
	if resp.Success {
		t.Fatalf("预期验证失败，实际 success=true")
	}
	if !strings.Contains(resp.Message, "Passkey 验证未完成") {
		t.Fatalf("预期提示需先完成 Passkey 验证，实际 message=%q", resp.Message)
	}
}

func TestUniversalVerifyPasskeyConsumesOneTimeMarker(t *testing.T) {
	r := setupSecureVerificationRouterForTest(t)

	cookieValue := ""
	_, markResp, setCookie := postJSONAndDecode(t, r, "/api/test/passkey/mark", `{}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if !markResp.Success {
		t.Fatalf("设置 passkey 一次性标记失败: %s", markResp.Message)
	}

	_, verifyResp1, setCookie := postJSONAndDecode(t, r, "/api/verify", `{"method":"passkey"}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if !verifyResp1.Success {
		t.Fatalf("首次 passkey 验证应成功，实际失败: %s", verifyResp1.Message)
	}

	_, verifyResp2, _ := postJSONAndDecode(t, r, "/api/verify", `{"method":"passkey"}`, cookieValue)
	if verifyResp2.Success {
		t.Fatalf("一次性标记应被消费，第二次验证不应成功")
	}
	if !strings.Contains(verifyResp2.Message, "Passkey 验证未完成") {
		t.Fatalf("第二次失败原因不符合预期: %s", verifyResp2.Message)
	}
}

func TestSecureVerificationRequiredBlocksWithoutVerification(t *testing.T) {
	r := setupSecureVerificationRouterForTest(t)

	statusCode, resp, _ := postJSONAndDecode(t, r, "/api/protected", `{}`, "")
	if statusCode != http.StatusForbidden {
		t.Fatalf("未验证访问受限接口应返回 403，实际: %d", statusCode)
	}
	if resp.Success {
		t.Fatalf("未验证访问受限接口不应成功")
	}
	if resp.Message != "需要安全验证" {
		t.Fatalf("预期 message=需要安全验证，实际: %s", resp.Message)
	}
}

func TestSecureVerificationEndToEndPasskeyFlow(t *testing.T) {
	r := setupSecureVerificationRouterForTest(t)

	cookieValue := ""

	// 未完成挑战时调用 /api/verify(method=passkey) 应失败
	_, verifyRespBefore, setCookie := postJSONAndDecode(t, r, "/api/verify", `{"method":"passkey"}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if verifyRespBefore.Success {
		t.Fatalf("未完成 passkey 挑战时不应通过 /api/verify")
	}

	// 失败后访问受限接口仍应被拦截
	statusCode, protectedBefore, setCookie := postJSONAndDecode(t, r, "/api/protected", `{}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if statusCode != http.StatusForbidden {
		t.Fatalf("未验证访问受限接口应返回 403，实际: %d", statusCode)
	}
	if protectedBefore.Success {
		t.Fatalf("未验证访问受限接口不应成功")
	}

	// 模拟 PasskeyVerifyFinish 写入一次性标记
	_, markResp, setCookie := postJSONAndDecode(t, r, "/api/test/passkey/mark", `{}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if !markResp.Success {
		t.Fatalf("设置 passkey 一次性标记失败: %s", markResp.Message)
	}

	// 消费一次性标记完成通用安全验证
	_, verifyRespAfter, setCookie := postJSONAndDecode(t, r, "/api/verify", `{"method":"passkey"}`, cookieValue)
	cookieValue = mergeCookie(cookieValue, setCookie)
	if !verifyRespAfter.Success {
		t.Fatalf("有一次性标记时 /api/verify 应成功，实际失败: %s", verifyRespAfter.Message)
	}

	// 再访问受限接口应放行
	statusCode, protectedAfter, _ := postJSONAndDecode(t, r, "/api/protected", `{}`, cookieValue)
	if statusCode != http.StatusOK {
		t.Fatalf("完成安全验证后访问受限接口应返回 200，实际: %d", statusCode)
	}
	if !protectedAfter.Success {
		t.Fatalf("完成安全验证后访问受限接口应成功，message=%s", protectedAfter.Message)
	}
}
