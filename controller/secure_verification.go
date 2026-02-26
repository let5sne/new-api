package controller

import (
	"fmt"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	passkeysvc "github.com/QuantumNous/new-api/service/passkey"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
)

const (
	// SecureVerificationSessionKey 安全验证的 session key
	SecureVerificationSessionKey = "secure_verified_at"
	// SecureVerificationTimeout 验证有效期（秒）
	SecureVerificationTimeout = 300 // 5分钟
	// PasskeyVerifiedOnceKey 一次性标志：PasskeyVerifyForSecure 完成后设置，UniversalVerify 消费后清除
	PasskeyVerifiedOnceKey = "passkey_verified_once"
)

type UniversalVerifyRequest struct {
	Method string `json:"method"` // "2fa" 或 "passkey"
	Code   string `json:"code,omitempty"`
}

type VerificationStatusResponse struct {
	Verified  bool  `json:"verified"`
	ExpiresAt int64 `json:"expires_at,omitempty"`
}

// UniversalVerify 通用验证接口
// 支持 2FA 和 Passkey 验证，验证成功后在 session 中记录时间戳
func UniversalVerify(c *gin.Context) {
	userId := c.GetInt("id")
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "未登录",
		})
		return
	}

	var req UniversalVerifyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiError(c, fmt.Errorf("参数错误: %v", err))
		return
	}

	// 获取用户信息
	user := &model.User{Id: userId}
	if err := user.FillUserById(); err != nil {
		common.ApiError(c, fmt.Errorf("获取用户信息失败: %v", err))
		return
	}

	if user.Status != common.UserStatusEnabled {
		common.ApiError(c, fmt.Errorf("该用户已被禁用"))
		return
	}

	// 检查用户的验证方式
	twoFA, _ := model.GetTwoFAByUserId(userId)
	has2FA := twoFA != nil && twoFA.IsEnabled

	passkey, passkeyErr := model.GetPasskeyByUserID(userId)
	hasPasskey := passkeyErr == nil && passkey != nil

	if !has2FA && !hasPasskey {
		common.ApiError(c, fmt.Errorf("用户未启用2FA或Passkey"))
		return
	}

	// 根据验证方式进行验证
	var verified bool
	var verifyMethod string

	switch req.Method {
	case "2fa":
		if !has2FA {
			common.ApiError(c, fmt.Errorf("用户未启用2FA"))
			return
		}
		if req.Code == "" {
			common.ApiError(c, fmt.Errorf("验证码不能为空"))
			return
		}
		verified = validateTwoFactorAuth(twoFA, req.Code)
		verifyMethod = "2FA"

	case "passkey":
		if !hasPasskey {
			common.ApiError(c, fmt.Errorf("用户未启用Passkey"))
			return
		}
		// 🔒 安全修复：检查 PasskeyVerifyForSecure 是否已完成（一次性标志）
		// 前端必须先调用 /api/user/passkey/verify/begin + /api/user/passkey/verify/finish，
		// 后者会在 session 中写入 PasskeyVerifiedOnceKey，本接口消费后清除。
		session := sessions.Default(c)
		passkeyOnce := session.Get(PasskeyVerifiedOnceKey)
		if passkeyOnce == nil {
			common.ApiError(c, fmt.Errorf("Passkey 验证未完成，请先完成 Passkey 验证流程"))
			return
		}
		// 消费一次性标志，防止重放
		session.Delete(PasskeyVerifiedOnceKey)
		if err := session.Save(); err != nil {
			common.ApiError(c, fmt.Errorf("保存验证状态失败: %v", err))
			return
		}
		verified = true
		verifyMethod = "Passkey"

	default:
		common.ApiError(c, fmt.Errorf("不支持的验证方式: %s", req.Method))
		return
	}

	if !verified {
		common.ApiError(c, fmt.Errorf("验证失败，请检查验证码"))
		return
	}

	// 验证成功，在 session 中记录时间戳
	session := sessions.Default(c)
	now := time.Now().Unix()
	session.Set(SecureVerificationSessionKey, now)
	if err := session.Save(); err != nil {
		common.ApiError(c, fmt.Errorf("保存验证状态失败: %v", err))
		return
	}

	// 记录日志
	model.RecordLog(userId, model.LogTypeSystem, fmt.Sprintf("通用安全验证成功 (验证方式: %s)", verifyMethod))

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "验证成功",
		"data": gin.H{
			"verified":   true,
			"expires_at": now + SecureVerificationTimeout,
		},
	})
}

// PasskeyVerifyAndSetSession Passkey 验证完成后设置 session
// 这是一个辅助函数，供 PasskeyVerifyFinish 调用
func PasskeyVerifyAndSetSession(c *gin.Context) {
	session := sessions.Default(c)
	now := time.Now().Unix()
	session.Set(SecureVerificationSessionKey, now)
	// 🔒 写入一次性标志，供 UniversalVerify passkey 分支消费
	session.Set(PasskeyVerifiedOnceKey, now)
	_ = session.Save()
}

// PasskeyVerifyForSecure 用于安全验证的 Passkey 验证流程
// 整合了 begin 和 finish 流程
func PasskeyVerifyForSecure(c *gin.Context) {
	if !system_setting.GetPasskeySettings().Enabled {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "管理员未启用 Passkey 登录",
		})
		return
	}

	userId := c.GetInt("id")
	if userId == 0 {
		c.JSON(http.StatusUnauthorized, gin.H{
			"success": false,
			"message": "未登录",
		})
		return
	}

	user := &model.User{Id: userId}
	if err := user.FillUserById(); err != nil {
		common.ApiError(c, fmt.Errorf("获取用户信息失败: %v", err))
		return
	}

	if user.Status != common.UserStatusEnabled {
		common.ApiError(c, fmt.Errorf("该用户已被禁用"))
		return
	}

	credential, err := model.GetPasskeyByUserID(userId)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": "该用户尚未绑定 Passkey",
		})
		return
	}

	wa, err := passkeysvc.BuildWebAuthn(c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	waUser := passkeysvc.NewWebAuthnUser(user, credential)
	sessionData, err := passkeysvc.PopSessionData(c, passkeysvc.VerifySessionKey)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	_, err = wa.FinishLogin(waUser, *sessionData, c.Request)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	// 更新凭证的最后使用时间
	now := time.Now()
	credential.LastUsedAt = &now
	if err := model.UpsertPasskeyCredential(credential); err != nil {
		common.ApiError(c, err)
		return
	}

	// 验证成功，设置 session
	PasskeyVerifyAndSetSession(c)

	// 记录日志
	model.RecordLog(userId, model.LogTypeSystem, "Passkey 安全验证成功")

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Passkey 验证成功",
		"data": gin.H{
			"verified":   true,
			"expires_at": time.Now().Unix() + SecureVerificationTimeout,
		},
	})
}
