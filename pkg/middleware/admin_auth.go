package middleware

import (
	"crypto/subtle"
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
)

// AdminAuthMiddleware 保护管理后台 /admin/* 接口。
// 请求需携带 X-Admin-Token（或 Authorization: Bearer <token>），
// 与环境变量 ADMIN_TOKEN 常量时间比较，防止侧信道泄漏。
func AdminAuthMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		expected := strings.TrimSpace(os.Getenv("ADMIN_TOKEN"))
		if expected == "" {
			return c.Status(fiber.StatusServiceUnavailable).JSON(fiber.Map{
				"error": "管理后台密码未配置：请设置环境变量 ADMIN_TOKEN 后重启。",
			})
		}

		provided := strings.TrimSpace(c.Get("X-Admin-Token"))
		if provided == "" {
			auth := strings.TrimSpace(c.Get("Authorization"))
			if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				provided = strings.TrimSpace(auth[7:])
			}
		}

		if provided == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "管理后台密码错误或缺失。",
			})
		}

		return c.Next()
	}
}
